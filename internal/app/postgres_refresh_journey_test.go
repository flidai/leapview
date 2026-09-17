package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
)

const (
	postgresRefreshJourneyGeneration = "journey-generation"
	postgresRefreshJourneyPipeline   = "pipeline:journey-refresh"
	postgresRefreshJourneyModel      = "semantic:journey"
	postgresRefreshJourneyTable      = "orders"
	postgresRefreshJourneyArtifact   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	postgresRefreshJourneyKey        = "0198f2c0-7c7a-7f00-8a11-000000000123"
	postgresRefreshJourneyPrincipal  = "0198f2c0-7c7a-7f00-8a11-000000000777"
)

// TestPostgresRefreshRouteJourney exercises the generated application routes
// against native refresh/jobs/event/idempotency authorities. The serving
// reader and artifact loader are deliberately deterministic test seams: the
// durable refresh outcome remains PostgreSQL-owned without duplicating the
// run-tree SQL coverage in internal/refresh/module/postgres_integration_test.go.
func TestPostgresRefreshRouteJourney(t *testing.T) {
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		TargetID: "instance_00000000000000000000000000000099", NativeDashboard: true, BrowserSessionAuth: true,
	})
	identity, err := projectgraph.NewServingIdentity(postgresJourneyProject, "prod", postgresRefreshJourneyGeneration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.SeedPrincipal(t.Context(), access.PrincipalInput{
		ID: postgresRefreshJourneyPrincipal, Kind: access.PrincipalKindUser,
		Email: "journey-refresh@example.test", DisplayName: "Journey Refresh",
	}); err != nil {
		t.Fatalf("seed refresh principal: %v", err)
	}
	if _, err := fixture.Graph.Access.SetPlatformRole(t.Context(), access.PlatformRoleInput{
		PrincipalID: postgresRefreshJourneyPrincipal, Role: access.PlatformRoleAdmin,
	}); err != nil {
		t.Fatalf("grant refresh journey platform-admin role: %v", err)
	}
	sessionToken, err := fixture.Graph.Access.CreateSession(t.Context(), postgresRefreshJourneyPrincipal, time.Hour)
	if err != nil {
		t.Fatalf("create refresh browser session: %v", err)
	}
	session, err := fixture.Graph.Access.CredentialForSessionToken(t.Context(), sessionToken)
	if err != nil {
		t.Fatalf("resolve refresh browser session evidence: %v", err)
	}
	sessionExpiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil {
		t.Fatalf("parse refresh browser session expiry: %v", err)
	}
	sessionEvidence := access.CredentialEvidence{
		Class: "session", ID: session.ID, Fingerprint: session.TokenFingerprint,
		PrincipalID: session.PrincipalID, ExpiresAt: sessionExpiresAt.UTC(),
	}
	apiToken, _, err := fixture.Graph.Access.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: postgresRefreshJourneyPrincipal, Name: "refresh-journey-transport",
		Capabilities: append([]access.Capability{access.CapabilityPlatformAdmin}, access.LegacyProjectCapabilities()...), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create refresh API transport token: %v", err)
	}
	state := journeyRefreshStateReader{
		state:    servingstate.State{ID: servingstate.ID(identity.GenerationID), ProjectID: identity.ProjectID, Environment: servingstate.Environment(identity.Environment), Digest: postgresRefreshJourneyArtifact, DuckLakeSnapshotID: 1},
		artifact: servingstate.Artifact{ID: "artifact-journey", ServingStateID: servingstate.ID(identity.GenerationID), Digest: postgresRefreshJourneyArtifact, Format: "journey"},
	}
	definition := journeyRefreshDefinition()
	module, err := refreshmodule.Build(t.Context(), refreshmodule.Config{
		Persistence: fixture.RefreshPersistence, Production: true,
		Service: refreshrun.Service{
			ServingStates:         state,
			ResolveTargetRevision: func(context.Context, projectgraph.ServingIdentity) (int64, error) { return 1, nil },
			ResolveSourceDigest: func(context.Context, projectgraph.ServingIdentity) (string, error) {
				return postgresRefreshJourneyArtifact, nil
			},
			CanonicalExecutor: func(context.Context, refreshrun.JobRecord) (refreshrun.CanonicalRefreshResult, error) {
				return refreshrun.CanonicalRefreshResult{}, nil
			},
		},
		Artifacts: journeyRefreshArtifactLoader{definition: definition},
		Admission: fixture.Workload,
		HTTP: refreshmodule.HTTPConfig{
			RunnerConfigured: func() bool { return true },
			CurrentPrincipal: func(*http.Request) (refreshmodule.HTTPPrincipal, bool) {
				return refreshmodule.HTTPPrincipal{ID: postgresRefreshJourneyPrincipal}, true
			},
			ServingIdentity: func(*http.Request) (projectgraph.ServingIdentity, error) { return identity, nil },
		},
		CurrentSessionEvidence: func(ctx context.Context) (access.CredentialEvidence, bool) {
			evidence, ok := ctx.Value(postgresRefreshJourneySessionEvidenceContextKey{}).(access.CredentialEvidence)
			return evidence, ok
		},
		Authorization: refreshmodule.AuthorizationConfig{
			CurrentPrincipal: func(*http.Request) (refreshmodule.AuthorizationPrincipal, bool) {
				return refreshmodule.AuthorizationPrincipal{ID: postgresRefreshJourneyPrincipal, DevBypass: true}, true
			},
			AuthorizeObject: func(context.Context, string, access.Capability, access.ResourceRef) (bool, error) { return true, nil },
		},
		Events: fixture.platform.asyncJobs,
	})
	if err != nil {
		t.Fatalf("build deterministic native refresh route module: %v", err)
	}
	fixture.routes.refreshModule = module
	fixture.Handler = Routes(fixture.routes, fixture.runtime, fixture.platform, fixture.policy)
	innerHandler := fixture.Handler
	fixture.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), postgresRefreshJourneySessionEvidenceContextKey{}, sessionEvidence)
		innerHandler.ServeHTTP(w, r.WithContext(ctx))
	})

	request := func(method, path string, body io.Reader) *http.Request {
		req := fixture.Request(t.Context(), method, path, body)
		req.AddCookie(&http.Cookie{Name: "lv_session", Value: sessionToken})
		req.Header.Set("Authorization", "Bearer "+apiToken)
		return req
	}
	dispatch := func(req *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		fixture.Handler.ServeHTTP(rec, req)
		return rec
	}

	createPath := "/api/v1/projects/" + postgresJourneyProject.String() + "/refresh-runs"
	first := dispatch(requestWithHeaders(fixture, sessionToken, apiToken, sessionEvidence, http.MethodPost, createPath, strings.NewReader(`{"pipelineId":"`+postgresRefreshJourneyPipeline+`"}`), map[string]string{"Idempotency-Key": postgresRefreshJourneyKey}))
	if first.Code != http.StatusAccepted {
		t.Fatalf("manual refresh POST = %d body=%s", first.Code, first.Body.String())
	}
	var created refreshJourneyRunResponse
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode manual refresh response: %v", err)
	}
	if created.ID == "" || created.PipelineID != postgresRefreshJourneyPipeline || created.SemanticModel != postgresRefreshJourneyModel || created.Status != refreshrun.RunStatusQueued {
		t.Fatalf("manual refresh response = %#v", created)
	}
	if created.Identity != identity {
		t.Fatalf("manual refresh response identity = %#v, want %#v", created.Identity, identity)
	}
	for _, internalField := range []string{"targetType", "targetId", "triggerType", "triggerId", "parentRunId", "modelId", "servingStateId"} {
		if strings.Contains(first.Body.String(), `"`+internalField+`"`) {
			t.Fatalf("manual refresh response leaked internal field %q: %s", internalField, first.Body.String())
		}
	}
	if location := first.Header().Get("Location"); location != createPath+"/"+created.ID {
		t.Fatalf("manual refresh Location = %q, want %q", location, createPath+"/"+created.ID)
	}

	// The second command is a protocol replay, not another queue admission.
	replay := dispatch(requestWithHeaders(fixture, sessionToken, apiToken, sessionEvidence, http.MethodPost, createPath, strings.NewReader(`{"pipelineId":"`+postgresRefreshJourneyPipeline+`"}`), map[string]string{"Idempotency-Key": postgresRefreshJourneyKey}))
	if replay.Code != http.StatusAccepted || replay.Body.String() != first.Body.String() || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("refresh idempotent replay = %d body=%s, first=%s", replay.Code, replay.Body.String(), first.Body.String())
	}

	list := dispatch(request(http.MethodGet, createPath, nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), created.ID) {
		t.Fatalf("refresh list = %d body=%s", list.Code, list.Body.String())
	}
	get := dispatch(request(http.MethodGet, createPath+"/"+created.ID, nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), created.ID) {
		t.Fatalf("refresh GET = %d body=%s", get.Code, get.Body.String())
	}
	events := dispatch(request(http.MethodGet, createPath+"/"+created.ID+"/events", nil))
	if events.Code != http.StatusOK || !strings.Contains(events.Body.String(), "refresh.queued") {
		t.Fatalf("refresh events = %d body=%s", events.Code, events.Body.String())
	}

	foreign := dispatch(request(http.MethodGet, "/api/v1/projects/project:other/refresh-runs", nil))
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign project refresh list = %d body=%s", foreign.Code, foreign.Body.String())
	}
	missing := dispatch(request(http.MethodGet, createPath+"/missing", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing refresh GET = %d body=%s", missing.Code, missing.Body.String())
	}

	storage := dispatch(request(http.MethodGet, "/admin/storage", nil))
	if storage.Code != http.StatusOK || !strings.Contains(storage.Body.String(), `section="storage"`) {
		t.Fatalf("admin storage shell = %d body=%s", storage.Code, storage.Body.String())
	}
}

// requestWithHeaders keeps the request construction in the journey explicit
// while allowing command-specific protocol headers to be supplied.
type postgresRefreshJourneySessionEvidenceContextKey struct{}

func requestWithHeaders(fixture *PostgresJourneyFixture, sessionToken, apiToken string, sessionEvidence access.CredentialEvidence, method, path string, body io.Reader, headers map[string]string) *http.Request {
	req := fixture.Request(context.Background(), method, path, body)
	req.AddCookie(&http.Cookie{Name: "lv_session", Value: sessionToken})
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req = req.WithContext(context.WithValue(req.Context(), postgresRefreshJourneySessionEvidenceContextKey{}, sessionEvidence))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

type refreshJourneyRunResponse struct {
	ID            string                       `json:"id"`
	Identity      projectgraph.ServingIdentity `json:"identity"`
	PipelineID    string                       `json:"pipelineId"`
	SemanticModel string                       `json:"semanticModel"`
	Status        string                       `json:"status"`
}

type journeyRefreshStateReader struct {
	state    servingstate.State
	artifact servingstate.Artifact
}

func (r journeyRefreshStateReader) ActiveArtifact(_ context.Context, _ projectgraph.ResourceID, _ servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return r.state, r.artifact, nil
}
func (r journeyRefreshStateReader) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	if id != r.state.ID {
		return servingstate.State{}, errors.New("serving state not found")
	}
	return r.state, nil
}
func (r journeyRefreshStateReader) ArtifactByServingState(_ context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	if id != r.artifact.ServingStateID {
		return servingstate.Artifact{}, errors.New("serving artifact not found")
	}
	return r.artifact, nil
}

type journeyRefreshArtifactLoader struct {
	definition *refreshartifact.Definition
}

func (l journeyRefreshArtifactLoader) Load(context.Context, servingstate.Artifact) (refreshrun.LoadedArtifact, error) {
	if l.definition == nil {
		return refreshrun.LoadedArtifact{}, errors.New("journey refresh artifact is unavailable")
	}
	return refreshrun.LoadedArtifact{Definition: l.definition}, nil
}

func journeyRefreshDefinition() *refreshartifact.Definition {
	modelTable := semanticmodel.Table{ModelName: postgresRefreshJourneyTable}
	model := &semanticmodel.Model{
		Name:     postgresRefreshJourneyModel,
		Tables:   map[string]semanticmodel.Table{postgresRefreshJourneyTable: modelTable},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{postgresRefreshJourneyTable: {Model: postgresRefreshJourneyTable}},
	}
	return &refreshartifact.Definition{
		Models:      map[string]*semanticmodel.Model{postgresRefreshJourneyModel: model},
		ModelTables: map[string]semanticmodel.Table{postgresRefreshJourneyTable: modelTable},
		Pipelines: map[string]refreshschedule.Definition{postgresRefreshJourneyPipeline: {
			ID: postgresRefreshJourneyPipeline, Name: "Journey refresh", SemanticModelID: postgresRefreshJourneyModel,
			SelectionDigest: postgresRefreshJourneyArtifact,
		}},
	}
}
