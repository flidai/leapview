package query

import (
	"fmt"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// PlanResultCount counts the complete grouped result, preserving its semantic
// population and authorization projection while excluding transport pagination.
func (p *Planner) PlanResultCount(request Request) (Plan, error) {
	request.Limit, request.Offset, request.Sort = 0, 0, nil
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return Plan{}, err
	}
	if err := p.validateAggregateFilters(request.Filters, resolved); err != nil {
		return Plan{}, err
	}
	graph, err := p.buildAggregatePlanIR(request, resolved)
	if err != nil {
		return Plan{}, err
	}
	// Ordering is a transport concern; avoid sorting the full result to count it.
	if output, ok := graph.Nodes[graph.Output].(planir.SortLimit); ok {
		output.Sort = nil
		graph.Nodes[graph.Output] = output
	}
	meta := planir.NodeMeta{NodeID: "result_count", RootDatasets: graph.RootDatasets, FilterPhase: planir.FilterPhasePostAggregate, AvailableFields: []planir.Field{{Name: "value", Type: "integer"}}}
	graph.Nodes[meta.NodeID] = planir.TotalRows{NodeMeta: meta, Input: graph.Output, TotalField: "value", CountOnly: true}
	graph.Output, graph.NodeMeta = meta.NodeID, meta
	if _, err := p.securePlanGraph(graph, aggregateMemberRefs(p, request, resolved)...); err != nil {
		return Plan{}, err
	}
	rendered, err := planir.RenderDuckDB(graph)
	if err != nil {
		return Plan{}, fmt.Errorf("render result count: %w", err)
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Deterministic: true, IR: graph}, nil
}
