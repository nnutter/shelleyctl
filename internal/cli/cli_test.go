package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type recordingApp struct {
	calls []string
	ctx   context.Context
	err   error
}

func (a *recordingApp) Build(ctx context.Context) error { return a.record(ctx, "build") }
func (a *recordingApp) Create(ctx context.Context, name string) error {
	return a.record(ctx, "create "+name)
}

func (a *recordingApp) Delete(ctx context.Context, name string) error {
	return a.record(ctx, "delete "+name)
}

func (a *recordingApp) Status(ctx context.Context, w io.Writer) error {
	if _, err := io.WriteString(w, "container status\n"); err != nil {
		return err
	}
	return a.record(ctx, "status")
}

func (a *recordingApp) record(ctx context.Context, call string) error {
	a.calls = append(a.calls, call)
	a.ctx = ctx
	return a.err
}

func TestCommandDispatch(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, command := range []string{"status", "build", "create example", "delete example"} {
			t.Run(command+map[bool]string{false: " redirected", true: " plain"}[plain], func(t *testing.T) {
				for _, appErr := range []error{nil, errors.New("application failed")} {
					app := &recordingApp{err: appErr}
					cmd := New(app)
					var stdout, stderr bytes.Buffer
					cmd.SetOut(&stdout)
					cmd.SetErr(&stderr)
					cmd.SetIn(strings.NewReader(""))
					args := strings.Fields(command)
					if plain {
						args = append(args, "--plain")
					}
					cmd.SetArgs(args)
					ctx := t.Context()
					err := cmd.ExecuteContext(ctx)
					if appErr == nil {
						require.NoError(t, err)
					} else {
						require.Same(t, appErr, err, "must return the underlying error")
					}
					require.Equal(t, []string{command}, app.calls)
					require.Same(t, ctx, app.ctx)
					wantOutput := ""
					if command == "status" {
						wantOutput = "container status\n"
					}
					require.Equal(t, wantOutput, stdout.String())
					require.Empty(t, stderr.String())
				}
			})
		}
	}
}

func TestInvalidArgumentsDoNotDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"},
		{"status", "extra"},
		{"build", "extra"},
		{"create"},
		{"create", "one", "two"},
		{"delete"},
		{"delete", "one", "two"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app := &recordingApp{}
			cmd := New(app)
			cmd.SetArgs(args)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			require.Error(t, cmd.ExecuteContext(t.Context()), "expected argument error")
			require.Empty(t, app.calls, "invalid arguments must not dispatch")
		})
	}
}

func TestHelpDoesNotDispatchAndExplainsDataRetention(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"status", "--help"}, {"build", "--help"}, {"create", "--help"}, {"delete", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app := &recordingApp{}
			cmd := New(app)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs(args)
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			require.Empty(t, app.calls, "help must not dispatch")
			require.NotEmpty(t, output.String())
			if len(args) > 0 {
				if args[0] == "delete" {
					require.Contains(t, output.String(), "data is retained")
				}
			}
		})
	}
}

func TestCompletionGeneratesScriptsWithoutDispatch(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			app := &recordingApp{}
			cmd := New(app)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"completion", shell})
			require.NoError(t, Execute(t.Context(), cmd))
			require.Contains(t, output.String(), "shelleyctl")
			require.Empty(t, app.calls, "completion must not invoke application operations")
		})
	}
}
