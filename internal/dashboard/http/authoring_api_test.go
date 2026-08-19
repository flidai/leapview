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
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
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
	return f.mutationResult(), f.createErr
}
func (f *fakeHeadlessAuthoring) Execute(_ context.Context, _ projectgraph.ResourceID, command authoring.Command) (authoringservice.Result, error) {
	f.command = command
	return f.mutationResult(), f.executeErr
}
func (f *fakeHeadlessAuthoring) ExecuteIntent(_ context.Context, request application.IntentRequest) (authoringservice.Result, error) {
	f.command = request.Command
	return f.mutationResult(), f.executeErr
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
	return canonicalHTTPRevision(request.DashboardID, request.RevisionID, 1), nil
}

func canonicalHTTPRevision(dashboardID authoring.DashboardID, revisionID authoring.RevisionID, number uint64) authoring.Revision {
	displayName := "Sales"
	document := dashboarddocument.DashboardDocument{
		APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1,
		Kind:       dashboarddocument.DashboardResourceKindDashboard,
		Metadata:   dashboarddocument.DashboardMetadata{ID: dashboardID.String(), Name: "sales", DisplayName: &displayName},
		Spec: dashboarddocument.DashboardSpec{
			SemanticModel: "sales-model", Filters: []dashboarddocument.DashboardFilter{},
			Visuals: map[string]dashboarddocument.DashboardVisual{},
			Pages:   []dashboarddocument.DashboardPage{{ID: "overview", Title: "Overview", Components: []dashboarddocument.DashboardPageComponent{}}},
		},
	}
	revision, err := authoring.NewRevision(revisionID, dashboardID, number, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), document, authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal_1"})
	if err != nil {
		panic(err)
	}
	return revision
}
func (f *fakeHeadlessAuthoring) Fork(_ context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	f.fork = request
	return f.mutationResult(), nil
}

// mutationResult supplies the complete canonical lifecycle/revision payload
// required by the transport projection. Individual tests may override the
// result identity (for audit assertions), while the fixture fills the
// remaining persisted pointers with schema-valid values.
func (f *fakeHeadlessAuthoring) mutationResult() authoringservice.Result {
	result := f.result
	if result.Lifecycle.Validate() == nil && result.Revision.ValidateComplete() == nil {
		return result
	}
	dashboardID := result.Lifecycle.ID
	if dashboardID == "" {
		dashboardID = "created-dashboard"
	}
	title := result.Lifecycle.Title
	if title == "" {
		title = "Sales"
	}
	slug := result.Lifecycle.Slug
	if slug == "" {
		slug = "sales"
	}
	semanticModel := result.Lifecycle.SemanticModel
	if semanticModel == "" {
		semanticModel = "sales-model"
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal_1"}
	revisionID := result.Revision.RevisionID
	if revisionID == "" {
		revisionID = "revision-1"
	}
	number := result.Revision.Number
	if number == 0 {
		number = 1
	}
	revision := canonicalHTTPRevision(dashboardID, revisionID, number)
	draftID := authoring.DraftID("draft-1")
	if result.Lifecycle.Draft != nil && result.Lifecycle.Draft.ID != "" {
		draftID = result.Lifecycle.Draft.ID
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "sales", ID: dashboardID, OwnerPrincipalID: "principal_1", Slug: slug,
		Title: title, SemanticModel: semanticModel, Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: draftID, DashboardID: dashboardID, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		panic(err)
	}
	result.Lifecycle = lifecycle
	result.Revision = revision.Token()
	return result
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

func TestAuthoringGeneratedCommandContractRejectsUnknownVisualType(t *testing.T) {
	body := `{"kind":"addVisual","dashboardId":"dash","draftId":"draft","expectedRevision":{"revisionId":"rev","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"addVisual":{"pageId":"overview","type":"unknown"}}`
	var input dashboardgen.GenSchemaDashboardAuthoringCommandRequest
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		return // Generated schema validation may reject the enum before the mapper.
	}
	if _, _, err := commandFromAPIGen(input, "cmd", "actor"); err == nil {
		t.Fatal("unknown dashboard visual type was accepted")
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

func TestAuthoringCatalogProjectionPreservesTypedNumbersAndRejectsOverflow(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project")
	if err != nil {
		t.Fatal(err)
	}
	semanticModel, err := projectgraph.NewResourceID("sales-model")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation")
	if err != nil {
		t.Fatal(err)
	}
	value := catalog.Dashboard{
		ID: projectID, StableID: "project:project", ProjectID: projectID,
		Title: "Sales", SemanticModel: semanticModel, Source: catalog.SourceProject,
		Origin: authoring.OriginFile, Status: authoring.LifecycleStatusPublished, Visibility: authoring.VisibilityOrganization,
		ServingIdentity: identity,
		Revision:        &catalog.RevisionEvidence{ID: "revision", Number: uint64(1)<<53 + 1, ContentHash: strings.Repeat("a", 64), CreatedAt: time.Unix(0, 0).UTC()},
	}
	response, err := catalogDashboardResponse(value)
	if err != nil {
		t.Fatalf("catalog projection: %v", err)
	}
	if response.Revision == nil || response.Revision.Number != int64(uint64(1)<<53+1) {
		t.Fatalf("revision number = %#v, want exact value", response.Revision)
	}
	if response.ServingIdentity == nil || response.ServingIdentity.GenerationId != "generation" {
		t.Fatalf("serving identity = %#v", response.ServingIdentity)
	}
	if _, err := catalogDashboardResponse(func() catalog.Dashboard {
		copy := value
		overflow := *value.Revision
		overflow.Number = uint64(1) << 63
		copy.Revision = &overflow
		return copy
	}()); err == nil {
		t.Fatal("catalog revision number overflow was accepted")
	}
	if _, err := catalogListResponse(catalog.ListResult{Count: int(^uint(0) >> 1)}); err == nil {
		t.Fatal("catalog count overflow was accepted")
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

func TestAuthoringPreviewFiltersRejectUnknownSelectionVocabulary(t *testing.T) {
	filters := &dashboardgen.DashboardAuthoringPreviewFilters{Selections: []dashboardgen.DashboardAuthoringPreviewSelection{{SourceKind: "chart", InteractionKind: "hover", Entries: []dashboardgen.DashboardAuthoringPreviewSelectionEntry{{Mappings: []dashboardgen.DashboardAuthoringPreviewSelectionMapping{{Field: "country", Value: "US"}}}}}}}
	if _, err := filtersFromAPIGen(filters); err == nil {
		t.Fatal("preview selection accepted unknown source and interaction kinds")
	}
	grain := dashboarddocument.DashboardTimeGrain("fortnight")
	filters = &dashboardgen.DashboardAuthoringPreviewFilters{Selections: []dashboardgen.DashboardAuthoringPreviewSelection{{SourceKind: "visual", InteractionKind: "point_selection", Entries: []dashboardgen.DashboardAuthoringPreviewSelectionEntry{{Mappings: []dashboardgen.DashboardAuthoringPreviewSelectionMapping{{Field: "country", Grain: &grain, Value: "US"}}}}}}}
	if _, err := filtersFromAPIGen(filters); err == nil {
		t.Fatal("preview selection accepted unknown grain")
	}
}

func TestAuthoringPreviewFiltersAcceptCompiledInteractionID(t *testing.T) {
	filters := &dashboardgen.DashboardAuthoringPreviewFilters{Selections: []dashboardgen.DashboardAuthoringPreviewSelection{{SourceKind: "visual", SourceId: "categories", InteractionKind: "interaction-0", Entries: []dashboardgen.DashboardAuthoringPreviewSelectionEntry{{Mappings: []dashboardgen.DashboardAuthoringPreviewSelectionMapping{{Field: "category", Value: "books"}}}}}}}
	projected, err := filtersFromAPIGen(filters)
	if err != nil {
		t.Fatal(err)
	}
	if len(projected.Selections) != 1 || projected.Selections[0].InteractionKind != "interaction-0" {
		t.Fatalf("projected selections = %#v", projected.Selections)
	}
}

func TestAuthoringPreviewProjectionRoundTripPreservesFullRuntimeShape(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("project")
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "generation")
	if err != nil {
		t.Fatal(err)
	}
	progress := 42.5
	result := preview.Preview{
		Revision: authoring.RevisionToken{RevisionID: "revision", Number: 7, ContentHash: strings.Repeat("a", 64)},
		Definition: dashboarddefinition.Definition{
			ID: "dashboard", Title: "Sales", Description: "Revenue", SemanticModel: "sales-model",
			FilterApplication: dashboardfilter.ApplicationPolicy{}.WithDefaults(),
			Pages:             []dashboard.Page{},
			Visualizations:    map[string]visualizationdefinition.Definition{},
		},
		PagePatch: dashboard.Patch{
			Filters: dashboard.Filters{Selections: []dashboard.InteractionSelection{}, SpatialSelections: []dashboard.SpatialInteractionSelection{}, InteractionRevision: 3},
			Status:  dashboard.Status{Loading: true, Error: "", RefreshID: "refresh", Generation: 4, LastUpdated: "2026-08-18T00:00:00Z", SetupRequired: false, ProgressPercent: &progress},
			Visuals: map[string]visualizationir.VisualizationEnvelope{},
		},
		SemanticEvidence: preview.SemanticServingStateEvidence{SemanticModel: "sales-model", RuntimeModel: "runtime-sales", Identity: identity, DuckLakeSnapshotID: 99},
	}
	var projected dashboardgen.DashboardAuthoringPreviewResponse
	if err := decodeGeneratedProjection(result, &projected); err != nil {
		t.Fatalf("full preview projection rejected runtime shape: %v", err)
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	encodedProjection, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(encodedResult, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedProjection, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("full preview projection changed shape:\n got %s\nwant %s", encodedProjection, encodedResult)
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
	req := httptest.NewRequest(http.MethodPost, "/projects/sales/authoring/commands", strings.NewReader(`{"kind":"setVisibility","dashboardId":"dash","draftId":"draft-1","expectedRevision":{"revisionId":"rev-1","number":1,"contentHash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"setVisibility":{"visibility":"organization"}}`))
	req.Header.Set("Idempotency-Key", "cmd-1")
	rec := httptest.NewRecorder()
	testAuthoringRouter(app).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (%s)", rec.Code, http.StatusOK, rec.Body.String())
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
