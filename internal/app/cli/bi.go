package cli

import (
	"context"

	dashboardcli "github.com/flidai/leapview/internal/dashboard/cli"
	workspacecli "github.com/flidai/leapview/internal/workspace/cli"
	"github.com/spf13/cobra"
)

func workspacesCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return workspacecli.WorkspacesCommand(ctx, capabilityAPIClient{})
}

func dashboardsCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	return dashboardcli.Command(ctx, capabilityAPIClient{})
}

func semanticModelsCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	return dashboardcli.SemanticModelsCommand(ctx, capabilityAPIClient{})
}

func addTargetTokenFlags(command *cobra.Command, opts *rootOptions) {
	command.Flags().StringVar(&opts.target, "target", "", "LeapView server URL")
	command.Flags().StringVar(&opts.token, "token", "", "API token")
}
