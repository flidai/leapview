package planir

import (
	"database/sql"
	_ "github.com/duckdb/duckdb-go/v2"
	"reflect"
	"strings"
	"testing"
)

func protectedPlan(t *testing.T) *Graph {
	t.Helper()
	g := validPlan()
	scan := g.Nodes["scan"].(ScanDataset)
	secured, barrier, err := NewSecurityBarrier(scan, "security_scan", "policy-orders", []Predicate{{Kind: PredicateCompare, Field: "status", Operator: "=", Value: Literal{Kind: LiteralString, String: "tenant-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	g.Nodes["scan"], g.Nodes[barrier.NodeID] = secured, barrier
	f := g.Nodes["filter"].(FilterRows)
	f.Input = barrier.NodeID
	g.Nodes["filter"] = f
	if err := g.SealSecurity(); err != nil {
		t.Fatal(err)
	}
	return g
}

func securityJoinGraph(t *testing.T, join RelationshipJoinType, targetRelation ...string) *Graph {
	t.Helper()
	rootMeta := NodeMeta{NodeID: "left_scan", RootDatasets: []string{"left_rows"}, FilterPhase: FilterPhaseScan, AvailableFields: []Field{{Name: "left_rows.id", Type: "integer"}, {Name: "left_rows.allowed", Type: "boolean"}}}
	targetMeta := NodeMeta{NodeID: "right_scan", RootDatasets: []string{"right_rows"}, FilterPhase: FilterPhaseScan, AvailableFields: []Field{{Name: "right_rows.id", Type: "integer"}, {Name: "right_rows.allowed", Type: "boolean"}}}
	predicate := func(field string) []Predicate {
		return []Predicate{{Kind: PredicateCompare, Field: field, Operator: "=", Value: Literal{Kind: LiteralBool, Bool: true}}}
	}
	left, lb, err := NewSecurityBarrier(ScanDataset{NodeMeta: rootMeta, Dataset: "left_rows"}, "left_barrier", "left-policy", predicate("left_rows.allowed"))
	if err != nil {
		t.Fatal(err)
	}
	relation := ""
	if len(targetRelation) > 0 {
		relation = targetRelation[0]
	}
	right, rb, err := NewSecurityBarrier(ScanDataset{NodeMeta: targetMeta, Dataset: "right_rows", Relation: relation}, "right_barrier", "right-policy", predicate("right_rows.allowed"))
	if err != nil {
		t.Fatal(err)
	}
	edge := RelationshipPath{Name: "left_right", FromDataset: "left_rows", ToDataset: "right_rows", JoinKeys: []JoinKey{{From: "id", To: "id"}}}
	meta := rootMeta
	meta.NodeID = "join"
	meta.FilterPhase = FilterPhaseRelationship
	meta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "left_rows", Edges: []RelationshipPath{edge}}}
	aggregateMeta := NodeMeta{NodeID: "aggregate", RootDatasets: []string{"left_rows"}, FilterPhase: FilterPhaseAggregate, AvailableMetrics: []Metric{{Name: "count", Type: "integer"}}}
	g := &Graph{Nodes: map[string]Node{left.NodeID: left, right.NodeID: right, lb.NodeID: lb, rb.NodeID: rb, "join": TraverseRelationship{NodeMeta: meta, Input: lb.NodeID, TargetInput: rb.NodeID, Path: edge, JoinType: join}, "aggregate": AggregateMetrics{NodeMeta: aggregateMeta, Input: "join", Metrics: []MetricSpec{{Name: "count", Type: "integer", Aggregation: "COUNT_STAR"}}}}, Roots: []string{left.NodeID, right.NodeID}, Output: "aggregate"}
	if err := g.SealSecurity(); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSecurityBarrierOuterJoinGoldenExecution(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{`CREATE TABLE left_rows(id BIGINT,allowed BOOLEAN)`, `CREATE TABLE right_rows(id BIGINT,allowed BOOLEAN)`, `INSERT INTO left_rows VALUES (1,true),(2,true),(3,false)`, `INSERT INTO right_rows VALUES (1,true),(2,false),(3,true),(4,false)`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		join    RelationshipJoinType
		sqlJoin string
		count   int64
	}{{RelationshipJoinInner, "INNER", 1}, {RelationshipJoinLeft, "LEFT", 2}, {RelationshipJoinRight, "RIGHT", 2}, {RelationshipJoinFull, "FULL OUTER", 3}} {
		t.Run(string(tc.join), func(t *testing.T) {
			g := securityJoinGraph(t, tc.join)
			rendered, err := RenderDuckDB(g)
			if err != nil {
				t.Fatal(err)
			}
			want := `WITH p_left_barrier AS MATERIALIZED (SELECT * FROM "left_rows" WHERE "left_rows"."allowed" = ?),` + "\n" +
				`p_right_barrier AS MATERIALIZED (SELECT * FROM "right_rows" WHERE "right_rows"."allowed" = ?)` + "\n" +
				`SELECT COUNT(*) AS "count" FROM p_left_barrier AS "left_rows" ` + tc.sqlJoin + ` JOIN p_right_barrier AS "r2" ON "left_rows"."id" = "r2"."id"`
			if rendered.SQL != want {
				t.Fatalf("golden barrier shape mismatch: %s", rendered.SQL)
			}
			if !reflect.DeepEqual(rendered.Args, []any{true, true}) {
				t.Fatalf("bound parameters=%v", rendered.Args)
			}
			var count int64
			if err := db.QueryRow(rendered.SQL, rendered.Args...).Scan(&count); err != nil {
				t.Fatalf("%v\n%s", err, rendered.SQL)
			}
			if count != tc.count {
				t.Fatalf("count=%d want=%d", count, tc.count)
			}
			rewritten := *g
			rewritten.Nodes = map[string]Node{}
			for id, node := range g.Nodes {
				rewritten.Nodes[id] = node
			}
			n := rewritten.Nodes["join"].(TraverseRelationship)
			n.TargetInput = "right_scan"
			rewritten.Nodes["join"] = n
			if err := ValidateSecurityRewrite(g, &rewritten); err == nil {
				t.Fatal("join-side barrier bypass accepted")
			}
		})
	}
}

func TestSecurityBarrierRewriteRejectsLostSealAndPruning(t *testing.T) {
	g := protectedPlan(t)
	rebuilt := &Graph{NodeMeta: g.NodeMeta, Roots: g.Roots, Output: g.Output, Nodes: g.Nodes}
	if err := ValidateSecurityRewrite(g, rebuilt); err == nil {
		t.Fatal("fresh graph lost security requirements")
	}
	b := g.Nodes["security_scan"].(SecurityBarrier)
	b.NodeMeta.AvailableFields = append([]Field(nil), b.AvailableFields...)
	b.AvailableFields = b.AvailableFields[:1]
	g.Nodes[b.NodeID] = b
	if err := g.Validate(); err == nil {
		t.Fatal("security projection pruning accepted")
	}
}

func TestSecurityBarrierRejectsNewImplicitTraversal(t *testing.T) {
	g := securityJoinGraph(t, RelationshipJoinLeft)
	n := g.Nodes["join"].(TraverseRelationship)
	n.NodeID = "new_join"
	n.Input = "join"
	n.TargetInput = ""
	g.Nodes[n.NodeID] = n
	a := g.Nodes["aggregate"].(AggregateMetrics)
	a.Input = n.NodeID
	g.Nodes[a.NodeID] = a
	if err := g.Validate(); err == nil || !strings.Contains(err.Error(), "no sealed explicit target") {
		t.Fatal("new implicit relationship source escaped sealed security topology")
	}
}

func TestSecurityBarrierSelfJoinAndManyToManyExecution(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{`CREATE TABLE left_rows(id BIGINT,allowed BOOLEAN)`, `CREATE TABLE right_rows(id BIGINT,allowed BOOLEAN)`, `INSERT INTO left_rows VALUES (1,true),(1,true),(1,false)`, `INSERT INTO right_rows VALUES (1,true),(1,true),(1,false)`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ name, relation string }{{"self-join", `"left_rows"`}, {"many-to-many", `"right_rows"`}} {
		t.Run(tc.name, func(t *testing.T) {
			g := securityJoinGraph(t, RelationshipJoinInner, tc.relation)
			rendered, err := RenderDuckDB(g)
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.QueryRow(rendered.SQL, rendered.Args...).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 4 {
				t.Fatalf("protected occurrences count=%d, want 4", count)
			}
			if strings.Count(rendered.SQL, "AS MATERIALIZED") != 2 {
				t.Fatalf("one barrier per occurrence required: %s", rendered.SQL)
			}
		})
	}
}

func TestSecurityBarrierPreservesTotalRowsRewrite(t *testing.T) {
	g := protectedPlan(t)
	a := g.Nodes["aggregate"].(AggregateMetrics)
	meta := a.NodeMeta
	meta.NodeID = "sort"
	meta.FilterPhase = FilterPhasePostAggregate
	meta.AvailableFields = append(append([]Field(nil), a.AvailableFields...), Field{Name: "revenue", Type: "decimal"})
	meta.AvailableMetrics = nil
	g.Nodes[meta.NodeID] = SortLimit{NodeMeta: meta, Input: "aggregate", Limit: 1}
	g.Output = meta.NodeID
	g.NodeMeta = meta
	total, err := WithTotalRows(g, "__total")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSecurityRewrite(g, total); err != nil {
		t.Fatal(err)
	}
	result, err := RenderDuckDB(total)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SQL, "COUNT(*) OVER ()") || strings.Count(result.SQL, "AS MATERIALIZED") != 1 {
		t.Fatalf("total rows security shape: %s", result.SQL)
	}
	if !reflect.DeepEqual(result.Args, []any{"tenant-secret", "paid"}) {
		t.Fatalf("total args=%v", result.Args)
	}
}

func TestSecurityBarrierCannotBeRemovedMovedOrChanged(t *testing.T) {
	for _, mutation := range []string{"remove", "predicate", "scan", "duplicate", "move"} {
		t.Run(mutation, func(t *testing.T) {
			g := protectedPlan(t)
			b := g.Nodes["security_scan"].(SecurityBarrier)
			switch mutation {
			case "remove":
				delete(g.Nodes, b.NodeID)
				f := g.Nodes["filter"].(FilterRows)
				f.Input = "scan"
				g.Nodes["filter"] = f
			case "predicate":
				b.Predicates = nil
				g.Nodes[b.NodeID] = b
			case "scan":
				s := g.Nodes["scan"].(ScanDataset)
				s.Relation = "other"
				g.Nodes["scan"] = s
			case "duplicate":
				b.NodeID = "duplicate"
				g.Nodes[b.NodeID] = b
				f := g.Nodes["filter"].(FilterRows)
				f.Input = b.NodeID
				g.Nodes["filter"] = f
			case "move":
				f := g.Nodes["filter"].(FilterRows)
				f.Input = "scan"
				g.Nodes["filter"] = f
				b.Input = "filter"
				g.Nodes[b.NodeID] = b
				a := g.Nodes["aggregate"].(AggregateMetrics)
				a.Input = b.NodeID
				g.Nodes["aggregate"] = a
			}
			if err := g.Validate(); err == nil {
				t.Fatal("unsafe security rewrite accepted")
			}
			if _, err := RenderDuckDB(g); err == nil {
				t.Fatal("unsafe security rewrite rendered")
			}
		})
	}
}

func TestSecurityBarrierBindsValuesBeforeConsumerFilter(t *testing.T) {
	g := protectedPlan(t)
	result, err := RenderDuckDB(g)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.SQL, "tenant-secret") || !strings.Contains(result.SQL, "AS MATERIALIZED") {
		t.Fatalf("barrier SQL = %s", result.SQL)
	}
	if len(result.Args) != 2 || result.Args[0] != "tenant-secret" || result.Args[1] != "paid" {
		t.Fatalf("args=%v", result.Args)
	}
	explanation, err := g.Explain()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(explanation, "tenant-secret") || !strings.Contains(explanation, "SecurityBarrier") {
		t.Fatalf("explanation=%s", explanation)
	}
}
