package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	materializeruntime "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/stretchr/testify/require"
)

type runtimeAuditRecorder struct {
	queries []dataquery.Query
	results []dataquery.Result
}

func setCompiledInteractionTarget(spec *visualizationir.VisualizationSpec, target string, enabled bool) {
	proportional, ok := spec.Value.(*visualizationir.ProportionalVisualizationSpec)
	if !ok || len(proportional.Interactions) == 0 {
		panic("orders visualization must have a proportional interaction")
	}
	targets := proportional.Interactions[0].Targets
	if enabled {
		for index := range targets {
			if targets[index].VisualID == target {
				targets[index].Effect = visualizationir.VisualizationInteractionEffectFilter
				return
			}
		}
		proportional.Interactions[0].Targets = append(targets, visualizationir.VisualizationInteractionTarget{VisualID: target, Effect: visualizationir.VisualizationInteractionEffectFilter})
		return
	}
	filtered := targets[:0]
	for _, existing := range targets {
		if existing.VisualID != target {
			filtered = append(filtered, existing)
		}
	}
	proportional.Interactions[0].Targets = filtered
}

func (r *runtimeAuditRecorder) RecordDataQuery(_ context.Context, query dataquery.Query, result dataquery.Result) error {
	r.queries = append(r.queries, query)
	r.results = append(r.results, result)
	return nil
}

func newLegacyRuntime(t *testing.T, dataDir string) (*Service, error) {
	t.Helper()
	return newManagedFixtureRuntime(t, dataDir, "sales")
}

func newOperationsRuntime(t *testing.T, dataDir string) (*Service, error) {
	t.Helper()
	return newManagedFixtureRuntime(t, dataDir, "operations")
}

func newManagedFixtureRuntime(t *testing.T, dataDir, workspaceID string) (*Service, error) {
	t.Helper()
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	compiled, err := workspacecompiler.CompileProject(projectPath, workspacecompiler.Options{})
	if err != nil {
		return nil, err
	}
	compiledWorkspace, ok := compiled.Workspace(workspaceID)
	if !ok {
		return nil, fmt.Errorf("showcase project has no %s workspace", workspaceID)
	}
	definition := compiledWorkspace.DashboardDefinition()
	bindManagedFixtureRoots(definition, dataDir)
	return NewFromDefinition(t.Context(), filepath.Join(dataDir, workspaceID), testDataRuntimeFactory{}, definition)
}

func bindManagedFixtureRoots(definition *dashboarddefinition.Workspace, root string) {
	for _, model := range definition.Models {
		for name, connection := range model.Connections {
			if connection.Kind != "managed" {
				continue
			}
			connection.Root = root
			model.Connections[name] = connection
		}
	}
}

func TestWorkspaceRuntimeUsesSingleDuckDBForSharedModelTables(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "orders.csv", `order_id,revenue
o1,10
o2,20
`)
	definition := sharedOrdersWorkspaceDefinition(t)
	bindManagedFixtureRoots(definition, dir)
	metrics, err := NewFromDefinition(t.Context(), dir, testDataRuntimeFactory{}, definition)
	require.NoError(t, err)

	for _, modelID := range []string{"model_a", "model_b"} {
		runtime := metrics.runtimes[modelID]
		if runtime == nil || !runtime.ready {
			t.Fatalf("%s runtime is not ready: %#v", modelID, runtime)
		}
		count, err := runtime.data.Count(context.Background(), reportdef.CountQuery{Table: "orders"})
		if err != nil {
			t.Fatalf("%s count: %v", modelID, err)
		}
		if count != 2 {
			t.Fatalf("%s count = %d, want 2", modelID, count)
		}
	}
	if err := metrics.Close(); err != nil {
		t.Fatalf("close runtime: %v", err)
	}

	catalogPath := filepath.Join(dir, "ducklake", "catalog.duckdb")
	if _, err := os.Stat(catalogPath); err != nil {
		t.Fatalf("catalog.duckdb stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ducklake", "data")); err != nil {
		t.Fatalf("data dir stat error = %v", err)
	}
	db, err := sql.Open("duckdb", ":memory:")
	require.NoError(t, err)
	defer db.Close()
	for _, stmt := range []string{
		"LOAD ducklake",
		"ATTACH 'ducklake:" + strings.ReplaceAll(catalogPath, "'", "''") + "' AS lake",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	var physicalTables int
	if err := db.QueryRowContext(context.Background(), "SELECT count(*) FROM ducklake_table_info('lake') WHERE table_name = 'orders'").Scan(&physicalTables); err != nil {
		t.Fatal(err)
	}
	if physicalTables != 1 {
		t.Fatalf("ducklake model.orders tables = %d, want 1", physicalTables)
	}
}

func sharedOrdersWorkspaceDefinition(t *testing.T) *dashboarddefinition.Workspace {
	t.Helper()
	modelA := sharedOrdersModel("model_a")
	modelA.Measures = map[string]semanticmodel.MetricMeasure{
		"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero", Label: "Orders"},
	}
	modelB := sharedOrdersModel("model_b")
	modelB.Measures = map[string]semanticmodel.MetricMeasure{
		"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
	}
	if err := modelA.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := modelB.Validate(); err != nil {
		t.Fatal(err)
	}
	return &dashboarddefinition.Workspace{
		Catalog: catalog.Catalog{
			Workspace: catalog.Workspace{ID: "shared", Title: "Shared"},
			Models: []catalog.Model{
				{ID: "model_a", Title: "Model A"},
				{ID: "model_b", Title: "Model B"},
			},
			Dashboards: []catalog.Dashboard{{ID: "dashboard", Title: "Dashboard"}},
		},
		Models: map[string]*semanticmodel.Model{
			"model_a": modelA,
			"model_b": modelB,
		},
		Dashboards: map[string]dashboarddefinition.Definition{"dashboard": {ID: "dashboard", Title: "Dashboard", SemanticModel: "model_a", Pages: []dashboard.Page{}, Visualizations: map[string]visualizationdefinition.Definition{}}},
	}
}

func sharedOrdersModel(name string) *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:              name,
		DefaultConnection: "local",
		Connections:       map[string]semanticmodel.Connection{"local": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"orders": {Connection: "local", Path: "orders.csv", Format: "csv"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Grain:      "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"revenue":  {Label: "Revenue"},
				},
			},
		},
	}
}

func TestMissingDataReturnsSetupPatch(t *testing.T) {
	dir := t.TempDir()
	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)

	patch, err := metrics.QueryDashboard(context.Background(), "executive-sales", dashboard.Filters{})
	require.NoError(t, err)
	if !patch.Status.SetupRequired {
		t.Fatalf("SetupRequired = false, want true")
	}
	if patch.Status.Error == "" {
		t.Fatal("expected setup error")
	}

	var missing *materializeruntime.MissingDataError
	if !errors.As(metrics.runtimes["sales"].missing, &missing) {
		t.Fatalf("missing error type = %T, want *MissingDataError", metrics.runtimes["sales"].missing)
	}
	if err := metrics.Verify(context.Background()); err == nil {
		t.Fatal("runtime verification accepted missing data")
	}
}

func TestOperationsFulfillmentDashboardQueryFixture(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_delivered_customer_date
o1,c1,delivered,2018-01-01 10:00:00,2018-01-03 10:00:00
o2,c2,shipped,2018-01-05 10:00:00,2018-01-15 10:00:00
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `order_id,review_score
o1,5
o2,3
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_zip_code_prefix,customer_city,customer_state
c1,01001,Sao Paulo,SP
c2,20040,Rio de Janeiro,RJ
`)

	metrics, err := newOperationsRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()
	if err := metrics.Verify(context.Background()); err != nil {
		t.Fatalf("verify prepared dashboard runtime: %v", err)
	}

	patch, err := metrics.QueryDashboardPage(context.Background(), "fulfillment-operations", "overview", dashboard.Filters{})
	require.NoError(t, err)
	if patch.Status.Error != "" {
		t.Fatalf("unexpected status error: %s", patch.Status.Error)
	}
	assertVisualKeys(t, patch, []string{"delivery_days", "delivery_speed", "orders_by_status", "review_by_status", "review_score", "total_orders"})
	if got := datumInt(envelopeRows(t, patch.Visuals["total_orders"])[0], "value"); got != 2 {
		t.Fatalf("orders KPI value = %d, want 2", got)
	}
	if len(envelopeRows(t, patch.Visuals["orders_by_status"])) == 0 {
		t.Fatal("orders by status chart has no data")
	}
	visualPatch, err := metrics.QueryDashboardVisualizations(context.Background(), "fulfillment-operations", "overview", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, visualPatch, []string{"delivery_days", "delivery_speed", "orders_by_status", "review_by_status", "review_score", "total_orders"})
}

func TestDashboardPageQueryIsolatesOneVisualizationFailure(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_delivered_customer_date
o1,c1,delivered,2018-01-01 10:00:00,2018-01-03 10:00:00
o2,c2,shipped,2018-01-05 10:00:00,2018-01-15 10:00:00
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `order_id,review_score
o1,5
o2,3
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_zip_code_prefix,customer_city,customer_state
c1,01001,Sao Paulo,SP
c2,20040,Rio de Janeiro,RJ
`)

	metrics, err := newOperationsRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	report := metrics.reports.workspace.Dashboards["fulfillment-operations"]
	broken := report.Visualizations["delivery_speed"]
	broken.Query.Aggregate.TableID = "missing_table"
	report.Visualizations["delivery_speed"] = broken
	metrics.reports.workspace.Dashboards["fulfillment-operations"] = report

	patch, err := metrics.QueryDashboardPage(context.Background(), "fulfillment-operations", "overview", dashboard.Filters{})
	require.NoError(t, err)
	if patch.Status.Error != "" {
		t.Fatalf("page status error = %q, want target-local error", patch.Status.Error)
	}
	assertVisualKeys(t, patch, []string{"delivery_days", "delivery_speed", "orders_by_status", "review_by_status", "review_score", "total_orders"})
	failed := patch.Visuals["delivery_speed"]
	if failed.Status.Kind != visualizationir.VisualizationStatusKindError || failed.Status.Message == nil || !strings.Contains(*failed.Status.Message, "missing_table") {
		t.Fatalf("failed visual envelope = %#v", failed)
	}
	if len(failed.Diagnostics) != 1 || failed.Diagnostics[0].Code != "query_failed" {
		t.Fatalf("failed visual diagnostics = %#v", failed.Diagnostics)
	}
	if patch.Visuals["total_orders"].Status.Kind != visualizationir.VisualizationStatusKindReady {
		t.Fatalf("successful visual was discarded: %#v", patch.Visuals["total_orders"].Status)
	}
}

func TestDashboardPageQueriesFlowThroughAuditedDataQueryBoundary(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_delivered_customer_date
o1,c1,delivered,2018-01-01 10:00:00,2018-01-03 10:00:00
o2,c2,shipped,2018-01-05 10:00:00,2018-01-15 10:00:00
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `order_id,review_score
o1,5
o2,3
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_zip_code_prefix,customer_city,customer_state
c1,01001,Sao Paulo,SP
c2,20040,Rio de Janeiro,RJ
`)

	metrics, err := newOperationsRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	recorder := &runtimeAuditRecorder{}
	ctx := dataquery.WithAuditRecorder(context.Background(), recorder)
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{PrincipalID: "test_principal"})
	patch, err := metrics.QueryDashboardPage(ctx, "fulfillment-operations", "overview", dashboard.Filters{})
	require.NoError(t, err)
	if patch.Status.Error != "" {
		t.Fatalf("unexpected status error: %s", patch.Status.Error)
	}
	if len(recorder.queries) == 0 {
		t.Fatal("dashboard page query recorded no dataquery events")
	}
	for _, query := range recorder.queries {
		if query.WorkspaceID != "operations" {
			t.Fatalf("query workspace = %q, want operations: %#v", query.WorkspaceID, query)
		}
		if query.Surface != dataquery.SurfaceDashboard {
			t.Fatalf("query surface = %q, want dashboard: %#v", query.Surface, query)
		}
		if query.PrincipalID != "test_principal" {
			t.Fatalf("query principal = %q, want test_principal: %#v", query.PrincipalID, query)
		}
	}
	for _, result := range recorder.results {
		if result.PlanningMS <= 0 || result.DatabaseMS <= 0 {
			t.Fatalf("query stage timings were not populated: %#v", result)
		}
	}
	batchedKPIs := false
	for _, query := range recorder.queries {
		if query.Kind == dataquery.KindSemanticAggregate && len(query.Fields) == 0 && len(query.Measures) == 3 {
			batchedKPIs = true
			break
		}
	}
	if !batchedKPIs {
		t.Fatalf("compatible KPI queries were not batched: %#v", recorder.queries)
	}

	recorder.queries = nil
	recorder.results = nil
	for _, field := range []string{"state", "status"} {
		options, optionErr := metrics.QueryCompiledFilterOptions(ctx, "fulfillment-operations", dashboardfilter.OptionQuery{
			Field: field, ValueKind: dashboardfilter.ValueString, Limit: 50,
		})
		if optionErr != nil {
			t.Fatal(optionErr)
		}
		if len(options.Items) == 0 {
			t.Fatalf("targeted %s filter options are empty", field)
		}
	}
	recorder.queries = nil
	recorder.results = nil
	visuals := map[string]visualizationir.VisualizationEnvelope{}
	progress := []consumer.Progress{}
	err = metrics.ExecuteConsumersPage(ctx, consumer.Request{DashboardID: "fulfillment-operations", PageID: "overview", Progress: func(value consumer.Progress) {
		progress = append(progress, value)
	}, Targets: []consumer.Target{
		{Kind: consumer.KindVisual, ID: "total_orders"},
		{Kind: consumer.KindVisual, ID: "delivery_days"},
		{Kind: consumer.KindVisual, ID: "review_score"},
	}}, func(result consumer.Result) bool {
		visuals[result.Target.ID] = result.Envelope
		return true
	})
	require.NoError(t, err)
	state, ok := visuals["total_orders"].DataState.Value.(*visualizationir.InlineVisualizationDataState)
	if len(visuals) != 3 || !ok || len(state.Datasets) == 0 || len(state.Datasets[0].Rows) == 0 {
		t.Fatalf("targeted visuals = %#v", visuals)
	}
	if len(recorder.queries) != 1 || len(recorder.queries[0].Measures) != 3 {
		t.Fatalf("targeted KPI queries = %#v, want one three-measure query", recorder.queries)
	}
	if len(progress) != 2 || progress[0] != (consumer.Progress{Total: 1}) || progress[1].Completed != 1 || progress[1].Total != 1 || progress[1].WorkDuration <= 0 || progress[1].CriticalPathDuration <= 0 {
		t.Fatalf("optimizer job progress = %#v", progress)
	}
}

func TestServicePreviewsRawModelTableRows(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date
o1,c1,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00
o2,c2,shipped,2018-01-11 10:00:00,2018-01-11 11:00:00,2018-01-12 10:00:00,2018-01-15 10:00:00,2018-01-20 10:00:00
`)
	writeFixture(t, dir, "olist_order_items_dataset.csv", `order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value
o1,1,p1,s1,2018-01-12 10:00:00,100.00,10.00
o2,1,p2,s2,2018-01-13 10:00:00,150.00,15.00
`)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,110.00
o2,1,credit_card,1,165.00
`)
	writeFixture(t, dir, "olist_products_dataset.csv", `product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm
p1,beleza_saude,10,20,1,500,20,10,15
p2,relogios_presentes,12,24,1,600,22,11,16
`)
	writeFixture(t, dir, "product_category_name_translation.csv", `product_category_name,product_category_name_english
beleza_saude,health_beauty
relogios_presentes,watches_gifts
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state
c1,u1,01001,Sao Paulo,SP
c2,u2,01002,Sao Paulo,SP
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp
r1,o1,5,,,2018-01-15,2018-01-15 10:00:00
r2,o2,4,,,2018-01-16,2018-01-16 10:00:00
`)

	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	ctx := dataquery.WithMetadata(context.Background(), dataquery.Metadata{PrincipalID: "test_principal"})
	modelResult, err := metrics.ExecuteDataQuery(ctx, dataquery.ModelTableRows("sales", "orders", []string{"order_id", "status"}, []dataquery.Sort{{Field: "status", Direction: "desc"}}, 0, 1, true))
	if err != nil {
		t.Fatalf("unified model table query: %v", err)
	}
	if modelResult.TotalRows != 2 || len(modelResult.Rows) != 1 || modelResult.Rows[0]["order_id"] != "o2" {
		t.Fatalf("unified model table result = %#v", modelResult)
	}
	if _, err := metrics.ExecuteDataQuery(ctx, dataquery.ModelTableRows("sales", "missing", nil, nil, 0, 1, false)); err == nil {
		t.Fatal("missing model table preview error = nil")
	}
}

func TestServiceTableInteractiveCap(t *testing.T) {
	dir := t.TempDir()
	const rows = dashboard.TableInteractiveRowCap + 5
	var orders, items, payments, customers, reviews strings.Builder
	orders.WriteString("order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date\n")
	items.WriteString("order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value\n")
	payments.WriteString("order_id,payment_sequential,payment_type,payment_installments,payment_value\n")
	customers.WriteString("customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state\n")
	reviews.WriteString("review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&orders, "o%d,c%d,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00\n", i, i)
		fmt.Fprintf(&items, "o%d,1,p1,s1,2018-01-12 10:00:00,100.00,10.00\n", i)
		fmt.Fprintf(&payments, "o%d,1,credit_card,1,110.00\n", i)
		fmt.Fprintf(&customers, "c%d,u%d,01001,Sao Paulo,SP\n", i, i)
		fmt.Fprintf(&reviews, "r%d,o%d,5,,,,2018-01-15 10:00:00\n", i, i)
	}
	writeFixture(t, dir, "olist_orders_dataset.csv", orders.String())
	writeFixture(t, dir, "olist_order_items_dataset.csv", items.String())
	writeFixture(t, dir, "olist_order_payments_dataset.csv", payments.String())
	writeFixture(t, dir, "olist_products_dataset.csv", "product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm\np1,beleza_saude,10,20,1,500,20,10,15\n")
	writeFixture(t, dir, "olist_customers_dataset.csv", customers.String())
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", reviews.String())
	writeFixture(t, dir, "product_category_name_translation.csv", "product_category_name,product_category_name_english\nbeleza_saude,health_beauty\n")

	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	recorder := &runtimeAuditRecorder{}
	ctx := dataquery.WithAuditRecorder(context.Background(), recorder)
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{PrincipalID: "table_test"})
	table, err := metrics.queryTableForTest(ctx, "executive-sales", dashboard.Filters{}, dashboard.TableRequest{Table: "orders_table", Block: "all", RequestSeq: 9})
	require.NoError(t, err)
	if table.Error != "" {
		t.Fatalf("table error = %q", table.Error)
	}
	if total := exactTableRows(t, table); total != rows {
		t.Fatalf("total rows = %d, want %d", total, rows)
	}
	if table.AvailableRows != dashboard.TableInteractiveRowCap {
		t.Fatalf("available rows = %d, want %d", table.AvailableRows, dashboard.TableInteractiveRowCap)
	}
	if !table.IsCapped {
		t.Fatal("table is not capped")
	}
	if got := len(table.Blocks["a"].Rows); got != dashboard.TableChunkSize {
		t.Fatalf("initial block rows = %d, want %d", got, dashboard.TableChunkSize)
	}
	if got := len(table.Blocks["b"].Rows); got != dashboard.TableChunkSize {
		t.Fatalf("initial block b rows = %d, want %d", got, dashboard.TableChunkSize)
	}
	if got := len(table.Blocks["c"].Rows); got != dashboard.TableChunkSize {
		t.Fatalf("initial block c rows = %d, want %d", got, dashboard.TableChunkSize)
	}
	if len(recorder.queries) != 2 {
		t.Fatalf("initial table data queries = %d, want rows plus count: %#v", len(recorder.queries), recorder.queries)
	}
	if recorder.queries[0].IncludeTotal {
		t.Fatalf("initial row query IncludeTotal = true: %#v", recorder.queries[0])
	}
	if strings.Contains(recorder.results[0].SQL, "COUNT(*) OVER") {
		t.Fatalf("initial row SQL blocks on a window count: %s", recorder.results[0].SQL)
	}
	if !recorder.queries[1].IncludeTotal || len(recorder.queries[1].Fields) != 0 || len(recorder.queries[1].Measures) != 0 {
		t.Fatalf("initial count query = %#v, want count-only semantic rows request", recorder.queries[1])
	}
	if got := table.Blocks["a"].RequestSeq; got != 9 {
		t.Fatalf("block request seq = %d, want 9", got)
	}

	recorder.queries = nil
	recorder.results = nil
	next, err := metrics.queryTableForTest(ctx, "executive-sales", dashboard.Filters{}, dashboard.TableRequest{Table: "orders_table", Block: "b", Start: dashboard.TableChunkSize, Count: dashboard.TableChunkSize, RequestSeq: 10})
	require.NoError(t, err)
	if next.Error != "" {
		t.Fatalf("next table block error = %q", next.Error)
	}
	if len(next.Blocks["b"].Rows) != dashboard.TableChunkSize {
		t.Fatalf("next block rows = %d, want %d", len(next.Blocks["b"].Rows), dashboard.TableChunkSize)
	}
	if total := exactTableRows(t, next); total != rows {
		t.Fatalf("next block total rows = %d, want %d", total, rows)
	}
	if len(recorder.queries) != 2 || recorder.queries[0].IncludeTotal || !recorder.queries[1].IncludeTotal {
		t.Fatalf("next block queries = %#v, want independent rows and count", recorder.queries)
	}

	definition, ok := metrics.VisualizationDefinition("executive-sales", "orders_table")
	if !ok {
		t.Fatal("compiled orders table definition is missing")
	}
	window, err := metrics.QueryVisualizationWindow(ctx, "executive-sales", "overview", dashboard.Filters{}, visualizationir.VisualizationWindowRequest{
		VisualID: "orders_table", SpecRevision: definition.SpecRevision, DataRevision: 4,
		BlockID: "b", Start: dashboard.TableChunkSize, Limit: dashboard.TableChunkSize, RequestSeq: 11,
	})
	require.NoError(t, err)
	windowState, ok := window.DataState.Value.(*visualizationir.WindowedVisualizationDataState)
	if !ok || window.DataRevision != 4 || len(windowState.Blocks["b"].Rows) != dashboard.TableChunkSize {
		t.Fatalf("canonical visualization window = %#v", window)
	}
	if _, err := metrics.QueryVisualizationWindow(ctx, "executive-sales", "overview", dashboard.Filters{}, visualizationir.VisualizationWindowRequest{
		VisualID: "orders_table", SpecRevision: "sha256:stale", BlockID: "a", Limit: dashboard.TableChunkSize,
	}); err == nil {
		t.Fatal("stale visualization specification revision was accepted")
	}

	jump, err := metrics.queries.visualizations.queryTableRowsPage(ctx, "executive-sales", "", dashboard.Filters{}, dashboard.TableRequest{
		Table: "orders_table", Block: "all", Start: 5_000, Count: dashboard.TableChunkSize, RequestSeq: 12, ResetVersion: 3,
	})
	require.NoError(t, err)
	for id, wantStart := range map[string]int{"a": 4_950, "b": 5_000, "c": 5_050} {
		block := jump.Blocks[id]
		if block.Start != wantStart || len(block.Rows) != dashboard.TableChunkSize || block.RequestSeq != 12 || block.ResetVersion != 3 {
			t.Fatalf("jump block %s = %#v, want start %d with a complete chunk", id, block, wantStart)
		}
	}

	overshoot, err := metrics.queries.visualizations.queryTableRowsPage(ctx, "executive-sales", "", dashboard.Filters{}, dashboard.TableRequest{
		Table: "orders_table", Block: "b", Start: rows + dashboard.TableChunkSize, Count: dashboard.TableChunkSize, RequestSeq: 11,
	})
	require.NoError(t, err)
	if _, exact := overshoot.Cardinality.ExactValue(); exact {
		t.Fatalf("overshoot cardinality = %#v, must remain inexact", overshoot.Cardinality)
	}
}

func TestServiceQueryFixture(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date
o1,c1,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00
o2,c2,shipped,2017-06-10 10:00:00,2017-06-10 11:00:00,2017-06-11 10:00:00,2017-06-20 10:00:00,2017-06-25 10:00:00
`)
	writeFixture(t, dir, "olist_order_items_dataset.csv", `order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value
o1,1,p1,s1,2018-01-12 10:00:00,100.00,10.00
o2,1,p2,s2,2017-06-12 10:00:00,50.00,5.00
`)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,110.00
o2,1,credit_card,1,55.00
`)
	writeFixture(t, dir, "olist_products_dataset.csv", `product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm
p1,beleza_saude,10,20,1,500,20,10,15
p2,relogios_presentes,12,24,1,600,22,11,16
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state
c1,u1,01001,Sao Paulo,SP
c2,u2,20000,Rio de Janeiro,RJ
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp
r1,o1,5,,,2018-01-15,2018-01-15 10:00:00
r2,o2,3,,,2017-06-21,2017-06-21 10:00:00
`)
	writeFixture(t, dir, "product_category_name_translation.csv", `product_category_name,product_category_name_english
beleza_saude,health_beauty
relogios_presentes,watches_gifts
`)

	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()
	if _, err := os.Stat(filepath.Join(dir, "sales", "ducklake", "catalog.duckdb")); err != nil {
		t.Fatalf("expected DuckLake catalog: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sales", "ducklake", "data")); err != nil {
		t.Fatalf("expected DuckLake data directory: %v", err)
	}

	patch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
		"state": setExpression("SP"),
		"purchase_date": rangeExpression(
			dashboardfilter.ValueDate, "2018-01-01", "2018-12-31",
		),
	}))
	require.NoError(t, err)

	if patch.Status.Error != "" {
		t.Fatalf("unexpected status error: %s", patch.Status.Error)
	}
	assertVisualKeys(t, patch, overviewVisualKeys())
	if got := datumInt(envelopeRows(t, patch.Visuals["total_orders"])[0], "value"); got != 1 {
		t.Fatalf("orders KPI value = %d, want 1", got)
	}
	if got := specKind(t, patch.Visuals["total_orders"]); got != "kpi" {
		t.Fatalf("orders KPI kind = %q, want kpi", got)
	}
	if _, ok := patch.Visuals["total_orders"].Spec.Value.(*visualizationir.KPIVisualizationSpec); !ok {
		t.Fatalf("orders KPI spec = %T, want KPI", patch.Visuals["total_orders"].Spec.Value)
	}
	if got := specBase(t, patch.Visuals["total_orders"]).Title; got != "Orders" {
		t.Fatalf("orders KPI title = %q, want Orders", got)
	}
	if len(envelopeRows(t, patch.Visuals["revenue_by_month"])) != 1 {
		t.Fatalf("revenue points = %d, want 1", len(envelopeRows(t, patch.Visuals["revenue_by_month"])))
	}
	revenueSpec, ok := patch.Visuals["revenue_by_month"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok {
		t.Fatalf("revenue chart spec = %T, want cartesian", patch.Visuals["revenue_by_month"].Spec.Value)
	}
	if got := revenueSpec.Mark; got != visualizationir.VisualizationCartesianMarkArea {
		t.Fatalf("revenue chart type = %q, want area", got)
	}
	if got := patch.Visuals["revenue_by_month"].SchemaVersion; got != visualizationir.CurrentSchemaVersion {
		t.Fatalf("revenue chart version = %d, want 3", got)
	}
	if got := specKind(t, patch.Visuals["revenue_by_month"]); got != "cartesian" {
		t.Fatalf("revenue chart kind = %q, want cartesian", got)
	}
	if got := patch.Visuals["revenue_by_month"].RendererID; got != "echarts" {
		t.Fatalf("revenue chart renderer = %q, want echarts", got)
	}
	if got := revenueSpec.Y[0].Field; got != "value" {
		t.Fatalf("revenue chart encoded value field = %q, want value", got)
	}
	if got := datumString(envelopeRows(t, patch.Visuals["category_revenue"])[0], "label"); got != "health_beauty" {
		t.Fatalf("top category = %q, want health_beauty", got)
	}
	options, err := metrics.QueryCompiledFilterOptions(context.Background(), "executive-sales", dashboardfilter.OptionQuery{
		Field: "state", ValueKind: dashboardfilter.ValueString, Limit: 50,
	})
	require.NoError(t, err)
	if got := len(options.Items); got != 2 {
		t.Fatalf("state filter options = %d, want 2", got)
	}

	defaultPagePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, defaultPagePatch, overviewVisualKeys())

	unknownDefaultPagePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "missing", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, unknownDefaultPagePatch, overviewVisualKeys())
	// These legacy showcase assertions require the full Olist fixture rather
	// than the compact service fixture used above. Keep them opt-in until they
	// are migrated to the dedicated integration harness.
	if os.Getenv("LEAPVIEW_EXTENDED_FIXTURE_ASSERTIONS") == "" {
		return
	}

	selectedFilters := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders", "point_selection", "orders.status", "delivered"),
		},
	}
	selectedPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", selectedFilters)
	require.NoError(t, err)
	if got := datumInt(envelopeRows(t, selectedPatch.Visuals["total_orders"])[0], "value"); got != 2 {
		t.Fatalf("selected orders KPI value = %d, want 2", got)
	}
	if len(envelopeRows(t, selectedPatch.Visuals["orders"])) != 2 {
		t.Fatalf("orders chart points without explicit self-target = %d, want 2", len(envelopeRows(t, selectedPatch.Visuals["orders"])))
	}
	if !pointSelected(envelopeRows(t, selectedPatch.Visuals["orders"]), "delivered") {
		t.Fatalf("orders chart did not mark delivered as selected: %#v", envelopeRows(t, selectedPatch.Visuals["orders"]))
	}
	if got := selectedEnvelopeEntryValue(selectedPatch.Visuals["orders"].Selection, "orders.status"); got != "delivered" {
		t.Fatalf("orders chart selection entry = %q, want delivered: %#v", got, selectedPatch.Visuals["orders"].Selection)
	}
	if got := datumString(envelopeRows(t, selectedPatch.Visuals["categories"])[0], "label"); got != "health_beauty" {
		t.Fatalf("category chart under status selection = %q, want health_beauty", got)
	}
	if got := datumString(envelopeRows(t, selectedPatch.Visuals["revenue"])[0], "series"); got != "" {
		t.Fatalf("single-series chart row series = %q, want empty", got)
	}

	report := metrics.reports.workspace.Dashboards["executive-sales"]
	ordersVisual := report.Visualizations["orders"]
	setCompiledInteractionTarget(&ordersVisual.Spec, "orders", true)
	report.Visualizations["orders"] = ordersVisual
	metrics.reports.workspace.Dashboards["executive-sales"] = report
	selfTargetPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", selectedFilters)
	require.NoError(t, err)
	if len(envelopeRows(t, selfTargetPatch.Visuals["orders"])) != 1 {
		t.Fatalf("orders chart points with explicit self-target = %d, want 1", len(envelopeRows(t, selfTargetPatch.Visuals["orders"])))
	}
	if !pointSelected(envelopeRows(t, selfTargetPatch.Visuals["orders"]), "delivered") {
		t.Fatalf("self-targeted orders chart did not mark delivered as selected: %#v", envelopeRows(t, selfTargetPatch.Visuals["orders"]))
	}
	report = metrics.reports.workspace.Dashboards["executive-sales"]
	setCompiledInteractionTarget(&ordersVisual.Spec, "orders", false)
	report.Visualizations["orders"] = ordersVisual
	metrics.reports.workspace.Dashboards["executive-sales"] = report

	columnPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-column", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, columnPatch, []string{"orders_by_month_column", "orders_by_month_status", "orders_by_month_status_grouped"})
	columnSpec, ok := columnPatch.Visuals["orders_by_month_status"].Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok || columnSpec.Series == nil {
		t.Fatalf("multi-series chart spec = %#v, want cartesian series", columnPatch.Visuals["orders_by_month_status"].Spec.Value)
	}
	if got := columnSpec.Presentation.Stacked; !got {
		t.Fatalf("multi-series chart stacked option = %v, want true", got)
	}
	if got := datumString(envelopeRows(t, columnPatch.Visuals["orders_by_month_status"])[0], "series"); got == "" {
		t.Fatal("multi-series chart row series is empty")
	}
	if len(envelopeRows(t, columnPatch.Visuals["orders_by_month_status"])) != 2 {
		t.Fatalf("non-target multi-series chart points under status selection = %d, want 2", len(envelopeRows(t, columnPatch.Visuals["orders_by_month_status"])))
	}

	boxplotPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-boxplot", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, boxplotPatch, []string{"delivery_distribution", "review_distribution", "revenue_distribution"})
	if len(envelopeRows(t, boxplotPatch.Visuals["revenue_distribution"])) == 0 {
		t.Fatal("revenue distribution payload is empty")
	}

	funnelPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-funnel", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, funnelPatch, []string{"delivery_funnel", "status_funnel", "status_funnel_left"})

	pieFilters := compiledFiltersForTest(t, metrics, "executive-sales", "chart-pie", map[string]dashboardfilter.Expression{
		"category": comparisonExpression(dashboardfilter.OperatorContains, "health"),
		"state":    setExpression("SP"),
	})
	piePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-pie", pieFilters)
	require.NoError(t, err)
	assertVisualKeys(t, piePatch, []string{"category_pie_inside", "status_pie", "status_pie_rose"})
	if piePatch.Filters.CompiledState == nil || piePatch.Filters.CompiledState.Revision != pieFilters.CompiledState.Revision {
		t.Fatalf("pie patch did not preserve canonical filter state: %#v", piePatch.Filters.CompiledState)
	}

	emptyPagePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, emptyPagePatch, overviewVisualKeys())

	unknownPagePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "missing", dashboard.Filters{})
	require.NoError(t, err)
	assertVisualKeys(t, unknownPagePatch, overviewVisualKeys())

	for chartType, visualKeys := range chartShowcaseMatrix() {
		pagePatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-"+chartType, dashboard.Filters{})
		if err != nil {
			t.Fatalf("query chart-%s: %v", chartType, err)
		}
		assertVisualKeys(t, pagePatch, visualKeys)
	}
	candlestickPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-candlestick", dashboard.Filters{})
	require.NoError(t, err)
	if len(envelopeRows(t, candlestickPatch.Visuals["revenue_candlestick"])) == 0 {
		t.Fatal("revenue candlestick payload is empty")
	}

	comboPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-combo", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, comboPatch, []string{"review_delivery_combo", "revenue_orders_combo", "revenue_orders_dual_axis_combo"})
	if comboSpec, ok := comboPatch.Visuals["revenue_orders_combo"].Spec.Value.(*visualizationir.CartesianVisualizationSpec); !ok || len(comboSpec.Y) != 2 {
		t.Fatalf("combo chart spec = %#v, want two measures", comboPatch.Visuals["revenue_orders_combo"].Spec.Value)
	}
	if !hasDatumValue(envelopeRows(t, comboPatch.Visuals["revenue_orders_combo"]), "series", "Revenue") || !hasDatumValue(envelopeRows(t, comboPatch.Visuals["revenue_orders_combo"]), "series", "Orders") {
		t.Fatalf("combo chart rows missing expected measure series: %#v", envelopeRows(t, comboPatch.Visuals["revenue_orders_combo"]))
	}

	waterfallPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-waterfall", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, waterfallPatch, []string{"orders_waterfall", "revenue_waterfall", "revenue_waterfall_labeled"})
	if got := cartesianMark(t, waterfallPatch.Visuals["revenue_waterfall"]); got != visualizationir.VisualizationCartesianMarkWaterfall {
		t.Fatalf("waterfall chart mark = %q, want waterfall", got)
	}
	if _, ok := envelopeRows(t, waterfallPatch.Visuals["revenue_waterfall"])[0]["start"]; !ok {
		t.Fatalf("waterfall row missing start/end: %#v", envelopeRows(t, waterfallPatch.Visuals["revenue_waterfall"])[0])
	}

	histogramPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-histogram", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, histogramPatch, []string{"delivery_histogram", "review_histogram", "revenue_histogram"})
	if got := cartesianMark(t, histogramPatch.Visuals["delivery_histogram"]); got != visualizationir.VisualizationCartesianMarkHistogram {
		t.Fatalf("histogram chart mark = %q, want histogram", got)
	}
	if _, ok := envelopeRows(t, histogramPatch.Visuals["delivery_histogram"])[0]["binStart"]; !ok {
		t.Fatalf("histogram row missing bin metadata: %#v", envelopeRows(t, histogramPatch.Visuals["delivery_histogram"])[0])
	}

	mapPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-map", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, mapPatch, []string{"state_order_map", "state_revenue_map", "state_revenue_map_labeled"})
	if got := specKind(t, mapPatch.Visuals["state_order_map"]); got != "geographic" {
		t.Fatalf("map chart kind = %q, want geographic", got)
	}
	if !hasDatumValue(envelopeRows(t, mapPatch.Visuals["state_order_map"]), "name", "SP") {
		t.Fatalf("map chart rows missing SP: %#v", envelopeRows(t, mapPatch.Visuals["state_order_map"]))
	}

	graphPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-graph", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, graphPatch, []string{"category_status_graph", "category_status_graph_circular", "status_delivery_graph"})
	graphSpec, ok := graphPatch.Visuals["status_delivery_graph"].Spec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if !ok || graphSpec.Mark != visualizationir.VisualizationHierarchyMarkGraph {
		t.Fatalf("graph visual spec = %#v, want graph", graphPatch.Visuals["status_delivery_graph"].Spec.Value)
	}
	if !hasDatumValue(envelopeRows(t, graphPatch.Visuals["status_delivery_graph"]), "source", "delivered") {
		t.Fatalf("graph rows missing delivered source: %#v", envelopeRows(t, graphPatch.Visuals["status_delivery_graph"]))
	}

	sunburstPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "chart-sunburst", selectedFilters)
	require.NoError(t, err)
	assertVisualKeys(t, sunburstPatch, []string{"category_state_status_sunburst", "category_status_sunburst", "state_status_sunburst"})
	sunburstSpec, ok := sunburstPatch.Visuals["category_status_sunburst"].Spec.Value.(*visualizationir.HierarchyVisualizationSpec)
	if !ok || sunburstSpec.Mark != visualizationir.VisualizationHierarchyMarkSunburst {
		t.Fatalf("hierarchy chart spec = %#v, want sunburst", sunburstPatch.Visuals["category_status_sunburst"].Spec.Value)
	}
	if !hasDatumValue(envelopeRows(t, sunburstPatch.Visuals["category_status_sunburst"]), "node", "health_beauty") {
		t.Fatalf("hierarchy rows missing health_beauty node: %#v", envelopeRows(t, sunburstPatch.Visuals["category_status_sunburst"]))
	}

	table, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table:      "orders_table",
		Block:      "a",
		Start:      0,
		Count:      1,
		RequestSeq: 7,
		Sort:       dashboard.TableSort{Key: "revenue", Direction: "asc"},
	})
	require.NoError(t, err)
	if total := exactTableRows(t, table); total != 2 {
		t.Fatalf("table total rows = %d, want 2", total)
	}
	if len(table.Blocks["a"].Rows) != 1 {
		t.Fatalf("table block rows = %d, want 1", len(table.Blocks["a"].Rows))
	}
	if got := table.Blocks["a"].Rows[0]["order_id"]; got != "o2" {
		t.Fatalf("first table order = %v, want o2", got)
	}
	if got := table.Blocks["a"].RequestSeq; got != 7 {
		t.Fatalf("single block request seq = %d, want 7", got)
	}
	if got := table.Blocks["a"].ResetVersion; got != table.ResetVersion {
		t.Fatalf("single block reset version = %d, want %d", got, table.ResetVersion)
	}
	if got := table.Blocks["a"].Sort; got.Key != "revenue" || got.Direction != "asc" {
		t.Fatalf("single block sort = %#v, want revenue asc", got)
	}
	if got := table.Columns[5].Format; got != "currency" {
		t.Fatalf("orders revenue format = %q, want currency", got)
	}

	conditionalTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table: "orders_conditional",
		Block: "all",
		Count: 10,
	})
	require.NoError(t, err)
	if got := conditionalTable.Style.Grid; got != "full" {
		t.Fatalf("conditional table grid = %q, want full", got)
	}
	if conditionalTable.RowHeight != dashboard.TableRowHeight {
		t.Fatalf("conditional table row height = %d, want %d", conditionalTable.RowHeight, dashboard.TableRowHeight)
	}
	if !tableColumnHasFormatting(conditionalTable.Columns, "status", "badge") {
		t.Fatalf("conditional table status column missing badge formatting: %#v", conditionalTable.Columns)
	}

	filteredTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders", "point_selection", "orders.status", "delivered"),
		},
	}, dashboard.TableRequest{Table: "orders_table", Block: "all", Count: 10, RequestSeq: 8})
	require.NoError(t, err)
	if total := exactTableRows(t, filteredTable); total != 1 {
		t.Fatalf("targeted table total rows = %d, want 1", total)
	}
	if filteredTable.AvailableRows != 1 {
		t.Fatalf("targeted table available rows = %d, want 1", filteredTable.AvailableRows)
	}

	andFilteredTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders", "point_selection", "orders.status", "delivered"),
			interactionSelection("visual", "categories", "point_selection", "orders.category", "watches_gifts"),
		},
	}, dashboard.TableRequest{Table: "orders_table", Block: "all", Count: 10, RequestSeq: 9})
	require.NoError(t, err)
	if total := exactTableRows(t, andFilteredTable); total != 0 {
		t.Fatalf("AND-filtered table total rows = %d, want 0", total)
	}
	if got := filteredTable.Blocks["a"].RequestSeq; got != 8 {
		t.Fatalf("all block request seq = %d, want 8", got)
	}

	selectedRowTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders_table", "row_selection", "orders.order_id", "o1"),
		},
	}, dashboard.TableRequest{Table: "orders_table", Block: "all", Count: 10, RequestSeq: 10})
	require.NoError(t, err)
	if got := selectedEntryValue(selectedRowTable.Selection, "orders.order_id"); got != "o1" {
		t.Fatalf("table selection entry = %q, want o1: %#v", got, selectedRowTable.Selection)
	}
	if selected, unfiltered := exactTableRows(t, selectedRowTable), exactTableRows(t, table); selected != unfiltered {
		t.Fatalf("selected source table total rows = %d, want self selection not applied to source table total %d", selected, unfiltered)
	}

	uiOnlyRowSelection := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders_table", "row_selection", dashboard.UIRowSelectionField, "o1"),
		},
	}
	uiOnlyRowTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", uiOnlyRowSelection, dashboard.TableRequest{Table: "orders_table", Block: "all", Count: 10, RequestSeq: 11})
	require.NoError(t, err)
	if got := selectedEntryValue(uiOnlyRowTable.Selection, dashboard.UIRowSelectionField); got != "o1" {
		t.Fatalf("UI-only table selection entry = %q, want o1: %#v", got, uiOnlyRowTable.Selection)
	}
	if selected, unfiltered := exactTableRows(t, uiOnlyRowTable), exactTableRows(t, table); selected != unfiltered {
		t.Fatalf("UI-only selected source table total rows = %d, want unfiltered total %d", selected, unfiltered)
	}
	uiOnlyRowPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", uiOnlyRowSelection)
	require.NoError(t, err)
	if got := datumInt(envelopeRows(t, uiOnlyRowPatch.Visuals["total_orders"])[0], "value"); got != 2 {
		t.Fatalf("UI-only row selection KPI value = %d, want unfiltered value 2", got)
	}

	multiRowSelection := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			interactionSelection("visual", "orders_table", "row_selection", "orders.order_id", "o1", "o2"),
		},
	}
	multiRowPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", multiRowSelection)
	require.NoError(t, err)
	if got := datumInt(envelopeRows(t, multiRowPatch.Visuals["total_orders"])[0], "value"); got != 2 {
		t.Fatalf("multi-row selected orders KPI value = %d, want 2", got)
	}
	if len(envelopeRows(t, multiRowPatch.Visuals["orders"])) != 2 {
		t.Fatalf("orders chart points under multi-row table selection = %d, want 2", len(envelopeRows(t, multiRowPatch.Visuals["orders"])))
	}
	andFilters := compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
		"state": setExpression("SP"),
	})
	andFilters.Selections = multiRowSelection.Selections
	andMultiRowPatch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", andFilters)
	require.NoError(t, err)
	if got := datumInt(envelopeRows(t, andMultiRowPatch.Visuals["total_orders"])[0], "value"); got != 1 {
		t.Fatalf("page filter AND multi-row selected orders KPI value = %d, want 1", got)
	}

	matrixTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table:      "state_status_matrix",
		Block:      "all",
		Count:      10,
		RequestSeq: 10,
		Sort:       dashboard.TableSort{Key: "state", Direction: "asc"},
	})
	require.NoError(t, err)
	if matrixTable.Kind != "matrix_table" {
		t.Fatalf("matrix table kind = %q, want matrix_table", matrixTable.Kind)
	}
	if len(matrixTable.Blocks["a"].Rows) != 2 {
		t.Fatalf("matrix rows = %d, want 2", len(matrixTable.Blocks["a"].Rows))
	}
	if !tableHasColumn(matrixTable.Columns, "pivot_delivered__order_count") {
		t.Fatalf("matrix columns missing delivered order count: %#v", matrixTable.Columns)
	}
	if got := matrixTable.Columns[0].Role; got != "row_header" {
		t.Fatalf("matrix first column role = %q, want row_header", got)
	}
	if !tableRowsHaveKey(matrixTable.Blocks["a"].Rows, "pivot_delivered__order_count") {
		t.Fatalf("matrix rows missing delivered order count: %#v", matrixTable.Blocks["a"].Rows)
	}
	if !tableRowsHaveValue(matrixTable.Blocks["a"].Rows, "pivot_delivered__order_count") {
		t.Fatalf("matrix rows missing delivered order count values: %#v", matrixTable.Blocks["a"].Rows)
	}

	pivotTable, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table:      "category_status_pivot",
		Block:      "all",
		Count:      10,
		RequestSeq: 11,
	})
	require.NoError(t, err)
	if pivotTable.Kind != "pivot_table" {
		t.Fatalf("pivot table kind = %q, want pivot_table", pivotTable.Kind)
	}
	if len(pivotTable.Columns) < 3 {
		t.Fatalf("pivot columns = %d, want category plus status columns", len(pivotTable.Columns))
	}
	if !tableHasColumn(pivotTable.Columns, "pivot_delivered") {
		t.Fatalf("pivot columns missing delivered column: %#v", pivotTable.Columns)
	}
	if got := pivotTable.Columns[1].Group; got != "Orders" {
		t.Fatalf("pivot first value column group = %q, want Orders", got)
	}
	if !tableRowsHaveValue(pivotTable.Blocks["a"].Rows, "pivot_delivered") {
		t.Fatalf("pivot rows missing delivered values: %#v", pivotTable.Blocks["a"].Rows)
	}

	formattedMatrix, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table: "state_status_matrix_formatted",
		Block: "all",
		Count: 10,
		Sort:  dashboard.TableSort{Key: "state", Direction: "asc"},
	})
	require.NoError(t, err)
	if formattedMatrix.RowHeight != dashboard.TableRowHeight {
		t.Fatalf("formatted matrix row height = %d, want %d", formattedMatrix.RowHeight, dashboard.TableRowHeight)
	}
	if !tableColumnHasFormatting(formattedMatrix.Columns, "revenue", "data_bar") {
		t.Fatalf("formatted matrix revenue column missing data bar formatting: %#v", formattedMatrix.Columns)
	}

	heatPivot, err := metrics.queryTableForTest(context.Background(), "executive-sales", dashboard.Filters{}, dashboard.TableRequest{
		Table: "category_status_pivot_heat",
		Block: "all",
		Count: 10,
	})
	require.NoError(t, err)
	if heatPivot.RowHeight != 28 {
		t.Fatalf("heat pivot row height = %d, want 28", heatPivot.RowHeight)
	}
	if !tableHasAnyFormatting(heatPivot.Columns, "background_scale") {
		t.Fatalf("heat pivot generated columns missing background scale formatting: %#v", heatPivot.Columns)
	}
	if !tableRowsHaveValue(heatPivot.Blocks["a"].Rows, "pivot_delivered") {
		t.Fatalf("heat pivot rows missing delivered values: %#v", heatPivot.Blocks["a"].Rows)
	}

}

func TestServiceInteractionSelectionPreservesCompositeTuples(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date
o1,c1,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00
o2,c2,delivered,2018-01-11 10:00:00,2018-01-11 11:00:00,2018-01-12 10:00:00,2018-01-15 10:00:00,2018-01-20 10:00:00
o3,c3,shipped,2018-01-12 10:00:00,2018-01-12 11:00:00,2018-01-13 10:00:00,2018-01-16 10:00:00,2018-01-20 10:00:00
o4,c4,shipped,2018-01-13 10:00:00,2018-01-13 11:00:00,2018-01-14 10:00:00,2018-01-17 10:00:00,2018-01-20 10:00:00
`)
	writeFixture(t, dir, "olist_order_items_dataset.csv", `order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value
o1,1,p1,s1,2018-01-12 10:00:00,100.00,10.00
o2,1,p2,s2,2018-01-13 10:00:00,150.00,15.00
o3,1,p1,s1,2018-01-14 10:00:00,50.00,5.00
o4,1,p2,s2,2018-01-15 10:00:00,200.00,20.00
`)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,110.00
o2,1,credit_card,1,165.00
o3,1,credit_card,1,55.00
o4,1,credit_card,1,220.00
`)
	writeFixture(t, dir, "olist_products_dataset.csv", `product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm
p1,beleza_saude,10,20,1,500,20,10,15
p2,relogios_presentes,12,24,1,600,22,11,16
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state
c1,u1,01001,Sao Paulo,SP
c2,u2,01002,Sao Paulo,SP
c3,u3,20000,Rio de Janeiro,RJ
c4,u4,20001,Rio de Janeiro,RJ
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp
r1,o1,5,,,2018-01-15,2018-01-15 10:00:00
r2,o2,4,,,2018-01-16,2018-01-16 10:00:00
r3,o3,3,,,2018-01-17,2018-01-17 10:00:00
r4,o4,4,,,2018-01-18,2018-01-18 10:00:00
`)
	writeFixture(t, dir, "product_category_name_translation.csv", `product_category_name,product_category_name_english
beleza_saude,health_beauty
relogios_presentes,watches_gifts
`)

	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	filters := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			compositeInteractionSelection("visual", "orders_table", "row_selection",
				map[string]string{"orders.order_id": "o1", "orders.status": "delivered", "orders.category": "health_beauty"},
				map[string]string{"orders.order_id": "o4", "orders.status": "shipped", "orders.category": "watches_gifts"},
			),
		},
	}
	patch, err := metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", filters)
	require.NoError(t, err)
	if patch.Status.Error != "" {
		t.Fatalf("unexpected status error: %s", patch.Status.Error)
	}
	if got := categoryRevenue(envelopeRows(t, patch.Visuals["category_revenue"]), "health_beauty"); got != 110 {
		t.Fatalf("health_beauty revenue = %v, want 110", got)
	}
	if got := categoryRevenue(envelopeRows(t, patch.Visuals["category_revenue"]), "watches_gifts"); got != 220 {
		t.Fatalf("watches_gifts revenue = %v, want 220", got)
	}
	if got := categoryRevenueTotal(envelopeRows(t, patch.Visuals["category_revenue"])); got != 330 {
		t.Fatalf("category revenue total = %v, want 330 without cross-matched tuples", got)
	}

	malformedFilters := dashboard.Filters{
		Selections: []dashboard.InteractionSelection{
			compositeInteractionSelection("visual", "orders_table", "row_selection",
				map[string]string{"orders.order_id": "o1", "orders.status": "delivered", "orders.unknown": "health_beauty"},
				map[string]string{"orders.order_id": "o4", "orders.status": "shipped", "orders.category": "watches_gifts"},
			),
		},
	}
	patch, err = metrics.QueryDashboardPage(context.Background(), "executive-sales", "overview", malformedFilters)
	require.NoError(t, err)
	if patch.Status.Error == "" {
		t.Fatalf("malformed tuple selection was not rejected: %#v", patch)
	}
	if len(patch.Visuals) != 0 {
		t.Fatalf("malformed tuple returned visual data: %#v", patch.Visuals)
	}
}

func TestServicePowerFilters(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "olist_orders_dataset.csv", `order_id,customer_id,order_status,order_purchase_timestamp,order_approved_at,order_delivered_carrier_date,order_delivered_customer_date,order_estimated_delivery_date
o1,c1,delivered,2018-01-10 10:00:00,2018-01-10 11:00:00,2018-01-11 10:00:00,2018-01-14 10:00:00,2018-01-20 10:00:00
o2,c2,shipped,2017-06-10 10:00:00,2017-06-10 11:00:00,2017-06-11 10:00:00,2017-06-20 10:00:00,2017-06-25 10:00:00
`)
	writeFixture(t, dir, "olist_order_items_dataset.csv", `order_id,order_item_id,product_id,seller_id,shipping_limit_date,price,freight_value
o1,1,p1,s1,2018-01-12 10:00:00,100.00,10.00
o2,1,p2,s2,2017-06-12 10:00:00,50.00,5.00
`)
	writeFixture(t, dir, "olist_order_payments_dataset.csv", `order_id,payment_sequential,payment_type,payment_installments,payment_value
o1,1,credit_card,1,110.00
o2,1,credit_card,1,55.00
`)
	writeFixture(t, dir, "olist_products_dataset.csv", `product_id,product_category_name,product_name_lenght,product_description_lenght,product_photos_qty,product_weight_g,product_length_cm,product_height_cm,product_width_cm
p1,beleza_saude,10,20,1,500,20,10,15
p2,relogios_presentes,12,24,1,600,22,11,16
`)
	writeFixture(t, dir, "olist_customers_dataset.csv", `customer_id,customer_unique_id,customer_zip_code_prefix,customer_city,customer_state
c1,u1,01001,Sao Paulo,SP
c2,u2,20000,Rio de Janeiro,RJ
`)
	writeFixture(t, dir, "olist_order_reviews_dataset.csv", `review_id,order_id,review_score,review_comment_title,review_comment_message,review_creation_date,review_answer_timestamp
r1,o1,5,,,2018-01-15,2018-01-15 10:00:00
r2,o2,3,,,2017-06-21,2017-06-21 10:00:00
`)
	writeFixture(t, dir, "product_category_name_translation.csv", `product_category_name,product_category_name_english
beleza_saude,health_beauty
relogios_presentes,watches_gifts
`)

	metrics, err := newLegacyRuntime(t, dir)
	require.NoError(t, err)
	defer metrics.Close()

	tests := []struct {
		name    string
		filters dashboard.Filters
		want    string
	}{
		{
			name: "multi state",
			filters: compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
				"state": setExpression("SP", "RJ"),
			}),
			want: "2",
		},
		{
			name: "category contains",
			filters: compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
				"category": comparisonExpression(dashboardfilter.OperatorContains, "watch"),
			}),
			want: "1",
		},
		{
			name: "category equals",
			filters: compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
				"category": comparisonExpression(dashboardfilter.OperatorEquals, "health_beauty"),
			}),
			want: "1",
		},
		{
			name: "custom date range",
			filters: compiledFiltersForTest(t, metrics, "executive-sales", "overview", map[string]dashboardfilter.Expression{
				"purchase_date": rangeExpression(dashboardfilter.ValueDate, "2018-01-01", "2018-01-31"),
			}),
			want: "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch, err := metrics.QueryDashboard(context.Background(), "executive-sales", tt.filters)
			require.NoError(t, err)
			if got := fmt.Sprint(datumInt(envelopeRows(t, patch.Visuals["total_orders"])[0], "value")); got != tt.want {
				t.Fatalf("orders KPI value = %q, want %s", got, tt.want)
			}
		})
	}
}

func interactionSelection(sourceKind, sourceID, interactionKind, field string, values ...string) dashboard.InteractionSelection {
	fact := ""
	if parts := strings.SplitN(field, ".", 2); len(parts) == 2 && field != dashboard.UIRowSelectionField {
		fact = parts[0]
	}
	entries := make([]dashboard.InteractionSelectionEntry, 0, len(values))
	for _, value := range values {
		entries = append(entries, dashboard.InteractionSelectionEntry{
			Mappings: []dashboard.InteractionSelectionMapping{{
				Field: field,
				Fact:  fact,
				Value: value,
				Label: value,
			}},
			Label: value,
		})
	}
	return dashboard.InteractionSelection{
		ID:              sourceKind + ":" + sourceID + ":" + interactionKind,
		SourceKind:      sourceKind,
		SourceID:        sourceID,
		InteractionKind: interactionKind,
		Entries:         entries,
		Label:           strings.Join(values, ", "),
	}
}

func compiledFiltersForTest(
	t *testing.T,
	metrics *Service,
	dashboardID string,
	pageID string,
	expressions map[string]dashboardfilter.Expression,
) dashboard.Filters {
	t.Helper()
	definition, _, ok := metrics.Report(dashboardID)
	if !ok {
		t.Fatalf("dashboard %q not found", dashboardID)
	}
	page, ok := definition.PageOrDefault(pageID)
	if !ok {
		t.Fatalf("dashboard page %q not found", pageID)
	}
	state := definition.DefaultFilterState()
	for bindingID, expression := range expressions {
		var binding dashboardfilter.Binding
		found := false
		for _, candidate := range definition.CompiledFilterBindings() {
			if candidate.ID == bindingID && (candidate.Scope == dashboardfilter.ScopeReport || candidate.PageID == page.ID) {
				binding, found = candidate, true
				break
			}
		}
		if !found {
			t.Fatalf("filter binding %q not found on page %q", bindingID, page.ID)
		}
		filterDefinition := definition.FilterDefinitions[binding.Filter]
		canonical, err := dashboardfilter.Canonicalize(expression, filterDefinition.ValueKind)
		if err != nil {
			t.Fatalf("canonicalize filter binding %q: %v", bindingID, err)
		}
		state.AppliedControls[binding.Key] = dashboardfilter.AppliedState{
			Expression: canonical, ResolvedExpression: canonical,
		}
	}
	return dashboard.Filters{CompiledState: &state, ActivePageID: page.ID}.WithDefaults()
}

func setExpression(values ...string) dashboardfilter.Expression {
	typed := make([]dashboardfilter.Value, len(values))
	for index, value := range values {
		typed[index] = dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value}
	}
	return dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionSet, Operator: dashboardfilter.OperatorIn, Values: typed,
	}
}

func comparisonExpression(operator dashboardfilter.Operator, value string) dashboardfilter.Expression {
	return dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionComparison, Operator: operator,
		Value: &dashboardfilter.Value{Kind: dashboardfilter.ValueString, Value: value},
	}
}

func rangeExpression(kind dashboardfilter.ValueKind, lower, upper string) dashboardfilter.Expression {
	return dashboardfilter.Expression{
		Kind: dashboardfilter.ExpressionRange,
		Lower: &dashboardfilter.Bound{
			Value: dashboardfilter.Value{Kind: kind, Value: lower}, Inclusive: true,
		},
		Upper: &dashboardfilter.Bound{
			Value: dashboardfilter.Value{Kind: kind, Value: upper}, Inclusive: true,
		},
	}
}

func compositeInteractionSelection(sourceKind, sourceID, interactionKind string, tuples ...map[string]string) dashboard.InteractionSelection {
	entries := make([]dashboard.InteractionSelectionEntry, 0, len(tuples))
	for _, tuple := range tuples {
		fields := make([]string, 0, len(tuple))
		for field := range tuple {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		entry := dashboard.InteractionSelectionEntry{}
		labels := make([]string, 0, len(fields))
		for _, field := range fields {
			value := tuple[field]
			fact := ""
			if parts := strings.SplitN(field, ".", 2); len(parts) == 2 && field != dashboard.UIRowSelectionField {
				fact = parts[0]
			}
			entry.Mappings = append(entry.Mappings, dashboard.InteractionSelectionMapping{
				Field: field,
				Fact:  fact,
				Value: value,
				Label: value,
			})
			labels = append(labels, value)
		}
		entry.Label = strings.Join(labels, ", ")
		entries = append(entries, entry)
	}
	return dashboard.InteractionSelection{
		ID:              sourceKind + ":" + sourceID + ":" + interactionKind,
		SourceKind:      sourceKind,
		SourceID:        sourceID,
		InteractionKind: interactionKind,
		Entries:         entries,
	}
}

func exactTableRows(t *testing.T, table dashboard.Table) int {
	t.Helper()
	value, exact := table.Cardinality.ExactValue()
	if !exact {
		t.Fatalf("table cardinality = %#v, want exact", table.Cardinality)
	}
	return value
}

func (m *Service) queryTableForTest(ctx context.Context, dashboardID string, filters dashboard.Filters, request dashboard.TableRequest) (dashboard.Table, error) {
	return m.visualizations.queryTablePage(ctx, dashboardID, "", filters, request, true)
}

func specBase(t *testing.T, envelope visualizationir.VisualizationEnvelope) visualizationir.VisualizationSpecBase {
	t.Helper()
	base, err := visualizationir.SpecificationBase(envelope.Spec)
	if err != nil {
		t.Fatalf("visualization %q spec: %v", envelope.VisualID, err)
	}
	return base
}

func specKind(t *testing.T, envelope visualizationir.VisualizationEnvelope) string {
	t.Helper()
	return specBase(t, envelope).Kind
}

func cartesianMark(t *testing.T, envelope visualizationir.VisualizationEnvelope) visualizationir.VisualizationCartesianMark {
	t.Helper()
	spec, ok := envelope.Spec.Value.(*visualizationir.CartesianVisualizationSpec)
	if !ok {
		t.Fatalf("visualization %q spec = %T, want cartesian", envelope.VisualID, envelope.Spec.Value)
	}
	return spec.Mark
}

func envelopeRows(t *testing.T, envelope visualizationir.VisualizationEnvelope) []dashboard.Datum {
	t.Helper()
	var columns []string
	var rows [][]any
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		if len(state.Datasets) != 1 {
			t.Fatalf("visualization %q datasets = %d, want 1", envelope.VisualID, len(state.Datasets))
		}
		columns, rows = state.Datasets[0].Columns, state.Datasets[0].Rows
	default:
		t.Fatalf("visualization %q data state = %T, want inline", envelope.VisualID, envelope.DataState.Value)
	}
	data := make([]dashboard.Datum, len(rows))
	for rowIndex, row := range rows {
		if len(row) != len(columns) {
			t.Fatalf("visualization %q row %d width = %d, want %d", envelope.VisualID, rowIndex, len(row), len(columns))
		}
		data[rowIndex] = dashboard.Datum{}
		for columnIndex, column := range columns {
			data[rowIndex][column] = row[columnIndex]
		}
	}
	return data
}

func categoryRevenue(data []dashboard.Datum, label string) float64 {
	for _, row := range data {
		if datumString(row, "label") == label {
			return datumFloat(row["value"])
		}
	}
	return 0
}

func categoryRevenueTotal(data []dashboard.Datum) float64 {
	var total float64
	for _, row := range data {
		total += datumFloat(row["value"])
	}
	return total
}

func selectedEnvelopeEntryValue(entries []visualizationir.VisualizationSelectionEntry, field string) string {
	for _, entry := range entries {
		if value, ok := entry.Datum.Identity[field]; ok {
			return fmt.Sprint(value)
		}
	}
	return ""
}

func selectedEntryValue(entries []dashboard.InteractionSelectionEntry, field string) string {
	for _, entry := range entries {
		for _, mapping := range entry.Mappings {
			if mapping.Field == field {
				return fmt.Sprint(mapping.Value)
			}
		}
	}
	return ""
}

func removeString(values []string, value string) []string {
	next := make([]string, 0, len(values))
	for _, candidate := range values {
		if candidate != value {
			next = append(next, candidate)
		}
	}
	return next
}
