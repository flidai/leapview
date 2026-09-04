package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehost "github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/google/uuid"
)

const postgresPublicJourneyGenerationID = "0198f2c0-7c7a-7f00-8a11-000000000001"

func TestPostgresPublicDashboardJourney(t *testing.T) {
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{
		NativeDashboard:   true,
		SkipRouteAssembly: true,
	})
	fixture.assembleNativeDashboard(t, PostgresJourneyFixtureOptions{TargetID: postgresJourneyTargetID, ProjectID: postgresJourneyProject})
	principal, err := fixture.SeedPrincipal(t.Context(), access.PrincipalInput{
		Kind:        access.PrincipalKindUser,
		Email:       "journey-admin@example.test",
		DisplayName: "Journey Admin",
	})
	if err != nil {
		t.Fatalf("seed journey principal: %v", err)
	}
	journeyToken, _, err := fixture.Graph.Access.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: principal.ID, Name: "journey-public-dashboard", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create journey API token: %v", err)
	}
	auth := accessmodule.NewAuth(fixture.AccessPersistence.Repository, accessmodule.AuthConfig{
		APITokenOnly: true, CSRFKey: strings.Repeat("journey-auth", 4),
	})
	accessSurface, err := accessmodule.Build(t.Context(), accessmodule.Config{
		ExistingAuth: auth,
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return postgresJourneyProject, nil
		},
	})
	if err != nil {
		t.Fatalf("build authenticated PostgreSQL access surface: %v", err)
	}
	runtime := newPostgresPublicJourneyRuntime(t, postgresJourneyProject, principal.ID)
	handler := assemblePostgresPublicJourneyRoutes(t, fixture, runtime, accessSurface, auth)

	if err := fixture.Graph.Project.EnsureIdentity(t.Context(), postgresJourneyProject); err != nil {
		t.Fatalf("ensure journey project identity: %v", err)
	}
	// Exercise one native authoring mutation through the generated API route
	// without client-supplied request/correlation headers. RequestCorrelation
	// must generate canonical UUIDv7 identities that the event/audit boundary
	// can carry transactionally.
	authoringCreate := fixture.Request(t.Context(), http.MethodPost, "/api/v1/projects/"+postgresJourneyProject.String()+"/authoring/drafts", strings.NewReader(`{"title":"Journey dashboard","semanticModel":"semantic:journey"}`))
	authoringCreate.Header.Set("Authorization", "Bearer "+journeyToken)
	authoringCreate.Header.Set("Content-Type", "application/json")
	authoringCreate.Header.Set("Idempotency-Key", "0198f2c0-7c7a-7f00-8a11-000000000011")
	authoringResponse := httptest.NewRecorder()
	handler.ServeHTTP(authoringResponse, authoringCreate)
	if authoringResponse.Code != http.StatusCreated {
		t.Fatalf("native authoring create = %d body=%s", authoringResponse.Code, authoringResponse.Body.String())
	}
	generatedCorrelation := authoringResponse.Header().Get("X-Correlation-ID")
	parsedCorrelation, parseCorrelationErr := uuid.Parse(generatedCorrelation)
	if parseCorrelationErr != nil || parsedCorrelation.String() != generatedCorrelation || parsedCorrelation.Version() != 7 {
		t.Fatalf("generated authoring correlation = %q, want canonical UUIDv7", generatedCorrelation)
	}
	if err := fixture.SeedNativePublication(t.Context(), publication.ReconcileInput{
		ProjectID:      postgresJourneyProject,
		ServingStateID: postgresPublicJourneyGenerationID,
		ActorID:        principal.ID,
		Publications: map[string]publication.Definition{
			"website": {
				Name:                "website",
				Dashboard:           "executive-sales",
				DefaultPage:         "overview",
				ConfigurationDigest: "sha256:" + strings.Repeat("a", 64),
				AllowedOrigins:      []string{"https://partner.example", "https://leapview.dev"},
				DependencyAssetIDs:  []string{"dashboard:executive-sales", "semantic-model:test"},
			},
		},
	}); err != nil {
		t.Fatalf("seed native publication: %v", err)
	}
	row, err := fixture.Graph.DashboardPublication.Get(t.Context(), postgresJourneyProject, "website")
	if err != nil {
		t.Fatalf("load seeded publication: %v", err)
	}
	if row.Status() != publication.StatusActive || row.Revision != 1 || row.ServingStateID != postgresPublicJourneyGenerationID {
		t.Fatalf("seeded publication = %#v", row)
	}
	oldPublicID := row.PublicID

	request := func(method, path string, body *strings.Reader) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := fixture.Request(t.Context(), method, path, body)
		handler.ServeHTTP(recorder, req)
		return recorder
	}
	resource, _ := access.NewResourceRef("executive-sales", projectgraph.KindDashboard)
	if allowed, authErr := authorizeProjectResources(t.Context(), accessSurface, runtime, principal.ID, postgresJourneyProject, []access.ResourceRef{resource}, access.CapabilityResourceRead); authErr != nil || !allowed {
		t.Fatalf("journey dashboard authorization allowed=%v err=%v", allowed, authErr)
	}

	publicPath := "/public/dashboards/" + oldPublicID
	embedPath := "/embed/dashboards/" + oldPublicID
	public := request(http.MethodGet, publicPath, strings.NewReader(""))
	if public.Code != http.StatusOK {
		t.Fatalf("public document status = %d, body=%s", public.Code, public.Body.String())
	}
	for _, want := range []string{
		`<lv-dashboard-page`,
		`presentation="public"`,
		publicPath + `/updates?`,
		publicPath + `/commands/filter`,
	} {
		if !strings.Contains(public.Body.String(), want) {
			t.Fatalf("public document missing %q:\n%s", want, public.Body.String())
		}
	}
	if strings.Contains(public.Body.String(), "lv-app-shell") || public.Header().Get("Set-Cookie") != "" {
		t.Fatalf("public document exposed private shell or cookie: headers=%v", public.Header())
	}
	if got := public.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("public X-Frame-Options = %q", got)
	}
	if csp := public.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("public CSP = %q", csp)
	}
	if public.Header().Get("Referrer-Policy") != "no-referrer" || public.Header().Get("X-Robots-Tag") != "noindex" || public.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public privacy headers = %v", public.Header())
	}

	embed := request(http.MethodGet, embedPath, strings.NewReader(""))
	if embed.Code != http.StatusOK {
		t.Fatalf("embed document status = %d, body=%s", embed.Code, embed.Body.String())
	}
	if embed.Header().Get("X-Frame-Options") != "" {
		t.Fatalf("embed X-Frame-Options = %q", embed.Header().Get("X-Frame-Options"))
	}
	if csp := embed.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors https://leapview.dev https://partner.example") {
		t.Fatalf("embed CSP = %q", csp)
	}
	if !strings.Contains(embed.Body.String(), `presentation="embed"`) || !strings.Contains(embed.Body.String(), publicPath+`/updates?`) {
		t.Fatalf("embed canonical route/presentation missing:\n%s", embed.Body.String())
	}

	unknown := request(http.MethodGet, "/public/dashboards/unknown-publication", strings.NewReader(""))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown publication = %d headers=%v", unknown.Code, unknown.Header())
	}

	apiRequest := func(method, path, key string, revision int64) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		req := fixture.Request(t.Context(), method, path, strings.NewReader(""))
		req.Header.Set("Authorization", "Bearer "+journeyToken)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("If-Match", `"`+strconv.FormatInt(revision, 10)+`"`)
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	list := apiRequest(http.MethodGet, "/api/v1/projects/"+postgresJourneyProject.String()+"/dashboard-publications", "", 0)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"publicUrl":"http://localhost/public/dashboards/`+oldPublicID+`"`) || !strings.Contains(list.Body.String(), `"embedUrl":"http://localhost/embed/dashboards/`+oldPublicID+`"`) {
		t.Fatalf("publication list = %d %s", list.Code, list.Body.String())
	}

	suspendPath := "/api/v1/projects/" + postgresJourneyProject.String() + "/dashboard-publications/website/suspend"
	missingKey := httptest.NewRecorder()
	missingRequest := fixture.Request(t.Context(), http.MethodPost, suspendPath, strings.NewReader(""))
	missingRequest.Header.Set("Authorization", "Bearer "+journeyToken)
	missingRequest.Header.Set("If-Match", `"1"`)
	handler.ServeHTTP(missingKey, missingRequest)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d body=%s", missingKey.Code, missingKey.Body.String())
	}

	suspendKey := "0198f2c0-7c7a-7f00-8a11-000000000021"
	firstSuspend := apiRequest(http.MethodPost, suspendPath, suspendKey, 1)
	replaySuspend := apiRequest(http.MethodPost, suspendPath, suspendKey, 1)
	if firstSuspend.Code != http.StatusOK || replaySuspend.Code != http.StatusOK || firstSuspend.Body.String() != replaySuspend.Body.String() || replaySuspend.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("suspend idempotency first=%d %s replay=%d headers=%v body=%s", firstSuspend.Code, firstSuspend.Body.String(), replaySuspend.Code, replaySuspend.Header(), replaySuspend.Body.String())
	}
	if !strings.Contains(firstSuspend.Body.String(), `"status":"suspended"`) {
		t.Fatalf("suspend response = %s", firstSuspend.Body.String())
	}
	row, err = fixture.Graph.DashboardPublication.Get(t.Context(), postgresJourneyProject, "website")
	if err != nil {
		t.Fatalf("load suspended publication: %v", err)
	}
	if row.Status() != publication.StatusSuspended || row.Revision != 2 || row.SuspendedBy != principal.ID || row.SuspendedAt == "" {
		t.Fatalf("suspended durable publication = %#v", row)
	}
	events, err := fixture.Graph.DashboardPublication.ListEvents(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("list publication events after suspend: %v", err)
	}
	if len(events) != 2 || events[0].Type != "dashboard_publication.suspended" {
		t.Fatalf("publication events after suspend = %#v", events)
	}
	suspended := request(http.MethodGet, publicPath, strings.NewReader(""))
	if suspended.Code != http.StatusNotFound {
		t.Fatalf("suspended public document = %d headers=%v", suspended.Code, suspended.Header())
	}

	resumePath := "/api/v1/projects/" + postgresJourneyProject.String() + "/dashboard-publications/website/resume"
	resumed := apiRequest(http.MethodPost, resumePath, "0198f2c0-7c7a-7f00-8a11-000000000022", 2)
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), `"status":"active"`) {
		t.Fatalf("resume = %d %s", resumed.Code, resumed.Body.String())
	}
	row, err = fixture.Graph.DashboardPublication.Get(t.Context(), postgresJourneyProject, "website")
	if err != nil {
		t.Fatalf("load resumed publication: %v", err)
	}
	if row.Status() != publication.StatusActive || row.Revision != 3 || row.SuspendedAt != "" {
		t.Fatalf("resumed durable publication = %#v", row)
	}

	rotatePath := "/api/v1/projects/" + postgresJourneyProject.String() + "/dashboard-publications/website/rotate"
	rotated := apiRequest(http.MethodPost, rotatePath, "0198f2c0-7c7a-7f00-8a11-000000000023", 3)
	if rotated.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rotated.Code, rotated.Body.String())
	}
	row, err = fixture.Graph.DashboardPublication.Get(t.Context(), postgresJourneyProject, "website")
	if err != nil {
		t.Fatalf("load rotated publication: %v", err)
	}
	if row.Status() != publication.StatusActive || row.Revision != 4 || row.PublicID == oldPublicID || row.RotatedAt == "" {
		t.Fatalf("rotated durable publication = %#v", row)
	}
	if got := request(http.MethodGet, publicPath, strings.NewReader("")); got.Code != http.StatusNotFound {
		t.Fatalf("rotated old public ID status = %d", got.Code)
	}
	newPublicPath := "/public/dashboards/" + row.PublicID
	if got := request(http.MethodGet, newPublicPath, strings.NewReader("")); got.Code != http.StatusOK {
		t.Fatalf("rotated new public ID status = %d body=%s", got.Code, got.Body.String())
	}
	events, err = fixture.Graph.DashboardPublication.ListEvents(t.Context(), row.ID)
	if err != nil {
		t.Fatalf("list publication events after rotate: %v", err)
	}
	if len(events) != 4 || events[0].Type != "dashboard_publication.rotated" || events[1].Type != "dashboard_publication.resumed" || events[2].Type != "dashboard_publication.suspended" || events[3].Type != "dashboard_publication.configured" {
		t.Fatalf("publication event history = %#v", events)
	}
}

func assemblePostgresPublicJourneyRoutes(t *testing.T, fixture *PostgresJourneyFixture, runtime *runtimehostmodule.Module, accessSurface *accessmodule.Module, auth *accessmodule.Auth) http.Handler {
	t.Helper()
	data := dataAssemblyInputs{
		PlatformHealth: fixture.RuntimePool, ServingStateRepo: fixture.Graph.ServingState,
		AccessRepo: fixture.Graph.Access, APIIdempotency: fixture.Graph.Idempotency,
		CursorSigning: fixture.Graph.CursorSigning, RequireExplicitAPIProtocol: true,
		DashboardPublicationReconciler: fixture.DashboardPublicationReconciler,
		DashboardPersistence:           fixture.Graph.DashboardPersistence, RefreshPersistence: fixture.RefreshPersistence,
		RequireNativeDashboard: true,
	}
	capabilities := capabilityAssemblyInputs{
		JobModule: fixture.JobsModule, AccessModule: accessSurface,
		AgentPersistence: fixture.Graph.AgentPersistence, Authoring: fixture.DashboardAuthoring,
	}
	runtimeConfig := runtimeAssemblyInputs{
		RuntimeHost: runtime, ProjectID: postgresJourneyProject,
		ProjectIDResolver:       func(context.Context) (projectgraph.ResourceID, error) { return postgresJourneyProject, nil },
		ServingSnapshotResolver: func(context.Context) (string, error) { return "", servingstate.ErrNotFound },
		InstanceID:              postgresJourneyTargetID, DefaultEnvironment: "prod", AllowDevAuthBypass: true,
	}
	routes, runtimeServices, platform, policy, err := buildApplicationSurfaces(t.Context(), fakeMetrics{}, data, capabilities, workflowAssemblyInputs{Workload: fixture.Workload, AgentSettings: fixture.Graph.Bootstrap, Auth: auth}, runtimeConfig, httpAssemblyInputs{PublicURL: "http://localhost"})
	if err != nil {
		t.Fatalf("assemble PostgreSQL public dashboard journey routes: %v", err)
	}
	t.Cleanup(func() { _ = platform.workers.Stop(context.Background()) })
	return Routes(routes, runtimeServices, platform, policy)
}

type postgresPublicJourneyRuntime struct {
	state       servingstate.State
	artifact    servingstate.Artifact
	graph       projectgraph.ProjectGraph
	principalID string
}

func newPostgresPublicJourneyRuntime(t *testing.T, projectID projectgraph.ResourceID, principalID string) *runtimehostmodule.Module {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "project"},
		{ID: "test", Kind: projectgraph.KindSemanticModel, Name: "test"},
		{ID: "executive-sales", Kind: projectgraph.KindDashboard, Name: "executive_sales"},
		{ID: "model.orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "connection:test", Kind: projectgraph.KindConnection, Name: "test_connection"},
		{ID: "source:test", Kind: projectgraph.KindSource, Name: "test_source"},
		{ID: "pipeline:visuals-refresh", Kind: projectgraph.KindPipeline, Name: "visuals_refresh"},
	}, nil)
	if err != nil {
		t.Fatalf("build journey runtime graph: %v", err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	state := servingstate.State{ID: postgresPublicJourneyGenerationID, ProjectID: projectID, Environment: "prod", Status: servingstate.StatusActive, Source: servingstate.SourcePublish, Digest: digest}
	artifact := servingstate.Artifact{ID: "artifact-journey", ServingStateID: state.ID, Digest: digest, Format: servingstate.ArtifactBundleFormat}
	repo := &postgresPublicJourneyRuntime{state: state, artifact: artifact, graph: graph, principalID: principalID}
	host, err := runtimehostmodule.Build(t.Context(), runtimehostmodule.Config{
		States: repo, ProjectID: projectID, Environment: "prod", Factory: repo, Authorization: postgresPublicJourneyAuthorizationInstaller{},
	})
	if err != nil {
		t.Fatalf("build journey runtime host: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	return host
}

func (r *postgresPublicJourneyRuntime) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	return r.state, r.artifact, nil
}
func (r *postgresPublicJourneyRuntime) ByID(context.Context, servingstate.ID) (servingstate.State, error) {
	return r.state, nil
}
func (r *postgresPublicJourneyRuntime) ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error) {
	return r.artifact, nil
}
func (r *postgresPublicJourneyRuntime) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}
func (r *postgresPublicJourneyRuntime) Prepare(_ context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(input.State.Environment), string(input.State.ID))
	if err != nil {
		return nil, err
	}
	subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, r.principalID)
	if err != nil {
		return nil, err
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshotWithRoleBindings(identity, r.graph, []accesssnapshot.RoleBinding{{ID: "journey-admin-binding", Name: "Journey administrator", Subject: subject, Role: access.ProjectRoleAdmin, Capabilities: access.ProjectRoleCapabilities(access.ProjectRoleAdmin)}}, nil, nil)
	if err != nil {
		return nil, err
	}
	return postgresPublicJourneyPreparedRuntime{snapshot: snapshot}, nil
}

type postgresPublicJourneyPreparedRuntime struct {
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (postgresPublicJourneyPreparedRuntime) Close() error { return nil }
func (r postgresPublicJourneyPreparedRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.snapshot
}

type postgresPublicJourneyAuthorizationInstaller struct{}

func (postgresPublicJourneyAuthorizationInstaller) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

var _ runtimehost.RuntimeFactory = (*postgresPublicJourneyRuntime)(nil)
var _ runtimehost.ServingStateRepository = (*postgresPublicJourneyRuntime)(nil)
var _ runtimehost.AuthorizationSnapshotInstaller = postgresPublicJourneyAuthorizationInstaller{}
