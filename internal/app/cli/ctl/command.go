// Package ctl constructs the deployed controller command tree without executing it.
package ctl

import (
	"context"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	"github.com/flidai/leapview/internal/app/cli/hostinstall"
	"github.com/spf13/cobra"
)

// NewCommand is shared by the executable and the audit catalog so host and
// Compose command registration have one composition root.
func NewCommand(ctx context.Context, options composectl.Options) (*cobra.Command, error) {
	controller, err := composectl.New(options)
	if err != nil {
		return nil, err
	}
	command := composectl.Command(ctx, controller)
	command.AddCommand(hostinstall.Command(ctx, hostinstall.CommandOptions{
		Root: options.Root, DockerBin: options.DockerBin,
		Stdin: options.Stdin, Stdout: options.Stdout, Stderr: options.Stderr,
	}))
	return command, nil
}
