package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ServingStateReader is the immutable read surface needed to resolve the
// active generation and inspect candidate artifacts. Native PostgreSQL
// serving state intentionally exposes this surface without legacy candidate
// mutation methods.
type ServingStateReader interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

// ServingStateRepository is the legacy mutable lifecycle used by noncanonical
// refresh execution. Keeping it as a refinement of ServingStateReader makes
// the mutation authority explicit while preserving the existing adapter
// contract for SQLite callers.
type ServingStateRepository interface {
	ServingStateReader
	Create(context.Context, servingstate.CreateInput) (servingstate.State, error)
	SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
	Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error)
	MarkFailed(context.Context, servingstate.ID, error) error
}

// ErrServingStateMutationsRequired is returned whenever a legacy refresh path
// reaches a lifecycle write without an explicitly supplied mutation authority.
// Canonical delivery paths do not require this authority.
var ErrServingStateMutationsRequired = errors.New("refresh serving-state mutation authority is required")

type WorkflowRepository interface {
	RunTreeRepository
	CreateRun(context.Context, RunInput) (RunRecord, error)
	ListChildRuns(context.Context, ReadScope, string) ([]RunRecord, error)
	MarkRunRunning(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error)
	MarkRunSucceeded(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error)
	MarkRunFailed(context.Context, projectgraph.ServingIdentity, string, string) (RunRecord, error)
	MarkRunPrepared(context.Context, JobRecord) (RunRecord, error)
	RunMayPublish(context.Context, JobRecord) (bool, error)
}

// ProducerFailureRepository is the narrow pre-lease failure capability. It
// may only transition a queued root created by the producer; running work
// must use LeaseFencedRunRepository with the exact worker fence.
type ProducerFailureRepository interface {
	MarkQueuedRunFailed(context.Context, projectgraph.ServingIdentity, string, string) (RunRecord, error)
}

type LoadedArtifact struct {
	Definition           *refreshartifact.Definition
	Graph                projectgraph.ProjectGraph
	ManagedDataRevisions map[string]string
}

type ArtifactLoader interface {
	Load(context.Context, servingstate.Artifact) (LoadedArtifact, error)
}

type Materializer interface {
	Materialize(context.Context, MaterializeInput) (int64, error)
}

type MaterializeInput struct {
	Definition  *refreshartifact.Definition
	Active      servingstate.State
	Candidate   servingstate.State
	Artifact    servingstate.Artifact
	Environment servingstate.Environment
	Plan        refreshplan.Plan
}

type RuntimeHost interface {
	PrepareServingState(context.Context, string) (*runtimehost.Prepared, error)
	ActivatePrepared(*runtimehost.Prepared, func() error) error
}

type RetentionRunner interface {
	Run(context.Context, bool) error
}

type Publisher interface {
	PublishRefreshTarget(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID)
}

type DataVersionRepository interface {
	SaveDataVersion(context.Context, refreshschedule.DataVersion) error
}

type CandidateValidationHook interface {
	AfterArtifactValidation(context.Context, servingstate.State, servingstate.Validation) error
}

type PublicationUnitOfWork interface {
	Publish(context.Context, projectgraph.ServingIdentity, servingstate.ID, refreshschedule.DataVersion) error
}

type CanonicalPublicationUnitOfWork interface {
	CompleteCanonicalRefresh(context.Context, JobRecord, CanonicalRefreshResult) error
}

// CanonicalResultReconciler applies the committed canonical result to runtime
// and serving projections. The durable publication is already authoritative
// when this hook runs, so implementations must reconcile the exact result
// generation and remain safe to replay.
type CanonicalResultReconciler func(context.Context, JobRecord, CanonicalRefreshResult) error

// CanonicalRefreshResult is the exact committed delivery identity produced by
// a refresh restatement. The old job identity remains the workflow/lease
// fence; this result identifies the new immutable serving generation whose
// data-version metadata must be committed with workflow completion.
type CanonicalRefreshResult struct {
	PlanID         string
	ServingStateID string
	// NativeGenerationID is the delivery PostgreSQL generation identity used
	// by the atomic native refresh finalizer. It is explicit because a serving
	// state ID is not inherently a delivery generation ID across authorities.
	NativeGenerationID string
	SnapshotID         int64
}

type Service struct {
	// ServingStates is the read-only serving authority used by both canonical
	// and legacy refresh flows. Native PostgreSQL composition supplies its
	// immutable repository directly.
	ServingStates ServingStateReader
	// ServingStateMutations is required only for the legacy candidate/activate
	// flow. Native canonical execution leaves it nil and performs lifecycle
	// writes through its canonical delivery executor instead.
	ServingStateMutations ServingStateRepository
	ResolveActive         func(context.Context, projectgraph.ServingIdentity) (ServingState, error)
	// ResolveTargetRevision reads the authoritative deployment target fence at
	// queue time. Native PostgreSQL refreshes must carry this exact revision
	// into the durable run; a zero or inferred revision is never publishable.
	ResolveTargetRevision     func(context.Context, projectgraph.ServingIdentity) (int64, error)
	ResolveSourceDigest       func(context.Context, projectgraph.ServingIdentity) (string, error)
	CanonicalExecutor         func(context.Context, JobRecord) (CanonicalRefreshResult, error)
	CanonicalResultReconciler CanonicalResultReconciler
	Runs                      WorkflowRepository
	Artifacts                 ArtifactLoader
	Materializer              Materializer
	Runtime                   RuntimeHost
	Retention                 RetentionRunner
	Publisher                 Publisher
	DataVersions              DataVersionRepository
	Publication               PublicationUnitOfWork
	CandidateValidationHooks  []CandidateValidationHook
	Now                       func() time.Time
}

type ServingState struct {
	State    servingstate.State
	Artifact servingstate.Artifact
}

func stateIdentity(state servingstate.State) (projectgraph.ServingIdentity, error) {
	return projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
}

type QueueAssetResult struct {
	Run            RunRecord
	DependencyRuns []RunRecord
	ServingStateID servingstate.ID
}

type QueuePipelineInput struct {
	// RunID carries the command/operation identity used for idempotent replay.
	// Empty values are generated as fresh identities by the persistence adapter.
	RunID                string
	Identity             projectgraph.ServingIdentity
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	PipelineID           projectgraph.ResourceID
	TriggerType          string
	InvocationSource     string
	TriggerID            string
	MatchingScheduleIDs  []string
	ArtifactDigest       string
	Occurrence           *refreshschedule.Occurrence
	// AuditIntent is committed with the root queued run when supplied by an
	// authenticated command transport. Dependency runs are observational and
	// do not emit duplicate command intents.
	AuditIntent *access.AuditIntent
	// IdempotencyKey is carried only by an explicitly keyed manual command.
	// Scheduled and UI retries leave it empty and therefore create fresh runs.
	IdempotencyKey string
}

// IdempotentRunTreeRepository is an optional read fast-path for native
// operation replay. It runs before mutable serving-state/pipeline preflight so
// an already accepted command remains replayable after deployment changes.
type IdempotentRunTreeRepository interface {
	LookupIdempotentRun(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID, string, string) (RunRecord, []RunRecord, bool, error)
}

// RequestDigest binds a keyed refresh command to its actor and stable logical
// scope. The serving generation is intentionally excluded so a committed
// result remains replayable after deployment cutover. JSON field order is
// fixed by the struct declaration,
// yielding a stable sha256 digest across retries and transports.
func RequestDigest(identity projectgraph.ServingIdentity, actorID string, pipelineID projectgraph.ResourceID) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	if err := pipelineID.Validate(); err != nil {
		return "", err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return "", errors.New("refresh actor id is required")
	}
	payload, err := json.Marshal(struct {
		Actor       string `json:"actor"`
		Project     string `json:"project"`
		Environment string `json:"environment"`
		Pipeline    string `json:"pipeline"`
	}{actorID, identity.ProjectID.String(), identity.Environment, pipelineID.String()})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// CancelRequestDigest binds a keyed cancellation to the authenticated actor,
// the stable project/environment scope, the originating serving generation,
// and the exact run identity. The originating generation is included because
// cancellation is generation-fenced even though reads remain available after
// a later deployment cutover.
func CancelRequestDigest(identity projectgraph.ServingIdentity, actorID, runID string) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return "", errors.New("refresh actor id is required")
	}
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedRunID == "" || trimmedRunID != runID {
		return "", errors.New("refresh run id is required")
	}
	runID = trimmedRunID
	payload, err := json.Marshal(struct {
		Actor       string `json:"actor"`
		Project     string `json:"project"`
		Environment string `json:"environment"`
		Generation  string `json:"generation"`
		Run         string `json:"run"`
	}{actorID, identity.ProjectID.String(), identity.Environment, identity.GenerationID, runID})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// InvocationAdmissionChecker is an optional repository fast-path used before
// candidate construction. The durable CreateRun transaction remains the final
// fence; this check prevents an external conflict from allocating physical
// candidate state that can never be queued.
type InvocationAdmissionChecker interface {
	CheckInvocationAdmission(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID, string) error
}

type ScheduledInvocationAdmissionChecker interface {
	CheckScheduledInvocationAdmission(context.Context, refreshschedule.Occurrence) error
}

func (s Service) QueuePipelineRefresh(ctx context.Context, input QueuePipelineInput) (QueueAssetResult, error) {
	if s.ServingStates == nil || s.Runs == nil || s.Artifacts == nil {
		return QueueAssetResult{}, fmt.Errorf("serving state, refresh run, and artifact repositories are required")
	}
	if err := input.Identity.Validate(); err != nil {
		return QueueAssetResult{}, err
	}
	if err := input.PipelineID.Validate(); err != nil {
		return QueueAssetResult{}, err
	}
	if input.PrincipalID == "" {
		return QueueAssetResult{}, fmt.Errorf("refresh principal id is required")
	}
	if err := ValidateGroupIDs(input.GroupIDs); err != nil {
		return QueueAssetResult{}, err
	}
	if input.EstimatedMemoryBytes <= 0 {
		return QueueAssetResult{}, fmt.Errorf("refresh estimated memory must be positive")
	}
	if input.TriggerType == "" {
		input.TriggerType = TriggerManual
	}
	if input.InvocationSource == "" {
		input.InvocationSource = input.TriggerType
	}
	if input.TriggerType != TriggerManual && input.TriggerType != TriggerSchedule {
		return QueueAssetResult{}, fmt.Errorf("unsupported refresh pipeline trigger %q", input.TriggerType)
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey != "" && input.TriggerType != TriggerManual {
		return QueueAssetResult{}, fmt.Errorf("idempotency key is supported only for manual refresh commands")
	}
	requestDigest := ""
	if idempotencyKey != "" {
		var digestErr error
		requestDigest, digestErr = RequestDigest(input.Identity, input.PrincipalID, input.PipelineID)
		if digestErr != nil {
			return QueueAssetResult{}, digestErr
		}
		if replayRepo, ok := s.Runs.(IdempotentRunTreeRepository); ok {
			root, children, replay, replayErr := replayRepo.LookupIdempotentRun(ctx, input.Identity, input.PipelineID, idempotencyKey, requestDigest)
			if replayErr != nil {
				return QueueAssetResult{}, replayErr
			}
			if replay {
				return QueueAssetResult{Run: root, DependencyRuns: children, ServingStateID: servingstate.ID(root.Identity.GenerationID)}, nil
			}
		}
	}
	if s.CanonicalExecutor != nil && s.ResolveTargetRevision == nil {
		return QueueAssetResult{}, fmt.Errorf("canonical refresh target revision resolver is required")
	}
	active, err := s.activeForIdentity(ctx, input.Identity)
	if err != nil {
		return QueueAssetResult{}, err
	}
	targetRevision := int64(0)
	if s.ResolveTargetRevision != nil {
		targetRevision, err = s.ResolveTargetRevision(ctx, input.Identity)
		if err != nil {
			return QueueAssetResult{}, fmt.Errorf("resolve refresh target revision: %w", err)
		}
		if targetRevision <= 0 {
			return QueueAssetResult{}, fmt.Errorf("resolve refresh target revision: revision must be positive")
		}
	}
	if input.ArtifactDigest != "" && input.ArtifactDigest != active.Artifact.Digest {
		return QueueAssetResult{}, fmt.Errorf("refresh pipeline schedule belongs to superseded artifact %q", input.ArtifactDigest)
	}
	loaded, err := s.Artifacts.Load(ctx, active.Artifact)
	if err != nil {
		return QueueAssetResult{}, err
	}
	if loaded.Definition == nil {
		return QueueAssetResult{}, fmt.Errorf("compiled project definition is required")
	}
	pipeline, ok := loaded.Definition.Pipelines[input.PipelineID.String()]
	if !ok {
		return QueueAssetResult{}, fmt.Errorf("unknown refresh pipeline %q", input.PipelineID)
	}
	if err := validatePipelineInvocation(pipeline, &input); err != nil {
		return QueueAssetResult{}, err
	}
	if checker, ok := s.Runs.(InvocationAdmissionChecker); ok {
		if err := checker.CheckInvocationAdmission(ctx, input.Identity, input.PipelineID, input.InvocationSource); err != nil {
			return QueueAssetResult{}, err
		}
	}
	if input.TriggerType == TriggerSchedule && input.Occurrence != nil {
		if checker, ok := s.Runs.(ScheduledInvocationAdmissionChecker); ok {
			if err := checker.CheckScheduledInvocationAdmission(ctx, *input.Occurrence); err != nil {
				return QueueAssetResult{}, err
			}
		}
	}
	plan, err := refreshplan.ForPipeline(loaded.Definition, input.Identity.ProjectID, input.PipelineID)
	if err != nil {
		return QueueAssetResult{}, err
	}
	candidate := active
	if s.CanonicalExecutor == nil {
		if s.ServingStateMutations == nil {
			return QueueAssetResult{}, fmt.Errorf("%w for noncanonical execution", ErrServingStateMutationsRequired)
		}
		candidate, err = s.CreateRefreshCandidate(ctx, RefreshCandidateInput{Identity: input.Identity, CreatedBy: input.PrincipalID, Active: active, ArtifactGraph: loaded.Graph, ManagedDataRevisions: loaded.ManagedDataRevisions})
		if err != nil {
			return QueueAssetResult{}, err
		}
	}
	runIdentity := mustStateIdentity(candidate.State)
	planArtifactDigest := active.Artifact.Digest
	if s.CanonicalExecutor != nil {
		if s.ResolveSourceDigest == nil {
			return QueueAssetResult{}, fmt.Errorf("canonical refresh source digest resolver is required")
		}
		planArtifactDigest, err = s.ResolveSourceDigest(ctx, input.Identity)
		if err != nil {
			return QueueAssetResult{}, fmt.Errorf("resolve canonical refresh source digest: %w", err)
		}
	}
	plan, err = plan.BindGeneration(runIdentity, planArtifactDigest)
	if err != nil {
		return QueueAssetResult{}, err
	}
	matchingScheduleIDs := append([]string(nil), input.MatchingScheduleIDs...)
	if input.TriggerType == TriggerSchedule {
		matchingScheduleIDs = append([]string(nil), input.Occurrence.MatchingScheduleIDs...)
	}
	policy := refreshplan.InvocationPolicy{InvocationSource: input.InvocationSource}
	if input.TriggerType == TriggerSchedule {
		policy.MatchingScheduleIDs = matchingScheduleIDs
		policy.StartingDeadlineSeconds = pipeline.StartingDeadlineSeconds
		policy.ConcurrencyPolicy = pipeline.ConcurrencyPolicy
	}
	pipelinePlan, err := plan.DeliveryPipelinePlan(policy)
	if err != nil {
		return QueueAssetResult{}, err
	}
	payload, err := json.Marshal(struct {
		PipelineID    string `json:"pipelineId"`
		SemanticModel string `json:"semanticModel"`
		Plan          any    `json:"pipelinePlan"`
	}{input.PipelineID.String(), pipeline.SemanticModelID.String(), pipelinePlan})
	if err != nil {
		return QueueAssetResult{}, fmt.Errorf("encode refresh pipeline job: %w", err)
	}
	nominalTime := ""
	if input.Occurrence != nil {
		nominalTime = input.Occurrence.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	rootInput := RunInput{RunID: input.RunID, Identity: runIdentity, SemanticModelID: pipeline.SemanticModelID, PipelineID: input.PipelineID, PipelinePlan: &pipelinePlan, InvocationSource: input.InvocationSource, MatchingScheduleIDs: matchingScheduleIDs, TriggerID: input.TriggerID, NominalTime: nominalTime, ConcurrencyPolicy: policy.ConcurrencyPolicy, PrincipalID: input.PrincipalID, GroupIDs: append([]string(nil), input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes, TargetType: TargetRefreshPipeline, TargetID: input.PipelineID, TargetRevision: targetRevision, TriggerType: input.TriggerType, JobKind: JobKindRefreshPipeline, PayloadJSON: string(payload), AuditIntent: input.AuditIntent}
	dependencyTargets := make([]projectgraph.ResourceID, 0, len(plan.DependencyTables))
	for _, table := range plan.DependencyTables {
		targetID, parseErr := projectgraph.NewResourceID(table)
		if parseErr != nil {
			return QueueAssetResult{}, parseErr
		}
		dependencyTargets = append(dependencyTargets, targetID)
	}
	root, children, err := s.Runs.CreateRunTree(ctx, RunTreeInput{Root: rootInput, DependencyTargets: dependencyTargets, Occurrence: input.Occurrence, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest})
	if err != nil {
		if s.CanonicalExecutor == nil {
			_ = s.MarkFailed(ctx, candidate, err)
		}
		return QueueAssetResult{}, err
	}
	if root.Status == RunStatusSkipped {
		return QueueAssetResult{Run: root, ServingStateID: candidate.State.ID}, nil
	}
	s.publish(ctx, root.Identity, root.TargetType, root.TargetID)
	return QueueAssetResult{Run: root, DependencyRuns: children, ServingStateID: candidate.State.ID}, nil
}

func validatePipelineInvocation(pipeline refreshschedule.Definition, input *QueuePipelineInput) error {
	if input == nil {
		return fmt.Errorf("refresh pipeline invocation is required")
	}
	if input.TriggerType == TriggerSchedule {
		if input.Occurrence == nil {
			return fmt.Errorf("scheduled refresh occurrence is required")
		}
		if len(input.Occurrence.MatchingScheduleIDs) == 0 {
			return fmt.Errorf("scheduled refresh matching schedule ids are required")
		}
		schedules := make(map[string]struct{}, len(pipeline.Schedules))
		for _, schedule := range pipeline.Schedules {
			schedules[schedule.ID] = struct{}{}
		}
		for _, id := range input.Occurrence.MatchingScheduleIDs {
			if _, ok := schedules[id]; !ok {
				return fmt.Errorf("unknown schedule %q for pipeline %q", id, pipeline.ID)
			}
		}
		return nil
	}
	if input.TriggerType == TriggerManual {
		// Manual invocation is implicit and intentionally has no authored trigger
		// ID list. Authorization is performed by the API/module boundary.
		input.TriggerID = ""
		input.MatchingScheduleIDs = nil
		return nil
	}
	return fmt.Errorf("unsupported refresh pipeline trigger %q", input.TriggerType)
}

func (s Service) activeForIdentity(ctx context.Context, identity projectgraph.ServingIdentity) (ServingState, error) {
	if s.ResolveActive == nil {
		return s.Active(ctx, identity.ProjectID, servingstate.Environment(identity.Environment))
	}
	active, err := s.ResolveActive(ctx, identity)
	if err != nil {
		return ServingState{}, err
	}
	resolved, err := stateIdentity(active.State)
	if err != nil {
		return ServingState{}, err
	}
	if resolved != identity || active.Artifact.ServingStateID != active.State.ID {
		return ServingState{}, fmt.Errorf("resolved refresh base does not match active serving identity")
	}
	return active, nil
}

func mustStateIdentity(state servingstate.State) projectgraph.ServingIdentity {
	identity, err := stateIdentity(state)
	if err != nil {
		panic(err)
	}
	return identity
}

func (s Service) ExecuteClaimedJob(ctx context.Context, job JobRecord) error {
	if s.CanonicalExecutor != nil {
		if s.Runs == nil {
			return fmt.Errorf("refresh run repository is required")
		}
		if err := job.Validate(); err != nil {
			return err
		}
		if _, err := s.Runs.MarkRunPrepared(ctx, job); err != nil {
			return err
		}
		result, err := s.CanonicalExecutor(ctx, job)
		if err != nil {
			if errors.Is(err, ErrRunStale) {
				if fenced, ok := s.Runs.(LeaseFencedSupersedeRepository); ok {
					if supersedeErr := fenced.MarkRunTreeSupersededClaimed(ctx, job, err.Error()); supersedeErr != nil {
						return fmt.Errorf("supersede stale refresh tree: %w", supersedeErr)
					}
				} else {
					return fmt.Errorf("supersede stale refresh tree: %w", ErrLeaseLost)
				}
				return err
			}
			_ = markRunFailedForWorker(ctx, s.Runs, job, err.Error())
			return err
		}
		publication, ok := s.Publication.(CanonicalPublicationUnitOfWork)
		if !ok {
			return fmt.Errorf("canonical refresh publication unit of work is required")
		}
		if err := publication.CompleteCanonicalRefresh(ctx, job, result); err != nil {
			return err
		}
		if s.CanonicalResultReconciler != nil {
			if err := s.CanonicalResultReconciler(ctx, job, result); err != nil {
				return fmt.Errorf("reconcile canonical refresh result: %w", err)
			}
		}
		s.publish(ctx, job.Identity, job.TargetType, job.TargetID)
		return nil
	}
	if s.ServingStates == nil || s.Runs == nil || s.Artifacts == nil || s.Materializer == nil {
		return fmt.Errorf("serving state, refresh run, artifact loader, and materializer are required")
	}
	if s.ServingStateMutations == nil {
		return fmt.Errorf("%w for noncanonical execution", ErrServingStateMutationsRequired)
	}
	if err := job.Validate(); err != nil {
		return err
	}
	candidateState, err := s.ServingStates.ByID(ctx, servingstate.ID(job.Identity.GenerationID))
	if err != nil {
		return err
	}
	candidateIdentity := mustStateIdentity(candidateState)
	if candidateIdentity != job.Identity {
		return fmt.Errorf("refresh job serving identity does not match candidate")
	}
	if candidateState.Status == servingstate.StatusActive && candidateState.DuckLakeSnapshotID > 0 {
		return markRunSucceededForWorker(ctx, s.Runs, job)
	}
	candidateArtifact, err := s.ServingStates.ArtifactByServingState(ctx, candidateState.ID)
	if err != nil {
		return err
	}
	active, err := s.Active(ctx, job.Identity.ProjectID, servingstate.Environment(job.Identity.Environment))
	if err != nil {
		return err
	}
	loaded, err := s.Artifacts.Load(ctx, candidateArtifact)
	if err != nil {
		return err
	}
	if loaded.Definition == nil {
		return fmt.Errorf("compiled project definition is required")
	}
	plan, err := refreshplan.ForPipeline(loaded.Definition, job.Identity.ProjectID, job.PipelineID)
	if err != nil {
		return err
	}
	readScope, err := ReadScopeForIdentity(job.Identity)
	if err != nil {
		return err
	}
	children, err := s.Runs.ListChildRuns(ctx, readScope, job.RunID)
	if err != nil {
		return err
	}
	// Child runs are queued records owned by the same root command. They do
	// not have an independently inferred worker fence here: the root worker
	// performs the governed materialization and the fenced tree terminal
	// transition closes every child. Calling MarkRunRunning on a child would
	// silently invent a lease owner/fence in PostgreSQL.
	_ = children
	candidate := ServingState{State: candidateState, Artifact: candidateArtifact}
	snapshotID, err := s.Materializer.Materialize(ctx, MaterializeInput{Definition: loaded.Definition, Active: active.State, Candidate: candidate.State, Artifact: candidate.Artifact, Environment: candidateState.Environment, Plan: plan})
	if err != nil {
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	if err := s.RecordSnapshot(ctx, candidate, snapshotID); err != nil {
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	if s.Runtime == nil {
		err = fmt.Errorf("runtime host is required for refresh activation")
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	prepared, err := s.Runtime.PrepareServingState(ctx, string(candidateState.ID))
	if err != nil {
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	if prepared == nil {
		err = fmt.Errorf("runtime host returned a nil prepared runtime")
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	if _, err := s.Runs.MarkRunPrepared(ctx, job); err != nil {
		_ = prepared.Close()
		return err
	}
	mayPublish, err := s.Runs.RunMayPublish(ctx, job)
	if err != nil {
		_ = prepared.Close()
		return err
	}
	if !mayPublish {
		_ = prepared.Close()
		return ErrLeaseLost
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	version := refreshschedule.DataVersion{Identity: candidateIdentity, SemanticModelID: job.SemanticModelID, SnapshotID: snapshotID, RefreshedAt: now.UTC(), Source: refreshschedule.DataVersionSourceRefresh, PipelineID: job.PipelineID, RunID: job.RunID, TargetRevision: job.TargetRevision, LeaseOwner: job.LeaseOwner, LeaseRevision: job.LeaseRevision}
	activate := func() error { return s.activateRefresh(ctx, candidate, version) }
	if err := s.Runtime.ActivatePrepared(prepared, activate); err != nil {
		_ = prepared.Close()
		if errors.Is(err, ErrLeaseLost) {
			return err
		}
		_ = s.failJob(ctx, job, candidate, err)
		return err
	}
	if s.Retention != nil {
		_ = s.Retention.Run(ctx, false)
	}
	s.publish(ctx, candidateIdentity, job.TargetType, job.TargetID)
	return nil
}

func (s Service) activateRefresh(ctx context.Context, candidate ServingState, version refreshschedule.DataVersion) error {
	if s.Publication == nil {
		return fmt.Errorf("refresh publication unit of work is required")
	}
	identity := mustStateIdentity(candidate.State)
	return s.Publication.Publish(ctx, identity, candidate.State.ID, version)
}
func (s Service) failJob(ctx context.Context, job JobRecord, candidate ServingState, cause error) error {
	if err := markRunFailedForWorker(ctx, s.Runs, job, cause.Error()); err != nil {
		return err
	}
	_ = s.MarkFailed(ctx, candidate, cause)
	s.publish(ctx, job.Identity, job.TargetType, job.TargetID)
	return nil
}
func markRunSucceededForWorker(ctx context.Context, runs WorkflowRepository, job JobRecord) error {
	if fenced, ok := runs.(LeaseFencedRunRepository); ok {
		_, err := fenced.MarkRunSucceededClaimed(ctx, job)
		return err
	}
	return ErrLeaseLost
}
func markRunFailedForWorker(ctx context.Context, runs WorkflowRepository, job JobRecord, message string) error {
	if fenced, ok := runs.(LeaseFencedRunRepository); ok {
		return fenced.MarkRunTreeFailedClaimed(ctx, job, message)
	}
	return ErrLeaseLost
}
func (s Service) publish(ctx context.Context, identity projectgraph.ServingIdentity, targetType string, targetID projectgraph.ResourceID) {
	if s.Publisher != nil {
		s.Publisher.PublishRefreshTarget(ctx, identity, targetType, targetID)
	}
}

type RefreshCandidateInput struct {
	Identity             projectgraph.ServingIdentity
	CreatedBy            string
	Active               ServingState
	ArtifactGraph        projectgraph.ProjectGraph
	ManagedDataRevisions map[string]string
}

func (s Service) Active(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (ServingState, error) {
	state, artifact, err := s.ServingStates.ActiveArtifact(ctx, projectID, environment)
	if err != nil {
		return ServingState{}, err
	}
	return ServingState{State: state, Artifact: artifact}, nil
}

func (s Service) CreateRefreshCandidate(ctx context.Context, input RefreshCandidateInput) (ServingState, error) {
	if err := input.Identity.Validate(); err != nil {
		return ServingState{}, err
	}
	var accessPolicy projectmanifest.AccessPolicy
	if raw := strings.TrimSpace(input.Active.State.AccessPolicyJSON); raw != "" && raw != "null" {
		if err := json.Unmarshal([]byte(raw), &accessPolicy); err != nil {
			return ServingState{}, fmt.Errorf("decode active access policy: %w", err)
		}
	}
	if s.ServingStateMutations == nil {
		return ServingState{}, ErrServingStateMutationsRequired
	}
	created, err := s.ServingStateMutations.Create(ctx, servingstate.CreateInput{ProjectID: input.Identity.ProjectID, Environment: servingstate.Environment(input.Identity.Environment), CreatedBy: input.CreatedBy, Source: servingstate.SourceRefresh})
	if err != nil {
		return ServingState{}, err
	}
	candidateArtifact := servingstate.Artifact{ID: "artifact_" + string(created.ID), ServingStateID: created.ID, Digest: input.Active.Artifact.Digest, Format: input.Active.Artifact.Format, Path: input.Active.Artifact.Path, ManifestJSON: input.Active.Artifact.ManifestJSON, SizeBytes: input.Active.Artifact.SizeBytes, CreatedAt: input.Active.Artifact.CreatedAt}
	validation := servingstate.Validation{Digest: input.Active.State.Digest, ManifestJSON: input.Active.State.ManifestJSON, ProjectID: input.Identity.ProjectID, ProjectDigest: input.Active.State.ProjectDigest, AccessPolicy: accessPolicy, ManagedDataRevisions: cloneStringMap(input.ManagedDataRevisions), Graph: input.ArtifactGraph}
	for _, hook := range s.CandidateValidationHooks {
		if hook != nil {
			if err := hook.AfterArtifactValidation(ctx, created, validation); err != nil {
				_ = s.ServingStateMutations.MarkFailed(ctx, created.ID, err)
				return ServingState{}, err
			}
		}
	}
	validated, err := s.ServingStateMutations.SaveValidated(ctx, created.ID, validation, candidateArtifact)
	if err != nil {
		_ = s.ServingStateMutations.MarkFailed(ctx, created.ID, err)
		return ServingState{}, err
	}
	return ServingState{State: validated, Artifact: candidateArtifact}, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
func (s Service) RecordSnapshot(ctx context.Context, candidate ServingState, snapshotID int64) error {
	if snapshotID <= 0 {
		return fmt.Errorf("serving state snapshot id must be positive")
	}
	if s.ServingStateMutations == nil {
		return ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.RecordDuckLakeSnapshot(ctx, candidate.State.ID, snapshotID)
}
func (s Service) Activate(ctx context.Context, candidate ServingState) (servingstate.State, error) {
	identity := mustStateIdentity(candidate.State)
	if s.ServingStateMutations == nil {
		return servingstate.State{}, ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.Activate(ctx, identity.ProjectID, servingstate.Environment(identity.Environment), candidate.State.ID, "")
}
func (s Service) MarkFailed(ctx context.Context, state ServingState, cause error) error {
	if state.State.ID == "" || cause == nil {
		return nil
	}
	if s.ServingStateMutations == nil {
		return ErrServingStateMutationsRequired
	}
	return s.ServingStateMutations.MarkFailed(ctx, state.State.ID, cause)
}
