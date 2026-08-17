package signals

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

func TestDashboardInitialEnvelopeUsesCanonicalSemanticModelResourceID(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview"}
	report, err := dashboarddefinition.New("dashboard:showcase", "Showcase", "", "semantic-model:visuals", []dashboard.Page{page}, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{Name: "visuals", Title: "Visuals"}
	envelope := DashboardInitialEnvelope("client", "stream", dashboard.Catalog{}, report, model, map[string]visualizationdefinition.Definition{}, []dashboard.Page{page}, page, dashboard.Filters{})

	for label, got := range map[string]string{
		"runtime":       optionalString(envelope.Runtime.ModelID),
		"page":          envelope.Page.ModelID,
		"agent context": envelope.AgentContext.ModelID,
	} {
		if got != report.SemanticModel {
			t.Fatalf("%s model ID = %q, want canonical %q", label, got, report.SemanticModel)
		}
	}
	if envelope.Page.ModelTitle != model.Title {
		t.Fatalf("model title = %q, want %q", envelope.Page.ModelTitle, model.Title)
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
