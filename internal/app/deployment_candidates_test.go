package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/manageddata"
	manageddatasqlite "github.com/flidai/leapview/internal/manageddata/sqlite"
	"github.com/flidai/leapview/internal/platform"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatesqlite "github.com/flidai/leapview/internal/servingstate/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	"github.com/flidai/leapview/internal/workspace/api"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
	"github.com/gorilla/csrf"
)

const testOlistManagedRevision = "sha256:2ee6eed1b2527ed7729965453ce0ef136157784b5f55028c85e3825614a25944"

func testOlistManagedRevisions() map[string]string {
	return map[string]string{"olist": testOlistManagedRevision}
}

func seedTestOlistManagedRevision(t *testing.T, store *platform.Store) {
	t.Helper()
	ctx := context.Background()
	repository := manageddatasqlite.NewRepository(store.SQLDB())
	collection, err := repository.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ID: "collection_test_olist", ProjectID: "leapview-showcase", ConnectionName: "olist", Name: "Olist",
	})
	if err != nil {
		t.Fatalf("create managed Olist collection: %v", err)
	}
	session, err := repository.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: "upload_test_olist", CollectionID: collection.ID, Manifest: manageddata.Manifest{}, StorageBackend: "local",
		StagingPrefix: "test/olist", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create managed Olist upload: %v", err)
	}
	revision, err := repository.CompleteUpload(ctx, manageddata.CompleteUploadInput{SessionID: session.ID, RevisionID: "revision_test_olist"})
	if err != nil {
		t.Fatalf("complete managed Olist upload: %v", err)
	}
	if revision.Digest != testOlistManagedRevision {
		t.Fatalf("managed Olist revision digest = %q, want %q", revision.Digest, testOlistManagedRevision)
	}
}

type fakeReloader struct {
	prepareCalls int
	commitCalls  int
	prepareErr   error
}

type runtimeAssetMetrics struct {
	fakeMetrics
}

type emptyPageRuntimeAssetMetrics struct {
	fakeMetrics
}

type testRuntimeProvider struct {
	runtime runtimehost.Runtime
	err     error
}

type testWorkspaceAssetRuntime struct {
	assets []workspace.Asset
	edges  []workspace.AssetEdge
}

type workspaceAssetGraphProvider interface {
	WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool)
}

type authorizationGraphProvider struct{}

func (authorizationGraphProvider) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	wid := workspace.WorkspaceID(workspaceID)
	sid := workspace.ServingStateID(servingStateID)
	catalog := authorizationTestAsset(wid, sid, workspace.AssetTypeCatalog, workspaceID, "", "Catalog")
	visible := authorizationTestAsset(wid, sid, workspace.AssetTypeDashboard, "visible", catalog.ID, "Visible")
	hidden := authorizationTestAsset(wid, sid, workspace.AssetTypeDashboard, "hidden", catalog.ID, "Hidden")
	return []workspace.Asset{catalog, visible, hidden}, []workspace.AssetEdge{
		workspace.NewAssetEdge(wid, sid, catalog.ID, visible.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(wid, sid, catalog.ID, hidden.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(wid, sid, visible.ID, hidden.ID, workspace.AssetEdgeUsesSemanticModel),
	}, true
}

func authorizationTestAsset(workspaceID workspace.WorkspaceID, servingStateID workspace.ServingStateID, typ workspace.AssetType, key string, parentID workspace.AssetID, title string) workspace.Asset {
	asset, err := workspace.NewAssetWithSourceFile(workspaceID, servingStateID, typ, key, parentID, title, "", "testdata/"+key+".yaml", string(typ)+".v1", map[string]any{"key": key})
	if err != nil {
		panic(err)
	}
	return asset
}

func (runtimeAssetMetrics) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	connection, err := testWorkspaceAsset(
		workspace.WorkspaceID(workspaceID),
		workspace.ServingStateID(servingStateID),
		workspace.AssetTypeConnection,
		"unsupported.remote",
		"",
		"remote",
		"Runtime connection",
		"connection.v1",
		map[string]any{"Kind": "unsupported"},
	)
	if err != nil {
		return nil, nil, false
	}
	return []workspace.Asset{connection}, nil, true
}

func (emptyPageRuntimeAssetMetrics) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	catalog, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeCatalog, workspaceID, "", "Catalog", "", "catalog.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	model, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeSemanticModel, "olist", catalog.ID, "Olist", "", "semantic_model.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	modelTable, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeModelTable, "olist.orders", model.ID, "orders", "", "model_table.v1", map[string]any{
		"PrimaryKey": "order_id",
		"Source":     "orders",
	})
	if err != nil {
		return nil, nil, false
	}
	table, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeSemanticTable, "olist.orders", model.ID, "orders", "", "semantic_table.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	dashboard, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeDashboard, "sales", catalog.ID, "Sales", "", "dashboard.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	return []workspace.Asset{catalog, model, modelTable, table, dashboard}, []workspace.AssetEdge{
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), catalog.ID, model.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), model.ID, modelTable.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), model.ID, table.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), catalog.ID, dashboard.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), dashboard.ID, model.ID, workspace.AssetEdgeUsesSemanticModel),
	}, true
}

func (emptyPageRuntimeAssetMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if modelID != "olist" {
		return fakeMetrics{}.SemanticModel(modelID)
	}
	return &semanticmodel.Model{
		Name: "olist",
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {Source: "orders", PrimaryKey: "order_id"},
		},
	}, true
}

func testWorkspaceAsset(workspaceID workspace.WorkspaceID, servingStateID workspace.ServingStateID, typ workspace.AssetType, key string, parentID workspace.AssetID, title, description, payloadSchema string, payload any) (workspace.Asset, error) {
	sourceFile := "testdata/" + strings.ReplaceAll(string(typ)+"-"+key, ".", "-") + ".yaml"
	return workspace.NewAssetWithSourceFile(workspaceID, servingStateID, typ, key, parentID, title, description, sourceFile, payloadSchema, payload)
}

func (p testRuntimeProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &recordingLease{runtime: p.runtime}, nil
}

func (r testWorkspaceAssetRuntime) Close() error {
	return nil
}

func (r testWorkspaceAssetRuntime) WorkspaceAssets(string, string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	return r.assets, r.edges, true
}

func (r *fakeReloader) PrepareServingState(context.Context, string) (servingstate.PreparedRuntime, error) {
	r.prepareCalls++
	if r.prepareErr != nil {
		return nil, r.prepareErr
	}
	return fakePreparedRuntime{}, nil
}

func (r *fakeReloader) ActivatePrepared(_ servingstate.PreparedRuntime, activate func() error) error {
	r.commitCalls++
	return activate()
}

type fakePreparedRuntime struct{}

func (fakePreparedRuntime) Close() error { return nil }

func TestCSRFMiddlewareAllowsBrowserPostWithToken(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	handler := auth.CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(csrf.Token(r)))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	getReq := httptest.NewRequest(http.MethodGet, "/form", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRec.Code, http.StatusOK)
	}

	postReq := httptest.NewRequest(http.MethodPost, "/form", nil)
	postReq.Header.Set("X-CSRF-Token", getRec.Body.String())
	postReq.Header.Set("Origin", "http://example.com")
	for _, cookie := range getRec.Result().Cookies() {
		postReq.AddCookie(cookie)
	}
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want %d, body=%s", postRec.Code, http.StatusNoContent, postRec.Body.String())
	}
}

func TestCSRFMiddlewareCookieCoversDashboardCommands(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	handler := auth.CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(csrf.Token(r)))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	getReq := httptest.NewRequest(http.MethodGet, "http://localhost:8120/workspaces/test-workspace/dashboards/executive-sales/pages/overview", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRec.Code, http.StatusOK)
	}
	cookies := getRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("GET did not set CSRF cookie")
	}
	foundCSRF := false
	for _, cookie := range cookies {
		if cookie.Name != csrfCookieName {
			continue
		}
		foundCSRF = true
		if cookie.Path != "/" {
			t.Fatalf("CSRF cookie path = %q, want /", cookie.Path)
		}
	}
	if !foundCSRF {
		t.Fatalf("GET did not set %s cookie", csrfCookieName)
	}

	postReq := httptest.NewRequest(http.MethodPost, "http://localhost:8120/workspaces/test/commands/visual-window", nil)
	postReq.Header.Set("X-CSRF-Token", getRec.Body.String())
	postReq.Header.Set("Referer", "http://localhost:8120/workspaces/test-workspace/dashboards/executive-sales/pages/overview")
	for _, cookie := range cookies {
		postReq.AddCookie(cookie)
	}
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want %d, body=%s", postRec.Code, http.StatusNoContent, postRec.Body.String())
	}
}

func TestCSRFMiddlewareAllowsPlainHTTPPostWithToken(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true, CookieSecure: false})
	handler := auth.CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(csrf.Token(r)))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	getReq := httptest.NewRequest(http.MethodGet, "http://localhost:8120/form", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRec.Code, http.StatusOK)
	}

	postReq := httptest.NewRequest(http.MethodPost, "http://localhost:8120/form", nil)
	postReq.Header.Set("X-CSRF-Token", getRec.Body.String())
	postReq.Header.Set("Referer", "http://localhost:8120/form")
	for _, cookie := range getRec.Result().Cookies() {
		postReq.AddCookie(cookie)
	}
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, want %d, body=%s", postRec.Code, http.StatusNoContent, postRec.Body.String())
	}
}

func TestSessionCookieUsesConfiguredSecureFlag(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true, CookieSecure: true})
	cookie := auth.SessionCookie("token", time.Now().Add(time.Hour))
	if !cookie.Secure {
		t.Fatal("session cookie Secure = false, want true")
	}
}

func TestWorkspaceAssetAPIListsActiveDeploymentAssets(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets?type=connection", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []api.AssetSummaryResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode assets response: %v body=%s", err, rec.Body.String())
	}
	if len(body.Items) != 1 {
		t.Fatalf("asset count = %d, want 1 body=%s", len(body.Items), rec.Body.String())
	}
	connection := body.Items[0]
	if connection.ID != "connection:olist" || connection.SnapshotID == "" || connection.SnapshotID == connection.ID {
		t.Fatalf("connection identity = %#v", connection)
	}
	if connection.PayloadSchema != "connection.v1" {
		t.Fatalf("connection payload schema = %q", connection.PayloadSchema)
	}
	if connection.ContentHash == "" {
		t.Fatal("connection content hash is empty")
	}
	var rawListBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rawListBody); err != nil {
		t.Fatalf("decode raw list response: %v", err)
	}
	listItems, _ := rawListBody["items"].([]any)
	if len(listItems) != 1 {
		t.Fatalf("raw list items = %#v", rawListBody["items"])
	}
	listConnection, _ := listItems[0].(map[string]any)
	if _, ok := listConnection["payload"]; ok {
		t.Fatalf("asset list included payload: %s", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"auth"`)) {
		t.Fatalf("connection API leaked auth content:\n%s", rec.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets/connection:olist", nil)
	detailReq.Header.Set("Authorization", "Bearer dev")
	detailReq.Header.Set("Accept", "application/json")
	detailRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("asset detail status = %d body=%s", detailRec.Code, detailRec.Body.String())
	}
	var detail api.AssetResponse
	if err := json.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode asset detail response: %v body=%s", err, detailRec.Body.String())
	}
	if detail.ID != connection.ID || detail.SnapshotID != connection.SnapshotID || detail.PayloadSchema != "connection.v1" {
		t.Fatalf("asset detail = %#v, list connection = %#v", detail, connection)
	}
	if detail.Payload["Kind"] != "managed" {
		t.Fatalf("asset detail payload = %#v", detail.Payload)
	}
	for _, targetOwned := range []string{"Host", "Port", "Database", "Username", "SSLMode", "credentials_configured", "Credentials"} {
		if _, ok := detail.Payload[targetOwned]; ok {
			t.Fatalf("asset detail payload contains target-owned field %q: %#v", targetOwned, detail.Payload)
		}
	}

	lineageReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets/connection:olist/lineage", nil)
	lineageReq.Header.Set("Authorization", "Bearer dev")
	lineageReq.Header.Set("Accept", "application/json")
	lineageRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(lineageRec, lineageReq)
	if lineageRec.Code != http.StatusOK {
		t.Fatalf("asset lineage status = %d body=%s", lineageRec.Code, lineageRec.Body.String())
	}
	var lineage api.AssetLineageResponse
	if err := json.Unmarshal(lineageRec.Body.Bytes(), &lineage); err != nil {
		t.Fatalf("decode asset lineage response: %v body=%s", err, lineageRec.Body.String())
	}
	if lineage.AssetID != "connection:olist" || !stringSliceHas(lineage.Upstream, "source:olist.orders") {
		t.Fatalf("asset lineage = %#v", lineage)
	}

	edgesReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/asset-edges", nil)
	edgesReq.Header.Set("Authorization", "Bearer dev")
	edgesReq.Header.Set("Accept", "application/json")
	edgesRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(edgesRec, edgesReq)
	if edgesRec.Code != http.StatusOK {
		t.Fatalf("edges status = %d body=%s", edgesRec.Code, edgesRec.Body.String())
	}
	var edgesBody struct {
		Items []api.AssetEdgeResponse `json:"items"`
	}
	if err := json.Unmarshal(edgesRec.Body.Bytes(), &edgesBody); err != nil {
		t.Fatalf("decode edge response: %v body=%s", err, edgesRec.Body.String())
	}
	foundLogicalCatalogEdge := false
	for _, edge := range edgesBody.Items {
		if strings.HasPrefix(edge.FromAssetID, "asset_") || strings.HasPrefix(edge.ToAssetID, "asset_") {
			t.Fatalf("edge uses snapshot id endpoint: %#v", edge)
		}
		if edge.Type == string(workspace.AssetEdgeContains) && edge.FromAssetID == "catalog:test" && edge.ToAssetID == "source:olist.orders" {
			foundLogicalCatalogEdge = true
		}
	}
	if !foundLogicalCatalogEdge {
		t.Fatalf("logical catalog->source edge missing: %#v", edgesBody.Items)
	}
}

func TestWorkspaceGraphAPIsFilterUnauthorizedNodesBeforeEdgesAndLineage(t *testing.T) {
	store := testStore(t)
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", authorizationGraphProvider{})
	ctx := context.Background()
	repo := testAccessRepository(store)
	principal := testPrincipal(t, ctx, store, "limited@example.com", "Limited", "")
	workspaceObject := access.WorkspaceObject("test")
	visibleObject := access.ItemObjectWithParent(access.SecurableDashboard, "test", "visible", workspaceObject)
	hiddenObject := access.ItemObjectWithParent(access.SecurableDashboard, "test", "hidden", workspaceObject)
	for _, object := range []access.ObjectRef{workspaceObject, visibleObject, hiddenObject} {
		if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
			t.Fatalf("upsert securable object %#v: %v", object, err)
		}
	}
	for _, grant := range []access.GrantInput{
		{Object: workspaceObject, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeUseWorkspace},
		{Object: visibleObject, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem},
	} {
		if _, err := repo.CreateGrant(ctx, grant); err != nil {
			t.Fatalf("create grant %#v: %v", grant, err)
		}
	}
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID: principal.ID,
		WorkspaceID: "test",
		Name:        "limited-graph",
		Privileges:  []access.Privilege{access.PrivilegeUseWorkspace, access.PrivilegeViewItem},
	})
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		return rec
	}

	for _, path := range []string{
		"/api/v1/workspaces/test/assets?include=all",
		"/api/v1/workspaces/test/asset-edges",
		"/api/v1/workspaces/test/active-asset-graph",
		"/api/v1/workspaces/test/assets/dashboard:visible/lineage",
	} {
		body := get(path).Body.String()
		if strings.Contains(body, "dashboard:hidden") || strings.Contains(body, `"hidden"`) {
			t.Fatalf("GET %s leaked inaccessible graph node or edge: %s", path, body)
		}
		if path != "/api/v1/workspaces/test/assets/dashboard:visible/lineage" && !strings.Contains(body, "dashboard:visible") {
			t.Fatalf("GET %s omitted readable node: %s", path, body)
		}
	}
}

func TestWorkspaceAssetAPIIncludeAllReturnsFullActiveServingStateGraph(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	defaultReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets", nil)
	defaultReq.Header.Set("Authorization", "Bearer dev")
	defaultReq.Header.Set("Accept", "application/json")
	defaultRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("default status = %d body=%s", defaultRec.Code, defaultRec.Body.String())
	}
	var defaultBody struct {
		Items []api.AssetSummaryResponse `json:"items"`
	}
	if err := json.Unmarshal(defaultRec.Body.Bytes(), &defaultBody); err != nil {
		t.Fatalf("decode default assets response: %v body=%s", err, defaultRec.Body.String())
	}

	fullReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/assets?include=all", nil)
	fullReq.Header.Set("Authorization", "Bearer dev")
	fullReq.Header.Set("Accept", "application/json")
	fullRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(fullRec, fullReq)
	if fullRec.Code != http.StatusOK {
		t.Fatalf("full status = %d body=%s", fullRec.Code, fullRec.Body.String())
	}
	var fullBody struct {
		Items []api.AssetSummaryResponse `json:"items"`
	}
	if err := json.Unmarshal(fullRec.Body.Bytes(), &fullBody); err != nil {
		t.Fatalf("decode full assets response: %v body=%s", err, fullRec.Body.String())
	}
	if len(fullBody.Items) <= len(defaultBody.Items) {
		t.Fatalf("full asset count = %d, default = %d", len(fullBody.Items), len(defaultBody.Items))
	}
	foundLowLevel := false
	for _, asset := range fullBody.Items {
		if asset.Type == string(workspace.AssetTypeField) || asset.Type == string(workspace.AssetTypeMeasure) {
			foundLowLevel = true
			break
		}
	}
	if !foundLowLevel {
		t.Fatalf("full asset list missing low-level graph assets: %#v", fullBody.Items)
	}
}

func TestWorkspaceActiveServingStateGraphAPIReturnsPayloadsAndEdges(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/active-asset-graph?environment=dev", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body api.WorkspaceAssetGraphResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode active graph response: %v body=%s", err, rec.Body.String())
	}
	if len(body.Assets) == 0 || len(body.Edges) == 0 {
		t.Fatalf("active graph response assets=%d edges=%d, want both populated", len(body.Assets), len(body.Edges))
	}
	foundPayload := false
	for _, asset := range body.Assets {
		if asset.Type == string(workspace.AssetTypeDashboard) && asset.Payload["ID"] == "executive-sales" && asset.ContentHash != "" {
			foundPayload = true
			break
		}
	}
	if !foundPayload {
		t.Fatalf("active graph response missing dashboard payload/content hash: %#v", body.Assets)
	}
}

func TestWorkspaceListUsesActiveDeploymentCatalogMetadata(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(context.Background(), workspace.EnsureInput{
		ID:          "test",
		Title:       "stale title",
		Description: "stale description",
	}); err != nil {
		t.Fatalf("ensure stale workspace row: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []api.WorkspaceResponse `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode workspaces response: %v body=%s", err, rec.Body.String())
	}
	if len(body.Items) != 1 {
		t.Fatalf("workspace count = %d body=%s", len(body.Items), rec.Body.String())
	}
	got := body.Items[0]
	if got.Title != "Sales Workspace" || got.Description != "Revenue, orders, and product category analysis." || got.ActiveServingStateID == "" {
		t.Fatalf("workspace = %#v, want active catalog metadata", got)
	}
}

func TestWorkspaceListPageDoesNotRenderWorkspaceScopedChat(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	rendered := renderedWithBootstrap(t, server, rec.Body.String(), "Bearer dev")
	for _, notWant := range []string{`/workspaces/test/chat`, `"workspaceTitle":"LeapView Workspace"`} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("workspace list rendered workspace-scoped chat %q:\n%s", notWant, rec.Body.String())
		}
	}
	if !strings.Contains(rendered, `"id":"chat"`) || !strings.Contains(rendered, `"href":"/chats"`) {
		t.Fatalf("workspace list did not render global chat navigation:\n%s", rec.Body.String())
	}
	if !strings.Contains(rendered, `"workspaceTitle":"LeapView"`) {
		t.Fatalf("workspace list did not render global app chrome:\n%s", rec.Body.String())
	}
}

func TestWorkspacePageDefaultsToTopLevelAssets(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "Bearer dev")
	for _, want := range []string{"Executive Sales", "Sales Semantic Model", "orders"} {
		if !strings.Contains(body, want) {
			t.Fatalf("workspace page missing top-level asset %q:\n%s", want, body)
		}
	}
	for _, notWant := range []string{"olist_orders_dataset.csv", "orders_enriched", "review_score"} {
		if strings.Contains(body, notWant) {
			t.Fatalf("workspace page rendered low-level asset %q:\n%s", notWant, body)
		}
	}
}

func TestWorkspaceAssetSearchStaysWorkspaceFacing(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test?q=orders_enriched", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, notWant := range []string{"Cache table", "Dataset", `>orders_enriched<`} {
		if strings.Contains(rec.Body.String(), notWant) {
			t.Fatalf("workspace search rendered internal asset vocabulary %q:\n%s", notWant, rec.Body.String())
		}
	}
}

func TestWorkspaceAssetFilterUpdatesWorkspacePage(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=workspace&workspace=test", nil)
	query := req.URL.Query()
	query.Set("datastar", `{"workspaceAssetType":"dashboard","workspaceAssetQuery":""}`)
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Authorization", "Bearer dev")
	rec := newSynchronizedRecorder()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		server.Routes().ServeHTTP(rec, req)
	}()

	waitForRecorderBodyContains(t, rec, `"activeType":"dashboard"`)
	cancel()
	<-returned

	ssetest.RequirePatchSignal(t, rec.BodyString(), func(patch map[string]any) bool {
		page, ok := patch["page"].(map[string]any)
		if !ok {
			return false
		}
		assetList, ok := page["assetList"].(map[string]any)
		if !ok || assetList["activeType"] != "dashboard" {
			return false
		}
		assets, ok := assetList["assets"].([]any)
		if !ok || len(assets) == 0 {
			return false
		}
		for _, value := range assets {
			asset, ok := value.(map[string]any)
			if !ok || asset["type"] != "dashboard" {
				return false
			}
		}
		return true
	})
}

func TestWorkspaceConnectionFilterRedirectsToGlobalConnections(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test?type=connection&q=olist", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusFound, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/connections?q=olist" {
		t.Fatalf("Location = %q, want /connections?q=olist", got)
	}
}

func TestWorkspaceSourceFilterRedirectsToConnectionSources(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test?type=source&q=orders", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusFound, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/connections?type=source&q=orders" {
		t.Fatalf("Location = %q, want /connections?type=source&q=orders", got)
	}
}

func TestConnectionsPageRendersGlobalConnectionSurface(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/connections?q=olist", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "Bearer dev")
	for _, want := range []string{"<lv-connections-page", "Connections", "Connection", "Source", "assetList", "Project-global managed Olist ecommerce demo data.", "orders"} {
		if !strings.Contains(body, want) {
			t.Fatalf("connections page missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, `/connections/connection:olist/details`) {
		t.Fatalf("connections page did not link to canonical connection details:\n%s", body)
	}
	if !strings.Contains(body, `/sources/source:olist.orders/details`) {
		t.Fatalf("connections page did not link to canonical source details:\n%s", body)
	}
	if strings.Contains(body, `/workspaces/test/assets/`) {
		t.Fatalf("connections page linked to workspace asset details:\n%s", body)
	}
	if strings.Contains(body, `data-workspace-asset-toolbar`) {
		t.Fatalf("connections page rendered workspace asset toolbar:\n%s", body)
	}
}

func TestConnectionsPageFiltersSources(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/connections?type=source&q=orders", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "Bearer dev")
	for _, want := range []string{"Source", "orders", `/connections/`, `/sources/`} {
		if !strings.Contains(body, want) {
			t.Fatalf("source-filtered connections page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Local CSV files for the Olist ecommerce demo dataset.") {
		t.Fatalf("source-filtered connections page included connection row:\n%s", body)
	}
}

func TestConnectionsSearchCommandPatchesOnlyResultRows(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodPost, "/connections/search", strings.NewReader(`{"entityListQuery":"orders","entityListFilter":"source"}`))
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 1 {
		t.Fatalf("patch count = %d, want 1: %s", len(patches), rec.Body.String())
	}
	page, ok := patches[0]["page"].(map[string]any)
	if !ok || len(patches[0]) != 1 || len(page) != 1 {
		t.Fatalf("search command patched more than result data: %#v", patches[0])
	}
	assetList, ok := page["assetList"].(map[string]any)
	if !ok || len(assetList) != 1 {
		t.Fatalf("search command asset list patch = %#v", page["assetList"])
	}
	assets, ok := assetList["assets"].([]any)
	if !ok || len(assets) == 0 {
		t.Fatalf("search command assets = %#v", assetList["assets"])
	}
	for _, value := range assets {
		asset, ok := value.(map[string]any)
		if !ok || asset["type"] != "source" || !strings.Contains(strings.ToLower(fmt.Sprint(asset["title"])), "orders") {
			t.Fatalf("unexpected connection search result: %#v", value)
		}
	}
}

func TestConnectionAssetRoutesUseConnectionSurface(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	connectionID := activeAssetID(t, store, "test", "connection", "olist")

	redirectReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID, nil)
	redirectReq.Header.Set("Authorization", "Bearer dev")
	redirectRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(redirectRec, redirectReq)
	if redirectRec.Code != http.StatusFound {
		t.Fatalf("connection canonical redirect status = %d body=%s", redirectRec.Code, redirectRec.Body.String())
	}
	if got := redirectRec.Header().Get("Location"); got != "/connections/"+connectionID+"/details" {
		t.Fatalf("connection redirect Location = %q, want /connections/%s/details", got, connectionID)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID+"/details", nil)
	detailReq.Header.Set("Authorization", "Bearer dev")
	detailRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("connection detail status = %d body=%s", detailRec.Code, detailRec.Body.String())
	}
	detailBody := renderedWithBootstrap(t, server, detailRec.Body.String(), "Bearer dev")
	for _, want := range []string{"Connections", "olist", "Sources", "order_items", "Lineage"} {
		if !strings.Contains(detailBody, want) {
			t.Fatalf("connection detail missing %q:\n%s", want, detailBody)
		}
	}
	for _, notWant := range []string{`Workspaces /`, `Back to workspace`} {
		if strings.Contains(detailBody, notWant) {
			t.Fatalf("connection detail rendered workspace chrome %q:\n%s", notWant, detailBody)
		}
	}

	lineageReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID+"/lineage", nil)
	lineageReq.Header.Set("Authorization", "Bearer dev")
	lineageRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(lineageRec, lineageReq)
	if lineageRec.Code != http.StatusOK {
		t.Fatalf("connection lineage status = %d body=%s", lineageRec.Code, lineageRec.Body.String())
	}
	lineageBody := renderedWithBootstrap(t, server, lineageRec.Body.String(), "Bearer dev")
	for _, want := range []string{"<lv-workspace-asset-page", "/static/asset-lineage-graph.js", "lineage", "usesTable"} {
		if !strings.Contains(lineageBody, want) {
			t.Fatalf("connection lineage missing %q:\n%s", want, lineageBody)
		}
	}
}

func TestConnectionSourceAssetRoutesUseConnectionScopedSurface(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	connectionID := activeAssetID(t, store, "test", "connection", "olist")
	sourceID := activeAssetID(t, store, "test", "source", "olist.orders")

	redirectReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID+"/sources/"+sourceID, nil)
	redirectReq.Header.Set("Authorization", "Bearer dev")
	redirectRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(redirectRec, redirectReq)
	if redirectRec.Code != http.StatusFound {
		t.Fatalf("source canonical redirect status = %d body=%s", redirectRec.Code, redirectRec.Body.String())
	}
	if got := redirectRec.Header().Get("Location"); got != "/connections/"+connectionID+"/sources/"+sourceID+"/details" {
		t.Fatalf("source redirect Location = %q, want canonical source details", got)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID+"/sources/"+sourceID+"/details", nil)
	detailReq.Header.Set("Authorization", "Bearer dev")
	detailRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("source detail status = %d body=%s", detailRec.Code, detailRec.Body.String())
	}
	detailBody := renderedWithBootstrap(t, server, detailRec.Body.String(), "Bearer dev")
	for _, want := range []string{"Connections", "Sources", "orders", "Fields", "Physical type", "Lineage"} {
		if !strings.Contains(detailBody, want) {
			t.Fatalf("source detail missing %q:\n%s", want, detailBody)
		}
	}
	for _, notWant := range []string{`Workspaces /`, `Back to workspace`, `/workspaces/test/assets/` + sourceID + `/details`} {
		if strings.Contains(detailBody, notWant) {
			t.Fatalf("source detail rendered workspace chrome %q:\n%s", notWant, detailBody)
		}
	}

	lineageReq := httptest.NewRequest(http.MethodGet, "/connections/"+connectionID+"/sources/"+sourceID+"/lineage", nil)
	lineageReq.Header.Set("Authorization", "Bearer dev")
	lineageRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(lineageRec, lineageReq)
	if lineageRec.Code != http.StatusOK {
		t.Fatalf("source lineage status = %d body=%s", lineageRec.Code, lineageRec.Body.String())
	}
	lineageBody := renderedWithBootstrap(t, server, lineageRec.Body.String(), "Bearer dev")
	for _, want := range []string{"<lv-workspace-asset-page", "/static/asset-lineage-graph.js", "lineage", "usesTable"} {
		if !strings.Contains(lineageBody, want) {
			t.Fatalf("source lineage missing %q:\n%s", want, lineageBody)
		}
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/connections/"+sourceID+"/sources/"+sourceID+"/details", nil)
	invalidReq.Header.Set("Authorization", "Bearer dev")
	invalidRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusNotFound {
		t.Fatalf("invalid source/connection pair status = %d, want 404 body=%s", invalidRec.Code, invalidRec.Body.String())
	}

	invalidRedirectReq := httptest.NewRequest(http.MethodGet, "/connections/"+sourceID+"/sources/"+sourceID, nil)
	invalidRedirectReq.Header.Set("Authorization", "Bearer dev")
	invalidRedirectRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(invalidRedirectRec, invalidRedirectReq)
	if invalidRedirectRec.Code != http.StatusNotFound {
		t.Fatalf("invalid source/connection redirect status = %d, want 404 body=%s", invalidRedirectRec.Code, invalidRedirectRec.Body.String())
	}
}

func TestWorkspaceConnectionAssetRedirectsToConnectionSurface(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	connectionID := activeAssetID(t, store, "test", "connection", "olist")

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test/assets/"+connectionID+"/details", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("workspace connection detail status = %d, want redirect body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/connections/"+connectionID+"/details" {
		t.Fatalf("workspace connection detail Location = %q, want /connections/%s/details", got, connectionID)
	}
}

func TestWorkspaceSourceAssetRedirectsToConnectionScopedSourceSurface(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	connectionID := activeAssetID(t, store, "test", "connection", "olist")
	sourceID := activeAssetID(t, store, "test", "source", "olist.orders")

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test/assets/"+sourceID+"/details", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("workspace source detail status = %d, want redirect body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/connections/"+connectionID+"/sources/"+sourceID+"/details" {
		t.Fatalf("workspace source detail Location = %q, want canonical source route", got)
	}
}

func TestWorkspaceAssetVersionsRouteShowsConfigHistory(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	assetID := activeAssetIDByType(t, store, "test", "dashboard")

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test/assets/"+assetID+"/versions", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("versions status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "Bearer dev")
	if !strings.Contains(body, "Config hash") || strings.Contains(body, "Deployment digest") {
		t.Fatalf("versions body does not show asset config history cleanly:\n%s", body)
	}
}

func TestConnectionsPageDoesNotFallbackToRuntimeAssetsWithoutActiveDeployment(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(runtimeAssetMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/connections", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Connections") {
		t.Fatalf("connections page missing heading:\n%s", body)
	}
	for _, forbidden := range []string{"unsupported.remote", "Runtime connection"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("connections page rendered runtime-only asset %q without active serving state:\n%s", forbidden, body)
		}
	}
}

func TestAssetViewsDefaultToConfiguredEnvironment(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "test", "dev", "Dev Dashboard", "Dev Connection")
	seedEnvironmentAssetDeployment(t, store, "test", "prod", "Prod Dashboard", "Prod Connection")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{DefaultWorkspaceID: "test", DefaultEnvironment: "prod"}))

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "workspace assets", path: "/workspaces/test", want: "Prod Dashboard"},
		{name: "global connections", path: "/connections", want: "Prod Connection"},
		{name: "global search", path: "/api/v1/search?workspace=test&q=dashboard", want: "Prod Dashboard"},
		{name: "query cannot override instance", path: "/workspaces/test?environment=dev", want: "Prod Dashboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer dev")
			rec := httptest.NewRecorder()
			server.Routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if strings.HasPrefix(tc.path, "/workspaces") || strings.HasPrefix(tc.path, "/connections") {
				body = renderedWithBootstrap(t, server, body, "")
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("body missing %q:\n%s", tc.want, body)
			}
		})
	}
}

func TestWorkspaceAssetsDoesNotRefreshCleanGraphWithoutPageItems(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	ctx := context.Background()
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	servingStateRepo := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := servingStateRepo.Create(ctx, servingstate.CreateInput{WorkspaceID: "test", CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	runtimeAssets, runtimeEdges, ok := emptyPageRuntimeAssetMetrics{}.WorkspaceAssets("test", string(created.ID))
	if !ok {
		t.Fatal("runtime graph unavailable")
	}
	validation := completeTestValidation("test", servingstate.Validation{
		Digest:       "digest",
		ManifestJSON: "{}",
		ProjectID:    "project",
		Graph:        testSnapshotGraph(t, workspace.AssetGraph{Assets: runtimeAssets, Edges: runtimeEdges}),
	})
	if _, err := servingStateRepo.SaveValidated(ctx, created.ID, validation, zeroArtifact(created.ID, "test")); err != nil {
		t.Fatalf("save validated: %v", err)
	}
	if _, err := servingStateRepo.Activate(ctx, "test", servingstate.DefaultEnvironment, created.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(emptyPageRuntimeAssetMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test", nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	graph, ok, err := workspacesqlite.NewRepository(store.SQLDB()).ActiveServingStateGraph(ctx, "test", string(servingstate.DefaultEnvironment))
	if err != nil {
		t.Fatalf("active graph: %v", err)
	}
	if !ok {
		t.Fatal("active graph ok = false")
	}
	for _, asset := range graph.Assets {
		if asset.Type == workspace.AssetTypePageItem {
			t.Fatalf("graph was unexpectedly refreshed with page item asset: %#v", asset)
		}
	}
}

func mustWorkspaceAsset(t *testing.T, workspaceID workspace.WorkspaceID, servingStateID workspace.ServingStateID, typ workspace.AssetType, key string, parentID workspace.AssetID, title string, content any) workspace.Asset {
	t.Helper()
	sourceFile := "testdata/" + strings.ReplaceAll(string(typ)+"-"+key, ".", "-") + ".yaml"
	asset, err := workspace.NewAssetWithSourceFile(workspaceID, servingStateID, typ, key, parentID, title, "", sourceFile, string(typ)+".v1", content)
	if err != nil {
		t.Fatalf("new asset %s %s: %v", typ, key, err)
	}
	return asset
}

func graphHasAssetType(graph workspace.AssetGraph, typ workspace.AssetType) bool {
	for _, asset := range graph.Assets {
		if asset.Type == typ {
			return true
		}
	}
	return false
}

func graphAssetByTypeAndKey(t *testing.T, graph workspace.AssetGraph, typ workspace.AssetType, key string) workspace.Asset {
	t.Helper()
	for _, asset := range graph.Assets {
		if asset.Type == typ && asset.Key == key {
			return asset
		}
	}
	t.Fatalf("graph missing asset %s %q", typ, key)
	return workspace.Asset{}
}

func TestWorkspacePermissionsRouteIsRemoved(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/workspaces/test/permissions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestWorkspaceRoleBindingAPIUpsertsPrincipalRole(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	repo := testAccessRepository(store)
	analyst, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: access.PrincipalIDForEmail("analyst@example.com"), Email: "analyst@example.com", DisplayName: "Analyst"})
	if err != nil {
		t.Fatalf("seed analyst: %v", err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	missingKeyReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/role-bindings", bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"viewer"}`))
	missingKeyReq.Header.Set("Authorization", "Bearer "+token)
	missingKeyReq.Header.Set("Content-Type", "application/json")
	missingKeyRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(missingKeyRec, missingKeyReq)
	if missingKeyRec.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency key status = %d body=%s", missingKeyRec.Code, missingKeyRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/role-bindings", bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"viewer"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "create-analyst-role-binding")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rec.Code, rec.Body.String())
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/role-bindings", bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"viewer"}`))
	replayReq.Header = req.Header.Clone()
	replayRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusCreated || replayRec.Header().Get("Idempotency-Replayed") != "true" || replayRec.Body.String() != rec.Body.String() {
		t.Fatalf("replay status = %d replayed=%q body=%s", replayRec.Code, replayRec.Header().Get("Idempotency-Replayed"), replayRec.Body.String())
	}
	var createdBinding struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &createdBinding); err != nil {
		t.Fatalf("decode binding: %v", err)
	}
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings/"+createdBinding.ID, nil)
	getReq.Header.Set("Authorization", "Bearer "+token)
	getRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || getRec.Header().Get("ETag") == "" {
		t.Fatalf("get status = %d etag=%q body=%s", getRec.Code, getRec.Header().Get("ETag"), getRec.Body.String())
	}
	staleReq := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/test/role-bindings/"+createdBinding.ID, bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"editor"}`))
	staleReq.Header.Set("Authorization", "Bearer "+token)
	staleReq.Header.Set("Content-Type", "application/json")
	staleReq.Header.Set("If-Match", `"stale"`)
	staleRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale update status = %d body=%s", staleRec.Code, staleRec.Body.String())
	}
	updateReq := httptest.NewRequest(http.MethodPatch, "/api/v1/workspaces/test/role-bindings/"+createdBinding.ID, bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"editor"}`))
	updateReq.Header.Set("Authorization", "Bearer "+token)
	updateReq.Header.Set("Accept", "application/json")
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.Header.Set("If-Match", getRec.Header().Get("ETag"))
	updateRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	if !strings.Contains(body, `"email":"analyst@example.com"`) || !strings.Contains(body, `"role":"editor"`) {
		t.Fatalf("role binding missing:\n%s", body)
	}
	if strings.Contains(body, `"role":"viewer"`) {
		t.Fatalf("role binding was duplicated instead of replaced:\n%s", body)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/test/role-bindings/"+createdBinding.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	for action, operationID := range map[string]string{
		"role_binding.created": "createRoleBinding",
		"role_binding.updated": "updateRoleBinding",
		"role_binding.deleted": "deleteRoleBinding",
	} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: action})
		if err != nil || len(events) != 1 {
			t.Fatalf("%s audit events = %#v, %v", action, events, err)
		}
		assertOperationAuditMetadata(t, events[0], operationID, "api")
	}
}

func TestWorkspaceRoleBindingAPIRejectsViewerWithoutMutation(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	viewer := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	repo := testAccessRepository(store)
	analyst, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: access.PrincipalIDForEmail("denied-analyst@example.com"), Email: "denied-analyst@example.com", DisplayName: "Denied Analyst"})
	if err != nil {
		t.Fatalf("seed analyst: %v", err)
	}
	token := testAPIToken(t, ctx, store, viewer.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/test/role-bindings", bytes.NewBufferString(`{"subjectType":"principal","subjectId":"`+analyst.ID+`","role":"viewer"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "denied-role-binding")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	bindings, err := repo.ListRoleBindings(ctx, "test")
	if err != nil {
		t.Fatalf("list role bindings: %v", err)
	}
	for _, binding := range bindings {
		if binding.SubjectID == analyst.ID {
			t.Fatalf("denied API request created binding %#v", binding)
		}
	}
}

func TestGroupDeleteIsWorkspaceScopedAndCleansMembershipsAndBindings(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	member, err := testAccessRepository(store).UpsertPrincipal(ctx, access.PrincipalInput{ID: access.PrincipalIDForEmail("member@example.com"), Email: "member@example.com", DisplayName: "Member"})
	if err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "other", Title: "Other"}); err != nil {
		t.Fatalf("ensure other workspace: %v", err)
	}
	repo := testAccessRepository(store)
	group, err := repo.UpsertGroup(ctx, access.GroupInput{ID: "group_test_finance", WorkspaceID: "test", Provider: "local", ExternalID: "finance", Name: "Finance"})
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	otherGroup, err := repo.UpsertGroup(ctx, access.GroupInput{ID: "group_other_finance", WorkspaceID: "other", Provider: "local", ExternalID: "finance", Name: "Other Finance"})
	if err != nil {
		t.Fatalf("seed other group: %v", err)
	}
	if err := repo.AddGroupMember(ctx, "test", group.ID, member.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	if err := repo.AddGroupMember(ctx, "other", otherGroup.ID, member.ID); err != nil {
		t.Fatalf("add other group member: %v", err)
	}
	if _, err := repo.CreateRoleBinding(ctx, access.RoleBindingInput{WorkspaceID: "test", SubjectType: access.SubjectGroup, SubjectID: group.ID, Role: access.RoleViewer}); err != nil {
		t.Fatalf("create group binding: %v", err)
	}
	if _, err := repo.CreateRoleBinding(ctx, access.RoleBindingInput{WorkspaceID: "other", SubjectType: access.SubjectGroup, SubjectID: otherGroup.ID, Role: access.RoleViewer}); err != nil {
		t.Fatalf("create other group binding: %v", err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	if !strings.Contains(body, `"subjectType":"group"`) || !strings.Contains(body, `"subjectId":"`+group.ID+`"`) {
		t.Fatalf("group role binding did not preserve group subject:\n%s", body)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/workspaces/test/groups/"+group.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteReq.Header.Set("Accept", "application/json")
	deleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if groups, err := repo.ListGroups(ctx, "test"); err != nil {
		t.Fatalf("list groups: %v", err)
	} else if len(groups) != 0 {
		t.Fatalf("test groups after delete = %#v, want none", groups)
	}
	if _, err := repo.ListGroupMembers(ctx, "test", group.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("list members after delete = %v, want sql.ErrNoRows", err)
	}
	if bindings, err := repo.ListRoleBindings(ctx, "test"); err != nil {
		t.Fatalf("list bindings after delete: %v", err)
	} else {
		for _, binding := range bindings {
			if binding.SubjectType == access.SubjectGroup && binding.SubjectID == group.ID {
				t.Fatalf("deleted group binding remained: %#v", binding)
			}
		}
	}
	if groups, err := repo.ListGroups(ctx, "other"); err != nil {
		t.Fatalf("list other groups: %v", err)
	} else if len(groups) != 1 || groups[0].ID != otherGroup.ID {
		t.Fatalf("other groups after delete = %#v, want %s", groups, otherGroup.ID)
	}
	if members, err := repo.ListGroupMembers(ctx, "other", otherGroup.ID); err != nil {
		t.Fatalf("list other members after delete: %v", err)
	} else if len(members) != 1 || members[0].PrincipalID != member.ID {
		t.Fatalf("other group members after delete = %#v, want member", members)
	}
}

func TestWorkspaceAccessCommandUpsertsAndPatchesSignals(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	repo := testAccessRepository(store)
	analyst := testPrincipal(t, ctx, store, "analyst@example.com", "Analyst", "")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	signals := `{"workspaceAccess":{"command":{"email":"","role":"data_deployer","subjectType":"principal","subjectId":"` + analyst.ID + `"}}}`
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/access/upsert", bytes.NewBufferString(signals))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"event: datastar-patch-signals", "workspaceAccess", "analyst@example.com", "Access updated."} {
		if !strings.Contains(body, want) {
			t.Fatalf("workspace access upsert did not patch %q:\n%s", want, body)
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), `"email":"analyst@example.com"`) || !strings.Contains(listRec.Body.String(), `"role":"data_deployer"`) {
		t.Fatalf("role binding missing after command:\n%s", listRec.Body.String())
	}

	bindings, err := repo.ListRoleBindings(ctx, "test")
	if err != nil {
		t.Fatalf("list role bindings: %v", err)
	}
	bindingID := ""
	for _, binding := range bindings {
		if binding.SubjectType == access.SubjectPrincipal && binding.SubjectID == analyst.ID {
			bindingID = binding.ID
			break
		}
	}
	if bindingID == "" {
		t.Fatalf("analyst role binding missing: %#v", bindings)
	}
	updateSignals := `{"workspaceAccess":{"command":{"bindingId":"` + bindingID + `","role":"editor","subjectType":"principal","subjectId":"` + analyst.ID + `"}}}`
	updateReq := httptest.NewRequest(http.MethodPost, "/workspaces/test/access/upsert", bytes.NewBufferString(updateSignals))
	updateReq.Header.Set("Authorization", "Bearer "+token)
	updateRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK || !strings.Contains(updateRec.Body.String(), "Access updated.") {
		t.Fatalf("update status = %d body=%s", updateRec.Code, updateRec.Body.String())
	}
	removeSignals := `{"workspaceAccess":{"command":{"bindingId":"` + bindingID + `"}}}`
	removeReq := httptest.NewRequest(http.MethodPost, "/workspaces/test/access/remove", bytes.NewBufferString(removeSignals))
	removeReq.Header.Set("Authorization", "Bearer "+token)
	removeRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("remove status = %d body=%s", removeRec.Code, removeRec.Body.String())
	}
	if !strings.Contains(removeRec.Body.String(), "Access removed.") {
		t.Fatalf("workspace access remove did not patch success:\n%s", removeRec.Body.String())
	}

	removedListReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings", nil)
	removedListReq.Header.Set("Authorization", "Bearer "+token)
	removedListReq.Header.Set("Accept", "application/json")
	removedListRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(removedListRec, removedListReq)
	if strings.Contains(removedListRec.Body.String(), `"email":"analyst@example.com"`) {
		t.Fatalf("role binding remained after remove command:\n%s", removedListRec.Body.String())
	}
	for action, operationID := range map[string]string{
		"role_binding.created": "createRoleBinding",
		"role_binding.updated": "updateRoleBinding",
		"role_binding.deleted": "deleteRoleBinding",
	} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: action})
		if err != nil || len(events) != 1 {
			t.Fatalf("%s audit events = %#v, %v", action, events, err)
		}
		assertOperationAuditMetadata(t, events[0], operationID, "ui")
	}
}

func assertOperationAuditMetadata(t *testing.T, event access.AuditEvent, operationID, surface string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int            `json:"schemaVersion"`
		Retention     string         `json:"retention"`
		PayloadSchema string         `json:"payloadSchema"`
		Payload       map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(event.MetadataJSON), &envelope); err != nil {
		t.Fatalf("decode %s audit metadata: %v", event.Action, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema == "" ||
		envelope.Payload["operationId"] != operationID || envelope.Payload["surface"] != surface {
		t.Fatalf("%s audit metadata = %#v", event.Action, envelope)
	}
}

func TestWorkspaceAccessSearchReturnsPrincipalsAndGroups(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	financePrincipal := testPrincipal(t, ctx, store, "finance@example.com", "Finance Analyst", "")
	repo := testAccessRepository(store)
	if _, err := repo.UpsertGroup(ctx, access.GroupInput{ID: "group_finance", WorkspaceID: "test", Name: "Finance Team"}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_finance", DisplayName: "Finance Bot"}); err != nil {
		t.Fatalf("seed service principal: %v", err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	signals := `{"workspaceAccess":{"search":"finance"}}`
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test/access/search", nil)
	query := req.URL.Query()
	query.Set("datastar", signals)
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("search status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"event: datastar-patch-signals",
		`"search":"finance"`,
		`"subjectType":"principal"`,
		`"subjectId":"` + financePrincipal.ID + `"`,
		`"label":"Finance Analyst"`,
		`"subjectType":"group"`,
		`"subjectId":"group_finance"`,
		`"label":"Finance Team"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("workspace access search did not patch %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Finance Bot") || strings.Contains(body, `"subjectType":"service_principal"`) {
		t.Fatalf("workspace access search included a service principal:\n%s", body)
	}
}

func TestWorkspaceAssetAccessCommandCreatesAndRemovesGrant(t *testing.T) {
	store := testStore(t)
	seedActiveDeployment(t, store, "test")
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	repo := testAccessRepository(store)
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{ID: "group_scim_sales", ExternalID: "sales", Name: "Sales Analysts"})
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	servicePrincipal, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_ci", DisplayName: "CI Publisher"})
	if err != nil {
		t.Fatalf("seed service principal: %v", err)
	}

	signals := `{"workspaceAccess":{"command":{"email":"analyst@example.com","role":"VIEW_ITEM"}}}`
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/upsert", bytes.NewBufferString(signals))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("asset access upsert status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"event: datastar-patch-signals", "workspaceAccess", `"mode":"object"`, "analyst@example.com", "Access updated."} {
		if !strings.Contains(body, want) {
			t.Fatalf("asset access upsert did not patch %q:\n%s", want, body)
		}
	}

	groupSignals := `{"workspaceAccess":{"command":{"subjectType":"group","subjectId":"` + group.ID + `","privilege":"QUERY_DATA"}}}`
	groupReq := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/upsert", bytes.NewBufferString(groupSignals))
	groupReq.Header.Set("Authorization", "Bearer "+token)
	groupRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(groupRec, groupReq)
	if groupRec.Code != http.StatusOK {
		t.Fatalf("group asset access upsert status = %d body=%s", groupRec.Code, groupRec.Body.String())
	}
	if !strings.Contains(groupRec.Body.String(), "Sales Analysts") {
		t.Fatalf("group access patch did not render group name:\n%s", groupRec.Body.String())
	}

	servicePrincipalSignals := `{"workspaceAccess":{"command":{"subjectType":"service_principal","subjectId":"` + servicePrincipal.ID + `","privilege":"DEPLOY"}}}`
	servicePrincipalReq := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/upsert", bytes.NewBufferString(servicePrincipalSignals))
	servicePrincipalReq.Header.Set("Authorization", "Bearer "+token)
	servicePrincipalRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(servicePrincipalRec, servicePrincipalReq)
	if servicePrincipalRec.Code != http.StatusOK {
		t.Fatalf("service principal asset access upsert status = %d body=%s", servicePrincipalRec.Code, servicePrincipalRec.Body.String())
	}
	if !strings.Contains(servicePrincipalRec.Body.String(), "CI Publisher") {
		t.Fatalf("service principal access patch did not render display name:\n%s", servicePrincipalRec.Body.String())
	}

	grants, err := repo.ListGrants(ctx, access.ItemObject(access.SecurableSemanticModel, "test", "test.sales"))
	if err != nil {
		t.Fatalf("list grants: %v", err)
	}
	var grantID string
	foundGroupGrant := false
	foundServicePrincipalGrant := false
	for _, grant := range grants {
		if grant.SubjectID == access.PrincipalIDForEmail("analyst@example.com") && grant.Privilege == access.PrivilegeViewItem {
			grantID = grant.ID
		}
		if grant.SubjectType == access.SubjectGroup && grant.SubjectID == group.ID && grant.Privilege == access.PrivilegeQueryData {
			foundGroupGrant = true
		}
		if grant.SubjectType == access.SubjectServicePrincipal && grant.SubjectID == servicePrincipal.ID && grant.Privilege == access.PrivilegeDeploy {
			foundServicePrincipalGrant = true
		}
	}
	if grantID == "" {
		t.Fatalf("asset grant missing after command: %#v", grants)
	}
	if !foundGroupGrant {
		t.Fatalf("group asset grant missing after command: %#v", grants)
	}
	if !foundServicePrincipalGrant {
		t.Fatalf("service principal asset grant missing after command: %#v", grants)
	}

	removeSignals := `{"workspaceAccess":{"command":{"bindingId":"` + grantID + `"}}}`
	removeReq := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/semantic_model:test.sales/access/remove", bytes.NewBufferString(removeSignals))
	removeReq.Header.Set("Authorization", "Bearer "+token)
	removeRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(removeRec, removeReq)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("asset access remove status = %d body=%s", removeRec.Code, removeRec.Body.String())
	}
	if !strings.Contains(removeRec.Body.String(), "Access removed.") {
		t.Fatalf("asset access remove did not patch success:\n%s", removeRec.Body.String())
	}
	grants, err = repo.ListGrants(ctx, access.ItemObject(access.SecurableSemanticModel, "test", "test.sales"))
	if err != nil {
		t.Fatalf("list grants after remove: %v", err)
	}
	for _, grant := range grants {
		if grant.ID == grantID {
			t.Fatalf("asset grant remained after remove: %#v", grants)
		}
	}
}

func TestWorkspaceAccessCommandRejectsViewer(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	viewer := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	token := testAPIToken(t, ctx, store, viewer.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	signals := `{"workspaceAccess":{"command":{"email":"analyst@example.com","role":"viewer"}}}`
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/access/upsert", bytes.NewBufferString(signals))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestWorkspaceAccessCommandPatchesInvalidInput(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPrincipal(t, ctx, store, "owner@example.com", "Owner", "owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))

	signals := `{"workspaceAccess":{"command":{"email":"","role":"viewer"}}}`
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/access/upsert", bytes.NewBufferString(signals))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("invalid status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "email is required") {
		t.Fatalf("invalid access command did not patch validation error:\n%s", body)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces/test/role-bindings", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if strings.Contains(listRec.Body.String(), "analyst@example.com") {
		t.Fatalf("invalid command changed bindings:\n%s", listRec.Body.String())
	}
}

func testStore(t *testing.T) *platform.Store {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	if err := workspaceRepo.Ensure(context.Background(), workspace.EnsureInput{ID: workspace.WorkspaceID("test"), Title: "Test"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	return store
}

func testAccessRepository(store *platform.Store) access.Repository {
	return accesssqlite.NewRepository(store.SQLDB())
}

func testAgentRepository(store *platform.Store) agent.Repository {
	return agentsqlite.NewRepositoryWithEvents(store.SQLDB(), jobsqlite.NewRepository(store.SQLDB()))
}

func testPrincipal(t *testing.T, ctx context.Context, store *platform.Store, email, displayName, role string) access.Principal {
	t.Helper()
	repo := testAccessRepository(store)
	if role != "" {
		principal, err := repo.SetPrincipalRole(ctx, access.PrincipalRoleInput{WorkspaceID: "test", Email: email, DisplayName: displayName, Role: role})
		if err != nil {
			t.Fatalf("bind role: %v", err)
		}
		return principal
	}
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Email: email, DisplayName: displayName})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	return principal
}

func testPlatformPrincipal(t *testing.T, ctx context.Context, store *platform.Store, email, displayName, role string) access.Principal {
	t.Helper()
	principal, err := testAccessRepository(store).SetPlatformRole(ctx, access.PlatformRoleInput{
		Email:       email,
		DisplayName: displayName,
		Role:        role,
	})
	if err != nil {
		t.Fatalf("bind platform role: %v", err)
	}
	return principal
}

func testAPIToken(t *testing.T, ctx context.Context, store *platform.Store, principalID, name string) string {
	t.Helper()
	token, err := testAccessRepository(store).CreateAPIToken(ctx, principalID, name)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	return token
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, wantCode int, messageContains string) {
	t.Helper()
	if got := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") && !strings.HasPrefix(got, "application/problem+json") {
		t.Fatalf("Content-Type = %q, want JSON body=%s", got, rec.Body.String())
	}
	var body struct {
		Code      any            `json:"code"`
		Status    int            `json:"status"`
		Message   string         `json:"message"`
		Detail    string         `json:"detail"`
		Details   map[string]any `json:"details"`
		RequestID string         `json:"requestId"`
		Error     string         `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode API error: %v body=%s", err, rec.Body.String())
	}
	if rec.Code != wantCode || body.Status != 0 && body.Status != wantCode {
		t.Fatalf("status = %d/%d, want %d body=%s", rec.Code, body.Status, wantCode, rec.Body.String())
	}
	message := body.Message
	if message == "" {
		message = body.Detail
	}
	if !strings.Contains(message, messageContains) {
		t.Fatalf("message = %q, want to contain %q", message, messageContains)
	}
	if strings.HasPrefix(rec.Result().Header.Get("Content-Type"), "application/json") && body.Details == nil {
		t.Fatalf("details = nil, want object body=%s", rec.Body.String())
	}
	if body.Error != "" {
		t.Fatalf("legacy error field present: %q body=%s", body.Error, rec.Body.String())
	}
}

func seedActiveDeployment(t *testing.T, store *platform.Store, workspaceID string) {
	t.Helper()
	ctx := context.Background()
	servingStateRepo := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := servingStateRepo.Create(ctx, servingstate.CreateInput{WorkspaceID: servingstate.WorkspaceID(workspaceID), CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	compiled, err := workspacecompiler.CompileProject(filepath.Join(projectRoot(t), "dashboards", "leapview.yaml"), workspacecompiler.Options{})
	if err != nil {
		t.Fatalf("compile project: %v", err)
	}
	selected, ok := compiled.Workspace("sales")
	if !ok {
		t.Fatal("compile project: missing sales workspace")
	}
	workspaceDef := selected.Manifest()
	if workspaceDef == nil {
		t.Fatal("compile project: missing sales workspace definition")
	}
	workspaceDef.SourceFiles = remapTestSourceFiles(workspaceDef.SourceFiles, "sales", workspaceID)
	graph, err := workspacecompiler.ExtractLineage(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(created.ID), workspaceDef)
	if err != nil {
		t.Fatalf("extract assets: %v", err)
	}
	validation := completeTestValidation(workspaceID, servingstate.Validation{
		Digest:       "digest-" + string(created.ID),
		ManifestJSON: "{}",
		ProjectID:    compiled.ID(),
		Graph:        testSnapshotGraph(t, graph),
	})
	if _, err := servingStateRepo.SaveValidated(ctx, created.ID, validation, zeroArtifact(created.ID, workspaceID)); err != nil {
		t.Fatalf("validate deployment: %v", err)
	}
	if _, err := servingStateRepo.Activate(ctx, servingstate.WorkspaceID(workspaceID), servingstate.DefaultEnvironment, created.ID); err != nil {
		t.Fatalf("activate deployment: %v", err)
	}
}

func seedActiveDeploymentFromWorkspaceAssets(t *testing.T, store *platform.Store, workspaceID string, provider workspaceAssetGraphProvider) servingstate.ID {
	t.Helper()
	ctx := context.Background()
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: workspaceID}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	servingStateRepo := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := servingStateRepo.Create(ctx, servingstate.CreateInput{WorkspaceID: servingstate.WorkspaceID(workspaceID), CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	assets, edges, ok := provider.WorkspaceAssets(workspaceID, string(created.ID))
	if !ok {
		t.Fatal("workspace asset graph unavailable")
	}
	validation := completeTestValidation(workspaceID, servingstate.Validation{
		Digest:       "digest-" + string(created.ID),
		ManifestJSON: "{}",
		ProjectID:    "project",
		Graph:        testSnapshotGraph(t, workspace.AssetGraph{Assets: assets, Edges: edges}),
	})
	if _, err := servingStateRepo.SaveValidated(ctx, created.ID, validation, zeroArtifact(created.ID, workspaceID)); err != nil {
		t.Fatalf("validate deployment: %v", err)
	}
	if _, err := servingStateRepo.Activate(ctx, servingstate.WorkspaceID(workspaceID), servingstate.DefaultEnvironment, created.ID); err != nil {
		t.Fatalf("activate deployment: %v", err)
	}
	return created.ID
}

func seedEnvironmentAssetDeployment(t *testing.T, store *platform.Store, workspaceID string, environment servingstate.Environment, dashboardTitle, connectionTitle string) {
	t.Helper()
	ctx := context.Background()
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: workspaceID}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	servingStateRepo := servingstatesqlite.NewRepository(store.SQLDB())
	created, err := servingStateRepo.Create(ctx, servingstate.CreateInput{WorkspaceID: servingstate.WorkspaceID(workspaceID), Environment: environment, CreatedBy: "tester"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	workspaceIDValue := workspace.WorkspaceID(workspaceID)
	servingStateID := workspace.ServingStateID(created.ID)
	catalog := mustWorkspaceAsset(t, workspaceIDValue, servingStateID, workspace.AssetTypeCatalog, workspaceID, "", workspaceID, map[string]any{"key": workspaceID})
	connection := mustWorkspaceAsset(t, workspaceIDValue, servingStateID, workspace.AssetTypeConnection, string(environment)+"_conn", catalog.ID, connectionTitle, map[string]any{"key": string(environment) + "_conn"})
	source := mustWorkspaceAsset(t, workspaceIDValue, servingStateID, workspace.AssetTypeSource, string(environment)+".orders", catalog.ID, string(environment)+" source", map[string]any{"key": string(environment) + ".orders"})
	dashboard := mustWorkspaceAsset(t, workspaceIDValue, servingStateID, workspace.AssetTypeDashboard, string(environment)+"-dashboard", catalog.ID, dashboardTitle, map[string]any{"key": string(environment) + "-dashboard"})
	graph := workspace.AssetGraph{
		Assets: []workspace.Asset{catalog, connection, source, dashboard},
		Edges: []workspace.AssetEdge{
			workspace.NewAssetEdge(workspaceIDValue, servingStateID, catalog.ID, connection.ID, workspace.AssetEdgeContains),
			workspace.NewAssetEdge(workspaceIDValue, servingStateID, catalog.ID, source.ID, workspace.AssetEdgeContains),
			workspace.NewAssetEdge(workspaceIDValue, servingStateID, catalog.ID, dashboard.ID, workspace.AssetEdgeContains),
			workspace.NewAssetEdge(workspaceIDValue, servingStateID, source.ID, connection.ID, workspace.AssetEdgeUsesConnection),
		},
	}
	artifact := zeroArtifact(created.ID, workspaceID)
	artifact.Environment = environment
	if _, err := servingStateRepo.SaveValidated(ctx, created.ID, completeTestValidation(workspaceID, servingstate.Validation{Digest: "digest-" + string(environment), ManifestJSON: "{}", ProjectID: "project", Graph: testSnapshotGraph(t, graph)}), artifact); err != nil {
		t.Fatalf("save validated: %v", err)
	}
	if _, err := servingStateRepo.Activate(ctx, servingstate.WorkspaceID(workspaceID), environment, created.ID); err != nil {
		t.Fatalf("activate: %v", err)
	}
}

func remapTestSourceFiles(sourceFiles map[string]string, fromWorkspace, toWorkspace string) map[string]string {
	out := map[string]string{}
	for id, path := range sourceFiles {
		next := strings.Replace(id, ":"+fromWorkspace+".", ":"+toWorkspace+".", 1)
		next = strings.Replace(next, ":"+fromWorkspace, ":"+toWorkspace, 1)
		out[next] = path
	}
	return out
}

func activeAssetID(t *testing.T, store *platform.Store, workspaceID, typ, key string) string {
	t.Helper()
	repo := workspacesqlite.NewRepository(store.SQLDB())
	graph, ok, err := repo.ActiveServingStateGraph(context.Background(), workspace.WorkspaceID(workspaceID), string(servingstate.DefaultEnvironment))
	if err != nil {
		t.Fatalf("active serving state graph: %v", err)
	}
	if !ok {
		t.Fatalf("workspace %q has no active serving state graph", workspaceID)
	}
	for _, asset := range graph.Assets {
		if string(asset.Type) == typ && asset.Key == key {
			return string(asset.ID)
		}
	}
	t.Fatalf("asset %s %q not found in active graph", typ, key)
	return ""
}

func activeAssetIDByType(t *testing.T, store *platform.Store, workspaceID, typ string) string {
	t.Helper()
	repo := workspacesqlite.NewRepository(store.SQLDB())
	graph, ok, err := repo.ActiveServingStateGraph(context.Background(), workspace.WorkspaceID(workspaceID), string(servingstate.DefaultEnvironment))
	if err != nil {
		t.Fatalf("active serving state graph: %v", err)
	}
	if !ok {
		t.Fatalf("workspace %q has no active serving state graph", workspaceID)
	}
	for _, asset := range graph.Assets {
		if string(asset.Type) == typ {
			return string(asset.ID)
		}
	}
	t.Fatalf("asset type %s not found in active graph", typ)
	return ""
}

func zeroArtifact(servingStateID servingstate.ID, workspaceID string) servingstate.Artifact {
	return servingstate.Artifact{
		ID:             "artifact_" + string(servingStateID),
		ServingStateID: servingStateID,
		WorkspaceID:    servingstate.WorkspaceID(workspaceID),
		Digest:         "digest",
		Format:         "tar.gz",
		Path:           "artifact.tar.gz",
		ManifestJSON:   "{}",
	}
}

func completeTestValidation(workspaceID string, validation servingstate.Validation) servingstate.Validation {
	validation.ProjectDigest = "sha256:" + strings.Repeat("a", 64)
	validation.ProjectWorkspaces = []string{workspaceID}
	return validation
}

func writeMinimalOlistFixture(t *testing.T, dir string) {
	t.Helper()

	writeCSVFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date
o1,c1,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00
o2,c2,shipped,2017-06-10 10:00:00,2017-06-10 11:00:00,2017-06-12 10:00:00,2017-06-20 10:00:00,2017-06-25 10:00:00
`)
	writeCSVFixture(t, dir, "olist_order_items_dataset.csv", `order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value
o1,1,p1,s1,2018-01-12 10:00:00,100.00,10.00
o2,1,p2,s2,2017-06-15 10:00:00,50.00,5.00
`)
	writeCSVFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,110.00
o2,1,boleto,1,55.00
`)
	writeCSVFixture(t, dir, "olist_products_dataset.csv", `product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm
p1,beleza_saude,10,20,1,500,20,10,15
p2,relogios_presentes,12,22,1,700,25,12,16
`)
	writeCSVFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state
c1,u1,01000,sao paulo,SP
c2,u2,20000,rio de janeiro,RJ
`)
	writeCSVFixture(t, dir, "olist_order_reviews_dataset.csv", `review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp
r1,o1,5,great,fast,2018-01-15,2018-01-16
r2,o2,3,ok,slow,2017-06-21,2017-06-22
`)
	writeCSVFixture(t, dir, "product_category_name_translation.csv", `product_category_name,product_category_name_english
beleza_saude,health_beauty
relogios_presentes,watches_gifts
`)
}

func writeCSVFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}
