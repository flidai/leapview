package planir

import (
	"strings"
	"testing"
)

func TestSecurityBarrierAliasSkipsDatasetNamedLikeGeneratedAlias(t *testing.T) {
	graph := validPlan()
	graph.NodeMeta.RootDatasets = []string{"r2"}
	scan := graph.Nodes["scan"].(ScanDataset)
	scan.Dataset = "r2"
	scan.NodeMeta.RootDatasets = []string{"r2"}
	graph.Nodes["scan"] = scan
	filter := graph.Nodes["filter"].(FilterRows)
	filter.RootDatasets = []string{"r2"}
	graph.Nodes["filter"] = filter
	aggregate := graph.Nodes["aggregate"].(AggregateMetrics)
	aggregate.RootDatasets = []string{"r2"}
	aggregate.Input = "filter"
	graph.Nodes["aggregate"] = aggregate
	path := RelationshipPath{
		Name: "customers", FromDataset: "r2", ToDataset: "customers", ToRelation: "customers",
		JoinKeys: []JoinKey{{From: "id", To: "order_id"}},
	}
	meta := filter.NodeMeta
	meta.NodeID = "traverse"
	meta.FilterPhase = FilterPhaseRelationship
	meta.RelationshipRoutes = []RelationshipRoute{{RootDataset: "r2", Edges: []RelationshipPath{path}}}
	graph.Nodes["traverse"] = TraverseRelationship{NodeMeta: meta, Input: "filter", Path: path}
	aggregate.Input = "traverse"
	graph.Nodes["aggregate"] = aggregate
	if err := ApplySecurityBarriers(graph, map[string]SecurityPolicy{
		"r2":        {PolicyDigest: securityDigest("1"), DecisionDigest: securityDigest("2")},
		"customers": {PolicyDigest: securityDigest("3"), DecisionDigest: securityDigest("4")},
	}); err != nil {
		t.Fatalf("ApplySecurityBarriers() error = %v", err)
	}
	rendered, err := RenderDuckDB(graph)
	if err != nil {
		t.Fatalf("RenderDuckDB() error = %v", err)
	}
	if strings.Contains(rendered.SQL, `LEFT JOIN (SELECT * FROM customers`) && strings.Contains(rendered.SQL, `AS "r2" ON`) {
		t.Fatalf("target reused the root dataset alias r2: %s", rendered.SQL)
	}
	if !strings.Contains(rendered.SQL, `AS "r3" ON`) {
		t.Fatalf("target did not receive the next free alias: %s", rendered.SQL)
	}
}
