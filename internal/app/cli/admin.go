package cli

import (
	"context"

	admincli "github.com/flidai/leapview/internal/admin/cli"
	adminoffline "github.com/flidai/leapview/internal/app/adminoffline"
	"github.com/flidai/leapview/internal/app/adminpostgres"
	"github.com/spf13/cobra"
)

func adminCommand(ctx context.Context, _ *rootOptions) *cobra.Command {
	return admincli.Command(ctx, adminpostgres.Operations{Operations: adminoffline.Operations{}})
}
