package cli

import (
	"context"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/spf13/cobra"
)

func adminCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	command := admincli.Command(ctx, adminpostgres.New(adminpostgres.Dependencies{}))
	command.AddCommand(accessTransitionInventoryCommand(ctx))
	command.AddCommand(accessTransitionCommand(ctx))
	return command
}
