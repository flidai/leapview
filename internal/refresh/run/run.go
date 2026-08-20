// Package run owns the durable refresh lifecycle and its public contracts.
package run

import (
	"context"
	"errors"
	"strings"
	"unicode"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrRunNotCancellable = apigenfailure.New("not_cancellable", "refresh run is not cancellable")
	ErrTargetActive      = apigenfailure.New("conflict", "refresh target already has an active run")
	ErrLeaseLost         = errors.New("refresh job lease fence is no longer active")
	ErrRunStale          = errors.New("refresh run is stale")
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
	RunStatusSkipped    = "skipped"

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
	PipelineID           projectgraph.ResourceID   `json:"pipelineId,omitempty"`
	PipelinePlan         *projectpipelineplan.Plan `json:"pipelinePlan,omitempty"`
	TriggerID            string                    `json:"triggerId,omitempty"`
	NominalTime          string                    `json:"nominalTime,omitempty"`
	PlanDigest           string                    `json:"planDigest,omitempty"`
	MaterializationScope []string                  `json:"materializationScope,omitempty"`
	PrincipalID          string                    `json:"principalId,omitempty"`
	PrincipalDisplayName string                    `json:"principalDisplayName,omitempty"`
	TargetType           string                    `json:"targetType"`
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
	Identity             projectgraph.ServingIdentity
	SemanticModelID      projectgraph.ResourceID
	PipelineID           projectgraph.ResourceID
	PipelinePlan         *projectpipelineplan.Plan
	TriggerID            string
	NominalTime          string
	Overlap              string
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	TargetType           string
	TargetID             projectgraph.ResourceID
	TargetRevision       int64
	TriggerType          string
	ParentRunID          string
	RetryOf              string
	JobKind              string
	PayloadJSON          string
}

type JobRecord struct {
	ID                   string
	Identity             projectgraph.ServingIdentity
	SemanticModelID      projectgraph.ResourceID
	PipelineID           projectgraph.ResourceID
	PipelinePlan         *projectpipelineplan.Plan
	TriggerID            string
	NominalTime          string
	PrincipalID          string
	GroupIDs             []string
	Kind                 string
	PayloadJSON          string
	EstimatedMemoryBytes int64
	RunID                string
	TargetType           string
	TargetID             projectgraph.ResourceID
	TargetRevision       int64
	TriggerType          string
	AttemptCount         int
	LeaseOwner           string
	LeaseRevision        int64
}

type JobQueueStats struct {
	QueuedJobs      int
	RunningJobs     int
	StaleLeasedJobs int
}

// ReadScope is the stable API scope for refresh runs. Run records are created
// in an immutable serving generation, but callers must continue to be able to
// read them after that generation is replaced by a newer active generation.
// Mutating operations retain ServingIdentity and therefore remain generation
// fenced.
type ReadScope struct {
	ProjectID   projectgraph.ResourceID
	Environment string
}

// ReadScopeForIdentity derives the canonical project/environment read scope
// from a complete serving identity without discarding any validation of the
// originating generation.
func ReadScopeForIdentity(identity projectgraph.ServingIdentity) (ReadScope, error) {
	if err := identity.Validate(); err != nil {
		return ReadScope{}, err
	}
	return ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}, nil
}

func (scope ReadScope) Validate() error {
	return projectgraph.ValidateServingScope(scope.ProjectID, scope.Environment)
}

func (scope ReadScope) Matches(identity projectgraph.ServingIdentity) bool {
	return scope.ProjectID == identity.ProjectID && scope.Environment == identity.Environment
}

type RunRepository interface {
	CreateRun(ctx context.Context, input RunInput) (RunRecord, error)
	GetRun(ctx context.Context, scope ReadScope, runID string) (RunRecord, error)
	ListRuns(ctx context.Context, scope ReadScope, page RunPage) ([]RunRecord, error)
	ListTargetRuns(ctx context.Context, scope ReadScope, targetType string, targetID projectgraph.ResourceID, page RunPage) ([]RunRecord, error)
	ListChildRuns(ctx context.Context, scope ReadScope, parentRunID string) ([]RunRecord, error)
	LatestTargetRun(ctx context.Context, scope ReadScope, targetType string, targetID projectgraph.ResourceID) (RunRecord, bool, error)
	LatestSuccessfulTargetRun(ctx context.Context, scope ReadScope, targetType string, targetID projectgraph.ResourceID) (RunRecord, bool, error)
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

type LeaseFencedSupersedeRepository interface {
	MarkRunTreeSupersededClaimed(ctx context.Context, job JobRecord, message string) error
}

type RunPage struct {
	Limit int
	After string
}

// Validate enforces the immutable serving scope carried by a queued run. The
// generation is part of the identity and is never inferred from a container
// or a mutable serving-state alias.
func (input RunInput) Validate() error {
	if err := input.Identity.Validate(); err != nil {
		return err
	}
	if err := input.SemanticModelID.Validate(); err != nil {
		return err
	}
	if err := input.PipelineID.Validate(); err != nil {
		return err
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
	if input.TargetType == TargetRefreshPipeline && input.PipelineID != input.TargetID {
		return errors.New("refresh pipeline target must equal pipeline id")
	}
	if input.TargetType == TargetRefreshPipeline && input.ParentRunID == "" {
		if err := validateOperational(input.TriggerID, "trigger id", true); err != nil {
			return err
		}
		if input.Overlap != "forbid" && input.Overlap != "replace" {
			return errors.New("refresh overlap policy must be forbid or replace")
		}
		if input.PipelinePlan == nil {
			return errors.New("refresh pipeline plan is required")
		}
		if err := input.PipelinePlan.Validate(); err != nil {
			return err
		}
		if input.PipelinePlan.ProjectID != input.Identity.ProjectID.String() || input.PipelinePlan.Environment != input.Identity.Environment || input.PipelinePlan.PipelineID != input.PipelineID.String() || input.PipelinePlan.SemanticModelID != input.SemanticModelID.String() || input.PipelinePlan.ServingGenerationID != input.Identity.GenerationID {
			return errors.New("refresh pipeline plan does not match run identity")
		}
		if input.TriggerType == TriggerSchedule && input.NominalTime == "" {
			return errors.New("scheduled refresh nominal time is required")
		}
	}
	for name, value := range map[string]string{"principal id": input.PrincipalID, "parent run id": input.ParentRunID, "retry of": input.RetryOf} {
		required := name == "principal id"
		if err := validateOperational(value, name, required); err != nil {
			return err
		}
	}
	if input.TargetRevision < 0 {
		return errors.New("refresh target revision must not be negative")
	}
	if err := ValidateGroupIDs(input.GroupIDs); err != nil {
		return err
	}
	if input.EstimatedMemoryBytes <= 0 {
		return errors.New("refresh estimated memory must be positive")
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
	if err := job.PipelineID.Validate(); err != nil {
		return err
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
	if job.TargetType == TargetRefreshPipeline && job.PipelineID != job.TargetID {
		return errors.New("refresh pipeline target must equal pipeline id")
	}
	if job.TargetType == TargetRefreshPipeline {
		if err := validateOperational(job.TriggerID, "trigger id", true); err != nil {
			return err
		}
		if job.PipelinePlan == nil {
			return errors.New("refresh pipeline job plan is required")
		}
		if err := job.PipelinePlan.Validate(); err != nil {
			return err
		}
		if job.PipelinePlan.ProjectID != job.Identity.ProjectID.String() || job.PipelinePlan.Environment != job.Identity.Environment || job.PipelinePlan.PipelineID != job.PipelineID.String() || job.PipelinePlan.SemanticModelID != job.SemanticModelID.String() || job.PipelinePlan.ServingGenerationID != job.Identity.GenerationID {
			return errors.New("refresh pipeline job plan does not match job identity")
		}
	}
	for name, value := range map[string]string{"job id": job.ID, "run id": job.RunID, "principal id": job.PrincipalID} {
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
	if err := ValidateGroupIDs(job.GroupIDs); err != nil {
		return err
	}
	if job.EstimatedMemoryBytes <= 0 {
		return errors.New("refresh estimated memory must be positive")
	}
	return nil
}

func ValidateGroupIDs(groups []string) error {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if err := validateOperational(group, "group id", true); err != nil {
			return err
		}
		if _, exists := seen[group]; exists {
			return errors.New("refresh group ids must be unique")
		}
		if len(seen) > 0 {
			previous := groups[len(seen)-1]
			if group <= previous {
				return errors.New("refresh group ids must be sorted canonically")
			}
		}
		seen[group] = struct{}{}
	}
	return nil
}

func validateOperational(value, name string, required bool) error {
	if !required && value == "" {
		return nil
	}
	if value == "" || value != strings.TrimSpace(value) || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("refresh " + name + " must be canonical")
	}
	return nil
}
