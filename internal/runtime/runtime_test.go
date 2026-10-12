package runtime_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtime "github.com/nnutter/shelleyctl/internal/runtime"
)

type reply struct {
	args           []string
	output, stderr string
	exit           int
}

// fakeCLI supplies recorded CLI JSON at the executable boundary. Each response
// is independent of the adapter's decisions. Unexpected mutations exhaust the
// response queue and fail, including after a rejected configuration.
func fakeCLI(t *testing.T, platform string, replies []reply) (*runtime.Adapter, func()) {
	t.Helper()
	dir := t.TempDir()
	binary := "podman"
	if platform == "darwin" {
		binary = "container"
	}
	script := `#!/bin/sh
set -eu
dir=$(dirname "$0")
i=0
if [ -f "$dir/count" ]; then i=$(cat "$dir/count"); fi
printf '%s\n' "$@" > "$dir/args-$i"
printf '%s' "$((i + 1))" > "$dir/count"
if [ ! -f "$dir/out-$i" ]; then echo 'unexpected command' >&2; exit 99; fi
cat "$dir/out-$i"
cat "$dir/err-$i" >&2
exit "$(cat "$dir/exit-$i")"
`
	writeFile(t, filepath.Join(dir, binary), script, 0o700)
	for i, r := range replies {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("out-%d", i)), r.output, 0o600)
		writeFile(t, filepath.Join(dir, fmt.Sprintf("err-%d", i)), r.stderr, 0o600)
		writeFile(t, filepath.Join(dir, fmt.Sprintf("exit-%d", i)), strconv.Itoa(r.exit), 0o600)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a, err := runtime.New(platform)
	require.NoError(t, err)
	return a, func() {
		t.Helper()
		count, err := os.ReadFile(filepath.Join(dir, "count"))
		require.NoError(t, err)
		require.Equal(t, strconv.Itoa(len(replies)), string(count), "command count")
		for i, r := range replies {
			b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("args-%d", i)))
			require.NoError(t, err)
			actual := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			assert.Equal(t, r.args, actual, "command %d", i)
		}
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
}

const podmanJSON = `[{"Id":"abc123","Name":"shelleyctl-demo","ImageName":"localhost/shelleyctl:latest","State":{"Running":false},"Config":{"Labels":{"io.shelleyctl.managed":"true","io.shelleyctl.image":"localhost/shelleyctl:latest","io.shelleyctl.home":"/tmp/home space","io.shelleyctl.port":"19000"}},"Mounts":[{"Type":"bind","Source":"/tmp/home space","Destination":"/home/shelley","RW":true}],"HostConfig":{"PortBindings":{"9000/tcp":[{"HostIp":"127.0.0.1","HostPort":"19000"}]}}}]`

// Swift Codable encodes Filesystem.FSType.virtiofs as {"virtiofs":{}}.
const appleJSON = `[{"configuration":{"id":"shelleyctl-demo","image":{"reference":"localhost/shelleyctl:latest"},"labels":{"io.shelleyctl.managed":"true","io.shelleyctl.image":"localhost/shelleyctl:latest","io.shelleyctl.home":"/tmp/home space","io.shelleyctl.port":"19000"},"mounts":[{"type":{"virtiofs":{}},"source":"/tmp/home space","destination":"/home/shelley","options":[]}],"publishedPorts":[{"hostAddress":"127.0.0.1","hostPort":19000,"containerPort":9000,"proto":"tcp"}]},"status":"stopped","networks":[]}]`

func query(platform, json string) []reply {
	if platform == "darwin" {
		return []reply{{args: []string{"list", "--all", "--format", "json"}, output: json}}
	}
	return []reply{
		{args: []string{"ps", "--all", "--no-trunc", "--format", "json"}, output: `[{"Id":"abc123","Names":["shelleyctl-demo"]}]`},
		{args: []string{"inspect", "--type", "container", "abc123"}, output: json},
	}
}

func absent(platform string) reply {
	if platform == "darwin" {
		return reply{args: []string{"list", "--all", "--format", "json"}, output: "[]"}
	}
	return reply{args: []string{"ps", "--all", "--no-trunc", "--format", "json"}, output: "[]"}
}

func createArgs(platform string) []string {
	args := []string{"create", "--name", "shelleyctl-demo"}
	if platform == "linux" {
		args = append(args, "--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000")
	}
	return append(args, "--label", "io.shelleyctl.managed=true", "--label", "io.shelleyctl.image=localhost/shelleyctl:latest", "--label", "io.shelleyctl.home=/tmp/home space", "--label", "io.shelleyctl.port=19000", "--volume", "/tmp/home space:/home/shelley", "--publish", "127.0.0.1:19000:9000", "localhost/shelleyctl:latest")
}

func fixture(platform string) string {
	if platform == "darwin" {
		return appleJSON
	}
	return podmanJSON
}

func running(platform, json string) string {
	if platform == "darwin" {
		return strings.ReplaceAll(json, `"status":"stopped"`, `"status":"running"`)
	}
	return strings.ReplaceAll(json, `"Running":false`, `"Running":true`)
}

func TestLifecycle(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			id, remove := "abc123", "rm"
			if platform == "darwin" {
				id, remove = "shelleyctl-demo", "delete"
			}
			json := fixture(platform)
			replies := []reply{absent(platform), {args: createArgs(platform)}}
			replies = append(replies, query(platform, json)...)
			replies = append(replies, query(platform, json)...)
			replies = append(replies, reply{args: []string{"start", id}})
			replies = append(replies, query(platform, running(platform, json))...)
			replies = append(replies, query(platform, running(platform, json))...)
			replies = append(replies, reply{args: []string{remove, "--force", id}}, absent(platform))
			a, done := fakeCLI(t, platform, replies)
			spec := runtime.Spec{Name: "demo", Image: "localhost/shelleyctl:latest", Home: "/tmp/home space", Port: 19000}
			require.NoError(t, a.Ensure(t.Context(), spec))
			require.NoError(t, a.Ensure(t.Context(), spec))
			require.NoError(t, a.Start(t.Context(), "demo"))
			require.NoError(t, a.Start(t.Context(), "shelleyctl-demo"))
			require.NoError(t, a.Remove(t.Context(), "demo"))
			require.NoError(t, a.Remove(t.Context(), "demo"))
			done()
		})
	}
}

func unsafeFixture(platform, mismatch string) string {
	json := fixture(platform)
	switch mismatch {
	case "foreign":
		json = strings.ReplaceAll(json, `"io.shelleyctl.managed":"true"`, `"io.shelleyctl.managed":"false"`)
	case "image":
		if platform == "linux" {
			json = strings.ReplaceAll(json, `"ImageName":"localhost/shelleyctl:latest"`, `"ImageName":"foreign:latest"`)
		} else {
			json = strings.ReplaceAll(json, `"reference":"localhost/shelleyctl:latest"`, `"reference":"foreign:latest"`)
		}
	case "home":
		json = strings.ReplaceAll(json, `"Source":"/tmp/home space"`, `"Source":"/tmp/foreign"`)
		json = strings.ReplaceAll(json, `"source":"/tmp/home space"`, `"source":"/tmp/foreign"`)
	case "port":
		json = strings.ReplaceAll(json, `"HostIp":"127.0.0.1"`, `"HostIp":"0.0.0.0"`)
		json = strings.ReplaceAll(json, `"hostAddress":"127.0.0.1"`, `"hostAddress":"0.0.0.0"`)
	case "readonly":
		json = strings.ReplaceAll(json, `"RW":true`, `"RW":false`)
		json = strings.ReplaceAll(json, `"options":[]`, `"options":["ro"]`)
	case "extra-port":
		if platform == "linux" {
			json = strings.ReplaceAll(json, `"PortBindings":{`, `"PortBindings":{"22/tcp":[{"HostIp":"127.0.0.1","HostPort":"19001"}],`)
		} else {
			json = strings.ReplaceAll(json, `"publishedPorts":[`, `"publishedPorts":[{"hostAddress":"127.0.0.1","hostPort":19001,"containerPort":22,"proto":"tcp"},`)
		}
	case "labels":
		json = strings.ReplaceAll(json, `"io.shelleyctl.home":"/tmp/home space"`, `"io.shelleyctl.home":""`)
	}
	return json
}

func TestRefuseUnsafeContainer(t *testing.T) {
	// All three mutation methods must reject independently: a guard only in
	// Ensure cannot protect direct Start or Remove calls.
	for _, platform := range []string{"linux", "darwin"} {
		for _, mismatch := range []string{"foreign", "image", "home", "port", "readonly", "extra-port", "labels"} {
			t.Run(platform+"/"+mismatch, func(t *testing.T) {
				json := unsafeFixture(platform, mismatch)
				replies := append(query(platform, json), query(platform, json)...)
				replies = append(replies, query(platform, json)...)
				a, done := fakeCLI(t, platform, replies)
				spec := runtime.Spec{Name: "demo", Image: "localhost/shelleyctl:latest", Home: "/tmp/home space", Port: 19000}
				for _, err := range []error{a.Ensure(t.Context(), spec), a.Start(t.Context(), "demo"), a.Remove(t.Context(), "demo")} {
					require.ErrorContains(t, err, "refuse")
				}
				done()
			})
		}
	}
}

func inspectReplies(platform, scenario string) []reply {
	replies := []reply{absent(platform)}
	switch scenario {
	case "permission":
		replies[0].output, replies[0].stderr, replies[0].exit = "", "permission denied: daemon unavailable", 1
	case "malformed":
		replies[0].output = "[invalid"
	case "wrong-shape":
		replies[0].output = "null"
	case "missing-identity":
		replies[0].output = "[{}]"
	case "existing":
		replies = query(platform, running(platform, fixture(platform)))
	}
	return replies
}

func TestInspectAbsenceAndFailures(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, scenario := range []string{"absent", "permission", "malformed", "wrong-shape", "missing-identity", "existing"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				replies := inspectReplies(platform, scenario)
				a, done := fakeCLI(t, platform, replies)
				status, err := a.Inspect(t.Context(), "demo")
				switch scenario {
				case "absent":
					require.NoError(t, err)
					require.False(t, status.Exists)
				case "existing":
					require.NoError(t, err)
					require.True(t, status.Exists)
					require.True(t, status.Running)
					require.NotEmpty(t, status.ID)
				default:
					require.Error(t, err, "failure misreported as absence")
					if scenario == "permission" {
						require.ErrorContains(t, err, "permission denied: daemon unavailable")
					}
				}
				done()
			})
		}
	}
}

func TestBuildUsesExplicitPolicyAndLiteralArguments(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "Dockerfile")
			writeFile(t, file, "FROM scratch\n", 0o600)
			policy := filepath.Join(dir, "trusted-policy.json")
			t.Setenv("SHELLEYCTL_SIGNATURE_POLICY", policy)
			args := []string{"build", "--tag", "localhost/example:latest", "--file", file}
			if platform == "linux" {
				args = append(args, "--signature-policy", policy)
			}
			args = append(args, dir)
			a, done := fakeCLI(t, platform, []reply{{args: args}})
			require.NoError(t, a.Build(t.Context(), "localhost/example:latest", dir))
			done()
		})
	}
}

func TestCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "podman"), "#!/bin/sh\nexec sleep 30\n", 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	a, err := runtime.New("linux")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = a.Inspect(ctx, "demo")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func mutationScenario(t *testing.T, platform, operation string, completed bool) ([]reply, func(*runtime.Adapter) error) {
	t.Helper()
	json := fixture(platform)
	id, remove := "abc123", "rm"
	if platform == "darwin" {
		id, remove = "shelleyctl-demo", "delete"
	}
	var replies []reply
	var invoke func(*runtime.Adapter) error
	failed := reply{stderr: "daemon refused operation", exit: 1}
	switch operation {
	case "create":
		// Expected commands come from the protocol contract, not the adapter.
		failed.args = createArgs(platform)
		replies = []reply{absent(platform), failed}
		if completed {
			replies = append(replies, query(platform, json)...)
		} else {
			replies = append(replies, absent(platform))
		}
		invoke = func(a *runtime.Adapter) error {
			return a.Ensure(t.Context(), runtime.Spec{Name: "demo", Image: "localhost/shelleyctl:latest", Home: "/tmp/home space", Port: 19000})
		}
	case "start":
		failed.args = []string{"start", id}
		replies = append(query(platform, json), failed)
		if completed {
			json = running(platform, json)
		}
		replies = append(replies, query(platform, json)...)
		invoke = func(a *runtime.Adapter) error { return a.Start(t.Context(), "demo") }
	case "remove":
		failed.args = []string{remove, "--force", id}
		replies = append(query(platform, json), failed)
		if completed {
			replies = append(replies, absent(platform))
		} else {
			replies = append(replies, query(platform, json)...)
		}
		invoke = func(a *runtime.Adapter) error { return a.Remove(t.Context(), "demo") }
	default:
		require.FailNow(t, "unknown mutation", operation)
	}
	return replies, invoke
}

func TestMutationFailuresAndConcurrentCompletion(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		for _, operation := range []string{"create", "start", "remove"} {
			for _, completed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/completed=%t", platform, operation, completed), func(t *testing.T) {
					replies, invoke := mutationScenario(t, platform, operation, completed)
					a, done := fakeCLI(t, platform, replies)
					err := invoke(a)
					if completed {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, "daemon refused operation")
					}
					done()
				})
			}
		}
	}
}

func TestAppleStructuredStatus(t *testing.T) {
	json := strings.ReplaceAll(appleJSON, `"status":"stopped"`, `"status":{"state":"running","networks":[]}`)
	a, done := fakeCLI(t, "darwin", query("darwin", json))
	status, err := a.Inspect(t.Context(), "demo")
	require.NoError(t, err)
	require.True(t, status.Exists)
	require.True(t, status.Running)
	done()
}

func TestPodmanInspectFailureIsNotAbsence(t *testing.T) {
	replies := query("linux", podmanJSON)
	replies[1].output, replies[1].stderr, replies[1].exit = "", "permission denied inspecting container", 1
	a, done := fakeCLI(t, "linux", replies)
	_, err := a.Inspect(t.Context(), "demo")
	require.ErrorContains(t, err, "permission denied inspecting container")
	done()
}
