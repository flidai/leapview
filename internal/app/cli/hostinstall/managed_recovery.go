package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	"github.com/flidai/leapview/internal/app/providerrestore"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/refresh/recovery"
	"github.com/spf13/cobra"
)

func addManagedRecoveryCommand(host *cobra.Command) {
	var path string
	command := &cobra.Command{Use: "restore-managed", Short: "Restore one authoritative managed-local recovery frontier with enrolled original writers fenced", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			// PostgreSQL refuses a root postmaster. The explicitly provisioned
			// recovery owner must own private provider inputs and staging storage.
			if os.Geteuid() == 0 {
				return errors.New("managed recovery must run under the provisioned unprivileged PostgreSQL/recovery owner")
			}
			ctx, cancel := context.WithTimeout(command.Context(), 2*time.Hour)
			defer cancel()
			input, err := managedrecovery.ReadManagedInput(path)
			if err != nil {
				return err
			}
			configuration, err := input.Configuration(ctx)
			if err != nil {
				return err
			}
			lock, err := instancelock.Acquire(configuration.InstanceHome)
			if err != nil {
				return errors.New("managed recovery requires exclusive instance ownership")
			}
			defer lock.Release()
			pool, err := managedrecovery.OpenManagedAuthority(ctx, input.Authority, input.PrimaryFence.Primaries)
			if err != nil {
				return err
			}
			defer pool.Close()
			ledger, sets := refreshpostgres.NewRecoveryLedger(pool), recoverypostgres.New(pool)
			coordinator, err := managedrecovery.NewManaged(ctx, configuration, managedrecovery.ManagedAuthorities{Ledger: ledger, Sets: sets})
			if err != nil {
				return err
			}
			report, err := managedrecovery.RunManagedOccurrence(ctx, ledger, input.OccurrenceID, input.Validator, func(ctx context.Context, fence recovery.Fence) (providerrestore.Report, error) {
				return coordinator.Run(ctx, providerrestore.Request{OccurrenceID: input.OccurrenceID, Fence: fence, RecoverySetID: input.RecoverySetID, TargetID: configuration.Credentials.TargetID, ValidationAttemptID: input.ValidationAttemptID, Validator: input.Validator, Publisher: input.Publisher})
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(report)
		}}
	command.Flags().StringVar(&path, "input", "", "bounded private managed-local provider/authority input document")
	_ = command.MarkFlagRequired("input")
	host.AddCommand(command)
}
