package command

import (
	"fmt"
	"math"

	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	"github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type TargetKind = consumer.Kind

const (
	TargetVisual TargetKind = consumer.KindVisual
	TargetWindow TargetKind = consumer.KindWindow
)

type Target = consumer.Target

type RefreshPlan struct {
	Command string
	Targets []Target
}

type PreparedRefresh struct {
	Filters dashboard.Filters
	Plan    RefreshPlan
}

// PrepareFilterState attaches canonical compiled filter state and refreshes
// only active-page consumers targeted by the changed bindings.
func (s Service) PrepareFilterState(request Request, authoritative dashboard.Filters, state dashboardfilter.State, changedBindingKeys []string) (PreparedRefresh, error) {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return PreparedRefresh{}, fmt.Errorf("dashboard %q is not published", request.DashboardID)
	}
	page, ok := definition.PageOrDefault(request.PageID)
	if !ok || page.ID != request.PageID {
		return PreparedRefresh{}, fmt.Errorf("unknown dashboard page %q", request.PageID)
	}
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, authoritative)
	canonical := dashboardfilter.CloneState(state)
	filters.CompiledState = &canonical
	bindings := definition.CompiledFilterBindings()
	changed := make(map[string]struct{}, len(changedBindingKeys))
	for _, key := range changedBindingKeys {
		changed[key] = struct{}{}
	}
	componentIDs := map[string]struct{}{}
	for key, binding := range bindings {
		if len(changed) > 0 {
			if _, ok := changed[key]; !ok {
				continue
			}
		}
		prefix := page.ID + "/"
		for _, target := range binding.Targets {
			if len(target) > len(prefix) && target[:len(prefix)] == prefix {
				componentIDs[target[len(prefix):]] = struct{}{}
			}
		}
	}
	visualIDs := []string{}
	for _, component := range page.Visuals {
		if _, ok := componentIDs[component.ID]; ok && component.Visual != "" {
			visualIDs = append(visualIDs, component.Visual)
		}
	}
	targets, err := s.targetsForIDs(request, visualIDs)
	if err != nil {
		return PreparedRefresh{}, err
	}
	return PreparedRefresh{
		Filters: filters, Plan: RefreshPlan{Command: "filter_change", Targets: targets},
	}, nil
}

// PrepareSelect applies the command to coordinator-owned filters and plans
// only the interaction's explicit targets. A source is therefore queried only
// when it explicitly targets itself.
func (s Service) PrepareSelect(request Request, authoritative dashboard.Filters) (PreparedRefresh, error) {
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, authoritative)
	command, err := canonicalInteractionCommand(s.Metrics, request.DashboardID, filters, request.InteractionCommand)
	if err != nil {
		return PreparedRefresh{}, fmt.Errorf("invalid interaction selection: %w", err)
	}
	if err := s.requireVisualizationOnPage(request, command.SourceID); err != nil {
		return PreparedRefresh{}, fmt.Errorf("invalid interaction selection: %w", err)
	}
	filters = report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, filters.ApplyInteraction(command))
	targets, err := s.selectionTargets(request, command.SourceKind, command.SourceID)
	if err != nil {
		return PreparedRefresh{}, err
	}
	return PreparedRefresh{Filters: filters, Plan: RefreshPlan{Command: "select", Targets: targets}}, nil
}

func (s Service) PrepareSpatialSelect(request Request, authoritative dashboard.Filters) (PreparedRefresh, error) {
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, authoritative)
	command, err := canonicalSpatialInteractionCommand(s.Metrics, request.DashboardID, filters, request.SpatialInteractionCommand)
	if err != nil {
		return PreparedRefresh{}, fmt.Errorf("invalid spatial interaction selection: %w", err)
	}
	if err := s.requireVisualizationOnPage(request, command.VisualID); err != nil {
		return PreparedRefresh{}, fmt.Errorf("invalid spatial interaction selection: %w", err)
	}
	filters = report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, filters.ApplySpatialInteraction(command))
	targets, err := s.spatialSelectionTargets(request, command.VisualID, command.InteractionID)
	if err != nil {
		return PreparedRefresh{}, err
	}
	return PreparedRefresh{Filters: filters, Plan: RefreshPlan{Command: "spatial_select", Targets: targets}}, nil
}

// PrepareClearSelection queries the ordered union of targets affected by the
// selections being removed.
func (s Service) PrepareClearSelection(request Request, authoritative dashboard.Filters) (PreparedRefresh, error) {
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, authoritative)
	targets := []Target{}
	seen := map[string]struct{}{}
	for _, selection := range filters.Selections {
		selectionTargets, err := s.selectionTargets(request, selection.SourceKind, selection.SourceID)
		if err != nil {
			return PreparedRefresh{}, err
		}
		for _, target := range selectionTargets {
			key := target.Key()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	for _, selection := range filters.SpatialSelections {
		selectionTargets, err := s.spatialSelectionTargets(request, selection.VisualID, selection.InteractionID)
		if err != nil {
			return PreparedRefresh{}, err
		}
		for _, target := range selectionTargets {
			key := target.Key()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	if len(filters.Selections) > 0 || len(filters.SpatialSelections) > 0 {
		filters.InteractionRevision++
	}
	filters.Selections = []dashboard.InteractionSelection{}
	filters.SpatialSelections = []dashboard.SpatialInteractionSelection{}
	return PreparedRefresh{Filters: filters, Plan: RefreshPlan{Command: "clear_selection", Targets: targets}}, nil
}

func (s Service) PrepareInitial(request Request, initial dashboard.Filters) (PreparedRefresh, error) {
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, initial)
	return PreparedRefresh{Filters: filters, Plan: s.fullPlan(request, "initial")}, nil
}

func (s Service) PrepareVisualWindow(request Request, authoritative dashboard.Filters) (PreparedRefresh, error) {
	filters := report.NormalizeFilters(s.Metrics, request.DashboardID, request.PageID, authoritative)
	tableRequest, err := s.visualWindowTableRequest(request.DashboardID, request.VisualWindowCommand)
	if err != nil {
		return PreparedRefresh{}, err
	}
	return PreparedRefresh{
		Filters: filters,
		Plan: RefreshPlan{Command: "visual_window", Targets: []Target{{
			Kind:          TargetWindow,
			ID:            tableRequest.Table,
			WindowRequest: tableRequest,
		}}},
	}, nil
}

func (s Service) visualWindowTableRequest(dashboardID string, window dashboard.VisualizationWindowRequest) (dashboard.TableRequest, error) {
	if window.VisualID == "" {
		return dashboard.TableRequest{}, fmt.Errorf("visual window requires a visual ID")
	}
	definition, _, ok := s.Metrics.Report(dashboardID)
	if !ok {
		return dashboard.TableRequest{}, fmt.Errorf("dashboard %q is not published", dashboardID)
	}
	visual, ok := definition.Visualizations[window.VisualID]
	if !ok {
		return dashboard.TableRequest{}, fmt.Errorf("unknown windowed visual %q", window.VisualID)
	}
	if visual.Query.Kind != visualizationdefinition.QueryDetail && visual.Query.Kind != visualizationdefinition.QueryMatrix && visual.Query.Kind != visualizationdefinition.QueryPivot {
		return dashboard.TableRequest{}, fmt.Errorf("visual %q is not windowed", window.VisualID)
	}
	if window.SpecRevision != "" && window.SpecRevision != visual.SpecRevision {
		return dashboard.TableRequest{}, fmt.Errorf("visual %q specification revision is stale", window.VisualID)
	}
	if window.DataRevision < 0 || window.RequestSeq <= 0 || window.ResetVersion < 0 || window.Start < 0 || window.Start > math.MaxInt || window.Limit <= 0 || window.Limit > dashboard.TableMaxRequestCount {
		return dashboard.TableRequest{}, fmt.Errorf("invalid window coordinates for visual %q", window.VisualID)
	}
	if window.BlockID != "all" && window.BlockID != "a" && window.BlockID != "b" && window.BlockID != "c" {
		return dashboard.TableRequest{}, fmt.Errorf("invalid window block %q", window.BlockID)
	}
	if len(window.Sort) > 1 {
		return dashboard.TableRequest{}, fmt.Errorf("visual window supports exactly one active sort")
	}
	request := dashboard.TableRequest{
		Table: window.VisualID, Block: window.BlockID, Start: int(window.Start), Count: int(window.Limit),
		RequestSeq: int(window.RequestSeq), ResetVersion: int(window.ResetVersion),
	}
	if len(window.Sort) == 1 {
		sort := window.Sort[0]
		if sort.Field.Dataset == "" || sort.Field.Field == "" {
			return dashboard.TableRequest{}, fmt.Errorf("visual window sort field is required")
		}
		request.Sort.Key = sort.Field.Field
		switch sort.Direction {
		case visualizationir.VisualizationSortDirectionAscending:
			request.Sort.Direction = "asc"
		case visualizationir.VisualizationSortDirectionDescending:
			request.Sort.Direction = "desc"
		default:
			return dashboard.TableRequest{}, fmt.Errorf("unsupported visual window sort direction %q", sort.Direction)
		}
	}
	return s.Metrics.NormalizeVisualizationWindow(dashboardID, request), nil
}

func internalTableRequest(window dashboard.VisualizationWindowRequest) dashboard.TableRequest {
	request := dashboard.TableRequest{
		Table: window.VisualID, Block: window.BlockID, Start: int(window.Start), Count: int(window.Limit),
		RequestSeq: int(window.RequestSeq), ResetVersion: int(window.ResetVersion),
	}
	if len(window.Sort) > 0 {
		request.Sort.Key = window.Sort[0].Field.Field
		if window.Sort[0].Direction == visualizationir.VisualizationSortDirectionDescending {
			request.Sort.Direction = "desc"
		} else {
			request.Sort.Direction = "asc"
		}
	}
	return request
}

func (s Service) fullPlan(request Request, commandName string) RefreshPlan {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return RefreshPlan{Command: commandName}
	}
	page, ok := definition.PageOrDefault(request.PageID)
	if !ok {
		return RefreshPlan{Command: commandName}
	}
	tableRequest := s.Metrics.NormalizeVisualizationWindow(request.DashboardID, internalTableRequest(request.VisualWindowCommand)).Reset()
	targets := make([]Target, 0, len(page.Visuals))
	seen := map[string]struct{}{}
	for _, item := range page.Visuals {
		var target Target
		switch {
		case item.Visual != "":
			if compiled, ok := definition.Visualizations[item.Visual]; ok {
				if isGridVisualization(compiled) {
					requestForTable := tableRequest
					requestForTable.Table = item.Visual
					target = Target{Kind: TargetWindow, ID: item.Visual, WindowRequest: requestForTable}
				} else {
					target = Target{Kind: TargetVisual, ID: item.Visual}
				}
			} else {
				target = Target{Kind: TargetVisual, ID: item.Visual}
			}
		default:
			continue
		}
		key := target.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return RefreshPlan{Command: commandName, Targets: targets}
}

func (s Service) selectionTargets(request Request, sourceKind, sourceID string) ([]Target, error) {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return nil, fmt.Errorf("dashboard %q is not published", request.DashboardID)
	}
	var ids []string
	switch sourceKind {
	case "visual":
		visual, ok := definition.Visualizations[sourceID]
		if !ok {
			return nil, fmt.Errorf("unknown source visual %q", sourceID)
		}
		if interaction, ok := compiledInteraction(visual); ok {
			ids = activeInteractionTargetIDs(interaction.Targets)
		}
	default:
		return nil, fmt.Errorf("unknown source kind %q", sourceKind)
	}
	pageIDs, err := visualizationIDsForPage(definition, request.PageID)
	if err != nil {
		return nil, err
	}
	tableRequest := s.Metrics.NormalizeVisualizationWindow(request.DashboardID, internalTableRequest(request.VisualWindowCommand)).Reset()
	targets := make([]Target, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, onPage := pageIDs[id]; !onPage {
			continue
		}
		var target Target
		targetDefinition, ok := definition.Visualizations[id]
		if !ok {
			return nil, fmt.Errorf("interaction references unknown target %q", id)
		}
		if isGridVisualization(targetDefinition) {
			requestForTable := tableRequest
			requestForTable.Table = id
			target = Target{Kind: TargetWindow, ID: id, WindowRequest: requestForTable}
		} else {
			target = Target{Kind: TargetVisual, ID: id}
		}
		key := target.Key()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

func (s Service) spatialSelectionTargets(request Request, sourceID, interactionID string) ([]Target, error) {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return nil, fmt.Errorf("dashboard %q is not published", request.DashboardID)
	}
	source, ok := definition.Visualizations[sourceID]
	if !ok {
		return nil, fmt.Errorf("unknown source visual %q", sourceID)
	}
	spec, ok := source.Spec.Value.(*visualizationir.GeographicVisualizationSpec)
	if !ok {
		return nil, fmt.Errorf("visual %q is not geographic", sourceID)
	}
	var ids []string
	for _, interaction := range spec.SpatialInteractions {
		if interaction.ID == interactionID {
			ids = activeInteractionTargetIDs(interaction.Targets)
			break
		}
	}
	if ids == nil {
		return nil, fmt.Errorf("visual %q has no spatial interaction %q", sourceID, interactionID)
	}
	return s.targetsForIDs(request, ids)
}

func activeInteractionTargetIDs(targets []visualizationir.VisualizationInteractionTarget) []string {
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Effect != visualizationir.VisualizationInteractionEffectNone {
			ids = append(ids, target.VisualID)
		}
	}
	return ids
}

func (s Service) targetsForIDs(request Request, ids []string) ([]Target, error) {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return nil, fmt.Errorf("dashboard %q is not published", request.DashboardID)
	}
	pageIDs, err := visualizationIDsForPage(definition, request.PageID)
	if err != nil {
		return nil, err
	}
	tableRequest := s.Metrics.NormalizeVisualizationWindow(request.DashboardID, internalTableRequest(request.VisualWindowCommand)).Reset()
	targets := make([]Target, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, onPage := pageIDs[id]; !onPage {
			continue
		}
		targetDefinition, ok := definition.Visualizations[id]
		if !ok {
			return nil, fmt.Errorf("interaction references unknown target %q", id)
		}
		var target Target
		if isGridVisualization(targetDefinition) {
			requestForTable := tableRequest
			requestForTable.Table = id
			target = Target{Kind: TargetWindow, ID: id, WindowRequest: requestForTable}
		} else {
			target = Target{Kind: TargetVisual, ID: id}
		}
		if _, duplicate := seen[target.Key()]; duplicate {
			continue
		}
		seen[target.Key()] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

func (s Service) requireVisualizationOnPage(request Request, visualID string) error {
	definition, _, ok := s.Metrics.Report(request.DashboardID)
	if !ok {
		return fmt.Errorf("dashboard %q is not published", request.DashboardID)
	}
	ids, err := visualizationIDsForPage(definition, request.PageID)
	if err != nil {
		return err
	}
	if _, ok := ids[visualID]; !ok {
		return fmt.Errorf("source visual %q is not on page %q", visualID, request.PageID)
	}
	return nil
}

func visualizationIDsForPage(definition dashboarddefinition.Definition, pageID string) (map[string]struct{}, error) {
	page, ok := definition.PageOrDefault(pageID)
	if !ok || (pageID != "" && page.ID != pageID) {
		return nil, fmt.Errorf("unknown dashboard page %q", pageID)
	}
	ids := make(map[string]struct{}, len(page.Visuals))
	for _, placement := range page.Visuals {
		if placement.Visual != "" {
			ids[placement.Visual] = struct{}{}
		}
	}
	return ids, nil
}
