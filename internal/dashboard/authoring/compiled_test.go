package authoring

import (
	"encoding/json"
	"testing"
	"time"

	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func compiledDefinitionWithVisuals(order ...string) dashboarddefinition.Definition {
	visuals := make(map[string]visualizationdefinition.Definition, len(order))
	for _, id := range order {
		visuals[id] = visualizationdefinition.Definition{ID: id, RendererID: visualizationdefinition.RendererECharts, Spec: visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: visualizationir.VisualizationSpecBase{Kind: "kpi", Title: id}, Kind: "kpi"}}}
	}
	return dashboarddefinition.Definition{ID: "sales", Title: "Sales", SemanticModel: "sales_model", Pages: nil, Visualizations: visuals}
}

func compiledRevisionFixture(t *testing.T, definition dashboarddefinition.Definition) CompiledRevision {
	t.Helper()
	compiled, err := NewCompiledRevision("project-1", "sales", RevisionToken{RevisionID: "rev-1", Number: 1, ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000"}, definition, "state-1", time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func TestCompiledDefinitionHashIsDeterministicAcrossMapOrder(t *testing.T) {
	first, err := DefinitionHash(compiledDefinitionWithVisuals("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DefinitionHash(compiledDefinitionWithVisuals("b", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != len("sha256:")+64 {
		t.Fatalf("definition hashes = %q and %q", first, second)
	}
}

func TestNewCompiledRevisionDeepCopiesDefinitionAndValidatesHash(t *testing.T) {
	definition := compiledDefinitionWithVisuals("sales")
	compiled := compiledRevisionFixture(t, definition)
	definition.Visualizations["sales"].Spec.Value.(*visualizationir.KPIVisualizationSpec).Title = "caller mutation"
	if got := compiled.Definition.Visualizations["sales"].Spec.Value.(*visualizationir.KPIVisualizationSpec).Title; got != "sales" {
		t.Fatalf("compiled definition aliases compiler input: %q", got)
	}
	mutated := compiled
	mutated.Definition.Title = "tampered"
	if err := mutated.Validate(); err == nil {
		t.Fatal("definition hash mismatch unexpectedly validated")
	}
	if err := (CompiledRevision{ProjectID: "project-1", DashboardID: "sales", AuthoredRevision: compiled.AuthoredRevision, Definition: compiled.Definition, DefinitionHash: "sha256:" + "A" + "000000000000000000000000000000000000000000000000000000000000000", SemanticServingStateID: "state-1", CompiledAt: compiled.CompiledAt}).Validate(); err == nil {
		t.Fatal("uppercase definition hash unexpectedly validated")
	}
}

func TestCompiledRevisionJSONRoundTripPreservesInterfaceSpec(t *testing.T) {
	compiled := compiledRevisionFixture(t, compiledDefinitionWithVisuals("sales"))
	encoded, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CompiledRevision
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.Definition.Visualizations["sales"].Spec.Value.(*visualizationir.KPIVisualizationSpec); !ok {
		t.Fatalf("round-tripped visualization spec type = %T", decoded.Definition.Visualizations["sales"].Spec.Value)
	}
}
