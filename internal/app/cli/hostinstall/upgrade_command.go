//go:build cgo && duckdb_arrow

package hostinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flidai/leapview/internal/app/cli/composectl"
	releasetransitionapp "github.com/flidai/leapview/internal/app/releasetransitionpreflightproduction"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	recoverysetpostgres "github.com/flidai/leapview/internal/recoveryset/postgres"
	"github.com/flidai/leapview/internal/release/migrationcapability"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	"github.com/flidai/leapview/internal/release/transitionrunner"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
)

// The host OCI payload is built with CGO and duckdb_arrow. The standalone
// release-archive controller is intentionally CGO-free, so its existing
// cross-platform dependency boundary remains intact.
func addUpgradeCommand(ctx context.Context, host *cobra.Command, options CommandOptions) {
	var operationID, candidateImage, targetID, controlURLPath, ownerRegistryPath, phase string
	upgrade := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade an existing host through a fenced, admitted release transition",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if os.Geteuid() != 0 {
				return fmt.Errorf("host upgrade must run as root")
			}
			if strings.TrimSpace(controlURLPath) == "" || strings.TrimSpace(ownerRegistryPath) == "" {
				return fmt.Errorf("private control URL file and trusted migration owner registry are required")
			}
			controlURLBytes, err := securefs.ReadPrivateFile(controlURLPath)
			if err != nil {
				return fmt.Errorf("read private control URL: %w", err)
			}
			controlURL := strings.TrimSpace(string(controlURLBytes))
			if controlURL == "" || strings.ContainsAny(controlURL, "\r\n") {
				return fmt.Errorf("private control URL is empty or malformed")
			}
			registryBytes, err := securefs.ReadPrivateFile(ownerRegistryPath)
			if err != nil {
				return fmt.Errorf("read trusted migration owner registry: %w", err)
			}
			var registry migrationcapability.OwnerRegistry
			if err := json.Unmarshal(registryBytes, &registry); err != nil {
				return fmt.Errorf("decode trusted migration owner registry: %w", err)
			}
			if err := registry.Validate(); err != nil {
				return fmt.Errorf("validate trusted migration owner registry: %w", err)
			}
			pool, err := pgxpool.New(ctx, controlURL)
			if err != nil {
				return fmt.Errorf("connect release control database: %w", err)
			}
			defer pool.Close()
			releases := releasepostgres.New(pool)
			capabilities, err := releasepostgres.NewMigrationCapabilityAuthority(releases, registry)
			if err != nil {
				return err
			}
			preflight, err := releasetransitionapp.NewProductionResolver(releasetransitionapp.ProductionDependencies{
				Releases: releases, Targets: deploymentpostgres.New(pool), MigrationCapabilities: capabilities,
				RecoverySets: recoverysetpostgres.New(pool),
			})
			if err != nil {
				return err
			}
			root := strings.TrimSpace(os.Getenv("LEAPVIEWCTL_ROOT"))
			if root == "" {
				root = "/opt/leapview"
			}
			progress := options.Stderr
			if progress == nil {
				progress = os.Stderr
			}
			control, err := composectl.New(composectl.Options{Root: root, DockerBin: options.DockerBin, Stdin: options.Stdin, Stdout: progress, Stderr: progress})
			if err != nil {
				return err
			}
			upgrader, err := NewUpgrader(UpgradeOptions{
				Paths: InstalledPaths(root), Operations: releasepostgres.NewTransitionRepository(pool),
				Preflight: preflight,
				Control:   upgradeComposeControl(control),
				Payload: func(ctx context.Context, image string) (map[string][]byte, error) {
					return extractCandidatePayload(ctx, options.DockerBin, image, options.Stderr)
				},
			})
			if err != nil {
				return err
			}
			result, err := upgrader.Upgrade(ctx, UpgradeRequest{OperationID: operationID, CandidateImage: candidateImage, TargetID: targetID, Phase: transitionrunner.Phase(phase)})
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	upgrade.Flags().StringVar(&phase, "phase", "", "transition runner effect: candidate-staged, candidate-activated, or candidate-restarted")
	upgrade.Flags().StringVar(&operationID, "operation-id", "", "existing fenced transition operation ID")
	upgrade.Flags().StringVar(&candidateImage, "candidate-image", "", "exact admitted candidate repository@sha256 image")
	upgrade.Flags().StringVar(&targetID, "target-id", "", "authoritative deployment target ID")
	upgrade.Flags().StringVar(&controlURLPath, "control-url-file", "", "private file containing the PostgreSQL release control authority URL")
	upgrade.Flags().StringVar(&ownerRegistryPath, "migration-owner-registry", "", "private trusted migration capability owner public-key registry")
	host.AddCommand(upgrade)
}
