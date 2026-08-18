package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func (p *Planner) spatialAggregatePlanIR(request SpatialTileRequest, filters []Filter) (*planir.Graph, error) {
	semantic := Request{
		Dataset:       request.Dataset,
		Dimensions:    []Field{{Field: request.Latitude.Field, Alias: request.Latitude.Alias}, {Field: request.Longitude.Field, Alias: request.Longitude.Alias}},
		Metrics:       request.Metrics,
		Filters:       filters,
		ColumnMasks:   request.ColumnMasks,
		SpatialBucket: &SpatialBucket{Latitude: request.Latitude, Longitude: request.Longitude, Zoom: request.Zoom, CellPixels: request.CellPixels},
	}
	resolved, err := p.resolveAggregate(semantic)
	if err != nil {
		return nil, err
	}
	graph, err := p.buildAggregatePlanIR(semantic, resolved)
	if err != nil {
		return nil, err
	}
	if err := graph.Validate(); err != nil {
		return nil, fmt.Errorf("validate spatial aggregate plan IR: %w", err)
	}
	return graph, nil
}

func (p *Planner) spatialMetadataPlanIR(request SpatialMetadataRequest, filters []Filter, coordinate Plan) (*planir.Graph, error) {
	coordinateRequest := Request{Dataset: request.Dataset, Dimensions: []Field{request.Latitude, request.Longitude}, Metrics: request.Metrics, Filters: filters, ColumnMasks: request.ColumnMasks}
	coordinateResolved, err := p.resolveAggregate(coordinateRequest)
	if err != nil {
		return nil, err
	}
	if len(request.Metrics) == 0 {
		return coordinate.IR, nil
	}
	totalsRequest := Request{Dataset: request.Dataset, Metrics: request.Metrics, Filters: filters, ColumnMasks: request.ColumnMasks}
	totalsResolved, err := p.resolveAggregate(totalsRequest)
	if err != nil {
		return nil, err
	}
	return p.buildBundlePlanIR(
		[]BundleRequest{{ID: "coordinate", Request: coordinateRequest}, {ID: "totals", Request: totalsRequest}},
		[]aggregateResolution{coordinateResolved, totalsResolved},
	)
}
