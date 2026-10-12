package tailscale_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nnutter/shelleyctl/internal/tailscale"
)

const running = `{"BackendState":"Running","Self":{"Tags":["tag:another-host"]},"CurrentTailnet":{"MagicDNSSuffix":"example.ts.net"}}`

const ownedService = `{"TCP":{"443":{"HTTPS":true}},"Web":{"shelley-demo.example.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8080"}}}}}`

// Node-level Serve and another Service must never become deletion targets.
const unrelated = `"TCP":{"8443":{"HTTPS":true}},"Web":{"host.example.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:9000"}}}},"Services":{"svc:unrelated":{"TCP":{"80":{"HTTP":true}}}`

func config(service string) string {
	return "{" + unrelated + `,"svc:shelley-demo":` + service + "}}"
}

type reply struct {
	args string
	out  string
	fail bool
}

// Install an executable at the real PATH boundary. It accepts only the
// prescribed protocol and records every invocation, including unexpected ones.
func fakeCLI(t *testing.T, replies ...reply) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("executable protocol fixture requires /bin/sh")
	}
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	var script strings.Builder
	script.WriteString("#!/bin/sh\nn=0\nif [ -f '" + counter + "' ]; then read -r n < '" + counter + "'; fi\nn=$((n+1))\nprintf '%s' \"$n\" > '" + counter + "'\ncase $n in\n")
	for i, r := range replies {
		script.WriteString(strconv.Itoa(i+1) + ")\n[ \"$*\" = '" + r.args + "' ] || exit 97\n")
		script.WriteString("printf '%s' '" + r.out + "'\n")
		if r.fail {
			script.WriteString("printf '%s' 'secret-auth-token' >&2\nexit 1\n;;\n")
		} else {
			script.WriteString("exit 0\n;;\n")
		}
	}
	script.WriteString("*) exit 98;;\nesac\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tailscale"), []byte(script.String()), 0o700))
	t.Setenv("PATH", dir)
	t.Cleanup(func() {
		data, err := os.ReadFile(counter)
		if len(replies) == 0 && os.IsNotExist(err) {
			return
		}
		if assert.NoError(t, err) {
			assert.Equal(t, strconv.Itoa(len(replies)), string(data), "command count")
		}
	})
}

func TestServiceLifecycleProtocol(t *testing.T) {
	fakeCLI(t,
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: "{" + unrelated + "}}"},
		reply{args: "serve --service=svc:shelley-demo --https=443 --bg --yes http://127.0.0.1:8080"},
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: config(ownedService)},
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: config(ownedService)},
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: config(ownedService)},
		reply{args: "serve clear svc:shelley-demo"},
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: "{" + unrelated + "}}"},
	)
	a := tailscale.New()
	ctx := t.Context()
	require.NoError(t, a.Ensure(ctx, "demo", 8080))
	require.NoError(t, a.Ensure(ctx, "demo", 8080))
	s, err := a.Inspect(ctx, "demo", 8080)
	require.NoError(t, err)
	assert.True(t, s.Configured)
	assert.Equal(t, "https://shelley-demo.example.ts.net", s.URL)
	assert.Contains(t, s.Detail, "not verified")
	require.NoError(t, a.Remove(ctx, "demo", 8080))
	require.NoError(t, a.Remove(ctx, "demo", 8080))
}

func TestRejectConflictsWithoutMutating(t *testing.T) {
	cases := map[string]string{
		"different backend":      strings.ReplaceAll(ownedService, "8080", "8081"),
		"extra port":             strings.ReplaceAll(ownedService, `"443":{"HTTPS":true}`, `"443":{"HTTPS":true},"80":{"HTTP":true}`),
		"extra mount":            strings.ReplaceAll(ownedService, `"Handlers":{`, `"Handlers":{"/admin":{"Text":"private"},`),
		"different hostname":     strings.ReplaceAll(ownedService, "shelley-demo", "shelley-other"),
		"tun forwarding":         strings.ReplaceAll(ownedService, `{"TCP"`, `{"Tun":true,"TCP"`),
		"extra handler behavior": strings.ReplaceAll(ownedService, `"Proxy":`, `"AcceptAppCaps":["example.com/cap"],"Proxy":`),
		"unknown feature":        strings.ReplaceAll(ownedService, `{"TCP"`, `{"NewFeature":true,"TCP"`),
		"null service":           "null",
	}
	for name, service := range cases {
		for _, operation := range []string{"ensure", "remove", "inspect"} {
			t.Run(name+"/"+operation, func(t *testing.T) {
				fakeCLI(t, reply{args: "status --json", out: running}, reply{args: "serve status --json", out: config(service)})
				a := tailscale.New()
				var err error
				switch operation {
				case "ensure":
					err = a.Ensure(t.Context(), "demo", 8080)
				case "remove":
					err = a.Remove(t.Context(), "demo", 8080)
				default:
					_, err = a.Inspect(t.Context(), "demo", 8080)
				}
				require.ErrorContains(t, err, "conflicting")
			})
		}
	}
}

func TestPreflightAndDaemonFailures(t *testing.T) {
	cases := []struct {
		name    string
		replies []reply
		want    string
	}{
		{"host daemon failure", []reply{{args: "status --json", out: "secret-auth-token", fail: true}}, "command failed"},
		{"stopped", []reply{{args: "status --json", out: strings.ReplaceAll(running, "Running", "Stopped")}}, "not running"},
		{"untagged", []reply{{args: "status --json", out: strings.ReplaceAll(running, `["tag:another-host"]`, `[]`)}}, "tagged host"},
		{"missing suffix", []reply{{args: "status --json", out: strings.ReplaceAll(running, "example.ts.net", "")}}, "MagicDNS"},
		{"malformed host", []reply{{args: "status --json", out: "secret-auth-token"}}, "status JSON"},
		{"serve daemon failure", []reply{{args: "status --json", out: running}, {args: "serve status --json", out: "secret-auth-token", fail: true}}, "command failed"},
		{"malformed serve", []reply{{args: "status --json", out: running}, {args: "serve status --json", out: "secret-auth-token"}}, "status JSON"},
		{"clear failure", []reply{{args: "status --json", out: running}, {args: "serve status --json", out: config(ownedService)}, {args: "serve clear svc:shelley-demo", out: "secret-auth-token", fail: true}}, "command failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeCLI(t, tc.replies...)
			err := tailscale.New().Remove(t.Context(), "demo", 8080)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "secret-auth-token")
		})
	}
}

func TestMissingConfigurationAndCreateFailure(t *testing.T) {
	fakeCLI(t,
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: "null"},
		reply{args: "status --json", out: running},
		reply{args: "serve status --json", out: "{}"},
		reply{args: "serve --service=svc:shelley-demo --https=443 --bg --yes http://127.0.0.1:8080", out: "secret-auth-token", fail: true},
	)
	a := tailscale.New()
	s, err := a.Inspect(t.Context(), "demo", 8080)
	require.NoError(t, err)
	assert.False(t, s.Configured)
	assert.Equal(t, "https://shelley-demo.example.ts.net", s.URL)
	err = a.Ensure(t.Context(), "demo", 8080)
	require.ErrorContains(t, err, "command failed (exit 1)")
	assert.NotContains(t, err.Error(), "secret-auth-token")
	assert.Contains(t, err.Error(), "Define svc:shelley-demo manually")
	assert.Contains(t, err.Error(), "Approve this host")
	assert.Contains(t, err.Error(), "access policy")
}

func TestCanceledOperationDoesNotExecuteCLI(t *testing.T) {
	fakeCLI(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, tailscale.New().Ensure(ctx, "demo", 8080), context.Canceled)
}

func TestInvalidInputDoesNotExecuteCLI(t *testing.T) {
	fakeCLI(t)
	for _, name := range []string{"", "svc:demo", "../demo", "Demo", "demo;exit", strings.Repeat("a", 56)} {
		require.Error(t, tailscale.New().Ensure(t.Context(), name, 8080), "accepted name %q", name)
	}
	for _, port := range []int{0, -1, 65536} {
		require.Error(t, tailscale.New().Remove(t.Context(), "demo", port), "accepted port %d", port)
	}
}
