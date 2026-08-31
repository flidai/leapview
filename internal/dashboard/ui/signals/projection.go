package signals

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
)

const RouteDashboard RouteKind = "dashboard"
const dashboardAgentReferenceLimit int32 = 12

func DashboardInitialEnvelope(clientID, streamInstanceID string, catalog dashboard.Catalog, report dashboarddefinition.Definition, model *semanticmodel.Model, definitions map[string]visualizationdefinition.Definition, pages []dashboard.Page, activePage dashboard.Page, initialFilters dashboard.Filters) DashboardEnvelope {
	activePage = activePage.WithDefaults()
	appearance := dashboardAppearance(catalog, report.ID)
	tableRequest := DefaultTableRequest(report, activePage)
	initialFilters = report.NormalizeFiltersForPage(activePage.ID, initialFilters).WithDefaults()
	modelID, modelTitle := strings.TrimSpace(report.SemanticModel), ""
	if model != nil {
		if modelID == "" {
			modelID = model.Name
		}
		modelTitle = model.Title
	}
	filterState := report.DefaultFilterState()
	if initialFilters.CompiledState != nil {
		filterState = dashboardfilter.CloneState(*initialFilters.CompiledState)
	}
	return DashboardEnvelope{
		Agent: ChatSignal{
			Conversations: []ChatConversationSummary{},
			Transcript:    []ChatTranscriptItemSignal{},
			Status: ChatStatus{
				Enabled: false,
				Running: false,
				Error:   optionalValue("Agent is not configured"),
			},
			Composer: ComposerSignal{
				Disabled:    true,
				Placeholder: "Agent is not configured",
			},
		},
		AgentContext: AgentContextSignal{
			Surface:        "dashboard",
			DashboardID:    report.ID,
			DashboardTitle: report.Title,
			PageID:         activePage.ID,
			PageTitle:      activePage.Title,
			ModelID:        modelID,
			Filters:        DashboardFilterStateFromDomain(filterState),
			ReferenceLimit: dashboardAgentReferenceLimit,
			References:     []AgentReferenceSignal{},
		},
		AgentReferenceSearch: AgentReferenceSearchSignal{Results: []AgentReferenceSignal{}},
		AgentVisuals:         map[string]visualizationir.VisualizationEnvelope{},
		Page: DashboardPageSignal{
			AppearanceColor: appearance.Color,
			AppearanceIcon:  appearance.Icon,
			Kind:            RouteDashboard,
			Presentation:    "app",
			Title:           report.Title,
			Description:     optionalValue(report.Description),
			DashboardID:     report.ID,
			DashboardTitle:  report.Title,
			PageID:          activePage.ID,
			PageTitle:       activePage.Title,
			HeaderDetail:    ReportPageHeaderDetail(activePage),
			ModelID:         modelID,
			ModelTitle:      modelTitle,
			Canvas:          DashboardPageCanvasFromDashboard(activePage.Canvas),
			Grid:            DashboardPageGridFromDashboard(activePage.Grid),
			Pages:           dashboardPageNav(report.ID, pages, activePage),
			Components:      dashboardComponents(activePage),
		},
		Runtime: RouteRuntimeSignal{
			Kind:             RouteDashboard,
			ClientID:         optionalValue(clientID),
			StreamInstanceID: optionalValue(streamInstanceID),
			DashboardID:      optionalValue(report.ID),
			PageID:           optionalValue(activePage.ID),
			ModelID:          optionalValue(modelID),
			ServingStateID:   optionalValue(initialFilters.ServingStateID),
		},
		FilterContract:    DashboardFilterContractFromDefinition(report),
		FilterState:       DashboardFilterStateFromDomain(filterState),
		FilterOptionPages: map[string]DashboardFilterOptionPage{},
		FilterCommand: DashboardFilterCommand{Value: &DashboardFilterMutateCommand{
			DashboardFilterCommandBase: DashboardFilterCommandBase{
				Kind: "mutate", BaseRevision: int64(filterState.Revision),
			},
			Kind: "mutate", BindingKey: "", Operation: "clear",
		}},
		FilterOptionRequest: DashboardFilterOptionRequest{},
		FilterValidation: DashboardFilterValidationResult{
			Accepted: true, CurrentRevision: int64(filterState.Revision), ClientMutationID: "",
		},
		InteractionSelections: dashboardInteractionSelections(initialFilters.Selections),
		InteractionRevision:   int64(initialFilters.InteractionRevision),
		SpatialSelections:     dashboardSpatialSelections(initialFilters.SpatialSelections),
		NavigationCommand:     DashboardNavigationCommand{},
		URLParams:             report.URLParamsFromFiltersForPage(activePage.ID, initialFilters),
		InteractionCommand:    DashboardInteractionCommandFromDashboard(dashboard.InteractionCommand{Toggle: true, Mappings: []dashboard.InteractionCommandMapping{}}),
		VisualWindowCommand:   DashboardVisualWindowRequestFromDashboard(tableRequest),
		Visuals:               InitialVisualizationEnvelopes(definitions, activePage, tableRequest, initialFilters),
		Status:                DashboardStatusFromDashboard(dashboard.Status{}),
	}
}

func dashboardAppearance(catalog dashboard.Catalog, dashboardID string) dashboardappearance.Value {
	for _, candidate := range catalog.Dashboards {
		if candidate.ID.String() == dashboardID {
			return dashboardappearance.Resolve(candidate.Appearance)
		}
	}
	return dashboardappearance.Default()
}

func DefaultTableRequest(report dashboarddefinition.Definition, page dashboard.Page) dashboard.TableRequest {
	request := dashboard.TableRequest{Block: "all", Count: dashboard.TableChunkSize}
	for _, name := range pageVisualIDs(page) {
		table, ok := report.Visualizations[name]
		if !ok || table.Query.Kind != visualizationdefinition.QueryDetail {
			continue
		}
		request.Table = name
		if len(table.Query.Detail.DefaultSort) > 0 {
			request.Sort = dashboard.TableSort{
				Key:       table.Query.Detail.DefaultSort[0].FieldID,
				Direction: table.Query.Detail.DefaultSort[0].Direction,
			}
		}
		break
	}
	return request
}

func InitialVisualizationEnvelopes(definitions map[string]visualizationdefinition.Definition, page dashboard.Page, request dashboard.TableRequest, filters dashboard.Filters) map[string]DashboardVisualizationSignal {
	ids := pageVisualIDs(page)
	out := make(map[string]DashboardVisualizationSignal, len(ids))
	for _, id := range ids {
		definition, ok := definitions[id]
		if !ok {
			panic(fmt.Sprintf("compiled dashboard visualization %q is missing from initial signals", id))
		}
		dataRevision := int64(1)
		resetVersion := int64(0)
		if definition.Query.Kind == visualizationdefinition.QueryDetail || definition.Query.Kind == visualizationdefinition.QueryMatrix || definition.Query.Kind == visualizationdefinition.QueryPivot {
			resetVersion = int64(request.ResetVersion)
			dataRevision = int64(max(request.ResetVersion, 1))
		}
		envelope, err := visualizationruntime.EmptyEnvelopeFromDefinition(definition, dataRevision, 1, resetVersion)
		if err != nil {
			panic(fmt.Sprintf("compiled dashboard visualization %q has invalid initial envelope: %v", id, err))
		}
		signal := DashboardVisualizationSignalFromIR(envelope)
		signal.StreamGeneration = 1
		signal.ServingStateID = filters.ServingStateID
		if filters.CompiledState != nil {
			signal.FilterRevision = int64(filters.CompiledState.Revision)
		}
		signal.InteractionRevision = int64(filters.InteractionRevision)
		signal.ConsumerIdentity = page.ID + "/" + id
		out[id] = signal
	}
	return out
}

func ReportPageHeaderDetail(activePage dashboard.Page) string {
	return displayLabel(activePage.Title, activePage.ID)
}

func ValidateDashboardEnvelope(envelope DashboardEnvelope) error {
	if envelope.Page.Kind != RouteDashboard {
		return fmt.Errorf("dashboard envelope page kind = %q", envelope.Page.Kind)
	}
	if envelope.Runtime.Kind != RouteDashboard {
		return fmt.Errorf("dashboard envelope runtime kind = %q", envelope.Runtime.Kind)
	}
	if envelope.Page.DashboardID == "" || envelope.Page.PageID == "" {
		return fmt.Errorf("dashboard envelope requires dashboardId and pageId")
	}
	usedVisuals := map[string]struct{}{}
	for _, component := range envelope.Page.Components {
		switch {
		case component.Visual != nil && *component.Visual != "":
			usedVisuals[*component.Visual] = struct{}{}
			if _, ok := envelope.Visuals[*component.Visual]; !ok {
				return fmt.Errorf("component %q references missing visual %q", component.ID, *component.Visual)
			}
		case component.Binding != nil:
			var binding *DashboardCompiledFilterBinding
			for key, candidate := range envelope.FilterContract.Bindings {
				if candidate.Scope == component.Binding.Scope && candidate.ID == component.Binding.ID &&
					(candidate.Scope != "page" || ValueOrZero(candidate.PageID) == envelope.Page.PageID) {
					value := candidate
					value.Key = key
					binding = &value
					break
				}
			}
			if binding == nil {
				return fmt.Errorf("component %q references missing %s filter binding %q", component.ID, component.Binding.Scope, component.Binding.ID)
			}
			if _, ok := envelope.FilterState.AppliedControls[binding.Key]; !ok {
				return fmt.Errorf("component %q binding %q has no applied state", component.ID, binding.Key)
			}
		}
	}
	for id := range envelope.Visuals {
		if _, ok := usedVisuals[id]; !ok {
			return fmt.Errorf("unused visual payload %q", id)
		}
	}
	return nil
}

func dashboardPageNav(reportID string, pages []dashboard.Page, activePage dashboard.Page) []DashboardPageNavSignal {
	items := make([]DashboardPageNavSignal, 0, len(pages))
	for _, page := range pages {
		items = append(items, DashboardPageNavSignal{
			ID: page.ID, Title: page.Title,
			Href:   "/dashboards/" + reportID + "/pages/" + page.ID,
			Active: page.ID == activePage.ID,
		})
	}
	return items
}

func dashboardComponents(page dashboard.Page) []DashboardComponentSignal {
	components := make([]DashboardComponentSignal, 0, len(page.Visuals))
	for _, visual := range page.PlacedVisuals() {
		var binding *DashboardFilterBindingRef
		if visual.Binding.ID != "" {
			binding = &DashboardFilterBindingRef{Scope: string(visual.Binding.Scope), ID: visual.Binding.ID}
		}
		var presentation *DashboardFilterPresentation
		if visual.Kind == "slicer" {
			presentation = &DashboardFilterPresentation{
				Style: string(visual.Presentation.Style), Search: visual.Presentation.Search,
				SelectAll: visual.Presentation.SelectAll, ShowCounts: visual.Presentation.ShowCounts,
				ShowSummary: visual.Presentation.ShowSummary, Compact: visual.Presentation.Compact,
				Title: optionalValue(visual.Presentation.Title), Description: optionalValue(visual.Presentation.Description),
				AriaLabel: optionalValue(visual.Presentation.AriaLabel),
			}
		}
		components = append(components, DashboardComponentSignal{
			ID:           visual.ID,
			Kind:         visual.Kind,
			Visual:       optionalValue(visual.Visual),
			Binding:      binding,
			Presentation: presentation,
			Description:  optionalValue(visual.Description),
			Placement:    DashboardPagePlacementFromDashboard(visual.Placement),
			X:            visual.X,
			Y:            visual.Y,
			Width:        visual.Width,
			Height:       visual.Height,
			Eyebrow:      optionalValue(visual.Eyebrow),
			Title:        optionalValue(visual.Title),
			Subtitle:     optionalValue(visual.Subtitle),
			Badges:       optionalSlice(visual.Badges),
		})
	}
	return components
}

func pageVisualIDs(page dashboard.Page) []string {
	seen := map[string]struct{}{}
	ids := []string{}
	for _, item := range page.Visuals {
		if item.Visual == "" {
			continue
		}
		if _, ok := seen[item.Visual]; ok {
			continue
		}
		seen[item.Visual] = struct{}{}
		ids = append(ids, item.Visual)
	}
	sort.Strings(ids)
	return ids
}

func displayLabel(label, fallback string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return fallback
}
