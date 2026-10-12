// Package startup installs user-session launchers that resume desired instances.
// The CLI and runtime adapter own containers. Launchers never create containers
// directly, and SHELLEYCTL_RESUME prevents revival of deleted records.
package startup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const marker = "shelleyctl generated launcher v1"

var instanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// New selects a user-session launcher without modifying the host.
func New(platform string) (*Adapter, error) {
	if !supportedPlatform(platform) {
		return nil, fmt.Errorf("startup unsupported on %q", platform)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if platform == "linux" {
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		if !filepath.IsAbs(config) {
			return nil, errors.New("XDG_CONFIG_HOME must be absolute")
		}
		dir = filepath.Join(config, "systemd", "user")
	}
	return &Adapter{platform: platform, dir: dir}, nil
}

// Adapter manages generated files and the associated user-session scheduler.
type Adapter struct{ platform, dir string }

// Ensure installs or repairs a launcher. Files remain after command failures so
// a later call can complete the installation.
func (a *Adapter) Ensure(ctx context.Context, name, executable, stateDir string) (result error) {
	if err := validateName(name); err != nil {
		return err
	}
	lock, err := lockStartup(ctx, a.dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	executable, stateDir, err = launcherPaths(executable, stateDir)
	if err != nil {
		return err
	}
	files := render(a, name, executable, stateDir)
	// Check every destination before changing any file.
	for _, f := range files {
		if _, err := owned(f.path); err != nil {
			return err
		}
	}
	if a.platform == "darwin" {
		return ensureLaunchd(ctx, name, files[0])
	}
	if err := publish(files); err != nil {
		return err
	}
	if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return run(ctx, "systemctl", "--user", "enable", "--now", unit(name)+".timer")
}

// Installed reports whether all durable launcher files exist and are owned.
// It does not claim that the current user-session manager is available.
func (a *Adapter) Installed(name string) (bool, error) {
	if err := validateName(name); err != nil {
		return false, err
	}
	complete := true
	for _, f := range render(a, name, "", "") {
		exists, err := owned(f.path)
		if err != nil {
			return false, err
		}
		if !exists {
			complete = false
		}
	}
	return complete, nil
}

// Remove disables future invocations before deleting owned files. A failure
// leaves the files available for retry. It never deletes runtime containers.
func (a *Adapter) Remove(ctx context.Context, name string) (result error) {
	if err := validateName(name); err != nil {
		return err
	}
	lock, err := lockStartup(ctx, a.dir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, lock.Close()) }()
	present, err := presentLaunchers(render(a, name, "", ""))
	if err != nil {
		return err
	}
	if len(present) == 0 {
		return reload(ctx, a.platform)
	}
	if err := disableLaunchers(ctx, a.platform, name, present); err != nil {
		return err
	}
	if err := deleteLaunchers(present); err != nil {
		return err
	}
	return reload(ctx, a.platform)
}

func supportedPlatform(platform string) bool {
	switch platform {
	case "linux", "darwin":
		return true
	default:
		return false
	}
}

func launcherPaths(executable, stateDir string) (string, string, error) {
	executable, err := exec.LookPath(executable)
	if err != nil {
		return "", "", fmt.Errorf("find startup executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", "", err
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return "", "", err
	}
	if strings.ContainsAny(executable+stateDir+os.Getenv("PATH"), "\x00\r\n") {
		return "", "", errors.New("launcher values must not contain NUL or newlines")
	}
	return executable, stateDir, nil
}

func publish(files []launcher) error {
	for _, f := range files {
		if err := persist(f.path, f.data); err != nil {
			return err
		}
	}
	return nil
}

// prepareLaunchd publishes a first installation for retry without a GUI session.
// Existing definitions remain untouched until the old job is unloaded.
func prepareLaunchd(f launcher) (bool, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return false, persist(f.path, f.data)
	}
	if err != nil {
		return false, err
	}
	return string(data) == f.data, nil
}

func ensureLaunchd(ctx context.Context, name string, f launcher) error {
	unchanged, err := prepareLaunchd(f)
	if err != nil {
		return err
	}
	domain := launchDomain()
	if err := run(ctx, "launchctl", "print", domain); err != nil {
		return fmt.Errorf("GUI login session required (retry after login): %w", err)
	}
	target := domain + "/" + label(name)
	if err := run(ctx, "launchctl", "enable", target); err != nil {
		return err
	}
	loaded, err := launchLoaded(ctx, target)
	if err != nil {
		return err
	}
	if loaded {
		if unchanged {
			return nil
		}
		if err := run(ctx, "launchctl", "bootout", target); err != nil {
			return err
		}
	}
	// Keep the old definition until bootout succeeds. Otherwise a retry could
	// mistake the new durable file for the definition still loaded by launchd.
	if err := persist(f.path, f.data); err != nil {
		return err
	}
	return run(ctx, "launchctl", "bootstrap", domain, f.path)
}

func presentLaunchers(files []launcher) ([]launcher, error) {
	present := make([]launcher, 0, len(files))
	for _, f := range files {
		exists, err := owned(f.path)
		if err != nil {
			return nil, err
		}
		if exists {
			present = append(present, f)
		}
	}
	return present, nil
}

func disableLaunchers(ctx context.Context, platform, name string, present []launcher) error {
	if platform == "linux" {
		return disableSystemd(ctx, name, present)
	}
	domain := launchDomain()
	if err := run(ctx, "launchctl", "print", domain); err != nil {
		return fmt.Errorf("GUI login session required for removal: %w", err)
	}
	target := domain + "/" + label(name)
	if err := run(ctx, "launchctl", "disable", target); err != nil {
		return err
	}
	loaded, err := launchLoaded(ctx, target)
	if err != nil {
		return err
	}
	if loaded {
		return run(ctx, "launchctl", "bootout", target)
	}
	return nil
}

func disableSystemd(ctx context.Context, name string, present []launcher) error {
	if err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	for _, f := range present {
		if strings.HasSuffix(f.path, ".timer") {
			if err := run(ctx, "systemctl", "--user", "disable", "--now", unit(name)+".timer"); err != nil {
				return err
			}
		}
	}
	for _, f := range present {
		if strings.HasSuffix(f.path, ".service") {
			if err := run(ctx, "systemctl", "--user", "stop", unit(name)+".service"); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteLaunchers(files []launcher) error {
	for _, f := range files {
		if _, err := owned(f.path); err != nil {
			return err
		}
		if err := os.Remove(f.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func reload(ctx context.Context, platform string) error {
	if platform == "linux" {
		return run(ctx, "systemctl", "--user", "daemon-reload")
	}
	return nil
}

func validateName(name string) error {
	if !instanceName.MatchString(name) {
		return fmt.Errorf("invalid instance name %q", name)
	}
	return nil
}
func unit(name string) string  { return "shelleyctl-" + name }
func label(name string) string { return "io.github.nnutter.shelleyctl." + name }
func launchDomain() string     { return fmt.Sprintf("gui/%d", os.Getuid()) }

func run(ctx context.Context, command string, args ...string) error {
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", command, args, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func launchLoaded(ctx context.Context, target string) (bool, error) {
	output, err := exec.CommandContext(ctx, "launchctl", "print", target).CombinedOutput()
	if err == nil {
		return true, nil
	}
	// launchctl has no structured existence API. Only its specific missing-service
	// diagnostic is absence. Missing commands, domains and permission errors fail.
	if _, ok := errors.AsType[*exec.ExitError](err); ok && strings.Contains(string(output), "Could not find service") {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %w: %s", target, err, strings.TrimSpace(string(output)))
}

func safeFile(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if int64(stat.Uid) != int64(os.Getuid()) {
		return false
	}
	return info.Mode().Perm()&0o022 == 0
}

func generatedHeader(first string) bool {
	switch first {
	case "# " + marker, "<!-- " + marker + " -->":
		return true
	default:
		return false
	}
}

func owned(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !safeFile(info) {
		return false, fmt.Errorf("refuse foreign or unsafe launcher %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if !generatedHeader(first) {
		return false, fmt.Errorf("refuse foreign launcher %s", path)
	}
	return true, nil
}

// persist publishes a complete, synced file. Link avoids replacing a foreign
// file created while an absent destination is being prepared.
func persist(path, data string) (err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".shelleyctl-*")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, removeTemp(f.Name())) }()
	if _, err = f.WriteString(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err = f.Close(); err != nil {
		return err
	}
	exists, err := owned(path)
	if err != nil {
		return err
	}
	if exists {
		err = os.Rename(f.Name(), path)
	} else {
		err = os.Link(f.Name(), path)
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

func removeTemp(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
