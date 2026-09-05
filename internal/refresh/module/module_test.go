package module

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

type generatedRefreshAPI interface {
	CreateRefreshRun(http.ResponseWriter, *http.Request, string, string)
	ListRefreshRuns(http.ResponseWriter, *http.Request, string)
	GetRefreshRun(http.ResponseWriter, *http.Request, string, string)
	CancelRefreshRun(http.ResponseWriter, *http.Request, string, string, string)
	ListRefreshRunEvents(http.ResponseWriter, *http.Request, string, string, *int32, *string)
}

var _ generatedRefreshAPI = (*Module)(nil)

type refreshEventStore struct {
	eventType string
	data      []byte
	events    []jobs.Event
	err       error
	appends   int
}

func (s *refreshEventStore) AppendEvent(_ context.Context, _, _, eventType string, data []byte) (jobs.Event, error) {
	s.appends++
	s.eventType, s.data = eventType, data
	return jobs.Event{EventType: eventType, Data: data}, s.err
}
func (s *refreshEventStore) ListEvents(context.Context, string, string, int64, int) ([]jobs.Event, error) {
	return append([]jobs.Event(nil), s.events...), s.err
}
func testAuthorization() AuthorizationConfig {
	return AuthorizationConfig{AuthorizeObject: func(context.Context, string, access.Capability, access.ResourceRef) (bool, error) { return true, nil }}
}

func TestRefreshCreateDurableAuditDoesNotAppendPostCommitDuplicate(t *testing.T) {
	contract, ok := refreshgen.GetAPIGenCommandRuntimeContract(string(refreshgen.GenOperationCreateRefreshRun))
	if !ok || contract.Execution == nil {
		t.Fatal("generated refresh execution contract is unavailable")
	}
	store := &refreshEventStore{events: []jobs.Event{{EventType: contract.Execution.InitialEvent}}}
	m := &Module{events: store, durableAudit: true}
	run := refreshrun.RunRecord{ID: "run-1", Identity: projectgraphIdentity("sales", "dev", "generation"), SemanticModelID: "orders", PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := m.verifyRunCreated(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if store.appends != 0 {
		t.Fatalf("duplicate appends=%d", store.appends)
	}
}

func TestRefreshCreateDurableAuditSkipsPostCommitVerificationFailure(t *testing.T) {
	var logs bytes.Buffer
	m := &Module{events: &refreshEventStore{err: errors.New("event store unavailable")}, logger: slog.New(slog.NewTextHandler(&logs, nil)), durableAudit: true}
	run := refreshrun.RunRecord{ID: "run-audit-failure", Identity: projectgraphIdentity("sales", "dev", "generation"), SemanticModelID: "orders", PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := m.verifyRunCreated(t.Context(), run); err != nil {
		t.Fatalf("durable audit verification changed command result: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("post-commit verification log = %q", logs.String())
	}
}

func TestBuildConstructsOwnedHTTPHandler(t *testing.T) {
	m, err := Build(t.Context(), Config{Authorization: testAuthorization(), HTTP: HTTPConfig{ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) {
		return projectgraphIdentity("sales", "dev", "generation"), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if m.HTTP().ServingIdentity == nil {
		t.Fatal("serving identity resolver missing")
	}
}
func TestBuildRequiresCanonicalAuthorizer(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("Build accepted a missing canonical authorizer")
	}
}
func TestBuildProductionRejectsUnmarkedPersistence(t *testing.T) {
	_, err := Build(t.Context(), Config{Persistence: &Persistence{}, Production: true, Authorization: testAuthorization()})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
		t.Fatalf("unmarked production refresh build error = %v", err)
	}
}
func TestBuildRejectsInvalidRecoveryLifecycleConfiguration(t *testing.T) {
	if _, err := Build(t.Context(), Config{Authorization: testAuthorization(), RecoveryLifecycle: &RecoveryLifecycle{}}); err == nil {
		t.Fatal("Build accepted an invalid recovery lifecycle")
	}
}

func TestAssetRefreshStateMarksMissingPersistenceUnavailable(t *testing.T) {
	m, err := Build(t.Context(), Config{Authorization: testAuthorization()})
	if err != nil {
		t.Fatal(err)
	}
	state, err := m.AssetRefreshState(t.Context(), "project_sales", "dev", "pipeline_daily", "semantic_sales")
	if err != nil || !state.Unavailable {
		t.Fatalf("asset state = %#v, err=%v", state, err)
	}
}

func TestAssetRefreshStateReadsScopedRunsAndDataVersion(t *testing.T) {
	identity := projectgraphIdentity("project_sales", "dev", "generation_a")
	run := refreshrun.RunRecord{ID: "run_1", Identity: identity, SemanticModelID: "semantic_sales", PipelineID: "pipeline_daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "pipeline_daily", TargetRevision: 3, Status: refreshrun.RunStatusSucceeded}
	m := &Module{runs: &testRunPersistence{targetRuns: []refreshrun.RunRecord{run}, latest: run}, schedules: &testScheduleRepository{versions: map[string]refreshschedule.DataVersion{"generation_a/orders": {Identity: identity, SemanticModelID: "orders", SnapshotID: 42, RefreshedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), Source: refreshschedule.DataVersionSourceRefresh}}, next: time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)}, service: refreshrun.Service{ServingStates: reconciliationStates{state: servingstate.State{ID: "generation_a", ProjectID: "project_sales", Environment: "dev"}}}}
	state, err := m.AssetRefreshState(t.Context(), "project_sales", "dev", "pipeline_daily", "orders")
	if err != nil || len(state.Runs) != 1 || state.LatestSuccessful.ID != "run_1" || state.DataVersion.SnapshotID != 42 {
		t.Fatalf("asset state = %#v, err=%v", state, err)
	}
}

func TestCancelPipelineRefreshForUIDeniesCrossPipelineRun(t *testing.T) {
	run := refreshrun.RunRecord{ID: "run_1", Identity: projectgraphIdentity("project_sales", "dev", "generation_a"), PipelineID: "pipeline_daily", TargetID: "pipeline_daily", TargetType: refreshrun.TargetRefreshPipeline, Status: refreshrun.RunStatusQueued}
	fake := &testRunPersistence{run: run}
	m := &Module{runs: fake}
	if err := m.CancelPipelineRefreshForUI(t.Context(), run.Identity, "pipeline_other", "run_1", "user:test"); err == nil {
		t.Fatal("cross-pipeline cancel unexpectedly succeeded")
	}
	if fake.cancelled {
		t.Fatal("cross-pipeline cancel changed run state")
	}
}

func TestReconcileProjectsPublishedServingStateIntoRefreshDataVersions(t *testing.T) {
	schedules := &testScheduleRepository{}
	m := reconcileTestModule(schedules, servingstate.SourcePublish, 42, nil)
	if err := m.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	version, found := schedules.version("state_1/orders")
	if !found || version.SnapshotID != 42 || version.Source != refreshschedule.DataVersionSourcePublish {
		t.Fatalf("version = %#v, found=%v", version, found)
	}
}

func TestReconcileUsesCanonicalPublishedVersionWithoutRefreshDataVersion(t *testing.T) {
	schedules := &testScheduleRepository{}
	m := reconcileTestModule(schedules, servingstate.SourcePublish, 42, func(context.Context, projectgraph.ServingIdentity) (PublishedDataVersion, bool, error) {
		return PublishedDataVersion{SnapshotID: 42, RefreshedAt: time.Now()}, true, nil
	})
	if err := m.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, found := schedules.version("state_1/orders"); found {
		t.Fatal("canonical publication created refresh data version")
	}
}

func TestReconcileRejectsCanonicalPublishedSnapshotDrift(t *testing.T) {
	schedules := &testScheduleRepository{}
	m := reconcileTestModule(schedules, servingstate.SourcePublish, 42, func(context.Context, projectgraph.ServingIdentity) (PublishedDataVersion, bool, error) {
		return PublishedDataVersion{SnapshotID: 99, RefreshedAt: time.Now()}, true, nil
	})
	if err := m.Reconcile(t.Context()); err == nil || !strings.Contains(err.Error(), "differs from serving-state snapshot") {
		t.Fatalf("reconcile error = %v", err)
	}
}

func TestReconcileRejectsMultipleActiveServingScopes(t *testing.T) {
	m := reconcileTestModule(&testScheduleRepository{}, servingstate.SourcePublish, 42, nil)
	m.service.ServingStates = reconciliationStates{scopes: []servingstate.ActiveScope{{ProjectID: "sales", Environment: "prod"}, {ProjectID: "sales", Environment: "dev"}}}
	if err := m.Reconcile(t.Context()); err == nil {
		t.Fatal("Reconcile accepted multiple active serving scopes")
	}
}

func TestDataVersionUsesCanonicalRuntimeIdentity(t *testing.T) {
	identity := projectgraphIdentity("sales", "prod", "state-new")
	schedules := &testScheduleRepository{versions: map[string]refreshschedule.DataVersion{"state-new/orders": {Identity: identity, SemanticModelID: "orders", SnapshotID: 84, Source: refreshschedule.DataVersionSourceRefresh}}}
	m := &Module{schedules: schedules, resolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) { return identity, nil }}
	version, found, err := m.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found || version.SnapshotID != 84 || version.ServingStateID != "state-new" {
		t.Fatalf("version=%#v found=%v err=%v", version, found, err)
	}
}

func TestDataVersionFallsBackToCanonicalPublicationAndPrefersRefresh(t *testing.T) {
	identity := projectgraphIdentity("sales", "prod", "state-new")
	schedules := &testScheduleRepository{}
	m := &Module{schedules: schedules, resolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) { return identity, nil }, publishedVersion: func(context.Context, projectgraph.ServingIdentity) (PublishedDataVersion, bool, error) {
		return PublishedDataVersion{SnapshotID: 41, RefreshedAt: time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)}, true, nil
	}}
	version, found, err := m.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found || version.SnapshotID != 41 || version.Source != refreshschedule.DataVersionSourcePublish {
		t.Fatalf("published version=%#v found=%v err=%v", version, found, err)
	}
	schedules.versions = map[string]refreshschedule.DataVersion{"state-new/orders": {Identity: identity, SemanticModelID: "orders", SnapshotID: 84, RefreshedAt: time.Now(), Source: refreshschedule.DataVersionSourceRefresh}}
	version, found, err = m.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found || version.SnapshotID != 84 || version.Source != refreshschedule.DataVersionSourceRefresh {
		t.Fatalf("refresh version=%#v found=%v err=%v", version, found, err)
	}
}

func TestStartOwnsSchedulerLifecycle(t *testing.T) {
	reconciled, dispatched := make(chan struct{}, 1), make(chan struct{}, 2)
	m, err := Build(t.Context(), Config{Authorization: testAuthorization(), ReconcileSchedules: func(context.Context) error { reconciled <- struct{}{}; return nil }, Scheduler: schedulerFunc(func(context.Context) error { dispatched <- struct{}{}; return nil }), ScheduleInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not run")
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not run")
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// Small in-memory stores keep module orchestration tests independent of any
// database adapter. They implement only the calls exercised by these tests;
// embedded contracts make missing methods fail closed if production expands.
type testRunPersistence struct {
	targetRuns []refreshrun.RunRecord
	latest     refreshrun.RunRecord
	run        refreshrun.RunRecord
	cancelled  bool
}

func (*testRunPersistence) Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (*testRunPersistence) CreateRun(context.Context, refreshrun.RunInput) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) CreateRunTree(context.Context, refreshrun.RunTreeInput) (refreshrun.RunRecord, []refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil, nil
}
func (*testRunPersistence) ListRuns(context.Context, refreshrun.ReadScope, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (*testRunPersistence) ListChildRuns(context.Context, refreshrun.ReadScope, string) ([]refreshrun.RunRecord, error) {
	return nil, nil
}
func (*testRunPersistence) LatestTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return refreshrun.RunRecord{}, false, nil
}
func (*testRunPersistence) MarkRunRunning(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) MarkRunSucceeded(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) MarkRunFailed(context.Context, projectgraph.ServingIdentity, string, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) MarkRunPrepared(context.Context, refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) RunMayPublish(context.Context, refreshrun.JobRecord) (bool, error) {
	return true, nil
}
func (*testRunPersistence) ListExecutableJobs(context.Context, refreshrun.ReadScope, int) ([]refreshrun.JobRecord, error) {
	return nil, nil
}
func (*testRunPersistence) ClaimExecutableJob(context.Context, refreshrun.JobRecord, string, time.Duration) (refreshrun.JobRecord, bool, error) {
	return refreshrun.JobRecord{}, false, nil
}
func (*testRunPersistence) RenewJobLease(context.Context, refreshrun.JobRecord, time.Duration) error {
	return nil
}
func (*testRunPersistence) JobQueueStats(context.Context, refreshrun.ReadScope) (refreshrun.JobQueueStats, error) {
	return refreshrun.JobQueueStats{}, nil
}
func (*testRunPersistence) MarkRunSucceededClaimed(context.Context, refreshrun.JobRecord) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) MarkRunFailedClaimed(context.Context, refreshrun.JobRecord, string) (refreshrun.RunRecord, error) {
	return refreshrun.RunRecord{}, nil
}
func (*testRunPersistence) MarkRunTreeFailedClaimed(context.Context, refreshrun.JobRecord, string) error {
	return nil
}
func (*testRunPersistence) MarkRunTreeSupersededClaimed(context.Context, refreshrun.JobRecord, string) error {
	return nil
}
func (*testRunPersistence) CheckInvocationAdmission(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID, string) error {
	return nil
}
func (*testRunPersistence) CheckScheduledInvocationAdmission(context.Context, refreshschedule.Occurrence) error {
	return nil
}

func (p *testRunPersistence) ListTargetRuns(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return p.targetRuns, nil
}
func (p *testRunPersistence) LatestSuccessfulTargetRun(context.Context, refreshrun.ReadScope, string, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return p.latest, p.latest.ID != "", nil
}
func (p *testRunPersistence) ListSemanticModelRuns(context.Context, refreshrun.ReadScope, projectgraph.ResourceID, refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	return p.targetRuns, nil
}
func (p *testRunPersistence) LatestSuccessfulSemanticModelRun(context.Context, refreshrun.ReadScope, projectgraph.ResourceID) (refreshrun.RunRecord, bool, error) {
	return p.latest, p.latest.ID != "", nil
}
func (p *testRunPersistence) GetRun(context.Context, refreshrun.ReadScope, string) (refreshrun.RunRecord, error) {
	return p.run, nil
}
func (p *testRunPersistence) CancelRun(context.Context, projectgraph.ServingIdentity, string) (refreshrun.RunRecord, error) {
	p.cancelled = true
	return p.run, nil
}
func (p *testRunPersistence) CancelRunWithAudit(context.Context, projectgraph.ServingIdentity, string, *access.AuditIntent) (refreshrun.RunRecord, error) {
	p.cancelled = true
	return p.run, nil
}

type testScheduleRepository struct {
	versions map[string]refreshschedule.DataVersion
	next     time.Time
}

func (r *testScheduleRepository) Reconcile(context.Context, refreshschedule.ReconcileInput) error {
	return nil
}
func (r *testScheduleRepository) ClaimDue(context.Context, projectgraph.ServingIdentity, time.Time) ([]refreshschedule.Occurrence, error) {
	return nil, nil
}
func (r *testScheduleRepository) ReleaseOccurrence(context.Context, refreshschedule.Occurrence) error {
	return nil
}
func (r *testScheduleRepository) NextRun(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID) (time.Time, bool, error) {
	return r.next, !r.next.IsZero(), nil
}
func (r *testScheduleRepository) SaveDataVersion(_ context.Context, version refreshschedule.DataVersion) error {
	if r.versions == nil {
		r.versions = map[string]refreshschedule.DataVersion{}
	}
	r.versions[version.Identity.GenerationID+"/"+version.SemanticModelID.String()] = version
	return nil
}
func (r *testScheduleRepository) DataVersion(_ context.Context, identity projectgraph.ServingIdentity, model projectgraph.ResourceID) (refreshschedule.DataVersion, bool, error) {
	v, ok := r.versions[identity.GenerationID+"/"+model.String()]
	return v, ok, nil
}
func (r *testScheduleRepository) version(key string) (refreshschedule.DataVersion, bool) {
	v, ok := r.versions[key]
	return v, ok
}

func reconcileTestModule(schedules *testScheduleRepository, source servingstate.Source, snapshot int64, published PublishedDataVersionResolver) *Module {
	return &Module{schedules: schedules, publishedVersion: published, service: refreshrun.Service{ServingStates: reconciliationStates{state: servingstate.State{ID: "state_1", ProjectID: "sales", Environment: "prod", Source: source, DuckLakeSnapshotID: snapshot, ActivatedAt: "2026-07-22T12:00:00Z"}, artifact: servingstate.Artifact{Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}}, Artifacts: artifactLoaderFunc(func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
		return refreshrun.LoadedArtifact{Definition: &artifact.Definition{Models: map[string]*semanticmodel.Model{"orders": {}}, Pipelines: map[string]refreshschedule.Definition{"daily": {ID: "daily", SemanticModelID: "orders"}}}}, nil
	})}}
}

func projectgraphIdentity(project, environment, generation string) projectgraph.ServingIdentity {
	return projectgraph.ServingIdentity{ProjectID: projectgraph.ResourceID(project), Environment: environment, GenerationID: generation}
}

type reconciliationStates struct {
	state    servingstate.State
	artifact servingstate.Artifact
	scopes   []servingstate.ActiveScope
}

func (s reconciliationStates) ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error) {
	if s.scopes != nil {
		return s.scopes, nil
	}
	return []servingstate.ActiveScope{{ProjectID: s.state.ProjectID, Environment: s.state.Environment}}, nil
}
func (s reconciliationStates) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return s.state, s.artifact, nil
}
func (s reconciliationStates) Create(context.Context, servingstate.CreateInput) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return s.state, nil
}
func (s reconciliationStates) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return s.artifact, nil
}
func (s reconciliationStates) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}
func (s reconciliationStates) Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) MarkFailed(context.Context, servingstate.ID, error) error { return nil }

type artifactLoaderFunc func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error)

func (f artifactLoaderFunc) Load(ctx context.Context, a servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	return f(ctx, a)
}

type schedulerFunc func(context.Context) error

func (f schedulerFunc) DispatchDue(ctx context.Context) error { return f(ctx) }
