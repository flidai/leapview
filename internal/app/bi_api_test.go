package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func newPublicAPIRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer dev")
	return req
}

func servingSnapshotRequest(t *testing.T, server *appTestHarness, req *http.Request) *http.Request {
	t.Helper()
	if server == nil || server.runtime.runtimeHostModule == nil {
		t.Fatal("test runtime host is unavailable")
	}
	lease, err := server.runtime.runtimeHostModule.Acquire(req.Context())
	if err != nil {
		t.Fatalf("acquire test serving identity: %v", err)
	}
	req.Header.Set("X-Serving-Snapshot", lease.Identity().GenerationID)
	lease.Release()
	return req
}

func dashboardAPISetFilterBody(t *testing.T, pageID, bindingID string, values ...string) string {
	t.Helper()
	typed := make([]dashboardfilter.Value, len(values))
	for index, value := range values {
		typed[index] = dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value}
	}
	body := map[string]any{"filterState": map[string]any{
		"version": "typed_v1",
		"controls": map[string]any{
			dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, pageID, bindingID): dashboardfilter.Expression{
				Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: typed,
			},
		},
	}}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestBIAPIListResponsesUseStandardEnvelope(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	for _, tc := range []struct {
		path string
		name string
	}{
		{path: "/api/v1/dashboards?limit=1", name: "dashboards"},
		{path: "/api/v1/semantic-models?limit=1", name: "semantic models"},
	} {
		req := servingSnapshotRequest(t, server, newPublicAPIRequest(http.MethodGet, tc.path, nil))
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
		var response struct {
			Items []map[string]any `json:"items"`
			Page  struct {
				NextCursor string `json:"nextCursor"`
			} `json:"page"`
			Dashboards any `json:"dashboards"`
			Models     any `json:"models"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s response: %v body=%s", tc.name, err, rec.Body.String())
		}
		if len(response.Items) != 1 {
			t.Fatalf("%s items = %#v", tc.name, response.Items)
		}
		if response.Dashboards != nil || response.Models != nil {
			t.Fatalf("%s response leaked legacy wrapper: %s", tc.name, rec.Body.String())
		}
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{path: "/api/v1/dashboards/executive-sales", want: `"detail_tools"`},
		{path: "/api/v1/semantic-models/test", want: `"model_tables"`},
	} {
		req := servingSnapshotRequest(t, server, newPublicAPIRequest(http.MethodGet, tc.path, nil))
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
}

func TestBIAPIListPaginationRejectsMalformedLimit(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	req := newPublicAPIRequest(http.MethodGet, "/api/v1/dashboards?limit=oops", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertAPIError(t, rec, http.StatusBadRequest, "limit")
}

func TestBIAPIQueriesBoundRowsAndPageData(t *testing.T) {
	server := assembleRuntime(manyRowsMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	pageReq := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/query", strings.NewReader(dashboardAPISetFilterBody(t, "overview", "state", "SP")))
	pageReq.Header.Set("Accept", "application/json")
	pageReq.Header.Set("Content-Type", "application/json")
	pageRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK || !strings.Contains(pageRec.Body.String(), `"visuals"`) {
		t.Fatalf("page query status=%d body=%s", pageRec.Code, pageRec.Body.String())
	}

	tableReq := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/visuals/order_rows/query", strings.NewReader(`{"limit":500}`))
	tableReq.Header.Set("Accept", "application/json")
	tableReq.Header.Set("Content-Type", "application/json")
	tableRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(tableRec, tableReq)
	if tableRec.Code != http.StatusOK {
		t.Fatalf("table query status=%d body=%s", tableRec.Code, tableRec.Body.String())
	}
	var table visualizationir.VisualizationEnvelope
	if err := json.Unmarshal(tableRec.Body.Bytes(), &table); err != nil {
		t.Fatalf("decode table: %v body=%s", err, tableRec.Body.String())
	}
	state, ok := table.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || state.AvailableRows != 500 || len(state.Blocks["a"].Rows) != 500 {
		t.Fatalf("table did not honor query limit: %#v", table)
	}
}

func TestBIAPIDashboardVisualDataSurface(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	componentReq := newPublicAPIRequest(http.MethodGet, "/api/v1/dashboards/executive-sales/pages/overview", nil)
	componentReq.Header.Set("Accept", "application/json")
	componentRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(componentRec, componentReq)
	if componentRec.Code != http.StatusOK {
		t.Fatalf("components status=%d body=%s", componentRec.Code, componentRec.Body.String())
	}
	var components struct {
		Items []struct {
			ID        string  `json:"id"`
			Kind      string  `json:"kind"`
			Ref       string  `json:"ref"`
			Title     string  `json:"title"`
			Placement any     `json:"placement"`
			X         float64 `json:"x"`
		} `json:"items"`
		Page struct {
			NextCursor string `json:"nextCursor"`
		} `json:"page"`
	}
	if err := json.Unmarshal(componentRec.Body.Bytes(), &components); err != nil {
		t.Fatalf("decode components: %v body=%s", err, componentRec.Body.String())
	}
	if componentRec.Code != http.StatusOK || !strings.Contains(componentRec.Body.String(), `"id":"overview"`) {
		t.Fatalf("page response = %s", componentRec.Body.String())
	}

	visualReq := newPublicAPIRequest(http.MethodGet, "/api/v1/dashboards/executive-sales/pages/overview/visuals/orders", nil)
	visualReq.Header.Set("Accept", "application/json")
	visualRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(visualRec, visualReq)
	if visualRec.Code != http.StatusOK || !strings.Contains(visualRec.Body.String(), `"title":"Orders"`) || !strings.Contains(visualRec.Body.String(), `"componentId":"orders-chart"`) {
		t.Fatalf("visual describe status=%d body=%s", visualRec.Code, visualRec.Body.String())
	}

	dataReq := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/visuals/orders/query", strings.NewReader(dashboardAPISetFilterBody(t, "overview", "state", "SP")))
	dataReq.Header.Set("Accept", "application/json")
	dataReq.Header.Set("Content-Type", "application/json")
	dataRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(dataRec, dataReq)
	if dataRec.Code != http.StatusOK || !strings.Contains(dataRec.Body.String(), `"dataState"`) || !strings.Contains(dataRec.Body.String(), `"delivered"`) {
		t.Fatalf("visual data status=%d body=%s", dataRec.Code, dataRec.Body.String())
	}

	tableReq := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/visuals/order_rows/query", strings.NewReader(`{"limit":10}`))
	tableReq.Header.Set("Accept", "application/json")
	tableReq.Header.Set("Content-Type", "application/json")
	tableRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(tableRec, tableReq)
	if tableRec.Code != http.StatusOK || !strings.Contains(tableRec.Body.String(), `"o1"`) || !strings.Contains(tableRec.Body.String(), `"rows"`) {
		t.Fatalf("table data status=%d body=%s", tableRec.Code, tableRec.Body.String())
	}

	filterDescribeReq := newPublicAPIRequest(http.MethodGet, "/api/v1/dashboards/executive-sales/pages/overview/filters/state", nil)
	filterDescribeRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(filterDescribeRec, filterDescribeReq)
	filterDescribeBody := filterDescribeRec.Body.String()
	if filterDescribeRec.Code != http.StatusOK ||
		!strings.Contains(filterDescribeBody, `"definition"`) ||
		!strings.Contains(filterDescribeBody, `"binding"`) ||
		!strings.Contains(filterDescribeBody, `"key":"fb_`) ||
		!strings.Contains(filterDescribeBody, `"valueKind":"string"`) ||
		strings.Contains(filterDescribeBody, `"multiSelect"`) {
		t.Fatalf("filter describe status=%d body=%s", filterDescribeRec.Code, filterDescribeBody)
	}

	filterReq := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/filters/state/values?limit=1", strings.NewReader(`{}`))
	filterReq.Header.Set("Accept", "application/json")
	filterReq.Header.Set("Content-Type", "application/json")
	filterRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(filterRec, filterReq)
	if filterRec.Code != http.StatusOK || !strings.Contains(filterRec.Body.String(), `"items"`) || !strings.Contains(filterRec.Body.String(), `"SP"`) {
		t.Fatalf("filter options status=%d body=%s", filterRec.Code, filterRec.Body.String())
	}
}

func TestSemanticAPIQueryAuditIncludesProject(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))
	req := newPublicAPIRequest(http.MethodPost, "/api/v1/semantic-models/test/query", strings.NewReader(`{"dimensions":[{"field":"orders.status","alias":"status"}],"measures":[{"field":"order_count"}],"limit":1}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req_api_project")
	req.Header.Set("X-Correlation-ID", "corr_api_project")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{ProjectID: projectgraph.ResourceID("project:test"), Search: "req_api_project"})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %#v", len(events), events)
	}
	event := events[0]
	if event.ProjectID != "project:test" || event.Surface != dataquery.SurfaceAPI || event.Operation != dataquery.OperationAPIQuery {
		t.Fatalf("event metadata = %#v", event)
	}
	if event.RequestID != "req_api_project" || event.CorrelationID != "corr_api_project" {
		t.Fatalf("request/correlation = %q/%q", event.RequestID, event.CorrelationID)
	}
	if strings.Contains(event.QueryJSON, "delivered") || strings.Contains(event.QueryJSON, "shipped") {
		t.Fatalf("query event stored result row values: %s", event.QueryJSON)
	}

	listReq := newPublicAPIRequest(http.MethodGet, "/api/v1/projects/project:test/query-events?search=req_api_project&limit=10", nil)
	listReq.Header.Set("Accept", "application/json")
	listRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("query events status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(listRec.Body.String(), `"requestId":"req_api_project"`) || !strings.Contains(listRec.Body.String(), `"projectId":"project:test"`) {
		t.Fatalf("query events endpoint did not return project-scoped event: %s", listRec.Body.String())
	}
}

func TestDashboardPageQueryWritesQueryEvents(t *testing.T) {
	server := assembleRuntime(auditedDashboardMetrics{fakeMetrics: fakeMetrics{}}, testStoreOptions(testStore(t), assemblyConfig{}))
	req := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/query", strings.NewReader(`{}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req_dashboard_page")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{ProjectID: projectgraph.ResourceID("project:test"), Search: "req_dashboard_page"})
	if len(events) != 2 {
		t.Fatalf("events = %d, want aggregate and tabular queries: %#v", len(events), events)
	}
	operations := map[string]bool{}
	for _, event := range events {
		if event.Surface != dataquery.SurfaceAPI || event.ObjectType != "dashboard_page" {
			t.Fatalf("dashboard page event = %#v", event)
		}
		operations[event.Operation] = true
	}
	if !operations[dataquery.OperationDashboardAggregate] || !operations[dataquery.OperationDashboardRows] {
		t.Fatalf("dashboard page query operations = %#v", operations)
	}
}

func TestDashboardTableWindowWritesQueryEvents(t *testing.T) {
	server := assembleRuntime(auditedDashboardMetrics{fakeMetrics: fakeMetrics{}}, testStoreOptions(testStore(t), assemblyConfig{}))
	req := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/visuals/order_rows/query", strings.NewReader(`{"limit":10}`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer dev")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req_dashboard_table")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	events := queryEventsForTest(t, server, queryaudit.Filter{ProjectID: projectgraph.ResourceID("project:test"), Search: "req_dashboard_table"})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: %#v", len(events), events)
	}
	if events[0].Surface != dataquery.SurfaceAPI || events[0].Operation != dataquery.OperationDashboardRows || events[0].ObjectType != "dashboard_visual" {
		t.Fatalf("dashboard table event = %#v", events[0])
	}
}

func TestBIAPIDashboardVisualDataSurfaceNotFoundAndMalformedBody(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/v1/dashboards/executive-sales/pages/overview/visuals/missing"},
		{method: http.MethodPost, path: "/api/v1/dashboards/executive-sales/pages/overview/visuals/missing/query"},
		{method: http.MethodPost, path: "/api/v1/dashboards/executive-sales/pages/overview/visuals/missing/query"},
		{method: http.MethodPost, path: "/api/v1/dashboards/executive-sales/pages/overview/filters/missing/values"},
	} {
		req := newPublicAPIRequest(tc.method, tc.path, strings.NewReader(`{}`))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	req := newPublicAPIRequest(http.MethodPost, "/api/v1/dashboards/executive-sales/pages/overview/visuals/orders/query", strings.NewReader(`{"filterState":`))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBIAPISemanticDatasetSurface(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	for _, tc := range []struct {
		method string
		path   string
		body   string
		want   []string
	}{
		{
			method: http.MethodGet,
			path:   "/api/v1/semantic-models/test/fields",
			want:   []string{`"kind":"measure"`, `"name":"order_count"`},
		},
		{
			method: http.MethodPost,
			path:   "/api/v1/semantic-models/test/query",
			body:   `{"dimensions":[{"field":"orders.status","alias":"status"}],"measures":[{"field":"order_count"}],"sort":[{"field":"status","direction":"asc"}]}`,
			want:   []string{`"columns"`, `"rows"`, `"delivered"`},
		},
		{
			method: http.MethodPost,
			path:   "/api/v1/semantic-models/test/query/explain",
			body:   `{"measures":[{"field":"order_count"}]}`,
			want:   []string{`"mode":"single_fact"`, `"facts":["orders"]`, `"physicalDependencies"`},
		},
		{
			method: http.MethodGet,
			path:   "/api/v1/semantic-models/test/datasets?limit=1",
			want:   []string{`"items"`, `"id":"orders"`, `"page"`},
		},
		{
			method: http.MethodGet,
			path:   "/api/v1/semantic-models/test/datasets/orders",
			want:   []string{`"primaryKey":"order_id"`, `"grain":"order_id"`},
		},
		{
			method: http.MethodGet,
			path:   "/api/v1/semantic-models/test/datasets/orders/fields?limit=4",
			want:   []string{`"kind":"dimension"`, `"kind":"measure"`, `"order_count"`},
		},
		{
			method: http.MethodPost,
			path:   "/api/v1/semantic-models/test/datasets/orders/preview",
			body:   `{"dimensions":[{"field":"orders.order_id"},{"field":"orders.status"}],"sort":[{"field":"order_id","direction":"asc"}],"limit":1}`,
			want:   []string{`"order_id"`, `"o1"`, `"nextCursor"`},
		},
		{
			method: http.MethodPost,
			path:   "/api/v1/semantic-models/test/datasets/orders/preview/explain",
			body:   `{"dimensions":[{"field":"orders.order_id"}],"sort":[{"field":"order_id","direction":"asc"}]}`,
			want:   []string{`"mode":"preview"`, `"sql"`, `"columns"`},
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			body := strings.NewReader(tc.body)
			if tc.body == "" {
				body = strings.NewReader(`{}`)
			}
			req := servingSnapshotRequest(t, server, newPublicAPIRequest(tc.method, tc.path, body))
			req.Header.Set("Accept", "application/json")
			if tc.method == http.MethodPost {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			server.Routes().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(rec.Body.String(), want) {
					t.Fatalf("body missing %q: %s", want, rec.Body.String())
				}
			}
		})
	}
}

func TestBIAPISemanticDatasetErrors(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(testStore(t), assemblyConfig{}))

	for _, tc := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{method: http.MethodGet, path: "/api/v1/semantic-models/test/datasets/missing", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/semantic-models/test/query", body: `{"dimensions":[{"field":"missing.field"}]}`, status: http.StatusBadRequest},
		{method: http.MethodPost, path: "/api/v1/semantic-models/test/query", body: `{"dimensions":[{"field":"orders.status"}],"sort":[{"field":"missing"}]}`, status: http.StatusBadRequest},
		{method: http.MethodPost, path: "/api/v1/semantic-models/test/query", body: `{"dimensions":`, status: http.StatusBadRequest},
	} {
		req := newPublicAPIRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Accept", "application/json")
		if tc.method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", tc.method, tc.path, rec.Code, tc.status, rec.Body.String())
		}
	}
}

type manyRowsMetrics struct {
	fakeMetrics
}

type auditedDashboardMetrics struct {
	fakeMetrics
}

func (m auditedDashboardMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	_, err := m.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: projectgraph.ResourceID("project:test"),
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardAggregate,
		ModelID:   "test",
		Kind:      dataquery.KindSemanticAggregate,
		Target:    "orders",
		Fields:    []dataquery.Field{{Field: "orders.status", Alias: "status"}},
		Measures:  []dataquery.Field{{Field: "order_count"}},
		Limit:     10,
	})
	if err != nil {
		return dashboard.Patch{}, err
	}
	patch, err := m.fakeMetrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
	if err != nil {
		return dashboard.Patch{}, err
	}
	request := dashboard.TableRequest{Table: "order_rows", Block: "a", Count: dashboard.TableChunkSize}.WithDefaults()
	table, err := m.queryWindow(ctx, dashboardID, pageID, filters, request)
	if err != nil {
		return dashboard.Patch{}, err
	}
	definition, _ := m.visualizationDefinition(dashboardID, "order_rows")
	envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 0, 0)
	if err != nil {
		return dashboard.Patch{}, err
	}
	patch.Visuals["order_rows"] = envelope
	return patch, nil
}

func (m auditedDashboardMetrics) queryWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	_, err := m.ExecuteDataQuery(ctx, dataquery.Query{
		ProjectID: projectgraph.ResourceID("project:test"),
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardRows,
		ModelID:   "test",
		Kind:      dataquery.KindSemanticRows,
		Target:    "orders",
		Fields:    []dataquery.Field{{Field: "orders.order_id", Alias: "order_id"}},
		Limit:     request.Count,
	})
	if err != nil {
		return dashboard.Table{}, err
	}
	return m.fakeMetrics.queryWindow(ctx, dashboardID, pageID, filters, request)
}

func (m auditedDashboardMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return fakeVisualizationWindow(ctx, m, dashboardID, pageID, filters, request)
}

func (m auditedDashboardMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return dataquery.ExecuteAudited(ctx, request, m.fakeMetrics.ExecuteDataQuery)
}

func (manyRowsMetrics) queryWindow(_ context.Context, _ string, _ string, _ dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	const totalRows = 500
	count := min(request.Count, max(0, totalRows-request.Start))
	rows := make([]map[string]any, 0, count)
	for i := request.Start; i < request.Start+count; i++ {
		rows = append(rows, map[string]any{"order_id": fmt.Sprintf("order-%d", i)})
	}
	return dashboard.Table{
		Title:         "Orders",
		Columns:       []dashboard.TableColumn{{Key: "order_id", Label: "Order"}},
		Cardinality:   dashboard.ExactCardinality(totalRows),
		AvailableRows: totalRows,
		RowCap:        dashboard.TableInteractiveRowCap,
		ChunkSize:     dashboard.TableChunkSize,
		Sort:          dashboard.TableSort{Key: "order_id", Direction: "desc"},
		Blocks:        map[string]dashboard.TableBlock{"a": {Rows: rows}},
	}, nil
}

func (m manyRowsMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return fakeVisualizationWindow(ctx, m, dashboardID, pageID, filters, request)
}
