package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	"github.com/spf13/cobra"
)

func addManagedRecoveryEnrollmentCommand(host *cobra.Command) {
	var path string
	command := &cobra.Command{Use: "enroll-managed-recovery", Short: "Retain one prepared source checkpoint and pending restore intent in an independent authority", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			input, err := managedrecovery.ReadManagedEnrollmentInput(path)
			if err != nil {
				return err
			}
			lock, err := instancelock.Acquire(input.Request.InstanceHome)
			if err != nil {
				return errors.New("managed enrollment requires exclusive source instance ownership")
			}
			defer lock.Release()
			ctx, cancel := context.WithTimeout(command.Context(), 2*time.Minute)
			defer cancel()
			receipt, err := input.Enroll(ctx)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(receipt)
		}}
	command.Flags().StringVar(&path, "input", "", "bounded private source, independent authority and receipt input")
	_ = command.MarkFlagRequired("input")
	host.AddCommand(command)
}
