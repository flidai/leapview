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
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/project/graph"
)

type canonicalDataRuntime struct {
	rows       []dataquery.Row
	failTarget string
	queries    *[]dataquery.Query
	planner    *semanticquery.Planner
}

type canonicalVerifyingRuntime struct {
	canonicalDataRuntime
	verifyErr error
	verifies  int
}

func (r *canonicalVerifyingRuntime) VerifySemantic(context.Context) error {
	r.verifies++
	return r.verifyErr
}
func (r *canonicalDataRuntime) reportRows() report.QueryRows {
	rows := make(report.QueryRows, len(r.rows))
	for index, row := range r.rows {
		rows[index] = report.QueryRow(row)
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
	if query.Kind == dataquery.KindModelRows || query.Kind == dataquery.KindSemanticRows {
		result.TotalRows = len(r.rows)
		result.TotalRowsKnown = true
	}
	return result, nil
}
func (r *canonicalDataRuntime) Refresh(context.Context) error       { return nil }
func (r *canonicalDataRuntime) Close() error                        { return nil }
func (r *canonicalDataRuntime) LastRefresh() time.Time              { return time.Now() }
func (r *canonicalDataRuntime) setPlanner(p *semanticquery.Planner) { r.planner = p }
func (r *canonicalDataRuntime) Planner() consumer.Planner           { return r.planner }

type canonicalFactory struct {
	runtime DataRuntime
	err     error
}

func (f canonicalFactory) OpenDashboardProjectDataRuntimes(_ context.Context, config ProjectDataRuntimeConfig) (map[graph.ResourceID]DataRuntime, error) {
	if f.err != nil {
		return nil, f.err
	}
	if setter, ok := f.runtime.(interface{ setPlanner(*semanticquery.Planner) }); ok && config.Definition != nil {
		model := config.Definition.Models()[graph.ResourceID("model_1")]
		if model != nil {
			for alias, dataset := range model.Datasets {
				table := model.Tables[alias]
				table.ModelName = dataset.Model
				model.Tables[alias] = table
			}
			planner, err := semanticquery.NewCompiledPlanner(model)
			if err != nil {
				return nil, err
			}
			setter.setPlanner(planner)
		}
	}
	return map[graph.ResourceID]DataRuntime{"model_1": f.runtime}, nil
}

func canonicalBase(kind, title string, fields []visualizationir.VisualizationField) visualizationir.VisualizationSpecBase {
	return visualizationir.VisualizationSpecBase{Kind: kind, Title: title, Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: title}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 100}}
}

func canonicalCartesian(t *testing.T, id string) visualizationdefinition.Definition {
	fields := []visualizationir.VisualizationField{{ID: "status", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Status"}, {ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}
	base := canonicalBase("cartesian", id, fields)
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkBar, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "status"}, Y: []visualizationir.VisualizationFieldRef{{Dataset: "primary", Field: "value"}}, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, MaxCharacters: 24, TooltipFallback: true}}}}}
	definition, err := visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultCategoryValue, ModelID: "model_1", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Dimensions: []visualizationdefinition.FieldBinding{{FieldID: "status", Alias: "label"}}, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func canonicalTable(t *testing.T) visualizationdefinition.Definition {
	fields := []visualizationir.VisualizationField{{ID: "order_id", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Order"}, {ID: "status", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Status"}}
	base := canonicalBase("table", "Orders", fields)
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{VisualizationSpecBase: base, Kind: "table", Columns: []visualizationir.TableVisualizationColumn{{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "order_id"}, Label: "Order", Formatting: []visualizationir.TableVisualizationFormattingRule{}}, {Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "status"}, Label: "Status", Formatting: []visualizationir.TableVisualizationFormattingRule{}}}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true}}}
	definition, err := visualizationdefinition.New("orders", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow, ModelID: "model_1", DatasetID: "primary", Detail: &visualizationdefinition.DetailQueryBinding{TableID: "orders", Fields: []visualizationdefinition.FieldBinding{{FieldID: "order_id", Alias: "order_id"}, {FieldID: "status", Alias: "status"}}, Limit: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

func canonicalBehaviorDefinition(t *testing.T, withTable bool) (*ProjectDefinition, dashboarddefinition.Definition) {
	model := &semanticmodel.Model{Name: "model_1", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order_id", Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString}, "order_id": {Field: "orders.order_id", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"status": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}}, "order_id": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.order_id"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero"}}}
	good := canonicalCartesian(t, "good")
	broken := canonicalCartesian(t, "broken")
	visuals := map[string]visualizationdefinition.Definition{"good": good, "broken": broken}
	pageVisuals := []dashboard.PageVisual{{ID: "good", Kind: "visual", Visual: "good", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}, {ID: "broken", Kind: "visual", Visual: "broken", Placement: dashboard.PagePlacement{Col: 5, Row: 1, ColSpan: 4, RowSpan: 4}}}
	if withTable {
		table := canonicalTable(t)
		visuals["orders"] = table
		pageVisuals = append(pageVisuals, dashboard.PageVisual{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 5, ColSpan: 8, RowSpan: 4}})
	}
	compiled := dashboarddefinition.Definition{ID: "dashboard_1", Title: "Dashboard", SemanticModel: "model_1", Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: pageVisuals}}, Visualizations: visuals}
	definition, err := NewProjectDefinition("project_1", "Project", "", map[graph.ResourceID]*semanticmodel.Model{"model_1": model}, map[graph.ResourceID]dashboarddefinition.Definition{"dashboard_1": compiled})
	if err != nil {
		t.Fatal(err)
	}
	return definition, compiled
}

func canonicalBehaviorRuntime(t *testing.T, definition *ProjectDefinition, data DataRuntime, factoryErr error) *Service {
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
	service := canonicalBehaviorRuntime(t, definition, nil, &materializeruntime.MissingDataError{Missing: []string{"orders.csv"}})
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

func TestCanonicalServiceVerifyRunsGovernedSemanticVerifier(t *testing.T) {
	definition, _ := canonicalBehaviorDefinition(t, false)
	data := &canonicalVerifyingRuntime{canonicalDataRuntime: canonicalDataRuntime{rows: []dataquery.Row{{"status": "A", "value": int64(1)}}}}
	service := canonicalBehaviorRuntime(t, definition, data, nil)
	defer service.Close()
	if err := service.Verify(context.Background()); err != nil || data.verifies != 1 {
		t.Fatalf("verify error=%v calls=%d", err, data.verifies)
	}
}

func TestCanonicalServiceVerifyFailsClosedOnSemanticVerifierError(t *testing.T) {
	definition, _ := canonicalBehaviorDefinition(t, false)
	data := &canonicalVerifyingRuntime{canonicalDataRuntime: canonicalDataRuntime{rows: []dataquery.Row{{"status": "A", "value": int64(1)}}}, verifyErr: errors.New("representative plan failed")}
	service := canonicalBehaviorRuntime(t, definition, data, nil)
	defer service.Close()
	if err := service.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "representative plan failed") {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestCanonicalPageQueryIsolatesVisualizationFailure(t *testing.T) {
	definition, compiled := canonicalBehaviorDefinition(t, false)
	compiled.Visualizations["broken"].Query.Aggregate.TableID = "missing_table"
	definition, err := NewProjectDefinition("project_1", "Project", "", definition.Models(), map[graph.ResourceID]dashboarddefinition.Definition{"dashboard_1": compiled})
	if err != nil {
		t.Fatal(err)
	}
	service := canonicalBehaviorRuntime(t, definition, &canonicalDataRuntime{rows: []dataquery.Row{{"status": "A", "value": int64(1)}}, failTarget: "missing_table"}, nil)
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
	service := canonicalBehaviorRuntime(t, definition, &canonicalDataRuntime{rows: []dataquery.Row{{"status": "A", "value": int64(1)}}, queries: &queries}, nil)
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
	for index := range rows {
		rows[index] = dataquery.Row{"order_id": fmt.Sprintf("o%d", index), "status": "delivered"}
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
	definition := dashboardfilter.Definition{Field: "orders.category", Dataset: "orders"}
	contains, err := semanticFiltersForExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionComparison, Operator: dashboardfilter.OperatorContains, Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: "watch"}})
	if err != nil || len(contains) != 1 || contains[0].Operator != string(dashboardfilter.OperatorContains) || contains[0].Values[0] != "watch" {
		t.Fatalf("contains filter = %#v, err=%v", contains, err)
	}
	rangeFilters, err := semanticFiltersForExpression(definition, dashboardfilter.Expression{Kind: dashboardfilter.ExpressionRange, Lower: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "10"}, Inclusive: true}, Upper: &dashboardfilter.Bound{Value: dashboardfilter.Value{Kind: dashboardfilter.ValueDecimal, Value: "20"}, Inclusive: false}})
	if err != nil || len(rangeFilters) != 2 || rangeFilters[0].Operator != string(dashboardfilter.OperatorGreaterThanOrEqual) || rangeFilters[1].Operator != string(dashboardfilter.OperatorLessThan) {
		t.Fatalf("range filters = %#v, err=%v", rangeFilters, err)
	}
}
