// Package cli defines the shelleyctl command tree.
package cli

import (
	"context"
	"io"

	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"
)

// Application supplies the operations exposed by the command tree.
type Application interface {
	Status(context.Context, io.Writer) error
	Build(context.Context) error
	Create(context.Context, string) error
	Delete(context.Context, string) error
	Update(context.Context, string, string) error
}

// Execute applies Fang's help and error presentation to the command tree.
func Execute(ctx context.Context, root *cobra.Command) error {
	return fang.Execute(ctx, root, fang.WithoutManpage())
}

// New constructs the command tree. Configuration belongs to the application.
func New(app Application) *cobra.Command {
	var plain bool
	root := &cobra.Command{
		Use:           "shelleyctl",
		Short:         "Manage Shelley containers",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.PersistentFlags().BoolVar(&plain, "plain", false, "Disable interactive progress")

	run := func(cmd *cobra.Command, operation func(context.Context) error) error {
		if plain || !isTerminal(cmd.InOrStdin()) || !isTerminal(cmd.ErrOrStderr()) {
			return operation(cmd.Context())
		}
		return runProgress(cmd.Context(), cmd.InOrStdin(), cmd.ErrOrStderr(), cmd.Name(), operation)
	}
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show container status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return app.Status(cmd.Context(), cmd.OutOrStdout())
		},
	}, &cobra.Command{
		Use:   "build",
		Short: "Build the container image",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, app.Build)
		},
	}, &cobra.Command{
		Use:   "create NAME",
		Short: "Create a named container",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context) error { return app.Create(ctx, args[0]) })
		},
	}, &cobra.Command{
		Use:   "delete NAME",
		Short: "Delete a container and retain its data",
		Long:  "Delete the named container. Its data is retained.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context) error { return app.Delete(ctx, args[0]) })
		},
	})
	var targetImage string
	update := &cobra.Command{
		Use:   "update NAME",
		Short: "Replace an active container and retain its data",
		Long:  "Replace the named container with the current local image. Its data is retained. Build the image first. Repeat update or create to resume a failed update.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, func(ctx context.Context) error { return app.Update(ctx, args[0], targetImage) })
		},
	}
	update.Flags().StringVar(&targetImage, "to-image", "", "Explicitly change the saved image reference")
	root.AddCommand(update)
	return root
}

func isTerminal(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(file.Fd())
}
