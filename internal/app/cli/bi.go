package cli

import (
	"context"

	dashboardcli "github.com/flidai/leapview/internal/dashboard/cli"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

func dashboardsCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	command := dashboardcli.Command(ctx, capabilityAPIClient{})
	command.AddCommand(projectcli.DashboardExportCommand(ctx))
	return command
}

func semanticModelsCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	return dashboardcli.SemanticModelsCommand(ctx, capabilityAPIClient{})
}

func addTargetTokenFlags(command *cobra.Command, opts *rootOptions) {
	command.Flags().StringVar(&opts.target, "target", "", "LeapView server URL")
	command.Flags().StringVar(&opts.token, "token", "", "API token")
}
