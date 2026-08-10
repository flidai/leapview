// Package run owns the durable refresh lifecycle and its public contracts.
package run

import (
	"context"
	"errors"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
)

var (
	ErrRunNotCancellable = apigenfailure.New("not_cancellable", "refresh run is not cancellable")
	ErrLeaseLost         = errors.New("refresh job lease fence is no longer active")
)

const (
	RunStatusQueued     = "queued"
	RunStatusRunning    = "running"
	RunStatusPrepared   = "prepared"
	RunStatusSucceeded  = "succeeded"
	RunStatusFailed     = "failed"
	RunStatusCancelled  = "cancelled"
	RunStatusSuperseded = "superseded"

	TargetModelTable      = "model_table"
	TargetRefreshPipeline = "refresh_pipeline"

	TriggerDependency = "dependency"
	TriggerManual     = "manual"
	TriggerSchedule   = "schedule"
	TriggerRetry      = "retry"

	JobKindRefreshPipeline = "refresh_pipeline"
	JobKindChildRun        = "child_run"
)

type RunRecord struct {
	ID                   string `json:"id"`
	WorkspaceID          string `json:"workspaceId"`
	Environment          string `json:"-"`
	ModelID              string `json:"modelId"`
	ServingStateID       string `json:"servingStateId,omitempty"`
	PrincipalID          string `json:"principalId,omitempty"`
	PrincipalDisplayName string `json:"principalDisplayName,omitempty"`
	TargetType           string `json:"targetType"`
	TargetID             string `json:"targetId"`
	TargetGeneration     int64  `json:"targetGeneration"`
	TriggerType          string `json:"triggerType"`
	ParentRunID          string `json:"parentRunId,omitempty"`
	RetryOf              string `json:"retryOf,omitempty"`
	Status               string `json:"status"`
	CreatedAt            string `json:"createdAt"`
	UpdatedAt            string `json:"updatedAt"`
	StartedAt            string `json:"startedAt,omitempty"`
	FinishedAt           string `json:"finishedAt,omitempty"`
	Error                string `json:"error,omitempty"`
}

type RunInput struct {
	WorkspaceID      string
	Environment      string
	ModelID          string
	ServingStateID   string
	PrincipalID      string
	TargetType       string
	TargetID         string
	TargetGeneration int64
	TriggerType      string
	ParentRunID      string
	RetryOf          string
	JobKind          string
	PayloadJSON      string
}

type JobRecord struct {
	ID               string
	WorkspaceID      string
	Environment      string
	ServingStateID   string
	ModelID          string
	Kind             string
	PayloadJSON      string
	RunID            string
	TargetType       string
	TargetID         string
	TargetGeneration int64
	TriggerType      string
	AttemptCount     int
	LeaseOwner       string
	LeaseGeneration  int64
}

type JobQueueStats struct {
	QueuedJobs      int
	RunningJobs     int
	StaleLeasedJobs int
}

type RunRepository interface {
	CreateRun(ctx context.Context, input RunInput) (RunRecord, error)
	GetRun(ctx context.Context, workspaceID, runID string) (RunRecord, error)
	ListRuns(ctx context.Context, workspaceID string, page RunPage) ([]RunRecord, error)
	ListTargetRuns(ctx context.Context, workspaceID, targetType, targetID string, page RunPage) ([]RunRecord, error)
	ListChildRuns(ctx context.Context, workspaceID, parentRunID string) ([]RunRecord, error)
	LatestTargetRun(ctx context.Context, workspaceID, environment, targetType, targetID string) (RunRecord, bool, error)
	LatestSuccessfulTargetRun(ctx context.Context, workspaceID, environment, targetType, targetID string) (RunRecord, bool, error)
	MarkRunRunning(ctx context.Context, workspaceID, runID string) (RunRecord, error)
	MarkRunSucceeded(ctx context.Context, workspaceID, runID string) (RunRecord, error)
	MarkRunFailed(ctx context.Context, workspaceID, runID, message string) (RunRecord, error)
}

// LeaseFencedRunRepository contains worker-owned terminal transitions. The
// claim (owner, generation, and expiry) is carried with the job so a reclaimed
// or expired worker cannot mutate the authoritative run/job pair.
//
// Worker execution requires this contract. HTTP and reconciliation callers may
// continue using the legacy explicit domain transitions on RunRepository, but
// no worker path may fall back to them.
type LeaseFencedRunRepository interface {
	MarkRunSucceededClaimed(ctx context.Context, job JobRecord) (RunRecord, error)
	MarkRunFailedClaimed(ctx context.Context, job JobRecord, message string) (RunRecord, error)
	MarkRunTreeFailedClaimed(ctx context.Context, job JobRecord, message string) error
}

type RunPage struct {
	Limit       int
	After       string
	Environment string
}
