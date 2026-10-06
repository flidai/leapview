package signals

import (
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
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

func TestAttachDashboardExploreHrefsOnlyProjectsEligibleUnfilteredAppVisuals(t *testing.T) {
	page := dashboard.Page{ID: "overview"}
	visual := visualizationdefinition.Definition{
		ID: "revenue",
		Query: visualizationdefinition.QueryBinding{
			Kind: visualizationdefinition.QueryAggregate, ModelID: "semantic:sales", DatasetID: "primary",
			Aggregate: &visualizationdefinition.AggregateQueryBinding{
				TableID:    "orders",
				Dimensions: []visualizationdefinition.FieldBinding{},
				Metrics:    []visualizationdefinition.FieldBinding{{FieldID: "revenue", Alias: "revenue"}},
				Limit:      100,
			},
		},
	}
	report := dashboarddefinition.Definition{ID: "dash", Visualizations: map[string]visualizationdefinition.Definition{"revenue": visual}}
	envelope := DashboardEnvelope{Visuals: map[string]DashboardVisualizationSignal{"revenue": {VisualID: "revenue"}}}
	filters := dashboard.Filters{CompiledState: &dashboardfilter.State{AppliedControls: map[string]dashboardfilter.AppliedState{}}}

	model := &semanticmodel.Model{
		Tables:  map[string]semanticmodel.Table{"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"revenue": {}}}},
		Metrics: map[string]semanticmodel.Metric{"revenue": {Type: "aggregate", Dataset: "orders"}},
	}
	AttachDashboardExploreHrefs(&envelope, report, model, page, filters, "", "dash-client", "stream-7")
	href := envelope.Visuals["revenue"].ExploreHref
	if href == nil || *href != "/dashboards/dash/pages/overview/visuals/revenue/explore?clientId=dash-client&streamInstanceId=stream-7" {
		t.Fatalf("exploreHref = %v", href)
	}
	AttachDashboardExploreHrefs(&envelope, report, model, page, filters, "/candidates/candidate-1/projects/project-1", "dash-client", "stream-7")
	if got := envelope.Visuals["revenue"].ExploreHref; got != nil {
		t.Fatalf("candidate preview projected exploreHref = %q", *got)
	}

	filters.Selections = []dashboard.InteractionSelection{{ID: "selection-1"}}
	AttachDashboardExploreHrefs(&envelope, report, model, page, filters, "", "dash-client", "stream-7")
	if got := envelope.Visuals["revenue"].ExploreHref; got != nil {
		t.Fatalf("active interaction left exploreHref = %q", *got)
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
