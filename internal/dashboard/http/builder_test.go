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
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
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
	if !strings.Contains(body, `page-base-href="/dashboards/revenue/edit"`) {
		t.Fatalf("page base href missing from shell: %s", body)
	}
	if strings.Contains(body, "/projects/") {
		t.Fatalf("browser builder shell leaked a project-scoped path: %s", body)
	}
}

func TestDashboardBuilderPreviewVisualsUseAuthoredIDsAndRuntimeMetadata(t *testing.T) {
	definitions, err := dashboardcompiler.CompileVisualizationDefinitions(&authoring.Dashboard{
		ID: "sales", SemanticModel: "sales",
		Visuals: authoring.ChartVisualizations(map[string]authoring.Visual{
			"orders": {Type: "line", Title: "Orders", Query: authoring.VisualQuery{Table: "orders", Measures: []authoring.FieldRef{{Field: "orders.revenue"}}}},
		}),
	})
	if err != nil {
		t.Fatalf("compile visualization definition: %v", err)
	}
	envelope, err := visualizationruntime.EmptyEnvelopeFromDefinition(definitions["orders"], 2, 0, 0)
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
