package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func securityPlannerFixture(t *testing.T, routed bool) (*Planner, SemanticAccessEvaluationContext) {
	t.Helper()
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	m := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}}}, "region_grant")
	field := "order_status"
	m.Dimensions[field] = semanticmodel.SemanticDimension{Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}}
	if routed {
		field = "customer_state"
	}
	orders := m.Datasets["orders"]
	orders.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: field, UserAttribute: "region"}}
	m.Datasets["orders"] = orders
	c, err := CompileModelWithSemanticAccess(m, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	context := semanticAccessContext(registry, semanticAccessAttribute(t, region, "us"))
	p, err := NewSemanticAccessPlanner(c, context, WithTableRelation(func(table string) (string, error) { return `"` + table + `"`, nil }))
	if err != nil {
		t.Fatal(err)
	}
	return p, context
}

func TestSecurityPlannerExecutesRestrictedAggregateAndRelationship(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "relationship"}[routed], func(t *testing.T) {
			p, _ := securityPlannerFixture(t, routed)
			result, err := p.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("duckdb", "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, statement := range []string{`CREATE TABLE orders(order_id BIGINT, customer_id BIGINT,status VARCHAR)`, `INSERT INTO orders VALUES (1,1,'us'),(2,2,'eu'),(3,3,'eu')`, `CREATE TABLE customers(customer_id BIGINT,state VARCHAR)`, `INSERT INTO customers VALUES (1,'us'),(2,'eu')`} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			var count int64
			if err := db.QueryRow(result.SQL, result.Args...).Scan(&count); err != nil {
				t.Fatalf("%v\n%s\n%v", err, result.SQL, result.Args)
			}
			if count != 1 {
				t.Fatalf("restricted count=%d", count)
			}
			if _, err := db.Exec(`CREATE TABLE tags(tag_id BIGINT); INSERT INTO tags VALUES (1),(2)`); err != nil {
				t.Fatal(err)
			}
			derived, err := p.Plan(Request{Metrics: []Field{{Field: "tags_per_order"}}})
			if err != nil {
				t.Fatal(err)
			}
			var ratio float64
			if err := db.QueryRow(derived.SQL, derived.Args...).Scan(&ratio); err != nil {
				t.Fatalf("%v\n%s", err, derived.SQL)
			}
			if ratio != 2 {
				t.Fatalf("derived ratio=%v, want 2 over the restricted denominator", ratio)
			}
			kinds := map[planir.Kind]int{}
			for _, node := range derived.IR.Nodes {
				kinds[node.Kind()]++
			}
			if kinds[planir.KindAggregateMetrics] != 2 || kinds[planir.KindStitchAggregates] != 1 || kinds[planir.KindComputeDerived] != 1 || kinds[planir.KindSecurityBarrier] != kinds[planir.KindScanDataset] {
				t.Fatalf("derived golden plan shape=%v", kinds)
			}
			if routed && !strings.Contains(result.SQL, "EXISTS") {
				t.Fatal("relationship access filter was not scan-local")
			}
			repeated, err := p.Plan(Request{Metrics: []Field{{Field: "order_count"}}})
			if err != nil {
				t.Fatal(err)
			}
			a, err := result.IR.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			b, err := repeated.IR.Canonical()
			if err != nil {
				t.Fatal(err)
			}
			if string(a) != string(b) || !reflect.DeepEqual(result.Args, repeated.Args) {
				t.Fatal("security planning is nondeterministic")
			}
		})
	}
}

func TestSecurityPlannerDeniesInvalidContextAndGuardsAllSourceShapes(t *testing.T) {
	p, context := securityPlannerFixture(t, false)
	context.RegistryState.Digest = "stale"
	denied, err := NewSemanticAccessPlanner(p.compiled, context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil {
		t.Fatal("stale policy state was planned")
	}
	if _, err := (&Planner{compiled: p.compiled}).Plan(Request{Metrics: []Field{{Field: "order_count"}}}); err == nil {
		t.Fatal("missing policy context was planned")
	}
	cases := []struct {
		name string
		run  func() (Plan, error)
	}{
		{"aggregate", func() (Plan, error) {
			return p.Plan(Request{Dimensions: []Field{{Field: "customer_state"}}, Metrics: []Field{{Field: "order_count"}}})
		}},
		{"derived", func() (Plan, error) { return p.Plan(Request{Metrics: []Field{{Field: "tags_per_order"}}}) }},
		{"count", func() (Plan, error) { return p.PlanCount(CountRequest{Dataset: "orders"}) }},
		{"rows", func() (Plan, error) {
			return p.PlanRows(RowRequest{Dataset: "orders", Dimensions: []Field{{Field: "orders.status"}}, Limit: 10})
		}},
		{"histogram", func() (Plan, error) {
			return p.PlanHistogram(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, 5)
		}},
		{"distribution", func() (Plan, error) {
			return p.PlanDistribution(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}}, nil, 5)
		}},
		{"spatial-metadata", func() (Plan, error) {
			return p.PlanSpatialMetadata(SpatialMetadataRequest{Dataset: "orders", Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"}, Metrics: []Field{{Field: "revenue"}}, FeatureCap: 100, MaximumZoom: 18})
		}},
		{"spatial-tile", func() (Plan, error) {
			return p.PlanSpatialTileAggregate(SpatialTileRequest{Dataset: "orders", Latitude: Field{Field: "orders.latitude", Alias: "latitude"}, Longitude: Field{Field: "orders.longitude", Alias: "longitude"}, Metrics: []Field{{Field: "revenue"}}, Zoom: 2, TargetZoom: 3, MetatileSize: 1, CellPixels: 64})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.run()
			if err != nil {
				t.Fatal(err)
			}
			scans, barriers := 0, 0
			for _, node := range result.IR.Nodes {
				if node.Kind() == planir.KindScanDataset {
					scans++
				}
				if node.Kind() == planir.KindSecurityBarrier {
					barriers++
				}
			}
			if scans == 0 || scans != barriers {
				t.Fatalf("scans=%d barriers=%d", scans, barriers)
			}
			// Envelope-specific result formats do not expose the ordinary final
			// projection descriptor. Keep that existing boundary unchanged.
			kind := result.IR.Nodes[result.IR.Output].Kind()
			if kind != planir.KindSpatialEnvelope && kind != planir.KindAnalyticalEnvelope {
				if _, err := p.DescribeOutputSchema(result); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSecurityPlannerProtectsJoinedSideBeforeNullExtension(t *testing.T) {
	region := semanticAccessDefinition("region", "def-region", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(region)
	m := protectedSemanticAccessModel(nil)
	dimension := m.Dimensions["customer_state"]
	dimension.Bindings["customers"] = semanticmodel.DimensionBinding{Field: "customers.state"}
	m.Dimensions["customer_state"] = dimension
	dataset := m.Datasets["customers"]
	dataset.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "region"}}
	m.Datasets["customers"] = dataset
	c, err := CompileModelWithSemanticAccess(m, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewSemanticAccessPlanner(c, semanticAccessContext(registry, semanticAccessAttribute(t, region, "us")), WithTableRelation(func(table string) (string, error) { return `"` + table + `"`, nil }))
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.Plan(Request{Dimensions: []Field{{Field: "customer_state"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{`CREATE TABLE orders(order_id BIGINT,customer_id BIGINT)`, `INSERT INTO orders VALUES (1,1),(2,2),(3,3)`, `CREATE TABLE customers(customer_id BIGINT,state VARCHAR)`, `INSERT INTO customers VALUES (1,'us'),(2,'eu')`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(result.SQL, result.Args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, result.SQL)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var state sql.NullString
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			t.Fatal(err)
		}
		got[state.String] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, map[string]int64{"us": 1, "": 2}) {
		t.Fatalf("left join leaked or discarded unmatched rows: %v", got)
	}
}

func TestSecurityPlannerBundleAndRawValueGrants(t *testing.T) {
	p, _ := securityPlannerFixture(t, false)
	result, err := p.PlanBundle([]BundleRequest{{ID: "a", Request: Request{Metrics: []Field{{Field: "order_count"}}}}, {ID: "b", Request: Request{Metrics: []Field{{Field: "revenue"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, node := range result.Plan.IR.Nodes {
		if node.Kind() == planir.KindSecurityBarrier {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("shared scan requires one barrier, got %d", count)
	}
	raw, err := p.PlanRawValues(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue"}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw.SQL, "AS MATERIALIZED") {
		t.Fatal("raw values bypassed barrier")
	}
}

func TestSecurityPlannerMultiHopAndReverseAccessFilters(t *testing.T) {
	for _, tc := range []struct {
		name, dataset, field, value, metric string
		keys                                []string
	}{
		{"multi-hop", "orders", "tier", "gold", "order_count", []string{"orders.customer_id", "customers.customer_id", "profiles.customer_id", "profiles.tier"}},
		{"reverse", "profiles", "profile_region", "north", "profile_count", []string{"profiles.customer_id", "customers.customer_id", "customers.region"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := semanticAccessDefinition("scope", "def-scope", semanticvalue.TypeString, access.SemanticAttributeScalar)
			registry := semanticAccessRegistry(definition)
			model := singleDatasetFanoutModel()
			populateFixtureTableModelNames(model)
			dataset := model.Datasets[tc.dataset]
			dataset.AccessFilters = []semanticmodel.SemanticAccessFilterSpec{{Field: tc.field, UserAttribute: "scope"}}
			model.Datasets[tc.dataset] = dataset
			compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
			if err != nil {
				t.Fatal(err)
			}
			p, err := NewSemanticAccessPlanner(compiled, semanticAccessContext(registry, semanticAccessAttribute(t, definition, tc.value)))
			if err != nil {
				t.Fatal(err)
			}
			result, err := p.Plan(Request{Metrics: []Field{{Field: tc.metric}}})
			if err != nil {
				t.Fatal(err)
			}
			db := openFanoutDatabase(t, []string{`CREATE TABLE model.orders(order_id VARCHAR,customer_id VARCHAR,revenue DOUBLE)`, `INSERT INTO model.orders VALUES ('o1','a',10),('o2','b',20)`, `CREATE TABLE model.customers(customer_id VARCHAR,region VARCHAR)`, `INSERT INTO model.customers VALUES ('a','north'),('b','south')`, `CREATE TABLE model.profiles(customer_id VARCHAR,tier VARCHAR)`, `INSERT INTO model.profiles VALUES ('a','gold'),('b','silver')`})
			defer db.Close()
			var count int64
			if err := db.QueryRow(result.SQL, result.Args...).Scan(&count); err != nil {
				t.Fatalf("%v\n%s", err, result.SQL)
			}
			if count != 1 {
				t.Fatalf("count=%d", count)
			}
			dependencies, err := result.IR.Dependencies()
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range tc.keys {
				if !containsString(dependencies.PhysicalFields, field) {
					t.Errorf("security dependency %s missing: %v", field, dependencies.PhysicalFields)
				}
			}
		})
	}
}

func TestSecurityPlannerSharedModelAliasFilter(t *testing.T) {
	definition := semanticAccessDefinition("scope", "def-scope", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(definition)
	model := protectedSemanticAccessModel(nil)
	model.Datasets["customers_restricted"] = semanticmodel.SemanticDatasetSpec{Model: "customers", AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_state", UserAttribute: "scope"}}}
	model.Tables["customers_restricted"] = model.Tables["customers"]
	dimension := model.Dimensions["customer_state"]
	dimension.Bindings["customers_restricted"] = semanticmodel.DimensionBinding{Field: "customers_restricted.state"}
	model.Dimensions["customer_state"] = dimension
	populateFixtureTableModelNames(model)
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewSemanticAccessPlanner(compiled, semanticAccessContext(registry, semanticAccessAttribute(t, definition, "us")))
	if err != nil {
		t.Fatal(err)
	}
	result, err := p.PlanCount(CountRequest{Dataset: "customers"})
	if err != nil {
		t.Fatal(err)
	}
	db := openFanoutDatabase(t, []string{`CREATE TABLE model.customers(customer_id BIGINT,state VARCHAR)`, `INSERT INTO model.customers VALUES (1,'us'),(2,'eu')`})
	defer db.Close()
	var count int64
	if err := db.QueryRow(result.SQL, result.Args...).Scan(&count); err != nil {
		t.Fatalf("%v\n%s", err, result.SQL)
	}
	if count != 1 {
		t.Fatalf("sibling alias filter bypass: count=%d", count)
	}
}

func TestSecurityPlannerMetricAndDimensionGrantsCannotBeAliasedAway(t *testing.T) {
	definition := semanticAccessDefinition("scope", "def-scope", semanticvalue.TypeString, access.SemanticAttributeScalar)
	registry := semanticAccessRegistry(definition)
	model := protectedSemanticAccessModel(map[string]semanticmodel.SemanticAccessGrantSpec{"private": {UserAttribute: "scope", AllowedValues: []any{"yes"}}})
	for _, name := range []string{"revenue", "tag_count"} {
		metric := model.Metrics[name]
		metric.RequiredAccessGrants = []string{"private"}
		model.Metrics[name] = metric
	}
	dimension := model.Dimensions["customer_state"]
	dimension.RequiredAccessGrants = []string{"private"}
	model.Dimensions["customer_state"] = dimension
	compiled, err := CompileModelWithSemanticAccess(model, SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewSemanticAccessPlanner(compiled, semanticAccessContext(registry, semanticAccessAttribute(t, definition, "no")))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func() (Plan, error)
	}{
		{"metric", func() (Plan, error) { return p.Plan(Request{Metrics: []Field{{Field: "revenue", Alias: "public"}}}) }},
		{"derived", func() (Plan, error) {
			return p.Plan(Request{Metrics: []Field{{Field: "tags_per_order", Alias: "public"}}})
		}},
		{"raw", func() (Plan, error) {
			return p.PlanRawValues(RawValueRequest{Dataset: "orders", Metric: Field{Field: "revenue", Alias: "public"}, Limit: 5})
		}},
		{"dimension", func() (Plan, error) {
			return p.Plan(Request{Dimensions: []Field{{Field: "customer_state", Alias: "public"}}, Metrics: []Field{{Field: "order_count"}}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.run(); err == nil {
				t.Fatal("restricted member planned")
			}
		})
	}
}
