package http

import (
	"bytes"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/builderview"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/go-chi/chi/v5"
)

type builderAuthoringFake struct {
	builder          uisignals.DashboardBuilderSignal
	executed         authoring.Command
	preview          preview.Preview
	yaml             []byte
	err              error
	previewErr       error
	exportErr        error
	exportDraftCalls int
	exportDraftReq   sourceadapter.ExportRequest
	executeCalls     int
	intentCalls      int
}

func (f *builderAuthoringFake) Builder(context.Context, builderview.Request) (uisignals.DashboardBuilderSignal, error) {
	return f.builder, f.err
}
func (f *builderAuthoringFake) Execute(_ context.Context, _ string, command authoring.Command) (authoringservice.Result, error) {
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
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{WorkspaceID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/workspaces/sales/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"workspaceId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64),
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
func (f *builderAuthoringFake) Preview(context.Context, preview.PreviewRequest) (preview.Preview, error) {
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
	ctx.URLParams.Add("workspace", workspace)
	ctx.URLParams.Add("dashboard", dashboard)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
}

func TestDashboardBuilderCommandTranslatesPublishWithExactRevision(t *testing.T) {
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{WorkspaceID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	req := builderRequest(nethttp.MethodPost, "/workspaces/sales/dashboards/revenue/draft/command", map[string]any{
		"builderCommand": map[string]any{
			"workspaceId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1",
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
			req := builderRequest(nethttp.MethodPost, "/workspaces/sales/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
				"workspaceId": "sales", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": tc.action,
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
	previewReq := httptest.NewRequest(nethttp.MethodGet, "/workspaces/sales/dashboards/revenue/preview?page=overview&revisionId=revision-1&revisionNumber=2&revisionContentHash="+revisionHash, nil)
	previewRec := httptest.NewRecorder()
	handler.DashboardBuilderPreview(previewRec, withBuilderURLParams(previewReq, "sales", "revenue"))
	if previewRec.Code != nethttp.StatusOK || !strings.Contains(previewRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("preview response: status=%d content-type=%q", previewRec.Code, previewRec.Header().Get("Content-Type"))
	}

	exportReq := httptest.NewRequest(nethttp.MethodGet, "/workspaces/sales/dashboards/revenue/export.yaml", nil)
	exportRec := httptest.NewRecorder()
	handler.DashboardBuilderExportYAML(exportRec, withBuilderURLParams(exportReq, "sales", "revenue"))
	if exportRec.Code != nethttp.StatusOK || exportRec.Body.String() != string(fake.yaml) {
		t.Fatalf("export response: status=%d body=%q", exportRec.Code, exportRec.Body.String())
	}
	if got := exportRec.Header().Get("Content-Disposition"); !strings.Contains(got, `attachment; filename="revenue.yaml"`) {
		t.Fatalf("content disposition = %q", got)
	}
	if fake.exportDraftCalls != 1 || fake.exportDraftReq.Source.Kind != sourceadapter.SourceWorkspace || fake.exportDraftReq.Source.WorkspaceID != "sales" || fake.exportDraftReq.Source.DashboardID != "revenue" || fake.exportDraftReq.ActorID != "principal-1" {
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
			req := httptest.NewRequest(nethttp.MethodGet, "/workspaces/sales/dashboards/revenue/edit", nil)
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
	fake := &builderAuthoringFake{builder: uisignals.DashboardBuilderSignal{WorkspaceID: "sales", DashboardID: "revenue", DraftID: "draft-1"}}
	handler := Handler{Authoring: fake, CurrentPrincipalID: func(*nethttp.Request) string { return "principal-1" }}
	pageReq := httptest.NewRequest(nethttp.MethodGet, "/workspaces/sales/dashboards/revenue/edit?draft=draft-2", nil)
	pageRec := httptest.NewRecorder()
	handler.DashboardBuilder(pageRec, withBuilderURLParams(pageReq, "sales", "revenue"))
	if pageRec.Code != nethttp.StatusConflict {
		t.Fatalf("draft mismatch status = %d, body = %s", pageRec.Code, pageRec.Body.String())
	}

	commandReq := builderRequest(nethttp.MethodPost, "/workspaces/sales/dashboards/revenue/draft/command", map[string]any{"builderCommand": map[string]any{
		"workspaceId": "other", "dashboardId": "revenue", "draftId": "draft-1", "revisionId": "revision-1", "revisionNumber": "1", "revisionContentHash": "sha256:" + strings.Repeat("a", 64), "action": "publish",
	}})
	commandReq.Header.Set("X-LeapView-Operation-ID", dashboardBuilderOperationID)
	commandReq.Header.Set("X-Request-ID", "command-1")
	commandRec := httptest.NewRecorder()
	handler.DashboardBuilderCommand(commandRec, withBuilderURLParams(commandReq, "sales", "revenue"))
	if commandRec.Code != nethttp.StatusBadRequest {
		t.Fatalf("scope spoof status = %d, body = %s", commandRec.Code, commandRec.Body.String())
	}
}
