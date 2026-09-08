package module

import (
	"context"
	"testing"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

type semanticSnapshotLease struct {
	*resolverTestLease
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l semanticSnapshotLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type semanticSnapshotProvider struct {
	lease runtimehost.Lease
	calls int
}

func (p *semanticSnapshotProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.calls++
	return p.lease, nil
}

func TestSemanticPlannerSnapshotUsesOneCoherentLease(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeInteger}}}}}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	base := &resolverTestLease{runtime: &resolverTestRuntime{planner: planner}, stateID: "generation-1"}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(base.Identity(), graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider := &semanticSnapshotProvider{lease: semanticSnapshotLease{resolverTestLease: base, snapshot: snapshot}}
	m := runtimeMetrics{provider: provider, projectID: "project_1"}
	got, authority, err := m.SemanticPlannerSnapshot(t.Context(), "sales")
	if err != nil {
		t.Fatal(err)
	}
	if got != planner || authority.Identity() != base.Identity() || provider.calls != 1 || base.releases != 1 {
		t.Fatalf("unpaired planner/snapshot: calls=%d releases=%d", provider.calls, base.releases)
	}
	base.stateID = "generation-2"
	if _, _, err := m.SemanticPlannerSnapshot(t.Context(), "sales"); err == nil {
		t.Fatal("mismatched lease snapshot accepted")
	}
	provider.lease = base
	if _, _, err := m.SemanticPlannerSnapshot(t.Context(), "sales"); err == nil {
		t.Fatal("lease without authority accepted")
	}
}
