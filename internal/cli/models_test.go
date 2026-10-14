package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelFlagsConfigureCreatedAndExistingInstances(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "provider-key")
	require.NoError(t, os.WriteFile(keyPath, []byte("fixture-key\n"), 0o600))
	for _, command := range []string{"create", "models configure"} {
		for _, keyFile := range []string{keyPath, "-"} {
			t.Run(command+" "+keyFile, func(t *testing.T) {
				app := &recordingApp{}
				root := New(app)
				var output bytes.Buffer
				root.SetOut(&output)
				root.SetErr(&output)
				root.SetIn(strings.NewReader("fixture-key\n"))
				args := append(strings.Fields(command), "family", "--api-url", "https://api.example/v1", "--api-key-file", keyFile, "--model", "one", "--model", "two")
				root.SetArgs(args)
				require.NoError(t, root.ExecuteContext(t.Context()))
				want := []string{"models configure family"}
				if command == "create" {
					want = []string{"create family", "models configure family"}
				}
				require.Equal(t, want, app.calls)
				require.Equal(t, "fixture-key", app.modelConfig.APIKey)
				require.Equal(t, "https://api.example/v1", app.modelConfig.APIURL)
				require.Equal(t, "openai-responses", app.modelConfig.APIType)
				require.Equal(t, []string{"one", "two"}, app.modelConfig.Names)
				require.NotContains(t, output.String(), "fixture-key")
			})
		}
	}
}

func TestInvalidModelOptionsDoNotCreateResources(t *testing.T) {
	for _, scenario := range []string{"missing endpoint", "invalid endpoint", "missing key source", "missing model", "invalid format", "empty key", "multiline key", "oversized key"} {
		t.Run(scenario, func(t *testing.T) {
			args := []string{"create", "family", "--api-url", "https://api.example/v1", "--api-key-file", "-", "--model", "one"}
			key := "fixture-key\n"
			switch scenario {
			case "missing endpoint":
				args[3] = ""
			case "invalid endpoint":
				args[3] = "https://user:fixture-key@api.example/v1"
			case "missing key source":
				args[5] = ""
			case "missing model":
				args = args[:6]
			case "invalid format":
				args = append(args, "--api-type", "unsupported")
			case "empty key":
				key = "\n"
			case "multiline key":
				key = "fixture-key\nsecond-line"
			case "oversized key":
				key = strings.Repeat("x", (64<<10)+1)
			}
			app := &recordingApp{}
			root := New(app)
			root.SetIn(strings.NewReader(key))
			root.SetArgs(args)
			err := root.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Empty(t, app.calls)
			require.NotContains(t, err.Error(), "fixture-key")
		})
	}
}

func TestCreateReportsPartialModelSetupAndStopsOnCreationFailure(t *testing.T) {
	for _, failure := range []string{"create", "configure"} {
		t.Run(failure, func(t *testing.T) {
			app := &recordingApp{}
			if failure == "create" {
				app.err = errors.New("creation failed")
			} else {
				app.modelErr = errors.New("configuration failed")
			}
			root := New(app)
			root.SetIn(strings.NewReader("fixture-key"))
			root.SetArgs([]string{"create", "family", "--api-url", "https://api.example/v1", "--api-key-file", "-", "--model", "one", "--api-type", "openai"})
			err := root.ExecuteContext(t.Context())
			if failure == "create" {
				require.ErrorContains(t, err, "creation failed")
				require.Equal(t, []string{"create family"}, app.calls)
			} else {
				require.ErrorContains(t, err, "instance created but model configuration is incomplete")
				require.Equal(t, []string{"create family", "models configure family"}, app.calls)
				require.Equal(t, "openai", app.modelConfig.APIType)
			}
		})
	}
}
