package module

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type authorizedModelRuntimeStub struct {
	definition    projectmanifest.ResourceManifest
	compiled      map[string]*semanticquery.CompiledModel
	identity      projectgraph.ServingIdentity
	manifestReads int
	modelReads    int
	compiledReads int
}

func (r *authorizedModelRuntimeStub) Close() error                           { return nil }
func (r *authorizedModelRuntimeStub) Identity() projectgraph.ServingIdentity { return r.identity }

func (r *authorizedModelRuntimeStub) ProjectManifest() projectmanifest.ResourceManifest {
	r.manifestReads++
	return r.definition
}

func (r *authorizedModelRuntimeStub) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	r.modelReads++
	model, ok := r.definition.SemanticModels[modelID]
	return model, ok && model != nil
}

func (r *authorizedModelRuntimeStub) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	r.compiledReads++
	compiled, ok := r.compiled[modelID]
	return compiled, ok && compiled != nil
}

type authorizedModelLeaseStub struct {
	runtime  projectruntime.Runtime
	identity projectgraph.ServingIdentity
	snapshot accesssnapshot.AuthorizationSnapshot
	released bool
}

func (l *authorizedModelLeaseStub) Runtime() projectruntime.Runtime        { return l.runtime }
func (l *authorizedModelLeaseStub) Identity() projectgraph.ServingIdentity { return l.identity }
func (l *authorizedModelLeaseStub) Release()                               { l.released = true }
func (l *authorizedModelLeaseStub) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return l.snapshot
}

type authorizedModelProviderStub struct {
	lease        *authorizedModelLeaseStub
	acquisitions int
}

func (p *authorizedModelProviderStub) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquisitions++
	return p.lease, nil
}

type authorizedModelSubjectsStub struct {
	byActor map[string][]access.SubjectRef
	err     error
}

func (s authorizedModelSubjectsStub) Resolve(_ context.Context, actorID string) ([]access.SubjectRef, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]access.SubjectRef(nil), s.byActor[actorID]...), nil
}

func authorizedModelFixture(t *testing.T, allow bool) (*authorizedModelProviderStub, *authorizedModelRuntimeStub, AuthorizationSubjects) {
	t.Helper()
	model := &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}}},
	}
	planner, err := semanticquery.NewCompiledPlanner(model)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project:demo", "production", "generation:test")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := access.NewSubjectRef(access.SubjectKindPrincipal, "principal:alice")
	if err != nil {
		t.Fatal(err)
	}
	var grants []accesssnapshot.Grant
	if allow {
		resource, err := access.NewResourceRef("semantic:sales", projectgraph.KindSemanticModel)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := access.NewCanonicalGrant(project, principal, resource, access.CapabilityResourceUse)
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{{ID: "grant_use", Canonical: canonical}}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, project, grants, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &authorizedModelRuntimeStub{definition: projectmanifest.ResourceManifest{SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": planner.CompiledModel()}}
	runtime.identity = identity
	provider := &authorizedModelProviderStub{lease: &authorizedModelLeaseStub{runtime: runtime, identity: identity, snapshot: snapshot}}
	subjects := func(_ context.Context, actorID string) ([]access.SubjectRef, error) {
		return authorizedModelSubjectsStub{byActor: map[string][]access.SubjectRef{actorID: {principal}}}.Resolve(context.Background(), actorID)
	}
	return provider, runtime, subjects
}

func TestAuthorizedProjectModelReaderAuthorizesBeforeReadingDefinition(t *testing.T) {
	provider, runtime, subjects := authorizedModelFixture(t, false)
	reader := NewAuthorizedProjectDefinitionReader(provider, subjects).(AuthorizedProjectModelReader)
	_, _, _, err := reader.AuthorizedExploreModel(t.Context(), "project:demo", "principal:alice", "semantic:sales")
	if err == nil || runtime.manifestReads != 0 || runtime.modelReads != 0 || runtime.compiledReads != 0 {
		t.Fatalf("denied model read = %v, manifest/model/compiled reads = %d/%d/%d; want denial before metadata", err, runtime.manifestReads, runtime.modelReads, runtime.compiledReads)
	}
	if provider.acquisitions != 1 || !provider.lease.released {
		t.Fatalf("lease lifecycle = acquisitions:%d released:%v", provider.acquisitions, provider.lease.released)
	}
}

func TestAuthorizedProjectModelReaderReturnsOneLeaseBoundModelAndPlanner(t *testing.T) {
	provider, runtime, subjects := authorizedModelFixture(t, true)
	reader := NewAuthorizedProjectDefinitionReader(provider, subjects).(AuthorizedProjectModelReader)
	model, compiled, identity, err := reader.AuthorizedExploreModel(t.Context(), "project:demo", "principal:alice", "semantic:sales")
	if err != nil {
		t.Fatal(err)
	}
	if model == nil || compiled == nil || !compiled.MatchesModel(model) || identity.GenerationID != "generation:test" {
		t.Fatalf("authorized result = model:%#v compiled:%#v identity:%#v", model, compiled, identity)
	}
	if runtime.manifestReads != 0 || runtime.modelReads != 1 || runtime.compiledReads != 1 || provider.acquisitions != 1 || !provider.lease.released {
		t.Fatalf("reads/lifecycle = manifest:%d model:%d compiled:%d acquire:%d released:%v", runtime.manifestReads, runtime.modelReads, runtime.compiledReads, provider.acquisitions, provider.lease.released)
	}
}

func TestAuthorizedProjectModelReaderFailsClosedOnSubjectResolution(t *testing.T) {
	provider, runtime, _ := authorizedModelFixture(t, true)
	reader := NewAuthorizedProjectDefinitionReader(provider, func(context.Context, string) ([]access.SubjectRef, error) {
		return nil, errors.New("subject directory unavailable")
	}).(AuthorizedProjectModelReader)
	_, _, _, err := reader.AuthorizedExploreModel(t.Context(), "project:demo", "principal:alice", "semantic:sales")
	if err == nil || runtime.manifestReads != 0 || runtime.modelReads != 0 || runtime.compiledReads != 0 {
		t.Fatalf("subject failure = %v, reads = %d/%d/%d; want fail closed before metadata", err, runtime.manifestReads, runtime.modelReads, runtime.compiledReads)
	}
}

func TestAuthorizedProjectModelReaderRejectsLeaseIdentityMismatch(t *testing.T) {
	provider, runtime, subjects := authorizedModelFixture(t, true)
	provider.lease.identity.ProjectID = "project:other"
	reader := NewAuthorizedProjectDefinitionReader(provider, subjects).(AuthorizedProjectModelReader)
	_, _, _, err := reader.AuthorizedExploreModel(t.Context(), "project:demo", "principal:alice", "semantic:sales")
	if err == nil || runtime.manifestReads != 0 || runtime.modelReads != 0 || runtime.compiledReads != 0 {
		t.Fatalf("identity mismatch = %v, reads = %d/%d/%d; want fail closed before metadata", err, runtime.manifestReads, runtime.modelReads, runtime.compiledReads)
	}
}

func TestAuthorizedProjectModelReaderRejectsRuntimeIdentityMismatch(t *testing.T) {
	provider, runtime, subjects := authorizedModelFixture(t, true)
	runtime.identity.GenerationID = "generation:other"
	reader := NewAuthorizedProjectDefinitionReader(provider, subjects).(AuthorizedProjectModelReader)
	_, _, _, err := reader.AuthorizedExploreModel(t.Context(), "project:demo", "principal:alice", "semantic:sales")
	if err == nil || runtime.manifestReads != 0 || runtime.modelReads != 0 || runtime.compiledReads != 0 {
		t.Fatalf("runtime identity mismatch = %v, reads = %d/%d/%d; want fail closed before metadata", err, runtime.manifestReads, runtime.modelReads, runtime.compiledReads)
	}
}
