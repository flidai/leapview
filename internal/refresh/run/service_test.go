package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"

	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var serviceIdentity = projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "dep_candidate"}

func TestServiceExecuteClaimedJobActivatesAfterMaterializeAndPrepare(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	publisher := &fakePublisher{}
	materializer := &fakeMaterializer{snapshotID: 42}
	runtime := &fakeRuntimeHost{}
	retention := &fakeRetention{}
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		Materializer:  materializer,
		Runtime:       runtime,
		Retention:     retention,
		Publisher:     publisher,
		Publication:   fakePublication{repo: repo},
	}

	err := service.ExecuteClaimedJob(ctx, JobRecord{
		ID:       "job_1",
		Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_root",
		SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: TargetRefreshPipeline,
		TargetID: "sales-refresh", TriggerType: TriggerManual,
		Kind: JobKindRefreshPipeline,
	})
	if err != nil {
		t.Fatalf("execute claimed job: %v", err)
	}

	if repo.recordedSnapshotDeployment != "dep_candidate" || repo.recordedSnapshot != 42 {
		t.Fatalf("recorded snapshot = %s/%d, want dep_candidate/42", repo.recordedSnapshotDeployment, repo.recordedSnapshot)
	}
	if repo.activatedDeployment != "dep_candidate" {
		t.Fatalf("activated deployment = %s, want dep_candidate", repo.activatedDeployment)
	}
	if !runtime.prepared || !runtime.committed {
		t.Fatalf("runtime prepared/committed = %v/%v, want true/true", runtime.prepared, runtime.committed)
	}
	if !retention.ran {
		t.Fatal("retention was not reconciled")
	}
	if repo.runStatuses["run_root"] != RunStatusSucceeded || repo.runStatuses["run_child"] != RunStatusSucceeded {
		t.Fatalf("run statuses = %#v, want root and child succeeded", repo.runStatuses)
	}
	if got, want := materializer.tables, []string{"customers", "orders"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("materialized tables = %#v, want %#v", got, want)
	}
}

func TestServiceExecuteClaimedJobMaterializeFailureDoesNotActivate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		Materializer:  &fakeMaterializer{err: errors.New("materialize failed")},
		Runtime:       &fakeRuntimeHost{},
	}

	err := service.ExecuteClaimedJob(ctx, JobRecord{
		ID:       "job_1",
		Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_root",
		SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: TargetRefreshPipeline,
		TargetID: "sales-refresh", TriggerType: TriggerManual,
		Kind: JobKindRefreshPipeline,
	})
	if err == nil {
		t.Fatal("execute claimed job error = nil, want materialize failure")
	}
	if repo.activatedDeployment != "" {
		t.Fatalf("activated deployment = %s, want none", repo.activatedDeployment)
	}
	if repo.failedDeployment != "dep_candidate" {
		t.Fatalf("failed deployment = %s, want dep_candidate", repo.failedDeployment)
	}
	if repo.runStatuses["run_root"] != RunStatusFailed || repo.runStatuses["run_child"] != RunStatusFailed {
		t.Fatalf("run statuses = %#v, want root and child failed", repo.runStatuses)
	}
}

func TestServiceExecuteClaimedJobRuntimePrepareFailureDoesNotActivate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		Materializer:  &fakeMaterializer{snapshotID: 42},
		Runtime:       &fakeRuntimeHost{prepareErr: errors.New("prepare failed")},
	}

	err := service.ExecuteClaimedJob(ctx, JobRecord{
		ID:       "job_1",
		Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_root",
		SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: TargetRefreshPipeline,
		TargetID: "sales-refresh", TriggerType: TriggerManual,
		Kind: JobKindRefreshPipeline,
	})
	if err == nil {
		t.Fatal("execute claimed job error = nil, want prepare failure")
	}
	if repo.activatedDeployment != "" {
		t.Fatalf("activated deployment = %s, want none", repo.activatedDeployment)
	}
	if repo.recordedSnapshot != 42 {
		t.Fatalf("recorded snapshot = %d, want 42 before prepare", repo.recordedSnapshot)
	}
	if repo.runStatuses["run_root"] != RunStatusFailed {
		t.Fatalf("root run status = %s, want failed", repo.runStatuses["run_root"])
	}
}

func TestServiceExecuteClaimedJobRuntimeActivationFailureDoesNotPublishOrActivate(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	wantErr := errors.New("runtime activation failed")
	runtime := &fakeRuntimeHost{activateErr: wantErr}
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		Materializer:  &fakeMaterializer{snapshotID: 42},
		Runtime:       runtime,
	}

	err := service.ExecuteClaimedJob(ctx, JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, Kind: JobKindRefreshPipeline,
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("execute claimed job error = %v, want %v", err, wantErr)
	}
	if repo.activatedDeployment != "" {
		t.Fatalf("activated deployment = %s, want none", repo.activatedDeployment)
	}
	if runtime.committed {
		t.Fatal("runtime was published after atomic activation failed")
	}
	if repo.failedDeployment != "dep_candidate" {
		t.Fatalf("failed deployment = %s, want dep_candidate", repo.failedDeployment)
	}
}

func TestServiceExecuteClaimedJobRequiresAtomicRuntimeHost(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		Materializer:  &fakeMaterializer{snapshotID: 42},
	}

	err := service.ExecuteClaimedJob(ctx, JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20, RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, Kind: JobKindRefreshPipeline,
	})
	if err == nil || !strings.Contains(err.Error(), "runtime host is required") {
		t.Fatalf("execute claimed job error = %v, want required runtime host", err)
	}
	if repo.activatedDeployment != "" {
		t.Fatalf("activated deployment = %s, want none", repo.activatedDeployment)
	}
}

func TestServiceExecuteClaimedJobCompletesCanonicalTree(t *testing.T) {
	repo := newFakeRepo()
	executed := false
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(_ context.Context, job JobRecord) error {
			executed = true
			if job.RunID != "run_root" {
				t.Fatalf("canonical run = %q, want run_root", job.RunID)
			}
			return nil
		},
		Publication: fakePublication{repo: repo},
	}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh",
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual,
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if err != nil {
		t.Fatalf("execute canonical claimed job: %v", err)
	}
	if !executed || repo.runStatuses["run_root"] != RunStatusSucceeded || repo.runStatuses["run_child"] != RunStatusSucceeded {
		t.Fatalf("executed/statuses = %v/%#v", executed, repo.runStatuses)
	}
}

func TestServiceQueuePipelineRefreshCreatesFullSemanticModelRun(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	result, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if result.Run.TargetType != TargetRefreshPipeline || result.Run.TargetID != "sales-refresh" {
		t.Fatalf("root run = %#v", result.Run)
	}
	if len(repo.createdRuns) != 3 {
		t.Fatalf("created runs = %#v, want pipeline root plus both model-table tasks", repo.createdRuns)
	}
	if repo.createdRuns[0].SemanticModelID != "sales" || repo.createdRuns[0].TriggerType != TriggerManual {
		t.Fatalf("root input = %#v", repo.createdRuns[0])
	}
}

func TestServiceQueuePipelineRefreshUsesExactActiveResolver(t *testing.T) {
	repo := newFakeRepo()
	repo.activeErr = servingstate.ErrNotFound
	identity := serviceIdentity
	identity.GenerationID = string(repo.activeDeployment.ID)
	resolved := false
	service := Service{
		ServingStates: repo,
		ResolveActive: func(_ context.Context, requested projectgraph.ServingIdentity) (ServingState, error) {
			resolved = true
			if requested != identity {
				t.Fatalf("resolved identity = %#v, want %#v", requested, identity)
			}
			return ServingState{State: repo.activeDeployment, Artifact: repo.activeArtifact}, nil
		},
		Runs:      repo,
		Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	result, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if !resolved || result.ServingStateID != repo.candidateState.ID {
		t.Fatalf("resolved = %v candidate = %q, want exact resolver and %q", resolved, result.ServingStateID, repo.candidateState.ID)
	}
}

func TestServiceQueuePipelineRefreshDefersCandidateToCanonicalExecutor(t *testing.T) {
	repo := newFakeRepo()
	identity := serviceIdentity
	identity.GenerationID = string(repo.activeDeployment.ID)
	service := Service{
		ServingStates: repo,
		CanonicalExecutor: func(context.Context, JobRecord) error {
			return nil
		},
		Runs:      repo,
		Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	result, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if result.ServingStateID != repo.activeDeployment.ID || result.Run.Identity != identity {
		t.Fatalf("canonical refresh result = %#v, want active identity %#v", result, identity)
	}
	if repo.savedArtifact.ID != "" {
		t.Fatalf("legacy serving candidate was created: %#v", repo.savedArtifact)
	}
}

func TestServiceQueuePipelineRefreshRejectsMismatchedActiveResolver(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		ResolveActive: func(context.Context, projectgraph.ServingIdentity) (ServingState, error) {
			return ServingState{State: repo.activeDeployment, Artifact: repo.activeArtifact}, nil
		},
		Runs:      repo,
		Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("QueuePipelineRefresh() error = %v, want exact identity mismatch", err)
	}
	if len(repo.createdRuns) != 0 {
		t.Fatalf("created runs = %#v, want none", repo.createdRuns)
	}
}

func TestServiceQueuePipelineRefreshPinsCandidateManagedDataRevisions(t *testing.T) {
	repo := newFakeRepo()
	hook := &fakeCandidateValidationHook{}
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts: fakeArtifactLoader{
			definition:           refreshTestDefinition(),
			managedDataRevisions: map[string]string{"olist": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		CandidateValidationHooks: []CandidateValidationHook{hook},
	}

	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	want := map[string]string{"olist": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if hook.candidate.ID != "dep_candidate" || !reflect.DeepEqual(hook.validation.ManagedDataRevisions, want) {
		t.Fatalf("candidate hook = (%q, %#v), want dep_candidate and %#v", hook.candidate.ID, hook.validation.ManagedDataRevisions, want)
	}
	if !reflect.DeepEqual(repo.savedValidation.ManagedDataRevisions, want) {
		t.Fatalf("saved managed-data revisions = %#v, want %#v", repo.savedValidation.ManagedDataRevisions, want)
	}
}

func TestServiceQueuePipelineRefreshFailsCandidateWhenManagedDataPinningFails(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts: fakeArtifactLoader{
			definition:           refreshTestDefinition(),
			managedDataRevisions: map[string]string{"olist": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		CandidateValidationHooks: []CandidateValidationHook{&fakeCandidateValidationHook{err: errors.New("pin failed")}},
	}

	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err == nil || !strings.Contains(err.Error(), "pin failed") {
		t.Fatalf("QueuePipelineRefresh() error = %v, want pin failure", err)
	}
	if repo.failedDeployment != "dep_candidate" || len(repo.createdRuns) != 0 {
		t.Fatalf("failed candidate = %q created runs = %#v, want failed candidate and no runs", repo.failedDeployment, repo.createdRuns)
	}
}

func TestServiceQueuePipelineRefreshRejectsSupersededScheduledArtifact(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerSchedule,
		ArtifactDigest: "sha256:superseded",
	})
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("QueuePipelineRefresh() error = %v, want superseded artifact", err)
	}
	if len(repo.createdRuns) != 0 {
		t.Fatalf("created runs = %#v, want none", repo.createdRuns)
	}
}

func TestServiceCreateRefreshCandidateCopiesActiveArtifactMetadata(t *testing.T) {
	ctx := context.Background()
	repo := newFakeRepo()
	service := Service{ServingStates: repo}
	active := ServingState{
		State: servingstate.State{
			ID:               "dep_active",
			ProjectID:        "movie-project",
			ProjectDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AccessPolicyJSON: `{"groups":{"readers":{"name":"Readers"}}}`,
			Environment:      servingstate.DefaultEnvironment,
			Digest:           "artifact-digest",
			ManifestJSON:     "{}",
		},
		Artifact: servingstate.Artifact{
			ServingStateID: "dep_active",
			Digest:         "artifact-digest",
			Format:         "tar.gz",
			Path:           "/tmp/artifact.tgz",
		},
	}

	candidate, err := service.CreateRefreshCandidate(ctx, RefreshCandidateInput{
		Identity:      projectgraph.ServingIdentity{ProjectID: "movie-project", Environment: "dev", GenerationID: "dep_active"},
		CreatedBy:     "tester",
		Active:        active,
		ArtifactGraph: projectgraph.ProjectGraph{},
	})
	if err != nil {
		t.Fatalf("create refresh candidate: %v", err)
	}

	if repo.savedArtifact.Path != active.Artifact.Path || candidate.Artifact.Path != active.Artifact.Path {
		t.Fatalf("candidate artifact path = %q, want %q", candidate.Artifact.Path, active.Artifact.Path)
	}
	if repo.savedValidation.ProjectID != active.State.ProjectID {
		t.Fatalf("candidate project = %q, want %q", repo.savedValidation.ProjectID, active.State.ProjectID)
	}
	if repo.savedValidation.ProjectDigest != active.State.ProjectDigest {
		t.Fatalf("candidate project provenance = %q, want %q", repo.savedValidation.ProjectDigest, active.State.ProjectDigest)
	}
	if group := repo.savedValidation.AccessPolicy.Groups["readers"]; group.Name != "Readers" {
		t.Fatalf("candidate access policy = %#v, want active policy", repo.savedValidation.AccessPolicy)
	}
}

func refreshTestDefinition() *artifact.Definition {
	return &artifact.Definition{Pipelines: map[string]refreshschedule.Definition{
		"sales-refresh": {ID: "sales-refresh", Name: "sales-refresh", SemanticModelID: "sales"},
	}, Models: map[string]*semanticmodel.Model{
		"sales": {
			Name: "sales",
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{
				"customers": {Model: "customers"},
				"orders":    {Model: "orders"},
			},
			Tables: map[string]semanticmodel.Table{
				"customers": {
					ModelName: "customers", GrainEntity: "customer_id",
					Entities:   map[string]semanticmodel.ModelEntitySpec{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}},
					Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
				},
				"orders": {
					ModelName: "orders", GrainEntity: "order_id", ModelDependencies: []string{"customers"},
					Entities:   map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
					Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
				},
			},
		},
	}}
}

type fakeRepo struct {
	activeErr                  error
	activeDeployment           servingstate.State
	activeArtifact             servingstate.Artifact
	candidateState             servingstate.State
	candidateArtifact          servingstate.Artifact
	recordedSnapshotDeployment servingstate.ID
	recordedSnapshot           int64
	activatedDeployment        servingstate.ID
	failedDeployment           servingstate.ID
	runStatuses                map[string]string
	createdRuns                []RunInput
	savedArtifact              servingstate.Artifact
	savedValidation            servingstate.Validation
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		activeDeployment: servingstate.State{
			ID:               "dep_active",
			ProjectID:        "project",
			ProjectDigest:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AccessPolicyJSON: "{}",
			Environment:      servingstate.DefaultEnvironment,
			Status:           servingstate.StatusActive,
			Digest:           "digest",
			ManifestJSON:     "{}",
		},
		activeArtifact: servingstate.Artifact{
			ServingStateID: "dep_active",
			Digest:         "digest",
			Format:         "tar.gz",
			Path:           "/tmp/artifact.tar.gz",
			ManifestJSON:   "{}",
		},
		candidateState: servingstate.State{
			ID:           "dep_candidate",
			ProjectID:    "project",
			Environment:  servingstate.DefaultEnvironment,
			Status:       servingstate.StatusValidated,
			Digest:       "digest",
			ManifestJSON: "{}",
		},
		candidateArtifact: servingstate.Artifact{
			ServingStateID: "dep_candidate",
			Digest:         "digest",
			Format:         "tar.gz",
			Path:           "/tmp/artifact.tar.gz",
			ManifestJSON:   "{}",
		},
		runStatuses: map[string]string{
			"run_root":  RunStatusRunning,
			"run_child": RunStatusQueued,
		},
	}
}

func (r *fakeRepo) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	if r.activeErr != nil {
		return servingstate.State{}, servingstate.Artifact{}, r.activeErr
	}
	return r.activeDeployment, r.activeArtifact, nil
}

func (r *fakeRepo) Create(context.Context, servingstate.CreateInput) (servingstate.State, error) {
	return servingstate.State{ID: "dep_candidate", ProjectID: "project", Environment: servingstate.DefaultEnvironment, Status: servingstate.StatusPending}, nil
}

func (r *fakeRepo) SaveValidated(_ context.Context, servingStateID servingstate.ID, validation servingstate.Validation, artifact servingstate.Artifact) (servingstate.State, error) {
	r.savedValidation = validation
	r.savedArtifact = artifact
	r.candidateState.ID = servingStateID
	r.candidateState.ProjectID = "project"
	r.candidateState.Digest = validation.Digest
	r.candidateArtifact = artifact
	return r.candidateState, nil
}

func (r *fakeRepo) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return r.candidateState, nil
}

func (r *fakeRepo) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return r.candidateArtifact, nil
}

func (r *fakeRepo) RecordDuckLakeSnapshot(_ context.Context, servingStateID servingstate.ID, snapshotID int64) error {
	r.recordedSnapshotDeployment = servingStateID
	r.recordedSnapshot = snapshotID
	r.candidateState.DuckLakeSnapshotID = snapshotID
	return nil
}

func (r *fakeRepo) Activate(_ context.Context, _ projectgraph.ResourceID, _ servingstate.Environment, servingStateID servingstate.ID, _ servingstate.ID) (servingstate.State, error) {
	r.activatedDeployment = servingStateID
	return r.candidateState, nil
}

func (r *fakeRepo) MarkFailed(_ context.Context, servingStateID servingstate.ID, _ error) error {
	r.failedDeployment = servingStateID
	return nil
}

func (r *fakeRepo) CreateRun(_ context.Context, input RunInput) (RunRecord, error) {
	r.createdRuns = append(r.createdRuns, input)
	id := "run_root"
	if input.ParentRunID != "" {
		id = "run_child"
	}
	r.runStatuses[id] = RunStatusQueued
	return RunRecord{ID: id, Identity: input.Identity, SemanticModelID: input.SemanticModelID, PipelineID: input.PipelineID, PrincipalID: input.PrincipalID, TargetType: input.TargetType, TargetID: input.TargetID, TriggerType: input.TriggerType, ParentRunID: input.ParentRunID}, nil
}

func (r *fakeRepo) ListChildRuns(context.Context, ReadScope, string) ([]RunRecord, error) {
	return []RunRecord{{ID: "run_child", Identity: serviceIdentity, TargetType: TargetModelTable, TargetID: "customers"}}, nil
}

func (r *fakeRepo) MarkRunRunning(_ context.Context, _ projectgraph.ServingIdentity, runID string) (RunRecord, error) {
	r.runStatuses[runID] = RunStatusRunning
	return RunRecord{ID: runID, Status: RunStatusRunning}, nil
}

func (r *fakeRepo) MarkRunSucceeded(_ context.Context, _ projectgraph.ServingIdentity, runID string) (RunRecord, error) {
	r.runStatuses[runID] = RunStatusSucceeded
	return RunRecord{ID: runID, Status: RunStatusSucceeded}, nil
}

func (r *fakeRepo) MarkRunFailed(_ context.Context, _ projectgraph.ServingIdentity, runID, _ string) (RunRecord, error) {
	r.runStatuses[runID] = RunStatusFailed
	return RunRecord{ID: runID, Status: RunStatusFailed}, nil
}

func (r *fakeRepo) MarkRunFailedClaimed(ctx context.Context, job JobRecord, message string) (RunRecord, error) {
	return r.MarkRunFailed(ctx, job.Identity, job.RunID, message)
}

func (r *fakeRepo) MarkRunTreeFailedClaimed(ctx context.Context, job JobRecord, message string) error {
	_, err := r.MarkRunFailedClaimed(ctx, job, message)
	r.runStatuses["run_child"] = RunStatusFailed
	return err
}

func (r *fakeRepo) MarkRunSucceededClaimed(ctx context.Context, job JobRecord) (RunRecord, error) {
	result, err := r.MarkRunSucceeded(ctx, job.Identity, job.RunID)
	if err == nil {
		r.runStatuses["run_child"] = RunStatusSucceeded
	}
	return result, err
}

func (r *fakeRepo) MarkRunPrepared(_ context.Context, job JobRecord) (RunRecord, error) {
	r.runStatuses[job.RunID] = RunStatusPrepared
	return RunRecord{ID: job.RunID, Status: RunStatusPrepared}, nil
}

func (r *fakeRepo) RunMayPublish(context.Context, JobRecord) (bool, error) {
	return true, nil
}

type fakeArtifactLoader struct {
	definition           *artifact.Definition
	managedDataRevisions map[string]string
}

func (l fakeArtifactLoader) Load(context.Context, servingstate.Artifact) (LoadedArtifact, error) {
	return LoadedArtifact{Definition: l.definition, Graph: projectgraph.ProjectGraph{}, ManagedDataRevisions: l.managedDataRevisions}, nil
}

type fakeCandidateValidationHook struct {
	candidate  servingstate.State
	validation servingstate.Validation
	err        error
}

type fakePublication struct{ repo *fakeRepo }

func (p fakePublication) Publish(ctx context.Context, identity projectgraph.ServingIdentity, servingStateID servingstate.ID, version refreshschedule.DataVersion) error {
	if _, err := p.repo.Activate(ctx, identity.ProjectID, servingstate.Environment(identity.Environment), servingStateID, ""); err != nil {
		return err
	}
	_, err := p.repo.MarkRunSucceeded(ctx, identity, version.RunID)
	p.repo.runStatuses["run_child"] = RunStatusSucceeded
	return err
}

func (p fakePublication) CompleteCanonicalRefresh(_ context.Context, _ JobRecord) error {
	p.repo.runStatuses["run_root"] = RunStatusSucceeded
	p.repo.runStatuses["run_child"] = RunStatusSucceeded
	return nil
}

func (h *fakeCandidateValidationHook) AfterArtifactValidation(_ context.Context, candidate servingstate.State, validation servingstate.Validation) error {
	h.candidate = candidate
	h.validation = validation
	return h.err
}

type fakeMaterializer struct {
	snapshotID int64
	err        error
	tables     []string
}

func (m *fakeMaterializer) Materialize(_ context.Context, input MaterializeInput) (int64, error) {
	m.tables = append([]string(nil), input.Plan.Tables...)
	if m.err != nil {
		return 0, m.err
	}
	return m.snapshotID, nil
}

type fakeRuntimeHost struct {
	prepared    bool
	committed   bool
	prepareErr  error
	activateErr error
}

func (h *fakeRuntimeHost) PrepareServingState(context.Context, string) (*runtimehost.Prepared, error) {
	if h.prepareErr != nil {
		return nil, h.prepareErr
	}
	h.prepared = true
	return &runtimehost.Prepared{}, nil
}

func (h *fakeRuntimeHost) ActivatePrepared(_ *runtimehost.Prepared, activate func() error) error {
	if h.activateErr != nil {
		return h.activateErr
	}
	if err := activate(); err != nil {
		return err
	}
	h.committed = true
	return nil
}

type fakeRetention struct {
	ran bool
}

func (r *fakeRetention) Run(context.Context, bool) error {
	r.ran = true
	return nil
}

type fakePublisher struct {
	targets []string
}

func (p *fakePublisher) PublishRefreshTarget(_ context.Context, _ projectgraph.ServingIdentity, _ string, targetID projectgraph.ResourceID) {
	p.targets = append(p.targets, targetID.String())
}
