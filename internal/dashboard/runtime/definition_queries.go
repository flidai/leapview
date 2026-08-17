package runtime

import (
	"context"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

// QueryDashboardPageForDefinition executes an already compiled dashboard
// definition against this service's existing model/data runtime. It is used by
// instance-managed dashboards whose definition does not live in the
// project catalog.
func (m *Service) QueryDashboardPageForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters) (dashboard.Patch, error) {
	view, err := m.definitionService(definition)
	if err != nil {
		return dashboard.EmptyPatch(filters.WithDefaults(), err), nil
	}
	return view.QueryDashboardPage(ctx, definition.ID, pageID, filters)
}

func (m *Service) QueryVisualizationForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters, visualID string) (visualizationir.VisualizationEnvelope, error) {
	view, err := m.definitionService(definition)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return view.QueryVisualization(ctx, definition.ID, pageID, filters, visualID)
}

func (m *Service) QueryVisualizationWindowForDefinition(ctx context.Context, definition dashboarddefinition.Definition, pageID string, filters dashboard.Filters, request visualizationir.VisualizationWindowRequest) (visualizationir.VisualizationEnvelope, error) {
	view, err := m.definitionService(definition)
	if err != nil {
		return visualizationir.VisualizationEnvelope{}, err
	}
	return view.QueryVisualizationWindow(ctx, definition.ID, pageID, filters, request)
}

func (m *Service) QueryCompiledFilterOptionsForDefinition(ctx context.Context, definition dashboarddefinition.Definition, query dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	view, err := m.definitionService(definition)
	if err != nil {
		return dashboardfilter.OptionResult{}, err
	}
	return view.QueryCompiledFilterOptions(ctx, definition.ID, query)
}

func (m *Service) NormalizeVisualizationWindowForDefinition(definition dashboarddefinition.Definition, request dashboard.TableRequest) dashboard.TableRequest {
	view, err := m.definitionService(definition)
	if err != nil {
		return request.WithDefaults()
	}
	return view.reports.NormalizeVisualizationWindow(definition.ID, request)
}

func (m *Service) DefaultFiltersForDefinition(definition dashboarddefinition.Definition) dashboard.Filters {
	view, err := m.definitionService(definition)
	if err != nil {
		return dashboard.Filters{}.WithDefaults()
	}
	return view.reports.DefaultFilters(definition.ID)
}

func (m *Service) PagesForDefinition(definition dashboarddefinition.Definition) []dashboard.Page {
	view, err := m.definitionService(definition)
	if err != nil {
		return nil
	}
	return view.reports.Pages(definition.ID)
}

func (m *Service) ModelIDForDashboardDefinition(definition dashboarddefinition.Definition) string {
	if _, err := m.definitionService(definition); err != nil {
		return ""
	}
	return definition.SemanticModel
}

// ExecuteConsumersPageForDefinition is the definition-based counterpart to
// ExecuteConsumersPage, preserving the same optimizer and model runtime.
func (m *Service) ExecuteConsumersPageForDefinition(ctx context.Context, definition dashboarddefinition.Definition, request consumer.Request, publish consumer.Publisher) error {
	view, err := m.definitionService(definition)
	if err != nil {
		return err
	}
	return view.ExecuteConsumersPage(ctx, request, publish)
}
