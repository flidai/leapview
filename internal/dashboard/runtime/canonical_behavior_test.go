package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	materializeruntime "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/report"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
)

// canonicalDataRuntime is deliberately small: it exercises the runtime's
// governed data-query boundary without opening a second analytics engine.
type canonicalDataRuntime struct {
	rows       []dataquery.Row
	failTarget string
	queries    *[]dataquery.Query
}

func (r *canonicalDataRuntime) reportRows() report.QueryRows {
	rows := make(report.QueryRows, len(r.rows))
	for i, row := range r.rows {
		rows[i] = report.QueryRow(row)
	}
	return rows
}

func (r *canonicalDataRuntime) Query(context.Context, report.AggregateQuery) (report.QueryRows, error) {
	return r.reportRows(), nil
}
func (r *canonicalDataRuntime) Rows(context.Context, report.RowQuery) (report.QueryRows, error) {
	return r.reportRows(), nil
}
func (r *canonicalDataRuntime) Count(context.Context, report.CountQuery) (int, error) {
	return len(r.rows), nil
}
func (r *canonicalDataRuntime) Histogram(context.Context, report.RawValueQuery, int) ([]report.HistogramBin, error) {
	return nil, nil
}
func (r *canonicalDataRuntime) Distribution(context.Context, report.RawValueQuery, []report.QuerySort, int) (report.QueryRows, error) {
	return r.reportRows(), nil
}
func (r *canonicalDataRuntime) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	if r.queries != nil {
		*r.queries = append(*r.queries, query)
	}
	if r.failTarget != "" && query.Target == r.failTarget {
		return dataquery.Result{}, fmt.Errorf("target %s failed", r.failTarget)
	}
	result := dataquery.Result{Rows: append([]dataquery.Row(nil), r.rows...), RowsReturned: len(r.rows), PlanningMS: 1, DatabaseMS: 1, ExecutionMS: 1, Status: dataquery.StatusSuccess, ExecutionState: dataquery.ExecutionSucceeded}
	if query.Kind == dataquery.KindModelTableRows || query.Kind == dataquery.KindSemanticRows {
		result.TotalRows = len(r.rows)
		result.TotalRowsKnown = true
	}
	return result, nil
}
func (r *canonicalDataRuntime) Refresh(context.Context) error { return nil }
func (r *canonicalDataRuntime) Close() error                  { return nil }
func (r *canonicalDataRuntime) LastRefresh() time.Time        { return time.Now() }

type canonicalFactory struct {
	runtime DataRuntime
	err     error
}

func (f canonicalFactory) OpenDashboardProjectDataRuntimes(context.Context, ProjectDataRuntimeConfig) (map[graph.ResourceID]DataRuntime, error) {
	if f.err != nil {
		return nil, f.err
	}
	return map[graph.ResourceID]DataRuntime{"model_1": f.runtime}, nil
}

func canonicalBehaviorDefinition(t *testing.T, withTable bool) (*ProjectDefinition, dashboarddefinition.Definition) {
	t.Helper()
	model := &semanticmodel.Model{
		Name: "model_1",
		Tables: map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{
			"status":   {Field: "orders.status", Type: "string"},
			"order_id": {Field: "orders.order_id", Type: "string"},
		}}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"status":   {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
			"order_id": {Type: "string", Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.order_id"}}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero"}},
	}
	visuals := dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
		"good": {Type: "bar", Title: "Good", Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "status", Alias: "label"}}, Measures: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}},
		}},
		"broken": {Type: "bar", Title: "Broken", Query: dashboardauthoring.VisualQuery{
			Dimensions: []dashboardauthoring.FieldRef{{Field: "status", Alias: "label"}}, Measures: []dashboardauthoring.FieldRef{{Field: "order_count", Alias: "value"}},
		}},
	})
	if withTable {
		visuals = dashboardauthoring.MergeVisualizations(visuals, dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"orders": {Title: "Orders", Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.order_id", "orders.status"}}},
		}))
	}
	authored := dashboardauthoring.Dashboard{ID: "dashboard_1", Title: "Dashboard", SemanticModel: "model_1", Visuals: visuals,
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{
			{ID: "good", Kind: "visual", Visual: "good", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}},
			{ID: "broken", Kind: "visual", Visual: "broken", Placement: dashboard.PagePlacement{Col: 5, Row: 1, ColSpan: 4, RowSpan: 4}},
		}}}}
	if withTable {
		authored.Pages[0].Visuals = append(authored.Pages[0].Visuals, dashboard.PageVisual{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 5, ColSpan: 8, RowSpan: 4}})
	}
	compiled := dashboardfixture.Compile(authored, model)
	definition, err := NewProjectDefinition("project_1", "Project", "", map[graph.ResourceID]*semanticmodel.Model{"model_1": model}, map[graph.ResourceID]dashboarddefinition.Definition{"dashboard_1": compiled})
	if err != nil {
		t.Fatal(err)
	}
	return definition, compiled
}

func canonicalBehaviorRuntime(t *testing.T, definition *ProjectDefinition, data DataRuntime, factoryErr error) *Service {
	t.Helper()
	identity, err := graph.NewServingIdentity("project_1", "test", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFromGeneration(context.Background(), t.TempDir(), canonicalFactory{runtime: data, err: factoryErr}, identity, definition)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestCanonicalMissingDataReturnsSetupPatch(t *testing.T) {
	definition, _ := canonicalBehaviorDefinition(t, false)
	missing := &materializeruntime.MissingDataError{Missing: []string{"orders.csv"}}
	service := canonicalBehaviorRuntime(t, definition, nil, missing)
	defer service.Close()
	patch, err := service.QueryDashboardPage(context.Background(), "dashboard_1", "overview", dashboard.Filters{})
	if err != nil || !patch.Status.SetupRequired || !strings.Contains(patch.Status.Error, "orders.csv") {
		t.Fatalf("patch=%#v err=%v", patch, err)
	}
	var typed *materializeruntime.MissingDataError
	if !errors.As(service.runtimes[graph.ResourceID("model_1")].missing, &typed) {
		t.Fatalf("missing error type = %T", service.runtimes[graph.ResourceID("model_1")].missing)
	}
	if err := service.Verify(context.Background()); err == nil {
		t.Fatal("runtime verification accepted missing data")
	}
}

func TestCanonicalPageQueryIsolatesVisualizationFailure(t *testing.T) {
	definition, compiled := canonicalBehaviorDefinition(t, false)
	compiled.Visualizations["broken"].Query.Aggregate.TableID = "missing_table"
	definition, err := NewProjectDefinition("project_1", "Project", "", definition.Models(), map[graph.ResourceID]dashboarddefinition.Definition{"dashboard_1": compiled})
	if err != nil {
		t.Fatal(err)
	}
	service := canonicalBehaviorRuntime(t, definition, &canonicalDataRuntime{rows: []dataquery.Row{{"label": "A", "value": int64(1)}}, failTarget: "missing_table"}, nil)
	defer service.Close()
	patch, err := service.QueryDashboardPage(context.Background(), "dashboard_1", "overview", dashboard.Filters{})
	if err != nil || patch.Status.Error != "" {
		t.Fatalf("patch=%#v err=%v", patch, err)
	}
	if patch.Visuals["good"].Status.Kind != visualizationir.VisualizationStatusKindReady {
		t.Fatalf("good visual status = %#v", patch.Visuals["good"].Status)
	}
	failed := patch.Visuals["broken"]
	if failed.Status.Kind != visualizationir.VisualizationStatusKindError || len(failed.Diagnostics) != 1 || failed.Diagnostics[0].Code != "query_failed" {
		t.Fatalf("broken visual = %#v", failed)
	}
}

func TestCanonicalQueriesFlowThroughAuditedDataQueryBoundary(t *testing.T) {
	definition, _ := canonicalBehaviorDefinition(t, false)
	var queries []dataquery.Query
	service := canonicalBehaviorRuntime(t, definition, &canonicalDataRuntime{rows: []dataquery.Row{{"label": "A", "value": int64(1)}}, queries: &queries}, nil)
	defer service.Close()
	recorder := &runtimeAuditRecorder{}
	ctx := dataquery.WithAuditRecorder(context.Background(), recorder)
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{ProjectID: "project_1", PrincipalID: "principal_1"})
	patch, err := service.QueryDashboardPage(ctx, "dashboard_1", "overview", dashboard.Filters{})
	if err != nil || patch.Status.Error != "" || len(recorder.queries) == 0 {
		t.Fatalf("patch=%#v err=%v audit=%#v", patch, err, recorder)
	}
	for _, query := range recorder.queries {
		if query.ProjectID != "project_1" || query.Surface != dataquery.SurfaceDashboard || query.PrincipalID != "principal_1" {
			t.Fatalf("query identity = %#v", query)
		}
	}
	for _, result := range recorder.results {
		if result.PlanningMS <= 0 || result.DatabaseMS <= 0 || result.ExecutionMS <= 0 {
			t.Fatalf("query timings = %#v", result)
		}
	}
}

func TestCanonicalTableRowsRespectInteractiveCap(t *testing.T) {
	definition, _ := canonicalBehaviorDefinition(t, true)
	rows := make([]dataquery.Row, dashboard.TableInteractiveRowCap+5)
	for i := range rows {
		rows[i] = dataquery.Row{"order_id": fmt.Sprintf("o%d", i), "status": "delivered"}
	}
	service := canonicalBehaviorRuntime(t, definition, &canonicalDataRuntime{rows: rows}, nil)
	defer service.Close()
	table, err := service.queries.visualizations.queryTablePage(context.Background(), "dashboard_1", "overview", dashboard.Filters{}, dashboard.TableRequest{Table: "orders", Block: "all", RequestSeq: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !table.IsCapped || table.AvailableRows != dashboard.TableInteractiveRowCap || table.RowCap != dashboard.TableInteractiveRowCap {
		t.Fatalf("table cap = %#v", table)
	}
}

func TestCanonicalPowerFiltersTranslateComparisonAndRangePredicates(t *testing.T) {
	definition := dashboardfilter.Definition{Field: "orders.category", Fact: "orders"}
	contains, err := semanticFiltersForExpression(definition, dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorContains,
		Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "watch"},
	})
	if err != nil || len(contains) != 1 || contains[0].Operator != string(dashboardfilter.OperatorContains) || contains[0].Values[0] != "watch" {
		t.Fatalf("contains filter = %#v, err=%v", contains, err)
	}
	rangeFilters, err := semanticFiltersForExpression(definition, dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionRange,
		Lower: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "10"}, Inclusive: true},
		Upper: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "20"}, Inclusive: false},
	})
	if err != nil || len(rangeFilters) != 2 || rangeFilters[0].Operator != string(dashboardfilter.OperatorGreaterThanOrEqual) || rangeFilters[1].Operator != string(dashboardfilter.OperatorLessThan) {
		t.Fatalf("range filters = %#v, err=%v", rangeFilters, err)
	}
}
