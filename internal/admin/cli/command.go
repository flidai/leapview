// Package cli owns command-line adapters for Admin operations.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/spf13/cobra"
)

const (
	defaultAuditRetentionDays         = 365
	defaultQueryRetentionDays         = 90
	defaultArchivedAgentRetentionDays = 180
	defaultAuthStateRetentionDays     = 30
)

// MaintenanceRequest contains the bounded retention windows accepted by the
// native PostgreSQL maintenance command. It lives with the command adapter so
// no offline/Admin use-case package needs to expose a SQLite retention port.
type MaintenanceRequest struct {
	Apply             bool
	AuditDays         int
	QueryDays         int
	ArchivedAgentDays int
	AuthStateDays     int
}

// Options are the values accepted by Admin operations.
type Options struct {
	Apply             bool
	AuditDays         int
	QueryDays         int
	ArchivedAgentDays int
	AuthStateDays     int
}

// Operations are the administrative use cases exposed by the CLI.
// Application composition implements this contract because it owns process
// configuration and construction of cross-capability resources.
type Operations interface {
	Initialize(context.Context, adminoffline.InitializeRequest, io.Writer) error
	AcknowledgeInitialCredentials(context.Context) error
	Maintenance(context.Context, MaintenanceRequest, io.Writer) error
	BootstrapPhysicalPool(context.Context, adminoffline.PhysicalPoolBootstrapRequest, io.Writer) error
	BootstrapQualificationLocalPhysicalPool(context.Context, io.Writer) error
}

// Command constructs the Admin command tree.
func Command(ctx context.Context, operations Operations) *cobra.Command {
	values := Options{}
	parent := adminGroupCommand("admin", "Administrative utilities")
	parent.SilenceErrors = true
	parent.SilenceUsage = true

	initializeFormat := "json"
	acknowledgeCredentials := false
	initialize := &cobra.Command{
		Use:   "initialize",
		Short: "Initialize one instance administrator and publisher credential",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			if acknowledgeCredentials {
				return operations.AcknowledgeInitialCredentials(ctx)
			}
			return operations.Initialize(ctx, adminoffline.InitializeRequest{Format: initializeFormat}, command.OutOrStdout())
		},
	}
	initialize.Flags().StringVar(&initializeFormat, "format", "json", "output format (json)")
	initialize.Flags().BoolVar(&acknowledgeCredentials, "acknowledge-credentials", false, "remove the recoverable initialization credential bundle after it has been stored safely")

	maintenance := operationCommand(operations, "maintenance", "Prune bounded operational history", func(command *cobra.Command) error {
		return operations.Maintenance(ctx, MaintenanceRequest{
			Apply: values.Apply, AuditDays: values.AuditDays, QueryDays: values.QueryDays,
			ArchivedAgentDays: values.ArchivedAgentDays, AuthStateDays: values.AuthStateDays,
		}, command.OutOrStdout())
	})
	maintenance.Flags().BoolVar(&values.Apply, "apply", false, "delete rows instead of dry-run")
	maintenance.Flags().IntVar(&values.AuditDays, "audit-days", defaultAuditRetentionDays, "audit event retention in days; 0 disables audit pruning")
	maintenance.Flags().IntVar(&values.QueryDays, "query-days", defaultQueryRetentionDays, "query event retention in days; 0 disables query pruning")
	maintenance.Flags().IntVar(&values.ArchivedAgentDays, "archived-agent-days", defaultArchivedAgentRetentionDays, "archived agent conversation retention in days; 0 disables archived conversation pruning")
	maintenance.Flags().IntVar(&values.AuthStateDays, "auth-state-days", defaultAuthStateRetentionDays, "expired or revoked auth state retention in days; 0 disables auth-state pruning")

	parent.AddCommand(initialize, maintenance)
	delivery := deliveryPoolCommand(ctx, operations)
	parent.AddCommand(delivery)
	return parent
}

func deliveryPoolCommand(ctx context.Context, operations Operations) *cobra.Command {
	var poolPath, evidencePath string
	var apply bool
	bootstrap := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create and admit one operator-controlled delivery physical pool",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			if poolPath == "" || evidencePath == "" {
				return fmt.Errorf("--pool and --evidence are required")
			}
			poolBytes, err := os.ReadFile(poolPath)
			if err != nil {
				return fmt.Errorf("read pool contract: %w", err)
			}
			evidenceBytes, err := os.ReadFile(evidencePath)
			if err != nil {
				return fmt.Errorf("read conformance evidence: %w", err)
			}
			var identity physicalpool.PoolIdentity
			if err := json.Unmarshal(poolBytes, &identity); err != nil {
				return fmt.Errorf("decode pool contract: %w", err)
			}
			evidence, err := physicalpool.UnmarshalEvidenceArtifact(evidenceBytes)
			if err != nil {
				return fmt.Errorf("decode conformance evidence: %w", err)
			}
			return operations.BootstrapPhysicalPool(ctx, adminoffline.PhysicalPoolBootstrapRequest{Pool: identity, Evidence: evidence, Apply: apply}, command.OutOrStdout())
		},
	}
	bootstrap.Flags().StringVar(&poolPath, "pool", "", "path to non-secret physical-pool identity JSON")
	bootstrap.Flags().StringVar(&evidencePath, "evidence", "", "path to machine-readable shared-pool conformance evidence JSON")
	bootstrap.Flags().BoolVar(&apply, "apply", false, "persist the pool and admission; without this flag only validate and print digests")
	var qualificationApply bool
	qualificationBootstrap := &cobra.Command{
		Use:    "qualify",
		Short:  "Admit the isolated installed-candidate qualification pool",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			if !qualificationApply {
				return fmt.Errorf("--apply is required")
			}
			return operations.BootstrapQualificationLocalPhysicalPool(ctx, command.OutOrStdout())
		},
	}
	qualificationBootstrap.Flags().BoolVar(&qualificationApply, "apply", false, "run conformance and persist the isolated qualification admission")
	pool := adminGroupCommand("pool", "Manage the delivery physical-pool admission")
	pool.AddCommand(bootstrap, qualificationBootstrap)
	delivery := adminGroupCommand("delivery", "Manage plan-driven delivery state")
	delivery.AddCommand(pool)
	return delivery
}

func adminGroupCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
		Annotations: map[string]string{
			"leapview.dev/effect":       "read",
			"leapview.dev/confirmation": "never",
			"leapview.dev/help-group":   "true",
		},
	}
}

func operationCommand(operations Operations, use, short string, run func(*cobra.Command) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			return run(command)
		},
	}
}
