package runtime

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

func decodeArray(data []byte, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
		return fmt.Errorf("expected a JSON array")
	}
	return json.Unmarshal(data, target)
}

type podmanRow struct {
	ID    string `json:"Id"`
	Names []string
}

func (p podmanRow) hasIdentity() bool {
	if p.ID == "" {
		return false
	}
	return len(p.Names) != 0
}

func (p podmanRow) matches(name string) bool {
	for _, n := range p.Names {
		if strings.TrimPrefix(n, "/") == name {
			return true
		}
	}
	return false
}

type podmanInspect struct {
	ID              string `json:"Id"`
	Name, ImageName string
	State           struct{ Running *bool }
	Config          struct{ Labels map[string]string }
	Mounts          []struct {
		Type, Source, Destination string
		RW                        bool
	}
	HostConfig struct {
		PortBindings map[string][]struct{ HostIP, HostPort string }
	}
}

func (p podmanInspect) matches(id, name string) bool {
	if p.ID != id {
		return false
	}
	if strings.TrimPrefix(p.Name, "/") != name {
		return false
	}
	return p.State.Running != nil
}

func (p podmanInspect) resource() *resource {
	c := &resource{id: p.ID, name: strings.TrimPrefix(p.Name, "/"), image: p.ImageName, running: *p.State.Running, labels: p.Config.Labels}
	if len(p.Mounts) == 1 {
		m := p.Mounts[0]
		c.home = m.Source
		c.validMount = writableHomeMount(m.Type, m.Destination, m.RW)
	}
	bindings := p.HostConfig.PortBindings
	if len(bindings) != 1 {
		return c
	}
	ports := bindings["9000/tcp"]
	if len(ports) != 1 {
		return c
	}
	port, err := strconv.Atoi(ports[0].HostPort)
	c.port = port
	c.validPort = err == nil && isLoopback(ports[0].HostIP)
	return c
}

func writableHomeMount(kind, destination string, writable bool) bool {
	if kind != "bind" {
		return false
	}
	if destination != "/home/shelley" {
		return false
	}
	return writable
}

func isLoopback(address string) bool { return address == "127.0.0.1" }

type appleInspect struct {
	Configuration struct {
		ID     string
		Image  struct{ Reference string }
		Labels map[string]string
		Mounts []struct {
			Type                map[string]json.RawMessage
			Source, Destination string
			Options             []string
		}
		PublishedPorts []struct {
			HostAddress             string
			HostPort, ContainerPort int
			Proto                   string
			Count                   *int
		}
	}
	Status json.RawMessage
}

func (p appleInspect) resource(state string) *resource {
	cfg := p.Configuration
	c := &resource{id: cfg.ID, name: cfg.ID, image: cfg.Image.Reference, running: state == "running", labels: cfg.Labels}
	if len(cfg.Mounts) == 1 {
		m := cfg.Mounts[0]
		_, virtiofs := m.Type["virtiofs"]
		c.home = m.Source
		if virtiofs {
			c.validMount = writableAppleHome(m.Destination, m.Options)
		}
	}
	if len(cfg.PublishedPorts) == 1 {
		p := cfg.PublishedPorts[0]
		c.port = p.HostPort
		c.validPort = validApplePort(p.HostAddress, p.ContainerPort, p.Proto, p.Count)
	}
	return c
}

func (p appleInspect) state() (string, error) {
	// Apple 0.x uses a string. Newer releases wrap the state in an object.
	var state string
	if err := json.Unmarshal(p.Status, &state); err != nil {
		var status struct{ State string }
		if err := json.Unmarshal(p.Status, &status); err != nil {
			return "", fmt.Errorf("decode container status: %w", err)
		}
		state = status.State
	}
	switch state {
	case "running", "stopped", "created":
		return state, nil
	default:
		return "", fmt.Errorf("container returned unknown state %q for %q", state, p.Configuration.ID)
	}
}

func writableAppleHome(destination string, options []string) bool {
	if destination != "/home/shelley" {
		return false
	}
	return !slices.Contains(options, "ro")
}

func validApplePort(address string, port int, protocol string, count *int) bool {
	if !isLoopback(address) {
		return false
	}
	if port != 9000 {
		return false
	}
	if protocol != "tcp" {
		return false
	}
	if count == nil {
		return true
	}
	return *count == 1
}
