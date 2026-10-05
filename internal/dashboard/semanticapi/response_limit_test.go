package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/api"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type boundedResponseMetrics struct {
	semanticProjectionMetrics
	requests []dataquery.Query
	rowCount int
}

func (m *boundedResponseMetrics) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	m.requests = append(m.requests, query)
	rows := make([]dataquery.Row, m.rowCount)
	for i := range rows {
		rows[i] = dataquery.Row{"id": fmt.Sprint(i), "orders.id": fmt.Sprint(i)}
	}
	return dataquery.Result{Rows: rows}, nil
}

func TestQueryAndPreviewCallersBoundResponseAndPaginationProbe(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {
		ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
		Dimensions: map[string]semanticmodel.MetricDimension{"id": {Field: "orders.id", Table: "orders", Name: "id", Type: "string", Datatype: semanticmodel.DataTypeString}},
	}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Dimensions: map[string]semanticmodel.SemanticDimension{"id": {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.id"}}}}}
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		raw   string
		limit int
	}{{"", 100}, {"0", 100}, {"-1", 100}, {"1", 1}, {"1000", 1000}, {"1001", 1000}, {strconv.Itoa(int(^uint(0) >> 1)), 1000}}
	for _, preview := range []bool{false, true} {
		for _, test := range cases {
			for _, count := range []int{0, test.limit, test.limit + 1} {
				t.Run(fmt.Sprintf("preview=%v/limit=%s/rows=%d", preview, test.raw, count), func(t *testing.T) {
					metrics := &boundedResponseMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model, planner: planner, plannerOkay: true}, rowCount: count}
					handler := Handler{Metrics: metrics, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }, AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
						return true, nil
					}}
					body := `{"dimensions":[{"field":"id"}]`
					if test.raw != "" {
						body += `,"limit":` + test.raw
					}
					body += "}"
					response := invokeBoundedQuery(handler, preview, body)
					if response.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					if len(metrics.requests) != 1 || metrics.requests[0].Limit != test.limit+1 {
						t.Fatalf("physical requests=%+v", metrics.requests)
					}
					var result api.SemanticQueryResponse
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Rows) != min(count, test.limit) || result.Completeness.ReturnedRows != len(result.Rows) || result.Completeness.HasMore != (count > test.limit) || (result.Page.NextCursor != "") != (count > test.limit) {
						t.Fatalf("rows=%d completeness=%+v page=%+v", len(result.Rows), result.Completeness, result.Page)
					}
				})
			}
		}
		for _, invalid := range []string{`"bad"`, `1.5`, `9223372036854775808`} {
			metrics := &boundedResponseMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model, planner: planner, plannerOkay: true}}
			handler := Handler{Metrics: metrics, ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }}
			response := invokeBoundedQuery(handler, preview, `{"limit":`+invalid+`}`)
			if response.Code != http.StatusBadRequest || len(metrics.requests) != 0 {
				t.Fatalf("invalid limit %s reached execution: status=%d requests=%v", invalid, response.Code, metrics.requests)
			}
		}
	}
}

func invokeBoundedQuery(handler Handler, preview bool, body string) *httptest.ResponseRecorder {
	route := chi.NewRouteContext()
	route.URLParams.Add("model", "sales")
	route.URLParams.Add("dataset", "orders")
	request := httptest.NewRequest(http.MethodPost, "/semantic-models/sales/query", strings.NewReader(body))
	request.Header.Set("X-Request-ID", "query-1")
	request.Header.Set("X-Serving-Snapshot", "snapshot-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	if preview {
		handler.PreviewSemanticDataset(response, request)
	} else {
		handler.QuerySemanticModel(response, request)
	}
	return response
}
