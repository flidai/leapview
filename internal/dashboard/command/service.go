package command

import (
	"fmt"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/dashboard/reportmodel"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type Metrics interface {
	report.Metrics
	NormalizeVisualizationWindow(dashboardID string, request dashboard.TableRequest) dashboard.TableRequest
}

func resolveDashboard(metrics Metrics, dashboardID string) (dashboardresolver.Resolved, bool) {
	if metrics == nil || metrics.Resolver() == nil {
		return dashboardresolver.Resolved{}, false
	}
	resolved, err := metrics.Resolver().Resolve(dashboardID)
	return resolved, err == nil
}

func canonicalSpatialInteractionCommand(metrics Metrics, dashboardID string, filters dashboard.Filters, command dashboard.SpatialSelectionCommand) (dashboard.SpatialSelectionCommand, error) {
	resolved, ok := resolveDashboard(metrics, dashboardID)
	if !ok || resolved.Model == nil {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("dashboard %q is not published", dashboardID)
	}
	definition, model := resolved.Definition, resolved.Model
	source, ok := definition.Visualizations[command.VisualID]
	if !ok {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("unknown source visual %q", command.VisualID)
	}
	if err := validateInteractionRevisions(filters, source, command.SpecRevision, command.DataRevision, command.ServingStateID, command.FilterRevision, command.InteractionRevision); err != nil {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("spatial visual %q: %w", command.VisualID, err)
	}
	if source.SpecRevision != command.SpecRevision {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("spatial visual %q specification revision is stale", command.VisualID)
	}
	spec, ok := source.Spec.Value.(*visualizationir.GeographicVisualizationSpec)
	if !ok {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("visual %q is not geographic", command.VisualID)
	}
	var interaction *visualizationir.VisualizationSpatialSelectionInteraction
	for index := range spec.SpatialInteractions {
		if spec.SpatialInteractions[index].ID == command.InteractionID {
			interaction = &spec.SpatialInteractions[index]
			break
		}
	}
	if interaction == nil {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("visual %q has no spatial interaction %q", command.VisualID, command.InteractionID)
	}
	if command.Action != "set" && command.Action != "clear" {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("unsupported spatial selection action %q", command.Action)
	}
	if command.Action == "clear" {
		command.Geometry = visualizationir.VisualizationSpatialSelectionGeometry{}
		return command, nil
	}
	allowed := false
	for _, gesture := range interaction.Gestures {
		if gesture == command.Gesture {
			allowed = true
			break
		}
	}
	if !allowed {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("visual %q does not allow %q spatial selection", command.VisualID, command.Gesture)
	}
	filter, err := semanticSpatialFilterForGeometry(command.Geometry)
	if err != nil {
		return dashboard.SpatialSelectionCommand{}, err
	}
	if filter.Kind != string(command.Gesture) {
		return dashboard.SpatialSelectionCommand{}, fmt.Errorf("spatial gesture %q does not match geometry %q", command.Gesture, filter.Kind)
	}
	if err := semanticquery.ValidateSpatialFilter(filter); err != nil {
		return dashboard.SpatialSelectionCommand{}, err
	}
	if _, err := reportmodel.ResolveCompiledSpatialSelectionInteraction(&definition, model, command.VisualID, command.InteractionID); err != nil {
		return dashboard.SpatialSelectionCommand{}, err
	}
	return command, nil
}

func semanticSpatialFilterForGeometry(geometry visualizationir.VisualizationSpatialSelectionGeometry) (semanticquery.SpatialFilter, error) {
	switch value := geometry.Value.(type) {
	case *visualizationir.VisualizationSpatialBoxSelection:
		if value == nil {
			break
		}
		return semanticquery.SpatialFilter{Kind: "box", West: value.Bounds.West, South: value.Bounds.South, East: value.Bounds.East, North: value.Bounds.North}, nil
	case *visualizationir.VisualizationSpatialLassoSelection:
		if value == nil {
			break
		}
		points := make([]semanticquery.SpatialPoint, len(value.Points))
		for index, point := range value.Points {
			points[index] = semanticquery.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
		}
		return semanticquery.SpatialFilter{Kind: "lasso", Points: points}, nil
	case *visualizationir.VisualizationSpatialRadiusSelection:
		if value == nil {
			break
		}
		return semanticquery.SpatialFilter{Kind: "radius", Center: semanticquery.SpatialPoint{Longitude: value.Center.Longitude, Latitude: value.Center.Latitude}, RadiusMeters: value.RadiusMeters}, nil
	}
	return semanticquery.SpatialFilter{}, fmt.Errorf("spatial selection geometry is required")
}

type Service struct {
	Metrics Metrics
}

type Request struct {
	DashboardID               string
	PageID                    string
	ModelID                   string
	VisualWindowCommand       dashboard.VisualizationWindowRequest
	InteractionCommand        dashboard.InteractionCommand
	SpatialInteractionCommand dashboard.SpatialSelectionCommand
}

func canonicalInteractionCommand(metrics Metrics, dashboardID string, filters dashboard.Filters, command dashboard.InteractionCommand) (dashboard.InteractionCommand, error) {
	resolved, ok := resolveDashboard(metrics, dashboardID)
	if !ok || resolved.Model == nil {
		return dashboard.InteractionCommand{}, fmt.Errorf("dashboard %q is not published", dashboardID)
	}
	definition, model := resolved.Definition, resolved.Model
	wantKind := "point_selection"
	var toggle bool
	semanticMappingCount := 0
	switch command.SourceKind {
	case "visual":
		source, ok := definition.Visualizations[command.SourceID]
		if !ok {
			return dashboard.InteractionCommand{}, fmt.Errorf("unknown source visual %q", command.SourceID)
		}
		if err := validateInteractionRevisions(filters, source, command.SpecRevision, command.DataRevision, command.ServingStateID, command.FilterRevision, command.InteractionRevision); err != nil {
			return dashboard.InteractionCommand{}, fmt.Errorf("source visual %q: %w", command.SourceID, err)
		}
		if isGridVisualization(source) {
			wantKind = "row_selection"
		}
		interaction, hasInteraction := compiledInteraction(source)
		if hasInteraction {
			toggle = interaction.Mode == visualizationir.VisualizationSelectionModeMultiple
			semanticMappingCount = len(interaction.Mappings)
		}
	default:
		return dashboard.InteractionCommand{}, fmt.Errorf("unknown source kind %q", command.SourceKind)
	}
	if command.InteractionKind != wantKind {
		return dashboard.InteractionCommand{}, fmt.Errorf("source %s %q requires interaction kind %q", command.SourceKind, command.SourceID, wantKind)
	}
	if command.Action != "set" && command.Action != "replace" && command.Action != "clear" {
		return dashboard.InteractionCommand{}, fmt.Errorf("unsupported selection action %q", command.Action)
	}
	command.Toggle = toggle
	if command.Action == "clear" {
		command.Mappings = nil
		return command, nil
	}
	if wantKind == "row_selection" && semanticMappingCount == 0 {
		if len(command.Mappings) != 1 || command.Mappings[0].Field != dashboard.UIRowSelectionField || command.Mappings[0].Fact != "" || command.Mappings[0].Grain != "" || !dashboard.IsInteractionSelectionScalar(command.Mappings[0].Value) {
			return dashboard.InteractionCommand{}, fmt.Errorf("table %q without semantic selection mappings accepts only the UI row key", command.SourceID)
		}
		return command, nil
	}
	if semanticMappingCount == 0 {
		return dashboard.InteractionCommand{}, fmt.Errorf("%s %q has no semantic selection mappings", command.SourceKind, command.SourceID)
	}
	interaction, err := reportmodel.ResolveCompiledSelectionInteraction(&definition, model, command.SourceKind, command.SourceID)
	if err != nil {
		return dashboard.InteractionCommand{}, err
	}
	identities := make([]reportmodel.SelectionMappingIdentity, len(command.Mappings))
	incoming := make(map[reportmodel.SelectionMappingIdentity]dashboard.InteractionCommandMapping, len(command.Mappings))
	for index, mapping := range command.Mappings {
		if !mapping.HasValue() {
			return dashboard.InteractionCommand{}, fmt.Errorf("mapping %d must include value", index)
		}
		if !dashboard.IsInteractionSelectionScalar(mapping.Value) {
			return dashboard.InteractionCommand{}, fmt.Errorf("mapping %d value must be a JSON scalar", index)
		}
		identity := reportmodel.SelectionMappingIdentity{Field: mapping.Field, Fact: mapping.Fact, Grain: mapping.Grain}
		identities[index] = identity
		incoming[identity] = mapping
	}
	canonical, err := interaction.CanonicalizeMappings(identities)
	if err != nil {
		return dashboard.InteractionCommand{}, err
	}
	command.Mappings = make([]dashboard.InteractionCommandMapping, 0, len(canonical))
	for _, mapping := range canonical {
		identity := reportmodel.SelectionMappingIdentity{Field: mapping.Field, Fact: mapping.Fact, Grain: mapping.Grain}
		value := incoming[identity]
		if !dashboard.InteractionSelectionValueMatchesType(value.Value, mapping.Type, mapping.Grain) {
			return dashboard.InteractionCommand{}, fmt.Errorf("mapping field %q value type %T does not match semantic type %q", mapping.Field, value.Value, mapping.Type)
		}
		command.Mappings = append(command.Mappings, value)
	}
	return command, nil
}

func validateInteractionRevisions(
	filters dashboard.Filters,
	source visualizationdefinition.Definition,
	specRevision string,
	dataRevision int64,
	servingStateID string,
	filterRevision int64,
	interactionRevision int64,
) error {
	if servingStateID == "" || servingStateID != filters.ServingStateID {
		return fmt.Errorf("serving state is stale")
	}
	if specRevision == "" || specRevision != source.SpecRevision {
		return fmt.Errorf("specification revision is stale")
	}
	if dataRevision <= 0 || filters.DataRevisions[source.ID] != dataRevision {
		return fmt.Errorf("data revision is stale")
	}
	currentFilterRevision := int64(0)
	if filters.CompiledState != nil {
		currentFilterRevision = int64(filters.CompiledState.Revision)
	}
	if filterRevision != currentFilterRevision {
		return fmt.Errorf("filter revision is stale")
	}
	if interactionRevision < 0 || uint64(interactionRevision) != filters.InteractionRevision {
		return fmt.Errorf("interaction revision is stale")
	}
	return nil
}

func isGridVisualization(definition visualizationdefinition.Definition) bool {
	return definition.Query.Kind == visualizationdefinition.QueryDetail || definition.Query.Kind == visualizationdefinition.QueryMatrix || definition.Query.Kind == visualizationdefinition.QueryPivot
}

func compiledInteraction(definition visualizationdefinition.Definition) (visualizationir.VisualizationInteraction, bool) {
	base, err := visualizationir.SpecificationBase(definition.Spec)
	if err != nil || len(base.Interactions) == 0 {
		return visualizationir.VisualizationInteraction{}, false
	}
	return base.Interactions[0], true
}
