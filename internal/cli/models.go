package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nnutter/shelleyctl/internal/models"
)

type modelOptions struct {
	url, apiType, keyFile string
	names                 []string
}

func (o *modelOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.url, "api-url", "", "Provider endpoint (OpenAI: base URL including /v1)")
	cmd.Flags().StringVar(&o.apiType, "api-type", "openai-responses", "API format: openai-responses, openai (Chat Completions), anthropic, or gemini")
	cmd.Flags().StringVar(&o.keyFile, "api-key-file", "", "Read the API key from a file, or - for stdin")
	cmd.Flags().StringArrayVar(&o.names, "model", nil, "Provider model name (repeat for multiple models)")
}

func (o *modelOptions) read(input io.Reader) (models.Config, error) {
	c := models.Config{APIURL: o.url, APIType: o.apiType, Names: o.names}
	if o.keyFile == "" {
		return c, errors.New("--api-key-file is required with model configuration")
	}
	if o.keyFile == "-" {
		key, err := readKey(input)
		if err != nil {
			return c, err
		}
		c.APIKey = key
	} else {
		key, err := readKeyFile(o.keyFile)
		if err != nil {
			return c, err
		}
		c.APIKey = key
	}
	return c, c.Validate()
}

func readKeyFile(path string) (key string, result error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot open API key file")
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	return readKey(file)
}

func readKey(input io.Reader) (string, error) {
	const limit = 64 << 10
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return "", errors.New("cannot read API key")
	}
	if len(data) > limit {
		return "", errors.New("API key input exceeds 64 KiB")
	}
	return strings.TrimSpace(string(data)), nil
}

func requestedModels(cmd *cobra.Command) bool {
	for _, name := range []string{"api-url", "api-type", "api-key-file", "model"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

type commandRunner func(*cobra.Command, func(context.Context) error) error

func createCommand(app Application, run commandRunner) *cobra.Command {
	var options modelOptions
	cmd := &cobra.Command{
		Use: "create NAME", Short: "Create a named container", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !requestedModels(cmd) {
				return run(cmd, func(ctx context.Context) error { return app.Create(ctx, args[0]) })
			}
			config, err := options.read(cmd.InOrStdin())
			if err != nil {
				return err
			}
			return run(cmd, func(ctx context.Context) error {
				if err := app.Create(ctx, args[0]); err != nil {
					return err
				}
				if err := app.ConfigureModels(ctx, args[0], config); err != nil {
					return fmt.Errorf("instance created but model configuration is incomplete; repeat create with the same options or use models configure: %w", err)
				}
				return nil
			})
		},
	}
	options.bind(cmd)
	return cmd
}

func modelsCommand(app Application, run commandRunner) *cobra.Command {
	parent := &cobra.Command{Use: "models", Short: "Configure instance models"}
	var options modelOptions
	cmd := &cobra.Command{
		Use: "configure NAME", Short: "Add or update models on a running instance", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := options.read(cmd.InOrStdin())
			if err != nil {
				return err
			}
			return run(cmd, func(ctx context.Context) error { return app.ConfigureModels(ctx, args[0], config) })
		},
	}
	options.bind(cmd)
	parent.AddCommand(cmd)
	return parent
}
