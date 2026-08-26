package module

import (
	"context"
	"database/sql"
	"time"

	refreshrecovery "github.com/flidai/leapview/internal/refresh/recovery"
	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	StatusPending   = refreshrecovery.StatusPending
	StatusClaimed   = refreshrecovery.StatusClaimed
	StatusRunning   = refreshrecovery.StatusRunning
	StatusSucceeded = refreshrecovery.StatusSucceeded
	StatusFailed    = refreshrecovery.StatusFailed
	StatusCanceled  = refreshrecovery.StatusCanceled
	StatusExpired   = refreshrecovery.StatusExpired

	OperationBackup   = refreshrecovery.OperationBackup
	OperationRestore  = refreshrecovery.OperationRestore
	OperationUpgrade  = refreshrecovery.OperationUpgrade
	OperationRollback = refreshrecovery.OperationRollback
)

var (
	ErrConflict = refreshrecovery.ErrConflict
	ErrFenced   = refreshrecovery.ErrFenced
)

type EnqueueInput = refreshrecovery.EnqueueInput
type RecoveryDefinition = refreshrecovery.Definition
type Fence = refreshrecovery.Fence
type EvidenceReference = refreshrecovery.EvidenceReference
type Result = refreshrecovery.Result
type Occurrence = refreshrecovery.Occurrence
type Attempt = refreshrecovery.Attempt
type EvidenceAttempt = refreshrecovery.EvidenceAttempt
type ClaimInput = refreshrecovery.ClaimInput
type RetentionPolicy = refreshrecovery.RetentionPolicy
type RetentionResult = refreshrecovery.RetentionResult
type StatusSnapshot = refreshrecovery.StatusSnapshot
type Metric = refreshrecovery.Metric
type RecoveryRepository = refreshrecovery.Repository
type RecoveryLifecycle = refreshrecovery.Lifecycle
type RecoveryDefinitionProvider = refreshrecovery.DefinitionProvider
type RecoveryScenarioAdapter = refreshrecovery.ScenarioAdapter
type RecoveryScenarioAdapterFunc = refreshrecovery.ScenarioAdapterFunc

const (
	RecoveryPhaseRestore   = refreshrecovery.PhaseRestore
	RecoveryPhaseReadiness = refreshrecovery.PhaseReadiness
	RecoveryPhaseStarted   = refreshrecovery.PhaseStarted
	RecoveryPhaseCompleted = refreshrecovery.PhaseCompleted
)

func RecordRecoveryQualificationPhase(ctx context.Context, phase, event string) error {
	return refreshrecovery.RecordQualificationPhase(ctx, phase, event)
}

type RecoveryScenarioOutcome = refreshrecovery.ScenarioOutcome
type RecoveryEvidenceArtifact = refreshrecovery.EvidenceArtifact
type RecoveryEvidencePublisher = refreshrecovery.EvidencePublisher
type RecoveryFileEvidencePublisher = refreshrecovery.FileEvidencePublisher
type ProductionRecoveryQualificationConfig = refreshrecovery.ProductionQualificationConfig
type RecoveryQualificationCommand = refreshrecovery.QualificationCommand
type RecoveryStorageQualificationEvidence = refreshrecovery.StorageQualificationEvidence
type RecoveryStorageEvidenceProvider = refreshrecovery.StorageEvidenceProvider

const (
	EvidenceTransitionQualification = refreshrecovery.EvidenceTransitionQualification
	EvidenceBackupManifestV2        = refreshrecovery.EvidenceBackupManifestV2
	EvidenceRestorePreflight        = refreshrecovery.EvidenceRestorePreflight
)

func NewRecoveryRepository(database *sql.DB) RecoveryRepository {
	return newSQLiteRepository(database)
}

func NewRecoveryMetricsCollector(database *sql.DB, clock Clock) prometheus.Collector {
	return refreshrecovery.NewMetricsCollector(NewRecoveryRepository(database), clock)
}

func NewRecoveryLifecycle(database *sql.DB, lifecycle RecoveryLifecycle) *RecoveryLifecycle {
	lifecycle.Repository = NewRecoveryRepository(database)
	return &lifecycle
}

func NewProductionRecoveryLifecycle(config ProductionRecoveryQualificationConfig) *RecoveryLifecycle {
	return &RecoveryLifecycle{
		Definitions: config.ProductionDefinitions, Adapters: config.ProductionAdapters(),
		Publisher: RecoveryFileEvidencePublisher{Root: config.EvidenceRoot},
		WorkerID:  "production-recovery-worker", Actor: "scheduled-qualification",
		Lease: 15 * time.Minute, BatchSize: 4, ComplianceWindow: 90 * 24 * time.Hour,
		EvidenceRoot: config.EvidenceRoot,
	}
}

func RedactFailure(err error) string {
	return refreshrecovery.RedactFailure(err)
}

func newSQLiteRepository(database *sql.DB) *refreshsqlite.Repository {
	return refreshsqlite.NewRepository(database)
}
