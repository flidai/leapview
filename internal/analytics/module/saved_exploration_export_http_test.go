package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/go-chi/chi/v5"
)

type mountedAnalyticsAPIGenServer struct{ config AnalyticsAPIGenConfig }

func (server mountedAnalyticsAPIGenServer) HandleAPIGen(operationID string, w http.ResponseWriter, r *http.Request) {
	DispatchAPIGenOperation(server.config, operationID, nil, w, r)
}

type exportAuditRecorder struct {
	events []queryaudit.EventInput
	err    error
	cancel context.CancelFunc
}

func (r *exportAuditRecorder) RecordQueryEvent(_ context.Context, event queryaudit.EventInput) error {
	r.events = append(r.events, event)
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	return r.err
}

func executeExportResult() saved.ExecuteResult {
	return saved.ExecuteResult{
		Query: dataquery.Query{ProjectID: "project:sales", PrincipalID: "principal-1", Surface: "saved_exploration", ObjectType: "saved_exploration", ObjectID: "exploration-1"},
		Result: dataquery.Result{
			Columns:        []dataquery.Column{{Name: "name"}, {Name: "amount"}},
			Rows:           []dataquery.Row{{"name": "Orders", "amount": int64(3)}},
			TotalRows:      1,
			TotalRowsKnown: true,
			Status:         dataquery.StatusSuccess,
			ExecutionState: dataquery.ExecutionSucceeded,
		},
	}
}

func TestSavedExplorationExportReturnsRawCSVAndAuditsDelivery(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(_ context.Context, request saved.ExecuteRequest) (saved.ExecuteResult, error) {
		if request.ExpectedRevision != fixture.revision.Token() {
			t.Fatalf("expected revision = %#v, want %#v", request.ExpectedRevision, fixture.revision.Token())
		}
		result := executeExportResult()
		return result, nil
	}}
	audit := &exportAuditRecorder{}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: audit,
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "csv"})
	recorder := httptest.NewRecorder()
	handler.ExportSavedExploration(recorder, request, "project:sales", "exploration-1", analyticsgen.GenExportSavedExplorationHeaders{IfMatch: revisionETag(fixture.revision.Token())})

	if recorder.Code != http.StatusOK || recorder.Body.String() != "name,amount\nOrders,3\n" {
		t.Fatalf("export response = %d %q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if strings.HasPrefix(recorder.Body.String(), "{") || recorder.Header().Get("Content-Disposition") == "" {
		t.Fatalf("export was not a raw attachment: headers=%#v body=%q", recorder.Header(), recorder.Body.String())
	}
	if len(audit.events) != 1 || audit.events[0].Operation != "saved_exploration_export_preparation" || audit.events[0].Status != "success" {
		t.Fatalf("audit events = %#v", audit.events)
	}
}

func TestMountedSavedExplorationExportReturnsRawParquet(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(_ context.Context, _ saved.ExecuteRequest) (saved.ExecuteResult, error) {
		return executeExportResult(), nil
	}}
	config := AnalyticsAPIGenConfig{SavedExplorations: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: &exportAuditRecorder{},
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	router := chi.NewRouter()
	analyticsgen.RegisterAPIGenRoutes(router, mountedAnalyticsAPIGenServer{config: config})
	request := savedExplorationHTTPRequest(http.MethodPost, "/api/v1/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "parquet"})
	request.Header.Set("If-Match", revisionETag(fixture.revision.Token()))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.Len() < 8 {
		t.Fatalf("mounted export response = %d body length=%d", recorder.Code, recorder.Body.Len())
	}
	if string(recorder.Body.Bytes()[:4]) != "PAR1" || recorder.Header().Get("Content-Type") != "application/vnd.apache.parquet" {
		t.Fatalf("mounted export was not raw Parquet: content-type=%q prefix=%x", recorder.Header().Get("Content-Type"), recorder.Body.Bytes()[:4])
	}
}

func TestSavedExplorationExportRejectsStaleRevisionWithoutFile(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error) {
		return saved.ExecuteResult{}, saved.ErrStaleRevision
	}}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: &exportAuditRecorder{},
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "csv"})
	recorder := httptest.NewRecorder()
	handler.ExportSavedExploration(recorder, request, "project:sales", "exploration-1", analyticsgen.GenExportSavedExplorationHeaders{IfMatch: revisionETag(saved.RevisionToken{RevisionID: "revision-other", Number: 2, ContentHash: fixture.revision.Metadata.ContentHash})})
	if recorder.Code != http.StatusPreconditionFailed || recorder.Body.Len() == 0 {
		t.Fatalf("stale response = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("stale response advertised a file: headers=%#v", recorder.Header())
	}
}

func TestSavedExplorationExportRejectsInvalidBoundsAsClientError(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	called := false
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error) {
		called = true
		return executeExportResult(), nil
	}}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: &exportAuditRecorder{},
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "csv", "maxRows": 0})
	recorder := httptest.NewRecorder()
	handler.ExportSavedExploration(recorder, request, "project:sales", "exploration-1", analyticsgen.GenExportSavedExplorationHeaders{IfMatch: revisionETag(fixture.revision.Token())})
	if recorder.Code != http.StatusBadRequest || called || strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("invalid bounds response = %d called=%t headers=%#v body=%q", recorder.Code, called, recorder.Header(), recorder.Body.String())
	}
}

func TestSavedExplorationExportRejectsTruncationSentinel(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error) {
		result := executeExportResult()
		result.Query.Limit = len(result.Result.Rows)
		return result, nil
	}}
	audit := &exportAuditRecorder{}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: audit,
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "csv"})
	recorder := httptest.NewRecorder()
	handler.ExportSavedExploration(recorder, request, "project:sales", "exploration-1", analyticsgen.GenExportSavedExplorationHeaders{IfMatch: revisionETag(fixture.revision.Token())})
	if recorder.Code != http.StatusUnprocessableEntity || strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("truncation sentinel response = %d headers=%#v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if len(audit.events) != 1 || audit.events[0].ExecutionState != "export_partial" {
		t.Fatalf("truncation sentinel audit = %#v", audit.events)
	}
}

func TestSavedExplorationURLExportRequiresDeliveryAudit(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, executeSpec: func(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
		return executeExportResult(), nil
	}}
	audit := &exportAuditRecorder{err: errors.New("audit store unavailable")}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: audit,
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/url-export", analyticsgen.GenExportSavedExplorationURLBody{
		SavedExplorationExportRequest: analyticsgen.SavedExplorationExportRequest{Format: analyticsgen.SavedExplorationExportFormatCsv}, Spec: fixture.spec,
	})
	recorder := httptest.NewRecorder()
	handler.ExportSavedExplorationURL(recorder, request, "project:sales")
	if recorder.Code != http.StatusServiceUnavailable || recorder.Body.Len() == 0 {
		t.Fatalf("audit failure response = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("audit failure advertised a file: headers=%#v", recorder.Header())
	}
}

func TestSavedExplorationExportDoesNotSendAfterRequestCancellationDuringAudit(t *testing.T) {
	fixture := newSavedExplorationHTTPFixture(t)
	stub := &savedExplorationServiceStub{fixture: fixture, execute: func(_ context.Context, _ saved.ExecuteRequest) (saved.ExecuteResult, error) {
		return executeExportResult(), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	audit := &exportAuditRecorder{cancel: cancel}
	handler := savedExplorationAPIHandler{config: SavedExplorationAPIGenConfig{
		Service: stub, ExportAuditRecorder: audit,
		CurrentPrincipal: func(*http.Request) (string, bool) { return "principal-1", true },
	}}
	request := savedExplorationHTTPRequest(http.MethodPost, "/projects/project:sales/saved-explorations/exploration-1/export", map[string]any{"format": "csv"}).WithContext(ctx)
	recorder := httptest.NewRecorder()
	handler.ExportSavedExploration(recorder, request, "project:sales", "exploration-1", analyticsgen.GenExportSavedExplorationHeaders{IfMatch: revisionETag(fixture.revision.Token())})
	if recorder.Code != http.StatusConflict || strings.Contains(recorder.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("canceled-after-audit response = %d headers=%#v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if len(audit.events) != 2 || audit.events[0].ExecutionState != "export_success" || audit.events[1].ExecutionState != "export_canceled" {
		t.Fatalf("canceled-after-audit events = %#v", audit.events)
	}
}
