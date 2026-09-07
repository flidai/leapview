package planir

import (
	"strings"
	"testing"
)

func TestApplySecurityBarriersRedirectsProtectedScanOutput(t *testing.T) {
	graph := validPlan()
	delete(graph.Nodes, "filter")
	delete(graph.Nodes, "aggregate")
	scan := graph.Nodes["scan"].(ScanDataset)
	graph.NodeMeta = scan.NodeMeta
	graph.Roots = []string{"scan"}
	graph.Output = "scan"
	if err := graph.Validate(); err != nil {
		t.Fatalf("scan-only graph Validate() error = %v", err)
	}
	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"orders": {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	if graph.Output != "scan_security_barrier" {
		t.Fatalf("graph output = %q, want protected barrier", graph.Output)
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("sealed scan-only graph Validate() error = %v", err)
	}
	if _, err := graph.Canonical(); err != nil {
		t.Fatalf("sealed scan-only graph Canonical() error = %v", err)
	}
	if !strings.Contains(graph.Output, "security_barrier") {
		t.Fatalf("output does not identify the security barrier: %q", graph.Output)
	}
}
