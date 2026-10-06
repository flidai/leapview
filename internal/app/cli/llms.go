package cli

import (
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/clidoc"
	"github.com/spf13/cobra"
)

func writeAgentGuidance(command *cobra.Command) error {
	manifest, err := clidoc.Build(command.Root())
	if err != nil {
		return err
	}
	return clidoc.WriteAgentGuide(command.OutOrStdout(), manifest, buildinfo.Current().Version)
}
