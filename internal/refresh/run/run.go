// Package run owns the durable refresh lifecycle and its public contracts.
package run

import (
	"context"
	"errors"
	"strings"
	"unicode"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

var (
	ErrRunNotCancellable             = apigenfailure.New("not_cancellable", "refresh run is not cancellable")
	ErrTargetActive                  = apigenfailure.New("conflict", "refresh target already has an active run")
	ErrInvocationConflict            = apigenfailure.New("conflict", "refresh invocation conflicts with an active invocation")
	ErrAdmissionDeniedExternalActive = apigenfailure.New("admission_denied_external_active", "scheduled refresh admission denied while an external invocation is active")
	ErrLeaseLost                     = errors.New("refresh job lease fence is no longer active")
	ErrRunStale                      = errors.New("refresh run is stale")
)

var validTargetTypes = map[string]struct{}{TargetModelTable: {}, TargetRefreshPipeline: {}}
var validTriggerTypes = map[string]struct{}{TriggerDependency: {}, TriggerManual: {}, TriggerSchedule: {}}
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
	// AdmissionDeniedExternalActive is a terminal occurrence outcome. It is
	// intentionally not a runnable RunStatus: no job or run tree is created.
	AdmissionDeniedExternalActive = "admission_denied_external_active"

	TargetModelTable      = "model_table"
	TargetRefreshPipeline = "refresh_pipeline"

	TriggerDependency = "dependency"
	TriggerManual     = "manual"
	TriggerSchedule   = "schedule"

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
	InvocationSource     string                    `json:"invocationSource,omitempty"`
	MatchingScheduleIDs  []string                  `json:"matchingScheduleIds,omitempty"`
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
	Status         string                  `json:"status"`
	CreatedAt      string                  `json:"createdAt"`
	UpdatedAt      string                  `json:"updatedAt"`
	StartedAt      string                  `json:"startedAt,omitempty"`
	FinishedAt     string                  `json:"finishedAt,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

type RunInput struct {
	// RunID is an optional caller-owned command identity. PostgreSQL adapters
	// require it (or another explicit invocation identity) for exact replay;
	// they never collapse two otherwise-identical manual requests.
	RunID                string
	Identity             projectgraph.ServingIdentity
	SemanticModelID      projectgraph.ResourceID
	PipelineID           projectgraph.ResourceID
	PipelinePlan         *projectpipelineplan.Plan
	InvocationSource     string
	MatchingScheduleIDs  []string
	TriggerID            string
	NominalTime          string
	OccurrenceID         string
	ConcurrencyPolicy    string
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	TargetType           string
	TargetID             projectgraph.ResourceID
	TargetRevision       int64
	TriggerType          string
	ParentRunID          string
	JobKind              string
	PayloadJSON          string
	AuditIntent          *access.AuditIntent
}

type JobRecord struct {
	ID                   string
	Identity             projectgraph.ServingIdentity
	SemanticModelID      projectgraph.ResourceID
	PipelineID           projectgraph.ResourceID
	PipelinePlan         *projectpipelineplan.Plan
	InvocationSource     string
	MatchingScheduleIDs  []string
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

// RunTreeInput describes one refresh pipeline root and all dependency
// provenance rows that must be admitted with it. The optional occurrence is
// the scheduler's already-claimed fence; adapters close that claim in the
// same transaction as the root, children, and canonical job enqueue.
type RunTreeInput struct {
	Root              RunInput
	DependencyTargets []projectgraph.ResourceID
	Occurrence        *refreshschedule.Occurrence
}

// RunTreeRepository is the mandatory atomic batch/tree creation capability. A
// successful call commits exactly one root, zero or more dependency rows, and
// the linked queue/workflow authority records as one unit.
type RunTreeRepository interface {
	CreateRunTree(context.Context, RunTreeInput) (RunRecord, []RunRecord, error)
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
	if input.RunID != "" {
		if err := validateOperational(input.RunID, "run id", true); err != nil {
			return err
		}
	}
	if input.OccurrenceID != "" {
		if err := validateOperational(input.OccurrenceID, "occurrence id", true); err != nil {
			return err
		}
	}
	if input.TriggerType == "" {
		if _, ok := validTriggerTypes[input.InvocationSource]; ok {
			input.TriggerType = input.InvocationSource
		}
	}
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
		if input.InvocationSource == "" {
			input.InvocationSource = input.TriggerType
		}
		if input.InvocationSource != TriggerManual && input.InvocationSource != TriggerSchedule && input.InvocationSource != "backfill" && input.InvocationSource != "external" {
			return errors.New("refresh invocation source is unsupported")
		}
		if input.InvocationSource == TriggerSchedule && len(input.MatchingScheduleIDs) == 0 {
			return errors.New("scheduled refresh matching schedule ids are required")
		}
		if input.InvocationSource == TriggerSchedule && input.ConcurrencyPolicy != "Forbid" && input.ConcurrencyPolicy != "Replace" {
			return errors.New("scheduled refresh concurrency policy must be Forbid or Replace")
		}
		if input.TriggerID != "" {
			if err := validateOperational(input.TriggerID, "trigger id", false); err != nil {
				return err
			}
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
		if input.InvocationSource != "" && input.PipelinePlan.InvocationSource != "" && input.InvocationSource != input.PipelinePlan.InvocationSource {
			return errors.New("refresh pipeline invocation source does not match plan evidence")
		}
		if input.TriggerType == TriggerSchedule && input.NominalTime == "" {
			return errors.New("scheduled refresh nominal time is required")
		}
	}
	if err := validateScheduleIDs(input.MatchingScheduleIDs); err != nil {
		return err
	}
	for name, value := range map[string]string{"principal id": input.PrincipalID, "parent run id": input.ParentRunID} {
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

func validateScheduleIDs(ids []string) error {
	previous := ""
	for _, id := range ids {
		if id == "" || id != strings.TrimSpace(id) {
			return errors.New("refresh matching schedule id must be non-empty and canonical")
		}
		if previous != "" && id <= previous {
			return errors.New("matching schedule ids must be sorted canonically")
		}
		previous = id
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
		if err := validateOperational(job.TriggerID, "trigger id", false); err != nil {
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
		if job.InvocationSource == "" {
			job.InvocationSource = job.TriggerType
		}
		if job.InvocationSource == TriggerSchedule && len(job.MatchingScheduleIDs) == 0 {
			return errors.New("scheduled refresh job matching schedule ids are required")
		}
		if job.PipelinePlan.InvocationSource != "" && job.PipelinePlan.InvocationSource != job.InvocationSource {
			return errors.New("refresh pipeline job invocation source does not match plan evidence")
		}
	}
	if err := validateScheduleIDs(job.MatchingScheduleIDs); err != nil {
		return err
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
