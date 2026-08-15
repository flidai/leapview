package resolver

import (
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestCompositeResolvesProjectAndInstanceByStableID(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_1")
	project := fakeReports{definitions: map[projectgraph.ResourceID]dashboarddefinition.Definition{"dashboard_1": {ID: "dashboard_1", SemanticModel: "model_1"}}, models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {}}}
	projectResolver := mustProjectResolver(t, project, identity)
	composed, err := NewComposite(identity.ProjectID, projectResolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := composed.Resolve(projectgraph.ResourceID("dashboard_1"))
	if err != nil || resolved.Source.Identity != identity || resolved.Source.Kind != SourceProject {
		t.Fatalf("resolved = %#v, err = %v", resolved, err)
	}

	instance := fakePublished{resolved: Resolved{Definition: dashboarddefinition.Definition{ID: "dashboard_2", SemanticModel: "model_1"}, Model: &semanticmodel.Model{}, SemanticModelID: "model_1", Source: SourceMetadata{Identity: identity, AuthoredRevision: AuthoredRevisionEvidence{ID: "revision", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}}}}
	instanceResolver := mustPublishedResolver(t, instance, identity)
	composed, err = NewComposite(identity.ProjectID, nil, instanceResolver)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = composed.Resolve(projectgraph.ResourceID("dashboard_2"))
	if err != nil || resolved.Source.Kind != SourceInstance || resolved.Source.Identity != identity {
		t.Fatalf("instance resolved = %#v, err = %v", resolved, err)
	}
}

func TestCompositeRejectsCollisionAndScopeMismatch(t *testing.T) {
	identity := mustIdentity(t, "project_1", "production", "generation_1")
	project := mustProjectResolver(t, fakeReports{definitions: map[projectgraph.ResourceID]dashboarddefinition.Definition{"dashboard_1": {ID: "dashboard_1", SemanticModel: "model_1"}}, models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {}}}, identity)
	instance := fakePublished{resolved: Resolved{Definition: dashboarddefinition.Definition{ID: "dashboard_1", SemanticModel: "model_1"}, Model: &semanticmodel.Model{}, SemanticModelID: "model_1", Source: SourceMetadata{Identity: identity, AuthoredRevision: AuthoredRevisionEvidence{ID: "revision", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}}}}
	composed, err := NewComposite(identity.ProjectID, project, mustPublishedResolver(t, instance, identity))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Resolve(projectgraph.ResourceID("dashboard_1")); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want ambiguity", err)
	}
	other := mustIdentity(t, "project_2", "production", "generation_1")
	composed, err = NewComposite(identity.ProjectID, mustProjectResolver(t, fakeReports{definitions: map[projectgraph.ResourceID]dashboarddefinition.Definition{"dashboard_1": {ID: "dashboard_1", SemanticModel: "model_1"}}, models: map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": {}}}, other), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Resolve(projectgraph.ResourceID("dashboard_1")); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error = %v, want scope mismatch", err)
	}
}

func TestResolvedValidationRequiresCanonicalSemanticIdentity(t *testing.T) {
	resolved := Resolved{Definition: dashboarddefinition.Definition{ID: "dashboard_1", SemanticModel: "model_1"}, Model: &semanticmodel.Model{}, SemanticModelID: "other"}
	if err := validateResolved(projectgraph.ResourceID("dashboard_1"), resolved); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error = %v, want semantic scope mismatch", err)
	}
}

func TestResolvedVisualizationUsesDefinition(t *testing.T) {
	want := visualizationdefinition.Definition{ID: "chart"}
	resolved := Resolved{Definition: dashboarddefinition.Definition{ID: "dashboard_1", Visualizations: map[string]visualizationdefinition.Definition{"chart": want}}}
	got, ok := resolved.Visualization(" chart ")
	if !ok || got.ID != want.ID {
		t.Fatalf("visualization = %#v, %v", got, ok)
	}
}

type fakeReports struct {
	definitions map[projectgraph.ResourceID]dashboarddefinition.Definition
	models      map[projectgraph.ResourceID]*semanticmodel.Model
}

func (f fakeReports) Resolve(id projectgraph.ResourceID) (Resolved, error) {
	definition, ok := f.definitions[id]
	if !ok {
		return Resolved{}, ErrNotFound
	}
	model := f.models[projectgraph.ResourceID(definition.SemanticModel)]
	if model == nil {
		return Resolved{}, ErrNotFound
	}
	return Resolved{Definition: definition, Model: model, SemanticModelID: projectgraph.ResourceID(definition.SemanticModel)}, nil
}

type fakePublished struct{ resolved Resolved }

func (f fakePublished) Resolve(projectgraph.ResourceID) (Resolved, error) { return f.resolved, nil }

func mustProjectResolver(t *testing.T, provider Resolver, identity projectgraph.ServingIdentity) Resolver {
	t.Helper()
	resolver, err := NewProject(identityProvider{provider: provider, identity: identity}, identity, SourceMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

type identityProvider struct {
	provider Resolver
	identity projectgraph.ServingIdentity
}

func (p identityProvider) Resolve(id projectgraph.ResourceID) (Resolved, error) {
	resolved, err := p.provider.Resolve(id)
	if err != nil {
		return Resolved{}, err
	}
	resolved.Source.Identity = p.identity
	return resolved, nil
}

func mustPublishedResolver(t *testing.T, provider Resolver, identity projectgraph.ServingIdentity) Resolver {
	t.Helper()
	resolver, err := NewPublished(provider, identity, SourceMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}
