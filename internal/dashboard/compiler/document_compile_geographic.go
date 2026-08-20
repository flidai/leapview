package compiler

import (
	"fmt"
	"math"
	"strings"

	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/dashboard/visualization/geometry"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func canonicalSpatialBinding(binding visualizationdefinition.QueryBinding, presentation *document.GeographicDashboardPresentation, authoredQuery document.DashboardQuery) (visualizationdefinition.QueryBinding, error) {
	if binding.Aggregate == nil {
		return binding, nil
	}
	spatial := &visualizationdefinition.SpatialQueryBinding{TableID: binding.Aggregate.TableID, Dimensions: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Dimensions...), Metrics: append([]visualizationdefinition.FieldBinding(nil), binding.Aggregate.Metrics...), Limit: binding.Aggregate.Limit, Sort: append([]visualizationdefinition.Sort(nil), binding.Aggregate.Sort...)}
	result := visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QuerySpatial, ResultShape: visualizationdefinition.ResultGeographicFeatures, ModelID: binding.ModelID, DatasetID: binding.DatasetID, Identity: append([]string(nil), binding.Identity...), Spatial: spatial}
	if presentation == nil || presentation.Layers == nil {
		return result, nil
	}
	latitudeAlias, longitudeAlias := "", ""
	hasTiled, hasInline := false, false
	cellRadius := 32.0
	for index, layer := range *presentation.Layers {
		switch value := layer.Value.(type) {
		case *document.DashboardPointGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Size != nil && value.Size.MaximumRadius != nil {
					cellRadius = math.Max(cellRadius, *value.Size.MaximumRadius)
				}
				if value.Cluster != nil && value.Cluster.Radius != nil {
					cellRadius = math.Max(cellRadius, float64(*value.Cluster.Radius))
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardHeatGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Heat != nil && value.Heat.Radius != nil {
					cellRadius = math.Max(cellRadius, *value.Heat.Radius)
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardDensityGeographicLayer:
			hasTiled = true
			if value != nil {
				if value.Heat != nil && value.Heat.Radius != nil {
					cellRadius = math.Max(cellRadius, *value.Heat.Radius)
				}
				if err := mergeTiledCoordinates(&latitudeAlias, &longitudeAlias, value.Latitude, value.Longitude); err != nil {
					return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d: %w", index, err)
				}
			}
		case *document.DashboardChoroplethGeographicLayer, *document.DashboardPathGeographicLayer:
			hasInline = true
		case *document.DashboardReferenceGeographicLayer:
		default:
			return visualizationdefinition.QueryBinding{}, fmt.Errorf("layer %d has unsupported variant %T", index, layer.Value)
		}
	}
	if hasTiled && hasInline {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("cannot mix tiled point, heat, or density layers with inline choropleth or path layers")
	}
	if !hasTiled {
		return result, nil
	}
	if aggregate, ok := authoredQuery.Value.(*document.AggregateDashboardQuery); ok && aggregate.Limit != nil {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled geographic visual must not set query.limit; tile budgets govern transport")
	}
	latitude, latitudeOK := fieldBindingByAlias(spatial.Dimensions, latitudeAlias)
	longitude, longitudeOK := fieldBindingByAlias(spatial.Dimensions, longitudeAlias)
	if !latitudeOK || !longitudeOK {
		return visualizationdefinition.QueryBinding{}, fmt.Errorf("tiled coordinates %q and %q must reference compiled dimension aliases", latitudeAlias, longitudeAlias)
	}
	spatial.Limit = 0
	spatial.Tiles = &visualizationdefinition.SpatialTileBinding{
		Latitude: latitude, Longitude: longitude,
		MinimumZoom: 0, MaximumZoom: 18, RawMinimumZoom: 5,
		FeatureCap: 5000, MaximumBytes: 512 * 1024, MetatileSize: 4,
		CellRadius: int32(math.Round(math.Max(32, math.Min(64, cellRadius)))),
	}
	return result, nil
}

func mergeTiledCoordinates(latitude, longitude *string, nextLatitude, nextLongitude string) error {
	if strings.TrimSpace(nextLatitude) == "" || strings.TrimSpace(nextLongitude) == "" {
		return fmt.Errorf("tiled layer requires latitude and longitude")
	}
	if *latitude == "" && *longitude == "" {
		*latitude, *longitude = nextLatitude, nextLongitude
		return nil
	}
	if *latitude != nextLatitude || *longitude != nextLongitude {
		return fmt.Errorf("tiled coordinate layers must share one latitude and longitude pair")
	}
	return nil
}

func fieldBindingByAlias(fields []visualizationdefinition.FieldBinding, alias string) (visualizationdefinition.FieldBinding, bool) {
	for _, field := range fields {
		if field.Alias == alias {
			return field, true
		}
	}
	return visualizationdefinition.FieldBinding{}, false
}

func canonicalGeographicLayers(value *document.GeographicDashboardPresentation, query LoweredDashboardQuery) ([]visualizationir.VisualizationGeographicLayer, error) {
	if value == nil || value.Layers == nil {
		return nil, nil
	}
	result := make([]visualizationir.VisualizationGeographicLayer, 0, len(*value.Layers))
	for index, authored := range *value.Layers {
		if authored.Value == nil {
			return nil, fmt.Errorf("map layer %d is required", index)
		}
		var layer visualizationir.VisualizationGeographicLayer
		var err error
		switch variant := authored.Value.(type) {
		case *document.DashboardPointGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalPointGeographicLayer(variant, query)
		case *document.DashboardChoroplethGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalChoroplethGeographicLayer(variant, query)
		case *document.DashboardReferenceGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalReferenceGeographicLayer(variant, query)
		case *document.DashboardHeatGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalHeatGeographicLayer(variant, query)
		case *document.DashboardDensityGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalDensityGeographicLayer(variant, query)
		case *document.DashboardPathGeographicLayer:
			if variant == nil {
				return nil, fmt.Errorf("map layer %d variant is nil", index)
			}
			layer, err = canonicalPathGeographicLayer(variant, query)
		default:
			return nil, fmt.Errorf("map layer %d uses unsupported variant %T", index, authored.Value)
		}
		if err != nil {
			return nil, fmt.Errorf("map layer %d: %w", index, err)
		}
		result = append(result, layer)
	}
	return result, nil
}

func canonicalMapLayerBase(base *document.DashboardGeographicLayerBase, kind string, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayerBase, error) {
	if base == nil {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer base is required")
	}
	id := strings.TrimSpace(base.ID)
	if id == "" {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer id is required")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer %q kind is required", id)
	}
	out := visualizationir.VisualizationGeographicLayerBase{
		ID:         id,
		Kind:       kind,
		Tooltip:    []visualizationir.VisualizationFieldRef{},
		Position:   visualizationir.VisualizationMapLayerPositionBelowLabels,
		Visibility: visualizationir.VisualizationMapVisibility{MinimumZoom: 0, MaximumZoom: 24},
	}
	if base.Label != nil {
		label, err := canonicalResultRef(query, "primary", *base.Label)
		if err != nil {
			return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("label: %w", err)
		}
		out.Label = &label
	}
	if base.Tooltip != nil {
		out.Tooltip = make([]visualizationir.VisualizationFieldRef, 0, len(*base.Tooltip))
		for _, name := range *base.Tooltip {
			ref, err := canonicalResultRef(query, "primary", name)
			if err != nil {
				return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("tooltip %q: %w", name, err)
			}
			out.Tooltip = append(out.Tooltip, ref)
		}
	}
	if base.Position != nil {
		out.Position = *base.Position
	}
	if base.MinimumZoom != nil {
		out.Visibility.MinimumZoom = *base.MinimumZoom
	}
	if base.MaximumZoom != nil {
		out.Visibility.MaximumZoom = *base.MaximumZoom
	}
	if out.Visibility.MinimumZoom < 0 || out.Visibility.MaximumZoom <= out.Visibility.MinimumZoom {
		return visualizationir.VisualizationGeographicLayerBase{}, fmt.Errorf("map layer %q has invalid visibility", id)
	}
	return out, nil
}

func canonicalMapOptionalRef(query LoweredDashboardQuery, name *string, field string) (*visualizationir.VisualizationFieldRef, error) {
	if name == nil {
		return nil, nil
	}
	ref, err := canonicalResultRef(query, "primary", *name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", field, err)
	}
	return &ref, nil
}

func canonicalMapRequiredRef(query LoweredDashboardQuery, name, field string) (visualizationir.VisualizationFieldRef, error) {
	if strings.TrimSpace(name) == "" {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("%s is required", field)
	}
	ref, err := canonicalResultRef(query, "primary", name)
	if err != nil {
		return visualizationir.VisualizationFieldRef{}, fmt.Errorf("%s: %w", field, err)
	}
	return ref, nil
}

func canonicalMapColor(value *document.DashboardMapColorScale) visualizationir.VisualizationMapColorScale {
	out := visualizationir.VisualizationMapColorScale{Kind: visualizationir.VisualizationMapColorScaleKindSequential, Palette: "default"}
	if value == nil {
		return out
	}
	if value.Kind != nil {
		out.Kind = *value.Kind
	}
	if value.Palette != nil {
		out.Palette = *value.Palette
	}
	if value.Reverse != nil {
		out.Reverse = *value.Reverse
	}
	out.DomainMinimum = value.DomainMinimum
	out.DomainMidpoint = value.DomainMidpoint
	out.DomainMaximum = value.DomainMaximum
	if value.NullColor != nil {
		out.NullColor = *value.NullColor
	}
	return out
}

func canonicalMapStroke(value *document.DashboardMapStroke) (visualizationir.VisualizationMapStroke, error) {
	out := visualizationir.VisualizationMapStroke{Color: "#ffffff", Width: 1, Opacity: .8}
	if value == nil {
		return out, nil
	}
	if value.Color != nil {
		out.Color = *value.Color
	}
	if value.Width != nil {
		out.Width = *value.Width
	}
	if value.Opacity != nil {
		out.Opacity = *value.Opacity
	}
	if out.Width < 0 {
		return visualizationir.VisualizationMapStroke{}, fmt.Errorf("stroke width must be non-negative")
	}
	if out.Opacity < 0 || out.Opacity > 1 {
		return visualizationir.VisualizationMapStroke{}, fmt.Errorf("stroke opacity must be between 0 and 1")
	}
	return out, nil
}

func canonicalMapSize(value *document.DashboardMapSizeScale) (visualizationir.VisualizationMapSizeScale, error) {
	out := visualizationir.VisualizationMapSizeScale{MinimumRadius: 2, MaximumRadius: 12}
	if value == nil {
		return out, nil
	}
	if value.MinimumRadius != nil {
		out.MinimumRadius = *value.MinimumRadius
	}
	if value.MaximumRadius != nil {
		out.MaximumRadius = *value.MaximumRadius
	}
	out.DomainMinimum = value.DomainMinimum
	out.DomainMaximum = value.DomainMaximum
	if out.MinimumRadius < 0 || out.MaximumRadius < out.MinimumRadius {
		return visualizationir.VisualizationMapSizeScale{}, fmt.Errorf("size scale has invalid radius range")
	}
	if out.DomainMinimum != nil && out.DomainMaximum != nil && *out.DomainMaximum < *out.DomainMinimum {
		return visualizationir.VisualizationMapSizeScale{}, fmt.Errorf("size scale has invalid domain range")
	}
	return out, nil
}

func canonicalMapHeat(value *document.DashboardMapHeatStyle) (visualizationir.VisualizationMapHeatStyle, error) {
	out := visualizationir.VisualizationMapHeatStyle{Radius: 24, Intensity: 1}
	if value == nil {
		return out, nil
	}
	if value.Radius != nil {
		out.Radius = *value.Radius
	}
	if value.Intensity != nil {
		out.Intensity = *value.Intensity
	}
	if out.Radius <= 0 || out.Intensity < 0 {
		return visualizationir.VisualizationMapHeatStyle{}, fmt.Errorf("heat style requires positive radius and non-negative intensity")
	}
	return out, nil
}

func canonicalMapLine(value *document.DashboardMapLineStyle) (visualizationir.VisualizationMapLineStyle, error) {
	out := visualizationir.VisualizationMapLineStyle{Width: 3}
	if value == nil {
		return out, nil
	}
	if value.Width != nil {
		out.Width = *value.Width
	}
	if value.Curvature != nil {
		out.Curvature = *value.Curvature
	}
	if out.Width < 0 || out.Curvature < 0 || out.Curvature > 1 {
		return visualizationir.VisualizationMapLineStyle{}, fmt.Errorf("line style has invalid width or curvature")
	}
	return out, nil
}

func canonicalMapCluster(value *document.DashboardMapCluster) (visualizationir.VisualizationMapCluster, error) {
	out := visualizationir.VisualizationMapCluster{Enabled: true, Radius: 40, MaximumZoom: 14, MinimumPoints: 2}
	if value == nil {
		return out, nil
	}
	if value.Enabled != nil {
		out.Enabled = *value.Enabled
	}
	if value.Radius != nil {
		out.Radius = *value.Radius
	}
	if value.MaximumZoom != nil {
		out.MaximumZoom = *value.MaximumZoom
	}
	if value.MinimumPoints != nil {
		out.MinimumPoints = *value.MinimumPoints
	}
	if value.ShowCount != nil {
		out.ShowCount = *value.ShowCount
	}
	if out.Radius <= 0 || out.MaximumZoom < 0 || out.MinimumPoints < 2 {
		return visualizationir.VisualizationMapCluster{}, fmt.Errorf("cluster configuration is invalid")
	}
	return out, nil
}

func canonicalMapOpacity(value *float64) (float64, error) {
	opacity := 1.0
	if value != nil {
		opacity = *value
	}
	if opacity < 0 || opacity > 1 {
		return 0, fmt.Errorf("opacity must be between 0 and 1")
	}
	return opacity, nil
}

func canonicalPointGeographicLayer(layer *document.DashboardPointGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, layer.Latitude, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, layer.Longitude, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	size, err := canonicalMapSize(layer.Size)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	cluster, err := canonicalMapCluster(layer.Cluster)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPointLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Latitude: latitude, Longitude: longitude, Value: value, Category: category, Size: size, Color: canonicalMapColor(layer.Color), Stroke: stroke, Cluster: cluster, Opacity: opacity}}, nil
}

func canonicalChoroplethGeographicLayer(layer *document.DashboardChoroplethGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	join, err := canonicalMapRequiredRef(query, layer.Join, "join")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	geometryAsset, err := geometry.Resolve(layer.GeometryAsset)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationChoroplethLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Geometry: geometryAsset, Join: join, Value: value, Category: category, Color: canonicalMapColor(layer.Color), Stroke: stroke, Opacity: opacity}}, nil
}

func canonicalReferenceGeographicLayer(layer *document.DashboardReferenceGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	geometryAsset, err := geometry.Resolve(layer.GeometryAsset)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationReferenceLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Geometry: geometryAsset, Color: canonicalMapColor(layer.Color), Stroke: stroke, Opacity: opacity}}, nil
}

func canonicalHeatGeographicLayer(layer *document.DashboardHeatGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	return canonicalHeatOrDensityGeographicLayer(layer.Kind, layer.Latitude, layer.Longitude, layer.Value, layer.Color, layer.Heat, layer.Opacity, query, true, &layer.DashboardGeographicLayerBase)
}

func canonicalDensityGeographicLayer(layer *document.DashboardDensityGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	return canonicalHeatOrDensityGeographicLayer(layer.Kind, layer.Latitude, layer.Longitude, layer.Value, layer.Color, layer.Heat, layer.Opacity, query, false, &layer.DashboardGeographicLayerBase)
}

func canonicalHeatOrDensityGeographicLayer(kind, latitudeName, longitudeName string, valueName *string, color *document.DashboardMapColorScale, heatStyle *document.DashboardMapHeatStyle, opacityValue *float64, query LoweredDashboardQuery, heat bool, baseValue *document.DashboardGeographicLayerBase) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(baseValue, kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, latitudeName, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, longitudeName, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, valueName, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	heatValue, err := canonicalMapHeat(heatStyle)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(opacityValue)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	if heat {
		return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationHeatLayer{VisualizationGeographicLayerBase: base, Kind: kind, Latitude: latitude, Longitude: longitude, Value: value, Color: canonicalMapColor(color), Heat: heatValue, Opacity: opacity}}, nil
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationDensityLayer{VisualizationGeographicLayerBase: base, Kind: kind, Latitude: latitude, Longitude: longitude, Value: value, Color: canonicalMapColor(color), Heat: heatValue, Opacity: opacity}}, nil
}

func canonicalPathGeographicLayer(layer *document.DashboardPathGeographicLayer, query LoweredDashboardQuery) (visualizationir.VisualizationGeographicLayer, error) {
	base, err := canonicalMapLayerBase(&layer.DashboardGeographicLayerBase, layer.Kind, query)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	latitude, err := canonicalMapRequiredRef(query, layer.Latitude, "latitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	longitude, err := canonicalMapRequiredRef(query, layer.Longitude, "longitude")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	path, err := canonicalMapRequiredRef(query, layer.Path, "path")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	order, err := canonicalMapRequiredRef(query, layer.Order, "order")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	value, err := canonicalMapOptionalRef(query, layer.Value, "value")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	category, err := canonicalMapOptionalRef(query, layer.Category, "category")
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	stroke, err := canonicalMapStroke(layer.Stroke)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	line, err := canonicalMapLine(layer.Line)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	opacity, err := canonicalMapOpacity(layer.Opacity)
	if err != nil {
		return visualizationir.VisualizationGeographicLayer{}, err
	}
	return visualizationir.VisualizationGeographicLayer{Value: &visualizationir.VisualizationPathLayer{VisualizationGeographicLayerBase: base, Kind: layer.Kind, Latitude: latitude, Longitude: longitude, Path: path, Order: order, Value: value, Category: category, Color: canonicalMapColor(layer.Color), Stroke: stroke, Line: line, Opacity: opacity}}, nil
}
