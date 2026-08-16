package runtime

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestNewProjectDefinitionCopiesInputsAndAllowsEmptyDashboards(t *testing.T) {
	projectID := projectgraph.ResourceID("project_1")
	modelID := projectgraph.ResourceID("model_1")
	model := &semanticmodel.Model{Name: "display-name"}
	models := map[projectgraph.ResourceID]*semanticmodel.Model{modelID: model}
	dashboards := map[projectgraph.ResourceID]dashboarddefinition.Definition{"dashboard_1": {ID: "dashboard_1", SemanticModel: "model_1", Pages: []dashboard.Page{{ID: "overview"}}}}
	definition, err := NewProjectDefinition(projectID, "Title", "Description", models, dashboards)
	if err != nil {
		t.Fatal(err)
	}
	model.Name = "mutated"
	delete(models, modelID)
	dashboards["dashboard_1"].Pages[0].ID = "mutated"
	if got := definition.Models()[modelID].Name; got != "display-name" {
		t.Fatalf("model mutation leaked into immutable definition: %q", got)
	}
	if got := definition.Dashboards()["dashboard_1"].Pages[0].ID; got != "overview" {
		t.Fatalf("dashboard mutation leaked into immutable definition: %q", got)
	}
}

func TestNewProjectDefinitionRejectsResourceIdentityMismatches(t *testing.T) {
	model := &semanticmodel.Model{Name: "display"}
	models := map[projectgraph.ResourceID]*semanticmodel.Model{"model_1": model}
	if _, err := NewProjectDefinition("project_1", "", "", models, map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_1": {ID: "dashboard_1", SemanticModel: "missing_model"},
	}); err == nil {
		t.Fatal("unknown semantic model unexpectedly accepted")
	}
	if _, err := NewProjectDefinition("project_1", "", "", models, map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_1": {ID: "other_dashboard", SemanticModel: "model_1"},
	}); err == nil {
		t.Fatal("mismatched dashboard identity unexpectedly accepted")
	}
	if _, err := NewProjectDefinition("other_project", "", "", models, nil); err != nil {
		t.Fatal(err)
	}
}

func TestProjectDefinitionResourceIDsAreCanonicalOrder(t *testing.T) {
	models := map[projectgraph.ResourceID]*semanticmodel.Model{
		"model_z": {Name: "z"},
		"model_a": {Name: "a"},
	}
	dashboards := map[projectgraph.ResourceID]dashboarddefinition.Definition{
		"dashboard_z": {ID: "dashboard_z", SemanticModel: "model_a"},
		"dashboard_a": {ID: "dashboard_a", SemanticModel: "model_z"},
	}
	definition, err := NewProjectDefinition("project_1", "", "", models, dashboards)
	if err != nil {
		t.Fatal(err)
	}
	if got := definition.ModelIDs(); len(got) != 2 || got[0] != "model_a" || got[1] != "model_z" {
		t.Fatalf("model IDs = %v, want canonical order", got)
	}
	if got := definition.DashboardIDs(); len(got) != 2 || got[0] != "dashboard_a" || got[1] != "dashboard_z" {
		t.Fatalf("dashboard IDs = %v, want canonical order", got)
	}
}

func TestNewFromGenerationRejectsWrongProjectIdentity(t *testing.T) {
	definition, err := NewProjectDefinition("project_1", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity("project_2", "production", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewFromGeneration(t.Context(), "", nil, identity, definition); err == nil {
		t.Fatal("wrong project identity unexpectedly accepted")
	}
}
