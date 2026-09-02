package module

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	"github.com/flidai/leapview/internal/refresh/artifact"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
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

var testRefreshWorkflow = jobplatform.WorkflowRecorderFunc(func(context.Context, transaction.Transaction, jobs.WorkflowIntent) error { return nil })

func sqlitePersistence(t *testing.T, database *sql.DB) *Persistence {
	t.Helper()
	persistence, err := NewSQLitePersistence(SQLitePersistenceConfig{
		Database: database,
		Workflow: testRefreshWorkflow,
		Execution: RunWorkflowConfig{
			ResourceKind: "refresh",
			InitialEvent: "refresh.queued",
			InitialState: refreshrun.RunStatusQueued,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &persistence
}

func testAuthorization() AuthorizationConfig {
	return AuthorizationConfig{AuthorizeObject: func(context.Context, string, access.Capability, access.ResourceRef) (bool, error) {
		return true, nil
	}}
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

func TestRefreshCreateDurableAuditDoesNotAppendPostCommitDuplicate(t *testing.T) {
	contract, ok := refreshgen.GetAPIGenCommandRuntimeContract(string(refreshgen.GenOperationCreateRefreshRun))
	if !ok || contract.Execution == nil {
		t.Fatal("generated refresh execution contract is unavailable")
	}
	store := &refreshEventStore{events: []jobs.Event{{EventType: contract.Execution.InitialEvent}}}
	module := &Module{events: store, durableAudit: true}
	run := refreshrun.RunRecord{ID: "run-1", Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation"}, SemanticModelID: "orders", PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := module.verifyRunCreated(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if store.appends != 0 {
		t.Fatalf("duplicate appends=%d", store.appends)
	}
}

func TestRefreshCreateDurableAuditSkipsLegacyVerificationFailure(t *testing.T) {
	var logs bytes.Buffer
	module := &Module{events: &refreshEventStore{err: errors.New("event store unavailable")}, logger: slog.New(slog.NewTextHandler(&logs, nil)), durableAudit: true}
	run := refreshrun.RunRecord{ID: "run-audit-failure", Identity: projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation"}, SemanticModelID: "orders", PipelineID: "daily", TargetType: refreshrun.TargetRefreshPipeline, TargetID: "daily", Status: refreshrun.RunStatusQueued, CreatedAt: "2026-08-10T12:00:00Z"}
	if err := module.verifyRunCreated(t.Context(), run); err != nil {
		t.Fatalf("durable audit verification changed command result: %v", err)
	}
	if logs.Len() != 0 {
		t.Fatalf("legacy verification log = %q", logs.String())
	}
}

func TestBuildConstructsOwnedHTTPHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{Authorization: testAuthorization(), HTTP: HTTPConfig{ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) {
		return projectgraph.ServingIdentity{ProjectID: "sales", Environment: "dev", GenerationID: "generation"}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if module.HTTP().ServingIdentity == nil {
		t.Fatal("serving identity resolver missing")
	}
}

func TestBuildRequiresCanonicalAuthorizer(t *testing.T) {
	if _, err := Build(t.Context(), Config{}); err == nil {
		t.Fatal("Build accepted a missing canonical authorizer")
	}
}

func TestBuildProductionRejectsSQLitePersistence(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()

	_, err = Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Production: true,
		Authorization: testAuthorization(),
	})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
		t.Fatalf("production refresh build error = %v, want PostgreSQL persistence admission", err)
	}
}

func TestBuildProductionRejectsUnmarkedPersistence(t *testing.T) {
	_, err := Build(t.Context(), Config{
		Persistence: &Persistence{}, Production: true,
		Authorization: testAuthorization(),
	})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL persistence") {
		t.Fatalf("unmarked production refresh build error = %v, want PostgreSQL persistence admission", err)
	}
}

func TestPersistenceValidateRejectsUnmarkedBackend(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()

	persistence := sqlitePersistence(t, store.SQLDB())
	persistence.backend = backendUnknown
	if err := persistence.Validate(); err == nil || !strings.Contains(err.Error(), "backend is not configured") {
		t.Fatalf("unmarked persistence validation error = %v, want backend admission", err)
	}
}

func TestPersistenceValidateRequiresTerminalRecovery(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	persistence := sqlitePersistence(t, store.SQLDB())
	persistence.TerminalRecovery = nil
	if err := persistence.Validate(); err == nil {
		t.Fatal("persistence validation accepted missing terminal recovery")
	} else if !strings.Contains(err.Error(), "terminal recovery") {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestBuildRejectsInvalidRecoveryLifecycleConfiguration(t *testing.T) {
	if _, err := Build(t.Context(), Config{
		Authorization: testAuthorization(), RecoveryLifecycle: &RecoveryLifecycle{},
	}); err == nil {
		t.Fatal("Build accepted an invalid recovery lifecycle")
	}
}

func TestAssetRefreshStateReadsScopedRunsAndDataVersion(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'active');
INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test');
INSERT INTO refresh_jobs (
  id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json,
  estimated_memory_bytes, kind, status
) VALUES (
  'job_1', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'user:test', '[]',
  67108864, 'refresh_pipeline', 'succeeded'
);
INSERT INTO refresh_job_runs (
	  id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type, invocation_source,
	  status, created_sequence
	) VALUES (
	  'run_1', 'job_1', 'user:test', 'dev', 'refresh_pipeline', 'pipeline_daily', 3, 'manual', 'manual',
  'succeeded', 1
);`); err != nil {
		t.Fatalf("seed refresh state: %v", err)
	}
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_test", PipelineID: "pipeline_daily", ProjectID: "project_sales", Environment: "dev", SemanticModelID: "semantic_sales", ServingGenerationID: "generation_a",
		ArtifactDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SelectionDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MaterializationScope: []string{"model_orders"},
	})
	if err != nil {
		t.Fatalf("build test pipeline plan: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"pipelinePlan": plan})
	scope, _ := json.Marshal(plan.MaterializationScope)
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_jobs SET payload_json = ? WHERE id = 'job_1'`, string(payload)); err != nil {
		t.Fatalf("persist test pipeline plan: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `UPDATE refresh_job_runs SET project_id = 'project_sales', trigger_id = 'manual', plan_digest = ?, materialization_scope_json = ? WHERE id = 'run_1'`, plan.Digest, string(scope)); err != nil {
		t.Fatalf("persist test run plan evidence: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO refresh_jobs (
  id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json,
  estimated_memory_bytes, kind, status
) VALUES (
  'job_model', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'user:test', '[]',
  67108864, 'child_run', 'succeeded'
);
INSERT INTO refresh_job_runs (
  id, job_id, project_id, principal_id, environment, target_type, target_id, target_revision,
  trigger_type, invocation_source, parent_run_id, status, created_sequence
) VALUES (
  'run_model', 'job_model', 'project_sales', 'user:test', 'dev', 'model_table', 'model:sales_customers', 3,
  'dependency', 'dependency', 'run_1', 'succeeded', 2
);`); err != nil {
		t.Fatalf("seed model refresh state: %v", err)
	}
	module, err := Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Authorization: testAuthorization(),
		Service: refreshrun.Service{ServingStates: reconciliationStates{state: servingstate.State{
			ID: "generation_a", ProjectID: "project_sales", Environment: "dev",
		}}},
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	identity := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_a"}
	if err := module.schedules.SaveDataVersion(t.Context(), refreshschedule.DataVersion{
		Identity: identity, SemanticModelID: "semantic_sales", SnapshotID: 42,
		RefreshedAt: now, Source: refreshschedule.DataVersionSourceRefresh, PipelineID: "pipeline_daily", RunID: "run_1",
	}); err != nil {
		t.Fatalf("save data version: %v", err)
	}
	state, err := module.AssetRefreshState(t.Context(), "project_sales", "dev", "pipeline_daily", "semantic_sales")
	if err != nil {
		t.Fatalf("asset refresh state: %v", err)
	}
	if len(state.Runs) != 1 || state.Runs[0].ID != "run_1" || state.Runs[0].TargetGeneration != 3 {
		t.Fatalf("runs = %#v", state.Runs)
	}
	if state.LatestSuccessful.ID != "run_1" {
		t.Fatalf("latest successful = %#v", state.LatestSuccessful)
	}
	if state.DataVersion.SnapshotID != 42 || state.DataVersion.ServingStateID != "generation_a" || !state.DataVersion.RefreshedAt.Equal(now) {
		t.Fatalf("data version = %#v", state.DataVersion)
	}
	if state.RunCommand.OperationID() == "" || state.CancelCommand.OperationID() == "" {
		t.Fatalf("generated command bindings missing: run=%#v cancel=%#v", state.RunCommand, state.CancelCommand)
	}
	modelState, err := module.ModelRefreshState(t.Context(), "project_sales", "dev", "model:sales_customers")
	if err != nil {
		t.Fatalf("model refresh state: %v", err)
	}
	if len(modelState.Runs) != 1 || modelState.Runs[0].ID != "run_model" || modelState.LatestSuccessful.ID != "run_model" {
		t.Fatalf("model runs = %#v, latest successful = %#v", modelState.Runs, modelState.LatestSuccessful)
	}
	semanticState, err := module.SemanticModelRefreshState(t.Context(), "project_sales", "dev", "semantic_sales")
	if err != nil {
		t.Fatalf("semantic model refresh state: %v", err)
	}
	if len(semanticState.Runs) != 1 || semanticState.Runs[0].ID != "run_1" || semanticState.LatestSuccessful.ID != "run_1" {
		t.Fatalf("semantic model runs = %#v, latest successful = %#v", semanticState.Runs, semanticState.LatestSuccessful)
	}
	if semanticState.DataVersion.SnapshotID != 42 || !semanticState.DataVersion.RefreshedAt.Equal(now) {
		t.Fatalf("semantic model data version = %#v", semanticState.DataVersion)
	}
}

func TestAssetRefreshStateMarksMissingPersistenceUnavailable(t *testing.T) {
	module, err := Build(t.Context(), Config{Authorization: testAuthorization()})
	if err != nil {
		t.Fatal(err)
	}
	state, err := module.AssetRefreshState(t.Context(), "project_sales", "dev", "pipeline_daily", "semantic_sales")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Unavailable {
		t.Fatalf("refresh state = %#v, want unavailable without persistence", state)
	}
	modelState, err := module.ModelRefreshState(t.Context(), "project_sales", "dev", "model:sales_customers")
	if err != nil {
		t.Fatal(err)
	}
	if !modelState.Unavailable {
		t.Fatalf("model refresh state = %#v, want unavailable without persistence", modelState)
	}
	semanticState, err := module.SemanticModelRefreshState(t.Context(), "project_sales", "dev", "semantic_sales")
	if err != nil {
		t.Fatal(err)
	}
	if !semanticState.Unavailable {
		t.Fatalf("semantic model refresh state = %#v, want unavailable without persistence", semanticState)
	}
}

func TestCancelPipelineRefreshForUIDeniesCrossPipelineRun(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation_a', 'project_sales', 'dev', 'active');
INSERT INTO principals (id, email, display_name) VALUES ('user:test', 'test@example.test', 'Test');
INSERT INTO refresh_jobs (
  id, project_id, generation_id, semantic_model_id, pipeline_id, principal_id, group_ids_json,
  estimated_memory_bytes, kind, status
) VALUES (
  'job_1', 'project_sales', 'generation_a', 'semantic_sales', 'pipeline_daily', 'user:test', '[]',
  67108864, 'refresh_pipeline', 'queued'
);
INSERT INTO refresh_job_runs (
  id, job_id, principal_id, environment, target_type, target_id, target_revision, trigger_type,
  invocation_source, status, created_sequence
) VALUES (
  'run_1', 'job_1', 'user:test', 'dev', 'refresh_pipeline', 'pipeline_daily', 1, 'manual',
  'manual', 'queued', 1
);`); err != nil {
		t.Fatalf("seed refresh run: %v", err)
	}
	module, err := Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Authorization: testAuthorization(),
		Service: refreshrun.Service{ServingStates: reconciliationStates{state: servingstate.State{ID: "generation_a", ProjectID: "project_sales", Environment: "dev"}}},
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project_sales", Environment: "dev", GenerationID: "generation_a"}
	if err := module.CancelPipelineRefreshForUI(t.Context(), identity, "pipeline_other", "run_1", "user:test"); err == nil {
		t.Fatal("cross-pipeline cancel unexpectedly succeeded")
	}
	var status string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT status FROM refresh_job_runs WHERE id = 'run_1'`).Scan(&status); err != nil {
		t.Fatalf("read run status after denied cancel: %v", err)
	}
	if status != refreshrun.RunStatusQueued {
		t.Fatalf("cross-pipeline cancel changed run status to %q", status)
	}
}

func TestReconcileProjectsPublishedServingStateIntoRefreshDataVersions(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('state_1', 'sales', 'prod', 'active')`); err != nil {
		t.Fatalf("insert serving state: %v", err)
	}
	states := reconciliationStates{
		state: servingstate.State{
			ID: "state_1", ProjectID: "sales", Environment: "prod", Source: servingstate.SourcePublish,
			DuckLakeSnapshotID: 42, ActivatedAt: "2026-07-22T12:00:00Z",
		},
		artifact: servingstate.Artifact{Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
	}
	publisher := &versionPublisher{}
	module, err := Build(t.Context(), Config{
		Persistence:   sqlitePersistence(t, store.SQLDB()),
		Authorization: testAuthorization(),
		Service: refreshrun.Service{
			ServingStates: states,
			Artifacts: artifactLoaderFunc(func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
				return refreshrun.LoadedArtifact{Definition: &artifact.Definition{
					Models: map[string]*semanticmodel.Model{"orders": {}},
					Pipelines: map[string]refreshschedule.Definition{
						"daily": {ID: "daily", SemanticModelID: "orders"},
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
	identity, err := projectgraph.NewServingIdentity("sales", "prod", "state_1")
	if err != nil {
		t.Fatal(err)
	}
	modelID, err := projectgraph.NewResourceID("orders")
	if err != nil {
		t.Fatal(err)
	}
	version, found, err := refreshsqlite.NewRepository(store.SQLDB()).DataVersion(t.Context(), identity, modelID)
	if err != nil || !found {
		t.Fatalf("data version = %#v, %v, %v", version, found, err)
	}
	if version.SnapshotID != 42 || version.Identity.GenerationID != "state_1" || version.Source != refreshschedule.DataVersionSourcePublish {
		t.Fatalf("unexpected data version: %#v", version)
	}
	if got := publisher.modelID; got != "orders" {
		t.Fatalf("published model = %q, want orders", got)
	}
}

func TestReconcileRejectsMultipleActiveServingScopes(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	module, err := Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Authorization: testAuthorization(),
		Service: refreshrun.Service{
			ServingStates: reconciliationStates{scopes: []servingstate.ActiveScope{{ProjectID: "sales", Environment: "prod"}, {ProjectID: "sales", Environment: "dev"}}},
			Artifacts: artifactLoaderFunc(func(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
				return refreshrun.LoadedArtifact{}, nil
			}),
		},
	})
	if err != nil {
		t.Fatalf("build module: %v", err)
	}
	if err := module.Reconcile(t.Context()); err == nil {
		t.Fatal("Reconcile accepted multiple active serving scopes")
	}
}

func TestDataVersionUsesCanonicalRuntimeIdentity(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO serving_states (id, project_id, environment, status) VALUES
  ('state-old', 'sales', 'prod', 'superseded'),
  ('state-new', 'sales', 'prod', 'active');
INSERT INTO semantic_model_data_versions (
  project_id, environment, semantic_model_id, snapshot_id, generation_id, refreshed_at, source
) VALUES
  ('sales', 'prod', 'orders', 41, 'state-old', '2026-08-18T00:00:00Z', 'publish'),
  ('sales', 'prod', 'orders', 84, 'state-new', '2026-08-19T00:00:00Z', 'refresh');`); err != nil {
		t.Fatal(err)
	}
	module, err := Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Authorization: testAuthorization(),
		ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) {
			return projectgraph.ServingIdentity{ProjectID: "sales", Environment: "prod", GenerationID: "state-new"}, nil
		},
		Service: refreshrun.Service{ServingStates: reconciliationStates{state: servingstate.State{ID: "state-old", ProjectID: "sales", Environment: "prod"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, found, err := module.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found {
		t.Fatalf("DataVersion() = %#v, %t, %v", version, found, err)
	}
	if version.SnapshotID != 84 || version.ServingStateID != "state-new" || version.Source != refreshschedule.DataVersionSourceRefresh {
		t.Fatalf("DataVersion() = %#v, want canonical runtime version", version)
	}
}

func TestDataVersionFallsBackToCanonicalPublicationAndPrefersRefresh(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity := projectgraph.ServingIdentity{ProjectID: "sales", Environment: "prod", GenerationID: "state-new"}
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('state-new', 'sales', 'prod', 'active')`); err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	module, err := Build(t.Context(), Config{
		Persistence: sqlitePersistence(t, store.SQLDB()), Authorization: testAuthorization(),
		ResolveIdentity: func(context.Context) (projectgraph.ServingIdentity, error) { return identity, nil },
		PublishedVersion: func(context.Context, projectgraph.ServingIdentity) (PublishedDataVersion, bool, error) {
			return PublishedDataVersion{SnapshotID: 41, RefreshedAt: publishedAt}, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	version, found, err := module.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found {
		t.Fatalf("published DataVersion() = %#v, %t, %v", version, found, err)
	}
	if version.SnapshotID != 41 || version.Source != refreshschedule.DataVersionSourcePublish || !version.RefreshedAt.Equal(publishedAt) {
		t.Fatalf("published DataVersion() = %#v", version)
	}
	pipelineState, err := module.AssetRefreshState(t.Context(), "sales", "prod", "pipeline_daily", "orders")
	if err != nil || pipelineState.DataVersion.SnapshotID != 41 || !pipelineState.DataVersion.RefreshedAt.Equal(publishedAt) {
		t.Fatalf("pipeline published data version = %#v, %v", pipelineState.DataVersion, err)
	}
	semanticState, err := module.SemanticModelRefreshState(t.Context(), "sales", "prod", "orders")
	if err != nil || semanticState.DataVersion.SnapshotID != 41 || !semanticState.DataVersion.RefreshedAt.Equal(publishedAt) {
		t.Fatalf("semantic-model published data version = %#v, %v", semanticState.DataVersion, err)
	}

	refreshedAt := publishedAt.Add(time.Hour)
	if err := module.schedules.SaveDataVersion(t.Context(), refreshschedule.DataVersion{
		Identity: identity, SemanticModelID: "orders", SnapshotID: 84,
		RefreshedAt: refreshedAt, Source: refreshschedule.DataVersionSourceRefresh,
	}); err != nil {
		t.Fatal(err)
	}
	version, found, err = module.DataVersion(t.Context(), "sales", "prod", "orders")
	if err != nil || !found {
		t.Fatalf("refreshed DataVersion() = %#v, %t, %v", version, found, err)
	}
	if version.SnapshotID != 84 || version.Source != refreshschedule.DataVersionSourceRefresh || !version.RefreshedAt.Equal(refreshedAt) {
		t.Fatalf("refreshed DataVersion() = %#v", version)
	}
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
	return servingstate.State{}, nil
}
func (s reconciliationStates) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return servingstate.Artifact{}, nil
}
func (s reconciliationStates) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}
func (s reconciliationStates) Activate(context.Context, projectgraph.ResourceID, servingstate.Environment, servingstate.ID, servingstate.ID) (servingstate.State, error) {
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
func (p *versionPublisher) PublishSemanticModelVersion(_ context.Context, _ projectgraph.ServingIdentity, modelID projectgraph.ResourceID) {
	p.modelID = modelID.String()
}

type dispatcherFunc func(context.Context)

func (f dispatcherFunc) Run(ctx context.Context) { f(ctx) }

type terminalRecoveryFunc func(context.Context, string, string) error

func (f terminalRecoveryFunc) FailRunsForTerminalServingStates(ctx context.Context, environment, message string) error {
	return f(ctx, environment, message)
}

func TestStartRunsTerminalRecoveryBeforeDispatch(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persistence := sqlitePersistence(t, store.SQLDB())
	var recovered atomic.Bool
	dispatched := make(chan struct{}, 1)
	persistence.TerminalRecovery = terminalRecoveryFunc(func(_ context.Context, environment, message string) error {
		if environment != "dev" || message != "refresh did not complete" {
			t.Fatalf("unexpected recovery scope environment=%q message=%q", environment, message)
		}
		recovered.Store(true)
		return nil
	})
	module, err := Build(t.Context(), Config{
		Persistence: persistence, Authorization: testAuthorization(), RecoveryEnvironment: "dev",
		Dispatcher: dispatcherFunc(func(context.Context) {
			if !recovered.Load() {
				t.Error("dispatcher ran before terminal recovery")
			}
			dispatched <- struct{}{}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not run")
	}
	if err := module.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type schedulerFunc func(context.Context) error

func (f schedulerFunc) DispatchDue(ctx context.Context) error { return f(ctx) }

func TestDispatchCoalescesConcurrentRequests(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	module, err := Build(t.Context(), Config{Authorization: testAuthorization(), Dispatcher: dispatcherFunc(func(context.Context) {
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
		Authorization: testAuthorization(),
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
		Authorization: testAuthorization(),
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
	module, err := Build(t.Context(), Config{Authorization: testAuthorization(), Dispatcher: dispatcherFunc(func(context.Context) {
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
