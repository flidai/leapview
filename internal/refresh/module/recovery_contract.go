package module

import refreshrecovery "github.com/flidai/leapview/internal/refresh/recovery"

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

const (
	EvidenceTransitionQualification = refreshrecovery.EvidenceTransitionQualification
)

func RedactFailure(err error) string {
	return refreshrecovery.RedactFailure(err)
}
