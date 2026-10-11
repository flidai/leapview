package hostinstall

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/flidai/leapview/internal/app/managedrecovery"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/spf13/cobra"
)

func addManagedRecoveryQualificationCommand(host *cobra.Command) {
	var path, output string
	command := &cobra.Command{Use: "qualify-managed-recovery", Short: "Exercise a fresh managed restore, completed retry and stopped-provider admission", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if runtime.GOOS != "linux" || os.Geteuid() == 0 {
				return errors.New("managed qualification requires the provisioned unprivileged recovery owner on Linux")
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
			if err := managedrecovery.ValidateManagedQualificationOutput(output, configuration); err != nil {
				return err
			}
			lock, err := instancelock.Acquire(configuration.InstanceHome)
			if err != nil {
				return errors.New("managed qualification requires exclusive instance ownership")
			}
			defer lock.Release()
			if input.Authority.SystemIdentifier != input.Enrollment.Request.AuthoritySystemID {
				return errors.New("managed qualification authority differs from enrollment")
			}
			pool, err := managedrecovery.OpenManagedAuthority(ctx, input.Authority, input.PrimaryFence.Primaries)
			if err != nil {
				return err
			}
			defer pool.Close()
			evidence, err := managedrecovery.QualifyManagedRecovery(ctx, input, configuration, managedrecovery.ManagedAuthorities{AuthoritySystemIdentifier: input.Authority.SystemIdentifier, Ledger: refreshpostgres.NewRecoveryLedger(pool), Sets: recoverypostgres.New(pool)})
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			if err := securefs.WritePrivateFileAtomicOnce(output, encoded, 0600); err != nil {
				return errors.New("managed qualification evidence cannot be retained")
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(evidence)
		}}
	command.Flags().StringVar(&path, "input", "", "existing strict private managed-local restore input")
	command.Flags().StringVar(&output, "output", "", "new private bounded qualification evidence path")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("output")
	host.AddCommand(command)
}
