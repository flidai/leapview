package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"

	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var serviceIdentity = projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "dep_active"}

func canonicalQueueService(repo *fakeRepo) Service {
	return Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     fakeArtifactLoader{definition: refreshTestDefinition()},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) {
			return 1, nil
		},
		ResolveSourceDigest: func(_ context.Context, _ projectgraph.ServingIdentity) (string, error) {
			return repo.activeArtifact.Digest, nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, nil
		},
	}
}

func TestServiceExecuteClaimedJobCompletesCanonicalTree(t *testing.T) {
	repo := newFakeRepo()
	executed := false
	reconciled := false
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(_ context.Context, job JobRecord) (CanonicalRefreshResult, error) {
			executed = true
			if job.RunID != "run_root" {
				t.Fatalf("canonical run = %q, want run_root", job.RunID)
			}
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo},
		CanonicalResultReconciler: func(_ context.Context, job JobRecord, result CanonicalRefreshResult) error {
			if job.RunID != "run_root" || result.PlanID != "plan-refresh" || result.ServingStateID != "generation-refresh" {
				t.Fatalf("canonical reconciliation input = job %#v, result %#v", job, result)
			}
			if repo.runStatuses["run_root"] != RunStatusSucceeded {
				t.Fatal("canonical result reconciled before durable completion")
			}
			reconciled = true
			return nil
		},
	}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if err != nil {
		t.Fatalf("execute canonical claimed job: %v", err)
	}
	if !executed || !reconciled || repo.runStatuses["run_root"] != RunStatusSucceeded || repo.runStatuses["run_child"] != RunStatusSucceeded {
		t.Fatalf("executed/reconciled/statuses = %v/%v/%#v", executed, reconciled, repo.runStatuses)
	}
}

func TestServiceExecuteClaimedJobCoordinatesCanonicalCompletion(t *testing.T) {
	repo := newFakeRepo()
	order := make([]string, 0, 3)
	canonicalCalls := 0
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo, canonicalCalls: &canonicalCalls},
		CanonicalCompletionCoordinator: func(_ context.Context, _ JobRecord, _ CanonicalRefreshResult, complete func() error) error {
			order = append(order, "before")
			if err := complete(); err != nil {
				return err
			}
			order = append(order, "after")
			return nil
		},
		CanonicalResultReconciler: func(_ context.Context, _ JobRecord, _ CanonicalRefreshResult) error {
			if repo.runStatuses["run_root"] != RunStatusSucceeded {
				t.Fatal("canonical result reconciled before durable completion")
			}
			order = append(order, "reconciler")
			return nil
		},
	}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if err != nil {
		t.Fatalf("execute coordinated canonical job: %v", err)
	}
	if got, want := strings.Join(order, ","), "before,after,reconciler"; got != want {
		t.Fatalf("canonical completion order = %q, want %q", got, want)
	}
	if canonicalCalls != 1 {
		t.Fatalf("canonical completion calls = %d, want 1", canonicalCalls)
	}
}

func TestServiceExecuteClaimedJobRejectsSkippedCanonicalCompletion(t *testing.T) {
	repo := newFakeRepo()
	reconciled := false
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo},
		CanonicalCompletionCoordinator: func(context.Context, JobRecord, CanonicalRefreshResult, func() error) error {
			return nil
		},
		CanonicalResultReconciler: func(context.Context, JobRecord, CanonicalRefreshResult) error {
			reconciled = true
			return nil
		},
	}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "completion callback was not invoked") {
		t.Fatalf("execute skipped canonical completion error = %v", err)
	}
	if reconciled {
		t.Fatal("canonical result reconciled without durable completion")
	}
}

func TestServiceExecuteClaimedJobReturnsCanonicalReconciliationFailure(t *testing.T) {
	repo := newFakeRepo()
	wantErr := errors.New("runtime cutover unavailable")
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{PlanID: "plan-refresh", ServingStateID: "generation-refresh"}, nil
		},
		Publication: fakePublication{repo: repo},
		CanonicalResultReconciler: func(context.Context, JobRecord, CanonicalRefreshResult) error {
			return wantErr
		},
	}
	err := service.ExecuteClaimedJob(t.Context(), JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	})
	if !errors.Is(err, wantErr) || !strings.Contains(err.Error(), "reconcile canonical refresh result") {
		t.Fatalf("canonical reconciliation error = %v, want %v", err, wantErr)
	}
}

func TestServiceExecuteClaimedJobSupersedesStaleCanonicalTree(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, ErrRunStale
		},
	}
	job := JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	}
	if err := service.ExecuteClaimedJob(t.Context(), job); !errors.Is(err, ErrRunStale) {
		t.Fatalf("ExecuteClaimedJob() error = %v, want stale", err)
	}
	if repo.runStatuses["run_root"] != RunStatusSuperseded || repo.runStatuses["run_child"] != RunStatusSuperseded {
		t.Fatalf("stale statuses = %#v, want superseded tree", repo.runStatuses)
	}
}

func TestServiceExecuteClaimedJobPropagatesSupersedeFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.supersedeErr = errors.New("supersede persistence unavailable")
	service := Service{
		Runs: repo,
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, ErrRunStale
		},
	}
	job := JobRecord{
		ID: "job_1", Identity: serviceIdentity, PrincipalID: "principal:test", EstimatedMemoryBytes: 64 << 20,
		RunID: "run_root", SemanticModelID: "sales", PipelineID: "sales-refresh", PipelinePlan: testPipelinePlan(serviceIdentity, "sales-refresh", "sales"),
		TargetType: TargetRefreshPipeline, TargetID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
		Kind: JobKindRefreshPipeline, LeaseOwner: "worker", LeaseRevision: 1,
	}
	err := service.ExecuteClaimedJob(t.Context(), job)
	if err == nil || !strings.Contains(err.Error(), "supersede stale refresh tree") || !strings.Contains(err.Error(), "supersede persistence unavailable") {
		t.Fatalf("ExecuteClaimedJob() error = %v, want supersede failure", err)
	}
}

func TestServiceQueuePipelineRefreshCreatesFullSemanticModelRun(t *testing.T) {
	repo := newFakeRepo()
	service := canonicalQueueService(repo)
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
		t.Fatalf("created runs = %#v, want pipeline root plus both Model tasks", repo.createdRuns)
	}
	if repo.createdRuns[0].SemanticModelID != "sales" || repo.createdRuns[0].TriggerType != TriggerManual {
		t.Fatalf("root input = %#v", repo.createdRuns[0])
	}
	if repo.createdRuns[0].TriggerID != "" || repo.createdRuns[0].PipelinePlan == nil || repo.createdRuns[0].PipelinePlan.ServingGenerationID != serviceIdentity.GenerationID || repo.createdRuns[0].PipelinePlan.Digest == "" {
		t.Fatalf("root plan evidence = %#v, want manual generation-bound plan", repo.createdRuns[0])
	}
	if repo.createdRuns[0].PipelinePlan.InvocationSource != TriggerManual || repo.createdRuns[0].PipelinePlan.ConcurrencyPolicy != "" {
		t.Fatalf("root effective policy = %#v", repo.createdRuns[0].PipelinePlan)
	}
	if got, want := repo.createdRuns[0].PipelinePlan.MaterializationScope, []string{"customers", "orders"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("materialization scope = %#v, want %#v", got, want)
	}
}

func TestServiceQueuePipelineRefreshTerminalReplayBypassesPreflight(t *testing.T) {
	repo := newFakeRepo()
	replayedIdentity := serviceIdentity
	replayedIdentity.GenerationID = "dep_original"
	wantRoot := RunRecord{
		ID:              "run_original",
		Identity:        replayedIdentity,
		PipelineID:      "sales-refresh",
		TargetType:      TargetRefreshPipeline,
		TargetID:        "sales-refresh",
		TriggerType:     TriggerManual,
		Status:          RunStatusSucceeded,
		PrincipalID:     "principal",
		SemanticModelID: "sales",
	}
	wantChildren := []RunRecord{{
		ID:          "run_original_child",
		Identity:    replayedIdentity,
		PipelineID:  "sales-refresh",
		TargetType:  TargetModel,
		TargetID:    "customers",
		TriggerType: TriggerDependency,
		Status:      RunStatusSucceeded,
		ParentRunID: wantRoot.ID,
	}}
	repo.idempotentRoot = wantRoot
	repo.idempotentChildren = wantChildren
	repo.idempotentReplay = true
	artifactLoads := 0
	activeResolves := 0
	targetRevisionResolves := 0
	sourceDigestResolves := 0
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     countingArtifactLoader{calls: &artifactLoads},
		ResolveActive: func(context.Context, projectgraph.ServingIdentity) (ServingState, error) {
			activeResolves++
			return ServingState{}, errors.New("unexpected active preflight")
		},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) {
			targetRevisionResolves++
			return 1, nil
		},
		ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
			sourceDigestResolves++
			return "sha256:" + strings.Repeat("c", 64), nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, errors.New("unexpected canonical preflight")
		},
		Publisher: &fakePublisher{},
	}

	got, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity:             serviceIdentity,
		PrincipalID:          "principal",
		EstimatedMemoryBytes: 1,
		PipelineID:           "sales-refresh",
		TriggerType:          TriggerManual,
		IdempotencyKey:       "terminal-replay",
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if !reflect.DeepEqual(got.Run, wantRoot) || !reflect.DeepEqual(got.DependencyRuns, wantChildren) {
		t.Fatalf("replayed result = %#v/%#v, want %#v/%#v", got.Run, got.DependencyRuns, wantRoot, wantChildren)
	}
	if got.ServingStateID != "dep_original" {
		t.Fatalf("replayed serving state = %q, want original generation dep_original", got.ServingStateID)
	}
	if repo.idempotentLookupCalls != 1 {
		t.Fatalf("idempotent lookup calls = %d, want 1", repo.idempotentLookupCalls)
	}
	if artifactLoads != 0 || activeResolves != 0 || targetRevisionResolves != 0 || sourceDigestResolves != 0 {
		t.Fatalf("preflight calls = active:%d targetRevision:%d sourceDigest:%d artifact:%d, want all zero", activeResolves, targetRevisionResolves, sourceDigestResolves, artifactLoads)
	}
	if len(repo.createdRuns) != 0 {
		t.Fatalf("created runs = %#v, want none on replay", repo.createdRuns)
	}
	if publisher, ok := service.Publisher.(*fakePublisher); !ok || len(publisher.targets) != 0 {
		t.Fatalf("publish callbacks = %#v, want none on replay", publisher)
	}
}

func TestServiceQueuePipelineRefreshIdempotencyConflictBypassesPreflight(t *testing.T) {
	repo := newFakeRepo()
	wantErr := errors.New("idempotency digest conflict")
	repo.idempotentErr = wantErr
	artifactLoads := 0
	activeResolves := 0
	targetRevisionResolves := 0
	sourceDigestResolves := 0
	service := Service{
		ServingStates: repo,
		Runs:          repo,
		Artifacts:     countingArtifactLoader{calls: &artifactLoads},
		ResolveActive: func(context.Context, projectgraph.ServingIdentity) (ServingState, error) {
			activeResolves++
			return ServingState{}, errors.New("unexpected active preflight")
		},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) {
			targetRevisionResolves++
			return 1, nil
		},
		ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
			sourceDigestResolves++
			return "sha256:" + strings.Repeat("c", 64), nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, errors.New("unexpected canonical preflight")
		},
	}

	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity:             serviceIdentity,
		PrincipalID:          "principal",
		EstimatedMemoryBytes: 1,
		PipelineID:           "sales-refresh",
		TriggerType:          TriggerManual,
		IdempotencyKey:       "digest-conflict",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("QueuePipelineRefresh() error = %v, want %v", err, wantErr)
	}
	if repo.idempotentLookupCalls != 1 {
		t.Fatalf("idempotent lookup calls = %d, want 1", repo.idempotentLookupCalls)
	}
	if artifactLoads != 0 || activeResolves != 0 || targetRevisionResolves != 0 || sourceDigestResolves != 0 {
		t.Fatalf("preflight calls = active:%d targetRevision:%d sourceDigest:%d artifact:%d, want all zero", activeResolves, targetRevisionResolves, sourceDigestResolves, artifactLoads)
	}
	if len(repo.createdRuns) != 0 {
		t.Fatalf("created runs = %#v, want none on digest conflict", repo.createdRuns)
	}
}

func TestServiceQueuePipelineRefreshAcceptsImplicitManualInvocation(t *testing.T) {
	repo := newFakeRepo()
	service := canonicalQueueService(repo)
	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual,
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v, want implicit manual admission", err)
	}
	if len(repo.createdRuns) != 3 {
		t.Fatalf("created runs = %#v, want implicit manual root plus dependencies", repo.createdRuns)
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
		Runs:                  repo,
		Artifacts:             fakeArtifactLoader{definition: refreshTestDefinition()},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 1, nil },
		ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
			return repo.activeArtifact.Digest, nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) { return CanonicalRefreshResult{}, nil },
	}
	result, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if !resolved || result.ServingStateID != repo.activeDeployment.ID {
		t.Fatalf("resolved = %v serving state = %q, want exact resolver and %q", resolved, result.ServingStateID, repo.activeDeployment.ID)
	}
}

func TestServiceQueuePipelineRefreshUsesActiveServingIdentity(t *testing.T) {
	repo := newFakeRepo()
	identity := serviceIdentity
	identity.GenerationID = string(repo.activeDeployment.ID)
	sourceDigest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	service := Service{
		ServingStates:         repo,
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 1, nil },
		ResolveSourceDigest: func(_ context.Context, got projectgraph.ServingIdentity) (string, error) {
			if got != identity {
				t.Fatalf("source digest identity = %#v, want %#v", got, identity)
			}
			return sourceDigest, nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) {
			return CanonicalRefreshResult{}, nil
		},
		Runs:      repo,
		Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	result, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
	})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if result.ServingStateID != repo.activeDeployment.ID || result.Run.Identity != identity {
		t.Fatalf("canonical refresh result = %#v, want active identity %#v", result, identity)
	}
	if result.Run.PipelinePlan == nil || result.Run.PipelinePlan.ArtifactDigest != sourceDigest {
		t.Fatalf("canonical pipeline plan = %#v, want source digest %q", result.Run.PipelinePlan, sourceDigest)
	}
}

func TestServiceQueuePipelineRefreshCarriesResolvedTargetRevision(t *testing.T) {
	repo := newFakeRepo()
	identity := serviceIdentity
	identity.GenerationID = string(repo.activeDeployment.ID)
	const sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	service := Service{
		ServingStates: repo,
		ResolveTargetRevision: func(_ context.Context, got projectgraph.ServingIdentity) (int64, error) {
			if got != identity {
				t.Fatalf("target revision identity = %#v, want %#v", got, identity)
			}
			return 7, nil
		},
		ResolveSourceDigest: func(_ context.Context, got projectgraph.ServingIdentity) (string, error) {
			if got != identity {
				t.Fatalf("source digest identity = %#v, want %#v", got, identity)
			}
			return sourceDigest, nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) { return CanonicalRefreshResult{}, nil },
		Runs:              repo, Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 1, PipelineID: "sales-refresh", TriggerType: TriggerManual})
	if err != nil {
		t.Fatalf("QueuePipelineRefresh() error = %v", err)
	}
	if len(repo.createdRuns) == 0 || repo.createdRuns[0].TargetRevision != 7 {
		var got int64
		if len(repo.createdRuns) > 0 {
			got = repo.createdRuns[0].TargetRevision
		}
		t.Fatalf("queued target revision = %d, want 7", got)
	}
}

func TestServiceQueuePipelineRefreshRejectsUnresolvedTargetRevision(t *testing.T) {
	repo := newFakeRepo()
	identity := serviceIdentity
	identity.GenerationID = string(repo.activeDeployment.ID)
	service := Service{
		ServingStates:         repo,
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 0, nil },
		ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
			return "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) { return CanonicalRefreshResult{}, nil },
		Runs:              repo, Artifacts: fakeArtifactLoader{definition: refreshTestDefinition()},
	}
	if _, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{Identity: identity, PrincipalID: "principal", EstimatedMemoryBytes: 1, PipelineID: "sales-refresh", TriggerType: TriggerManual}); err == nil || !strings.Contains(err.Error(), "revision must be positive") {
		t.Fatalf("QueuePipelineRefresh() error = %v, want unresolved target revision", err)
	}
}

func TestServiceQueuePipelineRefreshRejectsMismatchedActiveResolver(t *testing.T) {
	repo := newFakeRepo()
	service := Service{
		ServingStates: repo,
		ResolveActive: func(context.Context, projectgraph.ServingIdentity) (ServingState, error) {
			wrong := repo.activeDeployment
			wrong.ID = "other-generation"
			return ServingState{State: wrong, Artifact: repo.activeArtifact}, nil
		},
		Runs:                  repo,
		Artifacts:             fakeArtifactLoader{definition: refreshTestDefinition()},
		ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 1, nil },
		ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
			return repo.activeArtifact.Digest, nil
		},
		CanonicalExecutor: func(context.Context, JobRecord) (CanonicalRefreshResult, error) { return CanonicalRefreshResult{}, nil },
	}
	_, err := service.QueuePipelineRefresh(t.Context(), QueuePipelineInput{
		Identity: serviceIdentity, PrincipalID: "principal", EstimatedMemoryBytes: 64 << 20,
		PipelineID: "sales-refresh", TriggerType: TriggerManual, TriggerID: "manual",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("QueuePipelineRefresh() error = %v, want exact identity mismatch", err)
	}
	if len(repo.createdRuns) != 0 {
		t.Fatalf("created runs = %#v, want none", repo.createdRuns)
	}
}

func TestServiceQueuePipelineRefreshRejectsSupersededScheduledArtifact(t *testing.T) {
	repo := newFakeRepo()
	service := canonicalQueueService(repo)
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

func refreshTestDefinition() *artifact.Definition {
	return &artifact.Definition{Pipelines: map[string]refreshschedule.Definition{
		"sales-refresh": {ID: "sales-refresh", Name: "sales-refresh", SemanticModelID: "sales", SelectionDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Timezone: "UTC", ConcurrencyPolicy: refreshschedule.ConcurrencyReplace, Schedules: []refreshschedule.Schedule{{ID: "daily", Expression: "0 6 * * *"}}},
	}, ModelTables: map[string]semanticmodel.Table{
		"customers": {},
		"orders":    {ModelDependencies: []string{"customers"}},
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
					Entities:   map[string]semanticmodel.EntityDefinition{"customer_id": {Type: "primary", Fields: []string{"customer_id"}}},
					Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
				},
				"orders": {
					ModelName: "orders", GrainEntity: "order_id", ModelDependencies: []string{"customers"},
					Entities:   map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}},
					Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}},
				},
			},
		},
	}}
}

type fakeRepo struct {
	activeErr             error
	activeDeployment      servingstate.State
	activeArtifact        servingstate.Artifact
	runStatuses           map[string]string
	createdRuns           []RunInput
	idempotentRoot        RunRecord
	idempotentChildren    []RunRecord
	idempotentReplay      bool
	idempotentErr         error
	idempotentLookupCalls int
	supersedeErr          error
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
			Digest:           "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ManifestJSON:     "{}",
		},
		activeArtifact: servingstate.Artifact{
			ServingStateID: "dep_active",
			Digest:         "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
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

func (r *fakeRepo) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return r.activeDeployment, nil
}

func (r *fakeRepo) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return r.activeArtifact, nil
}

func (r *fakeRepo) CreateRun(_ context.Context, input RunInput) (RunRecord, error) {
	r.createdRuns = append(r.createdRuns, input)
	id := "run_root"
	if input.ParentRunID != "" {
		id = "run_child"
	}
	r.runStatuses[id] = RunStatusQueued
	result := RunRecord{ID: id, Identity: input.Identity, SemanticModelID: input.SemanticModelID, PipelineID: input.PipelineID, PipelinePlan: input.PipelinePlan, TriggerID: input.TriggerID, NominalTime: input.NominalTime, PrincipalID: input.PrincipalID, TargetType: input.TargetType, TargetID: input.TargetID, TriggerType: input.TriggerType, ParentRunID: input.ParentRunID}
	if input.PipelinePlan != nil {
		result.PlanDigest = input.PipelinePlan.Digest
		result.MaterializationScope = append([]string(nil), input.PipelinePlan.MaterializationScope...)
	}
	return result, nil
}

func (r *fakeRepo) CreateRunTree(ctx context.Context, tree RunTreeInput) (RunRecord, []RunRecord, error) {
	root, err := r.CreateRun(ctx, tree.Root)
	if err != nil {
		return RunRecord{}, nil, err
	}
	children := make([]RunRecord, 0, len(tree.DependencyTargets))
	for _, targetID := range tree.DependencyTargets {
		child, childErr := r.CreateRun(ctx, RunInput{Identity: root.Identity, SemanticModelID: tree.Root.SemanticModelID, PipelineID: tree.Root.PipelineID, PipelinePlan: tree.Root.PipelinePlan, InvocationSource: tree.Root.InvocationSource, MatchingScheduleIDs: append([]string(nil), tree.Root.MatchingScheduleIDs...), TriggerID: tree.Root.TriggerID, NominalTime: tree.Root.NominalTime, PrincipalID: tree.Root.PrincipalID, GroupIDs: append([]string(nil), tree.Root.GroupIDs...), EstimatedMemoryBytes: tree.Root.EstimatedMemoryBytes, TargetType: TargetModel, TargetID: targetID, TargetRevision: root.TargetRevision, TriggerType: TriggerDependency, ParentRunID: root.ID, JobKind: JobKindChildRun})
		if childErr != nil {
			return RunRecord{}, nil, childErr
		}
		children = append(children, child)
	}
	return root, children, nil
}

func (r *fakeRepo) LookupIdempotentRun(_ context.Context, _ projectgraph.ServingIdentity, _ projectgraph.ResourceID, _, _ string) (RunRecord, []RunRecord, bool, error) {
	r.idempotentLookupCalls++
	if r.idempotentErr != nil {
		return RunRecord{}, nil, false, r.idempotentErr
	}
	return r.idempotentRoot, append([]RunRecord(nil), r.idempotentChildren...), r.idempotentReplay, nil
}

func (r *fakeRepo) ListChildRuns(context.Context, ReadScope, string) ([]RunRecord, error) {
	return []RunRecord{{ID: "run_child", Identity: serviceIdentity, TargetType: TargetModel, TargetID: "customers"}}, nil
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

func (r *fakeRepo) MarkRunTreeSupersededClaimed(_ context.Context, job JobRecord, _ string) error {
	if r.supersedeErr != nil {
		return r.supersedeErr
	}
	r.runStatuses[job.RunID] = RunStatusSuperseded
	r.runStatuses["run_child"] = RunStatusSuperseded
	return nil
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

type countingArtifactLoader struct {
	calls *int
}

func (l countingArtifactLoader) Load(context.Context, servingstate.Artifact) (LoadedArtifact, error) {
	if l.calls != nil {
		*l.calls = *l.calls + 1
	}
	return LoadedArtifact{}, errors.New("unexpected artifact preflight")
}

type fakePublication struct {
	repo           *fakeRepo
	canonicalCalls *int
}

func (p fakePublication) CompleteCanonicalRefresh(_ context.Context, _ JobRecord, _ CanonicalRefreshResult) error {
	if p.canonicalCalls != nil {
		*p.canonicalCalls++
	}
	p.repo.runStatuses["run_root"] = RunStatusSucceeded
	p.repo.runStatuses["run_child"] = RunStatusSucceeded
	return nil
}

type fakePublisher struct {
	targets []string
}

func (p *fakePublisher) PublishRefreshTarget(_ context.Context, _ projectgraph.ServingIdentity, _ string, targetID projectgraph.ResourceID) {
	p.targets = append(p.targets, targetID.String())
}
