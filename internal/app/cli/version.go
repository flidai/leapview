package cli

import (
	"fmt"

	"github.com/flidai/leapview/internal/platform/cliapi"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/spf13/cobra"
)

func versionCommand() *cobra.Command {
	format := "text"
	command := &cobra.Command{
		Use:   "version",
		Short: "Report the LeapView build identity",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if format != "text" && format != "json" {
				return cliapi.NewUsageError(fmt.Errorf("version format must be text or json"))
			}
			return buildinfo.Write(command.OutOrStdout(), "leapview", buildinfo.Current(), format == "json")
		},
	}
	command.Flags().StringVar(&format, "format", format, "output format: text or json")
	return command
}
