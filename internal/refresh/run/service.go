package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type ServingStateRepository interface {
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
	Create(context.Context, servingstate.CreateInput) (servingstate.State, error)
	SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error)
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error
	Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error)
	MarkFailed(context.Context, servingstate.ID, error) error
}

type WorkflowRepository interface {
	CreateRun(context.Context, RunInput) (RunRecord, error)
	ListChildRuns(context.Context, ReadScope, string) ([]RunRecord, error)
	MarkRunRunning(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error)
	MarkRunSucceeded(context.Context, projectgraph.ServingIdentity, string) (RunRecord, error)
	MarkRunFailed(context.Context, projectgraph.ServingIdentity, string, string) (RunRecord, error)
	MarkRunPrepared(context.Context, JobRecord) (RunRecord, error)
	RunMayPublish(context.Context, JobRecord) (bool, error)
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
	CompleteCanonicalRefresh(context.Context, JobRecord) error
}

type Service struct {
	ServingStates            ServingStateRepository
	ResolveActive            func(context.Context, projectgraph.ServingIdentity) (ServingState, error)
	CanonicalExecutor        func(context.Context, JobRecord) error
	Runs                     WorkflowRepository
	Artifacts                ArtifactLoader
	Materializer             Materializer
	Runtime                  RuntimeHost
	Retention                RetentionRunner
	Publisher                Publisher
	DataVersions             DataVersionRepository
	Publication              PublicationUnitOfWork
	CandidateValidationHooks []CandidateValidationHook
	Now                      func() time.Time
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
	Identity             projectgraph.ServingIdentity
	PrincipalID          string
	GroupIDs             []string
	EstimatedMemoryBytes int64
	PipelineID           projectgraph.ResourceID
	TriggerType          string
	RetryOf              string
	ArtifactDigest       string
	Occurrence           *refreshschedule.Occurrence
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
	if input.TriggerType != TriggerManual && input.TriggerType != TriggerSchedule && input.TriggerType != TriggerRetry {
		return QueueAssetResult{}, fmt.Errorf("unsupported refresh pipeline trigger %q", input.TriggerType)
	}
	active, err := s.activeForIdentity(ctx, input.Identity)
	if err != nil {
		return QueueAssetResult{}, err
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
	plan, err := refreshplan.ForPipeline(loaded.Definition, input.Identity.ProjectID, input.PipelineID)
	if err != nil {
		return QueueAssetResult{}, err
	}
	candidate := active
	if s.CanonicalExecutor == nil {
		candidate, err = s.CreateRefreshCandidate(ctx, RefreshCandidateInput{Identity: input.Identity, CreatedBy: input.PrincipalID, Active: active, ArtifactGraph: loaded.Graph, ManagedDataRevisions: loaded.ManagedDataRevisions})
		if err != nil {
			return QueueAssetResult{}, err
		}
	}
	payload, _ := json.Marshal(map[string]string{"pipelineId": input.PipelineID.String(), "semanticModel": pipeline.SemanticModelID.String()})
	rootInput := RunInput{Identity: mustStateIdentity(candidate.State), SemanticModelID: pipeline.SemanticModelID, PipelineID: input.PipelineID, PrincipalID: input.PrincipalID, GroupIDs: append([]string(nil), input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes, TargetType: TargetRefreshPipeline, TargetID: input.PipelineID, TriggerType: input.TriggerType, RetryOf: input.RetryOf, JobKind: JobKindRefreshPipeline, PayloadJSON: string(payload)}
	var root RunRecord
	if input.Occurrence != nil {
		creator, ok := s.Runs.(interface {
			CreateScheduledRun(context.Context, RunInput, refreshschedule.Occurrence) (RunRecord, error)
		})
		if !ok {
			return QueueAssetResult{}, fmt.Errorf("refresh run repository does not support atomic scheduled runs")
		}
		root, err = creator.CreateScheduledRun(ctx, rootInput, *input.Occurrence)
	} else {
		root, err = s.Runs.CreateRun(ctx, rootInput)
	}
	if err != nil {
		if s.CanonicalExecutor == nil {
			_ = s.MarkFailed(ctx, candidate, err)
		}
		return QueueAssetResult{}, err
	}
	children := make([]RunRecord, 0, len(plan.DependencyTables))
	for _, table := range plan.DependencyTables {
		targetID, parseErr := projectgraph.NewResourceID(table)
		if parseErr != nil {
			return QueueAssetResult{}, parseErr
		}
		child, childErr := s.Runs.CreateRun(ctx, RunInput{Identity: root.Identity, SemanticModelID: pipeline.SemanticModelID, PipelineID: input.PipelineID, PrincipalID: input.PrincipalID, GroupIDs: append([]string(nil), input.GroupIDs...), EstimatedMemoryBytes: input.EstimatedMemoryBytes, TargetType: TargetModelTable, TargetID: targetID, TargetRevision: root.TargetRevision, TriggerType: TriggerDependency, ParentRunID: root.ID, JobKind: JobKindChildRun})
		if childErr != nil {
			_, _ = s.Runs.MarkRunFailed(ctx, root.Identity, root.ID, childErr.Error())
			if s.CanonicalExecutor == nil {
				_ = s.MarkFailed(ctx, candidate, childErr)
			}
			return QueueAssetResult{}, childErr
		}
		children = append(children, child)
	}
	s.publish(ctx, root.Identity, root.TargetType, root.TargetID)
	return QueueAssetResult{Run: root, DependencyRuns: children, ServingStateID: candidate.State.ID}, nil
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
		if err := s.CanonicalExecutor(ctx, job); err != nil {
			_ = markRunFailedForWorker(ctx, s.Runs, job, err.Error())
			return err
		}
		publication, ok := s.Publication.(CanonicalPublicationUnitOfWork)
		if !ok {
			return fmt.Errorf("canonical refresh publication unit of work is required")
		}
		if err := publication.CompleteCanonicalRefresh(ctx, job); err != nil {
			return err
		}
		s.publish(ctx, job.Identity, job.TargetType, job.TargetID)
		return nil
	}
	if s.ServingStates == nil || s.Runs == nil || s.Artifacts == nil || s.Materializer == nil {
		return fmt.Errorf("serving state, refresh run, artifact loader, and materializer are required")
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
	for _, child := range children {
		_, _ = s.Runs.MarkRunRunning(ctx, job.Identity, child.ID)
	}
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
	created, err := s.ServingStates.Create(ctx, servingstate.CreateInput{ProjectID: input.Identity.ProjectID, Environment: servingstate.Environment(input.Identity.Environment), CreatedBy: input.CreatedBy, Source: servingstate.SourceRefresh})
	if err != nil {
		return ServingState{}, err
	}
	candidateArtifact := servingstate.Artifact{ID: "artifact_" + string(created.ID), ServingStateID: created.ID, Digest: input.Active.Artifact.Digest, Format: input.Active.Artifact.Format, Path: input.Active.Artifact.Path, ManifestJSON: input.Active.Artifact.ManifestJSON, SizeBytes: input.Active.Artifact.SizeBytes, CreatedAt: input.Active.Artifact.CreatedAt}
	validation := servingstate.Validation{Digest: input.Active.State.Digest, ManifestJSON: input.Active.State.ManifestJSON, ProjectID: input.Identity.ProjectID, ProjectDigest: input.Active.State.ProjectDigest, AccessPolicy: accessPolicy, ManagedDataRevisions: cloneStringMap(input.ManagedDataRevisions), Graph: input.ArtifactGraph}
	for _, hook := range s.CandidateValidationHooks {
		if hook != nil {
			if err := hook.AfterArtifactValidation(ctx, created, validation); err != nil {
				_ = s.ServingStates.MarkFailed(ctx, created.ID, err)
				return ServingState{}, err
			}
		}
	}
	validated, err := s.ServingStates.SaveValidated(ctx, created.ID, validation, candidateArtifact)
	if err != nil {
		_ = s.ServingStates.MarkFailed(ctx, created.ID, err)
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
	return s.ServingStates.RecordDuckLakeSnapshot(ctx, candidate.State.ID, snapshotID)
}
func (s Service) Activate(ctx context.Context, candidate ServingState) (servingstate.State, error) {
	identity := mustStateIdentity(candidate.State)
	return s.ServingStates.Activate(ctx, identity.ProjectID, servingstate.Environment(identity.Environment), candidate.State.ID, "")
}
func (s Service) MarkFailed(ctx context.Context, state ServingState, cause error) error {
	if state.State.ID == "" || cause == nil {
		return nil
	}
	return s.ServingStates.MarkFailed(ctx, state.State.ID, cause)
}
