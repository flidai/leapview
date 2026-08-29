package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	httpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type builderAuthoringFake struct {
	builder          uisignals.DashboardBuilderSignal
	builderReq       builderview.Request
	executed         authoring.Command
	preview          preview.Preview
	previewCtx       context.Context
	previewReq       preview.PreviewRequest
	previewCalls     int
	yaml             []byte
	err              error
	previewErr       error
	exportErr        error
	exportDraftCalls int
	exportDraftReq   sourceadapter.ExportRequest
	executeCalls     int
	intentCalls      int
	createResult     authoringservice.Result
	forkResult       authoringservice.Result
	createRequest    authoringservice.CreateRequest
	forkRequest      sourceadapter.ForkRequest
}

func (f *builderAuthoringFake) Builder(_ context.Context, request builderview.Request) (uisignals.DashboardBuilderSignal, error) {
	f.builderReq = request
	return f.builder, f.err
}
func (f *builderAuthoringFake) Execute(_ context.Context, _ projectgraph.ResourceID, command authoring.Command) (authoringservice.Result, error) {
	f.executeCalls++
	f.executed = command
	return authoringservice.Result{Revision: command.ExpectedRevision}, f.err
}
func (f *builderAuthoringFake) ExecuteIntent(_ context.Context, request application.IntentRequest) (authoringservice.Result, error) {
	f.intentCalls++
	f.executed = request.Command
	return authoringservice.Result{Revision: request.Command.ExpectedRevision}, f.err
}
func (f *builderAuthoringFake) Create(_ context.Context, request authoringservice.CreateRequest) (authoringservice.Result, error) {
	f.createRequest = request
	return f.createResult, f.err
}
func (f *builderAuthoringFake) Fork(_ context.Context, request sourceadapter.ForkRequest) (authoringservice.Result, error) {
	f.forkRequest = request
	return f.forkResult, f.err
}

func browserDraftResult(t *testing.T, dashboardID authoring.DashboardID) authoringservice.Result {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "principal-1"}
	revision := authoring.RevisionToken{RevisionID: "revision-created", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "sales", ID: dashboardID, OwnerPrincipalID: "principal-1", Slug: "sales", Title: "Sales", SemanticModel: "sales-model", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-created", DashboardID: dashboardID, Revision: revision, Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	return authoringservice.Result{Lifecycle: lifecycle, Revision: revision}
}

func TestDashboardDraftCreateAndForkBrowserActionsUseAuthoringApplication(t *testing.T) {
	fake := &builderAuthoringFake{createResult: browserDraftResult(t, "dashboard-created"), forkResult: browserDraftResult(t, "dashboard-forked")}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }, CSRFToken: func(*nethttp.Request) string { return "csrf" }}
	getCreate := httptest.NewRecorder()
	handler.DashboardDraftCreate(getCreate, httptest.NewRequest(nethttp.MethodGet, "/dashboards/new", nil))
	if getCreate.Code != nethttp.StatusOK || !strings.Contains(getCreate.Body.String(), `action="/dashboards/new"`) || !strings.Contains(getCreate.Body.String(), "Governed semantic model") || !strings.Contains(getCreate.Body.String(), `name="gorilla.csrf.Token" value="csrf"`) || !strings.Contains(getCreate.Body.String(), `name="idempotencyKey" value="req_`) {
		t.Fatalf("create page = %d %s", getCreate.Code, getCreate.Body.String())
	}
	getFork := httptest.NewRecorder()
	getForkRequest := withBuilderURLParams(httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/fork", nil), "sales", "revenue")
	handler.DashboardDraftFork(getFork, getForkRequest)
	if getFork.Code != nethttp.StatusOK || !strings.Contains(getFork.Body.String(), `action="/dashboards/revenue/fork"`) || !strings.Contains(getFork.Body.String(), `name="idempotencyKey" value="req_`) {
		t.Fatalf("fork page = %d %s", getFork.Code, getFork.Body.String())
	}
	create := httptest.NewRequest(nethttp.MethodPost, "/dashboards/new", strings.NewReader("title=Sales&semanticModel=sales-model&slug=sales&idempotencyKey=create-form-1"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	handler.DashboardDraftCreate(createRec, create)
	if createRec.Code != nethttp.StatusSeeOther || createRec.Header().Get("Location") != "/dashboards/dashboard-created/edit?draft=draft-created" {
		t.Fatalf("create redirect = %d %q", createRec.Code, createRec.Header().Get("Location"))
	}
	if fake.createRequest.SemanticModel != "sales-model" || fake.createRequest.IdempotencyKey != "create-form-1" || fake.createRequest.Origin != authoring.OriginUI {
		t.Fatalf("create request = %#v", fake.createRequest)
	}
	fork := httptest.NewRequest(nethttp.MethodPost, "/dashboards/revenue/fork", strings.NewReader("title=Sales%20copy&slug=sales-copy&idempotencyKey=fork-form-1"))
	fork.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fork = withBuilderURLParams(fork, "sales", "revenue")
	forkRec := httptest.NewRecorder()
	handler.DashboardDraftFork(forkRec, fork)
	if forkRec.Code != nethttp.StatusSeeOther || forkRec.Header().Get("Location") != "/dashboards/dashboard-forked/edit?draft=draft-created" {
		t.Fatalf("fork redirect = %d %q", forkRec.Code, forkRec.Header().Get("Location"))
	}
	if fake.forkRequest.Source.Kind != sourceadapter.SourceProject || fake.forkRequest.Source.DashboardID != "revenue" || fake.forkRequest.IdempotencyKey != "fork-form-1" {
		t.Fatalf("fork request = %#v", fake.forkRequest)
	}
}

func TestDashboardDraftCreateAndForkRequireFormIdempotencyKey(t *testing.T) {
	fake := &builderAuthoringFake{createResult: browserDraftResult(t, "dashboard-created"), forkResult: browserDraftResult(t, "dashboard-forked")}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	create := httptest.NewRequest(nethttp.MethodPost, "/dashboards/new", strings.NewReader("title=Sales&semanticModel=sales-model"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	handler.DashboardDraftCreate(createRec, create)
	if createRec.Code != nethttp.StatusBadRequest || fake.createRequest.IdempotencyKey != "" {
		t.Fatalf("missing create idempotency key = %d request=%#v body=%s", createRec.Code, fake.createRequest, createRec.Body.String())
	}
	fork := httptest.NewRequest(nethttp.MethodPost, "/dashboards/revenue/fork", strings.NewReader("title=Sales%20copy"))
	fork.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fork = withBuilderURLParams(fork, "sales", "revenue")
	forkRec := httptest.NewRecorder()
	handler.DashboardDraftFork(forkRec, fork)
	if forkRec.Code != nethttp.StatusBadRequest || fake.forkRequest.IdempotencyKey != "" {
		t.Fatalf("missing fork idempotency key = %d request=%#v body=%s", forkRec.Code, fake.forkRequest, forkRec.Body.String())
	}
}

func TestDashboardDraftCreateAndForkAuthorizationFailuresRenderBrowserRecovery(t *testing.T) {
	fake := &builderAuthoringFake{err: access.ErrForbidden}
	handler := Handler{Authoring: fake, ProjectID: "sales", CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	for name, request := range map[string]*nethttp.Request{
		"create": httptest.NewRequest(nethttp.MethodPost, "/dashboards/new", strings.NewReader("title=Sales&semanticModel=sales-model&idempotencyKey=create-denied")),
		"fork":   withBuilderURLParams(httptest.NewRequest(nethttp.MethodPost, "/dashboards/revenue/fork", strings.NewReader("title=Sales%20copy&idempotencyKey=fork-denied")), "sales", "revenue"),
	} {
		t.Run(name, func(t *testing.T) {
			request.Header.Set("Accept", "text/html")
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			if name == "create" {
				handler.DashboardDraftCreate(recorder, request)
			} else {
				handler.DashboardDraftFork(recorder, request)
			}
			if recorder.Code != nethttp.StatusForbidden || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
				t.Fatalf("response = %d %q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
			for _, want := range []string{"You don't have access to this dashboard", "Return to Insights", "No changes were made"} {
				if !strings.Contains(recorder.Body.String(), want) {
					t.Fatalf("response missing %q: %s", want, recorder.Body.String())
				}
			}
		})
	}
}

func TestDashboardBuilderCommandRoutesBuilderIntentsWithServerGeneratedIDs(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64),
		"pageId": "overview", "visualId": "", "componentId": "", "type": "bar", "action": "add_visual",
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "intent-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.intentCalls != 1 || fake.executeCalls != 0 || fake.executed.AddVisual == nil {
		t.Fatalf("builder dispatch calls=%d/%d command=%#v", fake.intentCalls, fake.executeCalls, fake.executed)
	}
	if fake.executed.AddVisual.VisualID != "" || fake.executed.AddVisual.ComponentID != "" || fake.executed.AddVisual.Type != "bar" {
		t.Fatalf("server-generated visual IDs were not preserved: %#v", fake.executed.AddVisual)
	}
}

func TestDashboardBuilderCommandPreservesIdempotencyFallbackWithGeneratedRequestID(t *testing.T) {
	fake := &builderAuthoringFake{}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	correlated := httpmiddleware.RequestCorrelation(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		handler.DashboardBuilderCommand(w, withBuilderURLParams(r, "sales", "revenue"))
	}))

	for range 2 {
		req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
			"dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": "publish",
		}})
		req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
		req.Header.Set("Idempotency-Key", "builder-retry-1")
		rec := httptest.NewRecorder()

		correlated.ServeHTTP(rec, req)

		if rec.Code != nethttp.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		if fake.executed.ID != "builder-retry-1" {
			t.Fatalf("command ID = %q, want stable Idempotency-Key fallback", fake.executed.ID)
		}
		if got := rec.Header().Get("X-Request-ID"); !strings.HasPrefix(got, "req_") {
			t.Fatalf("response request ID = %q, want generated correlation identity", got)
		}
	}
}

func TestDashboardBuilderCommandTranslatesSmartVisualField(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64),
		"pageId": "overview", "type": "kpi", "title": "Revenue", "fieldId": "revenue", "role": "metric", "action": "add_visual",
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "smart-visual-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.executed.AddVisual == nil || fake.executed.AddVisual.Type != "kpi" || fake.executed.AddVisual.FieldID != "revenue" || fake.executed.AddVisual.Role != authoring.FieldRoleMetric {
		t.Fatalf("smart visual command = %#v", fake.executed)
	}
}

func TestDashboardBuilderCommandTranslatesContractFormatOption(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64),
		"pageId": "overview", "visualId": "sales-chart", "formatKey": "stacking", "formatValue": "percent", "action": "update_visual_format",
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "format-option-1")
	recorder := httptest.NewRecorder()
	handler.DashboardBuilderCommand(recorder, withBuilderURLParams(req, "sales", "revenue"))
	if recorder.Code != nethttp.StatusOK {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if fake.executed.UpdateVisualFormat == nil || fake.executed.UpdateVisualFormat.FormatKey != "stacking" || fake.executed.UpdateVisualFormat.FormatValue == nil || *fake.executed.UpdateVisualFormat.FormatValue != "percent" {
		t.Fatalf("format command = %#v", fake.executed.UpdateVisualFormat)
	}
}

func TestDashboardBuilderCommandTranslatesExactRevisionRestore(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	currentHash := "sha256:" + strings.Repeat("b", 64)
	targetHash := "sha256:" + strings.Repeat("a", 64)
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-8", "revisionNumber": "8", "revisionContentHash": currentHash,
		"targetRevisionId": "revision-7", "targetRevisionNumber": "7", "targetRevisionContentHash": targetHash, "action": "restore_revision",
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "restore-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	want := authoring.RevisionToken{RevisionID: "revision-7", Number: 7, ContentHash: targetHash}
	if fake.intentCalls != 1 || fake.executed.RestoreRevision == nil || fake.executed.RestoreRevision.TargetRevision != want {
		t.Fatalf("restore command = %#v", fake.executed)
	}
}

func TestDashboardBuilderCommandTranslatesAtomicPlacements(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	revisionHash := "sha256:" + strings.Repeat("a", 64)
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": revisionHash,
		"pageId": "overview", "action": "set_placements", "placements": []map[string]any{
			{"componentId": "orders-component", "column": 1, "row": 1, "columnSpan": 6, "rowSpan": 4},
			{"visualId": "summary-component", "col": 7, "row": 1, "colSpan": 6, "rowSpan": 4},
		},
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "placement-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if fake.intentCalls != 1 || fake.executeCalls != 0 || fake.executed.SetPlacements == nil {
		t.Fatalf("builder dispatch calls=%d/%d command=%#v", fake.intentCalls, fake.executeCalls, fake.executed)
	}
	placements := fake.executed.SetPlacements.Placements
	if len(placements) != 2 || placements[0].ComponentID != "orders-component" || placements[0].Placement.ColumnSpan != 6 || placements[1].ComponentID != "summary-component" || placements[1].Placement.Column != 7 {
		t.Fatalf("translated placements = %#v", placements)
	}
}

func TestDashboardBuilderCommandTranslatesFocusedFilterMutations(t *testing.T) {
	tests := []struct {
		name   string
		action map[string]any
		assert func(*testing.T, authoring.Command)
	}{
		{name: "add", action: map[string]any{"action": "add_filter", "fieldId": "status", "title": "Status", "dataset": "orders", "controlType": "multiSelect"}, assert: func(t *testing.T, command authoring.Command) {
			if command.AddFilter == nil || command.AddFilter.Dimension != "status" || command.AddFilter.Dataset != "orders" || command.AddFilter.ControlType != "multiSelect" {
				t.Fatalf("add filter = %#v", command.AddFilter)
			}
		}},
		{name: "update", action: map[string]any{"action": "update_filter", "filterId": "status", "title": "Order status", "dataset": "orders", "controlType": "singleSelect", "required": true, "readerEditable": false, "urlParameter": "status"}, assert: func(t *testing.T, command authoring.Command) {
			if command.UpdateFilter == nil || command.UpdateFilter.FilterID != "status" || !command.UpdateFilter.Required || command.UpdateFilter.ReaderEditable || command.UpdateFilter.URLParameter != "status" {
				t.Fatalf("update filter = %#v", command.UpdateFilter)
			}
		}},
		{name: "remove", action: map[string]any{"action": "remove_filter", "filterId": "status"}, assert: func(t *testing.T, command authoring.Command) {
			if command.RemoveFilter == nil || command.RemoveFilter.FilterID != "status" {
				t.Fatalf("remove filter = %#v", command.RemoveFilter)
			}
		}},
		{name: "place slicer", action: map[string]any{"action": "add_filter_component", "pageId": "overview", "filterId": "status", "componentId": "status-slicer"}, assert: func(t *testing.T, command authoring.Command) {
			if command.AddFilterComponent == nil || command.AddFilterComponent.PageID != "overview" || command.AddFilterComponent.FilterID != "status" || command.AddFilterComponent.ComponentID != "status-slicer" {
				t.Fatalf("add filter component = %#v", command.AddFilterComponent)
			}
		}},
		{name: "remove slicer", action: map[string]any{"action": "remove_filter_component", "pageId": "overview", "componentId": "status-slicer"}, assert: func(t *testing.T, command authoring.Command) {
			if command.RemoveFilterComponent == nil || command.RemoveFilterComponent.PageID != "overview" || command.RemoveFilterComponent.ComponentID != "status-slicer" {
				t.Fatalf("remove filter component = %#v", command.RemoveFilterComponent)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
			handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
			signal := map[string]any{"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64)}
			for key, value := range test.action {
				signal[key] = value
			}
			req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": signal})
			req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
			req.Header.Set("X-Request-ID", "filter-"+test.name)
			recorder := httptest.NewRecorder()
			handler.DashboardBuilderCommand(recorder, withBuilderURLParams(req, "sales", "revenue"))
			if recorder.Code != nethttp.StatusOK {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
			test.assert(t, fake.executed)
		})
	}
}

func (f *builderAuthoringFake) Preview(ctx context.Context, request preview.PreviewRequest) (preview.Preview, error) {
	f.previewCtx = ctx
	f.previewReq = request
	f.previewCalls++
	return f.preview, f.previewErr
}
func (f *builderAuthoringFake) ExportYAML(context.Context, sourceadapter.ExportRequest) ([]byte, error) {
	return f.yaml, f.exportErr
}
func (f *builderAuthoringFake) ExportDraftYAML(_ context.Context, request sourceadapter.ExportRequest) ([]byte, error) {
	f.exportDraftCalls++
	f.exportDraftReq = request
	return f.yaml, f.exportErr
}

func builderRequest(method, path string, body any) *nethttp.Request {
	encoded, _ := json.Marshal(body)
	return httptest.NewRequest(method, path, bytes.NewReader(encoded))
}

func withBuilderURLParams(r *nethttp.Request, workspace, dashboard string) *nethttp.Request {
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("project", workspace)
	ctx.URLParams.Add("dashboard", dashboard)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
}

func TestDashboardBuilderCommandTranslatesPublishWithExactRevision(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{
			"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1",
			"revisionNumber": "7", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": "publish",
		},
	})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "command-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fake.executed.ID != "command-1" || fake.executed.DraftID != "draft-1" || fake.executed.ExpectedRevision.Number != 7 || fake.executed.Publish == nil {
		t.Fatalf("translated command = %#v", fake.executed)
	}
	if fake.executed.Provenance.Origin != authoring.OriginUI || fake.executed.Provenance.ActorID != "principal-1" {
		t.Fatalf("provenance = %#v", fake.executed.Provenance)
	}
}

func TestDashboardBuilderCommandRejectsUnsupportedAndMissingClaims(t *testing.T) {
	fake := &builderAuthoringFake{}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	for name, tc := range map[string]struct{ claim, action string }{
		"missing claim": {action: "publish"}, "unsupported action": {claim: dashboardBuilderOperationID, action: "save"},
	} {
		t.Run(name, func(t *testing.T) {
			req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
				"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": tc.action,
			}})
			if tc.claim != "" {
				req.Header.Set("X-LeapView-Operation-ID", tc.claim)
			}
			req.Header.Set("X-Request-ID", "command-1")
			rec := httptest.NewRecorder()
			handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
			if rec.Code != nethttp.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDashboardBuilderPreviewAndExport(t *testing.T) {
	fake := &builderAuthoringFake{yaml: []byte("title: Revenue\n")}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	revisionHash := "sha256:" + strings.Repeat("b", 64)
	previewReq := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/preview?page=overview&revisionId=revision-1&revisionNumber=2&revisionContentHash="+revisionHash, nil)
	previewRec := httptest.NewRecorder()
	handler.DashboardBuilderPreview(previewRec, withBuilderURLParams(previewReq, "sales", "revenue"))
	if previewRec.Code != nethttp.StatusOK || !strings.Contains(previewRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("preview response: status=%d content-type=%q", previewRec.Code, previewRec.Header().Get("Content-Type"))
	}

	exportReq := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/export.yaml", nil)
	exportRec := httptest.NewRecorder()
	handler.DashboardBuilderExportYAML(exportRec, withBuilderURLParams(exportReq, "sales", "revenue"))
	if exportRec.Code != nethttp.StatusOK || exportRec.Body.String() != string(fake.yaml) {
		t.Fatalf("export response: status=%d body=%q", exportRec.Code, exportRec.Body.String())
	}
	if got := exportRec.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="revenue.yaml"`) {
		t.Fatalf("content disposition = %q", got)
	}
	if fake.exportDraftCalls != 1 || fake.exportDraftReq.Source.Kind != sourceadapter.SourceInstance || fake.exportDraftReq.Source.ProjectID != "sales" || fake.exportDraftReq.Source.DashboardID != "revenue" || fake.exportDraftReq.ActorID != "principal-1" {
		t.Fatalf("draft export request = %#v calls=%d", fake.exportDraftReq, fake.exportDraftCalls)
	}
}

func TestDashboardBuilderErrorsDistinguishStaleAndForbidden(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"stale", authoring.ErrStaleRevision, nethttp.StatusConflict},
		{"forbidden", access.ErrForbidden, nethttp.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := Handler{Authoring: &builderAuthoringFake{err: tc.err}, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
			req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/edit", nil)
			rec := httptest.NewRecorder()
			handler.DashboardBuilder(rec, withBuilderURLParams(req, "sales", "revenue"))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if strings.Contains(rec.Body.String(), "revision-1") {
				t.Fatalf("error leaked revision: %s", rec.Body.String())
			}
		})
	}
}

func TestDashboardBuilderRejectsDraftURLMismatchAndCommandScopeSpoof(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	pageReq := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/edit?draft=draft-2", nil)
	pageRec := httptest.NewRecorder()
	handler.DashboardBuilder(pageRec, withBuilderURLParams(pageReq, "sales", "revenue"))
	if pageRec.Code != nethttp.StatusConflict {
		t.Fatalf("draft mismatch status = %d, body = %s", pageRec.Code, pageRec.Body.String())
	}

	commandReq := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "other", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": "publish",
	}})
	commandReq.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	commandReq.Header.Set("X-Request-ID", "command-1")
	commandRec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(commandRec, withBuilderURLParams(commandReq, "sales", "revenue"))
	if commandRec.Code != nethttp.StatusOK {
		t.Fatalf("project field was ignored status = %d, body = %s", commandRec.Code, commandRec.Body.String())
	}
}

func TestDashboardBuilderGETPropagatesPageSelectionAndPageBaseHref(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/edit?page=details&visual=orders-card", nil)
	rec := httptest.NewRecorder()
	handler.DashboardBuilder(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fake.builderReq.SelectedPageID != "details" || fake.builderReq.SelectedVisualID != "orders-card" {
		t.Fatalf("builder selection request = %#v", fake.builderReq)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `page-base-href="/dashboards/revenue/edit?draft=draft-1"`) {
		t.Fatalf("page base href missing from shell: %s", body)
	}
	if strings.Contains(body, "/projects/") {
		t.Fatalf("browser builder shell leaked a project-scoped path: %s", body)
	}
}

func TestDashboardBuilderPreviewVisualsUseAuthoredIDsAndRuntimeMetadata(t *testing.T) {
	definition := canonicalBuilderVisualDefinition(t)
	envelope, err := visualizationruntime.EmptyEnvelopeFromDefinition(definition, 2, 0, 0)
	if err != nil {
		t.Fatalf("empty visualization envelope: %v", err)
	}
	selectedPage := "overview"
	got := dashboardBuilderPreviewVisuals(uisignals.DashboardBuilderSignal{
		DashboardID: "sales", SelectedPageID: &selectedPage,
	}, preview.Preview{
		PagePatch: dashboard.Patch{
			Filters: dashboard.Filters{InteractionRevision: 4},
			Status:  dashboard.Status{Generation: 9},
			Visuals: map[string]visualizationir.VisualizationEnvelope{"orders": envelope},
		},
		SemanticEvidence: preview.SemanticServingStateEvidence{Identity: projectgraph.ServingIdentity{ProjectID: "project", Environment: "dev", GenerationID: "state-7"}},
	})
	signal, ok := got["orders"]
	if !ok {
		t.Fatalf("preview visuals = %#v, want authored orders key", got)
	}
	if signal.VisualID != "orders" || signal.ServingStateID != "state-7" || signal.StreamGeneration != 9 || signal.InteractionRevision != 4 || signal.ConsumerIdentity != "overview/orders" {
		t.Fatalf("preview visual metadata = %#v", signal)
	}
}

func canonicalBuilderVisualDefinition(t *testing.T) visualizationdefinition.Definition {
	t.Helper()
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "cartesian", Title: "Orders", Accessibility: visualizationir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "label", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Label"}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}}, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkLine, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	definition, err := visualizationdefinition.New("orders", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "label", Alias: "label"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func TestDashboardBuilderPreviewFailureKeepsBuilderVisible(t *testing.T) {
	fake := &builderAuthoringFake{
		builder:    uisignals.DashboardBuilderSignal{ProjectID: "project", DashboardID: "sales", DraftID: "draft-1"},
		previewErr: errors.New("query preview failed"),
	}
	handler := Handler{Authoring: fake}
	envelope := handler.dashboardBuilderEnvelopeWithPreview(context.Background(), "actor-1", fake.builder)
	if fake.previewCalls != 1 {
		t.Fatalf("preview calls = %d, want 1", fake.previewCalls)
	}
	if envelope.Builder.Preview.Active || envelope.Builder.Preview.Error == nil || *envelope.Builder.Preview.Error != "query preview failed" {
		t.Fatalf("preview failure state = %#v", envelope.Builder.Preview)
	}
	if envelope.BuilderVisuals == nil || len(envelope.BuilderVisuals) != 0 {
		t.Fatalf("preview failure visuals = %#v, want empty map", envelope.BuilderVisuals)
	}
	statusFailure := &builderAuthoringFake{
		builder: uisignals.DashboardBuilderSignal{ProjectID: "project", DashboardID: "sales", DraftID: "draft-1"},
		preview: preview.Preview{PagePatch: dashboard.Patch{Status: dashboard.Status{Error: "runtime unavailable"}}},
	}
	statusEnvelope := (Handler{Authoring: statusFailure}).dashboardBuilderEnvelopeWithPreview(context.Background(), "actor-1", statusFailure.builder)
	if statusEnvelope.Builder.Preview.Active || statusEnvelope.Builder.Preview.Error == nil || *statusEnvelope.Builder.Preview.Error != "runtime unavailable" {
		t.Fatalf("status preview failure state = %#v", statusEnvelope.Builder.Preview)
	}
}

func TestDashboardBuilderPreviewSuccessExplicitlyClearsStreamedError(t *testing.T) {
	stale := "aggregate visual requires a metric"
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		ProjectID: "project", DashboardID: "sales", DraftID: "draft-1",
		Preview: uisignals.DashboardBuilderPreviewStateSignal{Error: &stale},
	}}
	envelope := (Handler{Authoring: fake}).dashboardBuilderEnvelopeWithPreview(context.Background(), "actor-1", fake.builder)
	if !envelope.Builder.Preview.Active || envelope.Builder.Preview.Error == nil || *envelope.Builder.Preview.Error != "" {
		t.Fatalf("successful preview did not explicitly clear prior error: %#v", envelope.Builder.Preview)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"error":""`)) {
		t.Fatalf("successful preview patch omits explicit error clear: %s", encoded)
	}
}

func TestDashboardBuilderInlinePreviewUsesAnalyticalContext(t *testing.T) {
	marker := &struct{}{}
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{ProjectID: "project", DashboardID: "sales", DraftID: "draft-1"}}
	handler := Handler{
		Authoring: fake,
		AnalyticalContext: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, marker, true)
		},
	}
	_ = handler.dashboardBuilderEnvelopeWithPreview(context.Background(), "actor-1", fake.builder)
	if fake.previewCtx == nil || fake.previewCtx.Value(marker) != true {
		t.Fatal("inline builder preview did not receive analytical context")
	}
}

func TestDashboardBuilderStandalonePreviewUsesAnalyticalContext(t *testing.T) {
	marker := &struct{}{}
	fake := &builderAuthoringFake{}
	handler := Handler{
		Authoring:          fake,
		CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" },
		AnalyticalContext: func(ctx context.Context) context.Context {
			return context.WithValue(ctx, marker, true)
		},
	}
	hash := "sha256:" + strings.Repeat("a", 64)
	req := httptest.NewRequest(nethttp.MethodGet, "/dashboards/revenue/preview?draft=draft-1&page=overview&revisionId=revision-1&revisionNumber=1&revisionContentHash="+hash, nil)
	rec := httptest.NewRecorder()
	handler.DashboardBuilderPreview(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fake.previewCtx == nil || fake.previewCtx.Value(marker) != true {
		t.Fatal("standalone builder preview did not receive analytical context")
	}
}

func TestDashboardBuilderCommandRepreviewsAuthoritativeRevision(t *testing.T) {
	selectedPage := "overview"
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{
		ProjectID: "sales", DashboardID: "revenue", DraftID: "draft-1",
		Revision: uisignals.DashboardBuilderRevisionSignal{ID: "revision-1", Number: 7, ContentHash: "sha256:" + strings.Repeat("a", 64)},
		Pages:    []uisignals.DashboardBuilderPageSignal{{ID: selectedPage}}, SelectedPageID: &selectedPage,
	}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"projectId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "7", "revisionContentHash": "sha256:" + strings.Repeat("a", 64),
		"pageId": selectedPage, "action": "publish",
	}})
	req.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	req.Header.Set("X-Request-ID", "command-preview-1")
	rec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(rec, withBuilderURLParams(req, "sales", "revenue"))
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if fake.previewCalls != 1 {
		t.Fatalf("preview calls = %d, want 1", fake.previewCalls)
	}
	if fake.previewReq.ProjectID != "sales" || fake.previewReq.ActorID != "principal-1" || fake.previewReq.DashboardID != "revenue" || fake.previewReq.DraftID != "draft-1" || fake.previewReq.ExpectedRevision.Number != 7 || fake.previewReq.ExpectedRevision.RevisionID != "revision-1" || fake.previewReq.PageID != selectedPage {
		t.Fatalf("preview request = %#v", fake.previewReq)
	}
}
