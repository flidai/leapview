// Package run owns the durable refresh lifecycle and its public contracts.
package run

import (
	"context"
	"errors"
	"strings"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrRunNotCancellable = apigenfailure.New("not_cancellable", "refresh run is not cancellable")
	ErrLeaseLost         = errors.New("refresh job lease fence is no longer active")
)

var validTargetTypes = map[string]struct{}{TargetModelTable: {}, TargetRefreshPipeline: {}}
var validTriggerTypes = map[string]struct{}{TriggerDependency: {}, TriggerManual: {}, TriggerSchedule: {}, TriggerRetry: {}}
var validJobKinds = map[string]struct{}{JobKindRefreshPipeline: {}, JobKindChildRun: {}}

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
	ID       string                       `json:"id"`
	Identity projectgraph.ServingIdentity `json:"identity"`
	// SemanticModelID is the governed semantic model being materialized.
	SemanticModelID projectgraph.ResourceID `json:"semanticModelId"`
	// PipelineID identifies the authored refresh pipeline; it is equal to
	// TargetID only for refresh_pipeline targets, never for model_table targets.
	PipelineID           projectgraph.ResourceID `json:"pipelineId,omitempty"`
	PrincipalID          string                  `json:"principalId,omitempty"`
	PrincipalDisplayName string                  `json:"principalDisplayName,omitempty"`
	TargetType           string                  `json:"targetType"`
	// TargetID is the concrete materialization target, distinct from the
	// semantic model and pipeline identities above.
	TargetID       projectgraph.ResourceID `json:"targetId"`
	TargetRevision int64                   `json:"targetRevision"`
	TriggerType    string                  `json:"triggerType"`
	ParentRunID    string                  `json:"parentRunId,omitempty"`
	RetryOf        string                  `json:"retryOf,omitempty"`
	Status         string                  `json:"status"`
	CreatedAt      string                  `json:"createdAt"`
	UpdatedAt      string                  `json:"updatedAt"`
	StartedAt      string                  `json:"startedAt,omitempty"`
	FinishedAt     string                  `json:"finishedAt,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

type RunInput struct {
	Identity        projectgraph.ServingIdentity
	SemanticModelID projectgraph.ResourceID
	PipelineID      projectgraph.ResourceID
	PrincipalID     string
	TargetType      string
	TargetID        projectgraph.ResourceID
	TargetRevision  int64
	TriggerType     string
	ParentRunID     string
	RetryOf         string
	JobKind         string
	PayloadJSON     string
}

type JobRecord struct {
	ID              string
	Identity        projectgraph.ServingIdentity
	SemanticModelID projectgraph.ResourceID
	PipelineID      projectgraph.ResourceID
	Kind            string
	PayloadJSON     string
	RunID           string
	TargetType      string
	TargetID        projectgraph.ResourceID
	TargetRevision  int64
	TriggerType     string
	AttemptCount    int
	LeaseOwner      string
	LeaseRevision   int64
}

type JobQueueStats struct {
	QueuedJobs      int
	RunningJobs     int
	StaleLeasedJobs int
}

type RunRepository interface {
	CreateRun(ctx context.Context, input RunInput) (RunRecord, error)
	GetRun(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (RunRecord, error)
	ListRuns(ctx context.Context, identity projectgraph.ServingIdentity, page RunPage) ([]RunRecord, error)
	ListTargetRuns(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID, page RunPage) ([]RunRecord, error)
	ListChildRuns(ctx context.Context, identity projectgraph.ServingIdentity, parentRunID string) ([]RunRecord, error)
	LatestTargetRun(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) (RunRecord, bool, error)
	LatestSuccessfulTargetRun(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) (RunRecord, bool, error)
	MarkRunRunning(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (RunRecord, error)
	MarkRunSucceeded(ctx context.Context, identity projectgraph.ServingIdentity, runID string) (RunRecord, error)
	MarkRunFailed(ctx context.Context, identity projectgraph.ServingIdentity, runID, message string) (RunRecord, error)
}

// LeaseFencedRunRepository contains worker-owned terminal transitions. The
// claim (owner, revision, and expiry) is carried with the job so a reclaimed
// or expired worker cannot mutate the authoritative run/job pair.
type LeaseFencedRunRepository interface {
	MarkRunSucceededClaimed(ctx context.Context, job JobRecord) (RunRecord, error)
	MarkRunFailedClaimed(ctx context.Context, job JobRecord, message string) (RunRecord, error)
	MarkRunTreeFailedClaimed(ctx context.Context, job JobRecord, message string) error
}

type RunPage struct {
	Limit int
	After string
}

// Validate enforces the immutable serving scope carried by a queued run. The
// generation is part of the identity and is never inferred from a workspace
// or a mutable serving-state alias.
func (input RunInput) Validate() error {
	if err := input.Identity.Validate(); err != nil {
		return err
	}
	if err := input.SemanticModelID.Validate(); err != nil {
		return err
	}
	if input.PipelineID != "" {
		if err := input.PipelineID.Validate(); err != nil {
			return err
		}
	}
	if err := input.TargetID.Validate(); err != nil {
		return err
	}
	for name, value := range map[string]string{"target type": input.TargetType, "trigger type": input.TriggerType, "job kind": input.JobKind} {
		if err := validateOperational(value, name, true); err != nil {
			return err
		}
	}
	if _, ok := validTargetTypes[input.TargetType]; !ok {
		return errors.New("refresh target type is unsupported")
	}
	if _, ok := validTriggerTypes[input.TriggerType]; !ok {
		return errors.New("refresh trigger type is unsupported")
	}
	if _, ok := validJobKinds[input.JobKind]; !ok {
		return errors.New("refresh job kind is unsupported")
	}
	if input.TargetType == TargetRefreshPipeline && (input.PipelineID == "" || input.PipelineID != input.TargetID) {
		return errors.New("refresh pipeline target must equal pipeline id")
	}
	for name, value := range map[string]string{"principal id": input.PrincipalID, "parent run id": input.ParentRunID, "retry of": input.RetryOf} {
		if err := validateOperational(value, name, false); err != nil {
			return err
		}
	}
	if input.TargetRevision < 0 {
		return errors.New("refresh target revision must not be negative")
	}
	return nil
}

func (job JobRecord) Validate() error {
	if err := job.Identity.Validate(); err != nil {
		return err
	}
	if err := job.SemanticModelID.Validate(); err != nil {
		return err
	}
	if job.PipelineID != "" {
		if err := job.PipelineID.Validate(); err != nil {
			return err
		}
	}
	if err := job.TargetID.Validate(); err != nil {
		return err
	}
	if _, ok := validTargetTypes[job.TargetType]; !ok {
		return errors.New("refresh target type is unsupported")
	}
	if _, ok := validTriggerTypes[job.TriggerType]; !ok {
		return errors.New("refresh trigger type is unsupported")
	}
	if _, ok := validJobKinds[job.Kind]; !ok {
		return errors.New("refresh job kind is unsupported")
	}
	if job.TargetType == TargetRefreshPipeline && (job.PipelineID == "" || job.PipelineID != job.TargetID) {
		return errors.New("refresh pipeline target must equal pipeline id")
	}
	for name, value := range map[string]string{"job id": job.ID, "run id": job.RunID} {
		required := true
		if err := validateOperational(value, name, required); err != nil {
			return err
		}
	}
	if (job.LeaseOwner == "") != (job.LeaseRevision == 0) {
		return errors.New("refresh lease owner and revision must be provided together")
	}
	if job.LeaseOwner != "" {
		if err := validateOperational(job.LeaseOwner, "lease owner", true); err != nil {
			return err
		}
	}
	if job.AttemptCount < 0 {
		return errors.New("refresh job attempt count must not be negative")
	}
	for name, value := range map[string]string{"target type": job.TargetType, "trigger type": job.TriggerType} {
		if err := validateOperational(value, name, true); err != nil {
			return err
		}
	}
	if job.TargetRevision < 0 || job.LeaseRevision < 0 {
		return errors.New("refresh job revisions must not be negative")
	}
	return nil
}

func validateOperational(value, name string, required bool) error {
	if !required && value == "" {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("refresh " + name + " must be canonical")
	}
	return nil
}
