package tools

import (
	"testing"

	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestAgentVisualOmittedBudgetUsesAgentLimit(t *testing.T) {
	visual := testAgentVisualWithLimit("bar", 5)
	visual.DataBudget = nil
	compiled, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, testAgentModel(), "chart")
	if err != nil {
		t.Fatal(err)
	}
	base, err := visualizationir.SpecificationBase(compiled.Visualizations["chart"].Spec)
	if err != nil {
		t.Fatal(err)
	}
	if base.DataBudget.MaxRows != maxVisualRows {
		t.Fatalf("budget = %d", base.DataBudget.MaxRows)
	}
	if visual.DataBudget != nil {
		t.Fatal("mutated input")
	}
}

func TestAgentVisualExplicitExcessBudgetStillRejected(t *testing.T) {
	visual := testAgentVisualWithLimit("bar", 5)
	visual.DataBudget = &dashboarddocument.DashboardDataBudget{MaxRows: 1000}
	if _, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, testAgentModel(), "chart"); err == nil {
		t.Fatal("accepted excessive budget")
	}
}

func TestAgentVisualOmittedQueryLimitUsesAgentBudget(t *testing.T) {
	visual := testAgentVisual("bar")
	_, err := compileAgentVisual(agentVisualInput{Visual: visual, Model: "commerce"}, testAgentModel(), "chart")
	if err != nil {
		t.Fatal(err)
	}
	if visual.Query.Value.(*dashboarddocument.AggregateDashboardQuery).Limit != nil {
		t.Fatal("mutated input query")
	}
}
