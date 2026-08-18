package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type fakeHeadlessAuthoring struct {
	draftErr     error
	executeErr   error
	command      authoring.Command
	createErr    error
	create       authoringservice.CreateRequest
	result       authoringservice.Result
	audits       []access.AuditEventInput
	fork         sourceadapter.ForkRequest
	revision     application.RevisionRequest
	previewErr   error
	exports      []sourceadapter.ExportRequest
	draftExports []sourceadapter.ExportRequest
}

func (f *fakeHeadlessAuthoring) Create(_ context.Context, request authoringservice.CreateRequest) (authoringservice.Result, error) {
	f.create = request
	return f.result, f.createErr
}
func (f *fakeHeadlessAuthoring) Execute(_ context.Context, _ projectgraph.ResourceID, command authoring.Command) (authoringservice.Result, error) {
	f.command = command
	return f.result, f.executeErr
}
func (f *fakeHeadlessAuthoring) ExecuteIntent(_ context.Context, request application.IntentRequest) (authoringservice.Result, error) {
	f.command = request.Command
	return f.result, f.executeErr
}
func (f *fakeHeadlessAuthoring) List(context.Context, catalog.ListRequest) (catalog.ListResult, error) {
	return catalog.ListResult{}, nil
}
func (f *fakeHeadlessAuthoring) Get(context.Context, catalog.GetRequest) (catalog.Dashboard, error) {
	return catalog.Dashboard{}, nil
}
func (f *fakeHeadlessAuthoring) Draft(context.Context, application.DraftRequest) (application.DraftRead, error) {
	return application.DraftRead{Lifecycle: authoring.DashboardLifecycle{ProjectID: "sales", ID: "dash"}, Revision: authoring.Revision{DashboardID: "dash", ID: "rev", Number: 1, ContentHash: strings.Repeat("a", 64)}}, f.draftErr
}
func (f *fakeHeadlessAuthoring) Revision(_ context.Context, request application.RevisionRequest) (authoring.Revision, error) {
	f.revision = request
	return authoring.Revision{}, nil
}
func (f *fakeHeadlessAuthoring) Fork(_ context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	f.fork = request
	return f.result, nil
}
func (f *fakeHeadlessAuthoring) Preview(context.Context, preview.PreviewRequest) (preview.Preview, error) {
	return preview.Preview{}, f.previewErr
}
func (f *fakeHeadlessAuthoring) ExportYAML(_ context.Context, request sourceadapter.ExportRequest) ([]byte, error) {
	f.exports = append(f.exports, request)
	return []byte("dashboard: {}\n"), nil
}
func (f *fakeHeadlessAuthoring) ExportDraftYAML(_ context.Context, request sourceadapter.ExportRequest) ([]byte, error) {
	f.draftExports = append(f.draftExports, request)
	return []byte("dashboard: draft\n"), nil
}

type testAuthoringAPIGenDispatcher struct {
	dashboardgen.GenOperationDispatcher
	api AuthoringAPI
}

func (d testAuthoringAPIGenDispatcher) ListDashboardAuthoringCatalog(w http.ResponseWriter, r *http.Request, workspace string) {
	d.api.ListCatalog(w, r)
}
func (d testAuthoringAPIGenDispatcher) ExecuteDashboardAuthoringCommand(w http.ResponseWriter, r *http.Request, workspace string, _ dashboardgen.GenExecuteDashboardAuthoringCommandHeaders) {
	d.api.ExecuteCommand(w, r)
}
func (d testAuthoringAPIGenDispatcher) GetDashboardAuthoringDashboard(w http.ResponseWriter, r *http.Request, workspace, dashboard string) {
	d.api.GetDashboard(w, r)
}
func (d testAuthoringAPIGenDispatcher) GetDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, workspace, dashboard string) {
	d.api.GetDraft(w, r)
}
func (d testAuthoringAPIGenDispatcher) PreviewDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, workspace, dashboard, draft string) {
	d.api.Preview(w, r)
}
func (d testAuthoringAPIGenDispatcher) GetDashboardAuthoringDraftRevision(w http.ResponseWriter, r *http.Request, workspace, dashboard, draft, revision string) {
	d.api.GetRevision(w, r)
}
func (d testAuthoringAPIGenDispatcher) GetDashboardAuthoringPublishedRevision(w http.ResponseWriter, r *http.Request, workspace, dashboard, revision string) {
	d.api.GetRevision(w, r)
}
func (d testAuthoringAPIGenDispatcher) CreateDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, workspace string, _ dashboardgen.GenCreateDashboardAuthoringDraftHeaders) {
	d.api.CreateDraft(w, r)
}
func (d testAuthoringAPIGenDispatcher) ForkDashboardAuthoringDraft(w http.ResponseWriter, r *http.Request, workspace string, _ dashboardgen.GenForkDashboardAuthoringDraftHeaders) {
	d.api.Fork(w, r)
}
func (d testAuthoringAPIGenDispatcher) ExportDashboardAuthoringSource(w http.ResponseWriter, r *http.Request, workspace, kind, dashboard string) {
	d.api.Export(w, r)
}

type testAuthoringAPIGenServer struct{ dispatcher testAuthoringAPIGenDispatcher }

func (s testAuthoringAPIGenServer) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request) {
	dashboardgen.DispatchAPIGenOperation(operationID, s.dispatcher, APIGenTransportErrorResponder{}, w, r)
}

func testAuthoringRouter(app HeadlessAuthoringApplication) *chi.Mux {
	api := AuthoringAPI{Application: app, ActorID: func(*http.Request) string { return "principal_1" }}
	return testAuthoringRouterWithAPI(api)
}

func testAuthoringRouterWithAPI(api AuthoringAPI) *chi.Mux {
	router := chi.NewRouter()
	if fake, ok := api.Application.(*fakeHeadlessAuthoring); ok {
		api.RecordAudit = func(_ context.Context, event access.AuditEventInput) error {
			fake.audits = append(fake.audits, event)
			return nil
		}
	}
	// Keep old fixture paths readable while exercising the generated /api/v1
	// route table; production has no alternate manual authoring mount.
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
				r.URL.Path = "/api/v1" + r.URL.Path
			}
			next.ServeHTTP(w, r)
		})
	})
	dashboardgen.RegisterAPIGenRoutes(router, testAuthoringAPIGenServer{dispatcher: testAuthoringAPIGenDispatcher{api: api}})
	return router
}

func TestAuthoringAPIRequiresIdempotencyKeyForCommands(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/commands", strings.NewReader(`{"id":"cmd-1"}`))
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("body = %s, want generated invalid-request problem", rec.Body.String())
	}
}

func TestAuthoringGeneratedCommandContractRejectsZeroAndMultiplePayloads(t *testing.T) {
	base := `"kind":"metadata","dashboardId":"dash","draftId":"draft","expectedRevision":{"revisionId":"rev","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	var zero dashboardgen.GenSchemaDashboardAuthoringCommandRequest
	if err := json.Unmarshal([]byte("{"+base+"}"), &zero); err == nil {
		t.Fatal("command without payload decoded successfully")
	}
	var multiple dashboardgen.GenSchemaDashboardAuthoringCommandRequest
	if err := json.Unmarshal([]byte("{"+base+`,"metadata":{},"setVisibility":{"visibility":"private"}}`+"}"), &multiple); err == nil {
		t.Fatal("command with multiple payloads decoded successfully")
	}
}

func TestAuthoringGeneratedCommandContractRejectsLegacyVisibility(t *testing.T) {
	var input dashboardgen.GenSchemaDashboardAuthoringCommandRequest
	body := `{"kind":"setVisibility","dashboardId":"dash","draftId":"draft","expectedRevision":{"revisionId":"rev","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"setVisibility":{"visibility":"organization-visible"}}`
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatalf("generated command decode unexpectedly failed: %v", err)
	}
	if _, _, err := commandFromAPIGen(input, "cmd", "actor"); err == nil {
		t.Fatal("legacy organization-visible visibility was accepted")
	}
}

func TestAuthoringGeneratedForkEvidenceRoundTripAndBranchExclusivity(t *testing.T) {
	valid := `{"kind":"project","project":{"sourceProjectId":"source","sourceDashboardId":"dash","identity":{"projectId":"source","environment":"prod","generationId":"gen"}}}`
	var evidence dashboardgen.GenSchemaDashboardAuthoringForkEvidence
	if err := json.Unmarshal([]byte(valid), &evidence); err != nil {
		t.Fatalf("decode project fork evidence: %v", err)
	}
	encoded, err := json.Marshal(evidence)
	var gotMap, wantMap map[string]any
	if err == nil {
		err = json.Unmarshal(encoded, &gotMap)
	}
	if err == nil {
		err = json.Unmarshal([]byte(valid), &wantMap)
	}
	if err != nil || !reflect.DeepEqual(gotMap, wantMap) {
		t.Fatalf("fork evidence round trip = %s, want %s (err=%v)", encoded, valid, err)
	}
	var exclusive dashboardgen.GenSchemaDashboardAuthoringForkEvidence
	if err := json.Unmarshal([]byte(`{"kind":"project","project":{"sourceProjectId":"source","sourceDashboardId":"dash","identity":{"projectId":"source","environment":"prod","generationId":"gen"}},"instance":{}}`), &exclusive); err == nil {
		t.Fatal("fork evidence with both branches decoded successfully")
	}
}

func TestAuthoringGeneratedLifecycleAndCompilationRoundTrip(t *testing.T) {
	body := `{"projectId":"project","id":"dash","ownerPrincipalId":"owner","slug":"sales","title":"Sales","semanticModel":"sales-model","visibility":"organization","status":"published","draft":{"id":"draft","dashboardId":"dash","revision":{"revisionId":"rev","number":2,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"provenance":{"origin":"file","actorId":"owner"}},"published":{"revision":{"revisionId":"rev","number":2,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"compilation":{"authoredRevision":{"revisionId":"rev","number":2,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"definitionHash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","semanticModelId":"sales-model","semanticIdentity":{"projectId":"project","environment":"prod","generationId":"gen"}},"publishedAt":"2026-01-02T03:04:05Z","provenance":{"origin":"file","actorId":"owner"}}}`
	var lifecycle dashboardgen.GenSchemaDashboardAuthoringLifecycle
	if err := json.Unmarshal([]byte(body), &lifecycle); err != nil {
		t.Fatalf("decode lifecycle: %v", err)
	}
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	var gotMap, wantMap map[string]any
	if err := json.Unmarshal(encoded, &gotMap); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &wantMap); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotMap, wantMap) {
		t.Fatalf("lifecycle round trip changed persisted shape:\n got %s\nwant %s", encoded, body)
	}
}

func TestAuthoringGeneratedRevisionProjectionRejectsUint64Overflow(t *testing.T) {
	var token dashboardgen.GenSchemaDashboardAuthoringRevisionToken
	err := decodeGeneratedProjection(authoring.RevisionToken{RevisionID: "rev", Number: uint64(1) << 63, ContentHash: strings.Repeat("a", 64)}, &token)
	if err == nil {
		t.Fatal("uint64 revision number overflow decoded into int64 transport")
	}
}

func TestAuthoringPreviewFiltersRejectObjectAndArraySelectionValues(t *testing.T) {
	for _, invalid := range []any{map[string]any{"nested": true}, []any{"nested"}} {
		filters := &dashboardgen.DashboardAuthoringPreviewFilters{Selections: []dashboardgen.DashboardAuthoringPreviewSelection{{Entries: []dashboardgen.DashboardAuthoringPreviewSelectionEntry{{Mappings: []dashboardgen.DashboardAuthoringPreviewSelectionMapping{{Field: "country", Value: invalid}}}}}}}
		if _, err := filtersFromAPIGen(filters); err == nil {
			t.Fatalf("preview filter accepted non-scalar value %#v", invalid)
		}
	}
}

func TestAuthoringAPIMapsStaleRevisionToConflict(t *testing.T) {
	app := &fakeHeadlessAuthoring{draftErr: authoring.ErrStaleRevision}
	req := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/dashboards/dash/draft", nil)
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if !strings.Contains(rec.Body.String(), "STALE_REVISION") {
		t.Fatalf("body = %s, want stale revision problem", rec.Body.String())
	}
}

func TestAuthoringAPIDraftUsesAuthenticatedActor(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	api := AuthoringAPI{Application: app, ActorID: func(*http.Request) string { return "" }}
	router := testAuthoringRouterWithAPI(api)
	req := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/dashboards/dash/draft", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestAuthoringAPIMapsValidation(t *testing.T) {
	app := &fakeHeadlessAuthoring{executeErr: errors.New("invalid dashboard authoring contract")}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/commands", strings.NewReader(`{"id":"cmd-1"}`))
	req.Header.Set("Idempotency-Key", "cmd-1")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	// The fake error is intentionally not wrapped in ErrInvalidAuthoring; it
	// remains a server-side failure rather than being guessed as validation.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestAuthoringAPIMutationDoesNotSpoofToolCallProvenance(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/drafts", strings.NewReader(`{"title":"Sales","semanticModel":"sales"}`))
	req.Header.Set("Idempotency-Key", "idem-1")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if app.create.ToolCallID != "" {
		t.Fatalf("tool call id = %q, want empty for generic API", app.create.ToolCallID)
	}

}

func TestAuthoringAPICreateAuditBindsResultIdentityAndOrigin(t *testing.T) {
	app := &fakeHeadlessAuthoring{result: authoringservice.Result{Lifecycle: authoring.DashboardLifecycle{
		ID: "created-dashboard", Draft: &authoring.Draft{ID: "created-draft"},
	}}}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/drafts", strings.NewReader(`{"title":"Sales","semanticModel":"sales","origin":"file"}`))
	req.Header.Set("Idempotency-Key", "idem-audit")
	req.Header.Set("X-Correlation-ID", "corr-1")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want created (%s)", rec.Code, rec.Body.String())
	}
	if len(app.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(app.audits))
	}
	event := app.audits[0]
	if event.ResourceKind != "dashboard" || event.ResourceID != "created-dashboard" || event.Capability != access.CapabilityResourceEdit || event.CorrelationID != "corr-1" {
		t.Fatalf("audit identity = %#v", event)
	}
	if !strings.Contains(event.MetadataJSON, `"origin":"file"`) || !strings.Contains(event.MetadataJSON, `"draftId":"created-draft"`) {
		t.Fatalf("audit metadata = %s", event.MetadataJSON)
	}
}

func TestAuthoringAPICommandAuditUsesDomainPrivilegeAndIdentity(t *testing.T) {
	app := &fakeHeadlessAuthoring{result: authoringservice.Result{Lifecycle: authoring.DashboardLifecycle{ID: "dash-command"}}}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/commands", strings.NewReader(`{"kind":"publish","dashboardId":"dash-command","draftId":"draft-command","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"origin":"agent","publish":{}}`))
	req.Header.Set("Idempotency-Key", "cmd-audit")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want ok (%s)", rec.Code, rec.Body.String())
	}
	if len(app.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(app.audits))
	}
	event := app.audits[0]
	if event.ResourceKind != "dashboard" || event.ResourceID != "dash-command" || event.Capability != access.CapabilityResourcePublish {
		t.Fatalf("audit identity = %#v", event)
	}
	if !strings.Contains(event.MetadataJSON, `"origin":"agent"`) || !strings.Contains(event.MetadataJSON, `"draftId":"draft-command"`) {
		t.Fatalf("audit metadata = %s", event.MetadataJSON)
	}
}

func TestAuthoringAPIForkBindsInstanceSourceToRouteProject(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodPost, "/projects/target/authoring/forks", strings.NewReader(`{"source":{"kind":"instance","dashboardId":"dash"}}`))
	req.Header.Set("Idempotency-Key", "fork-cross-workspace")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if app.fork.Source.ProjectID != "target" || app.fork.Source.Kind != sourceadapter.SourceInstance {
		t.Fatalf("fork source was not bound to route project: %#v", app.fork.Source)
	}
}

func TestAuthoringAPIForkAuditBindsResultIdentityAndOrigin(t *testing.T) {
	app := &fakeHeadlessAuthoring{result: authoringservice.Result{Lifecycle: authoring.DashboardLifecycle{
		ID: "forked-dashboard", Draft: &authoring.Draft{ID: "forked-draft"},
	}}}
	req := httptest.NewRequest(http.MethodPost, "/projects/target/authoring/forks", strings.NewReader(`{"source":{"kind":"project","dashboardId":"source-dashboard"},"origin":"file"}`))
	req.Header.Set("Idempotency-Key", "fork-audit")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want created (%s)", rec.Code, rec.Body.String())
	}
	if len(app.audits) != 1 {
		t.Fatalf("audit count = %d, want 1", len(app.audits))
	}
	event := app.audits[0]
	if event.ResourceKind != "dashboard" || event.ResourceID != "forked-dashboard" || event.Capability != access.CapabilityResourceEdit {
		t.Fatalf("audit identity = %#v", event)
	}
	if !strings.Contains(event.MetadataJSON, `"origin":"file"`) || !strings.Contains(event.MetadataJSON, `"draftId":"forked-draft"`) {
		t.Fatalf("audit metadata = %s", event.MetadataJSON)
	}
}

func TestAuthoringAPIRevisionRouteBindsExactDraftIdentity(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/dashboards/dash/drafts/draft-1/revisions/rev-1", nil)
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if app.revision.DraftID != "draft-1" || app.revision.RevisionID != "rev-1" {
		t.Fatalf("revision request = %#v, want exact draft/revision identities", app.revision)
	}
}

func TestAuthoringAPIPublishedRevisionUsesViewAction(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/dashboards/dash/revisions/rev-1", nil)
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if app.revision.Action != authoring.AuthorizationActionView {
		t.Fatalf("revision action = %q, want view", app.revision.Action)
	}
}

func TestAuthoringAPIExportSetsSafeDownloadFilename(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/sources/instance/dash.bad/export", nil)
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="dashboard-dash.bad.yaml"` {
		t.Fatalf("content disposition = %q", got)
	}
	if len(app.draftExports) != 1 || app.draftExports[0].Source.Kind != sourceadapter.SourceInstance || app.draftExports[0].Source.DashboardID != "dash.bad" {
		t.Fatalf("workspace export requests = %#v", app.draftExports)
	}
}

func TestAuthoringAPIProjectExportUsesActiveSourceExport(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	request := httptest.NewRequest(http.MethodGet, "/projects/sales/authoring/sources/project/project-sales/export", nil)
	recording := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(recording, request)
	if recording.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", recording.Code, http.StatusOK, recording.Body.String())
	}
	if len(app.exports) != 1 || app.exports[0].Source.Kind != sourceadapter.SourceProject || app.exports[0].Source.DashboardID != "project-sales" {
		t.Fatalf("project export requests = %#v", app.exports)
	}
	if len(app.draftExports) != 0 {
		t.Fatalf("project export used draft path = %#v", app.draftExports)
	}
}

func TestAuthoringAPIRejectsCommandActorSpoof(t *testing.T) {
	app := &fakeHeadlessAuthoring{}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/commands", strings.NewReader(`{"kind":"setVisibility","dashboardId":"dash","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"setVisibility":{"visibility":"shared"}}`))
	req.Header.Set("Idempotency-Key", "cmd-1")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if app.command.ID != "cmd-1" || app.command.Provenance.ActorID != "principal_1" {
		t.Fatalf("command identity = %#v, want authenticated id/actor", app.command)
	}
}

func TestAuthoringAPIPreviewSemanticErrorIsUnprocessable(t *testing.T) {
	app := &fakeHeadlessAuthoring{previewErr: preview.ErrSemanticMismatch}
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/dashboards/dash/drafts/draft-1/preview", strings.NewReader(`{"pageId":"overview","revision":{"revisionId":"rev-1","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`))
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnprocessableEntity)
	}
}
