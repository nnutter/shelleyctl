package startup

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests replace external commands through PATH, never through a production hook.
func fixture(t *testing.T, platform string) (*Adapter, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	t.Setenv("PATH", bin)
	t.Setenv("TEST_LOG", filepath.Join(root, "commands"))
	t.Setenv("TEST_LOADED", filepath.Join(root, "loaded"))
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_LOG"
case "$*" in *"$TEST_FAIL"*) if [ -n "$TEST_FAIL" ]; then echo 'injected failure' >&2; exit 1; fi;; esac
case "$*" in
 print\ gui/*/*) if [ ! -f "$TEST_LOADED" ]; then echo 'Could not find service' >&2; exit 113; fi ;;
 bootstrap\ *) : > "$TEST_LOADED" ;;
 bootout\ *) /bin/rm -f "$TEST_LOADED" ;;
esac
`
	command := "systemctl"
	if platform == "darwin" {
		command = "launchctl"
	}
	require.NoError(t, os.WriteFile(filepath.Join(bin, command), []byte(script), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "shelleyctl"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	a, err := New(platform)
	require.NoError(t, err)
	return a, root
}

func TestRetryInstallationAndRemoval(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			a, root := fixture(t, platform)
			failure := "enable"
			if platform == "darwin" {
				failure = "bootstrap"
			}
			t.Setenv("TEST_FAIL", failure)
			require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "install should report manager failure")
			installed, err := a.Installed("demo")
			require.NoError(t, err)
			require.True(t, installed, "durable files lost")
			t.Setenv("TEST_FAIL", "")
			require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", root))
			require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", root))
			t.Setenv("TEST_FAIL", "disable")
			require.Error(t, a.Remove(t.Context(), "demo"), "disable failure must prevent deletion")
			installed, err = a.Installed("demo")
			require.NoError(t, err)
			require.True(t, installed, "cannot retry removal")
			t.Setenv("TEST_FAIL", "")
			require.NoError(t, os.WriteFile(os.Getenv("TEST_LOG"), nil, 0o600))
			require.NoError(t, a.Remove(t.Context(), "demo"))
			installed, err = a.Installed("demo")
			require.NoError(t, err)
			require.False(t, installed, "launcher remains")
			log, err := os.ReadFile(os.Getenv("TEST_LOG"))
			require.NoError(t, err)
			stop := "stop"
			if platform == "darwin" {
				stop = "bootout"
			}
			require.Contains(t, string(log), "disable")
			require.Contains(t, string(log), stop)
			require.Less(t, strings.Index(string(log), "disable"), strings.Index(string(log), stop), "execution stopped before scheduler disabled")
			require.NoError(t, a.Remove(t.Context(), "demo"))
		})
	}
}

func TestRefuseForeignFilesAndInvalidNames(t *testing.T) {
	a, root := fixture(t, "linux")
	for _, name := range []string{"../x", "Bad", "a\n", "", strings.Repeat("a", 41)} {
		require.Error(t, a.Ensure(t.Context(), name, "shelleyctl", root), "accepted %q", name)
	}
	require.NoError(t, os.MkdirAll(a.dir, 0o700))
	foreign := filepath.Join(a.dir, "shelleyctl-demo.service")
	require.NoError(t, os.WriteFile(foreign, []byte("foreign\n"), 0o600))
	require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "replaced foreign launcher")
	require.Error(t, a.Remove(t.Context(), "demo"), "removed foreign launcher")
	_, err := a.Installed("demo")
	require.Error(t, err, "foreign launcher reported installed")
	data, err := os.ReadFile(foreign)
	require.NoError(t, err)
	require.Equal(t, "foreign\n", string(data), "foreign file changed")
	_, err = os.Stat(filepath.Join(a.dir, "shelleyctl-demo.timer"))
	require.ErrorIs(t, err, os.ErrNotExist, "changed files before checking all ownership")
	require.NoError(t, os.Remove(foreign))
	require.NoError(t, os.Symlink(filepath.Join(root, "target"), foreign))
	require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "accepted launcher symlink")
}

func TestGUIUnavailablePreservesRetryableFiles(t *testing.T) {
	a, root := fixture(t, "darwin")
	t.Setenv("TEST_FAIL", "print gui/")
	require.ErrorContains(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "GUI login session required")
	require.Error(t, a.Remove(t.Context(), "demo"), "must not delete launcher without disabling its GUI service")
	installed, err := a.Installed("demo")
	require.NoError(t, err)
	require.True(t, installed, "GUI failure lost durable files")
	t.Setenv("TEST_FAIL", "")
	require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", root))
	require.NoError(t, a.Remove(t.Context(), "demo"))
}

func TestMissingManagerIsFailure(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			a, root := fixture(t, platform)
			command := "systemctl"
			if platform == "darwin" {
				command = "launchctl"
			}
			require.NoError(t, os.Remove(filepath.Join(os.Getenv("PATH"), command)))
			require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "missing manager treated as absence")
			require.Error(t, a.Remove(t.Context(), "demo"), "removal ignored missing manager")
		})
	}
}

func TestLaunchAgentEscaping(t *testing.T) {
	a, root := fixture(t, "darwin")
	executable := filepath.Join(root, `tool <&"$%`)
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700))
	state := filepath.Join(root, `state <&"$%`)
	path := os.Getenv("PATH") + `:<&"$%`
	t.Setenv("PATH", path)
	require.NoError(t, a.Ensure(t.Context(), "demo", executable, state))
	f, err := os.Open(filepath.Join(a.dir, "io.github.nnutter.shelleyctl.demo.plist"))
	require.NoError(t, err)
	defer func() { assert.NoError(t, f.Close()) }()
	decoder := xml.NewDecoder(f)
	var values []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if start, ok := token.(xml.StartElement); ok && plistValue(start) {
			var value string
			require.NoError(t, decoder.DecodeElement(&value, &start))
			values = append(values, value)
		}
	}
	expected := []string{"io.github.nnutter.shelleyctl.demo", executable, "--plain", "create", "demo", state, "1", path, "30"}
	require.Equal(t, expected, values, "plist values")
}

func plistValue(start xml.StartElement) bool {
	switch start.Name.Local {
	case "string", "integer":
		return true
	default:
		return false
	}
}

func TestSystemdSyntax(t *testing.T) {
	verifier, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze unavailable")
	}
	a, root := fixture(t, "linux")
	executable := filepath.Join(root, `tool "\$%`)
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700))
	require.NoError(t, a.Ensure(t.Context(), "demo", executable, filepath.Join(root, `state "\$%`)))
	service := filepath.Join(a.dir, "shelleyctl-demo.service")
	data, err := os.ReadFile(service)
	require.NoError(t, err)
	// These assertions guard systemd's distinct specifier and environment rules.
	for _, fragment := range []string{`ExecStart=:`, `\"`, `\\`, `%%`, `Environment="SHELLEYCTL_RESUME=1"`, `Environment="PATH=`} {
		require.Contains(t, string(data), fragment, "missing systemd syntax")
	}
	cmd := exec.CommandContext(t.Context(), verifier, "--user", "verify", service, filepath.Join(a.dir, "shelleyctl-demo.timer"))
	cmd.Env = append(os.Environ(), "SYSTEMD_UNIT_PATH="+a.dir+":/usr/lib/systemd/user")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "unit verification: %s", output)
}

func TestLaunchAgentUpdateRetriesWithoutRestartingIdenticalLauncher(t *testing.T) {
	a, root := fixture(t, "darwin")
	require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", root))
	require.NoError(t, os.WriteFile(os.Getenv("TEST_LOG"), nil, 0o600))
	t.Setenv("TEST_FAIL", "bootout")
	require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", root), "identical loaded launcher must remain running")
	log, err := os.ReadFile(os.Getenv("TEST_LOG"))
	require.NoError(t, err)
	require.NotContains(t, string(log), "bootout")
	require.NotContains(t, string(log), "bootstrap")

	// A changed definition must still unload the old job, including on retry
	// after bootout fails. New durable bytes must not hide that failure.
	state := filepath.Join(root, "updated-state")
	require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", state))
	require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", state), "failed update must remain retryable")
	t.Setenv("TEST_FAIL", "bootstrap")
	require.Error(t, a.Ensure(t.Context(), "demo", "shelleyctl", state))
	t.Setenv("TEST_FAIL", "")
	require.NoError(t, os.WriteFile(os.Getenv("TEST_LOG"), nil, 0o600))
	require.NoError(t, a.Ensure(t.Context(), "demo", "shelleyctl", state), "bootstrap failure must remain retryable")
	log, err = os.ReadFile(os.Getenv("TEST_LOG"))
	require.NoError(t, err)
	require.Contains(t, string(log), "bootstrap")
	require.NotContains(t, string(log), "bootout")
	data, err := os.ReadFile(filepath.Join(a.dir, "io.github.nnutter.shelleyctl.demo.plist"))
	require.NoError(t, err)
	require.Contains(t, string(data), state)
}
