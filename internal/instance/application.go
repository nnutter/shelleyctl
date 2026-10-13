// Package instance reconciles durable desired state with native host resources.
package instance

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/nnutter/shelleyctl/image"
	cr "github.com/nnutter/shelleyctl/internal/runtime"
	"github.com/nnutter/shelleyctl/internal/startup"
	"github.com/nnutter/shelleyctl/internal/tailscale"
)

// Options defines host-local configuration. Image applies only to new records.
type Options struct{ StateDir, Image, Executable string }

// Defaults derives configuration without creating resources.
func Defaults() (Options, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Options{}, err
	}
	dir := os.Getenv("SHELLEYCTL_STATE_DIR")
	if dir == "" {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		if runtime.GOOS == "darwin" {
			base = filepath.Join(home, "Library", "Application Support")
		}
		dir = filepath.Join(base, "shelleyctl")
	}
	executable, err := os.Executable()
	if err != nil {
		return Options{}, err
	}
	return Options{StateDir: dir, Image: "localhost/shelleyctl:local", Executable: executable}, nil
}

type containerRuntime interface {
	Build(context.Context, string, string) error
	Check(context.Context, cr.Spec) error
	Inspect(context.Context, string) (cr.Status, error)
	Ensure(context.Context, cr.Spec) error
	Start(context.Context, string) error
	Remove(context.Context, string) error
}
type exposure interface {
	Ensure(context.Context, string, int) error
	Remove(context.Context, string, int) error
	Inspect(context.Context, string, int) (tailscale.Status, error)
}
type launcher interface {
	Ensure(context.Context, string, string, string) error
	Remove(context.Context, string) error
	Installed(string) (bool, error)
}

// Application coordinates native adapters. Options remain mutable until command
// execution so Cobra can bind flags without initializing the runtime for help.
type Application struct {
	opts       *Options
	containers containerRuntime
	serve      exposure
	startup    launcher
}

// New constructs adapters without issuing commands or changing host resources.
func New(opts *Options) (*Application, error) {
	containers, err := cr.New(runtime.GOOS)
	if err != nil {
		return nil, err
	}
	launches, err := startup.New(runtime.GOOS)
	if err != nil {
		return nil, err
	}
	return &Application{opts: opts, containers: containers, serve: tailscale.New(), startup: launches}, nil
}

// Build builds the embedded context; it does not replace running containers.
func (a *Application) Build(ctx context.Context) (result error) {
	dir, err := image.Materialize()
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, os.RemoveAll(dir)) }()
	return a.containers.Build(ctx, a.opts.Image, dir)
}

// Create records intent before side effects. Failures retain state for retry.
// Scheduled resumes cannot revive retained records marked for deletion.
func (a *Application) Create(ctx context.Context, name string) (result error) {
	if !namePattern.MatchString(name) {
		return errors.New("name must start with a lowercase letter, end with a letter or digit, and contain up to 40 lowercase letters, digits, or hyphens")
	}
	if err := a.prepare(); err != nil {
		return err
	}
	lock, err := a.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	s, err := a.load()
	if err != nil {
		return err
	}
	resume := os.Getenv("SHELLEYCTL_RESUME") == "1"
	if skipResume(s, name, resume) {
		return nil
	}
	r, err := a.recordIntent(s, name)
	if err != nil {
		return err
	}
	return a.activate(ctx, r, resume)
}

// Delete first records a tombstone, then removes exposure and execution resources.
// All independent cleanup actions run even if withdrawing exposure fails.
func (a *Application) Delete(ctx context.Context, name string) (result error) {
	if !namePattern.MatchString(name) {
		return errors.New("invalid instance name")
	}
	if err := a.prepare(); err != nil {
		return err
	}
	lock, err := a.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	s, err := a.load()
	if err != nil {
		return err
	}
	r, exists := s.Instances[name]
	if !exists {
		return fmt.Errorf("unknown instance %q; no resources changed", name)
	}
	r.Desired = false
	s.Instances[name] = r
	if err := a.save(s); err != nil {
		return err
	}
	spec := cr.Spec{Name: name, Image: r.Image, Port: r.Port, Home: filepath.Join(a.opts.StateDir, "instances", name, "home")}
	if err := a.containers.Check(ctx, spec); err != nil {
		return err
	}
	exposureErr := a.serve.Remove(ctx, name, r.Port)
	startupErr := a.startup.Remove(ctx, name)
	containerErr := a.containers.Remove(ctx, name)
	return errors.Join(exposureErr, startupErr, containerErr)
}

// Status reports observed state, including partial operations and retained data.
func (a *Application) Status(ctx context.Context, w io.Writer) (result error) {
	if err := a.prepare(); err != nil {
		return err
	}
	lock, err := a.lock(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	s, err := a.load()
	if err != nil {
		return err
	}
	if len(s.Instances) == 0 {
		_, err := fmt.Fprintln(w, "No instances.")
		return err
	}
	names := slices.Sorted(maps.Keys(s.Instances))
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "NAME\tDESIRED\tCONTAINER\tSTARTUP\tBACKEND\tSERVICE\tURL"); err != nil {
		return err
	}
	var failures []error
	for _, name := range names {
		row, inspectErr := a.statusRow(ctx, s.Instances[name])
		failures = append(failures, inspectErr)
		if _, err := fmt.Fprintln(table, row); err != nil {
			return err
		}
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "* Local service configuration does not verify approval or client access.\nHome directories: "+filepath.Join(a.opts.StateDir, "instances", "<name>", "home")); err != nil {
		return err
	}
	return errors.Join(failures...)
}

func (a *Application) activate(ctx context.Context, r record, resume bool) error {
	name := r.Name
	home := filepath.Join(a.opts.StateDir, "instances", name, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	spec := cr.Spec{Name: name, Image: r.Image, Home: home, Port: r.Port}
	if err := a.containers.Ensure(ctx, spec); err != nil {
		return fmt.Errorf("ensure container (build the image first): %w", err)
	}
	if err := a.containers.Start(ctx, name); err != nil {
		return err
	}
	if err := ready(ctx, r.Port); err != nil {
		return fmt.Errorf("backend not ready; inspect container logs and instance configuration, then repeat create: %w", err)
	}
	// Preserve an installed launcher during scheduled execution, but repair
	// missing durable files so future scheduled invocations remain possible.
	if err := a.ensureLauncher(ctx, name, resume); err != nil {
		return err
	}
	if err := a.serve.Ensure(ctx, name, r.Port); err != nil {
		return fmt.Errorf("configure svc:shelley-%s: %w; define the service on TCP 443 and approve this host manually, then repeat create", name, err)
	}
	return nil
}

func (a *Application) ensureLauncher(ctx context.Context, name string, resume bool) error {
	if resume {
		installed, err := a.startup.Installed(name)
		if err != nil {
			return err
		}
		if installed {
			return nil
		}
	}
	return a.startup.Ensure(ctx, name, a.opts.Executable, a.opts.StateDir)
}

func (a *Application) load() (state, error) {
	path := filepath.Join(a.opts.StateDir, "state.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return state{Version: 1, Instances: map[string]record{}}, nil
	}
	if err != nil {
		return state{}, err
	}
	if !info.Mode().IsRegular() {
		return state{}, fmt.Errorf("state must be a regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}, err
	}
	var s state
	if err := json.Unmarshal(data, &s, json.RejectUnknownMembers(true)); err != nil {
		return state{}, fmt.Errorf("read instance state: %w", err)
	}
	if err := validateState(s); err != nil {
		return state{}, err
	}
	return s, nil
}

func (a *Application) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(a.opts.StateDir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(a.opts.StateDir, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, f.Close())
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.Join(err, f.Close())
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), f.Close())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (a *Application) prepare() error {
	if !filepath.IsAbs(a.opts.StateDir) {
		return errors.New("state directory must be absolute")
	}
	a.opts.StateDir = filepath.Clean(a.opts.StateDir)
	return nil
}

func (a *Application) recordIntent(s state, name string) (record, error) {
	r, exists := s.Instances[name]
	if !exists {
		port, err := allocatePort(s)
		if err != nil {
			return record{}, err
		}
		r = record{Name: name, Image: a.opts.Image, Port: port}
	}
	r.Desired = true
	s.Instances[name] = r
	if err := a.save(s); err != nil {
		return record{}, err
	}
	return r, nil
}

func (a *Application) save(s state) (result error) {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(a.opts.StateDir, "state-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(f.Name()); !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(a.opts.StateDir, "state.json")); err != nil {
		return err
	}
	dir, err := os.Open(a.opts.StateDir)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func (a *Application) statusRow(ctx context.Context, r record) (string, error) {
	c, containerErr := a.containers.Inspect(ctx, r.Name)
	launched, startupErr := a.startup.Installed(r.Name)
	service, serveErr := a.serve.Inspect(ctx, r.Name, r.Port)
	container := containerLabel(c, containerErr)
	launch := observedLabel(launched, startupErr, "installed", "missing")
	exposed := observedLabel(service.Configured, serveErr, "configured*", "absent")
	desired := "active"
	if !r.Desired {
		desired = "deleted (data retained)"
	}
	row := fmt.Sprintf("%s\t%s\t%s\t%s\t127.0.0.1:%d\t%s\t%s", r.Name, desired, container, launch, r.Port, exposed, service.URL)
	return row, errors.Join(containerErr, startupErr, serveErr)
}

func containerLabel(c cr.Status, err error) string {
	if err != nil {
		return "error"
	}
	if c.Running {
		return "running"
	}
	if c.Exists {
		return "stopped"
	}
	return "absent"
}

func observedLabel(present bool, err error, presentLabel, absentLabel string) string {
	if err != nil {
		return "error"
	}
	if present {
		return presentLabel
	}
	return absentLabel
}

func ready(ctx context.Context, port int) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			closeErr := response.Body.Close()
			if closeErr != nil {
				return closeErr
			}
			if successfulResponse(response.StatusCode) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func skipResume(s state, name string, resume bool) bool {
	if !resume {
		return false
	}
	r, exists := s.Instances[name]
	if !exists {
		return true
	}
	return !r.Desired
}

func successfulResponse(status int) bool {
	if status < 200 {
		return false
	}
	return status < 400
}

// Retained records keep the image reference used at creation. Deliberate image
// upgrades are separate from create/delete recovery; rebuilding a tag alone
// does not replace an existing container.
