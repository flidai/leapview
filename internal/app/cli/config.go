package cli

import (
	"fmt"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/spf13/cobra"
)

func configCommand() *cobra.Command {
	var production bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and validate process-global configuration",
	}
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the active server environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			cfg.Production = cfg.Production || production
			if err := cfg.Validate(config.ProfileServe); err != nil {
				return err
			}
			if cfg.Production {
				if err := cfg.ValidatePostgresProduction(); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return err
		},
	}
	validate.Flags().BoolVar(&production, "production", false, "validate production requirements even when LEAPVIEW_PRODUCTION is unset")
	cmd.AddCommand(validate)
	return cmd
}
