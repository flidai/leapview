package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

const (
	SpatialCardinalityColumn    = "__spatial_cardinality"
	SpatialRawMinimumZoomColumn = "__spatial_raw_minimum_zoom"
)

// PlanSpatialMetadata returns one governed row for a tiled visual revision:
// valid-coordinate extent, exact coordinate-grain cardinality, coordinate-
// grain metric domains, and independently evaluated whole-filter totals.
func (p *Planner) PlanSpatialMetadata(request SpatialMetadataRequest) (Plan, error) {
	if request.Latitude.Field == "" || request.Longitude.Field == "" {
		return Plan{}, fmt.Errorf("spatial metadata requires coordinate fields")
	}
	if request.FeatureCap <= 0 || request.RawMinimumZoom < SpatialTileMinimumZoom || request.MaximumZoom < request.RawMinimumZoom || request.MaximumZoom > SpatialTileMaximumZoom {
		return Plan{}, fmt.Errorf("spatial metadata requires a valid feature cap and zoom range")
	}
	latitude, err := outputAlias(request.Latitude)
	if err != nil {
		return Plan{}, err
	}
	longitude, err := outputAlias(request.Longitude)
	if err != nil {
		return Plan{}, err
	}
	filters := append([]Filter(nil), request.Filters...)
	filters = append(filters,
		Filter{Field: request.Latitude.Field, Operator: "greater_than_or_equal", Values: []any{-mercatorMaximumLatitude}},
		Filter{Field: request.Latitude.Field, Operator: "less_than_or_equal", Values: []any{mercatorMaximumLatitude}},
		Filter{Field: request.Longitude.Field, Operator: "greater_than_or_equal", Values: []any{-180.0}},
		Filter{Field: request.Longitude.Field, Operator: "less_than_or_equal", Values: []any{180.0}},
	)
	coordinate, err := p.Plan(Request{
		Dataset: request.Dataset, Dimensions: []Field{request.Latitude, request.Longitude}, Metrics: request.Metrics,
		Filters: filters, ColumnMasks: request.ColumnMasks,
	})
	if err != nil {
		return Plan{}, err
	}
	if len(request.Metrics) > 0 {
		_, err = p.Plan(Request{Dataset: request.Dataset, Metrics: request.Metrics, Filters: filters, ColumnMasks: request.ColumnMasks})
		if err != nil {
			return Plan{}, err
		}
	}
	irGraph, err := p.spatialMetadataPlanIR(request, filters, coordinate)
	if err != nil {
		return Plan{}, err
	}
	columns := []string{"__spatial_west", "__spatial_south", "__spatial_east", "__spatial_north", SpatialCardinalityColumn}
	for _, metric := range request.Metrics {
		alias, err := outputAlias(metric)
		if err != nil {
			return Plan{}, err
		}
		columns = append(columns, "__spatial_raw_min_"+alias, "__spatial_raw_max_"+alias, "__spatial_total_"+alias)
	}
	columns = append(columns, SpatialRawMinimumZoomColumn)
	if irGraph == nil {
		return Plan{}, fmt.Errorf("spatial metadata plan IR is nil")
	}
	meta := spatialEnvelopeMeta(irGraph, columns, "spatial_metadata")
	coordinateInput, totalsInput := irGraph.Output, ""
	if len(request.Metrics) > 0 {
		bundle, ok := irGraph.Nodes[irGraph.Output].(planir.BundleBranches)
		if !ok {
			return Plan{}, fmt.Errorf("spatial metadata plan IR bundle is missing")
		}
		if len(bundle.Branches) != 2 {
			return Plan{}, fmt.Errorf("spatial metadata plan IR requires coordinate and totals branches")
		}
		coordinateInput, totalsInput = bundle.Branches[0].Input, bundle.Branches[1].Input
		delete(irGraph.Nodes, irGraph.Output)
	}
	envelope := planir.SpatialEnvelope{NodeMeta: meta, Operation: planir.SpatialEnvelopeMetadata, InputsList: []string{coordinateInput}, Latitude: latitude, Longitude: longitude, FeatureCap: request.FeatureCap, RawMinimumZoom: request.RawMinimumZoom, MaximumZoom: request.MaximumZoom}
	if totalsInput != "" {
		envelope.InputsList = append(envelope.InputsList, totalsInput)
	}
	for _, metric := range request.Metrics {
		alias, _ := outputAlias(metric)
		envelope.Metrics = append(envelope.Metrics, alias)
	}
	return renderSpatialEnvelopePlan(irGraph, envelope, "spatial_metadata")
}
