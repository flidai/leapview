package planir

import (
	"reflect"
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

func TestDuckDBRendererAliasesSnapshotScanForQualifiedFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		relation string
		wantFrom string
		aliased  bool
	}{
		{name: "snapshot expression", relation: "(FROM lake.model.orders AT (VERSION => 42))", wantFrom: `FROM (FROM lake.model.orders AT (VERSION => 42)) AS "orders"`, aliased: true},
		{name: "physical override", relation: "lake.model.orders", wantFrom: `FROM lake.model.orders AS "orders"`, aliased: true},
		{name: "default relation", wantFrom: `FROM "orders"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graph := validPlan()
			scan := graph.Nodes["scan"].(ScanDataset)
			scan.Relation = tc.relation
			scan.AvailableFields = append(scan.AvailableFields, Field{Name: "orders.amount", Type: "decimal"})
			scan.PhysicalLineage = append(scan.PhysicalLineage, PhysicalLineage{Logical: "orders.amount", Dataset: "orders", Field: "amount"})
			graph.Nodes["scan"] = scan
			filter := graph.Nodes["filter"].(FilterRows)
			filter.AvailableFields = append(filter.AvailableFields, Field{Name: "orders.amount", Type: "decimal"})
			filter.PhysicalLineage = append(filter.PhysicalLineage, PhysicalLineage{Logical: "orders.amount", Dataset: "orders", Field: "amount"})
			graph.Nodes["filter"] = filter
			aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
			aggregate.Metrics[0].Input = "orders.amount"
			graph.Nodes["aggregate"] = aggregate

			if err := graph.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			rendered, err := RenderDuckDB(graph)
			if err != nil {
				t.Fatalf("RenderDuckDB() error = %v", err)
			}
			if !strings.Contains(rendered.SQL, tc.wantFrom) {
				t.Fatalf("SQL does not contain expected relation form: %s", rendered.SQL)
			}
			if !tc.aliased && strings.Contains(rendered.SQL, `FROM "orders" AS "orders"`) {
				t.Fatalf("default relation unexpectedly received an explicit alias: %s", rendered.SQL)
			}
		})
	}
}

func TestDuckDBRenderRejectsUnavailableGroupField(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.GroupBy = []string{"missing"}
	graph.Nodes["aggregate"] = aggregate

	if _, err := RenderDuckDB(graph); err == nil || !strings.Contains(err.Error(), `group-by field "missing" is unavailable`) {
		t.Fatalf("RenderDuckDB() error = %v, want unavailable group field", err)
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

func TestSortLimitAcceptsProjectedAliasForQualifiedSource(t *testing.T) {
	graph := validPlan()
	delete(graph.Nodes, "filter")
	delete(graph.Nodes, "aggregate")
	scan := graph.Nodes["scan"].(ScanDataset)
	scan.AvailableFields = append(scan.AvailableFields, Field{Name: "orders.revenue", Type: "decimal"})
	scan.PhysicalLineage = append(scan.PhysicalLineage, PhysicalLineage{Logical: "orders.revenue", Dataset: "orders", Field: "revenue"})
	graph.Nodes["scan"] = scan
	sortMeta := scan.NodeMeta
	sortMeta.NodeID = "sort"
	sortMeta.FilterPhase = FilterPhasePostAggregate
	sortMeta.AvailableFields = []Field{{Name: "revenue", Type: "decimal"}}
	sortMeta.PhysicalLineage = nil
	graph.Nodes["sort"] = SortLimit{
		NodeMeta: sortMeta, Input: "scan",
		Sort:       []SortKey{{Field: "revenue"}},
		Projection: []Projection{{Name: "revenue", Source: "orders.revenue"}},
	}
	graph.Output = "sort"
	graph.NodeMeta = sortMeta
	if err := graph.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestSortLimitRejectsUnknownSortAlias(t *testing.T) {
	graph := validPlan()
	delete(graph.Nodes, "filter")
	delete(graph.Nodes, "aggregate")
	scan := graph.Nodes["scan"].(ScanDataset)
	sortMeta := scan.NodeMeta
	sortMeta.NodeID = "sort"
	sortMeta.FilterPhase = FilterPhasePostAggregate
	sortMeta.AvailableFields = []Field{{Name: "revenue", Type: "decimal"}}
	sortMeta.PhysicalLineage = nil
	graph.Nodes["sort"] = SortLimit{
		NodeMeta: sortMeta, Input: "scan",
		Sort:       []SortKey{{Field: "missing"}},
		Projection: []Projection{{Name: "revenue", Source: "amount"}},
	}
	graph.Output = "sort"
	graph.NodeMeta = sortMeta
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), `sort field "missing" is unavailable`) {
		t.Fatalf("Validate() error = %v, want unavailable sort alias", err)
	}
}

func totalRowsPlan() *Graph {
	graph := validPlan()
	delete(graph.Nodes, "aggregate")
	sortMeta := graph.Nodes["filter"].Meta()
	sortMeta.NodeID = "sort"
	sortMeta.FilterPhase = FilterPhasePostAggregate
	sortMeta.AvailableFields = []Field{{Name: "id", Type: "string"}, {Name: "amount", Type: "decimal"}}
	sortMeta.AvailableMetrics = nil
	sortMeta.PhysicalLineage = nil
	graph.Nodes["sort"] = SortLimit{NodeMeta: sortMeta, Input: "filter", Sort: []SortKey{{Field: "id"}}, Projection: []Projection{{Name: "id", Source: "id"}, {Name: "amount", Source: "amount"}}, Limit: 1, Offset: 1}
	graph.Output = "sort"
	graph.NodeMeta = sortMeta
	return graph
}

func TestWithTotalRowsRendersFilteredPopulationBeforePagination(t *testing.T) {
	graph := totalRowsPlan()
	withTotal, err := WithTotalRows(graph, "__leapview_total_rows")
	if err != nil {
		t.Fatal(err)
	}
	if graph.Output != "sort" {
		t.Fatal("WithTotalRows mutated input graph")
	}
	if err := withTotal.Validate(); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderDuckDB(withTotal)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`COUNT(*) OVER () AS "__leapview_total_rows"`,
		`WHERE "status" = ?`,
		`ORDER BY "id" ASC`,
		`LIMIT 1 OFFSET 1`,
	} {
		if !strings.Contains(rendered.SQL, want) {
			t.Fatalf("total rows SQL missing %q:\n%s", want, rendered.SQL)
		}
	}
	if strings.Index(rendered.SQL, `COUNT(*) OVER ()`) > strings.Index(rendered.SQL, `LIMIT 1`) {
		t.Fatalf("total window occurs after pagination:\n%s", rendered.SQL)
	}
	if got, want := rendered.Columns, []string{"id", "amount", "__leapview_total_rows"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered columns = %#v, want %#v", got, want)
	}
	explain, err := withTotal.Explain()
	if err != nil || !strings.Contains(explain, "TotalRows") || !strings.Contains(explain, "total_field=__leapview_total_rows") {
		t.Fatalf("total rows explain = %q, error=%v", explain, err)
	}
	fingerprint, err := withTotal.Fingerprint()
	if err != nil || fingerprint == "" {
		t.Fatalf("total rows fingerprint = %q, error=%v", fingerprint, err)
	}
}

func TestWithTotalRowsRejectsInvalidShape(t *testing.T) {
	graph := totalRowsPlan()
	for _, field := range []string{"", "bad field", "id"} {
		if _, err := WithTotalRows(graph, field); err == nil {
			t.Fatalf("WithTotalRows(%q) error = nil", field)
		}
	}
	graph.Output = "filter"
	delete(graph.Nodes, "sort")
	graph.NodeMeta = graph.Nodes["filter"].Meta()
	if _, err := WithTotalRows(graph, "__leapview_total_rows"); err == nil || !strings.Contains(err.Error(), "SortLimit") {
		t.Fatalf("non-sort total rows error = %v", err)
	}
}

func TestWithTotalRowsRejectsQualifiedTotalField(t *testing.T) {
	graph := totalRowsPlan()
	if _, err := WithTotalRows(graph, "orders.total"); err == nil || !strings.Contains(err.Error(), "unqualified") {
		t.Fatalf("qualified total field error = %v, want unqualified identifier error", err)
	}

	withTotal, err := WithTotalRows(graph, "__total_rows")
	if err != nil {
		t.Fatal(err)
	}
	total := withTotal.Nodes[withTotal.Output].(TotalRows)
	total.TotalField = "orders.total"
	withTotal.Nodes[withTotal.Output] = total
	if err := withTotal.Validate(); err == nil || !strings.Contains(err.Error(), "unqualified") {
		t.Fatalf("Validate() error = %v, want unqualified identifier error", err)
	}
	if _, err := RenderDuckDB(withTotal); err == nil || !strings.Contains(err.Error(), "unqualified") {
		t.Fatalf("RenderDuckDB() error = %v, want unqualified identifier error", err)
	}
}

func TestWithTotalRowsRejectsSortLimitMetrics(t *testing.T) {
	graph := totalRowsPlan()
	sortNode := graph.Nodes[graph.Output].(SortLimit)
	sortNode.AvailableMetrics = []Metric{{Name: "revenue", Type: "decimal"}}
	graph.Nodes[graph.Output] = sortNode
	if _, err := WithTotalRows(graph, "__total_rows"); err == nil || !strings.Contains(err.Error(), "available metrics") {
		t.Fatalf("WithTotalRows() error = %v, want available metrics invariant", err)
	}

	// Construct the invalid TotalRows graph directly to ensure Validate also
	// enforces the invariant for callers that bypass WithTotalRows.
	totalMeta := sortNode.NodeMeta
	totalMeta.NodeID = "total"
	totalMeta.AvailableFields = append(append([]Field(nil), sortNode.AvailableFields...), Field{Name: "__total_rows", Type: "integer"})
	totalMeta.AvailableMetrics = nil
	graph.Nodes["total"] = TotalRows{NodeMeta: totalMeta, Input: "sort", TotalField: "__total_rows"}
	graph.Output = "total"
	graph.NodeMeta = totalMeta
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "available metrics") {
		t.Fatalf("Validate() error = %v, want available metrics invariant", err)
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

func TestValidateRejectsUnknownMetricEmptyPolicy(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.AvailableMetrics[0].Empty = "error"
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported empty policy") {
		t.Fatalf("Validate() error = %v, want unsupported empty policy", err)
	}
}

func TestValidateRejectsMetricEmptyPolicyMetadataMismatch(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Metrics[0].Empty = "zero"
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "empty policy metadata") {
		t.Fatalf("Validate() error = %v, want metadata mismatch", err)
	}
}

func TestValidateRejectsAggregateMetricMetadataExtras(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.AvailableMetrics = append(aggregate.AvailableMetrics, Metric{Name: "unexpected", Type: "decimal"})
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "exactly match metric specs") {
		t.Fatalf("Validate() error = %v, want exact aggregate metadata cardinality", err)
	}
}

func TestValidateRejectsMetricTypeMetadataMismatch(t *testing.T) {
	graph := validPlan()
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Metrics[0].Type = "integer"
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "type metadata") {
		t.Fatalf("Validate() error = %v, want type metadata mismatch", err)
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
		{Kind: ScalarNeg, Children: []ScalarExpr{{Kind: ScalarLiteral, Literal: Literal{Kind: LiteralNumber, NumberKind: NumberDecimal, NumberText: "9007199254740993.125"}}}},
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
	filter.Predicate.Value = Literal{Kind: LiteralNumber, NumberKind: NumberDecimal, NumberText: "9007199254740993.125"}
	graph.Nodes["filter"] = filter
	canonical, err := graph.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(canonical), "9007199254740993.125") {
		t.Fatalf("canonical graph lost exact number token: %s", canonical)
	}
}
