package app

// Shared app tests use one small, project-scoped metrics fixture.  The fixture
// intentionally models the dashboard module contract directly; it does not
// provide workspace lookup or compatibility routing.

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/testing/dashboardfixture"
)

const testProjectID graph.ResourceID = "project:test"

var testServingIdentity = graph.ServingIdentity{ProjectID: testProjectID, Environment: "test", GenerationID: "generation:test"}

type fakeMetrics struct{}

type dashboardDefinitionProvider interface {
	dashboardDefinition(string) (dashboarddefinition.Definition, *semanticmodel.Model, bool)
}

type fixtureResolver struct{ provider dashboardDefinitionProvider }

func (r fixtureResolver) Resolve(dashboardID graph.ResourceID) (dashboardresolver.Resolved, error) {
	definition, model, ok := r.provider.dashboardDefinition(dashboardID.String())
	if !ok {
		return dashboardresolver.Resolved{}, dashboardresolver.ErrNotFound
	}
	return dashboardresolver.Resolved{
		Definition:      definition,
		Model:           model,
		SemanticModelID: graph.ResourceID("test"),
		Source: dashboardresolver.SourceMetadata{
			Kind:     dashboardresolver.SourceProject,
			Identity: testServingIdentity,
		},
	}, nil
}

func (fakeMetrics) Resolver() dashboardresolver.Resolver {
	return fixtureResolver{provider: fakeMetrics{}}
}

func (fakeMetrics) Catalog() dashboard.Catalog {
	return dashboard.Catalog{
		Project:    dashboard.CatalogProject{ID: testProjectID, Title: "Test Project", Description: "Fixture project"},
		Models:     []dashboard.CatalogModel{{ID: graph.ResourceID("test"), Title: "Test Model", Description: "Fixture model"}},
		Dashboards: []dashboard.CatalogDashboard{{ID: graph.ResourceID("executive-sales"), Title: "Executive Sales Dashboard", Description: "Fixture report", SemanticModel: graph.ResourceID("test"), Tags: []string{"sales"}, PageCount: 2}},
	}
}

func (fakeMetrics) DefaultDashboardID() string { return "executive-sales" }

func (fakeMetrics) ModelIDForDashboard(dashboardID string) string {
	if dashboardID == "executive-sales" {
		return "test"
	}
	return ""
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

type canceledTableMetrics struct{ fakeMetrics }

type recordingMetrics struct {
	fakeMetrics
	pageIDs chan string
}

func (m *recordingMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	for range request.Targets {
		m.pageIDs <- request.PageID
	}
	return m.fakeMetrics.ExecuteConsumersPage(ctx, request, publish)
}

func (m *recordingMetrics) QueryDashboardPage(ctx context.Context, dashboardID, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	m.pageIDs <- pageID
	return m.fakeMetrics.QueryDashboardPage(ctx, dashboardID, pageID, filters)
}

type namedProjectMetrics struct {
	fakeMetrics
	projectID   graph.ResourceID
	dashboardID string
	title       string
}

func (m namedProjectMetrics) Resolver() dashboardresolver.Resolver {
	return fixtureResolver{provider: m}
}

func (m namedProjectMetrics) Catalog() dashboard.Catalog {
	return dashboard.Catalog{
		Project:    graphProject(m.projectID),
		Models:     []dashboard.CatalogModel{{ID: graph.ResourceID("test"), Title: "Test Model"}},
		Dashboards: []dashboard.CatalogDashboard{{ID: graph.ResourceID(m.dashboardID), Title: m.title, SemanticModel: graph.ResourceID("test"), PageCount: 1}},
	}
}

func (m namedProjectMetrics) DefaultDashboardID() string { return m.dashboardID }

func (m namedProjectMetrics) dashboardDefinition(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if dashboardID != m.dashboardID {
		return dashboarddefinition.Definition{}, nil, false
	}
	authored := dashboardauthoring.Dashboard{ID: graph.ResourceID(m.dashboardID), Title: m.title, SemanticModel: "test", Visuals: dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
		"summary": {Type: "kpi", Title: "Summary", Query: dashboardauthoring.VisualQuery{Metrics: fieldRefs("order_count")}},
	}), Pages: m.Pages(dashboardID)}
	model := testSemanticModel()
	return dashboardfixture.Compile(authored, model), model, true
}

func (m namedProjectMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != m.dashboardID {
		return nil
	}
	return []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "summary", Kind: "visual", Visual: "summary", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 4}}}}}
}

func graphProject(id graph.ResourceID) dashboard.CatalogProject {
	return dashboard.CatalogProject{ID: id, Title: id.String()}
}

func (fakeMetrics) dashboardDefinition(dashboardID string) (dashboarddefinition.Definition, *semanticmodel.Model, bool) {
	if dashboardID != "executive-sales" {
		return dashboarddefinition.Definition{}, nil, false
	}
	authored := dashboardauthoring.Dashboard{
		ID: "executive-sales", Title: "Executive Sales Dashboard", SemanticModel: "test",
		FilterDefinitions: map[string]dashboardfilter.Definition{
			"state":    {Label: "State", Field: "orders.status", ValueKind: dashboardfilter.ValueString, Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}}, Options: dashboardfilter.OptionSource{Kind: dashboardfilter.OptionSourceDistinct, Limit: 50}},
			"category": {Label: "Category", Field: "orders.status", ValueKind: dashboardfilter.ValueString, Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionComparison, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorContains, dashboardfilter.OperatorEquals}}}},
		},
		FilterApplication: dashboardfilter.ApplicationPolicy{Mode: dashboardfilter.ApplicationImmediate},
		Visuals: dashboardauthoring.MergeVisualizations(dashboardauthoring.ChartVisualizations(map[string]dashboardauthoring.Visual{
			"orders":       {Title: "Orders", Type: "donut", Query: dashboardauthoring.VisualQuery{Dimensions: fieldRefs("orders.status"), Metrics: fieldRefs("order_count")}, Interaction: pointInteraction("orders.status", "orders", "ops_pipeline")},
			"ops_pipeline": {Title: "Ops Pipeline", Type: "bar", Query: dashboardauthoring.VisualQuery{Dimensions: fieldRefs("orders.status"), Metrics: fieldRefs("order_count")}, Interaction: pointInteraction("orders.status", "orders", "ops_pipeline")},
		}), dashboardauthoring.TabularVisualizations("table", map[string]dashboardauthoring.TableVisual{
			"order_rows": {Title: "Orders", Query: dashboardauthoring.TableQuery{Table: "orders", Fields: []string{"orders.order_id", "orders.revenue"}}, DefaultSort: dashboard.TableSort{Key: "order_id", Direction: "desc"}, Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order"}, {Key: "revenue", Label: "Revenue", Role: "metric", Format: "decimal"}}},
		})),
		Pages: fakeMetrics{}.Pages("executive-sales"),
	}
	model := testSemanticModel()
	return dashboardfixture.Compile(authored, model), model, true
}

func testSemanticModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "test", Title: "Test Model", Tables: map[string]semanticmodel.Table{"orders": {Source: "orders", Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id", Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "string"}, "status": {Type: "string"}, "revenue": {Type: "number"}}}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero", Label: "Orders"}}}
}

func (fakeMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if modelID != "test" {
		return nil, false
	}
	return testSemanticModel(), true
}

func (fakeMetrics) QuerySemantic(_ context.Context, _ string, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	rows := reportdef.QueryRows{{"status": "delivered", "order_count": 42}, {"status": "shipped", "order_count": 7}}
	return rows[:min(len(rows), request.Limit)], nil
}

func (fakeMetrics) PreviewSemantic(_ context.Context, _ string, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	rows := reportdef.QueryRows{{"order_id": "o1", "status": "delivered"}, {"order_id": "o2", "status": "shipped"}}
	return rows[:min(len(rows), request.Limit)], nil
}

func (fakeMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	switch request.Kind {
	case dataquery.KindSemanticAggregate:
		rows, err := fakeMetrics{}.QuerySemantic(ctx, request.ModelID, reportdef.AggregateQuery{Table: request.Target, Limit: request.Limit, Offset: request.Offset})
		return fakeDataQueryResult(rows, request.IncludeTotal), err
	case dataquery.KindSemanticRows:
		rows, err := fakeMetrics{}.PreviewSemantic(ctx, request.ModelID, reportdef.RowQuery{Table: request.Target, Limit: request.Limit, Offset: request.Offset})
		return fakeDataQueryResult(rows, request.IncludeTotal), err
	case dataquery.KindModelTableRows:
		return dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"order_id", "status"}), Rows: []dataquery.Row{{"order_id": "o1", "status": "delivered"}, {"order_id": "o2", "status": "shipped"}}, TotalRows: 2, TotalRowsKnown: request.IncludeTotal, SQL: string(request.Kind) + ": " + request.Target}, nil
	default:
		return dataquery.Result{}, fmt.Errorf("unsupported data query kind %q", request.Kind)
	}
}

func fakeDataQueryResult(rows reportdef.QueryRows, includeTotal bool) dataquery.Result {
	out := make([]dataquery.Row, 0, len(rows))
	columns := make([]string, 0)
	seen := map[string]bool{}
	for _, row := range rows {
		converted := dataquery.Row{}
		for key, value := range row {
			converted[key] = value
			if !seen[key] {
				seen[key] = true
				columns = append(columns, key)
			}
		}
		out = append(out, converted)
	}
	return dataquery.Result{Columns: dataquery.ColumnsFromNames(columns), Rows: out, TotalRows: len(out), TotalRowsKnown: includeTotal}
}

func (fakeMetrics) DefaultFilters(_ string) dashboard.Filters {
	definition, _, ok := fakeMetrics{}.dashboardDefinition("executive-sales")
	if !ok {
		return dashboard.Filters{}.WithDefaults()
	}
	return definition.DefaultFilters()
}

func (fakeMetrics) NormalizeVisualizationWindow(_ string, request dashboard.TableRequest) dashboard.TableRequest {
	if request.Sort.Key == "" {
		request.Sort = dashboard.TableSort{Key: "order_id", Direction: "desc"}
	}
	return request.WithDefaults()
}

func (fakeMetrics) visualizationDefinition(dashboardID, visualID string) (visualizationdefinition.Definition, bool) {
	definition, _, ok := fakeMetrics{}.dashboardDefinition(dashboardID)
	if !ok {
		return visualizationdefinition.Definition{}, false
	}
	visual, ok := definition.Visualizations[visualID]
	return visual, ok
}

func (fakeMetrics) Pages(dashboardID string) []dashboard.Page {
	if dashboardID != "executive-sales" {
		return nil
	}
	return []dashboard.Page{
		{ID: "overview", Title: "Overview", Width: 1366, Height: 940, FilterBindings: map[string]dashboardfilter.Binding{"state": {ID: "state", Key: dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "overview", "state"), Scope: dashboardfilter.ScopePage, PageID: "overview", Filter: "state", ValueKind: dashboardfilter.ValueString, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, URL: dashboardfilter.URLPolicy{Param: "state", Encoding: dashboardfilter.URLEncodingTypedV1}}}, Visuals: []dashboard.PageVisual{{ID: "header", Kind: "header", Title: "Test", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 1}}, {ID: "state-filter", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "state"}, Presentation: dashboardfilter.Presentation{Style: dashboardfilter.PresentationDropdown}, Placement: dashboard.PagePlacement{Col: 1, Row: 2, ColSpan: 12, RowSpan: 2}}, {ID: "orders-chart", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 3, ColSpan: 6, RowSpan: 4}}, {ID: "orders-table", Kind: "visual", Visual: "order_rows", Placement: dashboard.PagePlacement{Col: 7, Row: 3, ColSpan: 6, RowSpan: 4}}}},
		{ID: "operations", Title: "Operations", Width: 1366, Height: 940, FilterBindings: map[string]dashboardfilter.Binding{"category": {ID: "category", Key: dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "operations", "category"), Scope: dashboardfilter.ScopePage, PageID: "operations", Filter: "category", ValueKind: dashboardfilter.ValueString, Default: dashboardfilter.Expression{Kind: dashboardfilter.ExpressionUnfiltered}, URL: dashboardfilter.URLPolicy{Param: "category", Encoding: dashboardfilter.URLEncodingTypedV1}}}, Visuals: []dashboard.PageVisual{{ID: "category-filter", Kind: "slicer", Binding: dashboardfilter.BindingRef{Scope: dashboardfilter.ScopePage, ID: "category"}, Presentation: dashboardfilter.Presentation{Style: dashboardfilter.PresentationInput}, Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 12, RowSpan: 2}}, {ID: "ops-pipeline-chart", Kind: "visual", Visual: "ops_pipeline", Placement: dashboard.PagePlacement{Col: 1, Row: 2, ColSpan: 12, RowSpan: 4}}}},
	}
}

func (fakeMetrics) QueryDashboard(_ context.Context, _ string, filters dashboard.Filters) (dashboard.Patch, error) {
	return fakeMetrics{}.QueryDashboardPage(context.Background(), "executive-sales", "", filters)
}

func (fakeMetrics) QueryDashboardPage(ctx context.Context, _ string, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
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
		table, tableErr := fakeMetrics{}.queryWindow(ctx, "executive-sales", "overview", filters, dashboard.TableRequest{Table: "order_rows", Block: "a", Count: dashboard.TableChunkSize}.WithDefaults())
		if tableErr != nil {
			return dashboard.Patch{}, tableErr
		}
		tableEnvelope, tableEnvelopeErr := visualizationruntime.WindowEnvelopeFromDefinition(definition, table, 0, 0)
		if tableEnvelopeErr != nil {
			return dashboard.Patch{}, tableEnvelopeErr
		}
		visuals["order_rows"] = tableEnvelope
	}
	return dashboard.Patch{Filters: filters.WithDefaults(), Status: dashboard.Status{LastUpdated: "12:00:00"}, Visuals: visuals}, nil
}

func (fakeMetrics) queryWindow(_ context.Context, _ string, _ string, _ dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	count := request.Count
	if count <= 0 {
		count = dashboard.TableChunkSize
	}
	rows := []map[string]any{{"order_id": "o1", "status": "delivered"}, {"order_id": "o2", "status": "shipped"}}
	start := max(request.Start, 0)
	end := min(start+count, len(rows))
	if start > len(rows) {
		start = len(rows)
	}
	if end < start {
		end = start
	}
	blockID := request.Block
	if blockID == "" || blockID == "all" {
		blockID = "a"
	}
	sort := request.Sort
	if sort.Key == "" {
		sort = dashboard.TableSort{Key: "order_id", Direction: "desc"}
	}
	return dashboard.Table{
		Title: "Orders", Columns: []dashboard.TableColumn{{Key: "order_id", Label: "Order"}, {Key: "revenue", Label: "Revenue", Role: "metric", Format: "decimal"}},
		Cardinality: dashboard.ExactCardinality(len(rows)), AvailableRows: len(rows), RowCap: dashboard.TableInteractiveRowCap, ChunkSize: dashboard.TableChunkSize,
		ResetVersion: request.ResetVersion, Sort: sort,
		Blocks: map[string]dashboard.TableBlock{blockID: {Start: start, RequestSeq: request.RequestSeq, ResetVersion: request.ResetVersion, Sort: sort, Rows: rows[start:end]}},
	}, nil
}

func fieldRefs(fields ...string) []dashboardauthoring.FieldRef {
	refs := make([]dashboardauthoring.FieldRef, len(fields))
	for i, field := range fields {
		refs[i] = dashboardauthoring.FieldRef{Field: field}
	}
	return refs
}

func pointInteraction(field, fact string, targets ...string) dashboardauthoring.Interaction {
	return dashboardauthoring.Interaction{PointSelection: dashboardauthoring.SelectionInteraction{Toggle: true, Mappings: []dashboardauthoring.SelectionMapping{{Field: field, Fact: fact, Value: "label", Label: "label"}}, Targets: targets}}
}
