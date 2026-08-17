package cli

import (
	"context"

	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

func searchCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return projectcli.SearchCommand(ctx, capabilityAPIClient{})
}
