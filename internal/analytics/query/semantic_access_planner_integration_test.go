package query

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func semanticAccessPlannerForTest(t *testing.T, model *semanticmodel.Model, policy *CompiledSemanticAccessPolicy, snapshot SemanticAccessAttributeSnapshot, authority SemanticAccessAuthority, calls *int) *Planner {
	t.Helper()
	planner, err := NewCompiledPlanner(model,
		WithTableRelation(func(table string) (string, error) { return "model." + table, nil }),
		WithSemanticAccess(policy, func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
			if calls != nil {
				*calls = *calls + 1
			}
			return snapshot, authority, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewCompiledPlanner: %v", err)
	}
	return planner
}

func compileSemanticAccessPlannerPolicy(t *testing.T, model *semanticmodel.Model) *CompiledSemanticAccessPolicy {
	t.Helper()
	compiled, err := CompileModel(model)
	if err != nil {
		t.Fatalf("CompileModel: %v", err)
	}
	policy, err := CompileSemanticAccessPolicy("instance-1", "semantic-model:test", "generation-1", model, compiled, semanticAccessRegistry(semanticAccessDefinitions()))
	if err != nil {
		t.Fatalf("CompileSemanticAccessPolicy: %v", err)
	}
	return policy
}

func openSemanticAccessPlannerDB(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("execute setup %q: %v", statement, err)
		}
	}
	return db
}

func TestSemanticAccessPlannerExecutesAllowedAggregateAndCountPopulation(t *testing.T) {
	model := semanticAccessTestModel(t)
	policy := compileSemanticAccessPlannerPolicy(t, model)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	snapshot, authority := semanticAccessSnapshot(t, attributes)
	calls := 0
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, &calls)
	db := openSemanticAccessPlannerDB(t,
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders_model(id BIGINT, region VARCHAR, account_id BIGINT, amount DECIMAL(20,3), approved BOOLEAN, order_date DATE, occurred_at TIMESTAMPTZ)",
		"INSERT INTO model.orders_model VALUES (1, 'west', 9007199254740993, 9007199254740993.125, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (2, 'east', 7, 9007199254740993.125, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00'), (3, 'north', 7, 9007199254740993.125, true, DATE '2026-09-06', TIMESTAMPTZ '2026-09-06 03:30:00+00')",
	)

	plan, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue", Alias: "value"}}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sum any
	if err := db.QueryRow(plan.SQL, plan.Args...).Scan(&sum); err != nil {
		t.Fatalf("execute protected SUM: %v\n%s", err, plan.SQL)
	}
	if !exactDecimalEqual(sum, "18014398509481986.25") {
		t.Fatalf("protected SUM = %v, want 18014398509481986.25", sum)
	}

	countPlan, err := planner.PlanCount(CountRequest{Dataset: "orders"})
	if err != nil {
		t.Fatalf("PlanCount: %v", err)
	}
	var count int64
	if err := db.QueryRow(countPlan.SQL, countPlan.Args...).Scan(&count); err != nil {
		t.Fatalf("execute protected COUNT: %v\n%s", err, countPlan.SQL)
	}
	if count != 2 {
		t.Fatalf("protected COUNT = %d, want 2", count)
	}
	if calls != 2 {
		t.Fatalf("authority provider calls = %d, want one per final plan", calls)
	}
	assertSecurityBarriers(t, plan.IR, 1, true)
}

func TestSemanticAccessPlannerFiltersProtectedLeftJoinTargetBeforeJoin(t *testing.T) {
	model := testModel()
	populateFixtureTableModelNames(model)
	literal, err := semanticmodel.NewSemanticAccessLiteral("sales")
	if err != nil {
		t.Fatal(err)
	}
	model.Dimensions["customer_region"] = semanticmodel.SemanticDimension{
		Type: "string", Datatype: semanticmodel.DataTypeString,
		Bindings: map[string]semanticmodel.DimensionBinding{
			"orders":    {Field: "customers.state", Path: []string{"orders_customers"}},
			"customers": {Field: "customers.state"},
		},
	}
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"canViewSales": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{
			"orders":    {RequiredAccessGrants: []string{"canViewSales"}},
			"customers": {AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "customer_region", UserAttribute: "regions"}}},
		},
		Dimensions: map[string][]string{"customer_region": {"canViewSales"}},
		Metrics:    map[string][]string{"order_count": {"canViewSales"}},
	}
	policy := compileSemanticAccessPlannerPolicy(t, model)
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
	db := openSemanticAccessPlannerDB(t,
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders(order_id BIGINT, customer_id BIGINT)",
		"INSERT INTO model.orders VALUES (1, 10), (2, 99), (3, 20)",
		"CREATE TABLE model.customers(customer_id BIGINT, state VARCHAR)",
		"INSERT INTO model.customers VALUES (10, 'east'), (20, 'north')",
	)

	plan, err := planner.Plan(Request{Dataset: "orders", Dimensions: []Field{{Field: "customer_region"}}, Metrics: []Field{{Field: "order_count"}}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rows, err := db.Query(plan.SQL, plan.Args...)
	if err != nil {
		t.Fatalf("execute protected left join: %v\n%s", err, plan.SQL)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var region sql.NullString
		var count int64
		if err := rows.Scan(&region, &count); err != nil {
			t.Fatal(err)
		}
		key := "<null>"
		if region.Valid {
			key = region.String
		}
		got[key] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"east": 1, "<null>": 2}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("protected left-join population = %#v, want %#v", got, want)
	}
	assertSecurityBarriers(t, plan.IR, 2, true)
}

func TestSemanticAccessPlannerFailsClosedForMissingAndStaleAuthority(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, snapshot *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority)
		want  string
	}{
		{name: "missing evidence", setup: func(t *testing.T, snapshot *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			attributes := semanticAccessEffective(t, semanticAccessDefinitions())
			filtered := attributes[:0]
			for _, attribute := range attributes {
				if attribute.DefinitionName != "regions" {
					filtered = append(filtered, attribute)
				}
			}
			*snapshot, *authority = semanticAccessSnapshot(t, filtered)
		}, want: "semantic access denied"},
		{name: "stale control", setup: func(_ *testing.T, _ *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			authority.Control.State.Revision++
		}, want: "stale or inconsistent"},
		{name: "disabled registry definition", setup: func(_ *testing.T, snapshot *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			definitions := replaceDefinition(semanticAccessDefinitions(), "regions", func(value *access.SemanticAttributeDefinition) {
				value.Enabled = false
				value.LifecycleState = access.SemanticAttributeDisabled
				value.DisabledAt = semanticAccessObservedAt
			})
			registry := semanticAccessRegistry(definitions)
			snapshot.Registry, authority.Registry = registry, registry
		}, want: "disabled"},
		{name: "tombstoned assignment evidence", setup: func(t *testing.T, snapshot *SemanticAccessAttributeSnapshot, authority *SemanticAccessAuthority) {
			snapshot.Control.Assignments[0].Tombstoned = true
			snapshot.Control.Assignments[0].TombstonedAt = semanticAccessObservedAt
			state, err := access.SemanticAttributeControlDigest(snapshot.Control.Assignments, snapshot.Control.Mappings)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Control.State.Digest = state
			authority.Control = snapshot.Control
		}, want: "direct assignment evidence"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := semanticAccessTestModel(t)
			policy := compileSemanticAccessPlannerPolicy(t, model)
			snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
			test.setup(t, &snapshot, &authority)
			planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
			_, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Plan error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSemanticAccessPlannerCountFilterAdmitsDimensionBeforeExecution(t *testing.T) {
	model := semanticAccessTestModel(t)
	policy := compileSemanticAccessPlannerPolicy(t, model)
	attributes := semanticAccessEffective(t, semanticAccessDefinitions())
	filtered := attributes[:0]
	for _, attribute := range attributes {
		if attribute.DefinitionName != "regions" {
			filtered = append(filtered, attribute)
		}
	}
	snapshot, authority := semanticAccessSnapshot(t, filtered)
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, nil)
	_, err := planner.PlanCount(CountRequest{Dataset: "orders", Filters: []Filter{{Field: "region", Operator: "equals", Values: []any{"west"}}}})
	if err == nil || !strings.Contains(err.Error(), "semantic access denied for dimension") {
		t.Fatalf("filtered count error = %v, want denied semantic dimension", err)
	}
}

func TestSemanticAccessPlannerGrantOnlySealsDecisionAndBundleOnce(t *testing.T) {
	model := semanticAccessTestModel(t)
	model.AccessPolicy = semanticmodel.SemanticAccessPolicy{
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"canViewSales": {UserAttribute: "department", AllowedValues: []semanticmodel.SemanticAccessLiteral{mustSemanticAccessLiteral(t, "sales")}},
		},
		Metrics: map[string][]string{"revenue": {"canViewSales"}},
	}
	policy := compileSemanticAccessPlannerPolicy(t, model)
	snapshot, authority := semanticAccessSnapshot(t, semanticAccessEffective(t, semanticAccessDefinitions()))
	calls := 0
	planner := semanticAccessPlannerForTest(t, model, policy, snapshot, authority, &calls)
	db := openSemanticAccessPlannerDB(t,
		"CREATE SCHEMA model",
		"CREATE TABLE model.orders_model(id BIGINT, amount DECIMAL(20,3))",
		"INSERT INTO model.orders_model VALUES (1, 10), (2, 20)",
	)

	first, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
	if err != nil {
		t.Fatalf("grant-only Plan: %v", err)
	}
	second, err := planner.Plan(Request{Dataset: "orders", Metrics: []Field{{Field: "revenue"}}})
	if err != nil {
		t.Fatalf("repeat grant-only Plan: %v", err)
	}
	if first.SQL != second.SQL {
		t.Fatalf("grant-only SQL is not deterministic:\n%s\n---\n%s", first.SQL, second.SQL)
	}
	assertSecurityBarriers(t, first.IR, 1, false)
	var sum float64
	if err := db.QueryRow(first.SQL, first.Args...).Scan(&sum); err != nil {
		t.Fatalf("execute grant-only plan: %v\n%s", err, first.SQL)
	}
	if sum != 30 {
		t.Fatalf("grant-only SUM = %v, want 30", sum)
	}

	bundle, err := planner.PlanBundle([]BundleRequest{
		{ID: "total", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "revenue", Alias: "value"}}}},
		{ID: "count", Request: Request{Dataset: "orders", Metrics: []Field{{Field: "orderCount", Alias: "value"}}}},
	})
	if err != nil {
		t.Fatalf("grant-only bundle: %v", err)
	}
	if calls != 3 {
		t.Fatalf("provider calls after two plans and bundle = %d, want 3", calls)
	}
	assertSecurityBarriers(t, bundle.Plan.IR, 1, false)
}

func assertSecurityBarriers(t *testing.T, graph *planir.Graph, want int, wantPredicate bool) {
	t.Helper()
	if graph == nil {
		t.Fatal("plan graph is nil")
	}
	count := 0
	predicateCount := 0
	for _, node := range graph.Nodes {
		barrier, ok := node.(planir.SecurityBarrier)
		if !ok {
			continue
		}
		count++
		if barrier.Predicate != nil {
			predicateCount++
		}
		if barrier.PolicyDigest == "" || barrier.DecisionDigest == "" {
			t.Fatalf("security barrier %q lacks identity: %#v", barrier.NodeID, barrier)
		}
	}
	if count != want {
		explain, _ := graph.Explain()
		t.Fatalf("security barriers = %d, want %d\n%s", count, want, explain)
	}
	if wantPredicate && predicateCount == 0 {
		t.Fatalf("security barriers have no row predicate")
	}
}

func mustSemanticAccessLiteral(t *testing.T, value string) semanticmodel.SemanticAccessLiteral {
	t.Helper()
	literal, err := semanticmodel.NewSemanticAccessLiteral(value)
	if err != nil {
		t.Fatal(err)
	}
	return literal
}
