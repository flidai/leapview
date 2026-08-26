package module

import (
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
type RecoveryScenarioOutcome = refreshrecovery.ScenarioOutcome
type RecoveryEvidenceArtifact = refreshrecovery.EvidenceArtifact
type RecoveryEvidencePublisher = refreshrecovery.EvidencePublisher
type RecoveryFileEvidencePublisher = refreshrecovery.FileEvidencePublisher

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

func RedactFailure(err error) string {
	return refreshrecovery.RedactFailure(err)
}

func newSQLiteRepository(database *sql.DB) *refreshsqlite.Repository {
	return refreshsqlite.NewRepository(database)
}
