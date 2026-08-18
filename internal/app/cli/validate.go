package cli

import (
	"context"
	"net/http"

	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

func validateCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return projectcli.ValidateCommand(ctx)
}

func planCommand(ctx context.Context, opts *rootOptions) *cobra.Command {
	client := capabilityAPIClient{
		httpClient:        authoringRefreshingHTTPClient(http.DefaultClient),
		validateAuthoring: true,
	}
	return projectcli.PlanCommand(ctx, projectDeliveryPlanOperations{client: client, remotes: projectDevRemoteFactory{client: client}, checkpoints: projectcli.NewCandidateCheckpointStore(candidateCheckpointPath())})
}

func schemaCommand(_ *rootOptions) *cobra.Command {
	return projectcli.SchemaCommand()
}

func runSchemaExport(opts *rootOptions) error {
	return projectcli.ExportSchema(opts.schemaFormat, opts.schemaOut)
}
