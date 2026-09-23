package module

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projecthttp "github.com/flidai/leapview/internal/project/http"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/go-chi/chi/v5"
)

func TestPostgresRunAttemptDetailReadsPersistedFailureAndLifecycle(t *testing.T) {
	db := modulePostgresTestDB(t)
	refreshRepo := refreshpostgres.New(db)
	jobsRepo := jobspostgres.New(db)
	queue := NewPostgresJobsAdapter(jobsRepo, refreshRepo)
	identity := projectgraph.ServingIdentity{ProjectID: "project_run_attempt_detail", Environment: "dev", GenerationID: "generation_run_attempt_detail"}
	modelID := projectgraph.ResourceID("model_attempt_detail")
	pipelineID := projectgraph.ResourceID("pipeline_attempt_detail")
	semanticModelID := projectgraph.ResourceID("semantic_model_attempt_detail")
	plan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline_plan_attempt_detail", PipelineID: pipelineID.String(), ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		SemanticModelID: semanticModelID.String(), ServingGenerationID: identity.GenerationID,
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), SelectionDigest: "sha256:" + strings.Repeat("b", 64),
		MaterializationScope: []string{modelID.String()}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	persistence, err := NewPostgresPersistence(refreshRepo, PostgresPersistenceConfig{
		PublicationIdentityResolver: staticPublicationIdentityResolver("pool-run-attempt-detail", "catalog-run-attempt-detail"),
		SchedulerOwner:              "scheduler-run-attempt-detail", Jobs: queue,
		CanonicalVerifier: integrationCanonicalVerifier{physicalPoolID: "pool-run-attempt-detail", catalogID: "catalog-run-attempt-detail"},
		CancelAuditWriter: integrationAuditWriter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	root, children, err := persistence.Runs.CreateRunTree(t.Context(), refreshrun.RunTreeInput{
		Root: refreshrun.RunInput{
			RunID: "run_attempt_detail_failed", Identity: identity, SemanticModelID: semanticModelID, PipelineID: pipelineID, PipelinePlan: &plan,
			InvocationSource: "manual", PrincipalID: "principal:run-attempt-detail", EstimatedMemoryBytes: 1,
			TargetRevision: 1, TargetType: refreshrun.TargetRefreshPipeline, TargetID: pipelineID, TriggerType: refreshrun.TriggerManual,
			JobKind: refreshrun.JobKindRefreshPipeline, PayloadJSON: string(mustJSON(t, map[string]any{"pipelinePlan": plan})),
		},
		DependencyTargets: []projectgraph.ResourceID{modelID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("child runs = %#v, want the planned model run", children)
	}
	scope := refreshrun.ReadScope{ProjectID: identity.ProjectID, Environment: identity.Environment}
	module := &Module{runs: persistence.Runs, service: refreshrun.Service{Publication: persistence.Publication}}
	readStatus := func(want string) {
		t.Helper()
		run, readErr := module.GetRun(t.Context(), scope, root.ID)
		if readErr != nil || run.Status != want {
			t.Fatalf("persisted run status = %q, err = %v; want %q", run.Status, readErr, want)
		}
	}
	readStatus(refreshrun.RunStatusQueued)
	queuedChildren, err := module.ListChildRuns(t.Context(), scope, root.ID)
	if err != nil || len(queuedChildren) != 1 || queuedChildren[0].Status != refreshrun.RunStatusQueued {
		t.Fatalf("persisted queued model run = %#v, err = %v", queuedChildren, err)
	}

	candidates, err := listRiverRefreshJobs(persistence.Runs, t.Context(), scope, 10)
	if err != nil {
		t.Fatal(err)
	}
	var rootJob refreshrun.JobRecord
	for _, candidate := range candidates {
		if candidate.RunID == root.ID {
			rootJob = candidate
			break
		}
	}
	if rootJob.RunID == "" {
		t.Fatalf("root refresh job missing from candidates: %#v", candidates)
	}
	claimedJob, ok, err := claimRiverRefreshTest(t.Context(), queue, rootJob, "worker-run-attempt-detail", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim root job ok = %v, err = %v", ok, err)
	}
	readStatus(refreshrun.RunStatusRunning)
	module.service = refreshrun.Service{
		Runs: persistence.Runs,
		CanonicalExecutor: func(context.Context, refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
			// ExecuteClaimedJob records prepared before entering the executor.
			// Read it through the scoped repository so this integration test
			// proves the persisted queued → running → prepared → failed path.
			readStatus(refreshrun.RunStatusPrepared)
			return refreshrun.CanonicalRefreshResult{}, refreshrun.WithWorkerFailureStage(refreshrun.WorkerFailureStageBuild, errors.New("warehouse password=do-not-persist private SQL"))
		},
		Publication: persistence.Publication,
	}
	if err := module.service.ExecuteClaimedJob(t.Context(), claimedJob); err == nil || !strings.Contains(err.Error(), "warehouse password=do-not-persist") {
		t.Fatalf("worker execution error = %v; raw error should remain available to worker logs", err)
	}
	readStatus(refreshrun.RunStatusFailed)
	failedRoot, err := module.GetRun(t.Context(), scope, root.ID)
	if err != nil || failedRoot.Error != refreshrun.WorkerFailureMessageBuild || strings.Contains(failedRoot.Error, "do-not-persist") {
		t.Fatalf("persisted failed root error = %q, err = %v", failedRoot.Error, err)
	}
	failedChildren, err := module.ListChildRuns(t.Context(), scope, root.ID)
	if err != nil || len(failedChildren) != 1 || failedChildren[0].Status != refreshrun.RunStatusFailed || failedChildren[0].Error != refreshrun.WorkerFailureMessageBuild {
		t.Fatalf("persisted failed model run = %#v, err = %v", failedChildren, err)
	}

	rootAttemptPage, err := module.ListRunAttempts(t.Context(), scope, root.ID)
	if err != nil || len(rootAttemptPage.Attempts) != 1 {
		t.Fatalf("persisted root attempts = %#v, err = %v", rootAttemptPage, err)
	}
	rootAttempt := rootAttemptPage.Attempts[0]
	if rootAttempt.Number != 1 || rootAttempt.Status != "failed" || rootAttempt.Error != refreshrun.WorkerFailureMessageBuild || rootAttempt.StartedAt == "" || rootAttempt.FinishedAt == "" || strings.Contains(rootAttempt.Error, "do-not-persist") {
		t.Fatalf("persisted root attempt diagnostics = %#v", rootAttempt)
	}
	modelAttemptPage, err := module.ListRunAttempts(t.Context(), scope, children[0].ID)
	if err != nil || len(modelAttemptPage.Attempts) != 0 {
		t.Fatalf("model attempt projection = %#v, err = %v; a child attempt must not be synthesized", modelAttemptPage, err)
	}
	if _, err := module.ListRunAttempts(t.Context(), refreshrun.ReadScope{ProjectID: "project_other", Environment: identity.Environment}, root.ID); err == nil {
		t.Fatal("cross-project attempt read unexpectedly succeeded")
	}
	_, published, err := module.RunPublication(t.Context(), scope, root.ID)
	if err != nil || published {
		t.Fatalf("failed run publication found = %v, err = %v; failure must remain distinct from publication", published, err)
	}

	// Exercise the public HTTP route with the persisted repository as its live
	// reader so the test proves the failed model, safe error and publication
	// outcome survive the run-detail projection.
	projectID := identity.ProjectID
	browser := &projecthttp.BrowserHandler{
		RunDetailReader:      module,
		RunPublicationReader: module,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectID, nil
		},
		Environment: identity.Environment,
		CurrentUser: func(*http.Request) (projecthttp.Principal, bool) {
			return projecthttp.Principal{ID: "local-test", DevBypass: true}, true
		},
	}
	router := chi.NewRouter()
	browser.MountAuthenticated(router)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/pipelines/"+pipelineID.String()+"/runs/"+root.ID, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("persisted failed run document = %d: %s", response.Code, response.Body.String())
	}
	streamBody := persistedRunDetailUpdates(t, browser, url.Values{
		"route": {"pipeline_run_detail"}, "surface": {"pipeline_run_detail"},
		"asset": {pipelineID.String()}, "run": {root.ID}, "section": {"execution"},
	})
	if !strings.Contains(streamBody, refreshrun.WorkerFailureMessageBuild) || !strings.Contains(streamBody, "not_published") || !strings.Contains(streamBody, modelID.String()) || !strings.Contains(streamBody, `"status":"failed"`) {
		t.Fatalf("persisted failed run HTTP signal = %s", streamBody)
	}
	if strings.Contains(streamBody, "do-not-persist") {
		t.Fatal("run-detail HTTP projection exposed worker error details")
	}
	if evidenceDir := strings.TrimSpace(os.Getenv("LEAPVIEW_RUN_FAILURE_QA_DIR")); evidenceDir != "" {
		if err := writePersistedRunFailureEvidence(evidenceDir, response.Body.Bytes(), []byte(streamBody), root.ID, pipelineID.String(), modelID.String()); err != nil {
			t.Fatalf("write persisted run failure browser evidence: %v", err)
		}
	}
}

// writePersistedRunFailureEvidence keeps the browser QA handoff opt-in. Both
// artifacts come directly from the route backed by the temporary PostgreSQL
// database used by this integration test; no run state is fabricated here.
func writePersistedRunFailureEvidence(dir string, document, updates []byte, runID, pipelineID, modelID string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(struct {
		Database          string   `json:"database"`
		Transitions       []string `json:"transitions"`
		RunID             string   `json:"runId"`
		PipelineID        string   `json:"pipelineId"`
		FailedModelID     string   `json:"failedModelId"`
		FailureMessage    string   `json:"failureMessage"`
		PublicationResult string   `json:"publicationResult"`
	}{
		Database: "ephemeral PostgreSQL testcontainer",
		Transitions: []string{
			refreshrun.RunStatusQueued,
			refreshrun.RunStatusRunning,
			refreshrun.RunStatusPrepared,
			refreshrun.RunStatusFailed,
		},
		RunID: runID, PipelineID: pipelineID, FailedModelID: modelID,
		FailureMessage: refreshrun.WorkerFailureMessageBuild, PublicationResult: "not_published",
	}, "", "  ")
	if err != nil {
		return err
	}
	for name, body := range map[string][]byte{
		"document.html": document,
		"updates.sse":   updates,
		"evidence.json": append(manifest, '\n'),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type persistedRunDetailRecorder struct {
	*httptest.ResponseRecorder
	wrote chan struct{}
	once  sync.Once
}

func (r *persistedRunDetailRecorder) Write(data []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(data)
	r.once.Do(func() { close(r.wrote) })
	return n, err
}

func persistedRunDetailUpdates(t *testing.T, browser *projecthttp.BrowserHandler, values url.Values) string {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?"+values.Encode(), nil)
	recorder := &persistedRunDetailRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		browser.Updates(recorder, request)
	}()
	select {
	case <-recorder.wrote:
	case <-time.After(2 * time.Second):
		t.Fatal("run detail updates did not write the persisted bootstrap projection")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run detail updates stream did not stop after cancellation")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("run detail updates status = %d: %s", recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
