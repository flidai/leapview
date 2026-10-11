package hostinstall

import (
	"context"
	"encoding/json"
	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/spf13/cobra"
	"time"
)

func addManagedRecoveryAuthorityCommand(host *cobra.Command) {
	var path string
	command := &cobra.Command{Use: "init-managed-recovery-authority", Short: "Initialize one empty independent PostgreSQL recovery authority with dedicated roles", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		ctx, cancel := context.WithTimeout(command.Context(), 2*time.Minute)
		defer cancel()
		input, err := managedrecovery.ReadManagedAuthorityInitializationInput(path)
		if err != nil {
			return err
		}
		receipt, err := input.Initialize(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(command.OutOrStdout()).Encode(receipt)
	}}
	command.Flags().StringVar(&path, "input", "", "private exact authority bootstrap/operator input document")
	_ = command.MarkFlagRequired("input")
	host.AddCommand(command)
}
