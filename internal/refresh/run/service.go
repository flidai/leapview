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
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

// ServingStateReader is the immutable read surface needed to resolve the
// active generation and inspect its artifact.
type ServingStateReader interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

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

type Publisher interface {
	PublishRefreshTarget(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID)
}

type CanonicalPublicationUnitOfWork interface {
	CompleteCanonicalRefresh(context.Context, JobRecord, CanonicalRefreshResult) error
}

// CanonicalResultReconciler applies the committed canonical result to
// post-commit projections such as dashboard state. Runtime cutover belongs to
// CanonicalCompletionCoordinator, which runs durable completion as its
// activation callback before publishing the prepared runtime.
type CanonicalResultReconciler func(context.Context, JobRecord, CanonicalRefreshResult) error

// CanonicalCompletionCoordinator controls the boundary around durable
// canonical completion. The coordinator must invoke complete exactly once
// before it returns successfully; callers may use it to prepare process-local state first,
// commit the durable target through complete, and publish the prepared state
// only after complete succeeds.
//
// Keeping this seam in the refresh service avoids making the workflow aware of
// a particular runtime-host implementation while allowing production
// composition to make durable completion the activation callback.
type CanonicalCompletionCoordinator func(context.Context, JobRecord, CanonicalRefreshResult, func() error) error

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
	// ServingStates is the read-only serving authority used to resolve the
	// active generation. Native PostgreSQL composition supplies its immutable
	// repository directly.
	ServingStates ServingStateReader
	ResolveActive func(context.Context, projectgraph.ServingIdentity) (ServingState, error)
	// ResolveTargetRevision reads the authoritative deployment target fence at
	// queue time. Native PostgreSQL refreshes must carry this exact revision
	// into the durable run; a zero or inferred revision is never publishable.
	ResolveTargetRevision          func(context.Context, projectgraph.ServingIdentity) (int64, error)
	ResolveSourceDigest            func(context.Context, projectgraph.ServingIdentity) (string, error)
	CanonicalExecutor              func(context.Context, JobRecord) (CanonicalRefreshResult, error)
	CanonicalCompletionCoordinator CanonicalCompletionCoordinator
	CanonicalResultReconciler      CanonicalResultReconciler
	Runs                           WorkflowRepository
	Artifacts                      ArtifactLoader
	Publisher                      Publisher
	Publication                    CanonicalPublicationUnitOfWork
}

type ServingState struct {
	State    servingstate.State
	Artifact servingstate.Artifact
}

func (s Service) Active(ctx context.Context, projectID projectgraph.ResourceID, environment servingstate.Environment) (ServingState, error) {
	state, artifact, err := s.ServingStates.ActiveArtifact(ctx, projectID, environment)
	if err != nil {
		return ServingState{}, err
	}
	return ServingState{State: state, Artifact: artifact}, nil
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
	if s.CanonicalExecutor == nil {
		return QueueAssetResult{}, fmt.Errorf("canonical refresh executor is required")
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
	if s.ResolveTargetRevision == nil {
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
	runIdentity := mustStateIdentity(active.State)
	if s.ResolveSourceDigest == nil {
		return QueueAssetResult{}, fmt.Errorf("canonical refresh source digest resolver is required")
	}
	planArtifactDigest, err := s.ResolveSourceDigest(ctx, input.Identity)
	if err != nil {
		return QueueAssetResult{}, fmt.Errorf("resolve canonical refresh source digest: %w", err)
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
		return QueueAssetResult{}, err
	}
	if root.Status == RunStatusSkipped {
		return QueueAssetResult{Run: root, ServingStateID: active.State.ID}, nil
	}
	s.publish(ctx, root.Identity, root.TargetType, root.TargetID)
	return QueueAssetResult{Run: root, DependencyRuns: children, ServingStateID: active.State.ID}, nil
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
	if s.CanonicalExecutor == nil {
		return fmt.Errorf("canonical refresh executor is required")
	}
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
	publication := s.Publication
	if publication == nil {
		return fmt.Errorf("canonical refresh publication unit of work is required")
	}
	completionCalled := false
	var completionErr error
	complete := func() error {
		if completionCalled {
			completionErr = errors.Join(completionErr, errors.New("canonical refresh completion callback invoked more than once"))
			return completionErr
		}
		completionCalled = true
		completionErr = publication.CompleteCanonicalRefresh(ctx, job, result)
		return completionErr
	}
	if s.CanonicalCompletionCoordinator != nil {
		if err := s.CanonicalCompletionCoordinator(ctx, job, result, complete); err != nil {
			return fmt.Errorf("coordinate canonical refresh completion: %w", err)
		}
		if !completionCalled {
			return errors.New("coordinate canonical refresh completion: completion callback was not invoked")
		}
		if completionErr != nil {
			return fmt.Errorf("coordinate canonical refresh completion: %w", completionErr)
		}
	} else if err := complete(); err != nil {
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
