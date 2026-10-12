package instance

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	cr "github.com/nnutter/shelleyctl/internal/runtime"
)

func TestFirstCreatePersistsAllocationBeforeRuntimeFailure(t *testing.T) {
	failure := errors.New("image not built")
	var a *Application
	a = testApplication(t, func(action string) error {
		require.Equal(t, "ensure container", action)
		r := diskRecord(t, a)
		require.True(t, r.Desired)
		require.Equal(t, "original-image", r.Image)
		require.GreaterOrEqual(t, r.Port, 19000)
		require.LessOrEqual(t, r.Port, 29999)
		return failure
	})
	require.ErrorIs(t, a.Create(t.Context(), "demo"), failure)
	original := diskRecord(t, a)
	runtime := a.containers.(*runtimeStub)
	require.Len(t, runtime.specs, 1)
	spec := runtime.specs[0]
	info, err := os.Stat(spec.Home)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	a.opts.Image = "changed-default"
	require.ErrorIs(t, a.Create(t.Context(), "demo"), failure)
	require.Equal(t, original, diskRecord(t, a))
	require.Equal(t, []cr.Spec{spec, spec}, runtime.specs)
}
