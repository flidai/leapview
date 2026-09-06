package http

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestDataExplorerAccessPredicateUsesCompiledPolicyAndCanonicalResolution(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	handler := &BrowserHandler{ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, registry, definition, "us"), nil
	}}

	predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
	if err != nil {
		t.Fatalf("data explorer access predicate: %v", err)
	}
	if predicate == nil || !predicate("semantic:sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("canonical matching resolution was denied for protected orders dataset")
	}

	handler.ResolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, registry, definition, "eu"), nil
	}
	predicate, err = handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
	if err != nil {
		t.Fatalf("denied resolution unexpectedly failed to construct predicate: %v", err)
	}
	if predicate == nil || predicate("semantic:sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("non-matching canonical resolution was allowed for protected orders dataset")
	}
}

func TestDataExplorerAccessPredicateFailsClosedForMissingAuthorityOrSubject(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	for name, resolve := range map[string]func(context.Context) (access.SemanticAttributeResolution, error){
		"missing authority": nil,
		"resolver error": func(context.Context) (access.SemanticAttributeResolution, error) {
			return access.SemanticAttributeResolution{}, errors.New("authority unavailable")
		},
		"missing subject": func(context.Context) (access.SemanticAttributeResolution, error) {
			resolution := dataExplorerResolution(t, registry, definition, "us")
			resolution.Subject = access.SubjectRef{}
			return resolution, nil
		},
		"group subject": func(context.Context) (access.SemanticAttributeResolution, error) {
			resolution := dataExplorerResolution(t, registry, definition, "us")
			resolution.Subject = access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group-1"}
			return resolution, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := &BrowserHandler{ResolveSemanticAttributes: resolve}
			predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
			if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) {
				t.Fatalf("predicate error = %v, want %v", err, errDataExplorerSemanticAccessUnavailable)
			}
			if predicate != nil {
				t.Fatal("predicate was returned for unavailable semantic authority")
			}
		})
	}
}

func TestDataExplorerAccessPredicateFailsClosedForStaleRegistry(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	stale := registry
	stale.State.Revision++
	handler := &BrowserHandler{ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, stale, definition, "us"), nil
	}}
	predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
	if err != nil {
		t.Fatalf("stale registry unexpectedly failed predicate construction: %v", err)
	}
	if predicate == nil || predicate("semantic:sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("stale registry resolution was allowed by compiled protected policy")
	}
}

func TestDataExplorerAccessPredicateRejectsAuthoredPolicyMutation(t *testing.T) {
	project, compiled, _, _ := dataExplorerAuthorizationFixture(t, "us")
	project.SemanticModels["semantic:sales"].AccessGrants["region_grant"] = semanticmodel.SemanticAccessGrantSpec{
		UserAttribute: "region", AllowedValues: []any{"eu"},
	}
	called := false
	handler := &BrowserHandler{ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		called = true
		return access.SemanticAttributeResolution{}, nil
	}}
	predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
	if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) {
		t.Fatalf("mutated policy error = %v, want %v", err, errDataExplorerSemanticAccessUnavailable)
	}
	if predicate != nil {
		t.Fatal("predicate was returned for mutated authored policy")
	}
	if called {
		t.Fatal("authority was resolved before rejecting stale compiled policy")
	}
}

func TestDataExplorerAccessPredicateRejectsStrippedPolicy(t *testing.T) {
	project, compiled, _, _ := dataExplorerAuthorizationFixture(t, "us")
	model := project.SemanticModels["semantic:sales"]
	model.AccessGrants = nil
	for name, dataset := range model.Datasets {
		dataset.RequiredAccessGrants = nil
		dataset.AccessFilters = nil
		model.Datasets[name] = dataset
	}
	predicate, err := (&BrowserHandler{}).dataExplorerAccessPredicate(context.Background(), project, compiled)
	if err == nil || predicate != nil {
		t.Fatal("stripped authored policy bypassed compiled protection")
	}
}

func dataExplorerAuthorizationFixture(t testing.TB, region string) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, access.SemanticAttributeRegistrySnapshot, access.SemanticAttributeDefinition) {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "def-region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:registry"},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	model := &semanticmodel.Model{
		Name: "sales",
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders", RequiredAccessGrants: []string{"region_grant"}},
		},
	}
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatalf("compile protected semantic model: %v", err)
	}
	return projectmanifest.Project{ID: "project:demo", SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}, registry, definition
}

func dataExplorerResolution(t testing.TB, registry access.SemanticAttributeRegistrySnapshot, definition access.SemanticAttributeDefinition, region string) access.SemanticAttributeResolution {
	t.Helper()
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, region)
	if err != nil {
		t.Fatalf("canonical semantic attribute value: %v", err)
	}
	return access.SemanticAttributeResolution{
		Subject:      access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		Registry:     registry,
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control"},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}
