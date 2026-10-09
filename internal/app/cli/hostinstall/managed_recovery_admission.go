package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/spf13/cobra"
)

func addManagedRecoveryAdmissionCommand(host *cobra.Command) {
	var path, output string
	command := &cobra.Command{Use: "admit-managed-recovery", Short: "Reverify a completed managed-local frontier before separate host activation", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("managed recovery admission requires the provisioned unprivileged recovery owner")
			}
			if err := managedrecovery.InvalidateManagedAdmissionEvidence(output); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(command.Context(), 20*time.Minute)
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
				return errors.New("managed admission requires exclusive instance ownership")
			}
			defer lock.Release()
			if input.Authority.SystemIdentifier != input.Enrollment.Request.AuthoritySystemID {
				return errors.New("managed admission authority differs from enrollment")
			}
			pool, err := managedrecovery.OpenManagedAuthority(ctx, input.Authority, input.PrimaryFence.Primaries)
			if err != nil {
				return err
			}
			defer pool.Close()
			evidence, err := managedrecovery.AdmitManagedRecovery(ctx, configuration, managedrecovery.ManagedAuthorities{AuthoritySystemIdentifier: input.Authority.SystemIdentifier, Ledger: refreshpostgres.NewRecoveryLedger(pool), Sets: recoverypostgres.New(pool)})
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			if err := securefs.WritePrivateFileAtomicOnce(output, encoded, 0600); err != nil {
				return errors.New("managed admission output cannot be retained")
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(evidence)
		}}
	command.Flags().StringVar(&path, "input", "", "same bounded private managed-local restore input")
	command.Flags().StringVar(&output, "output", "", "new private admission evidence path")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("output")
	host.AddCommand(command)
}
