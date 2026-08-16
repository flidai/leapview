package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/command"
	lddatastar "github.com/flidai/leapview/internal/dashboard/datastar"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationsqlite "github.com/flidai/leapview/internal/dashboard/publication/sqlite"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
)

type spatialTileAcceptanceMetrics struct {
	fakeMetrics
}

func (*spatialTileAcceptanceMetrics) QueryVisualizationTile(ctx context.Context, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	metadata := dataquery.MetadataFromContext(ctx)
	if dashboardID != "executive-sales" || visualID != "orders" || revision != "active-auth" || metadata.PrincipalID == "" {
		return dashboardruntime.SpatialTileResult{}, errors.New("tile revision scope unavailable")
	}
	return dashboardruntime.SpatialTileResult{Bytes: []byte{0x1a, 0x00}, Features: 1, Precision: "raw", CacheOutcome: "hit"}, nil
}

func (*spatialTileAcceptanceMetrics) QueryPublicVisualizationTile(ctx context.Context, publicID, dashboardID, visualID, revision string, zoom, x, y int) (dashboardruntime.SpatialTileResult, error) {
	metadata := dataquery.MetadataFromContext(ctx)
	wantPrincipal := access.DashboardPublicationSubjectID("test-workspace", "website")
	if publicID != "opaque-public-id-12345678901234" || dashboardID != "executive-sales" || visualID != "orders" || revision != "active-public" || metadata.PrincipalID != wantPrincipal || metadata.Surface != dataquery.SurfacePublicDashboard {
		return dashboardruntime.SpatialTileResult{}, errors.New("tile revision scope unavailable")
	}
	return dashboardruntime.SpatialTileResult{Bytes: []byte{0x1a, 0x00}, Features: 1, Precision: "aggregate", CacheOutcome: "miss"}, nil
}

func TestSpatialTileHTTPAuthorizationExpiryAndPublicationInvalidation(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	metrics := &spatialTileAcceptanceMetrics{}
	server := assembleRuntime(NewMultiWorkspaceMetrics(map[string]QueryMetrics{"test-workspace": metrics}), testStoreOptions(store, assemblyConfig{}))

	request := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		server.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}
	publicPath := "/public/dashboards/opaque-public-id-12345678901234/visuals/orders/tiles/active-public/2/1/1.mvt"
	public := request(publicPath)
	if public.Code != http.StatusOK || public.Header().Get("Cache-Control") != "public, immutable" || public.Header().Get("X-LeapView-Tile-Precision") != "aggregate" {
		t.Fatalf("public tile = %d headers=%v body=%q", public.Code, public.Header(), public.Body.Bytes())
	}

	for name, path := range map[string]string{
		"expired revision":    "/public/dashboards/opaque-public-id-12345678901234/visuals/orders/tiles/expired/2/1/1.mvt",
		"cross-public replay": "/public/dashboards/another-publication/visuals/orders/tiles/active-public/2/1/1.mvt",
		"malformed xyz":       "/public/dashboards/opaque-public-id-12345678901234/visuals/orders/tiles/active-public/2/4/1.mvt",
	} {
		t.Run(name, func(t *testing.T) {
			response := request(path)
			if response.Code != http.StatusNotFound && name != "malformed xyz" {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			if name == "malformed xyz" && response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
		})
	}

	if _, err := store.SQLDB().Exec(`UPDATE dashboard_publications SET suspended_at = CURRENT_TIMESTAMP WHERE id = 'pub_website'`); err != nil {
		t.Fatal(err)
	}
	invalidated := request(publicPath)
	if invalidated.Code != http.StatusNotFound || invalidated.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invalidated publication tile = %d headers=%v body=%s", invalidated.Code, invalidated.Header(), invalidated.Body.String())
	}
}

func TestPublicDashboardDocumentsAreAnonymousAndRouteAware(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		WorkspaceID: "test-workspace", SecurityHeaders: SecurityHeaders(false),
	}))

	public := httptest.NewRecorder()
	server.Routes().ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/public/dashboards/opaque-public-id-12345678901234", nil))
	if public.Code != http.StatusOK {
		t.Fatalf("public status = %d, body=%s", public.Code, public.Body.String())
	}
	for _, want := range []string{
		`<lv-dashboard-page`, `presentation="public"`, `/public/dashboards/opaque-public-id-12345678901234/updates?`,
		`/public/dashboards/opaque-public-id-12345678901234/commands/filter`,
	} {
		if !strings.Contains(public.Body.String(), want) {
			t.Fatalf("public document missing %q:\n%s", want, public.Body.String())
		}
	}
	if strings.Contains(public.Body.String(), "lv-app-shell") || public.Header().Get("Set-Cookie") != "" {
		t.Fatalf("public document exposed app shell or cookie: headers=%v", public.Header())
	}
	if got := public.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("public X-Frame-Options = %q", got)
	}
	if csp := public.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("public CSP = %q", csp)
	}
	if public.Header().Get("Referrer-Policy") != "no-referrer" || public.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("public privacy headers = %v", public.Header())
	}
	if public.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("public Cache-Control = %q, want no-store", public.Header().Get("Cache-Control"))
	}

	embed := httptest.NewRecorder()
	server.Routes().ServeHTTP(embed, httptest.NewRequest(http.MethodGet, "/embed/dashboards/opaque-public-id-12345678901234", nil))
	if embed.Code != http.StatusOK {
		t.Fatalf("embed status = %d, body=%s", embed.Code, embed.Body.String())
	}
	if got := embed.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("embed X-Frame-Options = %q", got)
	}
	if csp := embed.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors https://leapview.dev https://partner.example") {
		t.Fatalf("embed CSP = %q", csp)
	}
	if !strings.Contains(embed.Body.String(), `presentation="embed"`) {
		t.Fatalf("embed document routes/presentation are wrong:\n%s", embed.Body.String())
	}
}

func TestPublicationDeploymentRequiresManagementPrivilege(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	states := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := states.Create(ctx, servingstate.CreateInput{WorkspaceID: "test", ProjectID: "project", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	validation := completeTestValidation("test", servingstate.Validation{
		Digest: "digest", ManifestJSON: "{}", ProjectID: "project",
		DashboardPublicationsJSON: `{"website":{"name":"website","dashboard":"showcase","defaultPage":"","dependencyAssetIds":null,"configurationDigest":""}}`,
	})
	if _, err := states.SaveValidated(ctx, created.ID, validation, zeroArtifact(created.ID, "test")); err != nil {
		t.Fatal(err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{ServingStateRepo: states, WorkspaceID: "test", DefaultEnvironment: "prod"}))
	targets := []apiadapter.TargetRequest{{Workspace: "test", CandidateID: string(created.ID)}}

	viewer := testPrincipal(t, ctx, store, "viewer-publication@example.com", "Viewer", "viewer")
	if err := server.routes.deploymentModule.AuthorizePublicationDeployment(ctx, viewer.ID, "prod", targets); !errors.Is(err, deploymentmodule.ErrPublicationForbidden) {
		t.Fatalf("viewer authorization error = %v", err)
	}
	owner := testPrincipal(t, ctx, store, "owner-publication@example.com", "Owner", "owner")
	if _, err := testAccessRepository(store).CreateGrant(ctx, access.GrantInput{
		Object:      access.ProjectEnvironmentObject("project", "prod"),
		SubjectType: access.SubjectPrincipal,
		SubjectID:   owner.ID,
		Privilege:   access.PrivilegeManagePublications,
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.routes.deploymentModule.AuthorizePublicationDeployment(ctx, owner.ID, "prod", targets); err != nil {
		t.Fatalf("owner authorization: %v", err)
	}
	if err := server.routes.deploymentModule.AuthorizePublicationDeployment(ctx, viewer.ID, "dev", targets); err != nil {
		t.Fatalf("development deployment authorization = %v, want publication check skipped", err)
	}
}

func TestPublicationAdminAuthorizationUsesAnyManagedWorkspace(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO workspaces (id, title) VALUES ('secondary', 'Secondary')`); err != nil {
		t.Fatal(err)
	}
	repo := testAccessRepository(store)
	principal, err := repo.SetPrincipalRole(ctx, access.PrincipalRoleInput{
		WorkspaceID: "secondary", Email: "secondary-owner@example.com", DisplayName: "Secondary Owner", Role: access.RoleOwner,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := repo.CreateAPIToken(ctx, principal.ID, "test")
	if err != nil {
		t.Fatal(err)
	}
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		WorkspaceID: "test", Auth: testAuth(store, "test", AuthConfig{APITokenOnly: true}),
	}))

	request := httptest.NewRequest(http.MethodGet, "/admin/publications", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("secondary workspace publication manager status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDisabledSuspendedAndRotatedPublicationIDsReturnNotFound(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test-workspace"}))
	request := func(path string) int {
		recorder := httptest.NewRecorder()
		server.Routes().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder.Code
	}
	path := "/public/dashboards/opaque-public-id-12345678901234"
	if _, err := store.SQLDB().Exec(`UPDATE dashboard_publications SET suspended_at = CURRENT_TIMESTAMP WHERE name = 'website'`); err != nil {
		t.Fatal(err)
	}
	if got := request(path); got != http.StatusNotFound {
		t.Fatalf("suspended status = %d", got)
	}
	if _, err := store.SQLDB().Exec(`UPDATE dashboard_publications SET suspended_at = NULL, public_id = 'rotated-public-id-123456789012345' WHERE name = 'website'`); err != nil {
		t.Fatal(err)
	}
	if got := request(path); got != http.StatusNotFound {
		t.Fatalf("rotated old id status = %d", got)
	}
	if _, err := store.SQLDB().Exec(`UPDATE dashboard_publications SET configured = 0, active_serving_state_id = NULL WHERE name = 'website'`); err != nil {
		t.Fatal(err)
	}
	if got := request("/public/dashboards/rotated-public-id-123456789012345"); got != http.StatusNotFound {
		t.Fatalf("disabled status = %d", got)
	}
}

func TestPublicDashboardDocumentsUseDedicatedRateLimitBucket(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		WorkspaceID: "test-workspace",
		RateLimits:  RateLimitConfig{Enabled: true, PublicPageLimit: 1, PublicPageWindow: time.Minute},
	}))
	handler := server.Routes()
	path := "/public/dashboards/opaque-public-id-12345678901234"
	request := func() int {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.10:1234"
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if first, second := request(), request(); first != http.StatusOK || second != http.StatusTooManyRequests {
		t.Fatalf("public page rate limit statuses = %d, %d", first, second)
	}
}

func TestDashboardPublicationManagementAPIRequiresAndReplaysIdempotencyKeys(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		WorkspaceID: "test-workspace", PublicURL: "https://app.leapview.dev",
		Auth: testAuth(store, "test-workspace", AuthConfig{DevBypass: true, DevAPIToken: "local-secret"}),
	}))

	list := httptest.NewRecorder()
	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test-workspace/dashboard-publications", nil)
	listRequest.Header.Set("Authorization", "Bearer local-secret")
	server.Routes().ServeHTTP(list, listRequest)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"publicUrl":"https://app.leapview.dev/public/dashboards/opaque-public-id-12345678901234"`) {
		t.Fatalf("list response = %d %s", list.Code, list.Body.String())
	}

	path := "/api/v1/workspaces/test-workspace/dashboard-publications/website/suspend"
	missing := httptest.NewRecorder()
	missingRequest := httptest.NewRequest(http.MethodPost, path, nil)
	missingRequest.Header.Set("Authorization", "Bearer local-secret")
	server.Routes().ServeHTTP(missing, missingRequest)
	if missing.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d, body=%s", missing.Code, missing.Body.String())
	}

	request := func() *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("Idempotency-Key", "suspend-website-1")
		r.Header.Set("Authorization", "Bearer local-secret")
		server.Routes().ServeHTTP(recorder, r)
		return recorder
	}
	first, replay := request(), request()
	if first.Code != http.StatusOK || replay.Code != first.Code || replay.Body.String() != first.Body.String() {
		t.Fatalf("idempotent suspend first=%d %s replay=%d %s", first.Code, first.Body.String(), replay.Code, replay.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"status":"suspended"`) || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("idempotent response headers=%v body=%s", replay.Header(), replay.Body.String())
	}
	contract, ok := dashboardgen.GetAPIGenOperationContract(dashboardgen.GenOperationSuspendDashboardPublication)
	if !ok || contract.Command == nil {
		t.Fatal("generated suspend publication command contract is missing")
	}
	events, err := testAccessRepository(store).ListAuditEvents(t.Context(), access.AuditEventFilter{
		WorkspaceID: "test-workspace", Action: contract.Command.Audit.SuccessAction,
	})
	if err != nil || len(events) != 1 || events[0].TargetType != "dashboard_publication" || events[0].Status != "success" {
		t.Fatalf("suspend publication audit events = %#v, err = %v", events, err)
	}
}

func TestPublicCommandsRequireMatchingLiveStreamAndSuspensionCancelsIt(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test-workspace"}))
	resolved, err := server.routes.dashboardModule.ResolvePublicDashboard(context.Background(), "opaque-public-id-12345678901234")
	if err != nil {
		t.Fatal(err)
	}
	clientID, instanceID, pageID := "client-a", "stream-a", "overview"
	streamID := lddatastar.StreamID(clientID, resolved.Publication.Dashboard, pageID, instanceID)
	version := publication.StreamVersion{PublicID: resolved.Publication.PublicID, ServingStateID: resolved.Publication.ServingStateID}
	streams := publicationsqlite.NewStreamRegistry(store.SQLDB())
	streamContext, unregister, err := streams.Register(context.Background(), resolved.Publication.ID, streamID, version)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	guard := server.routes.dashboardModule.PublicDashboardHTTP(resolved).CommandGuard
	request := command.Request{DashboardID: resolved.Publication.Dashboard, ModelID: resolved.ModelID, PageID: pageID}
	signals := dashboard.Signals{Runtime: dashboard.Runtime{ClientID: clientID, StreamInstanceID: instanceID}}
	if err := guard(httptest.NewRequest(http.MethodPost, "/", nil), resolved.Metrics, request, signals); err != nil {
		t.Fatalf("matching stream rejected: %v", err)
	}
	secondServer := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test-workspace"}))
	secondResolved, err := secondServer.routes.dashboardModule.ResolvePublicDashboard(context.Background(), "opaque-public-id-12345678901234")
	if err != nil {
		t.Fatal(err)
	}
	if err := secondServer.routes.dashboardModule.PublicDashboardHTTP(secondResolved).CommandGuard(httptest.NewRequest(http.MethodPost, "/", nil), secondResolved.Metrics, request, signals); err != nil {
		t.Fatalf("matching stream was rejected by a second replica: %v", err)
	}
	signals.Runtime.StreamInstanceID = "other-stream"
	if err := guard(httptest.NewRequest(http.MethodPost, "/", nil), resolved.Metrics, request, signals); err == nil {
		t.Fatal("mismatched stream was accepted")
	}
	if _, err := store.SQLDB().Exec(`UPDATE dashboard_publications SET suspended_at = CURRENT_TIMESTAMP WHERE id = ?`, resolved.Publication.ID); err != nil {
		t.Fatal(err)
	}
	streams.ClosePublication(resolved.Publication.ID)
	select {
	case <-streamContext.Done():
	default:
		t.Fatal("suspension did not cancel the live publication stream")
	}
	signals.Runtime.StreamInstanceID = instanceID
	if err := guard(httptest.NewRequest(http.MethodPost, "/", nil), resolved.Metrics, request, signals); err == nil {
		t.Fatal("suspended publication command was accepted")
	}
}

func TestPublicationBrokerRelaysEventsAcrossReplicas(t *testing.T) {
	store := testStore(t)
	first := publicationsqlite.NewBroker(store.SQLDB(), nil, nil)
	second := publicationsqlite.NewBroker(store.SQLDB(), nil, nil)
	updates, unsubscribe := first.Subscribe("shared-public-stream")
	defer unsubscribe()

	second.PublishEnvelope("shared-public-stream", pagestream.Envelope{
		Signals:  pagestream.SignalPatch{"status": map[string]any{"generation": 2}},
		Delivery: pagestream.DeliveryMetadata{Generation: 2, Boundary: true},
	})
	select {
	case patch := <-updates:
		status, ok := patch["status"].(map[string]any)
		if !ok || status["generation"] != float64(2) {
			t.Fatalf("relayed patch = %#v", patch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publication event was not relayed across replicas")
	}
}

func TestPublicationCommandGenerationAdvancesAcrossReplicas(t *testing.T) {
	store := testStore(t)
	seedActivePublication(t, store, "opaque-public-id-12345678901234")
	first := publicationsqlite.NewStreamRegistry(store.SQLDB())
	second := publicationsqlite.NewStreamRegistry(store.SQLDB())
	version := publication.StreamVersion{PublicID: "opaque-public-id-12345678901234", ServingStateID: "state_public"}
	_, unregister, err := first.Register(context.Background(), "pub_website", "shared-command-stream", version)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	prepare := func(filters dashboard.Filters) (command.PreparedRefresh, error) {
		return command.PreparedRefresh{Filters: filters}, nil
	}
	_, firstGeneration, err := first.PrepareCommand(context.Background(), "pub_website", "shared-command-stream", version, prepare)
	if err != nil {
		t.Fatal(err)
	}
	_, secondGeneration, err := second.PrepareCommand(context.Background(), "pub_website", "shared-command-stream", version, prepare)
	if err != nil {
		t.Fatal(err)
	}
	if firstGeneration != 2 || secondGeneration != 3 {
		t.Fatalf("distributed generations = %d, %d; want 2, 3", firstGeneration, secondGeneration)
	}
}

func seedActivePublication(t *testing.T, store *platform.Store, publicID string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{query: `INSERT INTO workspaces (id, title) VALUES ('test-workspace', 'Test Workspace')`},
		{query: `INSERT INTO serving_states (id, workspace_id, project_id, environment, status, source) VALUES ('state_public', 'test-workspace', 'site', 'prod', 'active', 'publish')`},
		{query: `INSERT INTO dashboard_publications (id, project_id, workspace_id, name, public_id, dashboard, default_page, configuration_digest, allowed_origins_json, dependency_asset_ids_json, configured, active_serving_state_id, configured_at) VALUES ('pub_website', 'site', 'test-workspace', 'website', ?, 'executive-sales', 'overview', 'sha256:test', '["https://partner.example","https://leapview.dev"]', '["dashboard:test-workspace.executive-sales","semantic_model:test-workspace.test"]', 1, 'state_public', CURRENT_TIMESTAMP)`, args: []any{publicID}},
	}
	for _, statement := range statements {
		if _, err := store.SQLDB().Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}
