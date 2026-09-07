package planir

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
)

// A caller holding a graph must not be able to turn an admitted protected
// scan into an unprotected scan by clearing its public marker, nor replace
// the predicate or physical binding after admission.
func TestSecurityBarrierSealedGraphRejectsMutation(t *testing.T) {
	for _, name := range []string{"remove barrier and marker", "replace predicate", "replace relation", "remove predicate"} {
		t.Run(name, func(t *testing.T) {
			graph := validPlan()
			predicate := Predicate{Kind: PredicateCompare, Field: "status", Operator: "=", Value: Literal{Kind: LiteralString, String: "allowed"}}
			if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{"orders": {
				PolicyDigest: "sha256:" + strings.Repeat("1", 64), DecisionDigest: "sha256:" + strings.Repeat("2", 64), Predicate: &predicate,
			}}); err != nil {
				t.Fatal(err)
			}
			var barrierID string
			var barrier SecurityBarrier
			for id, node := range graph.Nodes {
				if value, ok := node.(SecurityBarrier); ok {
					barrierID, barrier = id, value
				}
			}
			if barrierID == "" {
				t.Fatal("admitted graph has no barrier")
			}
			switch name {
			case "remove barrier and marker":
				scan := graph.Nodes["scan"].(ScanDataset)
				scan.RequiresSecurityBarrier = false
				graph.Nodes["scan"] = scan
				filter := graph.Nodes["filter"].(FilterRows)
				filter.Input = "scan"
				graph.Nodes["filter"] = filter
				delete(graph.Nodes, barrierID)
			case "replace predicate":
				replacement := predicate
				replacement.Value.String = "unauthorized"
				barrier.Predicate = &replacement
				graph.Nodes[barrierID] = barrier
			case "replace relation":
				scan := graph.Nodes["scan"].(ScanDataset)
				scan.Relation = "other_instance.orders"
				graph.Nodes["scan"] = scan
			case "remove predicate":
				barrier.Predicate = nil
				graph.Nodes[barrierID] = barrier
			}
			if err := graph.Validate(); err == nil {
				t.Fatal("mutated security graph validated")
			}
			if _, err := RenderDuckDB(graph); err == nil {
				t.Fatal("mutated security graph rendered")
			}
		})
	}
}

func TestSecurityBarrierExecutionPrecedesJoinAndAggregation(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		"CREATE TABLE orders(id VARCHAR, amount INTEGER, status VARCHAR)",
		"INSERT INTO orders VALUES ('one',10,'paid'),('two',20,'paid'),('three',100,'paid')",
		"CREATE TABLE customers(order_id VARCHAR, visible BOOLEAN)",
		"INSERT INTO customers VALUES ('one',true),('one',false),('one',false),('two',false),('three',true)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	graph := validPlan()
	path := RelationshipPath{Name: "customers", FromDataset: "orders", ToDataset: "customers", JoinKeys: []JoinKey{{From: "id", To: "order_id"}}}
	meta := graph.Nodes["filter"].Meta()
	meta.NodeID, meta.FilterPhase = "traverse", FilterPhaseRelationship
	meta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{path}}}
	graph.Nodes["traverse"] = TraverseRelationship{NodeMeta: meta, Input: "filter", Path: path}
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Input = "traverse"
	graph.Nodes["aggregate"] = aggregate
	rootPredicate := Predicate{Kind: PredicateCompare, Field: "amount", Operator: "<=", Value: Literal{Kind: LiteralNumber, NumberKind: NumberInteger, NumberText: "50"}}
	targetPredicate := Predicate{Kind: PredicateCompare, Field: "customers.visible", Operator: "=", Value: Literal{Kind: LiteralBool, Bool: true}}
	policies := map[string]SecurityPolicy{}
	for dataset, predicate := range map[string]*Predicate{"orders": &rootPredicate, "customers": &targetPredicate} {
		policies[dataset] = SecurityPolicy{PolicyDigest: "sha256:" + strings.Repeat("1", 64), DecisionDigest: "sha256:" + strings.Repeat("2", 64), Predicate: predicate}
	}
	if err := ApplySecurityBarriers(graph, policies); err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(rendered.SQL, rendered.Args...)
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, rendered.SQL)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var id string
		var amount int64
		if err := rows.Scan(&id, &amount); err != nil {
			t.Fatal(err)
		}
		got[id] = amount
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// "two" has no authorized match but must survive left null extension.
	// Unauthorized duplicate matches must not inflate "one" to 30.
	if want := (map[string]int64{"one": 10, "two": 20}); !reflect.DeepEqual(got, want) {
		t.Fatalf("protected join aggregates = %#v, want %#v", got, want)
	}
}
