package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nnutter/shelleyctl/internal/cli"
	"github.com/nnutter/shelleyctl/internal/instance"
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts, err := instance.Defaults()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	app, err := instance.New(&opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	root := cli.New(app)
	root.PersistentFlags().StringVar(&opts.StateDir, "state-dir", opts.StateDir, "Absolute directory for durable instance state and homes")
	root.PersistentFlags().StringVar(&opts.Image, "image", opts.Image, "Image to build or use for new instances")
	return cli.Execute(ctx, root)
}
