// Package tailscale manages only the Service Serve endpoint for a Shelley instance.
package tailscale

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// Status describes local configuration, not service approval or reachability.
type Status struct {
	Configured bool
	URL        string
	Detail     string
}

// New returns a host-side Tailscale adapter. It does not execute commands.
func New() *Adapter { return &Adapter{} }

// Adapter serializes its own changes. Other administrators must not edit the
// same service concurrently: the CLI does not provide a read/modify transaction.
type Adapter struct{ mu sync.Mutex }

// Ensure configures HTTPS on port 443 at /, without changing node-level Serve.
// Service definition, host approval, and access policy remain administrator tasks.
func (a *Adapter) Ensure(ctx context.Context, name string, port int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := inspect(ctx, name, port)
	if err != nil {
		return err
	}
	if s.Configured {
		return nil
	}
	_, err = command(ctx, "serve", "--service=svc:shelley-"+name, "--https=443", "--bg", "--yes", backend(port))
	if err != nil {
		return fmt.Errorf("%w. Define svc:shelley-%s manually in Tailscale. Approve this host for that service and check the service access policy", err, name)
	}
	return nil
}

// Inspect checks host eligibility and the exact owned service shape.
func (a *Adapter) Inspect(ctx context.Context, name string, port int) (Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return inspect(ctx, name, port)
}

// Remove clears only a service whose complete configuration matches this instance.
// Missing configuration succeeds, but daemon failures never count as missing.
func (a *Adapter) Remove(ctx context.Context, name string, port int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, err := inspect(ctx, name, port)
	if err != nil {
		return err
	}
	if !s.Configured {
		return nil
	}
	// Current CLI source: cmd/tailscale/cli/serve_v2.go runServeClear removes
	// this service from AdvertiseServices before saving its scoped deletion.
	// An extra drain command is therefore unnecessary for immediate removal.
	_, err = command(ctx, "serve", "clear", "svc:shelley-"+name)
	return err
}

func inspect(ctx context.Context, name string, port int) (Status, error) {
	if !validInstanceName(name) || !validPort(port) {
		return Status{}, errors.New("invalid instance name or port")
	}
	suffix, err := hostSuffix(ctx)
	if err != nil {
		return Status{}, err
	}
	fqdn := "shelley-" + name + "." + suffix
	data, err := command(ctx, "serve", "status", "--json")
	if err != nil {
		return Status{}, err
	}
	return inspectService(data, name, fqdn, port)
}

func hostSuffix(ctx context.Context) (string, error) {
	data, err := command(ctx, "status", "--json")
	if err != nil {
		return "", err
	}
	var host struct {
		BackendState string `json:"BackendState"`
		Self         *struct {
			Tags []string `json:"Tags"`
		} `json:"Self"`
		CurrentTailnet *struct {
			MagicDNSSuffix string `json:"MagicDNSSuffix"`
		} `json:"CurrentTailnet"`
	}
	if json.Unmarshal(data, &host) != nil {
		return "", errors.New("invalid Tailscale host status JSON")
	}
	if host.BackendState != "Running" {
		return "", errors.New("tailscale host is not running; connect this host first")
	}
	if host.Self == nil {
		return "", errors.New("tailscale host is not running; connect this host first")
	}
	if len(host.Self.Tags) == 0 {
		return "", errors.New("tailscale Services require a tagged host; assign tag:shelley-host or another permitted tag")
	}
	if host.CurrentTailnet == nil {
		return "", errors.New("tailscale host has no valid MagicDNS suffix")
	}
	if !validDNSName(host.CurrentTailnet.MagicDNSSuffix) {
		return "", errors.New("tailscale host has no valid MagicDNS suffix")
	}
	return host.CurrentTailnet.MagicDNSSuffix, nil
}

func inspectService(data []byte, name, fqdn string, port int) (Status, error) {
	var config struct {
		Services map[string]jsontext.Value `json:"Services"`
	}
	if json.Unmarshal(data, &config) != nil {
		return Status{}, errors.New("invalid Tailscale Serve status JSON")
	}
	s := Status{URL: "https://" + fqdn, Detail: "Not configured. Define the service and approve this host in Tailscale manually."}
	raw, exists := config.Services["svc:shelley-"+name]
	if !exists {
		return s, nil
	}
	var service *serviceConfig
	if json.Unmarshal(raw, &service, json.RejectUnknownMembers(true)) != nil || !matches(service, fqdn, port) {
		return Status{}, errors.New("conflicting Tailscale service configuration; no configuration was changed")
	}
	s.Configured = true
	s.Detail = "Configured locally. Service definition, host approval, and access are not verified. Manage them in Tailscale manually."
	return s, nil
}

// These types mirror ipn.ServiceConfig. Unknown members are rejected so a
// newer endpoint feature cannot be silently discarded by a scoped clear.
type serviceConfig struct {
	TCP map[string]*tcpHandler `json:"TCP"`
	Web map[string]*webConfig  `json:"Web"`
	Tun bool                   `json:"Tun"`
}

type tcpHandler struct {
	HTTPS         bool   `json:"HTTPS"`
	HTTP          bool   `json:"HTTP"`
	TCPForward    string `json:"TCPForward"`
	TerminateTLS  string `json:"TerminateTLS"`
	ProxyProtocol int    `json:"ProxyProtocol"`
}

type webConfig struct {
	Handlers map[string]*httpHandler `json:"Handlers"`
}

type httpHandler struct {
	Proxy         string   `json:"Proxy"`
	Path          string   `json:"Path"`
	Text          string   `json:"Text"`
	Redirect      string   `json:"Redirect"`
	AcceptAppCaps []string `json:"AcceptAppCaps"`
}

func matches(s *serviceConfig, fqdn string, port int) bool {
	if s == nil {
		return false
	}
	if s.Tun {
		return false
	}
	if len(s.TCP) != 1 {
		return false
	}
	if len(s.Web) != 1 {
		return false
	}
	return matchesTCP(s.TCP["443"]) && matchesWeb(s.Web[fqdn+":443"], port)
}

func matchesTCP(tcp *tcpHandler) bool {
	if tcp == nil {
		return false
	}
	if !tcp.HTTPS {
		return false
	}
	if tcp.HTTP {
		return false
	}
	if tcp.TCPForward != "" {
		return false
	}
	if tcp.TerminateTLS != "" {
		return false
	}
	return tcp.ProxyProtocol == 0
}

func matchesWeb(web *webConfig, port int) bool {
	if web == nil {
		return false
	}
	if len(web.Handlers) != 1 {
		return false
	}
	h := web.Handlers["/"]
	if h == nil {
		return false
	}
	if h.Proxy != backend(port) {
		return false
	}
	if h.Path != "" {
		return false
	}
	if h.Text != "" {
		return false
	}
	if h.Redirect != "" {
		return false
	}
	return len(h.AcceptAppCaps) == 0
}

func validInstanceName(name string) bool {
	if len("shelley-"+name) > 63 {
		return false
	}
	return validLabel(name)
}

func validPort(port int) bool {
	if port < 1 {
		return false
	}
	return port <= 65535
}

func backend(port int) string { return "http://127.0.0.1:" + strconv.Itoa(port) }

func validLabel(s string) bool {
	if len(s) == 0 {
		return false
	}
	if len(s) > 63 {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for _, c := range s {
		if !validLabelRune(c) {
			return false
		}
	}
	return true
}

func validLabelRune(c rune) bool {
	if c == '-' {
		return true
	}
	return runeBetween(c, 'a', 'z') || runeBetween(c, '0', '9')
}

func runeBetween(c, low, high rune) bool {
	if c < low {
		return false
	}
	return c <= high
}

func validDNSName(s string) bool {
	if len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func command(ctx context.Context, operation string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("tailscale")
	if err != nil {
		if runtime.GOOS == "darwin" {
			path, err = exec.LookPath("/Applications/Tailscale.app/Contents/MacOS/Tailscale")
		}
	}
	if err != nil {
		return nil, errors.New("tailscale executable not found; install Tailscale and add it to PATH")
	}
	// Discard stderr and never propagate raw CLI errors or output: they can
	// contain authentication URLs, identity details, or backend secrets.
	out, err := exec.CommandContext(ctx, path, append([]string{operation}, args...)...).Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("tailscale %s command failed (exit %d). Check the daemon with tailscale status. Check Service Serve support with tailscale serve --help", operation, exit.ExitCode())
		}
		return nil, fmt.Errorf("tailscale %s command failed to start. Check executable permissions and the daemon with tailscale status", operation)
	}
	return out, nil
}
