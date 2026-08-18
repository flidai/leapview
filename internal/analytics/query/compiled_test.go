package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// mustNewCompiledPlanner is test-only shorthand for the activation API. The
// production package intentionally has no fallible-constructor-swallowing
// NewPlanner helper.
func mustNewCompiledPlanner(t testing.TB, model *semanticmodel.Model, options ...PlannerOption) *Planner {
	t.Helper()
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, options...)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	return planner
}

// populateFixtureTableModelNames mirrors the authored dataset-to-model
// lowering used by test fixtures. Runtime validation intentionally requires
// this binding to be explicit; production code must never infer it.
func populateFixtureTableModelNames(model *semanticmodel.Model) {
	if model == nil {
		return
	}
	for alias, dataset := range model.Datasets {
		modelName := dataset.Model
		if modelName == "" {
			modelName = alias
		}
		table, ok := model.Tables[modelName]
		if !ok {
			continue
		}
		table.ModelName = dataset.Model
		model.Tables[modelName] = table
	}
}

func TestCompileModelBuildsReusableMetricDependencyMetadata(t *testing.T) {
	model := testModel()
	model.Metrics["nested_ratio"] = semanticmodel.Metric{Type: "derived", Expression: "${tags_per_order} * 100"}
	model.Metrics["net_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders",
		Aggregation: "sum",
		Input:       &semanticmodel.MetricInput{Field: "orders.revenue"},
		Empty:       "zero",
	}
	orders := model.Tables["orders"]
	orders.Dimensions["discount"] = semanticmodel.MetricDimension{Type: "number", Datatype: semanticmodel.DataTypeDecimal}
	model.Tables["orders"] = orders
	populateFixtureTableModelNames(model)

	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	nested, ok := compiled.metric("nested_ratio")
	if !ok || !reflect.DeepEqual(nested.RootDatasets, []string{"orders", "tags"}) {
		t.Fatalf("nested metric roots = %#v", nested.RootDatasets)
	}
	tagsPerOrder, ok := compiled.metric("tags_per_order")
	if !ok || tagsPerOrder.Derived == nil || len(tagsPerOrder.Derived.Expression.References()) == 0 {
		t.Fatal("metric expression was not compiled")
	}
	if tagsPerOrder.Aggregate != nil || tagsPerOrder.Ratio != nil {
		t.Fatal("derived metric has more than one typed payload")
	}
}

func TestCompiledDatasetTableGetterReturnsDetachedMetadata(t *testing.T) {
	model := testModel()
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	dataset, ok := compiled.Dataset("orders")
	if !ok {
		t.Fatal("orders dataset missing")
	}
	detached := dataset.Table()
	detached.Dimensions["order_id"] = semanticmodel.MetricDimension{Label: "mutated"}
	detached.Entities["order"] = semanticmodel.ModelEntitySpec{Type: "unique", Fields: []string{"status"}}
	detached.Description = "mutated"
	current := dataset.Table()
	if current.Dimensions["order_id"].Label == "mutated" || current.Entities["order"].Type != "primary" || current.Description == "mutated" {
		t.Fatal("CompiledDataset.Table() exposed mutable serving metadata")
	}
}

func TestCompileDatasetBindingsAllowsAuthoredSemanticDimensions(t *testing.T) {
	model := testModel()
	populateFixtureTableModelNames(model)
	compiled, err := CompileDatasetBindings(model)
	if err != nil {
		t.Fatalf("CompileDatasetBindings() with authored semantic dimensions: %v", err)
	}
	if _, ok := compiled.DimensionBinding("customer_state", "orders"); ok {
		t.Fatal("dataset-only compilation unexpectedly retained semantic lineage facts")
	}
}

func TestCompiledFactAccessorsReturnDetachedMetadata(t *testing.T) {
	model := rolePlayingDateModel()
	populateFixtureTableModelNames(model)
	metric := model.Metrics["order_count"]
	metric.TimeDimension = "order_date"
	model.Metrics["order_count"] = metric
	model.Tables["orders"].Dimensions["order_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger, AIContext: &semanticmodel.AIContext{Instructions: "authoring-only"}}
	model.Relationships[0].AIContext = &semanticmodel.AIContext{Instructions: "authoring-only"}
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}

	semantic, ok := compiled.SemanticDimension("activity_date")
	if !ok {
		t.Fatal("compiled semantic dimension missing")
	}
	semantic.Grains[0] = "mutated"
	semanticAgain, _ := compiled.SemanticDimension("activity_date")
	if semanticAgain.Grains[0] == "mutated" {
		t.Fatal("semantic dimension grains were not detached")
	}

	dimension, ok := compiled.DimensionBinding("order_date", "orders")
	if !ok || len(dimension.Path) == 0 || dimension.Physical.AIContext != nil {
		t.Fatalf("compiled dimension binding = %#v", dimension)
	}
	dimension.Path[0].ID = "mutated"
	dimension.Path[0].FromFields[0] = "mutated"
	dimensionAgain, _ := compiled.DimensionBinding("order_date", "orders")
	if dimensionAgain.Path[0].ID == "mutated" || dimensionAgain.Path[0].FromFields[0] == "mutated" {
		t.Fatal("dimension binding route was not detached")
	}

	field, ok := compiled.FieldBinding("orders", "customers.state")
	if !ok || len(field.Path) == 0 || field.Physical.AIContext != nil {
		t.Fatalf("compiled field binding = %#v", field)
	}
	field.Path[0].ToFields[0] = "mutated"
	fieldAgain, _ := compiled.FieldBinding("orders", "customers.state")
	if fieldAgain.Path[0].ToFields[0] == "mutated" {
		t.Fatal("field binding route was not detached")
	}

	relationshipPath, err := compiled.RelationshipPath("orders", "customers")
	if err != nil || len(relationshipPath) == 0 || relationshipPath[0].AIContext != nil {
		t.Fatalf("compiled relationship path = %#v, error=%v", relationshipPath, err)
	}
	relationshipPath[0].FromFields[0] = "mutated"
	pathAgain, _ := compiled.RelationshipPath("orders", "customers")
	if pathAgain[0].FromFields[0] == "mutated" {
		t.Fatal("relationship path was not detached")
	}

	compiledMetric, ok := compiled.Metric("order_count")
	if !ok || compiledMetric.Aggregate == nil || compiledMetric.Aggregate.InputPhysical.AIContext != nil || len(compiledMetric.Lineage.Entries) == 0 {
		t.Fatalf("compiled metric = %#v", compiledMetric)
	}
	compiledMetric.Aggregate.InputPath = append(compiledMetric.Aggregate.InputPath, semanticmodel.Relationship{ID: "mutated"})
	compiledMetric.Lineage.Entries[0].Path[0].ID = "mutated"
	metricAgain, _ := compiled.Metric("order_count")
	if len(metricAgain.Aggregate.InputPath) != 0 || metricAgain.Lineage.Entries[0].Path[0].ID == "mutated" {
		t.Fatal("compiled metric lineage was not detached")
	}
}

func TestCompileModelRetainsAggregateMetricDatasetMetadata(t *testing.T) {
	model := testModel()
	model.Metrics["gross_revenue"] = semanticmodel.Metric{
		Type: "aggregate", Dataset: "orders", Aggregation: "sum",
		Input: &semanticmodel.MetricInput{Field: "orders.revenue"},
	}
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := compiled.metric("gross_revenue")
	if !ok || !reflect.DeepEqual(metric.RootDatasets, []string{"orders"}) {
		t.Fatalf("aggregate metric roots = %#v, want [orders]", metric.RootDatasets)
	}
	if metric.Aggregate == nil || metric.Derived != nil || metric.Ratio != nil {
		t.Fatal("aggregate metric payload is not closed")
	}
}

func TestCompiledLineageFactsRetainRolePlayingRoutesAndIgnoreMutation(t *testing.T) {
	model := rolePlayingDateModel()
	planner := mustNewCompiledPlanner(t, model)
	orderDate, ok := planner.CompiledModel().DimensionBinding("order_date", "orders")
	if !ok {
		t.Fatal("compiled order_date binding missing")
	}
	shipDate, ok := planner.CompiledModel().DimensionBinding("ship_date", "orders")
	if !ok {
		t.Fatal("compiled ship_date binding missing")
	}
	if relationshipPathSignature(orderDate.Path) == relationshipPathSignature(shipDate.Path) {
		t.Fatalf("role-playing routes collapsed: order=%v ship=%v", orderDate.Path, shipDate.Path)
	}
	if orderDate.Physical.Field != "dates.date_value" || shipDate.Physical.Field != "dates.date_value" {
		t.Fatalf("compiled role-playing physical fields = %q, %q", orderDate.Physical.Field, shipDate.Physical.Field)
	}

	request := Request{Dimensions: []Field{{Field: "order_date"}, {Field: "ship_date"}}, Metrics: []Field{{Field: "order_count"}}}
	before, err := planner.Plan(request)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := before.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}

	// Mutate the caller's authoring maps. PlanIR lowering must continue to
	// consume activation facts rather than re-resolving the source model.
	model.Dimensions["order_date"].Bindings["orders"] = semanticmodel.DimensionBinding{Field: "orders.ordered_date_id"}
	model.Relationships = nil

	after, err := planner.Plan(request)
	if err != nil {
		t.Fatal(err)
	}
	afterFingerprint, err := after.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if beforeFingerprint != afterFingerprint {
		t.Fatalf("PlanIR changed after authored/snapshot lineage mutation:\nbefore=%s\nafter=%s", beforeFingerprint, afterFingerprint)
	}
}

func TestCompiledMetricInputLineageIgnoresTableMutation(t *testing.T) {
	model := testModel()
	planner := mustNewCompiledPlanner(t, model)
	compiled, ok := planner.CompiledModel().Metric("order_count")
	if !ok || compiled.Aggregate == nil {
		t.Fatal("compiled order_count aggregate missing")
	}
	if compiled.Aggregate.InputPhysical.Field != "orders.order_id" || len(compiled.Aggregate.InputPath) != 0 {
		t.Fatalf("compiled metric input lineage = %#v path=%v", compiled.Aggregate.InputPhysical, compiled.Aggregate.InputPath)
	}
	model.Tables["orders"].Dimensions["order_id"] = semanticmodel.MetricDimension{Type: "string", Datatype: semanticmodel.DataTypeString}
	dataset := planner.compiled.datasets["orders"]
	delete(dataset.table.Dimensions, "order_id")
	planner.compiled.datasets["orders"] = dataset
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.SQL, `COUNT("orders"."order_id")`) {
		t.Fatalf("compiled input lineage was not used after source mutation: %s", plan.SQL)
	}
}

func TestPlannerRequestBoundariesIgnoreAuthoredLineageMutation(t *testing.T) {
	model := rolePlayingDateModel()
	planner := mustNewCompiledPlanner(t, model)
	filter := Filter{Field: "ship_date", Path: []string{"orders_ship_date"}, Operator: "equals", Values: []any{"2026-08-01"}}
	requests := []struct {
		name string
		plan func() (Plan, error)
	}{
		{name: "aggregate", plan: func() (Plan, error) {
			return planner.Plan(Request{Dataset: "orders", Dimensions: []Field{{Field: "order_date"}}, Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{filter}})
		}},
		{name: "row", plan: func() (Plan, error) {
			return planner.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "order_date"}}, Metrics: []Field{{Field: "order_count"}}, Filters: []Filter{filter}})
		}},
		{name: "raw", plan: func() (Plan, error) {
			return planner.PlanRawValues(RawValueRequest{Dataset: "orders", Dimensions: []Field{{Field: "order_date"}}, Metric: Field{Field: "order_count"}, Filters: []Filter{filter}})
		}},
		{name: "count", plan: func() (Plan, error) {
			return planner.PlanCount(CountRequest{Dataset: "orders", Filters: []Filter{filter}})
		}},
	}
	fingerprints := make(map[string]string, len(requests))
	for _, request := range requests {
		plan, err := request.plan()
		if err != nil {
			t.Fatalf("%s before mutation: %v", request.name, err)
		}
		fingerprint, err := plan.IR.Fingerprint()
		if err != nil {
			t.Fatalf("%s fingerprint before mutation: %v", request.name, err)
		}
		fingerprints[request.name] = fingerprint
	}

	model.Dimensions["order_date"].Bindings["orders"] = semanticmodel.DimensionBinding{Field: "dates.date_value", Path: []string{"orders_ship_date"}}
	model.Relationships = nil
	dates := model.Tables["dates"]
	dates.Dimensions["date_value"] = semanticmodel.MetricDimension{Type: "string", Datatype: semanticmodel.DataTypeString}
	model.Tables["dates"] = dates
	for _, request := range requests {
		plan, err := request.plan()
		if err != nil {
			t.Fatalf("%s after mutation: %v", request.name, err)
		}
		fingerprint, err := plan.IR.Fingerprint()
		if err != nil {
			t.Fatalf("%s fingerprint after mutation: %v", request.name, err)
		}
		if fingerprint != fingerprints[request.name] {
			t.Fatalf("%s changed after authored lineage mutation", request.name)
		}
	}

	multiModel := executableMultiDatasetModel()
	multiPlanner := mustNewCompiledPlanner(t, multiModel)
	multiRequest := Request{Dimensions: []Field{{Field: "customer"}}, Metrics: []Field{{Field: "order_count"}, {Field: "tag_count"}}}
	multiPlan, err := multiPlanner.Plan(multiRequest)
	if err != nil {
		t.Fatalf("multi-dataset before mutation: %v", err)
	}
	multiFingerprint, err := multiPlan.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	multiModel.Dimensions["customer"].Bindings["orders"] = semanticmodel.DimensionBinding{Field: "orders.segment"}
	multiModel.Relationships = nil
	multiPlan, err = multiPlanner.Plan(multiRequest)
	if err != nil {
		t.Fatalf("multi-dataset after mutation: %v", err)
	}
	afterMultiFingerprint, err := multiPlan.IR.Fingerprint()
	if err != nil || afterMultiFingerprint != multiFingerprint {
		t.Fatalf("multi-dataset changed after authored lineage mutation: %v", err)
	}

	bundleRequests := []BundleRequest{
		{ID: "count", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "order_count"}}}},
		{ID: "customer", Request: Request{Dataset: "orders", Dimensions: []Field{{Field: "customer"}}, Metrics: []Field{{Field: "revenue"}}}},
	}
	bundle, err := multiPlanner.PlanBundle(bundleRequests)
	if err != nil {
		t.Fatalf("bundle before mutation: %v", err)
	}
	bundleFingerprint, err := bundle.Plan.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	multiModel.Dimensions["customer"].Bindings["orders"] = semanticmodel.DimensionBinding{Field: "orders.order_id"}
	bundle, err = multiPlanner.PlanBundle(bundleRequests)
	if err != nil {
		t.Fatalf("bundle after mutation: %v", err)
	}
	afterBundleFingerprint, err := bundle.Plan.IR.Fingerprint()
	if err != nil || afterBundleFingerprint != bundleFingerprint {
		t.Fatalf("bundle changed after authored lineage mutation: %v", err)
	}

	spatialModel := testModel()
	spatialPlanner := mustNewCompiledPlanner(t, spatialModel)
	spatialRequest := Request{Dataset: "orders", Dimensions: []Field{{Field: "orders.latitude"}, {Field: "orders.longitude"}}, Metrics: []Field{{Field: "order_count"}}, SpatialBucket: &SpatialBucket{Latitude: Field{Field: "orders.latitude"}, Longitude: Field{Field: "orders.longitude"}, Zoom: 4, CellPixels: 64}}
	spatialPlan, err := spatialPlanner.Plan(spatialRequest)
	if err != nil {
		t.Fatalf("spatial before mutation: %v", err)
	}
	spatialFingerprint, err := spatialPlan.IR.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	orders := spatialModel.Tables["orders"]
	orders.Dimensions["latitude"] = semanticmodel.MetricDimension{Type: "string", Datatype: semanticmodel.DataTypeString}
	spatialModel.Tables["orders"] = orders
	spatialPlan, err = spatialPlanner.Plan(spatialRequest)
	if err != nil {
		t.Fatalf("spatial after mutation: %v", err)
	}
	afterSpatialFingerprint, err := spatialPlan.IR.Fingerprint()
	if err != nil || afterSpatialFingerprint != spatialFingerprint {
		t.Fatalf("spatial changed after authored lineage mutation: %v", err)
	}
}

func TestCompileModelRetainsRatioPayloadWithoutExpression(t *testing.T) {
	model := testModel()
	model.Metrics["conversion"] = semanticmodel.Metric{Type: "ratio", Numerator: "tags_per_order", Denominator: "order_count"}
	populateFixtureTableModelNames(model)
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	metric, ok := compiled.metric("conversion")
	if !ok || metric.Ratio == nil || metric.Ratio.Numerator != "tags_per_order" || metric.Ratio.Denominator != "order_count" {
		t.Fatalf("ratio payload = %#v", metric.Ratio)
	}
	if metric.Aggregate != nil || metric.Derived != nil {
		t.Fatal("ratio metric has more than one typed payload")
	}
}

func TestCompiledMetricRejectsMultipleTypedPayloads(t *testing.T) {
	node := CompiledMetric{
		Name: "invalid", Type: "ratio",
		Derived: &CompiledDerivedMetric{}, Ratio: &CompiledRatioMetric{Numerator: "a", Denominator: "b"},
	}
	if err := node.validatePayload(); err == nil || !strings.Contains(err.Error(), "exactly one typed payload") {
		t.Fatalf("invalid payload error = %v", err)
	}
}

func TestPlannerLowersRatioPayloadAsComputeRatio(t *testing.T) {
	model := testModel()
	model.Metrics["tag_ratio"] = semanticmodel.Metric{Type: "ratio", Numerator: "tag_count", Denominator: "order_count"}
	planner := mustNewCompiledPlanner(t, model)
	node, ok := planner.compiled.metric("tag_ratio")
	if !ok || node.Ratio == nil || node.Derived != nil || node.Aggregate != nil {
		t.Fatalf("compiled ratio payload = %#v", node)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "tag_ratio"}}})
	if err != nil {
		t.Fatal(err)
	}
	var ratio *planir.ComputeRatio
	for _, candidate := range plan.IR.Nodes {
		if value, ok := candidate.(planir.ComputeRatio); ok && value.Output == "tag_ratio" {
			copy := value
			ratio = &copy
			break
		}
	}
	if ratio == nil || ratio.Numerator != "tag_count" || ratio.Denominator != "order_count" {
		t.Fatalf("ratio PlanIR node = %#v", ratio)
	}
}

func TestPlannerLowersCanonicalAggregateMetricsWithoutMetricsDualWrite(t *testing.T) {
	model := testModel()
	model.Metrics["order_count"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"}
	model.Metrics["revenue"] = semanticmodel.Metric{Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero"}
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model)
	if err != nil {
		t.Fatalf("NewCompiledPlanner() error = %v", err)
	}
	plan, err := planner.Plan(Request{Metrics: []Field{{Field: "order_count"}, {Field: "tags_per_order"}}})
	if err != nil {
		t.Fatalf("canonical aggregate plan error = %v", err)
	}
	explain, err := plan.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "[AggregateMetrics]") || !strings.Contains(explain, "[ComputeDerived]") || strings.Contains(explain, "Measure") {
		t.Fatalf("canonical aggregate PlanIR = %s", explain)
	}
}

func TestPlannerRendersCompositeRelationshipJoinTuple(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders (customer_id INTEGER, order_id INTEGER)",
		"CREATE TABLE model.customers (customer_id INTEGER, order_id INTEGER, state VARCHAR)",
		"INSERT INTO model.orders VALUES (10, 1), (20, 2), (20, 99)",
		"INSERT INTO model.customers VALUES (10, 1, 'DK'), (20, 2, 'SE')",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	model := testModel()
	customers := model.Tables["customers"]
	customers.Dimensions["order_id"] = semanticmodel.MetricDimension{Datatype: semanticmodel.DataTypeInteger}
	customers.Entities["customer_order"] = semanticmodel.ModelEntitySpec{Type: "unique", Fields: []string{"customer_id", "order_id"}}
	model.Tables["customers"] = customers
	model.Relationships[0] = semanticmodel.Relationship{
		ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id", "order_id"},
		ToDataset: "customers", ToFields: []string{"customer_id", "order_id"}, Cardinality: "many_to_one",
	}
	populateFixtureTableModelNames(model)
	planner, err := NewCompiledPlanner(model, WithTableRelation(func(table string) (string, error) { return "model." + table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planner.Plan(Request{Dimensions: []Field{{Field: "customers.state"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatalf("composite relationship planner error = %v", err)
	}
	if !strings.Contains(plan.SQL, `"orders"."customer_id" = "r2"."customer_id" AND "orders"."order_id" = "r2"."order_id"`) {
		t.Fatalf("composite relationship join = %s", plan.SQL)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute composite relationship plan: %v\nSQL: %s", err, plan.SQL)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("composite relationship query returned no row")
	}
	var state string
	var count int
	if err := rows.Scan(&state, &count); err != nil {
		t.Fatal(err)
	}
	if state != "DK" || count != 1 {
		t.Fatalf("composite relationship result = (%s, %d), want (DK, 1)", state, count)
	}
}

func TestCompileModelFailsClosedForInvalidMetricDAG(t *testing.T) {
	model := testModel()
	model.Metrics["broken"] = semanticmodel.Metric{Type: "derived", Expression: "${missing_member} + 1"}
	if _, err := CompileModel(model); err == nil || !strings.Contains(err.Error(), "unknown metric") {
		t.Fatalf("unknown dependency error = %v", err)
	}

	model = testModel()
	model.Metrics["cycle_a"] = semanticmodel.Metric{Type: "derived", Expression: "${cycle_b}"}
	model.Metrics["cycle_b"] = semanticmodel.Metric{Type: "derived", Expression: "${cycle_a}"}
	if _, err := CompileModel(model); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle error = %v", err)
	}
}
