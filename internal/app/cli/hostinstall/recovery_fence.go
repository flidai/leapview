package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/providerrestore"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	recoverypostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

func validateFenceAuthority(config providerrestore.PrimaryFenceSSHConfig, systemIdentifier string) error {
	if systemIdentifier == "" {
		return fmt.Errorf("recovery authority cluster identity is unavailable")
	}
	for _, primary := range config.Primaries {
		if primary.SystemIdentifier == systemIdentifier {
			return fmt.Errorf("recovery authority must remain available independently of every original writer being fenced")
		}
	}
	return nil
}

func addRecoveryFenceCommand(ctx context.Context, host *cobra.Command) {
	var enrollmentPath, controlURLPath, setID string
	var check bool
	command := &cobra.Command{
		Use:   "fence-recovery",
		Short: "Fence enrolled original PostgreSQL hosts for an authoritative recovery frontier",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			raw, err := securefs.ReadPrivateFile(enrollmentPath)
			if err != nil {
				return fmt.Errorf("read private primary enrollment: %w", err)
			}
			var enrollment providerrestore.PrimaryFenceSSHConfig
			if strictjson.DecodeWithOptions(raw, &enrollment, strictjson.Options{MaxBytes: 65536}) != nil {
				return fmt.Errorf("invalid private primary enrollment")
			}
			fence, err := providerrestore.NewSSHPrimaryFence(enrollment)
			if err != nil {
				return err
			}
			raw, err = securefs.ReadPrivateFile(controlURLPath)
			if err != nil {
				return fmt.Errorf("read private recovery authority URL: %w", err)
			}
			connection := strings.TrimSpace(string(raw))
			if connection == "" || strings.ContainsAny(connection, "\r\n") {
				return fmt.Errorf("invalid private recovery authority URL")
			}
			pool, err := pgxpool.New(ctx, connection)
			if err != nil {
				return fmt.Errorf("open recovery authority failed")
			}
			defer pool.Close()
			var authorityID string
			if err := pool.QueryRow(ctx, `SELECT system_identifier::text FROM pg_control_system()`).Scan(&authorityID); err != nil {
				return fmt.Errorf("cannot verify recovery authority cluster identity")
			}
			if err := validateFenceAuthority(enrollment, authorityID); err != nil {
				return err
			}
			set, err := recoverypostgres.New(pool).ReadExact(ctx, setID)
			if err != nil {
				return fmt.Errorf("read authoritative recovery frontier failed")
			}
			if check {
				err = fence.Verify(ctx, set)
			} else {
				err = fence.FenceOriginals(ctx, set)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(struct {
				RecoverySetID         string `json:"recoverySetID"`
				TargetID              string `json:"targetID"`
				FrontierDigest        string `json:"frontierDigest"`
				OriginalWritersFenced bool   `json:"originalWritersFenced"`
			}{set.ID, set.Delivery.TargetID, set.FrontierDigest, true})
		},
	}
	command.Flags().StringVar(&enrollmentPath, "enrollment-file", "", "private original-host enrollment with pinned SSH trust")
	command.Flags().StringVar(&controlURLPath, "control-url-file", "", "private URL of independent recovery authority PostgreSQL")
	command.Flags().StringVar(&setID, "recovery-set-id", "", "exact authoritative RecoverySet ID")
	command.Flags().BoolVar(&check, "check", false, "only verify already established durable fences")
	for _, flag := range []string{"enrollment-file", "control-url-file", "recovery-set-id"} {
		_ = command.MarkFlagRequired(flag)
	}
	host.AddCommand(command)
}
