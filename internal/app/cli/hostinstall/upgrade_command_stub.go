//go:build !cgo || !duckdb_arrow

package hostinstall

import (
	"context"

	"github.com/spf13/cobra"
)

func addUpgradeCommand(context.Context, *cobra.Command, CommandOptions) {}
