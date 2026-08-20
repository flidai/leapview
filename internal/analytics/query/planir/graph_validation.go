package planir

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

func (g *Graph) Validate() error {
	if g == nil {
		return fmt.Errorf("plan graph is nil")
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("plan graph has no nodes")
	}
	if g.Output == "" {
		return fmt.Errorf("plan graph output is required")
	}
	if _, ok := g.Nodes[g.Output]; !ok {
		return fmt.Errorf("plan graph output %q does not exist", g.Output)
	}
	if err := validateMeta(g.NodeMeta, "graph", false); err != nil {
		return err
	}

	ids := make([]string, 0, len(g.Nodes))
	for id, node := range g.Nodes {
		if id == "" {
			return fmt.Errorf("plan graph contains an empty node id")
		}
		if node == nil {
			return fmt.Errorf("node %q is nil", id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		node := g.Nodes[id]
		meta := node.Meta()
		if meta.NodeID != id {
			return fmt.Errorf("node %q has metadata id %q", id, meta.NodeID)
		}
		for _, input := range node.Inputs() {
			if input == "" {
				return fmt.Errorf("node %q: input id is empty", id)
			}
			if _, ok := g.Nodes[input]; !ok {
				return fmt.Errorf("node %q: input %q does not exist", id, input)
			}
		}
	}
	// Cycle detection is separate from input checks so the reported cycle is
	// stable and useful even when a node has several inputs.
	state := map[string]uint8{}
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("plan graph contains a cycle at node %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		inputs := append([]string(nil), g.Nodes[id].Inputs()...)
		sort.Strings(inputs)
		for _, input := range inputs {
			if err := visit(input); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	// Roots are an explicit description of the graph's source branches. Every
	// declared root must be a zero-input node, and every node must contribute to
	// the selected output; disconnected typed operations are not a valid plan.
	if len(g.Roots) == 0 {
		return fmt.Errorf("plan graph roots are required")
	}
	seenRoots := map[string]bool{}
	for _, root := range g.Roots {
		if seenRoots[root] {
			return fmt.Errorf("plan graph root %q is duplicated", root)
		}
		seenRoots[root] = true
		node, ok := g.Nodes[root]
		if !ok {
			return fmt.Errorf("plan graph root %q does not exist", root)
		}
		if len(node.Inputs()) != 0 {
			return fmt.Errorf("plan graph root %q has inputs", root)
		}
	}
	reachable := map[string]bool{}
	var markReachable func(string)
	markReachable = func(id string) {
		if reachable[id] {
			return
		}
		reachable[id] = true
		for _, input := range g.Nodes[id].Inputs() {
			markReachable(input)
		}
	}
	markReachable(g.Output)
	for _, id := range ids {
		if !reachable[id] {
			return fmt.Errorf("plan graph node %q is disconnected from output", id)
		}
	}
	for _, id := range ids {
		node := g.Nodes[id]
		meta := node.Meta()
		if err := validateMeta(meta, "node "+id, true); err != nil {
			return err
		}
		if err := validateNode(node, g.Nodes); err != nil {
			return fmt.Errorf("node %q: %w", id, err)
		}
		for _, input := range node.Inputs() {
			parent := g.Nodes[input].Meta()
			if parent.FilterPhase.rank() > meta.FilterPhase.rank() && parent.FilterPhase != FilterPhaseUnspecified && meta.FilterPhase != FilterPhaseUnspecified {
				return fmt.Errorf("node %q: filter phase %q moves backwards from input %q phase %q", id, meta.FilterPhase, input, parent.FilterPhase)
			}
		}
	}

	// A graph output is typed by the output node. Graph-level metadata is
	// optional for callers constructing plans incrementally, but when supplied
	// it must agree with the node it describes.
	outputMeta := g.Nodes[g.Output].Meta()
	if !g.OutputGrain.empty() && !g.OutputGrain.equal(outputMeta.OutputGrain) {
		return fmt.Errorf("graph output grain does not match output node %q", g.Output)
	}
	if len(g.AvailableFields) > 0 && !sameFields(g.AvailableFields, outputMeta.AvailableFields) {
		return fmt.Errorf("graph available fields do not match output node %q", g.Output)
	}
	if len(g.AvailableMetrics) > 0 && !sameMetrics(g.AvailableMetrics, outputMeta.AvailableMetrics) {
		return fmt.Errorf("graph available metrics do not match output node %q", g.Output)
	}
	return nil
}

func validateMeta(meta NodeMeta, where string, requireID bool) error {
	if !requireID {
		meta.NodeID = "graph"
	}
	return meta.validate(where)
}

func validateNode(node Node, nodes map[string]Node) error {
	// Pointers are convenient when callers build a graph incrementally; the
	// IR remains closed because all pointer forms still resolve to the eleven
	// concrete node types below.
	switch value := node.(type) {
	case *ScanDataset:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *TraverseRelationship:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *FilterRows:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *AggregateMetrics:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *StitchAggregates:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *ComputeRatio:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *ComputeDerived:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *SortLimit:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *TotalRows:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *BundleBranches:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *SpatialEnvelope:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	case *AnalyticalEnvelope:
		if value == nil {
			return fmt.Errorf("node is nil")
		}
		return validateNode(*value, nodes)
	}
	meta := node.Meta()
	inputMeta := func(id string) NodeMeta {
		if parent, ok := nodes[id]; ok && parent != nil {
			return parent.Meta()
		}
		return NodeMeta{}
	}
	availableFields := func(source NodeMeta) map[string]bool {
		out := map[string]bool{}
		for _, value := range source.AvailableFields {
			out[value.Name] = true
		}
		return out
	}
	availableMetrics := func(source NodeMeta) map[string]bool {
		out := map[string]bool{}
		for _, value := range source.AvailableMetrics {
			out[value.Name] = true
		}
		return out
	}
	fields := map[string]bool{}
	for _, field := range meta.AvailableFields {
		fields[field.Name] = true
	}
	metrics := map[string]bool{}
	for _, metric := range meta.AvailableMetrics {
		metrics[metric.Name] = true
	}
	switch n := node.(type) {
	case ScanDataset:
		if n.Dataset == "" {
			return fmt.Errorf("dataset is required")
		}
		if len(n.Inputs()) != 0 {
			return fmt.Errorf("scan cannot have inputs")
		}
		if len(meta.RootDatasets) != 1 || meta.RootDatasets[0] != n.Dataset {
			return fmt.Errorf("scan root datasets must contain only %q", n.Dataset)
		}
	case TraverseRelationship:
		if n.Input == "" {
			return fmt.Errorf("input is required")
		}
		if n.Path.Name == "" || n.Path.FromDataset == "" || n.Path.ToDataset == "" || len(n.Path.JoinKeys) == 0 {
			return fmt.Errorf("relationship path is incomplete")
		}
		if len(meta.RelationshipRoutes) == 0 || !containsRouteEdge(meta.RelationshipRoutes, n.Path) {
			return fmt.Errorf("relationship path is missing from metadata")
		}
		availableDatasets := nodeDatasets(n.Input, nodes, map[string]bool{})
		if !availableDatasets[n.Path.FromDataset] {
			return fmt.Errorf("relationship path %q starts at unavailable dataset %q", n.Path.Name, n.Path.FromDataset)
		}
	case FilterRows:
		if n.Input == "" {
			return fmt.Errorf("input and predicate are required")
		}
		if n.Meta().FilterPhase == FilterPhaseUnspecified {
			return fmt.Errorf("filter phase is required")
		}
		if !n.Source.valid() {
			return fmt.Errorf("filter source %q is invalid", n.Source)
		}
		switch n.Source {
		case FilterSourceNamed:
			if strings.TrimSpace(n.Name) == "" {
				return fmt.Errorf("named filter requires a name")
			}
		case FilterSourceRequest:
			if n.Name != "" {
				return fmt.Errorf("request filter must not have a name")
			}
		}
		if err := validateFilterPlacement(n, nodes); err != nil {
			return err
		}
		sourceFields, sourceMetrics := availableFields(inputMeta(n.Input)), availableMetrics(inputMeta(n.Input))
		if err := n.Predicate.validate(sourceFields, sourceMetrics); err != nil {
			return err
		}
		predicateFields := n.Predicate.fields()
		if len(n.Fields) > 0 && !sameStrings(predicateFields, n.Fields) {
			return fmt.Errorf("explicit predicate fields do not match predicate references")
		}
		predicateFieldSet := map[string]bool{}
		for _, field := range predicateFields {
			predicateFieldSet[field] = true
		}
		for field, routes := range n.FieldRoutes {
			if !predicateFieldSet[field] {
				return fmt.Errorf("filter field route %q is not referenced by predicate", field)
			}
			for _, route := range routes {
				if !containsRoute(metaOfInput(nodes, n.Input).RelationshipRoutes, route) {
					return fmt.Errorf("filter field route %q is unavailable", field)
				}
			}
		}
	case AggregateMetrics:
		if n.Input == "" || len(n.Metrics) == 0 {
			return fmt.Errorf("input and metrics are required")
		}
		input := nodes[n.Input]
		switch input.(type) {
		case ScanDataset, *ScanDataset, TraverseRelationship, *TraverseRelationship, FilterRows, *FilterRows, BundleBranches, *BundleBranches:
		default:
			return fmt.Errorf("aggregate input %q crosses an aggregate boundary", n.Input)
		}
		if input.Meta().FilterPhase.rank() > FilterPhaseRelationship.rank() {
			return fmt.Errorf("aggregate input %q has phase %q after the aggregate boundary", n.Input, input.Meta().FilterPhase)
		}
		seen := map[string]bool{}
		sourceFields, sourceMetrics := availableFields(inputMeta(n.Input)), availableMetrics(inputMeta(n.Input))
		for _, key := range n.GroupBy {
			if !sourceFields[key] {
				return fmt.Errorf("group-by field %q is unavailable", key)
			}
		}
		for _, metric := range n.Metrics {
			if metric.Name == "" || seen[metric.Name] {
				return fmt.Errorf("metric names must be non-empty and unique")
			}
			seen[metric.Name] = true
			var metadata *Metric
			for index := range meta.AvailableMetrics {
				if meta.AvailableMetrics[index].Name == metric.Name {
					metadata = &meta.AvailableMetrics[index]
					break
				}
			}
			if metadata == nil {
				return fmt.Errorf("metric %q is missing from aggregate available metrics", metric.Name)
			}
			if metadata.Empty != metric.Empty {
				return fmt.Errorf("metric %q empty policy metadata %q does not match spec %q", metric.Name, metadata.Empty, metric.Empty)
			}
			if metric.Type != "" && metadata.Type != metric.Type {
				return fmt.Errorf("metric %q type metadata %q does not match spec %q", metric.Name, metadata.Type, metric.Type)
			}
			if metric.Input != "" && !sourceFields[metric.Input] && !sourceMetrics[metric.Input] {
				return fmt.Errorf("metric %q input %q is unavailable", metric.Name, metric.Input)
			}
			if metric.Aggregation == "" {
				return fmt.Errorf("metric %q has no aggregation", metric.Name)
			}
			if strings.EqualFold(metric.Aggregation, "COUNT") && metric.Input == "" {
				return fmt.Errorf("metric %q COUNT requires an input", metric.Name)
			}
			if strings.EqualFold(metric.Aggregation, "COUNT_STAR") && metric.Input != "" {
				return fmt.Errorf("metric %q COUNT_STAR must not declare an input", metric.Name)
			}
			if strings.EqualFold(metric.Aggregation, "COUNT_DISTINCT_PAIR") {
				if len(metric.Inputs) != 2 {
					return fmt.Errorf("metric %q COUNT_DISTINCT_PAIR requires two inputs", metric.Name)
				}
				for _, input := range metric.Inputs {
					if input == "" || (!sourceFields[input] && !sourceMetrics[input]) {
						return fmt.Errorf("metric %q input %q is unavailable", metric.Name, input)
					}
				}
			}
			if len(metric.Inputs) != 0 && !strings.EqualFold(metric.Aggregation, "COUNT_DISTINCT_PAIR") {
				return fmt.Errorf("metric %q has inputs for unsupported aggregation %q", metric.Name, metric.Aggregation)
			}
			for _, filter := range metric.Filters {
				if filter.Source != FilterSourceNamed {
					return fmt.Errorf("metric %q filter source must be named", metric.Name)
				}
				if strings.TrimSpace(filter.Name) == "" {
					return fmt.Errorf("metric %q filter name is required", metric.Name)
				}
				if filter.Phase != FilterPhaseScan && filter.Phase != FilterPhaseRelationship {
					return fmt.Errorf("metric %q filter %q has invalid phase %q", metric.Name, filter.Name, filter.Phase)
				}
				if filter.Phase == FilterPhaseScan && len(filter.RelationshipRoutes) != 0 {
					return fmt.Errorf("metric %q filter %q scan phase has relationship routes", metric.Name, filter.Name)
				}
				if filter.Phase == FilterPhaseRelationship && len(filter.RelationshipRoutes) == 0 {
					return fmt.Errorf("metric %q filter %q relationship phase requires a relationship route", metric.Name, filter.Name)
				}
				for _, route := range filter.RelationshipRoutes {
					if !containsRoute(metaOfInput(nodes, n.Input).RelationshipRoutes, route) {
						return fmt.Errorf("metric %q filter %q relationship route is unavailable", metric.Name, filter.Name)
					}
				}
				if err := filter.Predicate.validate(sourceFields, sourceMetrics); err != nil {
					return fmt.Errorf("metric %q filter %q: %w", metric.Name, filter.Name, err)
				}
				if len(filter.Fields) > 0 && !sameStrings(filter.Fields, filter.Predicate.fields()) {
					return fmt.Errorf("metric %q filter %q fields do not match predicate references", metric.Name, filter.Name)
				}
			}
		}
		if len(meta.AvailableMetrics) != len(n.Metrics) {
			return fmt.Errorf("aggregate available metrics must exactly match metric specs")
		}
		if len(meta.OutputGrain.Fields) != len(n.GroupBy) || !sameOrdered(meta.OutputGrain.Fields, n.GroupBy) {
			return fmt.Errorf("output grain must equal group-by fields")
		}
		if len(meta.AvailableFields) != len(n.GroupBy) {
			return fmt.Errorf("aggregate available fields must equal group-by fields")
		}
		for index, field := range meta.AvailableFields {
			if field.Name != n.GroupBy[index] {
				return fmt.Errorf("aggregate available field %q does not match group-by field %q", field.Name, n.GroupBy[index])
			}
		}
		if n.Spatial != nil {
			if n.Spatial.Latitude == "" || n.Spatial.Longitude == "" {
				return fmt.Errorf("spatial bucket coordinates are required")
			}
			if n.Spatial.Latitude == n.Spatial.Longitude {
				return fmt.Errorf("spatial bucket coordinates must be distinct")
			}
			if n.Spatial.Zoom < 0 || n.Spatial.Zoom > 30 || n.Spatial.CellPixels <= 0 {
				return fmt.Errorf("spatial bucket zoom and cell size are invalid")
			}
			if !sourceFields[n.Spatial.Latitude] || !sourceFields[n.Spatial.Longitude] {
				return fmt.Errorf("spatial bucket coordinates are unavailable")
			}
		}
	case StitchAggregates:
		if len(n.InputsList) < 2 {
			return fmt.Errorf("at least two aggregate inputs are required")
		}
		if len(n.Keys) == 0 {
			return fmt.Errorf("stitch keys are required")
		}
		seenRoots := map[string]bool{}
		for index, input := range n.InputsList {
			parent, ok := nodes[input]
			if !ok {
				return fmt.Errorf("stitch input %q is unavailable", input)
			}
			switch parent.(type) {
			case AggregateMetrics:
			case *AggregateMetrics:
			default:
				return fmt.Errorf("stitch input %q must be AggregateMetrics", input)
			}
			parentMeta := parent.Meta()
			if len(parentMeta.RootDatasets) != 1 {
				return fmt.Errorf("stitch input %q must have exactly one root dataset", input)
			}
			if seenRoots[parentMeta.RootDatasets[0]] {
				return fmt.Errorf("stitch input roots must be distinct, %q is repeated", parentMeta.RootDatasets[0])
			}
			seenRoots[parentMeta.RootDatasets[0]] = true
			if !sameOrdered(parentMeta.OutputGrain.Fields, n.Keys) {
				return fmt.Errorf("stitch input %q grain must equal stitch keys", input)
			}
			available := availableFields(parentMeta)
			for _, key := range n.Keys {
				if !available[key] {
					return fmt.Errorf("stitch input %d key %q is unavailable", index, key)
				}
			}
		}
		if !sameOrdered(meta.OutputGrain.Fields, n.Keys) {
			return fmt.Errorf("stitch output grain must equal stitch keys")
		}
	case ComputeRatio:
		if n.Input == "" || n.Numerator == "" || n.Denominator == "" || n.Output == "" {
			return fmt.Errorf("input, numerator, denominator, and output are required")
		}
		sourceMetrics := availableMetrics(inputMeta(n.Input))
		if n.Numerator == n.Denominator || !sourceMetrics[n.Numerator] || !sourceMetrics[n.Denominator] {
			return fmt.Errorf("ratio operands must be distinct available metrics")
		}
		if !metrics[n.Output] {
			return fmt.Errorf("ratio output metric %q is not available", n.Output)
		}
		if err := validateComputeInput(n.Input, nodes); err != nil {
			return err
		}
		if meta.FilterPhase != FilterPhasePostAggregate {
			return fmt.Errorf("ratio compute must use post-aggregate phase")
		}
	case ComputeDerived:
		if n.Input == "" || n.Output == "" {
			return fmt.Errorf("input, output, and expression are required")
		}
		if err := n.Expression.validate(availableMetrics(inputMeta(n.Input))); err != nil {
			return err
		}
		if !metrics[n.Output] {
			return fmt.Errorf("derived output metric %q is not available", n.Output)
		}
		if err := validateComputeInput(n.Input, nodes); err != nil {
			return err
		}
		if meta.FilterPhase != FilterPhasePostAggregate {
			return fmt.Errorf("derived compute must use post-aggregate phase")
		}
	case SortLimit:
		if n.Input == "" {
			return fmt.Errorf("input is required")
		}
		if n.Limit < 0 || n.Offset < 0 {
			return fmt.Errorf("limit and offset cannot be negative")
		}
		sourceFields, sourceMetrics := availableFields(inputMeta(n.Input)), availableMetrics(inputMeta(n.Input))
		seenProjection := map[string]bool{}
		for _, projection := range n.Projection {
			if projection.Name == "" || projection.Source == "" {
				return fmt.Errorf("projection name and source are required")
			}
			if seenProjection[projection.Name] {
				return fmt.Errorf("projection name %q is duplicated", projection.Name)
			}
			seenProjection[projection.Name] = true
			if !sourceFields[projection.Source] && !sourceMetrics[projection.Source] {
				return fmt.Errorf("projection source %q is unavailable", projection.Source)
			}
		}
		// Sort keys are evaluated before the projection is rendered, but the
		// renderer orders by the projected output alias. Permit a key that is
		// one of those unique aliases while still rejecting unknown names.
		for _, key := range n.Sort {
			if !sourceFields[key.Field] && !sourceMetrics[key.Field] && !seenProjection[key.Field] {
				return fmt.Errorf("sort field %q is unavailable", key.Field)
			}
		}
	case TotalRows:
		if n.Input == "" || n.TotalField == "" {
			return fmt.Errorf("input and total field are required")
		}
		if err := validUnqualifiedName(n.TotalField); err != nil {
			return fmt.Errorf("total field %q: %w", n.TotalField, err)
		}
		input, ok := nodes[n.Input]
		if !ok || input == nil {
			return fmt.Errorf("input %q is unavailable", n.Input)
		}
		sortInput, ok := asSortLimit(input)
		if !ok {
			return fmt.Errorf("input %q must be a SortLimit", n.Input)
		}
		inputMeta := sortInput.Meta()
		if err := validateTotalRowsSortInput(sortInput); err != nil {
			return err
		}
		if n.FilterPhase != FilterPhasePostAggregate {
			return fmt.Errorf("total rows must use post-aggregate phase")
		}
		if !n.OutputGrain.equal(inputMeta.OutputGrain) {
			return fmt.Errorf("total rows output grain must match input")
		}
		if len(n.AvailableMetrics) != 0 {
			return fmt.Errorf("total rows must not declare available metrics")
		}
		if len(n.AvailableFields) != len(inputMeta.AvailableFields)+1 {
			return fmt.Errorf("total rows available fields must append exactly one field")
		}
		for index, field := range inputMeta.AvailableFields {
			if n.AvailableFields[index] != field {
				return fmt.Errorf("total rows field %q does not match input field %q", n.AvailableFields[index].Name, field.Name)
			}
		}
		last := n.AvailableFields[len(n.AvailableFields)-1]
		if last.Name != n.TotalField || last.Type != "integer" {
			return fmt.Errorf("total rows field metadata must be integer %q", n.TotalField)
		}
	case BundleBranches:
		if err := validateBundleEnvelope(n, nodes); err != nil {
			return err
		}
	case *BundleBranches:
		if n == nil {
			return fmt.Errorf("node is nil")
		}
		if err := validateBundleEnvelope(*n, nodes); err != nil {
			return err
		}
	case SpatialEnvelope:
		return validateSpatialEnvelope(n, nodes)
	case AnalyticalEnvelope:
		return validateAnalyticalEnvelope(n, nodes)
	default:
		return fmt.Errorf("unsupported node kind %q", node.Kind())
	}
	return nil
}

func validateSpatialEnvelope(n SpatialEnvelope, nodes map[string]Node) error {
	inputs := n.Inputs()
	if len(inputs) == 0 {
		return fmt.Errorf("spatial envelope input is required")
	}
	if n.Operation == SpatialEnvelopeMetadata {
		if len(inputs) > 2 {
			return fmt.Errorf("spatial metadata envelope accepts at most two inputs")
		}
	} else if len(inputs) != 1 {
		return fmt.Errorf("spatial envelope operation %q requires one input", n.Operation)
	}
	switch n.Operation {
	case SpatialEnvelopeTileAggregate:
		if n.Zoom < 0 || n.Zoom > 30 || n.TargetZoom <= n.Zoom || n.TargetZoom > 30 || n.CellPixels <= 0 || n.Buffer < 0 || n.Buffer > 4096 {
			return fmt.Errorf("spatial aggregate envelope options are invalid")
		}
		if n.Latitude == "" || n.Longitude == "" || (len(n.Metrics) == 0 && len(n.MetricProperties) == 0) {
			return fmt.Errorf("spatial aggregate envelope coordinates and metrics are required")
		}
	case SpatialEnvelopeTileRaw:
		if n.Zoom < 0 || n.Zoom > 30 || n.Buffer < 0 || n.Buffer > 4096 || n.FeatureCap <= 0 {
			return fmt.Errorf("spatial raw envelope options are invalid")
		}
		if n.Latitude == "" || n.Longitude == "" || len(n.Properties) == 0 {
			return fmt.Errorf("spatial raw envelope coordinates and properties are required")
		}
	case SpatialEnvelopeTileBudget:
		if n.Zoom < 0 || n.Zoom > 30 || n.Buffer < 0 || n.Buffer > 4096 || n.FeatureCap <= 0 || n.MaximumBytes <= 0 {
			return fmt.Errorf("spatial budget envelope options are invalid")
		}
		if n.Latitude == "" || n.Longitude == "" || len(n.Properties) == 0 {
			return fmt.Errorf("spatial budget envelope coordinates and properties are required")
		}
	case SpatialEnvelopeMetadata:
		if n.Latitude == "" || n.Longitude == "" || n.FeatureCap <= 0 || n.RawMinimumZoom < 0 || n.MaximumZoom < n.RawMinimumZoom || n.MaximumZoom > 30 {
			return fmt.Errorf("spatial metadata envelope options are invalid")
		}
	default:
		return fmt.Errorf("unsupported spatial envelope operation %q", n.Operation)
	}
	for _, input := range inputs {
		parent, ok := nodes[input]
		if !ok || parent == nil {
			return fmt.Errorf("spatial envelope input %q is unavailable", input)
		}
		available := map[string]bool{}
		for _, field := range parent.Meta().AvailableFields {
			available[field.Name] = true
		}
		for _, metric := range parent.Meta().AvailableMetrics {
			available[metric.Name] = true
		}
		if n.Operation == SpatialEnvelopeMetadata && input == inputs[0] {
			if !available[n.Latitude] || !available[n.Longitude] {
				return fmt.Errorf("spatial metadata coordinates are unavailable")
			}
			for _, metric := range n.Metrics {
				if !available[metric] {
					return fmt.Errorf("spatial metadata metric %q is unavailable", metric)
				}
			}
		}
		if n.Operation == SpatialEnvelopeMetadata && input != inputs[0] {
			for _, metric := range n.Metrics {
				if !available[metric] {
					return fmt.Errorf("spatial metadata total metric %q is unavailable", metric)
				}
			}
		}
		if n.Operation != SpatialEnvelopeMetadata {
			if !available[n.Latitude] || !available[n.Longitude] {
				return fmt.Errorf("spatial envelope coordinates are unavailable")
			}
		}
		if n.Operation == SpatialEnvelopeTileAggregate {
			for _, metric := range n.Metrics {
				if !available[metric] {
					return fmt.Errorf("spatial aggregate metric %q is unavailable", metric)
				}
			}
			for _, metric := range n.MetricProperties {
				if metric.Name == "" || metric.Source == "" || !available[metric.Source] {
					return fmt.Errorf("spatial aggregate metric property %q is unavailable", metric.Name)
				}
				if metric.Type != "decimal" && metric.Type != "integer" && metric.Type != "float" {
					return fmt.Errorf("spatial aggregate metric property %q has unsupported type %q", metric.Name, metric.Type)
				}
			}
		}
		if n.Operation == SpatialEnvelopeTileRaw || n.Operation == SpatialEnvelopeTileBudget {
			for _, property := range n.Properties {
				if !available[property.Source] {
					return fmt.Errorf("spatial property source %q is unavailable", property.Source)
				}
			}
		}
	}
	for _, property := range n.Properties {
		if property.Name == "" || property.Source == "" {
			return fmt.Errorf("spatial property name and source are required")
		}
	}
	return nil
}

func validateAnalyticalEnvelope(n AnalyticalEnvelope, nodes map[string]Node) error {
	if n.Input == "" {
		return fmt.Errorf("analytical envelope input is required")
	}
	parent, ok := nodes[n.Input]
	if !ok || parent == nil {
		return fmt.Errorf("analytical envelope input %q is unavailable", n.Input)
	}
	if n.Value == "" {
		return fmt.Errorf("analytical envelope value is required")
	}
	available := map[string]bool{}
	for _, field := range parent.Meta().AvailableFields {
		available[field.Name] = true
	}
	for _, metric := range parent.Meta().AvailableMetrics {
		available[metric.Name] = true
	}
	if !available[n.Value] {
		return fmt.Errorf("analytical envelope value %q is unavailable", n.Value)
	}
	if n.ValueType != "decimal" && n.ValueType != "integer" && n.ValueType != "float" {
		return fmt.Errorf("analytical envelope value type %q is unsupported", n.ValueType)
	}
	switch n.Operation {
	case AnalyticalEnvelopeHistogram:
		if n.BinCount <= 0 || n.BinCount > 100000 {
			return fmt.Errorf("histogram bin count must be positive")
		}
		if n.NullPolicy != "" && n.NullPolicy != "omit" && n.NullPolicy != "include" {
			return fmt.Errorf("histogram null policy must be omit or include")
		}
		if n.Approximation != "" && n.Approximation != "exact" && n.Approximation != "approximate" {
			return fmt.Errorf("histogram approximation must be exact or approximate")
		}
		if (n.DomainMinimum == nil) != (n.DomainMaximum == nil) {
			return fmt.Errorf("histogram domain requires both minimum and maximum")
		}
		if n.DomainMinimum != nil && (*n.DomainMinimum >= *n.DomainMaximum || math.IsNaN(*n.DomainMinimum) || math.IsNaN(*n.DomainMaximum) || math.IsInf(*n.DomainMinimum, 0) || math.IsInf(*n.DomainMaximum, 0)) {
			return fmt.Errorf("histogram domain requires finite minimum less than maximum")
		}
	case AnalyticalEnvelopeDistribution:
		// An omitted group computes one deterministic "all" population. When
		// present, the group must be a governed input field.
		if n.Group != "" && !available[n.Group] {
			return fmt.Errorf("distribution group %q is unavailable", n.Group)
		}
		if n.Limit < 0 {
			return fmt.Errorf("distribution limit cannot be negative")
		}
		if n.Approximation != "" && n.Approximation != "exact" && n.Approximation != "approximate" {
			return fmt.Errorf("distribution approximation must be exact or approximate")
		}
		if n.Outliers != "" && n.Outliers != "omit" && n.Outliers != "include" {
			return fmt.Errorf("distribution outliers must be omit or include")
		}
		if len(n.Quantiles) > 0 {
			previous := 0.0
			for index, quantile := range n.Quantiles {
				if math.IsNaN(quantile) || math.IsInf(quantile, 0) || quantile <= 0 || quantile >= 1 || (index > 0 && quantile <= previous) {
					return fmt.Errorf("distribution quantiles must be finite, strictly increasing, and between 0 and 1")
				}
				previous = quantile
			}
		}
		if (n.WhiskerLower == nil) != (n.WhiskerUpper == nil) {
			return fmt.Errorf("distribution whiskers require both lower and upper probabilities")
		}
		if n.WhiskerLower != nil && (math.IsNaN(*n.WhiskerLower) || math.IsNaN(*n.WhiskerUpper) || math.IsInf(*n.WhiskerLower, 0) || math.IsInf(*n.WhiskerUpper, 0) || *n.WhiskerLower <= 0 || *n.WhiskerUpper >= 1 || *n.WhiskerLower >= *n.WhiskerUpper) {
			return fmt.Errorf("distribution whiskers require finite probabilities 0 < lower < upper < 1")
		}
		if n.Outliers == "omit" && n.WhiskerLower == nil {
			return fmt.Errorf("distribution outliers omit requires whiskers")
		}
		if n.Outliers == "include" && n.WhiskerLower != nil {
			return fmt.Errorf("distribution whiskers require outliers omit")
		}
		for _, key := range n.Sort {
			switch key.Field {
			case "label", "min", "q1", "median", "q3", "max":
			default:
				return fmt.Errorf("unsupported distribution sort field %q", key.Field)
			}
		}
		if len(n.DistributionColumns) > 0 {
			if len(n.DistributionColumns) != len(n.Quantiles)+3 {
				return fmt.Errorf("distribution columns must contain label, min, quantiles, and max")
			}
			seen := make(map[string]struct{}, len(n.DistributionColumns))
			for _, column := range n.DistributionColumns {
				if column == "" {
					return fmt.Errorf("distribution result column cannot be empty")
				}
				if _, exists := seen[column]; exists {
					return fmt.Errorf("distribution result column %q is duplicated", column)
				}
				seen[column] = struct{}{}
			}
		}
	default:
		return fmt.Errorf("unsupported analytical envelope operation %q", n.Operation)
	}
	return nil
}

func metaOfInput(nodes map[string]Node, id string) NodeMeta {
	if node, ok := nodes[id]; ok && node != nil {
		return node.Meta()
	}
	return NodeMeta{}
}

func containsRoute(routes []RelationshipRoute, target RelationshipRoute) bool {
	want := relationshipRouteKey(target)
	for _, route := range routes {
		if relationshipRouteKey(route) == want {
			return true
		}
	}
	return false
}

func validateBundleEnvelope(n BundleBranches, nodes map[string]Node) error {
	if len(n.Branches) == 0 {
		return fmt.Errorf("at least one bundle branch is required")
	}
	if !n.OutputGrain.empty() || len(n.AvailableFields) != 0 || len(n.AvailableMetrics) != 0 || len(n.PhysicalLineage) != 0 {
		return fmt.Errorf("bundle envelope metadata must not declare a homogeneous grain or projection")
	}
	seenIDs := map[string]bool{}
	seenOrdinals := map[int]bool{}
	for _, branch := range n.Branches {
		if branch.ID == "" || branch.Input == "" || branch.Ordinal < 0 {
			return fmt.Errorf("bundle branch identity and input are required")
		}
		if seenIDs[branch.ID] {
			return fmt.Errorf("bundle branch id %q is duplicated", branch.ID)
		}
		if seenOrdinals[branch.Ordinal] {
			return fmt.Errorf("bundle branch ordinal %d is duplicated", branch.Ordinal)
		}
		seenIDs[branch.ID] = true
		seenOrdinals[branch.Ordinal] = true
		if _, ok := nodes[branch.Input]; !ok {
			return fmt.Errorf("bundle branch input %q is unavailable", branch.Input)
		}
	}
	return nil
}

func validateFilterPlacement(filter FilterRows, nodes map[string]Node) error {
	input, ok := nodes[filter.Input]
	if !ok || input == nil {
		return fmt.Errorf("filter input %q is unavailable", filter.Input)
	}
	phase := filter.Meta().FilterPhase
	switch input.(type) {
	case ScanDataset, *ScanDataset:
		if phase != FilterPhaseScan {
			return fmt.Errorf("scan filter must use scan phase, got %q", phase)
		}
	case TraverseRelationship, *TraverseRelationship, BundleBranches, *BundleBranches:
		if phase != FilterPhaseRelationship {
			return fmt.Errorf("relationship filter must use relationship phase, got %q", phase)
		}
	case FilterRows, *FilterRows:
		if input.Meta().FilterPhase != phase {
			return fmt.Errorf("filter phase %q does not match input filter phase %q", phase, input.Meta().FilterPhase)
		}
	case AggregateMetrics, *AggregateMetrics, StitchAggregates, *StitchAggregates, ComputeRatio, *ComputeRatio, ComputeDerived, *ComputeDerived:
		return fmt.Errorf("filter crosses aggregate boundary after %q", input.Kind())
	default:
		return fmt.Errorf("filter input %q has unsupported node kind %s", filter.Input, input.Kind())
	}
	return nil
}

func nodeDatasets(id string, nodes map[string]Node, visiting map[string]bool) map[string]bool {
	if visiting[id] {
		return map[string]bool{}
	}
	visiting[id] = true
	node, ok := nodes[id]
	if !ok || node == nil {
		return map[string]bool{}
	}
	result := map[string]bool{}
	switch value := node.(type) {
	case ScanDataset:
		result[value.Dataset] = true
	case *ScanDataset:
		if value != nil {
			result[value.Dataset] = true
		}
	case TraverseRelationship:
		for dataset := range nodeDatasets(value.Input, nodes, visiting) {
			result[dataset] = true
		}
		result[value.Path.ToDataset] = true
	case *TraverseRelationship:
		if value != nil {
			for dataset := range nodeDatasets(value.Input, nodes, visiting) {
				result[dataset] = true
			}
			result[value.Path.ToDataset] = true
		}
	default:
		for _, input := range node.Inputs() {
			for dataset := range nodeDatasets(input, nodes, visiting) {
				result[dataset] = true
			}
		}
	}
	delete(visiting, id)
	return result
}

// validateComputeInput ensures calculations begin only after every dataset root
// has been reduced. Chaining calculations is allowed, but a calculation may
// never consume a scan, relationship traversal, or row filter directly.
func validateComputeInput(id string, nodes map[string]Node) error {
	input, ok := nodes[id]
	if !ok || input == nil {
		return fmt.Errorf("compute input %q is unavailable", id)
	}
	switch input.(type) {
	case AggregateMetrics, *AggregateMetrics, StitchAggregates, *StitchAggregates, ComputeRatio, *ComputeRatio, ComputeDerived, *ComputeDerived:
		return nil
	default:
		return fmt.Errorf("compute input %q must be an aggregate, stitch, or compute node", id)
	}
}

func containsPath(paths []RelationshipPath, target RelationshipPath) bool {
	for _, path := range paths {
		if path.Name == target.Name && path.FromDataset == target.FromDataset && path.ToDataset == target.ToDataset && sameJoinKeys(path.JoinKeys, target.JoinKeys) {
			return true
		}
	}
	return false
}

func containsRouteEdge(routes []RelationshipRoute, target RelationshipPath) bool {
	for _, route := range routes {
		for _, path := range route.Edges {
			if path.Name == target.Name && path.FromDataset == target.FromDataset && path.ToDataset == target.ToDataset && sameJoinKeys(path.JoinKeys, target.JoinKeys) {
				return true
			}
		}
	}
	return false
}

func sameJoinKeys(a, b []JoinKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameOrdered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameStrings(a, b []string) bool {
	left, right := sortedStrings(a), sortedStrings(b)
	return sameOrdered(left, right)
}

func sameFields(a, b []Field) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]Field(nil), a...), append([]Field(nil), b...)
	sort.Slice(left, func(i, j int) bool { return left[i].Name < left[j].Name })
	sort.Slice(right, func(i, j int) bool { return right[i].Name < right[j].Name })
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sameMetrics(a, b []Metric) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]Metric(nil), a...), append([]Metric(nil), b...)
	sort.Slice(left, func(i, j int) bool { return left[i].Name < left[j].Name })
	sort.Slice(right, func(i, j int) bool { return right[i].Name < right[j].Name })
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// operation wrapping the existing SortLimit output. The input graph is never
// mutated; only the node map and output metadata are copied and updated.
func WithTotalRows(graph *Graph, totalField string) (*Graph, error) {
	if graph == nil {
		return nil, fmt.Errorf("plan graph is nil")
	}
	if err := graph.Validate(); err != nil {
		return nil, err
	}
	sortNode, ok := asSortLimit(graph.Nodes[graph.Output])
	if !ok {
		return nil, fmt.Errorf("total rows requires SortLimit output, got %s", graph.Nodes[graph.Output].Kind())
	}
	if err := validUnqualifiedName(totalField); err != nil {
		return nil, fmt.Errorf("total field %q: %w", totalField, err)
	}
	if err := validateTotalRowsSortInput(sortNode); err != nil {
		return nil, err
	}
	for _, field := range sortNode.AvailableFields {
		if field.Name == totalField {
			return nil, fmt.Errorf("total field %q already exists", totalField)
		}
	}
	totalID := graph.Output + "_total_rows"
	if _, exists := graph.Nodes[totalID]; exists {
		return nil, fmt.Errorf("total rows node %q already exists", totalID)
	}
	meta := sortNode.NodeMeta
	meta.NodeID = totalID
	meta.AvailableFields = append(append([]Field(nil), sortNode.AvailableFields...), Field{Name: totalField, Type: "integer"})
	meta.AvailableMetrics = nil
	meta.PhysicalLineage = nil
	meta.RelationshipRoutes = nil
	copyGraph := *graph
	copyGraph.Nodes = make(map[string]Node, len(graph.Nodes)+1)
	for id, node := range graph.Nodes {
		copyGraph.Nodes[id] = node
	}
	copyGraph.Nodes[totalID] = TotalRows{NodeMeta: meta, Input: graph.Output, TotalField: totalField}
	copyGraph.Output = totalID
	copyGraph.NodeMeta = meta
	if err := copyGraph.Validate(); err != nil {
		return nil, err
	}
	return &copyGraph, nil
}

func validateTotalRowsSortInput(sortNode SortLimit) error {
	if len(sortNode.AvailableMetrics) != 0 {
		return fmt.Errorf("total rows requires a row/field SortLimit input without available metrics")
	}
	return nil
}

// Canonical returns a stable JSON representation. All map-like collections
// are sorted before encoding, making this suitable for cache keys and plan
// fingerprints.
