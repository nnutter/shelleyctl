package runtime_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	cr "github.com/nnutter/shelleyctl/internal/runtime"
)

func TestCheckRejectsContainerFromAnotherStateDirectory(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			a, done := fakeCLI(t, platform, query(platform, fixture(platform)))
			spec := cr.Spec{Name: "demo", Image: "localhost/shelleyctl:latest", Home: filepath.Join(t.TempDir(), "other-home"), Port: 19000}
			require.ErrorContains(t, a.Check(t.Context(), spec), "expected writable home mount")
			done()
		})
	}
}
