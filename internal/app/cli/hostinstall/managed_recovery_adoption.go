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

func addManagedRecoveryAdoptionCommand(host *cobra.Command) {
	var path, replacementPath, output string
	command := &cobra.Command{Use: "adopt-managed-recovery", Short: "Promote an exact restored PostgreSQL service and atomically adopt its independent publication with ingress closed", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if runtime.GOOS != "linux" || os.Geteuid() == 0 {
				return errors.New("managed recovery adoption requires the provisioned unprivileged recovery owner on Linux")
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
			replacement, err := managedrecovery.ReadManagedAdoptionInput(replacementPath)
			if err != nil {
				return err
			}
			if err := managedrecovery.ValidateManagedQualificationOutput(output, configuration); err != nil {
				return err
			}
			lock, err := instancelock.Acquire(configuration.InstanceHome)
			if err != nil {
				return errors.New("managed adoption requires exclusive instance ownership")
			}
			defer lock.Release()
			pool, err := managedrecovery.OpenManagedAuthority(ctx, input.Authority, input.PrimaryFence.Primaries)
			if err != nil {
				return err
			}
			defer pool.Close()
			evidence, err := managedrecovery.AdoptManagedRecovery(ctx, input, configuration, managedrecovery.ManagedAuthorities{AuthoritySystemIdentifier: input.Authority.SystemIdentifier, Ledger: refreshpostgres.NewRecoveryLedger(pool), Sets: recoverypostgres.New(pool)}, replacement)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			if securefs.WritePrivateFileAtomicOnce(output, encoded, 0600) != nil {
				return errors.New("managed adoption evidence cannot be retained; retry with a new private output")
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(evidence)
		}}
	command.Flags().StringVar(&path, "input", "", "same exact private completed managed-local recovery input")
	command.Flags().StringVar(&replacementPath, "replacement", "", "private explicit replacement control maintenance credential input")
	command.Flags().StringVar(&output, "output", "", "new private bounded adoption evidence path")
	_ = command.MarkFlagRequired("input")
	_ = command.MarkFlagRequired("replacement")
	_ = command.MarkFlagRequired("output")
	host.AddCommand(command)
}
