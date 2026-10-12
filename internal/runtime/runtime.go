// Package runtime manages native Podman and Apple container resources.
package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	managedLabel = "io.shelleyctl.managed"
	imageLabel   = "io.shelleyctl.image"
	homeLabel    = "io.shelleyctl.home"
	portLabel    = "io.shelleyctl.port"
)

// Spec describes the desired container. Name can be an instance name or a
// complete shelleyctl-prefixed container name. Home must be an absolute path.
type Spec struct {
	Name, Image, Home string
	Port              int
}

// Status reports a resource found by a successful runtime query.
type Status struct {
	Exists, Running bool
	ID              string
}

// New selects the native executable for linux or darwin. Executables are
// resolved through PATH when each command runs.
func New(platform string) (*Adapter, error) {
	switch platform {
	case "linux":
		return &Adapter{binary: "podman"}, nil
	case "darwin":
		return &Adapter{binary: "container"}, nil
	default:
		return nil, fmt.Errorf("unsupported container platform %q: use linux or darwin", platform)
	}
}

// Adapter invokes the native CLI without a shell.
type Adapter struct{ binary string }

// Binary returns the native command name.
func (a *Adapter) Binary() string { return a.binary }

// Build builds an image from a Dockerfile (or Containerfile) in contextDir.
// SHELLEYCTL_SIGNATURE_POLICY optionally selects an administrator-provided
// Podman signature policy. No policy is generated or relaxed by this package.
func (a *Adapter) Build(ctx context.Context, image, contextDir string) error {
	if err := validImage(image); err != nil {
		return err
	}
	dir, err := filepath.Abs(contextDir)
	if err != nil {
		return fmt.Errorf("resolve build context: %w", err)
	}
	file := filepath.Join(dir, "Dockerfile")
	if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
		file = filepath.Join(dir, "Containerfile")
	}
	args := []string{"build", "--tag", image, "--file", file}
	if a.isPodman() {
		if policy := os.Getenv("SHELLEYCTL_SIGNATURE_POLICY"); policy != "" {
			policy, err = filepath.Abs(policy)
			if err != nil {
				return fmt.Errorf("resolve signature policy: %w", err)
			}
			args = append(args, "--signature-policy", policy)
		}
	}
	_, err = a.command(ctx, append(args, dir)...)
	if err != nil && a.isPodman() {
		return fmt.Errorf("%w; if Podman cannot find policy.json, install the distribution containers policy or set SHELLEYCTL_SIGNATURE_POLICY to an existing trusted policy file", err)
	}
	return err
}

// Ensure creates a stopped container, or validates an existing container.
// Call Start separately to run it. Existing resources are never replaced.
func (a *Adapter) Ensure(ctx context.Context, spec Spec) error {
	name, err := containerName(spec.Name)
	if err != nil {
		return err
	}
	spec.Name = name
	if err := validateSpec(spec); err != nil {
		return err
	}
	c, err := a.lookup(ctx, name)
	if err != nil {
		return err
	}
	if c != nil {
		return c.validate(spec)
	}
	args := []string{"create", "--name", name}
	if a.isPodman() {
		// Map the host operator to the image user so bind-mounted files remain writable.
		args = append(args, "--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000")
	}
	for _, label := range []string{managedLabel + "=true", imageLabel + "=" + spec.Image, homeLabel + "=" + spec.Home, portLabel + "=" + strconv.Itoa(spec.Port)} {
		args = append(args, "--label", label)
	}
	args = append(args, "--volume", spec.Home+":/home/shelley", "--publish", fmt.Sprintf("127.0.0.1:%d:9000", spec.Port), spec.Image)
	_, err = a.command(ctx, args...)
	if err != nil && a.creationCompleted(ctx, spec) {
		return nil
	}
	return err
}

// Inspect distinguishes absence from runtime failures. It does not assert
// ownership, so callers can display foreign/conflicting resources.
func (a *Adapter) Inspect(ctx context.Context, name string) (Status, error) {
	c, err := a.lookup(ctx, name)
	if err != nil {
		return Status{}, err
	}
	if c == nil {
		return Status{}, nil
	}
	return Status{Exists: true, Running: c.running, ID: c.id}, nil
}

// Remove deletes a validated managed container, including a running one.
// Missing containers are unchanged. Podman mutations use the inspected ID.
func (a *Adapter) Remove(ctx context.Context, name string) error {
	c, err := a.lookup(ctx, name)
	if err != nil {
		return err
	}
	if c == nil {
		return nil
	}
	if err := c.validateSaved(); err != nil {
		return err
	}
	operation := "delete"
	if a.isPodman() {
		operation = "rm"
	}
	_, err = a.command(ctx, operation, "--force", c.id)
	if err != nil && a.removalCompleted(ctx, name) {
		return nil
	}
	return err
}

// Start starts a validated managed container. Running containers are unchanged.
func (a *Adapter) Start(ctx context.Context, name string) error {
	c, err := a.lookup(ctx, name)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("container %q does not exist: create it first", name)
	}
	if err := c.validateSaved(); err != nil {
		return err
	}
	if c.running {
		return nil
	}
	_, err = a.command(ctx, "start", c.id)
	if err != nil && a.startCompleted(ctx, name, c.id) {
		return nil
	}
	return err
}

func (a *Adapter) command(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, a.binary, args...)
	// Bound waits when a CLI child retains stdout/stderr after cancellation.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: %w; stderr: %s", a.binary, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (a *Adapter) creationCompleted(ctx context.Context, spec Spec) bool {
	// Another client may create the container between list and create. Check
	// the actual configuration, never an error message, before reusing it.
	c, err := a.lookup(ctx, spec.Name)
	if err != nil {
		return false
	}
	if c == nil {
		return false
	}
	return c.validate(spec) == nil
}

func (a *Adapter) inspectPodman(ctx context.Context, id, name string) (*resource, error) {
	if strings.HasPrefix(id, "-") {
		return nil, fmt.Errorf("podman returned invalid ID for %q", name)
	}
	output, err := a.command(ctx, "inspect", "--type", "container", id)
	if err != nil {
		return nil, err
	}
	var inspected []podmanInspect
	if err := decodeArray(output, &inspected); err != nil {
		return nil, fmt.Errorf("decode podman inspect: %w", err)
	}
	if len(inspected) != 1 || !inspected[0].matches(id, name) {
		return nil, fmt.Errorf("podman inspect returned inconsistent identity or state for %q", name)
	}
	return inspected[0].resource(), nil
}

func (a *Adapter) isPodman() bool { return a.binary == "podman" }

// lookup lists first: an inspect command failure never means absence. Podman
// list lacks full mount data, so inspect the immutable ID after finding a name.
func (a *Adapter) lookup(ctx context.Context, name string) (*resource, error) {
	name, err := containerName(name)
	if err != nil {
		return nil, err
	}
	if a.binary == "container" {
		return a.lookupApple(ctx, name)
	}
	output, err := a.command(ctx, "ps", "--all", "--no-trunc", "--format", "json")
	if err != nil {
		return nil, err
	}
	var rows []podmanRow
	if err := decodeArray(output, &rows); err != nil {
		return nil, fmt.Errorf("decode podman list: %w", err)
	}
	for _, row := range rows {
		if !row.hasIdentity() {
			return nil, fmt.Errorf("podman list returned a resource without identity")
		}
		if row.matches(name) {
			return a.inspectPodman(ctx, row.ID, name)
		}
	}
	return nil, nil
}

func (a *Adapter) lookupApple(ctx context.Context, name string) (*resource, error) {
	output, err := a.command(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	var rows []appleInspect
	if err := decodeArray(output, &rows); err != nil {
		return nil, fmt.Errorf("decode container list: %w", err)
	}
	for _, row := range rows {
		if row.Configuration.ID == "" {
			return nil, fmt.Errorf("container list returned a resource without identity")
		}
		if row.Configuration.ID != name {
			continue
		}
		state, err := row.state()
		if err != nil {
			return nil, err
		}
		return row.resource(state), nil
	}
	return nil, nil
}

func (a *Adapter) removalCompleted(ctx context.Context, name string) bool {
	latest, err := a.lookup(ctx, name)
	if err != nil {
		return false
	}
	return latest == nil
}

func (a *Adapter) startCompleted(ctx context.Context, name, id string) bool {
	latest, err := a.lookup(ctx, name)
	if err != nil {
		return false
	}
	if latest == nil {
		return false
	}
	if latest.id != id {
		return false
	}
	if !latest.running {
		return false
	}
	return latest.validateSaved() == nil
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func containerName(name string) (string, error) {
	if !strings.HasPrefix(name, "shelleyctl-") {
		name = "shelleyctl-" + name
	}
	if !validContainerName(name) {
		return "", fmt.Errorf("invalid container name %q: use letters, digits, dots, underscores, or hyphens (maximum 63 characters including shelleyctl-)", name)
	}
	return name, nil
}

// Podman adds localhost to locally built short tags. Both CLIs may add
// the default latest tag. Explicit registry names remain distinct.
func imageMatches(actual, expected string) bool {
	if actual == expected {
		return true
	}
	ref := expected
	_, last, found := strings.CutLast(ref, "/")
	if !found {
		last = ref
	}
	if !strings.ContainsAny(last, ":@") {
		ref += ":latest"
	}
	if actual == ref {
		return true
	}
	if strings.Contains(expected, "/") {
		return false
	}
	return actual == "localhost/"+ref
}

func validContainerName(name string) bool {
	if name == "shelleyctl-" {
		return false
	}
	if len(name) > 63 {
		return false
	}
	return namePattern.MatchString(name)
}

func validImage(image string) error {
	if image == "" || strings.HasPrefix(image, "-") || strings.ContainsAny(image, " \t\n\r\x00") {
		return fmt.Errorf("invalid image reference %q", image)
	}
	return nil
}

func validateSpec(s Spec) error {
	if err := validImage(s.Image); err != nil {
		return err
	}
	if !validHome(s.Home) {
		return fmt.Errorf("invalid home path %q: use a clean absolute path without colons", s.Home)
	}
	if s.Port < 1 {
		return fmt.Errorf("invalid host port %d: use 1 through 65535", s.Port)
	}
	if s.Port > 65535 {
		return fmt.Errorf("invalid host port %d: use 1 through 65535", s.Port)
	}
	return nil
}

func validHome(home string) bool {
	if !filepath.IsAbs(home) {
		return false
	}
	if filepath.Clean(home) != home {
		return false
	}
	return !strings.ContainsAny(home, ":\n\r\x00")
}

type resource struct {
	id, name, image, home string
	running               bool
	labels                map[string]string
	port                  int
	validMount, validPort bool
}

func (c *resource) matchesHome(s Spec) bool {
	if !c.validMount {
		return false
	}
	if c.home != s.Home {
		return false
	}
	return c.labels[homeLabel] == s.Home
}

func (c *resource) matchesImage(s Spec) bool {
	return imageMatches(c.image, s.Image) && labelMatches(c.labels, imageLabel, s.Image)
}

func (c *resource) matchesPort(s Spec) bool {
	if !c.validPort {
		return false
	}
	if c.port != s.Port {
		return false
	}
	return c.labels[portLabel] == strconv.Itoa(s.Port)
}

func (c *resource) validate(s Spec) error {
	if c.labels[managedLabel] != "true" {
		return fmt.Errorf("refuse foreign container %q: label %s=true is required", c.name, managedLabel)
	}
	if !c.matchesImage(s) {
		return fmt.Errorf("refuse conflicting container %q: expected image %q, found %q", c.name, s.Image, c.image)
	}
	if !c.matchesHome(s) {
		return fmt.Errorf("refuse conflicting container %q: expected writable home mount %q at /home/shelley", c.name, s.Home)
	}
	if !c.matchesPort(s) {
		return fmt.Errorf("refuse conflicting container %q: expected only 127.0.0.1:%d:9000/tcp", c.name, s.Port)
	}
	return nil
}

func (c *resource) validateSaved() error {
	port, err := strconv.Atoi(c.labels[portLabel])
	s := Spec{Name: c.name, Image: c.labels[imageLabel], Home: c.labels[homeLabel], Port: port}
	if err != nil {
		return fmt.Errorf("refuse container %q: missing or invalid shelleyctl configuration labels", c.name)
	}
	if validateSpec(s) != nil {
		return fmt.Errorf("refuse container %q: missing or invalid shelleyctl configuration labels", c.name)
	}
	return c.validate(s)
}

func labelMatches(labels map[string]string, key, value string) bool { return labels[key] == value }
