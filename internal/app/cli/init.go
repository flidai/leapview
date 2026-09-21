package cli

import (
	"fmt"

	"github.com/flidai/leapview/internal/app/cli/projectinit"
	"github.com/spf13/cobra"
)

func initCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init <directory>",
		Short: "Create a portable analytics project with reproducible sample data",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			root, err := projectinit.Initialize(args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Initialized LeapView analytics project at %q\nNext: change into that directory and run leapview dev\n", root)
			return err
		},
	}
}
