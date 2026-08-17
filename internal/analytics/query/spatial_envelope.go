package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func spatialEnvelopeMeta(graph *planir.Graph, columns []string, id string) planir.NodeMeta {
	meta := planir.NodeMeta{NodeID: id, FilterPhase: planir.FilterPhasePostAggregate}
	if graph != nil {
		meta.RootDatasets = append(meta.RootDatasets, graph.RootDatasets...)
		meta.RelationshipRoutes = append(meta.RelationshipRoutes, graph.Nodes[graph.Output].Meta().RelationshipRoutes...)
	}
	for _, column := range columns {
		meta.AvailableFields = append(meta.AvailableFields, planir.Field{Name: column})
	}
	return meta
}

func renderSpatialEnvelopePlan(graph *planir.Graph, envelope planir.SpatialEnvelope, mode string) (Plan, error) {
	if graph == nil {
		return Plan{}, fmt.Errorf("spatial envelope graph is nil")
	}
	graph.Nodes[envelope.NodeID] = envelope
	graph.Output = envelope.NodeID
	graph.NodeMeta = envelope.NodeMeta
	if err := graph.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate spatial envelope plan IR: %w", err)
	}
	rendered, err := planir.RenderDuckDB(graph)
	if err != nil {
		return Plan{}, fmt.Errorf("render spatial envelope plan IR: %w", err)
	}
	deps, err := graph.Dependencies()
	if err != nil {
		return Plan{}, fmt.Errorf("derive spatial envelope dependencies: %w", err)
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Mode: mode, Datasets: deps.Datasets, PhysicalDependencies: deps.PhysicalFields, RelationshipPaths: deps.RelationshipPaths, IR: graph}, nil
}

func renderAnalyticalEnvelopePlan(graph *planir.Graph, envelope planir.AnalyticalEnvelope, mode string) (Plan, error) {
	if graph == nil {
		return Plan{}, fmt.Errorf("analytical envelope graph is nil")
	}
	graph.Nodes[envelope.NodeID] = envelope
	graph.Output = envelope.NodeID
	graph.NodeMeta = envelope.NodeMeta
	if err := graph.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate analytical envelope plan IR: %w", err)
	}
	rendered, err := planir.RenderDuckDB(graph)
	if err != nil {
		return Plan{}, fmt.Errorf("render analytical envelope plan IR: %w", err)
	}
	deps, err := graph.Dependencies()
	if err != nil {
		return Plan{}, fmt.Errorf("derive analytical envelope dependencies: %w", err)
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Mode: mode, Datasets: deps.Datasets, PhysicalDependencies: deps.PhysicalFields, RelationshipPaths: deps.RelationshipPaths, IR: graph}, nil
}
