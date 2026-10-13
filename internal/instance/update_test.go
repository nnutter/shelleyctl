package instance

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateResumesWithoutRemovingReplacementAndRetainsData(t *testing.T) {
	for _, image := range []string{"original-image", "new-image"} {
		for _, stage := range []string{"remove container", "ensure container", "start container", "ensure exposure"} {
			t.Run(image+"/"+stage, func(t *testing.T) { checkUpdateRecovery(t, image, stage) })
		}
	}
}

func checkUpdateRecovery(t *testing.T, image, stage string) {
	t.Helper()
	failure := errors.New("external failure")
	failing := true
	var actions []string
	var a *Application
	a = testApplication(t, func(action string) error {
		actions = append(actions, action)
		switch action {
		case "remove container", "ensure container":
			r := diskRecord(t, a)
			require.True(t, r.Desired)
			require.Equal(t, image, r.PendingImage, "update intent must precede mutation")
		}
		if failing {
			if action == stage {
				return failure
			}
		}
		return nil
	})
	port := backend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	seed(t, a, port, true)
	home := filepath.Join(a.opts.StateDir, "instances", "demo", "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	marker := filepath.Join(home, "shelley.db")
	require.NoError(t, os.WriteFile(marker, []byte("retained database"), 0o600))
	require.ErrorIs(t, a.Update(t.Context(), "demo", image), failure)
	require.Equal(t, "original-image", diskRecord(t, a).Image)
	failing = false
	a.opts.Image = "unrelated-default"
	t.Setenv("SHELLEYCTL_RESUME", "1")
	retry := &Application{opts: a.opts, containers: a.containers, serve: a.serve, startup: a.startup}
	require.NoError(t, retry.Create(t.Context(), "demo"))
	r := diskRecord(t, a)
	require.Equal(t, record{Name: "demo", Image: image, Port: port, Desired: true}, r)
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "retained database", string(data))
	removals := 0
	for _, action := range actions {
		if action == "remove container" {
			removals++
		}
	}
	want := 1
	if stage == "remove container" {
		want = 2
	}
	require.Equal(t, want, removals, "recovery must not remove a newly created replacement")
	require.Less(t, slices.Index(actions, "ensure startup"), slices.Index(actions, "remove container"), "the scheduled reconciler must know update phases before replacement")
	require.False(t, slices.Contains(actions, "remove exposure"))
	require.False(t, slices.Contains(actions, "remove startup"))
	for _, spec := range a.containers.(*runtimeStub).specs {
		require.Equal(t, image, spec.Image)
		require.Equal(t, home, spec.Home)
		require.Equal(t, port, spec.Port)
	}
}

func TestUpdatePreflightPreservesRunningIntent(t *testing.T) {
	for _, stage := range []string{"check container", "check image", "ensure startup"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("preflight failed")
			var actions []string
			a := testApplication(t, func(action string) error {
				actions = append(actions, action)
				if action == stage {
					return failure
				}
				return nil
			})
			seed(t, a, 19000, true)
			original := diskRecord(t, a)
			require.ErrorIs(t, a.Update(t.Context(), "demo", "replacement"), failure)
			require.Equal(t, original, diskRecord(t, a))
			require.NotContains(t, actions, "remove container")
			require.NotContains(t, actions, "ensure container")
		})
	}
}

func TestUpdateRejectsUnknownAndDeletedInstances(t *testing.T) {
	a := testApplication(t, func(action string) error {
		require.FailNow(t, "unexpected runtime action", "action: %s", action)
		return nil
	})
	require.Error(t, a.Update(t.Context(), "unknown", ""))
	seed(t, a, 19000, false)
	require.Error(t, a.Update(t.Context(), "demo", ""))
	require.False(t, diskRecord(t, a).Desired)
}

func TestDeleteCancelsPendingUpdateWithoutScheduledRevival(t *testing.T) {
	failure := errors.New("replacement failed")
	failing := true
	var actions []string
	a := testApplication(t, func(action string) error {
		actions = append(actions, action)
		if failing {
			if action == "ensure container" {
				return failure
			}
		}
		return nil
	})
	seed(t, a, 19000, true)
	require.ErrorIs(t, a.Update(t.Context(), "demo", "new-image"), failure)
	failing = false
	require.NoError(t, a.Delete(t.Context(), "demo"))
	require.Equal(t, record{Name: "demo", Image: "new-image", Port: 19000}, diskRecord(t, a))
	t.Setenv("SHELLEYCTL_RESUME", "1")
	actions = nil
	require.NoError(t, a.Create(t.Context(), "demo"))
	require.Empty(t, actions)
}
