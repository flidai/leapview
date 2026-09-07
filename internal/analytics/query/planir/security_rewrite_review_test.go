package planir

import (
	"bytes"
	"testing"
)

func TestSecurityRewritePreservesEquivalentNodeOrdering(t *testing.T) {
	before := validPlan()
	predicate := Predicate{Kind: PredicateCompare, Field: "status", Operator: "=", Value: Literal{Kind: LiteralString, String: "allowed"}}
	if err := ApplySecurityBarriers(before, map[string]SecurityPolicy{"orders": {
		PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2"), Predicate: &predicate,
	}}); err != nil {
		t.Fatal(err)
	}
	after := *before
	after.Nodes = map[string]Node{}
	ids := sortedNodeIDs(before)
	for i := len(ids) - 1; i >= 0; i-- {
		after.Nodes[ids[i]] = before.Nodes[ids[i]]
	}
	if err := ValidateSecurityRewrite(before, &after); err != nil {
		t.Fatalf("equivalent map ordering rejected: %v", err)
	}
	left, err := before.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	right, err := after.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("equivalent map ordering changed canonical security identity")
	}
	// Moving the predicate above a request filter must not be accepted even
	// when the same predicate and policy identity remain in the graph.
	filter := after.Nodes["filter"].(FilterRows)
	barrier := after.Nodes[filter.Input].(SecurityBarrier)
	filter.Input = barrier.Input
	barrier.Input = filter.NodeID
	after.Nodes[filter.NodeID] = filter
	after.Nodes[barrier.NodeID] = barrier
	aggregate := after.Nodes["aggregate"].(AggregateMetrics)
	aggregate.Input = barrier.NodeID
	after.Nodes["aggregate"] = aggregate
	if err := ValidateSecurityRewrite(before, &after); err == nil {
		t.Fatal("relocated barrier accepted")
	}
}

func TestSecurityBarrierRejectsMissingOrMalformedProtection(t *testing.T) {
	t.Run("required scan without barrier", func(t *testing.T) {
		graph := validPlan()
		scan := graph.Nodes["scan"].(ScanDataset)
		scan.RequiresSecurityBarrier = true
		graph.Nodes["scan"] = scan
		if _, err := RenderDuckDB(graph); err == nil {
			t.Fatal("unprotected required scan rendered")
		}
	})
	t.Run("malformed predicate", func(t *testing.T) {
		graph := validPlan()
		predicate := Predicate{Kind: "unknown"}
		if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{"orders": {
			PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2"), Predicate: &predicate,
		}}); err == nil {
			t.Fatal("malformed security predicate accepted")
		}
	})
}
