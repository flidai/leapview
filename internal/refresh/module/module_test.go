package module

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
)

type generatedRefreshAPI interface {
	CreateRefreshRun(http.ResponseWriter, *http.Request, string)
	ListRefreshRuns(http.ResponseWriter, *http.Request, string)
	GetRefreshRun(http.ResponseWriter, *http.Request, string, string)
	CancelRefreshRun(http.ResponseWriter, *http.Request, string, string)
	ListRefreshRunEvents(http.ResponseWriter, *http.Request, string, string, *int32, *string)
}

var testRefreshWorkflow = jobs.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error { return nil })

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

func TestRefreshCreateVerifiesPersistedLifecycleWithoutAppendingDuplicate(t *testing.T) {
	contract, ok := refreshgen.GetAPIGenCommandRuntimeContract(string(refreshgen.GenOperationCreateRefreshRun))
	if !ok || contract.Execution == nil {
		t.Fatal("generated refresh execution contract is unavailable")
	}
	store := &refreshEventStore{events: []jobs.Event{{EventType: contract.Execution.InitialEvent}}}
	module, err := Build(t.Context(), Config{Events: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, guard, err := apigencommand.Begin(t.Context(), contract)
	if err != nil {
		t.Fatal(err)
	}
	run := refreshrun.RunRecord{ID: "run-1", WorkspaceID: "sales", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales.daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := module.verifyRunCreated(ctx, run); err != nil {
		t.Fatal(err)
	}
	if !guard.Completed() || store.appends != 0 {
		t.Fatalf("guard completed=%t, duplicate appends=%d", guard.Completed(), store.appends)
	}
}

func TestRefreshCreatePersistedAuditVerificationFailureIsBestEffort(t *testing.T) {
	var logs bytes.Buffer
	module, err := Build(t.Context(), Config{
		Events: &refreshEventStore{err: errors.New("event store unavailable")},
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	run := refreshrun.RunRecord{ID: "run-audit-failure", WorkspaceID: "sales", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "sales.daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := module.verifyRunCreated(t.Context(), run); err != nil {
		t.Fatalf("best-effort verification changed command result: %v", err)
	}
	for _, expected := range []string{"refresh persisted audit verification failed", "operation_id=createRefreshRun", "event store unavailable"} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("verification log = %q, missing %q", logs.String(), expected)
		}
	}
}

func TestBuildConstructsOwnedHTTPHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{HTTP: HTTPConfig{
		WorkspaceID: func(value string) string { return "resolved-" + value },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := module.HTTP().WorkspaceID("sales"); got != "resolved-sales" {
		t.Fatalf("workspace = %q", got)
	}
}

func TestReconcileProjectsPublishedServingStateIntoRefreshDataVersions(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, workspace_id, environment, status) VALUES ('state_1', 'sales', 'prod', 'active')`); err != nil {
		t.Fatalf("insert serving state: %v", err)
	}
	states := reconciliationStates{
		state: servingstate.State{
			ID: "state_1", WorkspaceID: "sales", Environment: "prod", Source: servingstate.SourcePublish,
			DuckLakeSnapshotID: 42, ActivatedAt: "2026-07-22T12:00:00Z",
		},
		artifact: servingstate.Artifact{Digest: "sha256:artifact"},
	}
	publisher := &versionPublisher{}
	module, err := Build(t.Context(), Config{
		Database: store.SQLDB(), Environment: "prod", Workflow: testRefreshWorkflow,
		Service: refreshrun.Service{
			ServingStates: states,
			Artifacts: artifactLoaderFunc(func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
				return refreshrun.LoadedArtifact{Definition: &artifact.Definition{
					Models: map[string]*semanticmodel.Model{"orders": {}},
					Pipelines: map[string]refreshschedule.Definition{
						"daily": {ID: "daily", SemanticModel: "orders"},
					},
				}}, nil
			}),
			Publisher: publisher,
		},
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := module.Reconcile(t.Context()); err != nil {
		t.Fatalf("reconcile schedules: %v", err)
	}
	version, found, err := module.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found {
		t.Fatalf("data version = %#v, %v, %v", version, found, err)
	}
	if version.SnapshotID != 42 || version.ServingStateID != "state_1" || version.Source != refreshschedule.DataVersionSourcePublish {
		t.Fatalf("unexpected data version: %#v", version)
	}
	if got := publisher.modelID; got != "orders" {
		t.Fatalf("published model = %q, want orders", got)
	}
}

type reconciliationStates struct {
	state    servingstate.State
	artifact servingstate.Artifact
}

func (s reconciliationStates) ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error) {
	return []servingstate.ActiveScope{{WorkspaceID: s.state.WorkspaceID, Environment: s.state.Environment}}, nil
}
func (s reconciliationStates) ActiveArtifact(context.Context, servingstate.WorkspaceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return s.state, s.artifact, nil
}
func (s reconciliationStates) Create(context.Context, servingstate.CreateInput) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) SaveValidated(context.Context, servingstate.ID, servingstate.Validation, servingstate.Artifact) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return servingstate.Artifact{}, nil
}
func (s reconciliationStates) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}
func (s reconciliationStates) Activate(context.Context, servingstate.WorkspaceID, servingstate.Environment, servingstate.ID) (servingstate.State, error) {
	return servingstate.State{}, nil
}
func (s reconciliationStates) MarkFailed(context.Context, servingstate.ID, error) error { return nil }

type artifactLoaderFunc func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error)

func (f artifactLoaderFunc) Load(ctx context.Context, artifact servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	return f(ctx, artifact)
}

type versionPublisher struct{ modelID string }

func (*versionPublisher) PublishRefreshTarget(context.Context, projectgraph.ServingIdentity, string, projectgraph.ResourceID) {
}
func (p *versionPublisher) PublishSemanticModelVersion(_ context.Context, _, _, modelID string) {
	p.modelID = modelID
}

func TestActiveScopesMatchModuleEnvironment(t *testing.T) {
	scopes := []servingstate.ActiveScope{
		{WorkspaceID: "sales", Environment: "dev"},
		{WorkspaceID: "sales", Environment: "prod"},
		{WorkspaceID: "support", Environment: "prod"},
	}
	got := activeScopes(scopes, "prod")
	want := []servingstate.ActiveScope{
		{WorkspaceID: "sales", Environment: "prod"},
		{WorkspaceID: "support", Environment: "prod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activeScopes() = %#v, want %#v", got, want)
	}
}

func TestBuildConstructsCapabilityPrivatePersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	module, err := Build(t.Context(), Config{Database: store.SQLDB(), Workflow: testRefreshWorkflow})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if _, err := module.ListRuns(t.Context(), "sales", refreshrun.RunPage{Limit: 10, Environment: "prod"}); err != nil {
		t.Fatalf("list runs through module surface: %v", err)
	}
	repository, err := module.HTTP().Repository()
	if err != nil || repository == nil {
		t.Fatalf("HTTP repository delegation = %T, %v", repository, err)
	}
}

type dispatcherFunc func(context.Context)

func (f dispatcherFunc) Run(ctx context.Context) { f(ctx) }

type schedulerFunc func(context.Context) error

func (f schedulerFunc) DispatchDue(ctx context.Context) error { return f(ctx) }

func TestDispatchCoalescesConcurrentRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	module, err := Build(t.Context(), Config{Dispatcher: dispatcherFunc(func(context.Context) {
		calls.Add(1)
		close(entered)
		<-release
	})})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}

	module.Dispatch(t.Context())
	<-entered
	for range 8 {
		module.Dispatch(t.Context())
	}
	close(release)
	if err := module.Stop(t.Context()); err != nil {
		t.Fatalf("stop module: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", got)
	}
}

func TestStartOwnsSchedulerLifecycle(t *testing.T) {
	reconciled := make(chan struct{}, 1)
	dispatched := make(chan struct{}, 4)
	module, err := Build(t.Context(), Config{
		ReconcileSchedules: func(context.Context) error {
			reconciled <- struct{}{}
			return nil
		},
		Scheduler: schedulerFunc(func(context.Context) error {
			dispatched <- struct{}{}
			return nil
		}),
		ScheduleInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := module.Start(ctx); err != nil {
		t.Fatalf("start module: %v", err)
	}
	select {
	case <-reconciled:
	case <-time.After(time.Second):
		t.Fatal("schedule reconciliation did not run")
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("initial schedule dispatch did not run")
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatalf("stop module: %v", err)
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatalf("second stop: %v", err)
	}
}

func TestStartRedispatchesAfterLeaseWindow(t *testing.T) {
	dispatched := make(chan struct{}, 2)
	module, err := Build(t.Context(), Config{
		Dispatcher: dispatcherFunc(func(context.Context) {
			dispatched <- struct{}{}
		}),
		LeaseTimeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatalf("start module: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		select {
		case <-dispatched:
		case <-time.After(time.Second):
			t.Fatalf("dispatcher call %d did not run", attempt)
		}
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatalf("stop module: %v", err)
	}
}

func TestStopHonorsCancellationWhileWorkerDrains(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	module, err := Build(t.Context(), Config{Dispatcher: dispatcherFunc(func(context.Context) {
		close(entered)
		<-release
	})})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	module.Dispatch(t.Context())
	<-entered

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := module.Stop(ctx); err == nil {
		t.Fatal("expected cancelled stop to report its context error")
	}
	close(release)
	if err := module.Stop(t.Context()); err != nil {
		t.Fatalf("finish stop: %v", err)
	}
}
