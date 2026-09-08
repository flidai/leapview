package planir

import (
	"strings"
	"testing"
)

func TestApplySecurityBarriersUsesDistinctRolePlayingTargetOccurrences(t *testing.T) {
	graph := validPlan()
	firstPath := RelationshipPath{
		Name: "order_customer", FromDataset: "orders", ToDataset: "customers", ToRelation: "customers",
		JoinKeys: []JoinKey{{From: "id", To: "order_id"}},
	}
	secondPath := RelationshipPath{
		Name: "customer_parent", FromDataset: "customers", ToDataset: "customers", ToRelation: "customers",
		JoinKeys: []JoinKey{{From: "parent_id", To: "id"}},
	}
	firstMeta := graph.Nodes["filter"].Meta()
	firstMeta.NodeID = "traverse_first"
	firstMeta.FilterPhase = FilterPhaseRelationship
	firstMeta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{firstPath}}}
	graph.Nodes["traverse_first"] = TraverseRelationship{NodeMeta: firstMeta, Input: "filter", Path: firstPath}
	secondMeta := firstMeta
	secondMeta.NodeID = "traverse_second"
	secondMeta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "orders", Edges: []RelationshipPath{firstPath, secondPath}}}
	graph.Nodes["traverse_second"] = TraverseRelationship{NodeMeta: secondMeta, Input: "traverse_first", Path: secondPath}
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Input = "traverse_second"
	graph.Nodes["aggregate"] = aggregate

	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"orders":    {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")},
		"customers": {PolicyDigest: securityDigest("3"), DecisionDigest: securityDigest("4")},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	first := graph.Nodes["traverse_first"].(TraverseRelationship)
	second := graph.Nodes["traverse_second"].(TraverseRelationship)
	if first.TargetInput == "" || second.TargetInput == "" || first.TargetInput == second.TargetInput {
		t.Fatalf("target barriers = %q, %q, want distinct occurrences", first.TargetInput, second.TargetInput)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	if count := strings.Count(rendered.SQL, `LEFT JOIN (SELECT * FROM customers`); count != 2 {
		t.Fatalf("protected target subqueries = %d, want 2: %s", count, rendered.SQL)
	}
	if strings.Contains(rendered.SQL, `AS "r2" ON "r2"`) {
		t.Fatalf("role-playing joins reused an alias: %s", rendered.SQL)
	}
}
