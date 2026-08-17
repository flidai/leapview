package planir

import (
	"strings"
	"testing"
)

func validPlan() *Graph {
	lineage := []PhysicalLineage{
		{Logical: "id", Dataset: "orders", Field: "id"},
		{Logical: "amount", Dataset: "orders", Field: "amount"},
	}
	scanMeta := NodeMeta{NodeID: "scan", OutputGrain: Grain{Fields: []string{"id"}}, AvailableFields: []Field{{Name: "id", Type: "string"}, {Name: "amount", Type: "decimal"}, {Name: "status", Type: "string"}}, RootDatasets: []string{"orders"}, FilterPhase: FilterPhaseScan, PhysicalLineage: lineage}
	filterMeta := scanMeta
	filterMeta.NodeID = "filter"
	aggregateMeta := NodeMeta{NodeID: "aggregate", OutputGrain: Grain{Fields: []string{"id"}}, AvailableFields: []Field{{Name: "id", Type: "string"}}, AvailableMetrics: []Metric{{Name: "revenue", Type: "decimal"}}, RootDatasets: []string{"orders"}, FilterPhase: FilterPhaseAggregate, PhysicalLineage: []PhysicalLineage{{Logical: "id", Dataset: "orders", Field: "id"}, {Logical: "revenue", Dataset: "orders", Field: "amount"}}}
	return &Graph{
		NodeMeta: NodeMeta{OutputGrain: aggregateMeta.OutputGrain, AvailableFields: aggregateMeta.AvailableFields, AvailableMetrics: aggregateMeta.AvailableMetrics, RootDatasets: []string{"orders"}},
		Roots:    []string{"scan"}, Output: "aggregate",
		Nodes: map[string]Node{
			"scan":      ScanDataset{NodeMeta: scanMeta, Dataset: "orders"},
			"filter":    FilterRows{NodeMeta: filterMeta, Input: "scan", Predicate: Predicate{Kind: PredicateCompare, Field: "status", Operator: "=", Value: Literal{Kind: LiteralString, String: "paid"}}, Source: FilterSourceRequest, Fields: []string{"status"}},
			"aggregate": AggregateMetrics{NodeMeta: aggregateMeta, Input: "filter", GroupBy: []string{"id"}, Metrics: []MetricSpec{{Name: "revenue", Aggregation: "SUM", Input: "amount"}}},
		},
	}
}

func TestValidateValidPlanAndDuckDBRender(t *testing.T) {
	graph := validPlan()
	if err := graph.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	want := `SELECT "id", SUM("amount") AS "revenue" FROM "orders" WHERE "status" = ? GROUP BY "id"`
	if rendered.SQL != want {
		t.Fatalf("SQL = %q, want %q", rendered.SQL, want)
	}
	if len(rendered.Args) != 1 || rendered.Args[0] != "paid" {
		t.Fatalf("Args = %#v, want [paid]", rendered.Args)
	}
}

func TestValidateRejectsInvalidTopology(t *testing.T) {
	graph := validPlan()
	filter := graph.Nodes["filter"].(FilterRows)
	filter.Input = "missing"
	graph.Nodes["filter"] = filter
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), `input "missing" does not exist`) {
		t.Fatalf("Validate() error = %v, want missing input", err)
	}
}

func TestValidateRejectsCycle(t *testing.T) {
	graph := validPlan()
	a := graph.Nodes["filter"].(FilterRows)
	a.Input = "aggregate"
	graph.Nodes["filter"] = a
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Validate() error = %v, want cycle", err)
	}
}

func TestValidateRejectsGrainPhaseAndLineage(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.NodeMeta.OutputGrain = Grain{Fields: []string{"wrong"}}
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "output grain") {
		t.Fatalf("grain Validate() error = %v", err)
	}

	graph = validPlan()
	filter := graph.Nodes["filter"].(FilterRows)
	filter.FilterPhase = FilterPhase("not-a-phase")
	graph.Nodes["filter"] = filter
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "filter phase") {
		t.Fatalf("phase Validate() error = %v", err)
	}

	graph = validPlan()
	scan := graph.Nodes["scan"].(ScanDataset)
	scan.PhysicalLineage = []PhysicalLineage{{Logical: "id", Dataset: "orders"}}
	graph.Nodes["scan"] = scan
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "physical lineage") {
		t.Fatalf("lineage Validate() error = %v", err)
	}
}

func TestFingerprintIndependentOfMapAndSetOrder(t *testing.T) {
	left := validPlan()
	right := validPlan()
	right.Roots = []string{"scan"}
	// Reordering these collections must not alter the canonical plan.
	scan := right.Nodes["scan"].(ScanDataset)
	scan.AvailableFields = []Field{{Name: "status", Type: "string"}, {Name: "id", Type: "string"}, {Name: "amount", Type: "decimal"}}
	right.Nodes = map[string]Node{"aggregate": right.Nodes["aggregate"], "scan": scan, "filter": right.Nodes["filter"]}
	leftFingerprint, err := left.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	rightFingerprint, err := right.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("fingerprints differ: %s != %s", leftFingerprint, rightFingerprint)
	}
	leftCanonical, _ := left.Canonical()
	rightCanonical, _ := right.Canonical()
	if string(leftCanonical) != string(rightCanonical) {
		t.Fatalf("canonical plans differ:\n%s\n%s", leftCanonical, rightCanonical)
	}
}

func TestBundleBranchesPreserveOrderedIdentityAndHeterogeneousShapes(t *testing.T) {
	graph := validPlan()
	secondMeta := NodeMeta{NodeID: "aggregate_second", OutputGrain: Grain{Fields: []string{"status"}}, AvailableFields: []Field{{Name: "status", Type: "string"}}, AvailableMetrics: []Metric{{Name: "revenue_second", Type: "decimal"}}, RootDatasets: []string{"orders"}, FilterPhase: FilterPhaseAggregate}
	graph.Nodes[secondMeta.NodeID] = AggregateMetrics{NodeMeta: secondMeta, Input: "filter", GroupBy: []string{"status"}, Metrics: []MetricSpec{{Name: "revenue_second", Aggregation: "SUM", Input: "amount"}}}
	envelope := NodeMeta{NodeID: "bundle", RootDatasets: []string{"orders"}, FilterPhase: FilterPhasePostAggregate}
	graph.Nodes["bundle"] = BundleBranches{NodeMeta: envelope, Branches: []BundleBranch{{ID: "daily", Ordinal: 0, Input: "aggregate"}, {ID: "status", Ordinal: 1, Input: "aggregate_second"}}}
	graph.Output = "bundle"
	graph.NodeMeta = envelope
	if err := graph.Validate(); err != nil {
		t.Fatalf("heterogeneous bundle Validate() error = %v", err)
	}
	first, err := graph.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	bundle := graph.Nodes["bundle"].(BundleBranches)
	bundle.Branches[0], bundle.Branches[1] = bundle.Branches[1], bundle.Branches[0]
	graph.Nodes["bundle"] = bundle
	second, err := graph.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("bundle branch identity/order was omitted from fingerprint")
	}
}

func TestExplainDeterministicAndReadable(t *testing.T) {
	graph := validPlan()
	first, err := graph.Explain()
	if err != nil {
		t.Fatal(err)
	}
	second, err := graph.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Explain() is not deterministic")
	}
	for _, fragment := range []string{"PlanIR output=aggregate", "scan [ScanDataset]", "filter [FilterRows]", "aggregate [AggregateMetrics]", `predicate=status = "paid"`, "revenue"} {
		if !strings.Contains(first, fragment) {
			t.Errorf("Explain() missing %q in:\n%s", fragment, first)
		}
	}
}

func TestFilterSourceIdentityAndAggregateBarrier(t *testing.T) {
	requestGraph := validPlan()
	requestFingerprint, err := requestGraph.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	namedGraph := validPlan()
	named := namedGraph.Nodes["filter"].(FilterRows)
	named.Source = FilterSourceNamed
	named.Name = "paid_orders"
	namedGraph.Nodes["filter"] = named
	namedFingerprint, err := namedGraph.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if requestFingerprint == namedFingerprint {
		t.Fatal("filter provenance did not affect the plan fingerprint")
	}
	explained, err := namedGraph.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explained, "source=named name=paid_orders") {
		t.Fatalf("Explain() omitted named filter identity: %s", explained)
	}

	barrier := validPlan()
	aggregate := barrier.Nodes["aggregate"].(AggregateMetrics)
	postMeta := aggregate.NodeMeta
	postMeta.NodeID = "post_filter"
	postMeta.FilterPhase = FilterPhasePostAggregate
	barrier.Nodes[postMeta.NodeID] = FilterRows{
		NodeMeta: postMeta,
		Input:    "aggregate",
		Source:   FilterSourceRequest,
		Predicate: Predicate{Kind: PredicateCompare, Field: "id", Operator: "=", Value: Literal{
			Kind: LiteralString, String: "x",
		}},
		Fields: []string{"id"},
	}
	barrier.Output = postMeta.NodeID
	barrier.NodeMeta = postMeta
	if err := barrier.Validate(); err == nil || !strings.Contains(err.Error(), "filter crosses aggregate boundary") {
		t.Fatalf("post-aggregate filter Validate() error = %v", err)
	}
}

func TestRelationshipRoutesAreOrderedAndDependenciesDeterministic(t *testing.T) {
	graph := validPlan()
	edge := RelationshipPath{Name: "orders_customers", FromDataset: "orders", ToDataset: "customers", JoinKeys: []JoinKey{{From: "id", To: "id"}}}
	route := RelationshipRoute{RootDataset: "orders", Edges: []RelationshipPath{edge}}
	scan := graph.Nodes["scan"].(ScanDataset)
	scan.RelationshipRoutes = []RelationshipRoute{route}
	graph.Nodes["scan"] = scan
	filter := graph.Nodes["filter"].(FilterRows)
	filter.RelationshipRoutes = []RelationshipRoute{route}
	graph.Nodes["filter"] = filter
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	dependencies, err := graph.Dependencies()
	if err != nil {
		t.Fatal(err)
	}
	if !sameOrdered(dependencies.Datasets, []string{"customers", "orders"}) {
		t.Fatalf("datasets = %v", dependencies.Datasets)
	}
	if !sameOrdered(dependencies.RelationshipPaths, []string{"orders:orders_customers"}) {
		t.Fatalf("relationship paths = %v", dependencies.RelationshipPaths)
	}
	if !sameOrdered(dependencies.PhysicalFields, []string{"customers.id", "orders.amount", "orders.id"}) {
		t.Fatalf("physical fields = %v", dependencies.PhysicalFields)
	}

	bad := validPlan()
	badScan := bad.Nodes["scan"].(ScanDataset)
	badScan.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{
		edge, {Name: "orders_tags", FromDataset: "orders", ToDataset: "tags", JoinKeys: []JoinKey{{From: "id", To: "id"}}},
	}}}
	bad.Nodes["scan"] = badScan
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "not contiguous") {
		t.Fatalf("sibling-concatenated route Validate() error = %v", err)
	}
}

func TestDuckDBRendererRejectsNonAggregateOutput(t *testing.T) {
	graph := validPlan()
	delete(graph.Nodes, "aggregate")
	graph.Output = "filter"
	graph.NodeMeta = graph.Nodes["filter"].Meta()
	if _, err := RenderDuckDB(graph); err == nil || !strings.Contains(err.Error(), "supports AggregateMetrics output") {
		t.Fatalf("RenderDuckDB() error = %v, want unsupported output", err)
	}
}

func TestDuckDBRendererBindsMaliciousPredicateLiteral(t *testing.T) {
	graph := validPlan()
	filter := graph.Nodes["filter"].(FilterRows)
	filter.Predicate.Value = Literal{Kind: LiteralString, String: "paid' OR 1=1 --"}
	graph.Nodes["filter"] = filter
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.SQL, "paid") || strings.Contains(rendered.SQL, "1=1") {
		t.Fatalf("literal leaked into SQL: %s", rendered.SQL)
	}
	if len(rendered.Args) != 1 || rendered.Args[0] != "paid' OR 1=1 --" {
		t.Fatalf("Args = %#v", rendered.Args)
	}
}

func TestCountRequiresTypedInput(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Metrics = append(aggregate.Metrics, MetricSpec{Name: "rows", Aggregation: "COUNT"})
	aggregate.AvailableMetrics = append(aggregate.AvailableMetrics, Metric{Name: "rows", Type: "integer"})
	graph.AvailableMetrics = append(graph.AvailableMetrics, Metric{Name: "rows", Type: "integer"})
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "COUNT requires an input") {
		t.Fatalf("Validate() error = %v, want typed COUNT input error", err)
	}
}

func TestScalarFunctionUnaryAndExactNumberAreTyped(t *testing.T) {
	expression := ScalarExpr{Kind: ScalarFunction, Function: "coalesce", Children: []ScalarExpr{
		{Kind: ScalarNeg, Children: []ScalarExpr{{Kind: ScalarLiteral, Literal: Literal{Kind: LiteralNumber, NumberText: "9007199254740993.125"}}}},
		{Kind: ScalarMetricRef, Metric: "revenue"},
	}}
	if err := expression.validate(map[string]bool{"revenue": true}); err != nil {
		t.Fatalf("typed scalar expression rejected: %v", err)
	}
	if err := (ScalarExpr{Kind: ScalarFunction, Function: "coalesce", Children: []ScalarExpr{{Kind: ScalarMetricRef, Metric: "revenue"}}}).validate(map[string]bool{"revenue": true}); err == nil {
		t.Fatal("coalesce with one child was accepted")
	}
	graph := validPlan()
	filter := graph.Nodes["filter"].(FilterRows)
	filter.Predicate.Value = Literal{Kind: LiteralNumber, NumberText: "9007199254740993.125"}
	graph.Nodes["filter"] = filter
	canonical, err := graph.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), "9007199254740993.125") {
		t.Fatalf("canonical graph lost exact number token: %s", canonical)
	}
}
