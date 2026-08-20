package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	materializesqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type refreshRunResponse struct {
	ID                   string                       `json:"id"`
	Identity             projectgraph.ServingIdentity `json:"identity"`
	PipelineID           string                       `json:"pipelineId"`
	SemanticModel        string                       `json:"semanticModel"`
	Trigger              string                       `json:"trigger"`
	TriggerID            string                       `json:"triggerId"`
	PlanDigest           string                       `json:"planDigest"`
	MaterializationScope []string                     `json:"materializationScope"`
	Status               string                       `json:"status"`
	CreatedAt            string                       `json:"createdAt"`
}

func TestRefreshVisibilityStreamsAndPersistsSemanticModelRuns(t *testing.T) {
	h := newCanonicalRefreshHarness(t)
	ctx := context.Background()
	projectID := h.projectID.String()
	pipelineID := h.pipelineID.String()

	status, body, _ := refreshAPIRequest(t, h, http.MethodPost, "/api/v1/projects/"+projectID+"/refresh-runs", `{"pipelineId":"`+pipelineID+`","triggerId":"manual"}`, "canonical-refresh")
	if status != http.StatusAccepted {
		t.Fatalf("manual refresh API status = %d, body=%s", status, body)
	}
	var created refreshRunResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode manual refresh response: %v; body=%s", err, body)
	}
	if created.ID == "" || created.PipelineID != pipelineID || created.SemanticModel != h.semanticModel.String() || created.Trigger != refreshrun.TriggerManual || created.TriggerID != "manual" || created.PlanDigest == "" || len(created.MaterializationScope) == 0 {
		t.Fatalf("manual refresh response = %#v", created)
	}
	if created.Identity.ProjectID != h.projectID || created.Identity.Environment != string(h.environment) || created.Identity.GenerationID == "" {
		t.Fatalf("manual refresh identity = %#v", created.Identity)
	}
	if _, err := time.Parse(time.RFC3339Nano, created.CreatedAt); err != nil {
		t.Fatalf("manual refresh createdAt = %q, want RFC3339: %v", created.CreatedAt, err)
	}
	assertPublicRefreshResponse(t, body)

	var finished refreshRunResponse
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, body, _ = refreshAPIRequest(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/refresh-runs/"+created.ID, "", "")
		if status != http.StatusOK {
			t.Fatalf("get refresh run status = %d, body=%s", status, body)
		}
		if err := json.Unmarshal(body, &finished); err != nil {
			t.Fatalf("decode refresh run response: %v; body=%s", err, body)
		}
		if finished.Status == refreshrun.RunStatusSucceeded {
			break
		}
		if finished.Status == refreshrun.RunStatusFailed || finished.Status == refreshrun.RunStatusCancelled {
			t.Fatalf("refresh run reached terminal failure: %#v", finished)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for refresh run completion: %#v", finished)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if finished.Identity != created.Identity {
		t.Fatalf("refresh run identity changed: created=%#v finished=%#v", created.Identity, finished.Identity)
	}

	status, body, _ = refreshAPIRequest(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/refresh-runs?limit=10", "", "")
	if status != http.StatusOK {
		t.Fatalf("list refresh runs status = %d, body=%s", status, body)
	}
	var listed struct {
		Items []refreshRunResponse `json:"items"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode refresh run list: %v; body=%s", err, body)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != created.ID || listed.Items[0].Status != refreshrun.RunStatusSucceeded {
		t.Fatalf("refresh run list = %#v", listed.Items)
	}
	assertPublicRefreshResponse(t, body)

	status, body, _ = refreshAPIRequest(t, h, http.MethodGet, "/api/v1/projects/"+projectID+"/refresh-runs/"+created.ID+"/events?limit=200", "", "")
	if status != http.StatusOK {
		t.Fatalf("refresh event list status = %d, body=%s", status, body)
	}
	var events struct {
		Items []struct {
			Event string `json:"event"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("decode refresh events: %v; body=%s", err, body)
	}
	hasQueued, hasSucceeded := false, false
	for _, event := range events.Items {
		hasQueued = hasQueued || event.Event == "refresh.queued"
		hasSucceeded = hasSucceeded || event.Event == "refresh.succeeded"
	}
	if !hasQueued || !hasSucceeded {
		t.Fatalf("refresh events = %#v, want queued and succeeded lifecycle events", events.Items)
	}

	repo := materializesqlite.NewSQLRunRepository(h.store.SQLDB())
	scope, err := refreshrun.ReadScopeForIdentity(finished.Identity)
	if err != nil {
		t.Fatalf("refresh read scope: %v", err)
	}
	rootRuns, err := repo.ListTargetRuns(ctx, scope, refreshrun.TargetRefreshPipeline, h.pipelineID, refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list refresh pipeline runs: %v", err)
	}
	if len(rootRuns) != 1 {
		t.Fatalf("refresh pipeline runs = %#v, want one root run", rootRuns)
	}
	root := rootRuns[0]
	if root.Status != refreshrun.RunStatusSucceeded || root.TriggerType != refreshrun.TriggerManual || root.PrincipalID != "dev" || root.Identity != finished.Identity {
		t.Fatalf("refresh pipeline run = %#v, want succeeded manual run attributed to dev", root)
	}

	children, err := repo.ListChildRuns(ctx, scope, root.ID)
	if err != nil {
		t.Fatalf("list refresh child runs: %v", err)
	}
	if len(children) != len(h.modelIDs) {
		t.Fatalf("refresh child runs = %d, want %d (%#v)", len(children), len(h.modelIDs), children)
	}
	for _, expectedID := range h.modelIDs {
		childRuns, listErr := repo.ListTargetRuns(ctx, scope, refreshrun.TargetModelTable, expectedID, refreshrun.RunPage{Limit: 10})
		if listErr != nil {
			t.Fatalf("list %s child runs: %v", expectedID, listErr)
		}
		if len(childRuns) != 1 {
			t.Fatalf("%s child runs = %#v, want one child run", expectedID, childRuns)
		}
		child := childRuns[0]
		if child.Status != refreshrun.RunStatusSucceeded || child.TriggerType != refreshrun.TriggerDependency || child.ParentRunID != root.ID || child.PrincipalID != "dev" {
			t.Fatalf("%s child run = %#v, want succeeded dependency run attributed to dev", expectedID, child)
		}
	}

	active, artifact, err := h.states.ActiveArtifact(ctx, h.projectID, h.environment)
	if err != nil {
		t.Fatalf("load active serving state after refresh: %v", err)
	}
	if active.Status != servingstate.StatusActive || active.Source != servingstate.SourceRefresh || active.DuckLakeSnapshotID != 2 {
		t.Fatalf("active serving state = %#v, want refresh generation with snapshot 2", active)
	}
	if string(active.ID) != finished.Identity.GenerationID || artifact.ServingStateID != active.ID || active.ProjectID != h.projectID {
		t.Fatalf("active serving identity/artifact = state=%#v artifact=%#v run=%#v", active, artifact, finished.Identity)
	}
}

func refreshAPIRequest(t *testing.T, h *canonicalRefreshHarness, method, path, body, idempotencyKey string) (int, []byte, http.Header) {
	t.Helper()
	request, err := http.NewRequest(method, h.serverURL(t)+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer dev")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody, response.Header
}

func assertPublicRefreshResponse(t *testing.T, body []byte) {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode public refresh response: %v; body=%s", err, body)
	}
	for _, internalField := range []string{"modelId", "servingStateId", "targetType", "targetId", "parentRunId"} {
		if _, exists := value[internalField]; exists {
			t.Fatalf("public refresh response exposed internal field %q: %#v", internalField, value)
		}
	}
}
