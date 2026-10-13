package image

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cr "github.com/nnutter/shelleyctl/internal/runtime"
)

// Validate the distributed context, not just a checkout whose file permissions
// differ from Materialize. All image assets must be readable by the agent UID.
func TestNativeEmbeddedContext(t *testing.T) {
	if os.Getenv("SHELLEYCTL_TEST_IMAGE") == "" {
		t.Skip("set SHELLEYCTL_TEST_IMAGE to run native image contracts")
	}
	dir, err := Materialize()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	adapter, err := cr.New(runtime.GOOS)
	require.NoError(t, err)
	image := fmt.Sprintf("localhost/shelleyctl-context-test:%d", os.Getpid())
	require.NoError(t, adapter.Build(t.Context(), image, dir))
	t.Cleanup(func() {
		// The test context is canceled before cleanup. Bound cleanup separately.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		operation := "rm"
		if runtime.GOOS == "darwin" {
			operation = "delete"
		}
		output, err := exec.CommandContext(ctx, adapter.Binary(), "image", operation, image).CombinedOutput()
		require.NoError(t, err, "%s", output)
	})
	home := t.TempDir()
	args := []string{"run", "--rm"}
	if runtime.GOOS == "darwin" {
		args = append(args, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
	} else {
		args = append(args, "--pull=never", "--network=none", "--passwd=false", "--userns=keep-id:uid=1000,gid=1000", "--user", "1000:1000")
	}
	args = append(args, "--volume", home+":/home/shelley", image, "nginx", "-e", "stderr", "-t", "-c", "/etc/shelleyctl/nginx.conf")
	cmd := exec.CommandContext(t.Context(), adapter.Binary(), args...)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}
