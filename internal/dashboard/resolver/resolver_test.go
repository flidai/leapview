package resolver

import (
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestCompositeProjectOnlyResolutionCarriesProjectSource(t *testing.T) {
	project := fakeReports{definitions: map[string]dashboarddefinition.Definition{"sales": {ID: "sales", SemanticModel: "sales_model"}}, models: map[string]*semanticmodel.Model{"sales_model": {Name: "sales_model"}}, source: SourceMetadata{ServingStateID: "provider-state", SemanticServingStateID: "provider-semantic"}}
	provider := NewProject(project, "workspace-1", SourceMetadata{ProjectID: "project-1", ServingStateID: "state-1", SemanticServingStateID: "semantic-state-1", AuthoredRevision: AuthoredRevisionEvidence{ID: "authoring-must-not-leak"}})
	composed, err := NewComposite("workspace-1", provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := composed.Resolve("sales")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Definition.ID != "sales" || resolved.Model.Name != "sales_model" {
		t.Fatalf("resolved dashboard = %#v model = %#v", resolved.Definition, resolved.Model)
	}
	if resolved.Source.Kind != SourceProject || resolved.Source.WorkspaceID != "workspace-1" || resolved.Source.ProjectID != "project-1" || resolved.Source.ServingStateID != "provider-state" || resolved.Source.SemanticServingStateID != "provider-semantic" || !resolved.Source.AuthoredRevision.IsZero() {
		t.Fatalf("source = %#v", resolved.Source)
	}
}

func TestCompositePublishedResolutionCarriesWorkspaceSource(t *testing.T) {
	published := fakePublished{resolved: Resolved{
		Definition: dashboarddefinition.Definition{ID: "shared", SemanticModel: "model"},
		Model:      &semanticmodel.Model{Name: "model"},
		Source:     SourceMetadata{Kind: SourceProject, ProjectID: "forged", SemanticServingStateID: "semantic-state-exact", AuthoredRevision: AuthoredRevisionEvidence{ID: "published-revision-2", Number: 2, ContentHash: "sha256:" + strings.Repeat("a", 64)}},
	}}
	provider := NewPublished(published, "workspace-1", SourceMetadata{ProjectID: "project-must-not-leak", ServingStateID: "serving-state-must-not-leak", SemanticServingStateID: "forged-semantic-state", AuthoredRevision: AuthoredRevisionEvidence{ID: "published-revision"}})
	composed, err := NewComposite("workspace-1", nil, provider)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := composed.Resolve("shared")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source.Kind != SourceWorkspace || resolved.Source.WorkspaceID != "workspace-1" || resolved.Source.AuthoredRevision.ID != "published-revision-2" || resolved.Source.SemanticServingStateID != "semantic-state-exact" || resolved.Source.ProjectID != "" || resolved.Source.ServingStateID != "" {
		t.Fatalf("source = %#v", resolved.Source)
	}
	if _, ok := resolved.Visualization("missing"); ok {
		t.Fatal("missing visualization resolved")
	}
}

func TestCompositeRejectsProjectWorkspaceIDCollision(t *testing.T) {
	project := fakeReports{definitions: map[string]dashboarddefinition.Definition{"same": {ID: "same", SemanticModel: "project_model"}}, models: map[string]*semanticmodel.Model{"project_model": {Name: "project_model"}}}
	published := fakePublished{resolved: Resolved{Definition: dashboarddefinition.Definition{ID: "same", SemanticModel: "workspace_model"}, Model: &semanticmodel.Model{Name: "workspace_model"}, Source: SourceMetadata{AuthoredRevision: AuthoredRevisionEvidence{ID: "revision", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}, SemanticServingStateID: "semantic-state"}}}
	composed, err := NewComposite("workspace-1", NewProject(project, "workspace-1", SourceMetadata{}), NewPublished(published, "workspace-1", SourceMetadata{}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = composed.Resolve("same")
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("error = %v, want ErrAmbiguous", err)
	}
}

func TestCompositeNotFoundAndProjectMissDoNotShadow(t *testing.T) {
	project := fakeReports{definitions: map[string]dashboarddefinition.Definition{"sales": {ID: "sales", SemanticModel: "sales_model"}}, models: map[string]*semanticmodel.Model{"sales_model": {Name: "sales_model"}}}
	composed, err := NewComposite("workspace-1", NewProject(project, "workspace-1", SourceMetadata{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Resolve("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	resolved, err := composed.Resolve(" sales ")
	if err != nil || resolved.Definition.ID != "sales" {
		t.Fatalf("trimmed project lookup = %#v, %v", resolved, err)
	}
}

func TestCompositeRejectsProjectScopeMismatchWithoutFallback(t *testing.T) {
	project := fakeReports{definitions: map[string]dashboarddefinition.Definition{"sales": {ID: "sales", SemanticModel: "sales_model"}}, models: map[string]*semanticmodel.Model{"sales_model": {Name: "sales_model"}}}
	fallback := fakeReports{definitions: map[string]dashboarddefinition.Definition{"sales": {ID: "sales", SemanticModel: "sales_model"}}, models: map[string]*semanticmodel.Model{"sales_model": {Name: "sales_model"}}}
	composed, err := NewComposite("workspace-a", NewProject(project, "workspace-b", SourceMetadata{}), NewProject(fallback, "workspace-a", SourceMetadata{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Resolve("sales"); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error = %v, want ErrScopeMismatch", err)
	}
}

func TestCompositeRejectsPublishedScopeMismatchWithoutFallback(t *testing.T) {
	project := fakeReports{definitions: map[string]dashboarddefinition.Definition{"sales": {ID: "sales", SemanticModel: "sales_model"}}, models: map[string]*semanticmodel.Model{"sales_model": {Name: "sales_model"}}}
	published := fakePublished{resolved: Resolved{Definition: dashboarddefinition.Definition{ID: "sales", SemanticModel: "sales_model"}, Model: &semanticmodel.Model{Name: "sales_model"}, Source: SourceMetadata{AuthoredRevision: AuthoredRevisionEvidence{ID: "revision", Number: 1, ContentHash: "sha256:" + strings.Repeat("a", 64)}, SemanticServingStateID: "semantic-state"}}}
	composed, err := NewComposite("workspace-a", NewProject(project, "workspace-a", SourceMetadata{}), NewPublished(published, "workspace-b", SourceMetadata{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composed.Resolve("sales"); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error = %v, want ErrScopeMismatch", err)
	}
}

func TestResolvedVisualizationUsesDefinition(t *testing.T) {
	want := visualizationdefinition.Definition{ID: "chart"}
	resolved := Resolved{Definition: dashboarddefinition.Definition{ID: "sales", Visualizations: map[string]visualizationdefinition.Definition{"chart": want}}}
	got, ok := resolved.Visualization(" chart ")
	if !ok || got.ID != want.ID {
		t.Fatalf("visualization = %#v, %v", got, ok)
	}
}

type fakeReports struct {
	definitions map[string]dashboarddefinition.Definition
	models      map[string]*semanticmodel.Model
	source      SourceMetadata
}

func (f fakeReports) Resolve(id string) (Resolved, error) {
	definition, ok := f.definitions[id]
	if !ok {
		return Resolved{}, ErrNotFound
	}
	model := f.models[definition.SemanticModel]
	if model == nil {
		return Resolved{}, ErrNotFound
	}
	return Resolved{Definition: definition, Model: model, Source: f.source}, nil
}

type fakePublished struct {
	resolved Resolved
	err      error
}

func (f fakePublished) Resolve(string) (Resolved, error) {
	if f.err != nil {
		return Resolved{}, f.err
	}
	return f.resolved, nil
}
