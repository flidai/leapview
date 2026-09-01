package ui

import (
	"html"
	"net/url"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalPageDefinition(t *testing.T) dashboarddefinition.Definition {
	t.Helper()
	base := visualizationir.VisualizationSpecBase{Kind: "kpi", Title: "Orders", Accessibility: visualizationir.VisualizationAccessibility{Title: "Orders", Description: "Orders"}, Datasets: []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: []visualizationir.VisualizationField{{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"}}}}, DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 1}}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: base, Kind: "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"}, Presentation: visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable}}}
	visual, err := visualizationdefinition.New("active", spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: "model", DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: "orders", Metrics: []visualizationdefinition.FieldBinding{{FieldID: "order_count", Alias: "value"}}, Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	offPage := visual
	offPage.ID = "off_page"
	return dashboarddefinition.Definition{ID: "report", Title: "Report", SemanticModel: "model", Pages: []dashboard.Page{{ID: "showcase", Title: "Showcase", Visuals: []dashboard.PageVisual{{ID: "active", Kind: "visual", Visual: "active"}}}, {ID: "tables", Title: "Tables", Visuals: []dashboard.PageVisual{{ID: "off", Kind: "visual", Visual: "off_page"}}}}, Visualizations: map[string]visualizationdefinition.Definition{"active": visual, "off_page": offPage}}
}

func canonicalPageModel() *semanticmodel.Model { return &semanticmodel.Model{Name: "model"} }
func canonicalRenderedPage(t *testing.T, report dashboarddefinition.Definition, active dashboard.Page) string {
	t.Helper()
	var out strings.Builder
	if err := Page("client", "", dashboard.Catalog{}, report, canonicalPageModel(), report.Pages, active, dashboard.Filters{}).Render(&out); err != nil {
		t.Fatal(err)
	}
	return html.UnescapeString(out.String())
}
func canonicalStreamID(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "/updates?")
	if start < 0 {
		t.Fatal("updates URL missing")
	}
	end := strings.IndexAny(body[start:], "'\"")
	if end < 0 {
		t.Fatal("updates URL unterminated")
	}
	parsed, err := url.Parse(body[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query().Get("streamInstance")
}

func TestCanonicalPageInitialSignalsArePageScoped(t *testing.T) {
	report := canonicalPageDefinition(t)
	rendered := canonicalRenderedPage(t, report, report.Pages[0])
	if !strings.Contains(rendered, `dashboard-id="report"`) || !strings.Contains(rendered, `page-id="showcase"`) || strings.Contains(rendered, `page-id="tables"`) {
		t.Fatalf("page scope leaked: %s", rendered)
	}
	if !strings.Contains(rendered, `data-on:lv-interaction-select`) {
		t.Fatal("command bridge missing")
	}
}

func TestCanonicalPageCreatesUniqueStreamInstancePerRender(t *testing.T) {
	report := dashboarddefinition.Definition{ID: "report", SemanticModel: "model", Pages: []dashboard.Page{{ID: "overview"}}, Visualizations: map[string]visualizationdefinition.Definition{}}
	first, second := canonicalStreamID(t, canonicalRenderedPage(t, report, report.Pages[0])), canonicalStreamID(t, canonicalRenderedPage(t, report, report.Pages[0]))
	if first == "" || second == "" || first == second {
		t.Fatalf("stream instances=%q,%q", first, second)
	}
}

func TestCanonicalPrivateRouteScopeKeepsDashboardTrafficInsideCandidate(t *testing.T) {
	report := dashboarddefinition.Definition{ID: "report", SemanticModel: "model", Pages: []dashboard.Page{{ID: "overview"}, {ID: "details"}}, Visualizations: map[string]visualizationdefinition.Definition{}}
	base := "/candidates/cand_1"
	var out strings.Builder
	if err := PageWithRouteScope(Presentation{ProductName: "LeapView", FaviconPath: "/static/favicon.svg"}, RouteScope{BasePath: base}, "client", "", dashboard.Catalog{Project: dashboard.CatalogProject{ID: "sales"}}, report, canonicalPageModel(), report.Pages, report.Pages[0], dashboard.Filters{}).Render(&out); err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	for _, want := range []string{base + "/updates?", base + "/dashboards/report/commands/filter", base + "/dashboards/report/commands/navigate"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"@get('/updates?", "/workspaces/sales/commands/", "/chats/turns", "/chats/restore"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("route leaked %q", forbidden)
		}
	}
}

func TestCanonicalPageExposesOneContextualAuthoringAction(t *testing.T) {
	report := canonicalPageDefinition(t)
	var out strings.Builder
	action := DashboardAuthoringAction{Label: "Continue editing", Href: "/dashboards/report/edit?draft=draft-7&page=showcase"}
	if err := PageWithRouteScopeAndAgentCommandsAndAuthoring(
		Presentation{ProductName: "LeapView"}, RouteScope{}, "client", "", dashboard.Catalog{}, report,
		canonicalPageModel(), report.Pages, report.Pages[0], dashboard.Filters{}, AgentCommandBindings{}, action,
	).Render(&out); err != nil {
		t.Fatal(err)
	}
	rendered := html.UnescapeString(out.String())
	for _, want := range []string{`authoring-action-label="Continue editing"`, `authoring-action-href="/dashboards/report/edit?draft=draft-7&page=showcase"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("missing %q in page shell: %s", want, rendered)
		}
	}
}
