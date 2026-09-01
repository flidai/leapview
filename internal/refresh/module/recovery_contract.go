package module

import (
	"context"
	"database/sql"

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

const (
	EvidenceTransitionQualification = refreshrecovery.EvidenceTransitionQualification
)

// NewSQLiteRecoveryRepository constructs the local SQLite recovery ledger.
// Production composition injects a capability-owned repository instead of
// passing a database handle through this module boundary.
func NewSQLiteRecoveryRepository(database *sql.DB) RecoveryRepository {
	if database == nil {
		return nil
	}
	return refreshsqlite.NewRepository(database)
}

// NewSQLiteRecoveryMetricsCollector constructs recovery metrics for the local
// SQLite adapter. Native production composition owns its metrics source.
func NewSQLiteRecoveryMetricsCollector(database *sql.DB, clock Clock) prometheus.Collector {
	return refreshrecovery.NewMetricsCollector(NewSQLiteRecoveryRepository(database), clock)
}

// NewSQLiteRecoveryLifecycle binds a local SQLite ledger to a lifecycle
// definition. Production callers must inject a repository into the lifecycle.
func NewSQLiteRecoveryLifecycle(database *sql.DB, lifecycle RecoveryLifecycle) *RecoveryLifecycle {
	lifecycle.Repository = NewSQLiteRecoveryRepository(database)
	return &lifecycle
}

func RedactFailure(err error) string {
	return refreshrecovery.RedactFailure(err)
}
