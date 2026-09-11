package module

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticCatalogAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

func (r *semanticCatalogAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

type semanticCatalogLease struct {
	runtime  projectruntime.Runtime
	snapshot accesssnapshot.AuthorizationSnapshot
}

func (l semanticCatalogLease) Runtime() projectruntime.Runtime        { return l.runtime }
func (l semanticCatalogLease) Release()                               {}
func (l semanticCatalogLease) Identity() projectgraph.ServingIdentity { return l.snapshot.Identity() }
func (l semanticCatalogLease) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

func TestSemanticCatalogUsesLeaseCompiledProtection(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeInteger}}}}}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLease{runtime: projectDefinitionRuntimeStub{definition: projectmanifest.ResourceManifest{SemanticModels: map[string]*semanticmodel.Model{"semantic_sales": model}}, compiled: map[string]*semanticquery.CompiledModel{"semantic_sales": planner.CompiledModel()}}, snapshot: snapshot}
	visibility := SemanticCatalogVisibility("instance-1", nil, nil)
	allowed, err := visibility(t.Context(), lease, "alice", "semantic_sales")
	if err != nil || !allowed {
		t.Fatalf("public visibility = %v, %v", allowed, err)
	}
	model.AccessPolicy.Datasets = map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"unknown"}}}
	allowed, err = visibility(t.Context(), lease, "alice", "semantic_sales")
	if allowed || err == nil {
		t.Fatalf("mismatched model visibility = %v, %v", allowed, err)
	}
	var absent projectcatalog.Lease = semanticCatalogLease{snapshot: snapshot}
	if allowed, err := visibility(t.Context(), absent, "alice", "semantic_sales"); allowed || err == nil {
		t.Fatal("missing runtime became public")
	}
}

func TestProtectedSemanticCatalogRequiresDurableAudit(t *testing.T) {
	model := &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {}}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_demo", "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	lease := semanticCatalogLease{
		runtime: projectDefinitionRuntimeStub{
			definition: projectmanifest.ResourceManifest{SemanticModels: map[string]*semanticmodel.Model{"semantic_sales": model}},
			compiled:   map[string]*semanticquery.CompiledModel{"semantic_sales": planner.CompiledModel()},
		},
		snapshot: snapshot,
	}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	controlDigest, err := access.SemanticAttributeControlDigest(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(context.Context) (access.SemanticAttributeResolution, error) {
		principal := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
		return access.SemanticAttributeResolution{
			Subject: principal, Subjects: []access.SubjectRef{principal},
			Registry:   access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest}},
			Control:    access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest}},
			ObservedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
		}, nil
	}

	recorder := &semanticCatalogAuditRecorder{}
	visibility := SemanticCatalogVisibility("instance-1", resolve, recorder)
	allowed, err := visibility(t.Context(), lease, "alice", "semantic_sales")
	if err != nil || !allowed {
		t.Fatalf("protected visibility = %v, %v", allowed, err)
	}
	if len(recorder.events) != 1 || recorder.events[0].Action != semanticquery.SemanticAccessAuditAuthorization || recorder.events[0].Status != "success" {
		t.Fatalf("protected visibility audit = %#v", recorder.events)
	}

	allowed, err = SemanticCatalogVisibility("instance-1", resolve, nil)(t.Context(), lease, "alice", "semantic_sales")
	if allowed || err == nil {
		t.Fatalf("protected visibility without audit = %v, %v", allowed, err)
	}

	auditUnavailable := errors.New("audit unavailable")
	allowed, err = SemanticCatalogVisibility("instance-1", resolve, &semanticCatalogAuditRecorder{err: auditUnavailable})(t.Context(), lease, "alice", "semantic_sales")
	if allowed || !errors.Is(err, auditUnavailable) {
		t.Fatalf("protected visibility with failed audit = %v, %v", allowed, err)
	}
}
