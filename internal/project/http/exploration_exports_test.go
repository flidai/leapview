package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration"
	explorationexport "github.com/flidai/leapview/internal/analytics/exploration/export"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type browserExportServiceStub struct {
	savedExplorationBrowserServiceStub
	executeSpec func(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error)
}

func (s browserExportServiceStub) Execute(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error) {
	return saved.ExecuteResult{}, errors.New("unexpected saved export execution")
}

func (s browserExportServiceStub) ExecuteSpec(ctx context.Context, request saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
	if s.executeSpec != nil {
		return s.executeSpec(ctx, request)
	}
	return saved.ExecuteResult{}, errors.New("unexpected URL export execution")
}

type browserExportAuditRecorder struct {
	events   []queryaudit.EventInput
	onRecord func()
}

func (r *browserExportAuditRecorder) RecordQueryEvent(_ context.Context, event queryaudit.EventInput) error {
	r.events = append(r.events, event)
	if r.onRecord != nil {
		r.onRecord()
		r.onRecord = nil
	}
	return nil
}

type browserExportFailingService struct {
	savedExplorationBrowserServiceStub
	err error
}

func (s browserExportFailingService) Execute(context.Context, saved.ExecuteRequest) (saved.ExecuteResult, error) {
	return saved.ExecuteResult{}, s.err
}

func (s browserExportFailingService) ExecuteSpec(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
	return saved.ExecuteResult{}, s.err
}

func browserExportURL(t *testing.T, spec exploration.ExplorationSpec, format string) string {
	t.Helper()
	state, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}, "format": {format}, "saved": {"unpublished-copy"}}
	return "/explore/export?" + values.Encode()
}

func TestExplorationExportUsesCanonicalURLStateAndReturnsRawCSV(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	audit := &browserExportAuditRecorder{}
	stub := browserExportServiceStub{executeSpec: func(_ context.Context, request saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
		if request.ProjectID != "project:test" || request.ActorID != "principal:test" || request.Spec.ModelID != spec.ModelID {
			t.Fatalf("URL execution request = %#v", request)
		}
		return saved.ExecuteResult{Query: dataquery.Query{ProjectID: "project:test", PrincipalID: "principal:test"}, Result: dataquery.Result{
			Columns: []dataquery.Column{{Name: "status"}}, Rows: []dataquery.Row{{"status": "paid"}}, TotalRows: 1, TotalRowsKnown: true,
			Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded,
		}}, nil
	}}
	h := &BrowserHandler{
		SavedExplorations:              stub,
		ExplorationExportAuditRecorder: audit,
		ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	h.ExplorationExport(recorder, httptest.NewRequest(http.MethodGet, browserExportURL(t, spec, "csv"), nil))
	if recorder.Code != http.StatusOK || recorder.Body.String() != "status\npaid\n" {
		t.Fatalf("URL export response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") != "text/csv; charset=utf-8" || recorder.Header().Get("Content-Disposition") == "" {
		t.Fatalf("URL export headers = %#v", recorder.Header())
	}
	if len(audit.events) != 1 || audit.events[0].Status != "success" {
		t.Fatalf("URL export audit = %#v", audit.events)
	}
}

func TestExplorationExportRejectsNonCanonicalURLBeforeExecution(t *testing.T) {
	called := false
	stub := browserExportServiceStub{executeSpec: func(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
		called = true
		return saved.ExecuteResult{}, nil
	}}
	h := &BrowserHandler{
		SavedExplorations:              stub,
		ExplorationExportAuditRecorder: &browserExportAuditRecorder{},
		ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	h.ExplorationExport(recorder, httptest.NewRequest(http.MethodGet, "/explore/export?v=1&mode=explore&format=csv", nil))
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("non-canonical URL response = %d called=%t body=%q", recorder.Code, called, recorder.Body.String())
	}
}

func TestExplorationExportRejectsRepeatedBoundOption(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	called := false
	h := &BrowserHandler{
		SavedExplorations: browserExportServiceStub{executeSpec: func(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
			called = true
			return saved.ExecuteResult{}, nil
		}},
		ExplorationExportAuditRecorder: &browserExportAuditRecorder{},
		ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	state, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	h.ExplorationExport(recorder, httptest.NewRequest(http.MethodGet, "/explore/export?v=2&mode=explore&state="+url.QueryEscape(string(state))+"&format=csv&format=parquet", nil))
	if recorder.Code != http.StatusBadRequest || called {
		t.Fatalf("repeated option response = %d called=%t body=%q", recorder.Code, called, recorder.Body.String())
	}
}

func TestExplorationExportAuditsCanceledExecution(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	audit := &browserExportAuditRecorder{}
	h := &BrowserHandler{
		SavedExplorations:              browserExportFailingService{err: context.Canceled},
		ExplorationExportAuditRecorder: audit,
		ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	h.ExplorationExport(recorder, httptest.NewRequest(http.MethodGet, browserExportURL(t, spec, "csv"), nil))
	if recorder.Code != http.StatusConflict || len(audit.events) != 1 || audit.events[0].ExecutionState != "export_canceled" {
		t.Fatalf("canceled export response = %d audit=%#v", recorder.Code, audit.events)
	}
}

func TestExplorationExportDoesNotSendAfterRequestCancellationDuringAudit(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	ctx, cancel := context.WithCancel(context.Background())
	audit := &browserExportAuditRecorder{onRecord: cancel}
	h := &BrowserHandler{
		SavedExplorations: browserExportServiceStub{executeSpec: func(context.Context, saved.ExecuteSpecRequest) (saved.ExecuteResult, error) {
			return saved.ExecuteResult{Query: dataquery.Query{ProjectID: "project:test", PrincipalID: "principal:test"}, Result: dataquery.Result{
				Columns: []dataquery.Column{{Name: "status"}}, Rows: []dataquery.Row{{"status": "paid"}}, TotalRows: 1, TotalRowsKnown: true,
				Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded,
			}}, nil
		}},
		ExplorationExportAuditRecorder: audit,
		ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, browserExportURL(t, spec, "csv"), nil).WithContext(ctx)
	h.ExplorationExport(recorder, request)
	if recorder.Code != http.StatusConflict || recorder.Header().Get("Content-Disposition") != "" {
		t.Fatalf("canceled-after-audit response = %d headers=%#v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if len(audit.events) != 2 || audit.events[0].ExecutionState != "export_success" || audit.events[1].ExecutionState != "export_canceled" {
		t.Fatalf("canceled-after-audit events = %#v", audit.events)
	}
}

func TestExplorationExportMapsDeniedAndPartialErrorsWithoutLeak(t *testing.T) {
	dataset := "orders"
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "order_count"}},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{name: "denied", err: saved.ErrUnauthorized, want: http.StatusNotFound},
		{name: "partial", err: explorationexport.ErrPartial, want: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &BrowserHandler{
				SavedExplorations:              browserExportFailingService{err: test.err},
				ExplorationExportAuditRecorder: &browserExportAuditRecorder{},
				ResolveProjectID:               func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
				CurrentUser:                    func(*http.Request) (Principal, bool) { return Principal{ID: "principal:test"}, true },
			}
			recorder := httptest.NewRecorder()
			h.ExplorationExport(recorder, httptest.NewRequest(http.MethodGet, browserExportURL(t, spec, "csv"), nil))
			if recorder.Code != test.want {
				t.Fatalf("response status = %d, want %d body=%q", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}
