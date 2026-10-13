package instance

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cr "github.com/nnutter/shelleyctl/internal/runtime"
	"github.com/nnutter/shelleyctl/internal/tailscale"
)

// The adapters record external actions, not lifecycle decisions. Assertions on
// disk during each action independently check the coordinator's durable intent.
type runtimeStub struct {
	action func(string) error
	specs  []cr.Spec
}

func (r *runtimeStub) Build(context.Context, string, string) error { return r.action("build") }
func (r *runtimeStub) Check(context.Context, cr.Spec) error        { return r.action("check container") }

func (r *runtimeStub) CheckImage(context.Context, string) error { return r.action("check image") }

func (r *runtimeStub) Ensure(_ context.Context, s cr.Spec) error {
	r.specs = append(r.specs, s)
	return r.action("ensure container")
}

func (r *runtimeStub) Inspect(context.Context, string) (cr.Status, error) {
	return cr.Status{}, r.action("inspect container")
}

func (r *runtimeStub) Remove(context.Context, string) error { return r.action("remove container") }
func (r *runtimeStub) Start(context.Context, string) error  { return r.action("start container") }

type exposureStub struct{ action func(string) error }

func (s *exposureStub) Ensure(context.Context, string, int) error { return s.action("ensure exposure") }

func (s *exposureStub) Inspect(context.Context, string, int) (tailscale.Status, error) {
	return tailscale.Status{}, s.action("inspect exposure")
}

func (s *exposureStub) Remove(context.Context, string, int) error { return s.action("remove exposure") }

type launcherStub struct {
	action    func(string) error
	installed bool
}

func (s *launcherStub) Ensure(context.Context, string, string, string) error {
	err := s.action("ensure startup")
	if err == nil {
		s.installed = true
	}
	return err
}

func (s *launcherStub) Installed(string) (bool, error) {
	return s.installed, s.action("inspect startup")
}

func (s *launcherStub) Remove(context.Context, string) error {
	err := s.action("remove startup")
	if err == nil {
		s.installed = false
	}
	return err
}

func testApplication(t *testing.T, action func(string) error) *Application {
	t.Helper()
	t.Setenv("SHELLEYCTL_RESUME", "")
	return &Application{opts: &Options{StateDir: t.TempDir(), Image: "original-image", Executable: "/usr/bin/shelleyctl"}, containers: &runtimeStub{action: action}, serve: &exposureStub{action: action}, startup: &launcherStub{action: action}}
}

func diskRecord(t *testing.T, a *Application) record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(a.opts.StateDir, "state.json"))
	require.NoError(t, err)
	var s state
	require.NoError(t, json.Unmarshal(data, &s))
	r, ok := s.Instances["demo"]
	require.True(t, ok)
	return r
}

func seed(t *testing.T, a *Application, port int, desired bool) {
	t.Helper()
	require.NoError(t, a.save(state{Version: 1, Instances: map[string]record{"demo": {Name: "demo", Image: "original-image", Port: port, Desired: desired}}}))
}

func backend(t *testing.T, handler http.Handler) int {
	t.Helper()
	port, err := allocatePort(state{})
	require.NoError(t, err)
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(handler)
	require.NoError(t, server.Listener.Close())
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return port
}

func TestCreateResumesAfterExternalFailure(t *testing.T) {
	for _, stage := range []string{"ensure container", "start container", "ensure startup", "ensure exposure"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("external failure")
			failing := true
			var a *Application
			var actions []string
			a = testApplication(t, func(action string) error {
				require.True(t, diskRecord(t, a).Desired, "intent must precede %s", action)
				actions = append(actions, action)
				if failing {
					if action == stage {
						return failure
					}
				}
				return nil
			})
			port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			seed(t, a, port, false)
			require.ErrorIs(t, a.Create(t.Context(), "demo"), failure)
			require.Equal(t, stage, actions[len(actions)-1])
			original := diskRecord(t, a)
			failing = false
			a.opts.Image = "different-image"
			// A fresh coordinator proves recovery does not depend on in-memory progress.
			retry := &Application{opts: a.opts, containers: a.containers, serve: a.serve, startup: a.startup}
			require.NoError(t, retry.Create(t.Context(), "demo"))
			require.Equal(t, original, diskRecord(t, a))
			require.Equal(t, "ensure exposure", actions[len(actions)-1])
		})
	}
}

func TestDeleteResumesAndRetainsHomeForExplicitRecreate(t *testing.T) {
	var a *Application
	var actions []string
	failure := errors.New("tailscale unavailable")
	failing := true
	a = testApplication(t, func(action string) error {
		if slices.Contains([]string{"remove exposure", "remove startup", "remove container"}, action) {
			require.False(t, diskRecord(t, a).Desired)
		}
		actions = append(actions, action)
		if failing {
			if action == "remove exposure" {
				return failure
			}
		}
		return nil
	})
	port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	seed(t, a, port, true)
	home := filepath.Join(a.opts.StateDir, "instances", "demo", "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	retained := filepath.Join(home, "conversation")
	require.NoError(t, os.WriteFile(retained, []byte("keep me"), 0o600))
	require.ErrorIs(t, a.Delete(t.Context(), "demo"), failure)
	require.Equal(t, []string{"check container", "remove exposure", "remove startup", "remove container"}, actions)
	t.Setenv("SHELLEYCTL_RESUME", "1")
	require.NoError(t, a.Create(t.Context(), "demo"))
	require.Len(t, actions, 4, "scheduled resume must not revive a tombstone")
	failing = false
	require.NoError(t, a.Delete(t.Context(), "demo"))
	t.Setenv("SHELLEYCTL_RESUME", "")
	a.opts.Image = "new-default"
	require.NoError(t, a.Create(t.Context(), "demo"))
	r := diskRecord(t, a)
	require.True(t, r.Desired)
	require.Equal(t, port, r.Port)
	require.Equal(t, "original-image", r.Image)
	specs := a.containers.(*runtimeStub).specs
	require.Len(t, specs, 1)
	require.Equal(t, home, specs[0].Home)
	data, err := os.ReadFile(retained)
	require.NoError(t, err)
	require.Equal(t, "keep me", string(data))
}

func TestScheduledResumeRepairsOnlyMissingLauncher(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(fmt.Sprint(installed), func(t *testing.T) {
			var actions []string
			a := testApplication(t, func(action string) error { actions = append(actions, action); return nil })
			a.startup.(*launcherStub).installed = installed
			port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
			seed(t, a, port, true)
			t.Setenv("SHELLEYCTL_RESUME", "1")
			require.NoError(t, a.Create(t.Context(), "demo"))
			if installed {
				require.NotContains(t, actions, "ensure startup")
			} else {
				require.Contains(t, actions, "ensure startup")
			}
			require.Contains(t, actions, "ensure exposure")
		})
	}
}

func TestCreateWaitsForHTTPReadiness(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.Header().Set("Location", "http://127.0.0.1:1/")
				w.WriteHeader(status)
			}))
			var actions []string
			a := testApplication(t, func(action string) error { actions = append(actions, action); return nil })
			seed(t, a, port, true)
			ctx, cancel := context.WithTimeout(t.Context(), 600*time.Millisecond)
			defer cancel()
			err := a.Create(ctx, "demo")
			require.Positive(t, requests.Load())
			if status == http.StatusServiceUnavailable {
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.NotContains(t, actions, "ensure startup")
				require.NotContains(t, actions, "ensure exposure")
			} else {
				require.NoError(t, err)
				require.Contains(t, actions, "ensure exposure")
			}
		})
	}
}

func TestCorruptStateRejectsCommandsBeforeExternalActions(t *testing.T) {
	for _, data := range []string{
		`{`,
		`{"version":2,"instances":{}}`,
		`{"version":1,"instances":{"demo":{"name":"demo","image":"image","port":18999,"desired":true}}}`,
		`{"version":1,"instances":{"demo":{"name":"demo","image":"image","port":30000,"desired":true}}}`,
		`{"version":1,"instances":{"demo":{"name":"demo","image":"image","port":19000},"other":{"name":"other","image":"image","port":19000}}}`,
		`{"version":1,"version":1,"instances":{}}`,
		`{"version":1,"instances":{"demo":{"name":"other","image":"image","port":19000}}}`,
		`{"version":1,"instances":{"demo":{"name":"demo","image":"","port":19000}}}`,
	} {
		t.Run(data, func(t *testing.T) {
			var actions []string
			a := testApplication(t, func(action string) error { actions = append(actions, action); return nil })
			require.NoError(t, os.WriteFile(filepath.Join(a.opts.StateDir, "state.json"), []byte(data), 0o600))
			require.Error(t, a.Create(t.Context(), "demo"))
			require.Error(t, a.Delete(t.Context(), "demo"))
			require.Error(t, a.Status(t.Context(), io.Discard))
			require.Empty(t, actions)
			persisted, err := os.ReadFile(filepath.Join(a.opts.StateDir, "state.json"))
			require.NoError(t, err)
			require.Equal(t, data, string(persisted))
		})
	}
}

func TestLifecycleLockSerializesAndCancelsWaiters(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	failure := errors.New("stop before readiness")
	a := testApplication(t, func(action string) error {
		calls.Add(1)
		if action == "ensure container" {
			close(entered)
			<-release
			return failure
		}
		return nil
	})
	created := make(chan error, 1)
	go func() { created <- a.Create(t.Context(), "demo") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "create did not enter runtime")
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	// A separate coordinator uses the same durable store, as separate processes do.
	waiter := &Application{opts: &Options{StateDir: a.opts.StateDir}, containers: a.containers, serve: a.serve, startup: a.startup}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, waiter.Delete(ctx, "demo"), context.DeadlineExceeded)
	require.EqualValues(t, 1, calls.Load(), "no cleanup action may run while create holds the lock")
	require.True(t, diskRecord(t, a).Desired)
	close(release)
	require.ErrorIs(t, <-created, failure)
	require.NoError(t, waiter.Delete(t.Context(), "demo"))
	require.False(t, diskRecord(t, a).Desired)
	require.EqualValues(t, 5, calls.Load())
}
