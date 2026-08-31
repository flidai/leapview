package signals

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
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

func TestDashboardInitialEnvelopeCarriesTheCatalogAppearance(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview"}
	report, err := dashboarddefinition.New("dashboard:showcase", "Showcase", "", "semantic-model:visuals", []dashboard.Page{page}, nil)
	if err != nil {
		t.Fatal(err)
	}
	catalog := dashboard.Catalog{Dashboards: []dashboard.CatalogDashboard{{
		ID:         "dashboard:showcase",
		Appearance: dashboardappearance.Value{Icon: "gallery-vertical-end", Color: "blue"},
	}}}

	envelope := DashboardInitialEnvelope("client", "stream", catalog, report, nil, map[string]visualizationdefinition.Definition{}, []dashboard.Page{page}, page, dashboard.Filters{})

	if envelope.Page.AppearanceIcon != "gallery-vertical-end" {
		t.Fatalf("appearance icon = %q, want %q", envelope.Page.AppearanceIcon, "gallery-vertical-end")
	}
	if envelope.Page.AppearanceColor != "blue" {
		t.Fatalf("appearance color = %q, want %q", envelope.Page.AppearanceColor, "blue")
	}
}

func TestDashboardInitialEnvelopeDefaultsTheAppearanceWhenTheCatalogEntryIsMissing(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview"}
	report, err := dashboarddefinition.New("dashboard:showcase", "Showcase", "", "semantic-model:visuals", []dashboard.Page{page}, nil)
	if err != nil {
		t.Fatal(err)
	}

	envelope := DashboardInitialEnvelope("client", "stream", dashboard.Catalog{}, report, nil, map[string]visualizationdefinition.Definition{}, []dashboard.Page{page}, page, dashboard.Filters{})

	if envelope.Page.AppearanceIcon != dashboardappearance.DefaultIcon {
		t.Fatalf("appearance icon = %q, want default %q", envelope.Page.AppearanceIcon, dashboardappearance.DefaultIcon)
	}
	if envelope.Page.AppearanceColor != dashboardappearance.DefaultColor {
		t.Fatalf("appearance color = %q, want default %q", envelope.Page.AppearanceColor, dashboardappearance.DefaultColor)
	}
}

func TestReportPageHeaderDetailUsesThePageTitleWithoutItsNavigationOrdinal(t *testing.T) {
	page := dashboard.Page{ID: "overview", Title: "Overview"}

	if got := ReportPageHeaderDetail(page); got != "Overview" {
		t.Fatalf("header detail = %q, want page title without navigation ordinal", got)
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
