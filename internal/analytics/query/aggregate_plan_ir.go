package query

// This file lowers the already-resolved aggregate request into the closed
// PlanIR vocabulary.  SQL remains the execution contract for now; the graph
// is built from the same semantic resolution so it remains aligned with the
// governed SQL plan and compiled metric dependency DAG.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

func requestHasSpatialFilter(filters []Filter) bool {
	for _, filter := range filters {
		if filter.Spatial != nil {
			return true
		}
		for _, group := range filter.Groups {
			if requestHasSpatialFilter(group.Filters) {
				return true
			}
		}
	}
	return false
}

func (p *Planner) buildAggregatePlanIR(request Request, resolved aggregateResolution) (*planir.Graph, error) {
	if p == nil || p.model == nil || p.compiled == nil {
		return nil, fmt.Errorf("planner is not compiled")
	}
	graph := &planir.Graph{Nodes: map[string]planir.Node{}}
	dimensionNames := make([]string, 0, len(resolved.Dimensions))
	for _, dimension := range resolved.Dimensions {
		dimensionNames = append(dimensionNames, dimension.Name)
	}
	metricNames := sortedAggregateMetricNames(resolved.Aggregates)

	// Relationship metadata is collected per dataset before nodes are emitted.
	// A TraverseRelationship node carries one edge; the aggregate metadata
	// carries the complete set, including role-playing and composite paths.
	datasetRelationships := map[string]map[string]planir.RelationshipPath{}
	datasetRelationshipPaths := map[string]map[string][]planir.RelationshipPath{}
	for _, dataset := range resolved.Datasets {
		datasetRelationships[dataset] = map[string]planir.RelationshipPath{}
		datasetRelationshipPaths[dataset] = map[string][]planir.RelationshipPath{}
		for _, dimension := range resolved.Dimensions {
			_, path, err := p.aggregateDimensionBinding(dataset, dimension)
			if err != nil {
				return nil, err
			}
			addPlanIRRelationshipPath(datasetRelationships[dataset], datasetRelationshipPaths[dataset], path, dataset)
		}
		for _, metric := range resolved.Aggregates {
			if metric.Dataset != dataset {
				continue
			}
			physical, err := p.resolveDimension(metric.InputField)
			if err != nil {
				return nil, err
			}
			path, err := p.relationshipPath(dataset, physical.Table)
			if err != nil {
				return nil, err
			}
			addPlanIRRelationshipPath(datasetRelationships[dataset], datasetRelationshipPaths[dataset], path, dataset)
		}
		bindings, err := p.datasetFilterFields(request.Filters, resolved, dataset)
		if err != nil {
			return nil, err
		}
		for _, binding := range bindings {
			addPlanIRRelationshipPath(datasetRelationships[dataset], datasetRelationshipPaths[dataset], binding.Path, dataset)
		}
		for metricName, metric := range resolved.Aggregates {
			if metric.Dataset != dataset {
				continue
			}
			compiled, ok := p.compiled.metric(metricName)
			if !ok {
				continue
			}
			if compiled.Aggregate == nil {
				continue
			}
			for _, namedFilter := range compiled.Aggregate.NamedFilters {
				whereFilter := namedFilter.Filter
				whereBindings, err := p.datasetFilterFields([]Filter{whereFilter}, resolved, dataset)
				if err != nil {
					return nil, err
				}
				for _, binding := range whereBindings {
					addPlanIRRelationshipPath(datasetRelationships[dataset], datasetRelationshipPaths[dataset], binding.Path, dataset)
				}
			}
		}
	}

	branchIDs := make([]string, 0, len(resolved.Datasets))
	for datasetIndex, dataset := range resolved.Datasets {
		fields := p.planIRDatasetFields(dataset, resolved, request.Filters)
		if resolved.MultiDataset && len(dimensionNames) == 0 {
			fields = appendPlanIRField(fields, planir.Field{Name: "__scalar_key", Type: "integer"})
		}
		lineage := p.planIRDatasetLineage(dataset, resolved)
		relationshipPaths := sortedPlanIRRelationshipPaths(datasetRelationshipPaths[dataset])
		relationships := orderedPlanIRRelationships(datasetRelationships[dataset], relationshipPaths)
		routes := planIRRelationshipRoutes(dataset, relationshipPaths)
		for index := range relationships {
			fromRelation, relationErr := p.physicalTable(relationships[index].FromDataset)
			if relationErr != nil {
				return nil, fmt.Errorf("relationship %q source relation: %w", relationships[index].Name, relationErr)
			}
			toRelation, relationErr := p.physicalTable(relationships[index].ToDataset)
			if relationErr != nil {
				return nil, fmt.Errorf("relationship %q target relation: %w", relationships[index].Name, relationErr)
			}
			relationships[index].FromRelation = fromRelation
			relationships[index].ToRelation = toRelation
		}
		// Routes and edge metadata are both renderer inputs; keep their physical
		// endpoint relations identical so snapshot-qualified serving bindings do
		// not require semantic dataset re-resolution at execution time.
		for routeIndex := range routes {
			for edgeIndex := range routes[routeIndex].Edges {
				for _, relation := range relationships {
					if relation.Name == routes[routeIndex].Edges[edgeIndex].Name && relation.FromDataset == routes[routeIndex].Edges[edgeIndex].FromDataset && relation.ToDataset == routes[routeIndex].Edges[edgeIndex].ToDataset {
						routes[routeIndex].Edges[edgeIndex].FromRelation = relation.FromRelation
						routes[routeIndex].Edges[edgeIndex].ToRelation = relation.ToRelation
					}
				}
			}
		}
		grain := planir.Grain{}
		if table, ok := p.datasetTable(dataset); ok {
			grain.Fields = append([]string(nil), table.GrainFields()...)
		}
		scanID := fmt.Sprintf("scan_%d", datasetIndex)
		scanMeta := planIRMeta(scanID, grain, fields, nil, []string{dataset}, planir.FilterPhaseScan, planIRLineageForFields(lineage, fields), routes)
		relation, relationErr := p.physicalTable(dataset)
		if relationErr != nil {
			return nil, fmt.Errorf("dataset %q physical relation: %w", dataset, relationErr)
		}
		graph.Nodes[scanID] = planir.ScanDataset{NodeMeta: scanMeta, Dataset: dataset, Relation: relation}
		graph.Roots = append(graph.Roots, scanID)

		inputID := scanID
		datasetFilters := planIRDatasetFilters(request.Filters, dataset)
		rootFilters, relationshipFilters, err := p.splitPlanIRFilters(datasetFilters, resolved, dataset)
		if err != nil {
			return nil, fmt.Errorf("dataset %q filters: %w", dataset, err)
		}
		if len(rootFilters) > 0 {
			predicate, err := planIRAndPredicates(rootFilters)
			if err != nil {
				return nil, fmt.Errorf("dataset %q request filters: %w", dataset, err)
			}
			filterID := fmt.Sprintf("filter_request_scan_%d", datasetIndex)
			filterMeta := scanMeta
			filterMeta.NodeID = filterID
			filterMeta.PhysicalLineage = append(filterMeta.PhysicalLineage, p.planIRFilterLineage(dataset, rootFilters)...)
			graph.Nodes[filterID] = planir.FilterRows{NodeMeta: filterMeta, Input: inputID, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate)}
			inputID = filterID
		}
		for relationIndex, relation := range relationships {
			traverseID := fmt.Sprintf("traverse_%d_%d", datasetIndex, relationIndex)
			traverseMeta := scanMeta
			traverseMeta.NodeID = traverseID
			traverseMeta.FilterPhase = planir.FilterPhaseRelationship
			traverseMeta.RelationshipRoutes = routes
			graph.Nodes[traverseID] = planir.TraverseRelationship{NodeMeta: traverseMeta, Input: inputID, Path: relation}
			inputID = traverseID
		}

		if len(relationshipFilters) > 0 {
			predicate, err := planIRAndPredicates(relationshipFilters)
			if err != nil {
				return nil, fmt.Errorf("dataset %q relationship filters: %w", dataset, err)
			}
			filterID := fmt.Sprintf("filter_request_relationship_%d", datasetIndex)
			filterMeta := scanMeta
			filterMeta.NodeID = filterID
			filterMeta.FilterPhase = planir.FilterPhaseRelationship
			filterMeta.PhysicalLineage = append(filterMeta.PhysicalLineage, p.planIRFilterLineage(dataset, relationshipFilters)...)
			graph.Nodes[filterID] = planir.FilterRows{NodeMeta: filterMeta, Input: inputID, Predicate: predicate, Source: planir.FilterSourceRequest, Fields: predicateFields(predicate)}
			inputID = filterID
		}

		groupBy := append([]string(nil), dimensionNames...)
		// Scalar multi-dataset results have a single implicit, null-safe key. It is
		// explicit in PlanIR even though the existing SQL uses a CROSS JOIN.
		if resolved.MultiDataset && len(groupBy) == 0 {
			groupBy = []string{"__scalar_key"}
		}
		metrics, err := p.planIRAggregateMetrics(dataset, metricNames, resolved, fields)
		if err != nil {
			return nil, fmt.Errorf("dataset %q metrics: %w", dataset, err)
		}
		if len(metrics) == 0 {
			// Dimension-only branches still have a typed aggregate operation. The
			// synthetic row count is internal to the graph and does not alter SQL.
			input := firstPlanIRField(fields, dataset)
			metrics = []planir.MetricSpec{{Name: "__row_count", Type: "integer", Aggregation: "COUNT", Input: input}}
		}
		if request.SpatialBucket != nil {
			centerLongitudeType := p.planIRAverageType(request.SpatialBucket.Longitude.Field)
			centerLatitudeType := p.planIRAverageType(request.SpatialBucket.Latitude.Field)
			// Spatial tile envelopes consume these typed aggregate outputs at the
			// wrapper boundary. They are part of the governed aggregate graph, not
			// renderer-specific SQL aliases.
			metrics = append(metrics,
				planir.MetricSpec{Name: "__spatial_count", Type: "integer", Aggregation: "COUNT_STAR"},
				planir.MetricSpec{Name: "__spatial_coordinate_count", Type: "integer", Aggregation: "COUNT_DISTINCT_PAIR", Inputs: []string{request.SpatialBucket.Latitude.Field, request.SpatialBucket.Longitude.Field}},
				planir.MetricSpec{Name: "__spatial_center_longitude", Type: centerLongitudeType, Aggregation: "AVG", Input: request.SpatialBucket.Longitude.Field},
				planir.MetricSpec{Name: "__spatial_center_latitude", Type: centerLatitudeType, Aggregation: "AVG", Input: request.SpatialBucket.Latitude.Field},
				planir.MetricSpec{Name: "__spatial_west", Type: "decimal", Aggregation: "MIN", Input: request.SpatialBucket.Longitude.Field},
				planir.MetricSpec{Name: "__spatial_south", Type: "decimal", Aggregation: "MIN", Input: request.SpatialBucket.Latitude.Field},
				planir.MetricSpec{Name: "__spatial_east", Type: "decimal", Aggregation: "MAX", Input: request.SpatialBucket.Longitude.Field},
				planir.MetricSpec{Name: "__spatial_north", Type: "decimal", Aggregation: "MAX", Input: request.SpatialBucket.Latitude.Field},
			)
		}
		aggregateID := fmt.Sprintf("aggregate_%d", datasetIndex)
		aggregateFields := planIRGroupFields(fields, groupBy)
		aggregateMetrics := make([]planir.Metric, 0, len(metrics))
		for _, metric := range metrics {
			aggregateMetrics = append(aggregateMetrics, planir.Metric{Name: metric.Name, Type: metric.Type})
		}
		aggregateLineage := planIRLineageForFields(lineage, aggregateFields)
		aggregateMeta := planIRMeta(aggregateID, planir.Grain{Fields: groupBy}, aggregateFields, aggregateMetrics, []string{dataset}, planir.FilterPhaseAggregate, aggregateLineage, routes)
		timeBuckets := make([]planir.TimeBucket, 0, len(resolved.Dimensions))
		for _, dimension := range resolved.Dimensions {
			if dimension.Grain == "" {
				continue
			}
			timeBuckets = append(timeBuckets, planir.TimeBucket{Field: dimension.Name, Grain: dimension.Grain, Timezone: dimension.Timezone, WeekStart: dimension.WeekStart, DateTimeTZ: dimension.Datatype == semanticmodel.DataTypeDateTimeTZ})
		}
		var spatial *planir.SpatialBucket
		if request.SpatialBucket != nil {
			spatial = &planir.SpatialBucket{Latitude: request.SpatialBucket.Latitude.Field, Longitude: request.SpatialBucket.Longitude.Field, Zoom: request.SpatialBucket.Zoom, CellPixels: request.SpatialBucket.CellPixels}
		}
		graph.Nodes[aggregateID] = planir.AggregateMetrics{NodeMeta: aggregateMeta, Input: inputID, GroupBy: groupBy, TimeBuckets: timeBuckets, Spatial: spatial, Metrics: metrics}
		branchIDs = append(branchIDs, aggregateID)
	}

	currentID := branchIDs[0]
	currentMeta := graph.Nodes[currentID].Meta()
	if resolved.MultiDataset {
		keys := append([]string(nil), dimensionNames...)
		if len(keys) == 0 {
			keys = []string{"__scalar_key"}
		}
		stitchID := "stitch"
		availableMetrics := append([]planir.Metric(nil), currentMeta.AvailableMetrics...)
		for _, branchID := range branchIDs[1:] {
			availableMetrics = appendMissingPlanIRMetrics(availableMetrics, graph.Nodes[branchID].Meta().AvailableMetrics)
		}
		stitchMeta := currentMeta
		stitchMeta.NodeID = stitchID
		stitchMeta.OutputGrain = planir.Grain{Fields: keys}
		if len(keys) == 1 && keys[0] == "__scalar_key" {
			stitchMeta.AvailableFields = appendPlanIRField(stitchMeta.AvailableFields, planir.Field{Name: "__scalar_key", Type: "integer"})
		}
		stitchMeta.AvailableMetrics = availableMetrics
		stitchMeta.RootDatasets = append([]string(nil), resolved.Datasets...)
		graph.Nodes[stitchID] = planir.StitchAggregates{NodeMeta: stitchMeta, InputsList: branchIDs, Keys: keys}
		currentID, currentMeta = stitchID, stitchMeta
	}

	// Lower the compiled metric DAG after independent root aggregation and
	// stitching. This guarantees ratios and derived metrics cannot fan out.
	needed := map[string]bool{}
	var markNeeded func(string)
	markNeeded = func(name string) {
		if needed[name] {
			return
		}
		if _, ok := resolved.Metrics[name]; !ok {
			if node, exists := p.compiled.metric(name); !exists || node.Ratio == nil {
				return
			}
		}
		needed[name] = true
		if node, exists := p.compiled.metric(name); exists {
			for _, ref := range node.Dependencies {
				markNeeded(ref)
			}
		}
	}
	for _, member := range resolved.Members {
		if member.Kind == "metric" {
			markNeeded(member.Name)
		}
	}
	for _, name := range p.compiled.topologicalOrder {
		if !needed[name] {
			continue
		}
		node, ok := p.compiled.metric(name)
		if !ok {
			return nil, fmt.Errorf("metric %q is missing from compiled graph", name)
		}
		computedType := planIRComputedMetricType(node, currentMeta.AvailableMetrics)
		available := append([]planir.Metric(nil), currentMeta.AvailableMetrics...)
		available = appendMissingPlanIRMetrics(available, []planir.Metric{{Name: name, Type: computedType}})
		meta := currentMeta
		meta.NodeID = "compute_" + name
		meta.FilterPhase = planir.FilterPhasePostAggregate
		meta.AvailableMetrics = available
		if node.Ratio != nil {
			graph.Nodes[meta.NodeID] = planir.ComputeRatio{NodeMeta: meta, Input: currentID, Numerator: node.Ratio.Numerator, Denominator: node.Ratio.Denominator, Output: name}
		} else if node.Derived != nil {
			expression, err := planIRScalarExpression(node.Derived.Expression)
			if err != nil {
				return nil, fmt.Errorf("metric %q expression: %w", name, err)
			}
			graph.Nodes[meta.NodeID] = planir.ComputeDerived{NodeMeta: meta, Input: currentID, Output: name, Expression: expression}
		} else {
			return nil, fmt.Errorf("metric %q has no derived or ratio payload", name)
		}
		currentID, currentMeta = meta.NodeID, meta
	}

	// SortLimit is intentionally present even when no limit was requested: it
	// is the typed boundary at which caller ordering and pagination apply.
	finalMeta := currentMeta
	finalMeta.NodeID = "sort_limit"
	finalMeta.FilterPhase = planir.FilterPhasePostAggregate
	finalMeta.OutputGrain = currentMeta.OutputGrain
	finalMeta.AvailableFields = append([]planir.Field(nil), currentMeta.AvailableFields...)
	finalMeta.AvailableMetrics = append([]planir.Metric(nil), currentMeta.AvailableMetrics...)
	for index, dimension := range resolved.Dimensions {
		finalMeta.AvailableFields = appendPlanIRField(finalMeta.AvailableFields, planir.Field{Name: dimension.Alias, Type: dimension.Type})
		_ = index
	}
	for _, member := range resolved.Members {
		metricType := planIRMetricType(finalMeta.AvailableMetrics, member.Name)
		if metricType == "" {
			metricType = "decimal"
		}
		finalMeta.AvailableMetrics = appendMissingPlanIRMetrics(finalMeta.AvailableMetrics, []planir.Metric{{Name: member.Alias, Type: metricType}})
	}
	sortColumns := map[string]bool{}
	for _, dimension := range resolved.Dimensions {
		sortColumns[dimension.Alias] = true
	}
	for _, member := range resolved.Members {
		sortColumns[member.Alias] = true
	}
	sortKeys := make([]planir.SortKey, 0, len(request.Sort))
	for _, item := range effectiveOrderSorts(request.Sort, sortColumns) {
		field := item.Field
		// SQL ordering is allowed to use an authored output alias. The input
		// node exposes semantic names, so lower aliases to those names in the
		// typed operation while preserving the caller's ordering direction.
		if !planIRNameSet(currentMeta)[field] {
			for _, dimension := range resolved.Dimensions {
				if dimension.Alias == field {
					field = dimension.Name
					break
				}
			}
			for _, member := range resolved.Members {
				if member.Alias == item.Field {
					field = member.Name
					break
				}
			}
		}
		sortKeys = append(sortKeys, planir.SortKey{Field: field, Descending: strings.EqualFold(item.Direction, "desc")})
	}
	projection := make([]planir.Projection, 0, len(resolved.Dimensions)+len(resolved.Members))
	for _, dimension := range resolved.Dimensions {
		projection = append(projection, planir.Projection{Name: dimension.Alias, Source: dimension.Name})
	}
	for _, member := range resolved.Members {
		projection = append(projection, planir.Projection{Name: member.Alias, Source: member.Name})
	}
	if request.SpatialBucket != nil {
		for _, item := range []struct{ output, source string }{
			{output: "__lv_count", source: "__spatial_count"},
			{output: "__lv_coordinate_count", source: "__spatial_coordinate_count"},
			{output: "__lv_center_longitude", source: "__spatial_center_longitude"},
			{output: "__lv_center_latitude", source: "__spatial_center_latitude"},
			{output: "__lv_west", source: "__spatial_west"},
			{output: "__lv_south", source: "__spatial_south"},
			{output: "__lv_east", source: "__spatial_east"},
			{output: "__lv_north", source: "__spatial_north"},
		} {
			projection = append(projection, planir.Projection{Name: item.output, Source: item.source})
		}
	}
	graph.Nodes[finalMeta.NodeID] = planir.SortLimit{NodeMeta: finalMeta, Input: currentID, Sort: sortKeys, Projection: projection, Limit: request.Limit, Offset: request.Offset}
	graph.NodeMeta = finalMeta
	graph.Output = finalMeta.NodeID
	graph.Roots = append([]string(nil), graph.Roots...)
	return graph, nil
}

func (p *Planner) planIRAverageType(field string) string {
	physical, err := p.resolveDimension(field)
	if err == nil && physical.Datatype == semanticmodel.DataTypeFloat {
		return "float"
	}
	return "decimal"
}

func planIRMetricType(metrics []planir.Metric, name string) string {
	for _, metric := range metrics {
		if metric.Name == name {
			return strings.ToLower(strings.TrimSpace(metric.Type))
		}
	}
	return ""
}

func planIRComputedMetricType(metric CompiledMetric, available []planir.Metric) string {
	types := make(map[string]string, len(available))
	for _, item := range available {
		types[item.Name] = strings.ToLower(item.Type)
	}
	if metric.Ratio != nil {
		if isPlanIRFloatType(types[metric.Ratio.Numerator]) || isPlanIRFloatType(types[metric.Ratio.Denominator]) {
			return "float"
		}
		return "decimal"
	}
	if metric.Derived != nil {
		for _, reference := range metric.Derived.Expression.References() {
			if isPlanIRFloatType(types[reference]) {
				return "float"
			}
		}
	}
	return "decimal"
}

func isPlanIRFloatType(typ string) bool {
	typ = strings.ToLower(typ)
	return typ == "float" || typ == "double" || typ == "real"
}

func planIRMeta(id string, grain planir.Grain, fields []planir.Field, metrics []planir.Metric, roots []string, phase planir.FilterPhase, lineage []planir.PhysicalLineage, routes []planir.RelationshipRoute) planir.NodeMeta {
	return planir.NodeMeta{NodeID: id, OutputGrain: grain, AvailableFields: dedupePlanIRFields(fields), AvailableMetrics: dedupePlanIRMetrics(metrics), RootDatasets: append([]string(nil), roots...), FilterPhase: phase, PhysicalLineage: dedupePlanIRLineage(lineage), RelationshipRoutes: append([]planir.RelationshipRoute(nil), routes...)}
}

func (p *Planner) planIRDatasetFields(dataset string, resolved aggregateResolution, filters []Filter) []planir.Field {
	fields := []planir.Field{}
	if table, ok := p.datasetTable(dataset); ok {
		for name, dimension := range table.Dimensions {
			fields = appendPlanIRField(fields, planir.Field{Name: name, Type: dimension.Type})
			fields = appendPlanIRField(fields, planir.Field{Name: dataset + "." + name, Type: dimension.Type})
		}
	}
	for _, dimension := range resolved.Dimensions {
		fields = appendPlanIRField(fields, planir.Field{Name: dimension.Name, Type: dimension.Type})
		if physical, err := p.resolveDimension(dimension.Name); err == nil {
			fields = appendPlanIRField(fields, planir.Field{Name: physical.Field, Type: dimension.Type})
		}
	}
	for _, filter := range filters {
		appendPlanIRFilterFields(&fields, filter)
	}
	for _, metric := range resolved.Aggregates {
		if metric.Dataset != dataset {
			continue
		}
		if physical, err := p.resolveDimension(metric.InputField); err == nil {
			fields = appendPlanIRField(fields, planir.Field{Name: physical.Field, Type: physical.Type})
		}
		if compiled, ok := p.compiled.metric(metric.Name); ok && compiled.Aggregate != nil {
			for _, namedFilter := range compiled.Aggregate.NamedFilters {
				filter := namedFilter.Filter
				appendPlanIRFilterFields(&fields, filter)
			}
		}
	}
	return dedupePlanIRFields(fields)
}

func (p *Planner) planIRDatasetLineage(dataset string, resolved aggregateResolution) []planir.PhysicalLineage {
	lineage := []planir.PhysicalLineage{}
	for _, dimension := range resolved.Dimensions {
		field, path, err := p.aggregateDimensionBinding(dataset, dimension)
		if err != nil {
			continue
		}
		table, physical := splitPlanIRField(field, dataset)
		lineage = append(lineage, planir.PhysicalLineage{Logical: dimension.Name, Dataset: table, Field: physical, Route: planIRRouteNames(path)})
	}
	for name, metric := range resolved.Aggregates {
		if metric.Dataset != dataset {
			continue
		}
		if metric.InputField != "" {
			table, physical := splitPlanIRField(metric.InputField, dataset)
			// Pre-aggregate nodes expose physical input fields, not the metric
			// introduced by the AggregateMetrics node. Keeping the metric name in
			// scan lineage violates the typed invariant that every lineage logical
			// reference is available on its node; the metric itself is introduced
			// (and becomes available) only after aggregation.
			metricPath, _ := p.relationshipPath(dataset, table)
			lineage = append(lineage, planir.PhysicalLineage{Logical: metric.InputField, Dataset: table, Field: physical, Route: planIRRouteNames(metricPath)})
		}
		if compiled, ok := p.compiled.metric(name); ok {
			for _, entry := range compiled.Lineage.Entries {
				if entry.Role != "filter" || entry.Field == "" {
					continue
				}
				if resolvedField, err := p.resolveDimension(entry.Field); err == nil {
					path, _ := p.relationshipPath(dataset, resolvedField.Table)
					lineage = append(lineage, planir.PhysicalLineage{Logical: entry.Field, Dataset: resolvedField.Table, Field: resolvedField.Field, Route: planIRRouteNames(path)})
				}
			}
		}
	}
	return dedupePlanIRLineage(lineage)
}

func (p *Planner) planIRAggregateMetrics(dataset string, names []string, resolved aggregateResolution, fields []planir.Field) ([]planir.MetricSpec, error) {
	metrics := []planir.MetricSpec{}
	for _, name := range names {
		metric := resolved.Aggregates[name]
		if metric.Dataset != dataset {
			continue
		}
		aggregation := strings.ToUpper(metric.Aggregation)
		typ := "decimal"
		// Counts have an integer result regardless of input datatype (including
		// COUNT_DISTINCT over strings). AVG stays exact Decimal for integer or
		// Decimal inputs and is Float only when its input is explicitly Float.
		if aggregation == "COUNT" || aggregation == "COUNT_STAR" || aggregation == "COUNT_DISTINCT" || aggregation == "COUNT_DISTINCT_PAIR" {
			typ = "integer"
		} else if physical, err := p.resolveDimension(metric.InputField); err == nil {
			switch aggregation {
			case "AVG":
				if physical.Datatype == semanticmodel.DataTypeFloat {
					typ = "float"
				}
			case "SUM", "MIN", "MAX":
				switch physical.Datatype {
				case semanticmodel.DataTypeInteger:
					typ = "integer"
				case semanticmodel.DataTypeFloat:
					typ = "float"
				case semanticmodel.DataTypeDecimal:
					typ = "decimal"
				default:
					typ = physical.Type
				}
			}
		}
		if aggregation == "COUNT" || aggregation == "COUNT_DISTINCT" {
			typ = "integer"
		}
		input := metric.InputField
		if aggregation == "COUNT" && input == "" {
			input = firstPlanIRField(fields, dataset)
		}
		filters, err := p.planIRAggregateFilters(dataset, name, resolved)
		if err != nil {
			return nil, err
		}
		metrics = append(metrics, planir.MetricSpec{Name: name, Type: typ, Aggregation: aggregation, Input: input, Empty: metric.Empty, Filters: filters})
	}
	return metrics, nil
}

func (p *Planner) planIRAggregateFilters(dataset, metricName string, resolved aggregateResolution) ([]planir.AggregateFilter, error) {
	node, ok := p.compiled.metric(metricName)
	if !ok || node.Aggregate == nil || node.Aggregate.Dataset != dataset {
		return nil, nil
	}
	filters := make([]planir.AggregateFilter, 0, len(node.Aggregate.NamedFilters))
	for _, named := range node.Aggregate.NamedFilters {
		name := named.Name
		filter := named.Filter
		if name == "" {
			return nil, fmt.Errorf("metric %q named filter lineage is incomplete", metricName)
		}
		predicate, err := planIRPredicate(filter)
		if err != nil {
			return nil, fmt.Errorf("metric %q filter %q: %w", metricName, name, err)
		}
		phase, err := p.planIRFilterPhase(filter, resolved, dataset)
		if err != nil {
			return nil, fmt.Errorf("metric %q filter %q phase: %w", metricName, name, err)
		}
		routes := p.planIRNamedFilterRoutes(node, name, dataset)
		if phase == planir.FilterPhaseRelationship && len(routes) == 0 {
			return nil, fmt.Errorf("metric %q filter %q has no compiled relationship route", metricName, name)
		}
		fieldRoutes := map[string][]planir.RelationshipRoute{}
		var routeErr error
		var collectFieldRoutes func(Filter)
		collectFieldRoutes = func(item Filter) {
			if routeErr != nil {
				return
			}
			if item.Field != "" {
				if _, path, ok := p.planIRFilterPhysical(dataset, item.Field); ok && len(path) > 0 {
					edges := make([]planir.RelationshipPath, 0, len(path))
					for _, relation := range path {
						edge := planIRRelationshipPath(relation)
						fromRelation, err := p.physicalTable(edge.FromDataset)
						if err != nil {
							routeErr = err
							return
						}
						toRelation, err := p.physicalTable(edge.ToDataset)
						if err != nil {
							routeErr = err
							return
						}
						edge.FromRelation, edge.ToRelation = fromRelation, toRelation
						edges = append(edges, edge)
					}
					fieldRoutes[item.Field] = []planir.RelationshipRoute{{RootDataset: dataset, Edges: edges}}
				}
			}
			for _, group := range item.Groups {
				for _, child := range group.Filters {
					collectFieldRoutes(child)
				}
			}
		}
		collectFieldRoutes(filter)
		if routeErr != nil {
			return nil, fmt.Errorf("metric %q filter %q relation: %w", metricName, name, routeErr)
		}
		filters = append(filters, planir.AggregateFilter{Source: planir.FilterSourceNamed, Name: name, Predicate: predicate, Phase: phase, Fields: predicateFields(predicate), RelationshipRoutes: routes, MatchGuard: filter.MatchGuard || filter.RequireMatch, FieldRoutes: fieldRoutes})
	}
	return filters, nil
}

func (p *Planner) planIRNamedFilterRoutes(node CompiledMetric, name, dataset string) []planir.RelationshipRoute {
	byKey := map[string]planir.RelationshipRoute{}
	prefix := "filter:" + name
	for _, entry := range node.Lineage.Entries {
		if entry.Role != "filter" || !strings.HasPrefix(entry.Reference, prefix) || len(entry.Path) == 0 {
			continue
		}
		edges := make([]planir.RelationshipPath, 0, len(entry.Path))
		current := dataset
		for _, relationship := range entry.Path {
			edge := planIRRelationshipPath(relationship)
			if edge.FromDataset != current && edge.ToDataset == current && relationship.Cardinality == "one_to_one" {
				edge = reversePlanIRRelationshipPath(edge)
			}
			edges = append(edges, edge)
			current = edge.ToDataset
		}
		if len(edges) == 0 {
			continue
		}
		route := planir.RelationshipRoute{RootDataset: dataset, Edges: edges}
		byKey[planIRRelationshipSequenceSignature(edges)] = route
	}
	out := make([]planir.RelationshipRoute, 0, len(byKey))
	for _, route := range byKey {
		out = append(out, route)
	}
	sort.Slice(out, func(i, j int) bool {
		return planIRRelationshipSequenceSignature(out[i].Edges) < planIRRelationshipSequenceSignature(out[j].Edges)
	})
	return out
}

func (p *Planner) planIRFilterPhase(filter Filter, resolved aggregateResolution, dataset string) (planir.FilterPhase, error) {
	bindings, err := p.datasetFilterFields([]Filter{filter}, resolved, dataset)
	if err != nil {
		return planir.FilterPhaseUnspecified, err
	}
	for _, binding := range bindings {
		if len(binding.Path) > 0 {
			return planir.FilterPhaseRelationship, nil
		}
	}
	return planir.FilterPhaseScan, nil
}

func firstPlanIRField(fields []planir.Field, dataset string) string {
	for _, field := range fields {
		if strings.Contains(field.Name, ".") && strings.HasPrefix(field.Name, dataset+".") {
			return field.Name
		}
	}
	for _, field := range fields {
		if field.Name != "" {
			return field.Name
		}
	}
	return "__row"
}

func appendPlanIRFilterFields(fields *[]planir.Field, filter Filter) {
	if filter.Spatial != nil {
		*fields = appendPlanIRField(*fields, planir.Field{Name: filter.Spatial.LatitudeField, Type: "decimal"})
		*fields = appendPlanIRField(*fields, planir.Field{Name: filter.Spatial.LongitudeField, Type: "decimal"})
	}
	if filter.Field != "" {
		*fields = appendPlanIRField(*fields, planir.Field{Name: filter.Field, Type: "string"})
	}
	for _, group := range filter.Groups {
		for _, child := range group.Filters {
			appendPlanIRFilterFields(fields, child)
		}
	}
}

func appendPlanIRField(fields []planir.Field, field planir.Field) []planir.Field {
	for _, existing := range fields {
		if existing.Name == field.Name {
			return fields
		}
	}
	return append(fields, field)
}

func appendMissingPlanIRMetrics(dst, src []planir.Metric) []planir.Metric {
	for _, metric := range src {
		found := false
		for _, existing := range dst {
			if existing.Name == metric.Name {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, metric)
		}
	}
	return dst
}

func dedupePlanIRFields(fields []planir.Field) []planir.Field {
	out := []planir.Field{}
	for _, field := range fields {
		out = appendPlanIRField(out, field)
	}
	return out
}

func planIRGroupFields(fields []planir.Field, groupBy []string) []planir.Field {
	byName := make(map[string]planir.Field, len(fields))
	for _, field := range fields {
		if _, exists := byName[field.Name]; !exists {
			byName[field.Name] = field
		}
	}
	out := make([]planir.Field, 0, len(groupBy))
	for _, name := range groupBy {
		field, ok := byName[name]
		if !ok {
			field = planir.Field{Name: name}
		}
		out = append(out, field)
	}
	return out
}

func dedupePlanIRMetrics(metrics []planir.Metric) []planir.Metric {
	return appendMissingPlanIRMetrics(nil, metrics)
}

func dedupePlanIRLineage(lineage []planir.PhysicalLineage) []planir.PhysicalLineage {
	out := []planir.PhysicalLineage{}
	for _, item := range lineage {
		found := false
		for _, existing := range out {
			if existing.Logical == item.Logical && existing.Dataset == item.Dataset && existing.Field == item.Field && sameStringSlice(existing.Route, item.Route) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, item)
		}
	}
	return out
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func planIRRouteNames(path []semanticmodel.Relationship) []string {
	if len(path) == 0 {
		return nil
	}
	out := make([]string, len(path))
	for index, relation := range path {
		out[index] = relation.ID
	}
	return out
}

func planIRLineageForFields(lineage []planir.PhysicalLineage, fields []planir.Field) []planir.PhysicalLineage {
	available := map[string]bool{}
	for _, field := range fields {
		available[field.Name] = true
	}
	out := []planir.PhysicalLineage{}
	for _, item := range lineage {
		// Metric lineage is keyed by the semantic metric name while scan
		// projections expose the physical input field. Match both forms so
		// Dependencies() retains every metric input without a second resolver.
		// Match the logical lineage key (or its qualified form) only. Matching
		// the bare physical field name can alias a metric input such as
		// orders.state with a conformed semantic dimension named state. The
		// aggregate node exposes the semantic group field, not the raw input
		// field, so retaining that lineage makes PlanIR metadata self-invalid.
		if available[item.Logical] || available[item.Dataset+"."+item.Field] {
			out = append(out, item)
		}
	}
	return out
}

func planIRFieldNameSet(meta planir.NodeMeta) map[string]bool {
	set := map[string]bool{}
	for _, field := range meta.AvailableFields {
		set[field.Name] = true
	}
	return set
}

func planIRNameSet(meta planir.NodeMeta) map[string]bool {
	set := planIRFieldNameSet(meta)
	for _, metric := range meta.AvailableMetrics {
		set[metric.Name] = true
	}
	return set
}

func splitPlanIRField(field, fallback string) (string, string) {
	parts := strings.SplitN(field, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return fallback, field
}

func addPlanIRRelationshipPath(dst map[string]planir.RelationshipPath, paths map[string][]planir.RelationshipPath, path []semanticmodel.Relationship, rootDataset string) {
	if len(path) == 0 {
		return
	}
	converted := make([]planir.RelationshipPath, 0, len(path))
	currentDataset := rootDataset
	for _, relationship := range path {
		convertedRelationship := planIRRelationshipPath(relationship)
		if convertedRelationship.FromDataset != currentDataset && convertedRelationship.ToDataset == currentDataset && relationship.Cardinality == "one_to_one" {
			convertedRelationship = reversePlanIRRelationshipPath(convertedRelationship)
		}
		addPlanIRRelationship(dst, convertedRelationship)
		converted = append(converted, convertedRelationship)
		currentDataset = convertedRelationship.ToDataset
	}
	paths[planIRRelationshipSequenceSignature(converted)] = converted
}

func addPlanIRRelationship(dst map[string]planir.RelationshipPath, relationship planir.RelationshipPath) {
	key := relationship.Name + "\x00" + relationship.FromDataset + "\x00" + relationship.ToDataset
	dst[key] = relationship
}

func reversePlanIRRelationshipPath(path planir.RelationshipPath) planir.RelationshipPath {
	keys := make([]planir.JoinKey, len(path.JoinKeys))
	for index, key := range path.JoinKeys {
		keys[index] = planir.JoinKey{From: key.To, To: key.From}
	}
	return planir.RelationshipPath{Name: path.Name, FromDataset: path.ToDataset, ToDataset: path.FromDataset, JoinKeys: keys}
}

func planIRRelationshipPath(relationship semanticmodel.Relationship) planir.RelationshipPath {
	joinKeys := make([]planir.JoinKey, 0, len(relationship.FromFields))
	for index, from := range relationship.FromFields {
		if index < len(relationship.ToFields) {
			joinKeys = append(joinKeys, planir.JoinKey{From: from, To: relationship.ToFields[index]})
		}
	}
	return planir.RelationshipPath{Name: relationship.ID, FromDataset: relationship.FromDataset, ToDataset: relationship.ToDataset, JoinKeys: joinKeys}
}

func planIRRelationshipSequenceSignature(path []planir.RelationshipPath) string {
	parts := make([]string, len(path))
	for index, relationship := range path {
		parts[index] = relationship.Name + ":" + relationship.FromDataset + ">" + relationship.ToDataset
	}
	return strings.Join(parts, "/")
}

func sortedPlanIRRelationshipPaths(paths map[string][]planir.RelationshipPath) [][]planir.RelationshipPath {
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([][]planir.RelationshipPath, 0, len(keys))
	for _, key := range keys {
		out = append(out, append([]planir.RelationshipPath(nil), paths[key]...))
	}
	return out
}

func planIRRelationshipRoutes(root string, paths [][]planir.RelationshipPath) []planir.RelationshipRoute {
	routes := make([]planir.RelationshipRoute, 0, len(paths))
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		routes = append(routes, planir.RelationshipRoute{RootDataset: root, Edges: append([]planir.RelationshipPath(nil), path...)})
	}
	sort.Slice(routes, func(i, j int) bool {
		left, right := routes[i], routes[j]
		if left.RootDataset != right.RootDataset {
			return left.RootDataset < right.RootDataset
		}
		return planIRRelationshipSequenceSignature(left.Edges) < planIRRelationshipSequenceSignature(right.Edges)
	})
	return routes
}

func sortedPlanIRRelationships(values map[string]planir.RelationshipPath) []planir.RelationshipPath {
	out := make([]planir.RelationshipPath, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].FromDataset+out[i].ToDataset < out[j].FromDataset+out[j].ToDataset
	})
	return out
}

// orderedPlanIRRelationships topologically orders all required edges. A
// sibling edge may follow a prior edge because the accumulated root rowset
// still contains the root dataset; a child edge is emitted only after its
// explicit path parent.
func orderedPlanIRRelationships(values map[string]planir.RelationshipPath, paths [][]planir.RelationshipPath) []planir.RelationshipPath {
	ordered := sortedPlanIRRelationships(values)
	byKey := map[string]planir.RelationshipPath{}
	for _, value := range ordered {
		byKey[planIRRelationshipSequenceSignature([]planir.RelationshipPath{value})] = value
	}
	children := map[string]map[string]bool{}
	indegree := map[string]int{}
	for key := range byKey {
		indegree[key] = 0
	}
	for _, path := range paths {
		for index := 1; index < len(path); index++ {
			parent := planIRRelationshipSequenceSignature([]planir.RelationshipPath{path[index-1]})
			child := planIRRelationshipSequenceSignature([]planir.RelationshipPath{path[index]})
			if _, exists := children[parent]; !exists {
				children[parent] = map[string]bool{}
			}
			if !children[parent][child] {
				children[parent][child] = true
				indegree[child]++
			}
		}
	}
	ready := []string{}
	for key, degree := range indegree {
		if degree == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)
	result := make([]planir.RelationshipPath, 0, len(ordered))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		result = append(result, byKey[key])
		for child := range children[key] {
			indegree[child]--
			if indegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		sort.Strings(ready)
	}
	if len(result) != len(ordered) {
		return ordered
	}
	return result
}

func planIRDatasetFilters(filters []Filter, dataset string) []Filter {
	out := []Filter{}
	for _, filter := range filters {
		if planIRFilterApplies(filter, dataset) {
			out = append(out, filter)
		}
	}
	return out
}

func planIRFilterApplies(filter Filter, dataset string) bool {
	scope := filter.Dataset
	if filter.Spatial != nil && filter.Spatial.Dataset != "" {
		scope = filter.Spatial.Dataset
	}
	if scope != "" && scope != dataset {
		return false
	}
	for _, group := range filter.Groups {
		for _, child := range group.Filters {
			if planIRFilterApplies(child, dataset) {
				return true
			}
		}
	}
	return filter.Field != "" || len(filter.Groups) == 0
}

func (p *Planner) splitPlanIRFilters(filters []Filter, resolved aggregateResolution, dataset string) (root, relationship []Filter, err error) {
	for _, filter := range filters {
		phase, phaseErr := p.planIRFilterPhase(filter, resolved, dataset)
		if phaseErr != nil {
			return nil, nil, phaseErr
		}
		if phase == planir.FilterPhaseRelationship {
			relationship = append(relationship, filter)
		} else {
			root = append(root, filter)
		}
	}
	return root, relationship, nil
}

func (p *Planner) planIRFilterLineage(dataset string, filters []Filter) []planir.PhysicalLineage {
	_ = dataset
	lineage := []planir.PhysicalLineage{}
	var walk func(Filter)
	walk = func(filter Filter) {
		if filter.Field != "" {
			physical, path, ok := p.planIRFilterPhysical(dataset, filter.Field)
			if ok {
				lineage = append(lineage, planir.PhysicalLineage{Logical: filter.Field, Dataset: physical.Table, Field: physical.Field, Route: planIRRouteNames(path)})
			}
		}
		if filter.Spatial != nil {
			for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				if physical, path, ok := p.planIRFilterPhysical(dataset, field); ok {
					lineage = append(lineage, planir.PhysicalLineage{Logical: field, Dataset: physical.Table, Field: physical.Field, Route: planIRRouteNames(path)})
				}
			}
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				walk(child)
			}
		}
	}
	for _, filter := range filters {
		walk(filter)
	}
	return dedupePlanIRLineage(lineage)
}

func (p *Planner) planIRFilterPhysical(dataset, field string) (semanticmodel.MetricDimension, []semanticmodel.Relationship, bool) {
	if semanticDimension, ok := p.model.Dimensions[field]; ok {
		binding, bound := semanticDimension.Bindings[dataset]
		if !bound {
			return semanticmodel.MetricDimension{}, nil, false
		}
		physical, err := p.resolveDimension(binding.Field)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, false
		}
		path, err := p.resolveBindingPath(dataset, binding)
		if err != nil {
			return semanticmodel.MetricDimension{}, nil, false
		}
		return physical, path, true
	}
	physical, err := p.resolveDimension(field)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, false
	}
	path, err := p.relationshipPath(dataset, physical.Table)
	if err != nil {
		return semanticmodel.MetricDimension{}, nil, false
	}
	return physical, path, true
}

func planIRAndPredicates(filters []Filter) (planir.Predicate, error) {
	children := make([]planir.Predicate, 0, len(filters))
	for _, filter := range filters {
		predicate, err := planIRPredicate(filter)
		if err != nil {
			return planir.Predicate{}, err
		}
		children = append(children, predicate)
	}
	if len(children) == 1 {
		return children[0], nil
	}
	return planir.Predicate{Kind: planir.PredicateAnd, Children: children}, nil
}

func planIRPredicate(filter Filter) (planir.Predicate, error) {
	var predicate planir.Predicate
	if filter.Spatial != nil {
		if filter.Field != "" || len(filter.Groups) != 0 {
			return planir.Predicate{}, fmt.Errorf("spatial filter cannot combine scalar or grouped filter fields")
		}
		points := make([]planir.SpatialPoint, len(filter.Spatial.Points))
		for index, point := range filter.Spatial.Points {
			points[index] = planir.SpatialPoint{Longitude: point.Longitude, Latitude: point.Latitude}
		}
		predicate = planir.Predicate{Kind: planir.PredicateSpatial, Spatial: &planir.SpatialPredicate{
			Kind: filter.Spatial.Kind, Latitude: filter.Spatial.LatitudeField, Longitude: filter.Spatial.LongitudeField,
			West: filter.Spatial.West, South: filter.Spatial.South, East: filter.Spatial.East, North: filter.Spatial.North,
			Points: points, Center: planir.SpatialPoint{Longitude: filter.Spatial.Center.Longitude, Latitude: filter.Spatial.Center.Latitude}, RadiusMeters: filter.Spatial.RadiusMeters,
		}}
	} else if len(filter.Groups) > 0 {
		groups := make([]planir.Predicate, 0, len(filter.Groups))
		for _, group := range filter.Groups {
			groupPredicate, err := planIRAndPredicates(group.Filters)
			if err != nil {
				return planir.Predicate{}, err
			}
			groups = append(groups, groupPredicate)
		}
		if len(groups) == 1 {
			predicate = groups[0]
		} else {
			predicate = planir.Predicate{Kind: planir.PredicateOr, Children: groups}
		}
	} else {
		if filter.Field == "" {
			return planir.Predicate{}, fmt.Errorf("filter field is required")
		}
		operator := strings.ToLower(filter.Operator)
		switch operator {
		case "", "equals":
			if len(filter.Values) != 1 {
				return planir.Predicate{}, fmt.Errorf("equals filter requires one value")
			}
			predicate = planir.Predicate{Kind: planir.PredicateCompare, Field: filter.Field, Operator: "=", Value: planIRLiteral(filter.Values[0])}
		case "not_equals":
			if len(filter.Values) != 1 {
				return planir.Predicate{}, fmt.Errorf("not_equals filter requires one value")
			}
			predicate = planir.Predicate{Kind: planir.PredicateCompare, Field: filter.Field, Operator: "<>", Value: planIRLiteral(filter.Values[0])}
		case "greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal":
			if len(filter.Values) != 1 {
				return planir.Predicate{}, fmt.Errorf("comparison filter requires one value")
			}
			operatorMap := map[string]string{"greater_than": ">", "greater_than_or_equal": ">=", "less_than": "<", "less_than_or_equal": "<="}
			predicate = planir.Predicate{Kind: planir.PredicateCompare, Field: filter.Field, Operator: operatorMap[operator], Value: planIRLiteral(filter.Values[0])}
		case "in", "not_in":
			values := make([]planir.Literal, 0, len(filter.Values))
			for _, value := range filter.Values {
				values = append(values, planIRLiteral(value))
			}
			predicate = planir.Predicate{Kind: planir.PredicateIn, Field: filter.Field, Values: values}
			if operator == "not_in" {
				predicate = planir.Predicate{Kind: planir.PredicateNot, Children: []planir.Predicate{predicate}}
			}
		case "is_null", "is_not_null":
			predicate = planir.Predicate{Kind: planir.PredicateIsNull, Field: filter.Field, Negated: operator == "is_not_null"}
		case "contains", "not_contains", "starts_with", "ends_with":
			if len(filter.Values) != 1 {
				return planir.Predicate{}, fmt.Errorf("text filter requires one value")
			}
			value := fmt.Sprint(filter.Values[0])
			if operator == "contains" || operator == "not_contains" {
				value = "%" + value + "%"
			} else if operator == "starts_with" {
				value += "%"
			} else {
				value = "%" + value
			}
			predicate = planir.Predicate{Kind: planir.PredicateCompare, Field: filter.Field, Operator: "ILIKE", Value: planir.Literal{Kind: planir.LiteralString, String: value}}
			if operator == "not_contains" {
				predicate = planir.Predicate{Kind: planir.PredicateNot, Children: []planir.Predicate{predicate}}
			}
		default:
			return planir.Predicate{}, fmt.Errorf("unsupported filter operator %q", filter.Operator)
		}
	}
	if filter.Not {
		predicate = planir.Predicate{Kind: planir.PredicateNot, Children: []planir.Predicate{predicate}}
	}
	return predicate, nil
}

func planIRLiteral(value any) planir.Literal {
	switch value := value.(type) {
	case nil:
		return planir.Literal{Kind: planir.LiteralNull}
	case string:
		return planir.Literal{Kind: planir.LiteralString, String: value}
	case bool:
		return planir.Literal{Kind: planir.LiteralBool, Bool: value}
	case json.Number:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberDecimal, NumberText: string(value)}
	case int:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatInt(int64(value), 10)}
	case int8:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatInt(int64(value), 10)}
	case int16:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatInt(int64(value), 10)}
	case int32:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatInt(int64(value), 10)}
	case int64:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatInt(value, 10)}
	case uint:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatUint(uint64(value), 10)}
	case uint64:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: strconv.FormatUint(value, 10)}
	case float32:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberFloat, NumberText: strconv.FormatFloat(float64(value), 'g', -1, 32)}
	case float64:
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberFloat, NumberText: strconv.FormatFloat(value, 'g', -1, 64)}
	default:
		return planir.Literal{Kind: planir.LiteralString, String: fmt.Sprint(value)}
	}
}

func predicateFields(predicate planir.Predicate) []string {
	seen := map[string]bool{}
	var walk func(planir.Predicate)
	walk = func(item planir.Predicate) {
		if item.Field != "" {
			seen[item.Field] = true
		}
		for _, child := range item.Children {
			walk(child)
		}
	}
	walk(predicate)
	out := make([]string, 0, len(seen))
	for field := range seen {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func planIRScalarExpression(expression semanticmodel.Expression) (planir.ScalarExpr, error) {
	tree, err := expression.Tree()
	if err != nil {
		return planir.ScalarExpr{}, err
	}
	var convert func(semanticmodel.ScalarExpressionNode) (planir.ScalarExpr, error)
	convert = func(node semanticmodel.ScalarExpressionNode) (planir.ScalarExpr, error) {
		switch node.Kind {
		case semanticmodel.ScalarExpressionMetric:
			return planir.ScalarExpr{Kind: planir.ScalarMetricRef, Metric: node.Metric}, nil
		case semanticmodel.ScalarExpressionNumber:
			numberKind := planir.NumberInteger
			if strings.Contains(node.Number, ".") {
				numberKind = planir.NumberDecimal
			}
			if strings.ContainsAny(node.Number, "eE") {
				return planir.ScalarExpr{}, fmt.Errorf("exponent notation is not supported in authored numeric literals")
			}
			return planir.ScalarExpr{Kind: planir.ScalarLiteral, Literal: planir.Literal{Kind: planir.LiteralNumber, NumberKind: numberKind, NumberText: node.Number}}, nil
		case semanticmodel.ScalarExpressionUnary:
			if len(node.Children) != 1 {
				return planir.ScalarExpr{}, fmt.Errorf("unary expression requires one child")
			}
			child, err := convert(node.Children[0])
			if err != nil {
				return planir.ScalarExpr{}, err
			}
			kind := planir.ScalarNeg
			if node.Operator == "+" {
				kind = planir.ScalarPos
			} else if node.Operator != "-" {
				return planir.ScalarExpr{}, fmt.Errorf("unsupported unary operator %q", node.Operator)
			}
			return planir.ScalarExpr{Kind: kind, Children: []planir.ScalarExpr{child}}, nil
		case semanticmodel.ScalarExpressionBinary:
			if len(node.Children) != 2 {
				return planir.ScalarExpr{}, fmt.Errorf("binary expression requires two children")
			}
			left, err := convert(node.Children[0])
			if err != nil {
				return planir.ScalarExpr{}, err
			}
			right, err := convert(node.Children[1])
			if err != nil {
				return planir.ScalarExpr{}, err
			}
			kinds := map[string]planir.ScalarKind{"+": planir.ScalarAdd, "-": planir.ScalarSub, "*": planir.ScalarMul, "/": planir.ScalarDiv}
			kind, ok := kinds[string(node.Operator)]
			if !ok {
				return planir.ScalarExpr{}, fmt.Errorf("unsupported binary operator %q", node.Operator)
			}
			return planir.ScalarExpr{Kind: kind, Children: []planir.ScalarExpr{left, right}}, nil
		case semanticmodel.ScalarExpressionCall:
			children := make([]planir.ScalarExpr, len(node.Children))
			for index, child := range node.Children {
				converted, err := convert(child)
				if err != nil {
					return planir.ScalarExpr{}, err
				}
				children[index] = converted
			}
			return planir.ScalarExpr{Kind: planir.ScalarFunction, Function: string(node.Function), Children: children}, nil
		default:
			return planir.ScalarExpr{}, fmt.Errorf("unsupported expression node kind %q", node.Kind)
		}
	}
	return convert(tree)
}
