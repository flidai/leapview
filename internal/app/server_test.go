package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	materializesqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func fieldRefs(fields ...string) []dashboardauthoring.FieldRef {
	refs := make([]dashboardauthoring.FieldRef, len(fields))
	for i, field := range fields {
		refs[i] = dashboardauthoring.FieldRef{Field: field}
	}
	return refs
}

func typedSetURLValue(t *testing.T, values ...string) string {
	t.Helper()
	typed := make([]dashboardfilter.Value, len(values))
	for index, value := range values {
		typed[index] = dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value}
	}
	encoded, err := dashboardfilter.EncodeTypedV1(dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: typed,
	}, dashboardfilter.ValueString)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func typedComparisonURLValue(t *testing.T, operator dashboardfilter.Operator, value string) string {
	t.Helper()
	encoded, err := dashboardfilter.EncodeTypedV1(dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionComparison, Operator: operator,
		Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value},
	}, dashboardfilter.ValueString)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type fakeMetrics struct{}

func (m fakeMetrics) Resolver() dashboardresolver.Resolver {
	return fixtureResolver{provider: m, workspaceID: "test-workspace"}
}

// dashboardDefinitionProvider keeps the test fixture independent from the
// runtime resolver's project adapter contract.
type dashboardDefinitionProvider interface {
	dashboardDefinition(string) (dashboarddefinition.Definition, *semanticmodel.Model, bool)
}

type fixtureResolver struct {
	provider    dashboardDefinitionProvider
	workspaceID string
}

func (r fixtureResolver) Resolve(dashboardID string) (dashboardresolver.Resolved, error) {
	definition, model, ok := r.provider.dashboardDefinition(dashboardID)
	if !ok {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{
		Definition: definition,
		Model:      model,
		Source: dashboardresolver.SourceMetadata{
			Kind:        dashboardresolver.SourceProject,
			WorkspaceID: r.workspaceID,
		},
	}, nil
}

func (fakeMetrics) QueryCompiledFilterOptions(_ context.Context, _ string, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	return dashboardfilter.OptionResult{Items: []dashboardfilter.OptionItem{
		{Value: dashboardfilter.Value{Kind: query.ValueKind, Value: "SP"}, Label: "SP", Available: true},
		{Value: dashboardfilter.Value{Kind: query.ValueKind, Value: "RJ"}, Label: "RJ", Available: true},
	}}, nil
}

func (fakeMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	for _, target := range request.Targets {
		switch target.Kind {
		case consumer.KindVisual:
			definition, _ := fakeMetrics{}.visualizationDefinition(request.DashboardID, target.ID)
			envelope, err := visualizationruntime.EnvelopeFromFrame(definition, visualizationruntime.Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", 1}}}, nil, 0, 0)
			publish(consumer.Result{Target: target, Envelope: envelope, Err: err})
		case consumer.KindWindow:
			table, err := fakeMetrics{}.queryWindow(ctx, request.DashboardID, request.PageID, request.Filters, target.WindowRequest)
			definition, _ := fakeMetrics{}.visualizationDefinition(request.DashboardID, target.ID)
			envelope, envelopeErr := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 0, 0)
			publish(consumer.Result{Target: target, Envelope: envelope, Err: errors.Join(err, envelopeErr)})
		}
	}
	return ctx.Err()
}

type canceledTableMetrics struct {
	fakeMetrics
}

type recordingMetrics struct {
	fakeMetrics
	pageIDs chan string
}

func (fakeMetrics) QueryDashboardVisualizations(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	return fakeMetrics{}.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func (fakeMetrics) QueryVisualization(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	patch, err := fakeMetrics{}.QueryDashboardPage(ctx, dashboardID, pageID, filters)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return patch.Visuals[visualID], nil
}

func (fakeMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return fakeVisualizationWindow(ctx, fakeMetrics{}, dashboardID, pageID, filters, request)
}

type fakeWindowSource interface {
	queryWindow(context.Context, string, string, dashboard.Filters, dashboard.TableRequest) (dashboard.Table, error)
	visualizationDefinition(string, string) (visualizationdefinition.Definition, bool)
}

func fakeVisualizationWindow(ctx context.Context, source fakeWindowSource, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	tableRequest := dashboard.TableRequest{Table: request.VisualID, Block: request.BlockID, Start: int(request.Start), Count: int(request.Limit), RequestSeq: int(request.RequestSeq), ResetVersion: int(request.ResetVersion)}
	if len(request.Sort) > 0 {
		tableRequest.Sort.Key = request.Sort[0].Field.Field
		if request.Sort[0].Direction == visualizationir.VisualizationSortDirectionDescending {
			tableRequest.Sort.Direction = "desc"
		} else {
			tableRequest.Sort.Direction = "asc"
		}
	}
	table, err := source.queryWindow(ctx, dashboardID, pageID, filters, tableRequest)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	definition, _ := source.visualizationDefinition(dashboardID, request.VisualID)
	return visualizationruntime.WindowEnvelopeFromDefinition(definition, table, request.DataRevision, 0)
}

func (m *recordingMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	for range request.Targets {
		m.pageIDs <- request.PageID
	}
	return m.fakeMetrics.ExecuteConsumersPage(ctx, request, publish)
}

type namedWorkspaceMetrics struct {
	fakeMetrics
	workspaceID string
	dashboardID string
	title       string
}

func (m namedWorkspaceMetrics) Resolver() dashboardresolver.Resolver {
	return fixtureResolver{provider: m, workspaceID: m.workspaceID}
}

func (m namedWorkspaceMetrics) Catalog() dashboard.Catalog {
	return dashboard.Catalog{
		Workspace: dashboard.CatalogWorkspace{ID: m.workspaceID, Title: m.workspaceID},
		Models:    []dashboard.CatalogModel{{ID: "test", Title: "Test Model"}},
		Dashboards: []dashboard.CatalogDashboard{{
			ID:            m.dashboardID,
			Title:         m.title,
			SemanticModel: "test",
			PageCount:     1,
		}},
	}
}

func (m namedWorkspaceMetrics) DefaultDashboardID() string {
	return m.dashboardID
}

func (m namedWorkspaceMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != m.dashboardID {
		return nil
	}
	return []dashboard.Page{{
		ID: "overview", Title: "Overview",
		Visuals: []dashboard.PageVisual{{
			ID: "summary", Kind: "visual", Visual: "summary",
			Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 4},
		}},
	}}
}

func (m namedWorkspaceMetrics) dashboardDefinition(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if dashboardID != m.dashboardID {
		return dashboarddefinition.Definition{}, nil, false
	}
	authored := dashboardauthoring.Dashboard{
		ID:            m.dashboardID,
		Title:         m.title,
		SemanticModel: "test",
		Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"summary": {Type: "kpi", Title: "Summary", Query: dashboardauthoring.VisualQuery{Measures: fieldRefs("order_count")}},
		}),
		Pages: m.Pages(dashboardID),
	}
	model := &semanticmodel.Model{
		Name: "test", Title: "Test Model",
		Tables: map[string]semanticmodel.Table{
			"orders": {Source: "orders", PrimaryKey: "order_id", Grain: "order_id"},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero"},
		},
	}
	return dashboardfixture.Compile(authored, model), model, true
}

func (fakeMetrics) Catalog() dashboard.Catalog {
	return dashboard.Catalog{
		Workspace: dashboard.CatalogWorkspace{ID: "test-workspace", Title: "Test Workspace", Description: "Fixture workspace"},
		Models: []dashboard.CatalogModel{
			{ID: "test", Title: "Test Model", Description: "Fixture model"},
		},
		Dashboards: []dashboard.CatalogDashboard{
			{ID: "executive-sales", Title: "Executive Sales Dashboard", Description: "Fixture report", SemanticModel: "test", Tags: []string{"sales"}, PageCount: 2},
		},
	}
}

func (fakeMetrics) DefaultDashboardID() string {
	return "executive-sales"
}

func (fakeMetrics) ModelIDForDashboard(dashboardID string) string {
	if dashboardID == "executive-sales" {
		return "test"
	}
	return ""
}

func (fakeMetrics) dashboardDefinition(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if dashboardID != "executive-sales" {
		return dashboarddefinition.Definition{}, nil, false
	}
	authored := dashboardauthoring.Dashboard{
		ID:            "executive-sales",
		Title:         "Executive Sales Dashboard",
		SemanticModel: "test",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state": {
				Label: "State", Field: "orders.status", ValueKind: dashboardfilter.ValueString,
				Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}},
				Options:    dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceDistinct, Limit: 50},
			},
			"category": {
				Label: "Category", Field: "orders.status", ValueKind: dashboardfilter.ValueString,
				Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionComparison, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorContains, dashboardfilter.OperatorEquals}}},
			},
		},
		FilterApplication: dashboardfilter.ApplicationPolicy{Mode: dashboardfilter.ApplicationImmediate},
		Visuals: dashboardauthoring.MergeVisualizations(dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"orders":       {Title: "Orders", Type: "donut", Query: dashboardauthoring.VisualQuery{Dimensions: fieldRefs("orders.status"), Measures: fieldRefs("order_count")}, Interaction: pointInteraction("orders.status", "orders", "ops_pipeline")},
			"ops_pipeline": {Title: "Ops Pipeline", Type: "bar", Query: dashboardauthoring.VisualQuery{Dimensions: fieldRefs("orders.status"), Measures: fieldRefs("order_count")}, Interaction: pointInteraction("orders.status", "orders", "ops_pipeline")},
		}), dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"order_rows": {Title: "Orders", Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.order_id", "orders.revenue"}}, DefaultSort: dashboard.TableSort{Key: "order_id", Direction: "desc"}, Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order"}, {Key: "revenue", Label: "Revenue", Role: "measure", Format: "decimal"}}},
		})),
		Pages: fakeMetrics{}.Pages(dashboardID),
	}
	model := &semanticmodel.Model{
		Name:  "test",
		Title: "Test Model",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", PrimaryKey: "order_id", Grain: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Expr: "order_id", Type: "string"}, "status": {Expr: "status", Type: "string"}, "revenue": {Expr: "revenue", Type: "number"}},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero", Label: "Orders"}},
	}
	return dashboardfixture.Compile(authored, model), model, true
}

func (fakeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	_, model, ok := fakeMetrics{}.dashboardDefinition("executive-sales")
	if !ok || model.Name != modelID {
		return nil, false
	}
	return model, true
}

func (fakeMetrics) QuerySemantic(_ context.Context, _ string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	rows := reportdef.QueryRows{
		{"status": "delivered", "order_count": 42},
		{"status": "shipped", "order_count": 7},
	}
	return rows[:min(len(rows), request.Limit)], nil
}

func (fakeMetrics) PreviewSemantic(_ context.Context, _ string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	rows := reportdef.QueryRows{
		{"order_id": "o1", "status": "delivered"},
		{"order_id": "o2", "status": "shipped"},
	}
	return rows[:min(len(rows), request.Limit)], nil
}

func (fakeMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	switch request.Kind {
	case dataquery.KindSemanticAggregate:
		rows, err := fakeMetrics{}.QuerySemantic(ctx, request.ModelID, reportdef.AggregateQuery{
			Table:      request.Target,
			Dimensions: dataFieldsToReportFields(request.Fields),
			Measures:   dataFieldsToReportFields(request.Measures),
			Time:       dashboardauthoring.QueryTime{Field: request.Time.Field, Grain: request.Time.Grain, Alias: request.Time.Alias},
			Filters:    dataFiltersToReportFilters(request.Filters),
			Sort:       dataSortToReportSort(request.Sort),
			Limit:      request.Limit,
			Offset:     request.Offset,
		})
		return fakeDataQueryResult(rows, request.IncludeTotal), err
	case dataquery.KindSemanticRows:
		rows, err := fakeMetrics{}.PreviewSemantic(ctx, request.ModelID, reportdef.RowQuery{
			Table:      request.Target,
			Dimensions: dataFieldsToReportFields(request.Fields),
			Measures:   dataFieldsToReportFields(request.Measures),
			Filters:    dataFiltersToReportFilters(request.Filters),
			Sort:       dataSortToReportSort(request.Sort),
			Limit:      request.Limit,
			Offset:     request.Offset,
		})
		return fakeDataQueryResult(rows, request.IncludeTotal), err
	case dataquery.KindModelTableRows:
		return dataquery.Result{
			Columns:        dataquery.ColumnsFromNames([]string{"order_id", "status"}),
			Rows:           []dataquery.Row{{"order_id": "o1", "status": "delivered"}, {"order_id": "o2", "status": "shipped"}},
			TotalRows:      2,
			TotalRowsKnown: request.IncludeTotal,
			SQL:            string(request.Kind) + ": " + request.Target,
		}, nil
	default:
		return dataquery.Result{}, fmt.Errorf("unsupported data query kind %q", request.Kind)
	}
}

func fakeDataQueryResult(rows reportdef.QueryRows, includeTotal bool) dataquery.Result {
	out := make([]dataquery.Row, 0, len(rows))
	columnSet := map[string]bool{}
	columns := []string{}
	for _, row := range rows {
		converted := dataquery.Row{}
		for key, value := range row {
			converted[key] = value
			if !columnSet[key] {
				columnSet[key] = true
				columns = append(columns, key)
			}
		}
		out = append(out, converted)
	}
	return dataquery.Result{Columns: dataquery.ColumnsFromNames(columns), Rows: out, TotalRows: len(out), TotalRowsKnown: includeTotal}
}

func dataFieldsToReportFields(fields []dataquery.Field) []reportdef.QueryField {
	out := make([]reportdef.QueryField, 0, len(fields))
	for _, field := range fields {
		out = append(out, reportdef.QueryField{Field: field.Field, Alias: field.Alias})
	}
	return out
}

func dataFiltersToReportFilters(filters []dataquery.Filter) []reportdef.QueryFilter {
	out := make([]reportdef.QueryFilter, 0, len(filters))
	for _, filter := range filters {
		groups := make([]reportdef.QueryFilterGroup, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groups = append(groups, reportdef.QueryFilterGroup{Filters: dataFiltersToReportFilters(group.Filters)})
		}
		out = append(out, reportdef.QueryFilter{Field: filter.Field, Operator: filter.Operator, Values: append([]any{}, filter.Values...), Groups: groups})
	}
	return out
}

func dataSortToReportSort(sort []dataquery.Sort) []reportdef.QuerySort {
	out := make([]reportdef.QuerySort, 0, len(sort))
	for _, item := range sort {
		out = append(out, reportdef.QuerySort{Field: item.Field, Direction: item.Direction})
	}
	return out
}

func (fakeMetrics) ExplainSemanticQuery(_ string, request reportdef.AggregateQuery) (semanticquery.Plan, error) {
	return semanticquery.NewPlanner(fakeMetrics{}.mustSemanticModel()).Plan(reportdef.SemanticAggregateRequest(request))
}

func (fakeMetrics) ExplainSemanticPreview(_ string, request reportdef.RowQuery) (semanticquery.Plan, error) {
	return semanticquery.NewPlanner(fakeMetrics{}.mustSemanticModel()).PlanRows(reportdef.SemanticRowRequest(request))
}

func (fakeMetrics) mustSemanticModel() *semanticmodel.Model {
	_, model, _ := fakeMetrics{}.dashboardDefinition("executive-sales")
	return model
}

func (fakeMetrics) DefaultFilters(_ string) dashboard.Filters {
	definition, _, ok := (fakeMetrics{}).dashboardDefinition("executive-sales")
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return definition.DefaultFilters()
}

func pointInteraction(field, fact string, targets ...string) dashboardauthoring.Interaction {
	return dashboardauthoring.Interaction{
		PointSelection: dashboardauthoring.SelectionInteraction{
			Toggle: true,
			Mappings: []dashboardauthoring.SelectionMapping{{
				Field: field,
				Fact:  fact,
				Value: "label",
				Label: "label",
			}},
			Targets: targets,
		},
	}
}

func (fakeMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	if request.Sort.Key == "" {
		request.Sort = dashboard.TableSort{Key: "order_id", Direction: "desc"}
	}
	return request.WithDefaults()
}

func (fakeMetrics) visualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	report, _, ok := fakeMetrics{}.dashboardDefinition(dashboardID)
	if !ok {
		return visualizationdefinition.Definition{}, false
	}
	definition, ok := report.Visualizations[visualID]
	return definition, ok
}

func (fakeMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != "executive-sales" {
		return nil
	}
	return []dashboard.Page{
		{
			ID:     "overview",
			Title:  "Overview",
			Width:  1366,
			Height: 940,
			FilterBindings: map[string]dashboardfilter.Binding{
				"state": {
					ID: "state", Key: dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "overview", "state"),
					Scope: dashboardfilter.ScopePage, PageID: "overview", Filter: "state", ValueKind: dashboardfilter.ValueString,
					Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
					URL:     dashboardfilter.URLPolicy{Param: "state", Encoding: dashboardfilter.URLEncodingTypedV1},
				},
			},
			Visuals: []dashboard.PageVisual{
				{ID: "header", Kind: "header", X: 0, Y: 0, Width: 100, Height: 40, Title: "Test", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 1}},
				{ID: "state-filter", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"}, Presentation: dashboardfilter.Presentation{Style: dashboardfilter.PresentationDropdown}, X: 0, Y: 42, Width: 100, Height: 32, Placement: dashboard.PagePlacement{Col: 1, Row: 2, ColSpan: 12, RowSpan: 2}},
				{ID: "orders-chart", Kind: "visual", Visual: "orders", X: 0, Y: 48, Width: 100, Height: 100, Placement: dashboard.PagePlacement{Col: 1, Row: 3, ColSpan: 6, RowSpan: 4}},
				{ID: "orders-table", Kind: "visual", Visual: "order_rows", X: 0, Y: 160, Width: 100, Height: 100, Placement: dashboard.PagePlacement{Col: 7, Row: 3, ColSpan: 6, RowSpan: 4}},
			},
		},
		{
			ID:     "operations",
			Title:  "Operations",
			Width:  1366,
			Height: 940,
			FilterBindings: map[string]dashboardfilter.Binding{
				"category": {
					ID: "category", Key: dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "operations", "category"),
					Scope: dashboardfilter.ScopePage, PageID: "operations", Filter: "category", ValueKind: dashboardfilter.ValueString,
					Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered},
					URL:     dashboardfilter.URLPolicy{Param: "category", Encoding: dashboardfilter.URLEncodingTypedV1},
				},
			},
			Visuals: []dashboard.PageVisual{
				{ID: "category-filter", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "category"}, Presentation: dashboardfilter.Presentation{Style: dashboardfilter.PresentationInput}, X: 0, Y: 8, Width: 100, Height: 32, Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 2}},
				{ID: "ops-pipeline-chart", Kind: "visual", Visual: "ops_pipeline", X: 0, Y: 48, Width: 100, Height: 100, Placement: dashboard.PagePlacement{Col: 1, Row: 2, ColSpan: 12, RowSpan: 4}},
			},
		},
	}
}

func (fakeMetrics) QueryDashboard(_ context.Context, _ string, filters dashboard.Filters) (dashboard.Patch, error) {
	return fakeMetrics{}.QueryDashboardPage(context.Background(), "executive-sales", "", filters)
}

func (fakeMetrics) QueryDashboardPage(_ context.Context, _ string, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	chartID := "orders"
	if pageID == "operations" {
		chartID = "ops_pipeline"
	}
	definition, _ := fakeMetrics{}.visualizationDefinition("executive-sales", chartID)
	envelope, err := visualizationruntime.EnvelopeFromFrame(definition, visualizationruntime.Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", 1}}}, nil, 0, 0)
	if err != nil {
		return dashboard.Patch{}, err
	}
	visuals := map[string]visualizationir.VisualizationEnvelope{chartID: envelope}
	if pageID == "" || pageID == "overview" {
		definition, _ := fakeMetrics{}.visualizationDefinition("executive-sales", "order_rows")
		table, tableErr := fakeMetrics{}.queryWindow(context.Background(), "executive-sales", "overview", filters, dashboard.TableRequest{Table: "order_rows", Block: "a", Count: dashboard.TableChunkSize}.WithDefaults())
		if tableErr != nil {
			return dashboard.Patch{}, tableErr
		}
		tableEnvelope, tableEnvelopeErr := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 0, 0)
		if tableEnvelopeErr != nil {
			return dashboard.Patch{}, tableEnvelopeErr
		}
		visuals["order_rows"] = tableEnvelope
	}
	return dashboard.Patch{
		Filters: filters.WithDefaults(),
		Status: dashboard.Status{
			Loading:     false,
			LastUpdated: "12:00:00",
		},
		Visuals: visuals,
	}, nil
}

func (m *recordingMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	m.pageIDs <- pageID
	return m.fakeMetrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

func TestPageRouteRendersRequestedYamlPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test-workspace/dashboards/executive-sales/pages/operations", nil)
	rec := httptest.NewRecorder()

	server := newAppTestHarness(fakeMetrics{})
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "")
	if !strings.Contains(body, `<lv-app-shell`) || !strings.Contains(body, `<lv-dashboard-page`) {
		t.Fatalf("report page did not render app shell and dashboard route root:\n%s", body)
	}
	if strings.Contains(body, `<lv-report-sidebar`) {
		t.Fatalf("report page still rendered report sidebar:\n%s", body)
	}
	if strings.Contains(body, `<lv-sub-sidebar`) || strings.Contains(body, `<lv-report-canvas`) || strings.Contains(body, `<lv-echart`) || strings.Contains(body, `<lv-report-table`) {
		t.Fatalf("report page rendered dashboard product internals below route root:\n%s", body)
	}
	if !strings.Contains(body, `"compact":true`) {
		t.Fatalf("report page did not compact the primary sidebar:\n%s", body)
	}
	if !strings.Contains(body, `/workspaces/test-workspace/dashboards/executive-sales/pages/operations`) {
		t.Fatalf("report sidebar did not include operations page link:\n%s", body)
	}
	if strings.Contains(body, `class="page-tab`) {
		t.Fatalf("report header still rendered page tabs:\n%s", body)
	}
	decoded := html.UnescapeString(body)
	if strings.Contains(decoded, `"collapsible"`) || strings.Contains(decoded, `"numbered"`) {
		t.Fatalf("report sidebar should use default sub-sidebar behavior without chat overrides:\n%s", decoded)
	}
	if !strings.Contains(decoded, `2. Operations`) {
		t.Fatalf("report header did not include numbered active page title:\n%s", decoded)
	}
	visuals := streamedVisuals(t, decoded)
	if _, exists := visuals["ops_pipeline"]; !exists {
		t.Fatalf("operations page did not seed active page chart only:\n%s", decoded)
	}
	if _, exists := visuals["orders"]; exists {
		t.Fatalf("operations page seeded off-page order chart:\n%s", decoded)
	}
	if _, exists := visuals["order_rows"]; exists {
		t.Fatalf("operations page should seed no off-page tabular visuals:\n%s", decoded)
	}
}

func TestPageRouteSeedsPageScopedFiltersFromURL(t *testing.T) {
	state := typedSetURLValue(t, "RJ", "SP")
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test-workspace/dashboards/executive-sales/pages/overview?state="+state+"&category=ignored", nil)
	rec := httptest.NewRecorder()

	server := newAppTestHarness(fakeMetrics{})
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "")
	if !strings.Contains(body, `/static/url-sync.js`) {
		t.Fatalf("page did not include url sync script:\n%s", body)
	}
	if !strings.Contains(body, `"state":"`+state+`"`) {
		t.Fatalf("page did not seed state url params:\n%s", body)
	}
	if !strings.Contains(body, `"kind":"set","operator":"in","values":[{"kind":"string","value":"RJ"},{"kind":"string","value":"SP"}]`) {
		t.Fatalf("page did not seed state filter values:\n%s", body)
	}
	if strings.Contains(body, `"urlParams":{"category"`) || !strings.Contains(body, `"id":"category"`) || !strings.Contains(body, `"kind":"unfiltered"`) {
		t.Fatalf("overview page did not preserve off-page defaults while omitting their URL state:\n%s", body)
	}
}

func TestPageRouteSeedsOperationsPageFiltersFromURL(t *testing.T) {
	category := typedComparisonURLValue(t, dashboardfilter.OperatorContains, "ops")
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test-workspace/dashboards/executive-sales/pages/operations?state=ignored&category="+category, nil)
	rec := httptest.NewRecorder()

	server := newAppTestHarness(fakeMetrics{})
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "")
	if !strings.Contains(body, `"kind":"comparison","operator":"contains","value":{"kind":"string","value":"ops"}`) {
		t.Fatalf("operations page did not seed category URL filter:\n%s", body)
	}
	if strings.Contains(body, `"state":{"type"`) || strings.Contains(body, `"urlParams":{"state"`) || strings.Contains(body, `"urlParamShape":{"state"`) {
		t.Fatalf("operations page seeded off-page state filter:\n%s", body)
	}
}

func TestHTMLRoutesIncludeSelfHostedDatastarRuntimeAndDevInspector(t *testing.T) {
	for _, path := range []string{
		"/login",
		"/",
		"/workspaces/test-workspace/dashboards/executive-sales/pages/overview",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			assertDevDatastarRuntime(t, rec.Body.String())
		})
	}
}

func TestHTMLRoutesOmitDatastarInspectorInProduction(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{Assets: staticasset.New(staticasset.Config{Production: true})})
	for _, path := range []string{
		"/login",
		"/",
		"/workspaces/test-workspace/dashboards/executive-sales/pages/overview",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			server.Routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `/static/vendor/datastar-1.0.2.js?v=`) {
				t.Fatalf("page missing self-hosted Datastar runtime:\n%s", body)
			}
			for _, notWant := range []string{
				`/static/datastar-inspector.js`,
				`<datastar-inspector`,
			} {
				if strings.Contains(body, notWant) {
					t.Fatalf("production page included dev inspector marker %q:\n%s", notWant, body)
				}
			}
			if strings.Contains(body, "cdn.jsdelivr.net") {
				t.Fatalf("page references CDN-hosted Datastar runtime:\n%s", body)
			}
		})
	}
}

func TestHTMLRoutesHonorConfiguredStaticAssetVersion(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	assembleRuntime(fakeMetrics{}, assemblyConfig{
		Assets: staticasset.New(staticasset.Config{Version: "prod-build-123"}),
	}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `/static/app-shell.js?v=prod-build-123`) {
		t.Fatalf("home route missing configured static asset version:\n%s", body)
	}
	if strings.Contains(body, `?v=dev`) {
		t.Fatalf("home route leaked development static asset version:\n%s", body)
	}
}

func TestStaticAssetsCacheOnlyCurrentVersionedURLs(t *testing.T) {
	t.Chdir(projectRoot(t))
	handler := assembleRuntime(fakeMetrics{}, assemblyConfig{
		Assets: staticasset.New(staticasset.Config{Version: "prod-build-123"}),
	}).Routes()

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "current version",
			path: "/static/login-background-loader.js?v=prod-build-123",
			want: "public, max-age=31536000, immutable",
		},
		{
			name: "stale version",
			path: "/static/login-background-loader.js?v=old-build",
			want: "no-store",
		},
		{
			name: "unversioned",
			path: "/static/login-background-loader.js",
			want: "no-store",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/static/login-background-loader.js?v=dev", nil)
	rec := httptest.NewRecorder()
	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dev version status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("dev version Cache-Control = %q, want no-store", got)
	}
}

func TestStaticAssetCacheHeaderClasses(t *testing.T) {
	handler := staticAssetCache(staticasset.New(staticasset.Config{Version: "prod-build-123"}), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "current versioned asset",
			path: "/static/app.css?v=prod-build-123",
			want: "public, max-age=31536000, immutable",
		},
		{
			name: "hashed chunk asset",
			path: "/static/chunks/shared-app-shell-sv895r5c.js",
			want: "public, max-age=31536000, immutable",
		},
		{
			name: "font asset",
			path: "/static/files/inter-latin-wght-normal.woff2",
			want: "public, max-age=86400",
		},
		{
			name: "unversioned entrypoint",
			path: "/static/app-shell.js",
			want: "no-store",
		},
		{
			name: "stale versioned asset",
			path: "/static/app.css?v=old-build",
			want: "no-store",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if got := rec.Header().Get("Cache-Control"); got != tc.want {
				t.Fatalf("Cache-Control = %q, want %q", got, tc.want)
			}
		})
	}
}

func assertDevDatastarRuntime(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		`/static/vendor/datastar-1.0.2.js?v=dev`,
		`/static/datastar-inspector.js`,
		`<datastar-inspector`,
		`signals-url="/__dev/pagestream/signals"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing Datastar inspector marker %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "cdn.jsdelivr.net") {
		t.Fatalf("page references CDN-hosted Datastar runtime:\n%s", body)
	}
}

func TestHomeRouteRendersDashboardCatalog(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	server := newAppTestHarness(fakeMetrics{})
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "")
	rendered := body
	if !strings.Contains(rendered, `<lv-app-shell`) || !strings.Contains(rendered, `<lv-catalog-page`) {
		t.Fatalf("home did not mount catalog route root:\n%s", rendered)
	}
	if !strings.Contains(rendered, `/static/catalog-page.js`) {
		t.Fatalf("home missing catalog route bundle:\n%s", rendered)
	}
	if !strings.Contains(rendered, `Dashboards`) {
		t.Fatalf("home missing dashboard catalog title:\n%s", body)
	}
	if !strings.Contains(rendered, `Executive Sales Dashboard`) {
		t.Fatalf("home missing dashboard card:\n%s", body)
	}
	if !strings.Contains(rendered, `"href":"/workspaces/test-workspace/dashboards/executive-sales"`) {
		t.Fatalf("home missing dashboard link:\n%s", body)
	}
	for _, want := range []string{`Dashboards`, `/`, `Workspaces`, `/workspaces`, `Data`, `/data`, `Admin`, `/admin`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("home sidebar missing %q:\n%s", want, body)
		}
	}
	for _, notWant := range []string{`Connections`, `/connections`, `Metric Views`, `/metrics`, `Semantic Models`, `/models`, `Settings`, `/workspaces/test-workspace/permissions`, `/workspaces/test-workspace/chat`} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("home sidebar rendered removed navigation %q:\n%s", notWant, body)
		}
	}
	if !strings.Contains(rendered, `"id":"chat"`) || !strings.Contains(rendered, `"href":"/chats"`) {
		t.Fatalf("home sidebar did not render global chat navigation:\n%s", body)
	}
	if strings.Contains(rendered, `<lv-sub-sidebar`) {
		t.Fatalf("dashboard catalog should not render sub sidebar:\n%s", body)
	}
}

func TestHomeRouteAggregatesDBBackedWorkspaceCatalogs(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	for _, row := range []workspace.EnsureInput{
		{ID: "operations", Title: "Operations Workspace"},
		{ID: "sales", Title: "Sales Workspace"},
		{ID: "visuals", Title: "Visuals Workspace"},
	} {
		if err := workspaceRepo.Ensure(ctx, row); err != nil {
			t.Fatalf("ensure workspace: %v", err)
		}
	}
	metrics := NewMultiWorkspaceMetrics(map[string]QueryMetrics{
		"operations": namedWorkspaceMetrics{workspaceID: "operations", dashboardID: "fulfillment-operations", title: "Fulfillment Operations"},
		"sales":      namedWorkspaceMetrics{workspaceID: "sales", dashboardID: "executive-sales", title: "Executive Sales"},
		"visuals":    namedWorkspaceMetrics{workspaceID: "visuals", dashboardID: "visual-showcase", title: "Visual Showcase"},
	})
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceRepo: workspaceRepo, WorkspaceID: "operations"}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	rendered := renderedWithBootstrap(t, server, rec.Body.String(), "")
	for _, want := range []string{
		`Fulfillment Operations`,
		`Executive Sales`,
		`Visual Showcase`,
		`"href":"/workspaces/operations/dashboards/fulfillment-operations"`,
		`"href":"/workspaces/sales/dashboards/executive-sales"`,
		`"href":"/workspaces/visuals/dashboards/visual-showcase"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("home catalog missing %q:\n%s", want, rendered)
		}
	}
}

func TestLoginRouteRendersAzureADLogin(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()

	server := newAppTestHarness(fakeMetrics{})
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := renderedWithBootstrap(t, server, rec.Body.String(), "")
	if !strings.Contains(body, `<lv-login-page`) {
		t.Fatalf("login page did not mount login route root:\n%s", body)
	}
	if !strings.Contains(body, `background-module-src="/static/topology-background.js`) {
		t.Fatalf("login page did not seed versioned background module src on route root:\n%s", body)
	}
	if !strings.Contains(body, `Sign in with Azure Active Directory`) {
		t.Fatalf("login page did not seed Azure AD provider label:\n%s", body)
	}
	if strings.Contains(body, `data-init__delay`) || strings.Contains(body, `leapview-login-background-init`) {
		t.Fatalf("login page still uses Datastar for lazy background init:\n%s", body)
	}
	if !strings.Contains(body, `/static/login-background-loader.js`) {
		t.Fatalf("login page did not load the CSP-compatible background loader asset:\n%s", body)
	}
	if strings.Contains(body, `requestIdleCallback`) {
		t.Fatalf("login page rendered background loader inline instead of from static asset:\n%s", body)
	}
	if !strings.Contains(body, `/static/topology-background.js`) {
		t.Fatalf("login page did not include lazy topology background asset:\n%s", body)
	}
	if strings.Contains(body, `starfederation/datastar`) || strings.Contains(body, `cdn.jsdelivr`) {
		t.Fatalf("login page still references remote Datastar runtime:\n%s", body)
	}
	if !strings.Contains(body, `/static/vendor/datastar-1.0.2.js?v=dev`) {
		t.Fatalf("login page did not include framework Datastar runtime:\n%s", body)
	}
}

func TestDashboardRouteRedirectsToFirstPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test-workspace/dashboards/executive-sales", nil)
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/workspaces/test-workspace/dashboards/executive-sales/pages/overview" {
		t.Fatalf("Location = %q, want first page", got)
	}
}

func TestServerRejectsBlankWorkspaceMetricsLookup(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})

	if _, ok := server.metricsForWorkspace(""); ok {
		t.Fatal("metricsForWorkspace(\"\") returned metrics, want no implicit workspace")
	}
}

func TestWorkspaceScopedDashboardRoutesRejectCrossWorkspaceLookup(t *testing.T) {
	metrics := NewMultiWorkspaceMetrics(map[string]QueryMetrics{
		"sales":      namedWorkspaceMetrics{workspaceID: "sales", dashboardID: "executive-sales", title: "Executive Sales"},
		"operations": namedWorkspaceMetrics{workspaceID: "operations", dashboardID: "fulfillment-operations", title: "Fulfillment Operations"},
	})
	server := assembleRuntime(metrics, assemblyConfig{WorkspaceID: "sales"})

	okReq := httptest.NewRequest(http.MethodGet, "/workspaces/operations/dashboards/fulfillment-operations/pages/overview", nil)
	okRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("operations route status = %d, want 200; body:\n%s", okRec.Code, okRec.Body.String())
	}

	crossReq := httptest.NewRequest(http.MethodGet, "/workspaces/operations/dashboards/executive-sales/pages/overview", nil)
	crossRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(crossRec, crossReq)
	if crossRec.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace route status = %d, want 404; body:\n%s", crossRec.Code, crossRec.Body.String())
	}
}

func TestUnknownPageRouteReturnsNotFound(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/workspaces/test-workspace/dashboards/executive-sales/pages/missing", nil)
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestLegacyRoutesReturnNotFound(t *testing.T) {
	for _, path := range []string{
		"/pages/overview",
		"/model",
		"/models",
		"/models/test",
		"/metrics/orders",
		"/metrics/orders/measures",
		"/metrics/orders/dimensions",
		"/metrics/orders/usage",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()

		newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusNotFound)
		}
	}
}

func (fakeMetrics) queryWindow(_ context.Context, _ string, _ string, _ dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	request = request.WithDefaults()
	return dashboard.Table{
		Version: 2,
		Title:   "Orders",
		Columns: []dashboard.TableColumn{
			{Key: "order_id", Label: "Order"},
			{Key: "revenue", Label: "Revenue", Role: "measure", Format: "decimal"},
		},
		Cardinality:   dashboard.ExactCardinality(1),
		AvailableRows: 1,
		IsCapped:      false,
		RowCap:        dashboard.TableInteractiveRowCap,
		ChunkSize:     dashboard.TableChunkSize,
		RowHeight:     dashboard.TableRowHeight,
		ResetVersion:  request.ResetVersion,
		Sort:          request.Sort,
		Blocks: map[string]dashboard.TableBlock{
			"a": {
				Start:        request.Start,
				RequestSeq:   request.RequestSeq,
				ResetVersion: request.ResetVersion,
				Sort:         request.Sort,
				Rows:         []map[string]any{{"order_id": "o1", "revenue": 99.0}},
			},
		},
	}, nil
}

func (canceledTableMetrics) queryWindow(_ context.Context, _ string, _ string, _ dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	request = request.WithDefaults()
	return dashboard.EmptyTable(request, context.Canceled), nil
}

func (m canceledTableMetrics) QueryVisualizationWindow(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	return fakeVisualizationWindow(ctx, m, dashboardID, pageID, filters, request)
}

func (canceledTableMetrics) ExecuteConsumersPage(_ context.Context, request consumer.Request, publish consumer.Publisher) error {
	for _, target := range request.Targets {
		publish(consumer.Result{Target: target, Err: context.Canceled})
	}
	return nil
}

func TestUpdatesStreamsDatastarPatchSignals(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=dashboard&workspace=test-workspace&dashboard=executive-sales&page=overview&state="+typedSetURLValue(t, "SP")+"&category=ignored", nil)
	rec := newSynchronizedRecorder()
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		server.Routes().ServeHTTP(rec, req)
	}()
	waitForRecorderBodyContains(t, rec, `"loading":false`)
	cancel()
	<-returned

	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}

	body := rec.BodyString()
	patches := ssetest.PatchSignals(t, body)
	if len(patches) == 0 {
		t.Fatalf("body does not contain Datastar patch signal event:\n%s", body)
	}
	firstStatus, ok := patches[0]["status"].(map[string]any)
	if !ok || firstStatus["loading"] != true {
		t.Fatalf("first patch status = %#v, want loading=true; patches=%#v", firstStatus, patches)
	}
	ssetest.RequirePatchSignal(t, body, func(patch map[string]any) bool {
		status, ok := patch["status"].(map[string]any)
		return ok && status["loading"] == false
	})
	ssetest.RequirePatchSignal(t, body, func(patch map[string]any) bool {
		filterState, ok := patch["filterState"].(map[string]any)
		if !ok {
			return false
		}
		controls, ok := filterState["appliedControls"].(map[string]any)
		if !ok {
			return false
		}
		key := dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "overview", "state")
		state, ok := controls[key].(map[string]any)
		if !ok {
			return false
		}
		expression, ok := state["expression"].(map[string]any)
		if !ok {
			return false
		}
		values, ok := expression["values"].([]any)
		if !ok || len(values) != 1 {
			return false
		}
		value, ok := values[0].(map[string]any)
		return ok && value["value"] == "SP"
	})
}

func TestUpdatesStreamsPageScopedChartSignals(t *testing.T) {
	server := newAppTestHarness(fakeMetrics{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=dashboard&workspace=test-workspace&dashboard=executive-sales&page=operations&datastar=%7B%22runtime%22%3A%7B%22clientId%22%3A%22test-client%22%2C%22dashboardId%22%3A%22executive-sales%22%2C%22pageId%22%3A%22operations%22%7D%7D", nil)
	rec := newSynchronizedRecorder()
	returned := make(chan struct{})

	go func() {
		defer close(returned)
		server.Routes().ServeHTTP(rec, req)
	}()
	waitForRecorderBodyContains(t, rec, `"visuals":{"ops_pipeline"`)
	cancel()
	<-returned

	body := rec.BodyString()
	visuals := streamedVisuals(t, body)
	if _, exists := visuals["ops_pipeline"]; !exists {
		t.Fatalf("updates did not stream active page chart:\n%s", body)
	}
	if _, exists := visuals["orders"]; exists {
		t.Fatalf("updates streamed off-page chart:\n%s", body)
	}
	if _, exists := visuals["order_rows"]; exists {
		t.Fatalf("updates should not stream off-page tabular visuals:\n%s", body)
	}
	if strings.Contains(body, `"kpis"`) {
		t.Fatalf("updates streamed legacy KPI signal:\n%s", body)
	}
}

func streamedVisuals(t *testing.T, body string) map[string]any {
	t.Helper()
	if start := strings.Index(body, "event: datastar-patch-signals"); start >= 0 {
		body = body[start:]
	}
	for _, patch := range ssetest.PatchSignals(t, body) {
		if visuals, ok := patch["visuals"].(map[string]any); ok {
			return visuals
		}
	}
	t.Fatalf("Datastar stream did not include a visuals patch:\n%s", body)
	return nil
}

func mergedStreamedVisual(t *testing.T, body, visualID string) map[string]any {
	t.Helper()
	merged := map[string]any{}
	for _, patch := range ssetest.PatchSignals(t, body) {
		visuals, ok := patch["visuals"].(map[string]any)
		if !ok {
			continue
		}
		visual, ok := visuals[visualID].(map[string]any)
		if !ok {
			continue
		}
		for key, value := range visual {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		t.Fatalf("Datastar stream did not include visual %q:\n%s", visualID, body)
	}
	return merged
}

func TestDashboardRefreshCommandRouteIsRemoved(t *testing.T) {
	body := strings.NewReader(`{"runtime":{"clientId":"test-client"},"visualWindowCommand":{"visualID":"order_rows","blockID":"all","start":0,"limit":50,"sort":[]}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test-workspace/commands/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body:\n%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestSelectCommandAcceptsDatastarSignals(t *testing.T) {
	body := strings.NewReader(`{"interactionSelections":[],"runtime":{"clientId":"test-client"},"interactionCommand":{"sourceKind":"visual","sourceId":"orders","interactionKind":"point_selection","action":"set","toggle":true,"mappings":[{"field":"orders.status","fact":"orders","value":"delivered","label":"delivered"}]},"visualWindowCommand":{"visualID":"order_rows","blockID":"all","start":0,"limit":50,"sort":[]}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test-workspace/commands/select", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	assertDatastarCommandAccepted(t, rec)
}

func assertDatastarCommandAccepted(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body:\n%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != "{}\n" {
		t.Fatalf("body = %q, want empty Datastar JSON signal patch", got)
	}
}

func TestPageCommandsQueryActivePage(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		body    string
		source  string
		queries int
	}{
		{
			name:    "interaction select",
			path:    "/workspaces/test-workspace/commands/select",
			source:  "ops_pipeline",
			queries: 1,
		},
		{
			name: "clear selection",
			path: "/workspaces/test-workspace/commands/clear-selection",
			body: `{"runtime":{"clientId":"test-client","dashboardId":"executive-sales","pageId":"operations"},"interactionSelections":[{"sourceKind":"visual","sourceId":"ops_pipeline","interactionKind":"point_selection","entries":[{"mappings":[{"field":"orders.status","fact":"orders","value":"delivered","label":"delivered"}]}]}],"visualWindowCommand":{"blockID":"all","start":0,"limit":50,"sort":[]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics := &recordingMetrics{pageIDs: make(chan string, 4)}
			server := newAppTestHarness(metrics)
			body := tt.body
			var stopUpdates func()
			var updatesReturned chan struct{}
			if tt.source != "" {
				const streamInstanceID = "page-command-test"
				updatesContext, cancelUpdates := context.WithCancel(context.Background())
				stopUpdates = cancelUpdates
				updatesRecorder := newSynchronizedRecorder()
				updatesRequest := httptest.NewRequestWithContext(
					updatesContext,
					http.MethodGet,
					"/updates?route=dashboard&workspace=test-workspace&dashboard=executive-sales&page=operations&clientId=test-client&streamInstance="+streamInstanceID,
					nil,
				)
				updatesReturned = make(chan struct{})
				go func() {
					defer close(updatesReturned)
					server.Routes().ServeHTTP(updatesRecorder, updatesRequest)
				}()
				waitForRecorderBodyContains(t, updatesRecorder, `"kind":"ready"`)
				select {
				case pageID := <-metrics.pageIDs:
					if pageID != "operations" {
						t.Fatalf("initial query page ID = %q, want operations", pageID)
					}
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for the initial page query")
				}
				visual := mergedStreamedVisual(t, updatesRecorder.BodyString(), tt.source)
				interactionCommand := map[string]any{
					"sourceKind":          "visual",
					"sourceId":            tt.source,
					"interactionKind":     "point_selection",
					"action":              "set",
					"toggle":              true,
					"specRevision":        visual["specRevision"],
					"dataRevision":        visual["dataRevision"],
					"servingStateID":      visual["servingStateID"],
					"filterRevision":      visual["filterRevision"],
					"interactionRevision": visual["interactionRevision"],
					"mappings": []map[string]any{{
						"field": "orders.status", "fact": "orders", "value": "delivered", "label": "delivered",
					}},
				}
				encoded, err := json.Marshal(map[string]any{
					"runtime": map[string]any{
						"clientId": "test-client", "dashboardId": "executive-sales", "pageId": "operations", "streamInstanceId": streamInstanceID,
					},
					"interactionSelections": []any{},
					"interactionCommand":    interactionCommand,
					"visualWindowCommand": map[string]any{
						"blockID": "all", "start": 0, "limit": 50, "sort": []any{},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				body = string(encoded)
			}
			if stopUpdates != nil {
				defer func() {
					stopUpdates()
					<-updatesReturned
				}()
			}
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.Routes().ServeHTTP(rec, req)

			assertDatastarCommandAccepted(t, rec)
			for i := 0; i < tt.queries; i++ {
				select {
				case pageID := <-metrics.pageIDs:
					if pageID != "operations" {
						t.Fatalf("queried page ID = %q, want operations", pageID)
					}
				case <-time.After(time.Second):
					t.Fatalf("timed out after %d/%d targeted page queries", i, tt.queries)
				}
			}
		})
	}
}

func TestDashboardRefreshCommandDoesNotPersistRefreshRun(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal := testPrincipal(t, ctx, store, "editor@example.com", "Editor", "editor")
	token := testAPIToken(t, ctx, store, principal.ID, "dashboard-refresh")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test"}))
	body := strings.NewReader(`{"runtime":{"clientId":"test-client","dashboardId":"executive-sales","pageId":"operations","modelId":"test"},"visualWindowCommand":{"blockID":"all","start":0,"limit":50,"sort":[]}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/commands/refresh", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body:\n%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	repo := materializesqlite.NewSQLRunRepository(store.SQLDB())
	runs, err := repo.ListRuns(context.Background(), "test", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list model runs: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %#v, want none for removed dashboard refresh command", runs)
	}
}

func TestWorkspaceAssetDetailsUpdatesExcludeRefreshesTableAndUnusedRefreshFields(t *testing.T) {
	store := testStore(t)
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", emptyPageRuntimeAssetMetrics{})
	server := assembleRuntime(emptyPageRuntimeAssetMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	assetID := workspace.NewAssetID(workspace.AssetTypeSemanticModel, "olist")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=workspace_asset&workspace=test&asset="+string(assetID)+"&section=details", nil)
	rec := newSynchronizedRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(rec.BodyString(), "datastar-patch-signals") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	body := rec.BodyString()
	patches := ssetest.PatchSignals(t, body)
	if len(patches) == 0 {
		t.Fatalf("details updates did not stream patches:\n%s", body)
	}
	for _, patch := range patches {
		if _, ok := patch["assetRefreshesTable"]; ok {
			t.Fatalf("details updates streamed refreshes table: %#v", patch["assetRefreshesTable"])
		}
		page, ok := patch["page"].(map[string]any)
		if !ok {
			continue
		}
		refresh, ok := page["refresh"].(map[string]any)
		if !ok {
			continue
		}
		if _, ok := refresh["runsTable"]; ok {
			t.Fatalf("details updates streamed refreshes table: %#v", refresh["runsTable"])
		}
		for _, key := range []string{"error", "lastAttempt", "lastDuration"} {
			if _, ok := refresh[key]; ok {
				t.Fatalf("details assetRefresh included unused field %q: %#v", key, refresh)
			}
		}
	}
}

func TestLegacySemanticModelRefreshRouteIsRemoved(t *testing.T) {
	store := testStore(t)
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", emptyPageRuntimeAssetMetrics{})
	server := assembleRuntime(emptyPageRuntimeAssetMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test"}))
	assetID := workspace.NewAssetID(workspace.AssetTypeSemanticModel, "olist")
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test/assets/"+string(assetID)+"/refresh", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy semantic-model refresh status = %d, want 404", rec.Code)
	}
}

func TestClearSelectionCommandAcceptsDatastarSignals(t *testing.T) {
	body := strings.NewReader(`{"interactionSelections":[{"sourceKind":"visual","sourceId":"orders","interactionKind":"point_selection","entries":[{"mappings":[{"field":"orders.status","fact":"orders","value":"delivered","label":"delivered"}]}]}],"runtime":{"clientId":"test-client"},"visualWindowCommand":{"visualID":"order_rows","blockID":"all","start":0,"limit":50,"sort":[]}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test-workspace/commands/clear-selection", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	assertDatastarCommandAccepted(t, rec)
}

func TestTableWindowCommandAcceptsDatastarSignals(t *testing.T) {
	body := strings.NewReader(`{"runtime":{"clientId":"test-client"},"visualWindowCommand":{"visualID":"order_rows","specRevision":"","dataRevision":3,"blockID":"a","start":400,"limit":50,"requestSeq":42,"resetVersion":0,"sort":[{"field":{"dataset":"primary","field":"revenue"},"direction":"descending"}]}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test-workspace/commands/visual-window", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	newAppTestHarness(fakeMetrics{}).Routes().ServeHTTP(rec, req)

	assertDatastarCommandAccepted(t, rec)
}

func TestTableWindowCommandDoesNotPublishCanceledQueries(t *testing.T) {
	server := newAppTestHarness(canceledTableMetrics{})
	updates, unsubscribe := server.runtime.broker.Subscribe("test-client:executive-sales:overview")
	defer unsubscribe()

	body := strings.NewReader(`{"runtime":{"clientId":"test-client","dashboardId":"executive-sales","pageId":"overview"},"visualWindowCommand":{"visual":"order_rows","block":"all","start":400,"count":50,"requestSeq":42}}`)
	req := httptest.NewRequest(http.MethodPost, "/workspaces/test-workspace/commands/visual-window", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	assertDatastarCommandAccepted(t, rec)
	deadline := time.After(time.Second)
	for {
		select {
		case patch := <-updates:
			if _, ok := patch["tables"]; ok {
				t.Fatalf("received canceled table payload: %#v", patch)
			}
			if statuses, ok := patch["componentStatus"].(map[string]any); ok {
				if status, ok := statuses["visual:orders"].(map[string]any); ok && status["error"] != "" {
					t.Fatalf("cancellation surfaced as target error: %#v", patch)
				}
			}
			if status, ok := patch["status"].(map[string]any); ok && status["loading"] == false {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for canceled table generation to complete")
		}
	}
}
