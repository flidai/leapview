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
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
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

func (fakeMetrics) Planner(modelID string) (consumer.Planner, bool) {
	if modelID != "test" {
		return nil, false
	}
	planner, err := semanticquery.NewCompiledPlanner(testSemanticModel())
	if err != nil {
		return nil, false
	}
	return planner, true
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
	model := testSemanticModel()
	doc := canonicalAppDashboard(graph.ResourceID(m.dashboardID).String(), m.title, map[string]dashboarddocument.DashboardVisual{
		"summary": canonicalAppAggregateVisual("Summary", dashboarddocument.DashboardVisualTypeKpi, nil, "order_count"),
	}, m.Pages(dashboardID))
	return dashboardfixture.Compile(doc, model), model, true
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
	model := testSemanticModel()
	return dashboardfixture.Compile(canonicalAppDashboard("executive-sales", "Executive Sales Dashboard", map[string]dashboarddocument.DashboardVisual{
		"orders":       canonicalAppAggregateVisual("Orders", dashboarddocument.DashboardVisualTypeDonut, []string{"status"}, "order_count"),
		"ops_pipeline": canonicalAppAggregateVisual("Ops Pipeline", dashboarddocument.DashboardVisualTypeBar, []string{"status"}, "order_count"),
		"order_rows":   canonicalAppRecordsVisual("Orders", []string{"order_id", "revenue"}),
	}, fakeMetrics{}.Pages("executive-sales")), model), model, true
}

func testSemanticModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "test", Title: "Test Model", Tables: map[string]semanticmodel.Table{"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, ModelName: "orders", Entities: map[string]semanticmodel.EntityDefinition{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id", Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Type: "string", Datatype: semanticmodel.DataTypeString}, "status": {Type: "string", Datatype: semanticmodel.DataTypeString}, "revenue": {Type: "number", Datatype: semanticmodel.DataTypeFloat}}}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Dimensions: map[string]semanticmodel.SemanticDimension{
		"order_id": {Label: "Order", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.order_id"}}},
		"status":   {Label: "Status", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}},
		"revenue":  {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.revenue"}}},
	}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero", Label: "Orders"}}}
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
		rows, err := fakeMetrics{}.QuerySemantic(ctx, request.ModelID, reportdef.AggregateQuery{Dataset: request.Target, Limit: request.Limit, Offset: request.Offset})
		return fakeDataQueryResult(rows, request.IncludeTotal), err
	case dataquery.KindSemanticRows:
		rows, err := fakeMetrics{}.PreviewSemantic(ctx, request.ModelID, reportdef.RowQuery{Dataset: request.Target, Limit: request.Limit, Offset: request.Offset})
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

func canonicalAppDashboard(id, title string, visuals map[string]dashboarddocument.DashboardVisual, pages []dashboard.Page) dashboarddocument.DashboardDocument {
	canonicalPages := make([]dashboarddocument.DashboardPage, 0, len(pages))
	for _, page := range pages {
		components := make([]dashboarddocument.DashboardPageComponent, 0, len(page.Visuals))
		for _, item := range page.Visuals {
			placement := dashboarddocument.DashboardPlacement{Column: int32(item.Placement.Col), Row: int32(item.Placement.Row), ColumnSpan: int32(item.Placement.ColSpan), RowSpan: int32(item.Placement.RowSpan)}
			// The legacy page fixture intentionally places its filter slicer on
			// the same boundary row as the following visual. Canonical layout is
			// strict about overlap, so retain the runtime fixture's visual order
			// while moving those visual components below the slicer in the
			// generated document.
			if page.ID == "overview" && (item.ID == "orders-chart" || item.ID == "orders-table") {
				placement.Row = 4
			}
			if page.ID == "operations" && item.ID == "ops-pipeline-chart" {
				placement.Row = 3
			}
			switch item.Kind {
			case "visual":
				components = append(components, dashboarddocument.DashboardPageComponent{Value: &dashboarddocument.VisualDashboardPageComponent{DashboardPageComponentBase: dashboarddocument.DashboardPageComponentBase{ID: item.ID, Type: "visual", Placement: placement}, Type: "visual", Visual: item.Visual}})
			case "header":
				title := item.Title
				components = append(components, dashboarddocument.DashboardPageComponent{Value: &dashboarddocument.HeaderDashboardPageComponent{DashboardPageComponentBase: dashboarddocument.DashboardPageComponentBase{ID: item.ID, Type: "header", Placement: placement}, Type: "header", Title: &title}})
			case "slicer":
				filterID := item.Binding.ID
				components = append(components, dashboarddocument.DashboardPageComponent{Value: &dashboarddocument.FilterDashboardPageComponent{DashboardPageComponentBase: dashboarddocument.DashboardPageComponentBase{ID: item.ID, Type: "filter", Placement: placement}, Type: "filter", Filter: filterID}})
			}
		}
		canonicalPages = append(canonicalPages, dashboarddocument.DashboardPage{ID: page.ID, Title: page.Title, Components: components})
	}
	filters := []dashboarddocument.DashboardFilter(nil)
	if id == "executive-sales" {
		filters = canonicalAppFilters()
	}
	return dashboarddocument.DashboardDocument{APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1, Kind: dashboarddocument.DashboardResourceKindDashboard, Metadata: dashboarddocument.DashboardMetadata{ID: id, Name: id, DisplayName: &title}, Spec: dashboarddocument.DashboardSpec{SemanticModel: "test", Filters: filters, Visuals: visuals, Pages: canonicalPages}}
}

func canonicalAppAggregateVisual(title string, visualType dashboarddocument.DashboardVisualType, dimensions []string, metric string) dashboarddocument.DashboardVisual {
	visual := dashboarddocument.DashboardVisual{Type: visualType, Title: &title, Query: dashboarddocument.DashboardQuery{Value: &dashboarddocument.AggregateDashboardQuery{Type: "aggregate", Dimensions: canonicalAppDimensions(dimensions), Metrics: []dashboarddocument.DashboardMetricSelection{{String: &metric}}}}, Presentation: canonicalAppPresentation(visualType)}
	if len(dimensions) > 0 {
		targets := []string{"ops_pipeline"}
		dataset, label := "orders", dimensions[0]
		// Canonical bar/donut result schemas do not mark a category dimension as
		// a stable identity (that role is reserved for point visuals), so keep
		// the selection mapping and target edge without requesting toggle
		// identity semantics that the generated IR cannot satisfy.
		visual.Interactions = &[]dashboarddocument.DashboardInteraction{{Value: &dashboarddocument.SelectionDashboardInteraction{DashboardInteractionBase: dashboarddocument.DashboardInteractionBase{Type: "selection", Targets: &targets}, Type: "selection", Mode: dashboarddocument.DashboardSelectionModeSingle, Mappings: []dashboarddocument.DashboardInteractionMapping{{Field: dimensions[0], Dataset: &dataset, Value: dimensions[0], Label: &label}}}}}
	}
	return visual
}

func canonicalAppRecordsVisual(title string, fields []string) dashboarddocument.DashboardVisual {
	selected := make([]dashboarddocument.DashboardRecordFieldSelection, 0, len(fields))
	for _, field := range fields {
		value := field
		selected = append(selected, dashboarddocument.DashboardRecordFieldSelection{String: &value})
	}
	return dashboarddocument.DashboardVisual{Type: dashboarddocument.DashboardVisualTypeTable, Title: &title, Query: dashboarddocument.DashboardQuery{Value: &dashboarddocument.RecordsDashboardQuery{Type: "records", Dataset: "orders", Fields: selected}}, Presentation: dashboarddocument.DashboardPresentation{Value: &dashboarddocument.TableDashboardPresentation{Type: "table", RowHeight: 36, ShowHeader: true, Striped: false}}}
}

func canonicalAppDimensions(values []string) []dashboarddocument.DashboardDimensionSelection {
	result := make([]dashboarddocument.DashboardDimensionSelection, 0, len(values))
	for _, value := range values {
		value := value
		result = append(result, dashboarddocument.DashboardDimensionSelection{String: &value})
	}
	return result
}

func canonicalAppPresentation(visualType dashboarddocument.DashboardVisualType) dashboarddocument.DashboardPresentation {
	switch visualType {
	case dashboarddocument.DashboardVisualTypeKpi:
		return dashboarddocument.DashboardPresentation{Value: &dashboarddocument.KPIDashboardPresentation{Type: "kpi"}}
	case dashboarddocument.DashboardVisualTypeDonut, dashboarddocument.DashboardVisualTypePie, dashboarddocument.DashboardVisualTypeFunnel:
		return dashboarddocument.DashboardPresentation{Value: &dashboarddocument.ProportionalDashboardPresentation{Type: "proportional"}}
	default:
		return dashboarddocument.DashboardPresentation{Value: &dashboarddocument.CartesianDashboardPresentation{Type: "cartesian"}}
	}
}

func canonicalAppFilters() []dashboarddocument.DashboardFilter {
	limit := int32(50)
	stateTargets := []string{"orders"}
	categoryTargets := []string{"ops_pipeline"}
	stateParam, categoryParam := "state", "category"
	return []dashboarddocument.DashboardFilter{
		{ID: "state", Label: "State", Dimension: "status", Control: dashboarddocument.DashboardFilterControl{Value: &dashboarddocument.MultiSelectDashboardFilterControl{Type: "multiSelect", MaxSelectedValues: &limit, Options: &dashboarddocument.DashboardFilterOptions{Value: &dashboarddocument.DistinctDashboardFilterOptions{Type: "distinct", Dataset: "orders", Limit: &limit}}}}, Operators: dashboardFilterOperators(dashboarddocument.DashboardFilterOperatorIn), Targets: &stateTargets, URLParameter: &stateParam},
		{ID: "category", Label: "Category", Dimension: "status", Control: dashboarddocument.DashboardFilterControl{Value: &dashboarddocument.TextDashboardFilterControl{Type: "text"}}, Operators: dashboardFilterOperators(dashboarddocument.DashboardFilterOperatorContains, dashboarddocument.DashboardFilterOperatorEquals), Targets: &categoryTargets, URLParameter: &categoryParam},
	}
}

func dashboardFilterOperators(values ...dashboarddocument.DashboardFilterOperator) *[]dashboarddocument.DashboardFilterOperator {
	return &values
}
