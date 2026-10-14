package instance

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nnutter/shelleyctl/internal/models"
	cr "github.com/nnutter/shelleyctl/internal/runtime"
)

type modelRuntime struct {
	*runtimeStub
	status cr.Status
}

func (r *modelRuntime) Inspect(context.Context, string) (cr.Status, error) { return r.status, nil }

func TestModelCredentialsReachOnlyAnOwnedRunningInstance(t *testing.T) {
	for _, scenario := range []string{"unknown", "retained", "pending update", "foreign", "missing", "stopped", "running"} {
		t.Run(scenario, func(t *testing.T) {
			var requests atomic.Int32
			port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method == http.MethodGet {
					_, _ = io.WriteString(w, "[]")
					return
				}
				w.WriteHeader(http.StatusCreated)
			}))
			a := testApplication(t, func(action string) error {
				if scenario == "foreign" {
					if action == "check container" {
						return errors.New("foreign container")
					}
				}
				return nil
			})
			status := cr.Status{Exists: true, Running: true}
			if scenario == "missing" {
				status = cr.Status{}
			}
			if scenario == "stopped" {
				status.Running = false
			}
			a.containers = &modelRuntime{runtimeStub: a.containers.(*runtimeStub), status: status}
			require.NoError(t, a.prepare())
			r := record{Name: "family", Image: "original-image", Port: port, Desired: scenario != "retained"}
			if scenario == "pending update" {
				r.PendingImage = "next-image"
			}
			s := state{Version: 1, Instances: map[string]record{}}
			if scenario != "unknown" {
				s.Instances["family"] = r
			}
			require.NoError(t, a.save(s))
			config := models.Config{APIURL: "https://api.example/v1", APIType: "openai", APIKey: "fixture-key", Names: []string{"one"}}
			err := a.ConfigureModels(t.Context(), "family", config)
			if scenario == "running" {
				require.NoError(t, err)
				require.EqualValues(t, 2, requests.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, requests.Load(), "no HTTP request may precede admission")
			}
			data, err := os.ReadFile(filepath.Join(a.opts.StateDir, "state.json"))
			require.NoError(t, err)
			require.NotContains(t, string(data), config.APIKey)
		})
	}
}
