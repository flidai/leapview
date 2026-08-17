package cli

import (
	"context"

	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

func semanticModelOssieCommand(ctx context.Context) *cobra.Command {
	return projectcli.OssieCommand(ctx)
}
