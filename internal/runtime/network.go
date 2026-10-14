package runtime

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

type appleAttachment struct{ Network string }

func onlyAppleNetwork(attachments []appleAttachment, name string) bool {
	if len(attachments) != 1 {
		return false
	}
	return attachments[0].Network == name
}

type network struct {
	Name   string
	Labels map[string]string
}

func (n network) validate(name string) error {
	if n.Name != name {
		return fmt.Errorf("refuse inconsistent network identity for %q", name)
	}
	if n.Labels[managedLabel] != "true" {
		return fmt.Errorf("refuse foreign network %q: label %s=true is required", name, managedLabel)
	}
	if n.Labels[instanceLabel] != name {
		return fmt.Errorf("refuse conflicting network %q: instance label must be %q", name, name)
	}
	return nil
}

// Apple 0.x encodes NetworkState as a Swift associated-value enum. Apple
// 1.x encodes NetworkResource with configuration and status instead.
type appleNetwork struct {
	Configuration struct {
		Name, ID string
		Labels   map[string]string
	}
	Created *appleNetworkState
	Running *appleNetworkState
}

type appleNetworkState struct {
	Configuration struct {
		ID     string
		Labels map[string]string
	} `json:"_0"`
}

func decodeAppleNetwork(data jsontext.Value) (network, error) {
	var p appleNetwork
	if err := json.Unmarshal(data, &p, json.MatchCaseInsensitiveNames(true)); err != nil {
		return network{}, err
	}
	if p.Created != nil {
		if p.Running != nil {
			return network{}, fmt.Errorf("ambiguous Apple network state")
		}
		return network{Name: p.Created.Configuration.ID, Labels: p.Created.Configuration.Labels}, nil
	}
	if p.Running != nil {
		return network{Name: p.Running.Configuration.ID, Labels: p.Running.Configuration.Labels}, nil
	}
	name := p.Configuration.Name
	if name == "" {
		name = p.Configuration.ID
	}
	return network{Name: name, Labels: p.Configuration.Labels}, nil
}

func networkList(ctx context.Context, a *Adapter) ([]network, error) {
	operation := "list"
	if a.isPodman() {
		operation = "ls"
	}
	output, err := a.command(ctx, "network", operation, "--format", "json")
	if err != nil {
		return nil, err
	}
	return decodeNetworks(a, output)
}

func decodeNetworks(a *Adapter, output []byte) ([]network, error) {
	var raw []jsontext.Value
	if err := decodeArray(output, &raw); err != nil {
		return nil, fmt.Errorf("decode network JSON: %w", err)
	}
	var result []network
	for _, data := range raw {
		var n network
		var err error
		if a.isPodman() {
			err = json.Unmarshal(data, &n, json.MatchCaseInsensitiveNames(true))
		} else {
			n, err = decodeAppleNetwork(data)
		}
		if err != nil {
			return nil, fmt.Errorf("decode network: %w", err)
		}
		if n.Name == "" {
			return nil, fmt.Errorf("network query returned a resource without identity")
		}
		result = append(result, n)
	}
	return result, nil
}

// A successful list establishes absence. Inspect failures are never absence.
func lookupNetwork(ctx context.Context, a *Adapter, name string) (*network, error) {
	rows, err := networkList(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		output, err := a.command(ctx, "network", "inspect", name)
		if err != nil {
			return nil, err
		}
		inspected, err := decodeNetworks(a, output)
		if err != nil {
			return nil, err
		}
		if len(inspected) != 1 {
			return nil, fmt.Errorf("network inspect returned %d resources for %q", len(inspected), name)
		}
		if inspected[0].Name != name {
			return nil, fmt.Errorf("network inspect returned inconsistent identity for %q", name)
		}
		return &inspected[0], nil
	}
	return nil, nil
}

func validateNetwork(ctx context.Context, a *Adapter, name string) error {
	n, err := lookupNetwork(ctx, a, name)
	if err != nil {
		return err
	}
	if n == nil {
		return fmt.Errorf("network %q does not exist", name)
	}
	return n.validate(name)
}

func ensureNetwork(ctx context.Context, a *Adapter, name string) error {
	n, err := lookupNetwork(ctx, a, name)
	if err != nil {
		return err
	}
	if n != nil {
		return n.validate(name)
	}
	_, err = a.command(ctx, "network", "create", "--label", managedLabel+"=true", "--label", instanceLabel+"="+name, name)
	verified := validateNetwork(ctx, a, name)
	if verified == nil {
		return nil
	}
	if err != nil {
		return err
	}
	return verified
}

func removeNetwork(ctx context.Context, a *Adapter, name string) error {
	n, err := lookupNetwork(ctx, a, name)
	if err != nil {
		return err
	}
	if n == nil {
		return nil
	}
	if err := n.validate(name); err != nil {
		return err
	}
	if err := networkEmpty(ctx, a, name); err != nil {
		return err
	}
	operation := "delete"
	if a.isPodman() {
		operation = "rm"
	}
	// Never force deletion: the runtime also guards concurrent attachments.
	_, err = a.command(ctx, "network", operation, name)
	if err != nil {
		latest, lookupErr := lookupNetwork(ctx, a, name)
		if lookupErr != nil {
			return err
		}
		if latest != nil {
			return err
		}
	}
	return nil
}

func networkEmpty(ctx context.Context, a *Adapter, name string) error {
	if a.isPodman() {
		output, err := a.command(ctx, "ps", "--all", "--filter", "network="+name, "--format", "json")
		if err != nil {
			return err
		}
		var rows []podmanRow
		if err := decodeArray(output, &rows); err != nil {
			return fmt.Errorf("decode network members: %w", err)
		}
		if len(rows) != 0 {
			return fmt.Errorf("refuse nonempty network %q", name)
		}
		return nil
	}
	output, err := a.command(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return err
	}
	var rows []appleInspect
	if err := decodeArray(output, &rows); err != nil {
		return fmt.Errorf("decode network members: %w", err)
	}
	return appleNetworkEmpty(rows, name)
}

func appleNetworkEmpty(rows []appleInspect, name string) error {
	for _, row := range rows {
		if row.Configuration.ID == "" {
			return fmt.Errorf("container list returned a resource without identity")
		}
		for _, attachment := range append(row.Configuration.Networks, row.attachments()...) {
			if attachment.Network == name {
				return fmt.Errorf("refuse nonempty network %q", name)
			}
		}
	}
	return nil
}
