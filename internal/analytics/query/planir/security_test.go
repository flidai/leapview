package planir

import (
	"bytes"
	"strings"
	"testing"
)

func securityDigest(seed string) string { return "sha256:" + strings.Repeat(seed, 64) }

func TestApplySecurityBarriersSealsRootAndRendersPredicate(t *testing.T) {
	graph := validPlan()
	predicate := Predicate{Kind: PredicateCompare, Field: "status", Operator: "=", Value: Literal{Kind: LiteralString, String: "allowed"}}
	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"orders": {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2"), Predicate: &predicate},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("sealed graph Validate() error = %v", err)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	if !strings.Contains(rendered.SQL, `SELECT * FROM "orders" WHERE "status" = ?`) {
		t.Fatalf("rendered SQL does not contain an inner security predicate: %s", rendered.SQL)
	}
	if len(rendered.Args) != 2 || rendered.Args[0] != "allowed" || rendered.Args[1] != "paid" {
		t.Fatalf("rendered Args = %#v, want [allowed paid]", rendered.Args)
	}
}

func TestApplySecurityBarriersMaterializesProtectedRelationshipTarget(t *testing.T) {
	graph := validPlan()
	path := RelationshipPath{
		Name: "order_customer", FromDataset: "orders", ToDataset: "customers", ToRelation: "customers",
		JoinKeys: []JoinKey{{From: "id", To: "order_id"}},
	}
	traverseMeta := graph.Nodes["filter"].Meta()
	traverseMeta.NodeID = "traverse"
	traverseMeta.FilterPhase = FilterPhaseRelationship
	traverseMeta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{path}}}
	graph.Nodes["traverse"] = TraverseRelationship{NodeMeta: traverseMeta, Input: "filter", Path: path}
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Input = "traverse"
	graph.Nodes["aggregate"] = aggregate

	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"orders":    {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")},
		"customers": {PolicyDigest: securityDigest("3"), DecisionDigest: securityDigest("4")},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	traverse, ok := graph.Nodes["traverse"].(TraverseRelationship)
	if !ok || traverse.TargetInput == "" {
		t.Fatalf("traverse target input = %#v, want occurrence barrier", graph.Nodes["traverse"])
	}
	if _, ok := securityBarrier(graph.Nodes[traverse.TargetInput]); !ok {
		t.Fatalf("traverse target %q is not a security barrier", traverse.TargetInput)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	if !strings.Contains(rendered.SQL, `LEFT JOIN (SELECT * FROM customers`) {
		t.Fatalf("rendered SQL does not place protected target before join: %s", rendered.SQL)
	}
}

func TestSealedGraphRejectsNewRelationshipOccurrence(t *testing.T) {
	graph := validPlan()
	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"orders": {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	path := RelationshipPath{
		Name: "new_customer", FromDataset: "orders", ToDataset: "customers",
		JoinKeys: []JoinKey{{From: "id", To: "order_id"}},
	}
	meta := graph.Nodes["filter"].Meta()
	meta.NodeID = "new_traverse"
	meta.FilterPhase = FilterPhaseRelationship
	meta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{path}}}
	graph.Nodes["new_traverse"] = TraverseRelationship{NodeMeta: meta, Input: "filter", Path: path}
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Input = "new_traverse"
	graph.Nodes["aggregate"] = aggregate
	if err := graph.Validate(); err == nil {
		t.Fatal("sealed graph accepted a newly introduced relationship occurrence")
	}
}

func TestApplySecurityBarriersPolicyMapOrderIsCanonical(t *testing.T) {
	left, right := validPlan(), validPlan()
	leftPolicies := map[string]SecurityPolicy{}
	leftPolicies["orders"] = SecurityPolicy{PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")}
	leftPolicies["customers"] = SecurityPolicy{PolicyDigest: securityDigest("3"), DecisionDigest: securityDigest("4")}
	rightPolicies := map[string]SecurityPolicy{}
	rightPolicies["customers"] = SecurityPolicy{PolicyDigest: securityDigest("3"), DecisionDigest: securityDigest("4")}
	rightPolicies["orders"] = SecurityPolicy{PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")}
	if err := ApplySecurityBarriers(left, leftPolicies); err != nil {
		t.Fatalf("left ApplySecurityBarriers() error = %v", err)
	}
	if err := ApplySecurityBarriers(right, rightPolicies); err != nil {
		t.Fatalf("right ApplySecurityBarriers() error = %v", err)
	}
	leftCanonical, err := left.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	rightCanonical, err := right.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftCanonical, rightCanonical) {
		t.Fatalf("equivalent policy maps produced different canonical plans")
	}
}
