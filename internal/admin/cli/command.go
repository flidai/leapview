// Package cli owns command-line adapters for offline Admin operations.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/spf13/cobra"
)

const (
	defaultAuditRetentionDays         = 365
	defaultQueryRetentionDays         = 90
	defaultArchivedAgentRetentionDays = 180
	defaultAuthStateRetentionDays     = 30
)

// Options are the values accepted by offline Admin operations.
type Options struct {
	Apply                   bool
	AuditDays               int
	QueryDays               int
	ArchivedAgentDays       int
	AuthStateDays           int
	BackupOut               string
	RestoreFrom             string
	RestoreBefore           string
	ConfirmRestore          bool
	DatabaseOnly            bool
	PreflightOnly           bool
	ExternalRecovery        string
	CurrentExternalRecovery string
	ExternalEvidence        string
}

// Operations are the offline administrative use cases exposed by the CLI.
// Application composition implements this contract because it owns process
// configuration and construction of cross-capability resources.
type Operations interface {
	Initialize(context.Context, adminoffline.InitializeRequest, io.Writer) error
	AcknowledgeInitialCredentials(context.Context) error
	StorageCleanup(context.Context, adminoffline.StorageCleanupRequest, io.Writer) error
	Maintenance(context.Context, adminoffline.MaintenanceRequest, io.Writer) error
	AuditOutbox(context.Context, adminoffline.AuditOutboxRequest, io.Writer) error
	RecoveryLedgerStatus(context.Context, io.Writer) error
	Backup(context.Context, adminoffline.BackupRequest, io.Writer) error
	Restore(context.Context, adminoffline.RestoreRequest, io.Reader, io.Writer) error
	BootstrapPhysicalPool(context.Context, adminoffline.PhysicalPoolBootstrapRequest, io.Writer) error
	BootstrapQualificationLocalPhysicalPool(context.Context, io.Writer) error
	AuditDeliveryRoots(context.Context, adminoffline.DeliveryAuditRequest, io.Writer) error
	RepairDeliveryRoot(context.Context, adminoffline.DeliveryRepairRequest, io.Writer) error
}

// Command constructs the offline Admin command tree.
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

	storage := adminGroupCommand("storage", "Maintain analytical storage")
	cleanup := operationCommand(operations, "cleanup", "Reconcile serving-state snapshots and clean DuckLake storage", func(command *cobra.Command) error {
		return operations.StorageCleanup(ctx, adminoffline.StorageCleanupRequest{Apply: values.Apply}, command.OutOrStdout())
	})
	cleanup.Flags().BoolVar(&values.Apply, "apply", false, "perform destructive cleanup instead of dry-run")
	storage.AddCommand(cleanup)

	maintenance := operationCommand(operations, "maintenance", "Prune bounded operational history", func(command *cobra.Command) error {
		return operations.Maintenance(ctx, adminoffline.MaintenanceRequest{
			Apply: values.Apply, AuditDays: values.AuditDays, QueryDays: values.QueryDays,
			ArchivedAgentDays: values.ArchivedAgentDays, AuthStateDays: values.AuthStateDays,
		}, command.OutOrStdout())
	})
	maintenance.Flags().BoolVar(&values.Apply, "apply", false, "delete rows instead of dry-run")
	maintenance.Flags().IntVar(&values.AuditDays, "audit-days", defaultAuditRetentionDays, "audit event retention in days; 0 disables audit pruning")
	maintenance.Flags().IntVar(&values.QueryDays, "query-days", defaultQueryRetentionDays, "query event retention in days; 0 disables query pruning")
	maintenance.Flags().IntVar(&values.ArchivedAgentDays, "archived-agent-days", defaultArchivedAgentRetentionDays, "archived agent conversation retention in days; 0 disables archived conversation pruning")
	maintenance.Flags().IntVar(&values.AuthStateDays, "auth-state-days", defaultAuthStateRetentionDays, "expired or revoked auth state retention in days; 0 disables auth-state pruning")

	var requeueAuditEvent, requeueAuditState, requeueAuditFailureCode, requeueAuditPayloadDigest string
	var requeueAuditAttempts int
	auditOutbox := operationCommand(operations, "audit-outbox", "Inspect or recover the durable audit handoff", func(command *cobra.Command) error {
		request := adminoffline.AuditOutboxRequest{
			RequeueEventID: requeueAuditEvent, ExpectedState: requeueAuditState,
			ExpectedFailureCode: requeueAuditFailureCode, ExpectedPayloadDigest: requeueAuditPayloadDigest,
			Apply: values.Apply,
		}
		if command.Flags().Changed("attempt-count") || command.Flags().Changed("expected-attempts") {
			request.ExpectedAttempts = &requeueAuditAttempts
		}
		return operations.AuditOutbox(ctx, request, command.OutOrStdout())
	})
	auditOutbox.Flags().StringVar(&requeueAuditEvent, "requeue-event", "", "exact poison or quarantined audit event identity to requeue")
	auditOutbox.Flags().StringVar(&requeueAuditState, "expected-state", "", "expected terminal state (poison or quarantined) from inspection")
	auditOutbox.Flags().StringVar(&requeueAuditState, "state", "", "alias for --expected-state")
	auditOutbox.Flags().IntVar(&requeueAuditAttempts, "attempt-count", 0, "expected terminal attempt count from inspection")
	auditOutbox.Flags().IntVar(&requeueAuditAttempts, "expected-attempts", 0, "alias for --attempt-count")
	auditOutbox.Flags().StringVar(&requeueAuditFailureCode, "failure-code", "", "expected safe failure code from inspection")
	auditOutbox.Flags().StringVar(&requeueAuditPayloadDigest, "payload-digest", "", "expected immutable payload digest from inspection")
	auditOutbox.Flags().BoolVar(&values.Apply, "apply", false, "apply the exact requeue operation")

	backup := operationCommand(operations, "backup", "Create a consistent LeapView instance backup", func(command *cobra.Command) error {
		var recoveryPoints []adminoffline.ExternalRecoveryPoint
		if err := readOptionalJSONFile(values.ExternalRecovery, &recoveryPoints); err != nil {
			return fmt.Errorf("read external recovery points: %w", err)
		}
		return operations.Backup(ctx, adminoffline.BackupRequest{
			Out: values.BackupOut, DatabaseOnly: values.DatabaseOnly, ExternalRecoveryPoints: recoveryPoints,
		}, command.OutOrStdout())
	})
	backup.Flags().StringVar(&values.BackupOut, "out", "", "backup archive output path")
	backup.Flags().BoolVar(&values.DatabaseOnly, "database-only", false, "backup only the platform SQLite database")
	backup.Flags().StringVar(&values.ExternalRecovery, "external-recovery-points", "", "JSON file containing exact external recovery points (never credentials)")

	restore := operationCommand(operations, "restore", "Restore LeapView from a validated instance backup", func(command *cobra.Command) error {
		externalEvidence := map[string]string{}
		if err := readOptionalJSONFile(values.ExternalEvidence, &externalEvidence); err != nil {
			return fmt.Errorf("read external recovery evidence: %w", err)
		}
		var currentRecoveryPoints []adminoffline.ExternalRecoveryPoint
		if err := readOptionalJSONFile(values.CurrentExternalRecovery, &currentRecoveryPoints); err != nil {
			return fmt.Errorf("read current external recovery points: %w", err)
		}
		return operations.Restore(ctx, adminoffline.RestoreRequest{
			From: values.RestoreFrom, CurrentBackup: values.RestoreBefore,
			Confirm: values.ConfirmRestore, DatabaseOnly: values.DatabaseOnly, PreflightOnly: values.PreflightOnly,
			ExternalEvidence: externalEvidence, CurrentExternalRecoveryPoints: currentRecoveryPoints,
		}, command.InOrStdin(), command.OutOrStdout())
	})
	restore.Flags().StringVar(&values.RestoreFrom, "from", "", "backup archive path to restore")
	restore.Flags().StringVar(&values.RestoreBefore, "current-out", "", "path for a backup of the current instance before replacement; - creates and discards a validated temporary checkpoint")
	restore.Flags().BoolVar(&values.ConfirmRestore, "confirm", false, "confirm replacement of the configured LeapView instance")
	restore.Flags().BoolVar(&values.DatabaseOnly, "database-only", false, "restore only the platform SQLite database")
	restore.Flags().BoolVar(&values.PreflightOnly, "preflight-only", false, "emit the read-only restore plan without creating a checkpoint or replacing target state")
	restore.Flags().StringVar(&values.ExternalEvidence, "external-evidence", "", "JSON map of evidence keys to verified external recovery points")
	restore.Flags().StringVar(&values.CurrentExternalRecovery, "current-external-recovery-points", "", "JSON file containing exact recovery points for the pre-restore safety checkpoint")

	recoveryGroup := adminGroupCommand("recovery", "Inspect durable recovery qualification evidence")
	recoveryStatus := operationCommand(operations, "status", "Show due, stale, failed, and publication state from the recovery ledger", func(command *cobra.Command) error {
		return operations.RecoveryLedgerStatus(ctx, command.OutOrStdout())
	})
	recoveryStatus.Args = cobra.NoArgs
	recoveryStatus.Annotations = map[string]string{
		"leapview.dev/effect":       "read",
		"leapview.dev/confirmation": "never",
	}
	recoveryGroup.AddCommand(recoveryStatus)

	parent.AddCommand(initialize, storage, maintenance, auditOutbox, backup, restore, recoveryGroup)
	delivery := deliveryPoolCommand(ctx, operations)
	delivery.AddCommand(deliveryAuditCommand(ctx, operations))
	delivery.AddCommand(deliveryRepairCommand(ctx, operations))
	parent.AddCommand(delivery)
	return parent
}

func readOptionalJSONFile(path string, target any) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON file contains multiple values")
		}
		return err
	}
	return nil
}

func deliveryAuditCommand(ctx context.Context, operations Operations) *cobra.Command {
	var poolID string
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Verify every durable delivery root and immutable closure",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			if poolID == "" {
				return fmt.Errorf("--pool-id is required")
			}
			return operations.AuditDeliveryRoots(ctx, adminoffline.DeliveryAuditRequest{PhysicalPoolID: poolID}, command.OutOrStdout())
		},
	}
	audit.Flags().StringVar(&poolID, "pool-id", "", "durable physical-pool identity")
	return audit
}

func deliveryRepairCommand(ctx context.Context, operations Operations) *cobra.Command {
	var poolID, kind, sourceID, candidateID, generationID, leaseID, catalogDigest, objectKey, status, createdAt, expiresAt string
	var apply bool
	repair := &cobra.Command{
		Use:   "repair",
		Short: "Verify and quarantine one exact delivery root",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if operations == nil {
				return fmt.Errorf("Admin CLI operations are required")
			}
			if poolID == "" || kind == "" || sourceID == "" || catalogDigest == "" || objectKey == "" || createdAt == "" {
				return fmt.Errorf("--pool-id, --kind, --source-id, --catalog-digest, --object-key, and --created-at are required")
			}
			created, err := time.Parse(time.RFC3339Nano, createdAt)
			if err != nil || created.Location() != time.UTC {
				return fmt.Errorf("--created-at must be an RFC3339 UTC timestamp")
			}
			var expires time.Time
			if expiresAt != "" {
				expires, err = time.Parse(time.RFC3339Nano, expiresAt)
				if err != nil || expires.Location() != time.UTC {
					return fmt.Errorf("--expires-at must be an RFC3339 UTC timestamp")
				}
			}
			return operations.RepairDeliveryRoot(ctx, adminoffline.DeliveryRepairRequest{
				Root:   deployment.DeliveryRoot{PhysicalPoolID: poolID, Kind: kind, SourceID: sourceID, CandidateID: candidateID, GenerationID: generationID, LeaseID: leaseID, CatalogDigest: catalogDigest, ObjectKey: objectKey, Status: status, CreatedAt: created, ExpiresAt: expires},
				Action: "quarantine", Apply: apply,
			}, command.OutOrStdout())
		},
	}
	repair.Flags().StringVar(&poolID, "pool-id", "", "durable physical-pool identity")
	repair.Flags().StringVar(&kind, "kind", "", "durable root kind (build, candidate, published, rollback, lease, retained, quarantined)")
	repair.Flags().StringVar(&sourceID, "source-id", "", "durable root source identity")
	repair.Flags().StringVar(&candidateID, "candidate-id", "", "candidate identity when the root carries one")
	repair.Flags().StringVar(&generationID, "generation-id", "", "generation identity when the root carries one")
	repair.Flags().StringVar(&leaseID, "lease-id", "", "query lease identity when the root carries one")
	repair.Flags().StringVar(&catalogDigest, "catalog-digest", "", "immutable catalog digest")
	repair.Flags().StringVar(&objectKey, "object-key", "", "immutable catalog object key")
	repair.Flags().StringVar(&status, "status", "active", "durable root status")
	repair.Flags().StringVar(&createdAt, "created-at", "", "durable root creation timestamp (RFC3339 UTC)")
	repair.Flags().StringVar(&expiresAt, "expires-at", "", "durable root expiry timestamp (RFC3339 UTC), when present")
	repair.Flags().BoolVar(&apply, "apply", false, "persist the bounded quarantine action after all verification")
	return repair
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
