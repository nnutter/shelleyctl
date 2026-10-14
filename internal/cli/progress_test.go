package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProgressReturnsApplicationError(t *testing.T) {
	for _, want := range []error{nil, errors.New("build failed")} {
		err := runProgress(t.Context(), strings.NewReader(""), io.Discard, "build", func(context.Context) error {
			return want
		})
		if want == nil {
			require.NoError(t, err)
		} else {
			require.Same(t, want, err, "must return the underlying error")
		}
	}
}

func TestProgressCancellationWaitsForApplicationResult(t *testing.T) {
	for _, mode := range []string{"keyboard", "parent"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			input := ""
			if mode == "keyboard" {
				input = "\x03"
			}
			started := make(chan struct{})
			result := make(chan error, 1)
			want := errors.New("application cleanup failed")
			go func() {
				result <- runProgress(ctx, strings.NewReader(input), io.Discard, "build", func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return want
				})
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				require.FailNow(t, "application did not start")
			}
			if mode == "parent" {
				cancel()
			}
			select {
			case err := <-result:
				require.Same(t, want, err, "must return the underlying error")
			case <-time.After(3 * time.Second):
				require.FailNow(t, "progress did not finish after cancellation")
			}
		})
	}
}
