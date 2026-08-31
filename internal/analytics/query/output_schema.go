package query

import (
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// OutputNullability is the tri-state proof result for one final governed
// projection. Unknown is deliberately distinct from nullable for diagnostics,
// but both serialize to an Arrow nullable field.
type OutputNullability string

const (
	OutputNullabilityUnknown OutputNullability = "unknown"
	OutputNullable           OutputNullability = "nullable"
	OutputDefinitelyNonNull  OutputNullability = "definitely_non_null"
)

func (n OutputNullability) ArrowNullable() bool {
	return n != OutputDefinitelyNonNull
}

// OutputFieldProvenance is intentionally opaque outside the query package.
// It carries the governed derivation evidence needed to explain a decision,
// but its source and relationship details must never become response metadata.
type OutputFieldProvenance struct {
	sourceRef            string
	dataset              string
	physicalField        string
	relationshipPath     []string
	relationshipNullable bool
	transformation       string
	metricEmpty          string
}

// OutputFieldDescriptor is one ordered field in the final governed response.
type OutputFieldDescriptor struct {
	Alias       string                `json:"alias"`
	LogicalType string                `json:"logical_type"`
	Nullability OutputNullability     `json:"nullability"`
	Provenance  OutputFieldProvenance `json:"-"`
}

func (f OutputFieldDescriptor) ArrowNullable() bool {
	return f.Nullability.ArrowNullable()
}

// OutputSchemaDescriptor preserves the final governed projection order.
type OutputSchemaDescriptor struct {
	Fields []OutputFieldDescriptor `json:"fields"`
}

func (d OutputSchemaDescriptor) Validate() error {
	seen := make(map[string]struct{}, len(d.Fields))
	for index, field := range d.Fields {
		if strings.TrimSpace(field.Alias) == "" || field.Alias != strings.TrimSpace(field.Alias) {
			return fmt.Errorf("output schema field %d has invalid alias %q", index, field.Alias)
		}
		if _, exists := seen[field.Alias]; exists {
			return fmt.Errorf("output schema repeats alias %q", field.Alias)
		}
		seen[field.Alias] = struct{}{}
		if strings.TrimSpace(field.LogicalType) == "" || field.LogicalType != strings.TrimSpace(field.LogicalType) {
			return fmt.Errorf("output schema field %q has invalid logical type %q", field.Alias, field.LogicalType)
		}
		switch field.Nullability {
		case OutputNullabilityUnknown, OutputNullable, OutputDefinitelyNonNull:
		default:
			return fmt.Errorf("output schema field %q has invalid nullability %q", field.Alias, field.Nullability)
		}
	}
	return nil
}

type derivedOutputField struct {
	logicalType string
	nullability OutputNullability
	provenance  OutputFieldProvenance
}

// DescribeOutputSchema derives the response schema from the validated final
// PlanIR and the activation-owned compiled schema. It never observes result
// rows, Arrow null counts, or DuckDB field nullability.
func (p *Planner) DescribeOutputSchema(plan Plan) (OutputSchemaDescriptor, error) {
	if p == nil || p.compiled == nil {
		return OutputSchemaDescriptor{}, fmt.Errorf("compiled planner is required")
	}
	if plan.IR == nil {
		return OutputSchemaDescriptor{}, fmt.Errorf("governed plan has no PlanIR output schema evidence")
	}
	if err := plan.IR.Validate(); err != nil {
		return OutputSchemaDescriptor{}, fmt.Errorf("validate output schema PlanIR: %w", err)
	}
	states, err := p.deriveOutputNode(plan.IR, plan.IR.Output, map[string]map[string]derivedOutputField{}, map[string]bool{})
	if err != nil {
		return OutputSchemaDescriptor{}, err
	}
	output, ok := plan.IR.Nodes[plan.IR.Output]
	if !ok {
		return OutputSchemaDescriptor{}, fmt.Errorf("governed PlanIR output %q is unavailable", plan.IR.Output)
	}
	aliases, err := outputFieldOrder(output)
	if err != nil {
		return OutputSchemaDescriptor{}, err
	}
	descriptor := OutputSchemaDescriptor{Fields: make([]OutputFieldDescriptor, len(aliases))}
	for index, alias := range aliases {
		state, exists := states[alias]
		if !exists {
			state = derivedOutputField{nullability: OutputNullabilityUnknown}
		}
		descriptor.Fields[index] = OutputFieldDescriptor{
			Alias: alias, LogicalType: state.logicalType,
			Nullability: state.nullability, Provenance: cloneOutputProvenance(state.provenance),
		}
	}
	if len(plan.Columns) != 0 {
		if len(plan.Columns) != len(descriptor.Fields) {
			return OutputSchemaDescriptor{}, fmt.Errorf("governed plan columns and output descriptor differ: %d != %d", len(plan.Columns), len(descriptor.Fields))
		}
		for index, column := range plan.Columns {
			if column != descriptor.Fields[index].Alias {
				return OutputSchemaDescriptor{}, fmt.Errorf("governed plan column %d is %q, descriptor is %q", index, column, descriptor.Fields[index].Alias)
			}
		}
	}
	if err := descriptor.Validate(); err != nil {
		return OutputSchemaDescriptor{}, err
	}
	return descriptor, nil
}

func (p *Planner) deriveOutputNode(graph *planir.Graph, id string, memo map[string]map[string]derivedOutputField, visiting map[string]bool) (map[string]derivedOutputField, error) {
	if cached, ok := memo[id]; ok {
		return cloneDerivedOutputFields(cached), nil
	}
	if visiting[id] {
		return nil, fmt.Errorf("output schema PlanIR cycle includes %q", id)
	}
	node, ok := graph.Nodes[id]
	if !ok {
		return nil, fmt.Errorf("output schema PlanIR node %q is unavailable", id)
	}
	visiting[id] = true
	defer delete(visiting, id)

	var states map[string]derivedOutputField
	var err error
	switch value := node.(type) {
	case planir.ScanDataset:
		states = p.deriveScanOutput(value.NodeMeta)
	case planir.TraverseRelationship:
		states, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
	case planir.FilterRows:
		states, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
	case planir.AggregateMetrics:
		var input map[string]derivedOutputField
		input, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
		if err == nil {
			states = deriveAggregateOutput(value, input)
		}
	case planir.StitchAggregates:
		states = deriveStitchOutput(value)
	case planir.ComputeRatio:
		states, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
		if err == nil {
			states[value.Output] = derivedOutputField{
				logicalType: logicalTypeFromMeta(value.NodeMeta, value.Output),
				nullability: OutputNullable,
				provenance:  OutputFieldProvenance{sourceRef: value.Output, transformation: "ratio"},
			}
		}
	case planir.ComputeDerived:
		states, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
		if err == nil {
			states[value.Output] = derivedOutputField{
				logicalType: logicalTypeFromMeta(value.NodeMeta, value.Output),
				nullability: deriveExpressionNullability(value.Expression, states),
				provenance:  OutputFieldProvenance{sourceRef: value.Output, transformation: "derived_expression"},
			}
		}
	case planir.SortLimit:
		var input map[string]derivedOutputField
		input, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
		if err == nil {
			states, err = deriveProjectionOutput(value, input)
		}
	case planir.TotalRows:
		states, err = p.deriveOutputNode(graph, value.Input, memo, visiting)
		if err == nil {
			states[value.TotalField] = derivedOutputField{
				logicalType: "integer", nullability: OutputDefinitelyNonNull,
				provenance: OutputFieldProvenance{sourceRef: value.TotalField, transformation: "count"},
			}
		}
	default:
		states, err = deriveUnknownNodeOutput(graph, node, p, memo, visiting)
	}
	if err != nil {
		return nil, err
	}
	memo[id] = cloneDerivedOutputFields(states)
	return cloneDerivedOutputFields(states), nil
}

func (p *Planner) deriveScanOutput(meta planir.NodeMeta) map[string]derivedOutputField {
	states := make(map[string]derivedOutputField, len(meta.AvailableFields)+len(meta.AvailableMetrics))
	for _, field := range meta.AvailableFields {
		states[field.Name] = p.derivePhysicalOutput(meta, field.Name, field.Type)
	}
	for _, metric := range meta.AvailableMetrics {
		states[metric.Name] = derivedOutputField{
			logicalType: metric.Type, nullability: metricNullability("", metric.Empty),
			provenance: OutputFieldProvenance{sourceRef: metric.Name, transformation: "metric", metricEmpty: metric.Empty},
		}
	}
	return states
}

func (p *Planner) derivePhysicalOutput(meta planir.NodeMeta, name, logicalType string) derivedOutputField {
	state := derivedOutputField{
		logicalType: logicalType, nullability: OutputNullabilityUnknown,
		provenance: OutputFieldProvenance{sourceRef: name, transformation: "projection"},
	}
	found := false
	for _, lineage := range meta.PhysicalLineage {
		if lineage.Logical != name {
			continue
		}
		candidate := p.physicalOutputNullability(lineage)
		if !found {
			state.nullability = candidate
			state.provenance.dataset = lineage.Dataset
			state.provenance.physicalField = lineage.Field
			state.provenance.relationshipPath = append([]string(nil), lineage.Route...)
			state.provenance.relationshipNullable = len(lineage.Route) != 0
			found = true
		} else {
			state.nullability = mergeOutputNullability(state.nullability, candidate)
			if len(lineage.Route) != 0 {
				state.provenance.relationshipNullable = true
			}
		}
	}
	return state
}

func (p *Planner) physicalOutputNullability(lineage planir.PhysicalLineage) OutputNullability {
	if len(lineage.Route) != 0 {
		return OutputNullable
	}
	table, ok := p.datasetTable(lineage.Dataset)
	if !ok {
		return OutputNullabilityUnknown
	}
	name := fieldAlias(lineage.Field)
	for _, column := range table.Schema.Columns {
		if column.Name != name {
			continue
		}
		if column.Nullable == nil {
			return OutputNullabilityUnknown
		}
		if *column.Nullable {
			return OutputNullable
		}
		return OutputDefinitelyNonNull
	}
	return OutputNullabilityUnknown
}

func deriveAggregateOutput(node planir.AggregateMetrics, input map[string]derivedOutputField) map[string]derivedOutputField {
	states := make(map[string]derivedOutputField, len(node.GroupBy)+len(node.Metrics))
	for _, name := range node.GroupBy {
		state, ok := input[name]
		if !ok {
			state = derivedOutputField{logicalType: logicalTypeFromMeta(node.NodeMeta, name), nullability: OutputNullabilityUnknown}
		}
		states[name] = state
	}
	for _, bucket := range node.TimeBuckets {
		state, ok := input[bucket.Field]
		if !ok {
			state = derivedOutputField{nullability: OutputNullabilityUnknown}
		}
		state.logicalType = logicalTypeFromMeta(node.NodeMeta, bucket.Field)
		state.provenance.transformation = "time_bucket"
		states[bucket.Field] = state
	}
	for _, metric := range node.Metrics {
		states[metric.Name] = derivedOutputField{
			logicalType: metric.Type,
			nullability: metricNullability(metric.Aggregation, metric.Empty),
			provenance:  OutputFieldProvenance{sourceRef: metric.Name, transformation: "aggregate", metricEmpty: metric.Empty},
		}
	}
	return states
}

func deriveStitchOutput(node planir.StitchAggregates) map[string]derivedOutputField {
	states := make(map[string]derivedOutputField, len(node.AvailableFields)+len(node.AvailableMetrics))
	for _, field := range node.AvailableFields {
		nullability := OutputNullable
		if field.Name == "__scalar_key" {
			nullability = OutputDefinitelyNonNull
		}
		states[field.Name] = derivedOutputField{
			logicalType: field.Type, nullability: nullability,
			provenance: OutputFieldProvenance{sourceRef: field.Name, transformation: "full_outer_stitch"},
		}
	}
	for _, metric := range node.AvailableMetrics {
		states[metric.Name] = derivedOutputField{
			logicalType: metric.Type, nullability: metricNullability("", metric.Empty),
			provenance: OutputFieldProvenance{sourceRef: metric.Name, transformation: "full_outer_stitch", metricEmpty: metric.Empty},
		}
	}
	return states
}

func deriveProjectionOutput(node planir.SortLimit, input map[string]derivedOutputField) (map[string]derivedOutputField, error) {
	states := make(map[string]derivedOutputField, len(node.Projection))
	for _, projection := range node.Projection {
		state, ok := input[projection.Source]
		if !ok {
			state = derivedOutputField{logicalType: logicalTypeFromMeta(node.NodeMeta, projection.Name), nullability: OutputNullabilityUnknown}
		}
		state.provenance = cloneOutputProvenance(state.provenance)
		state.provenance.sourceRef = projection.Source
		switch strings.ToLower(strings.TrimSpace(projection.Mask)) {
		case "":
		case "null":
			state.nullability = OutputNullable
			state.provenance.transformation = "mask_null"
		case "redact", "redacted":
			state.nullability = OutputDefinitelyNonNull
			state.provenance.transformation = "mask_redact"
		case "zero":
			state.nullability = OutputDefinitelyNonNull
			state.provenance.transformation = "mask_zero"
		default:
			return nil, fmt.Errorf("output schema projection %q has unsupported mask %q", projection.Name, projection.Mask)
		}
		if state.logicalType == "" {
			state.logicalType = logicalTypeFromMeta(node.NodeMeta, projection.Name)
		}
		states[projection.Name] = state
	}
	return states, nil
}

func deriveUnknownNodeOutput(graph *planir.Graph, node planir.Node, p *Planner, memo map[string]map[string]derivedOutputField, visiting map[string]bool) (map[string]derivedOutputField, error) {
	states := map[string]derivedOutputField{}
	inputs := node.Inputs()
	if len(inputs) == 1 {
		input, err := p.deriveOutputNode(graph, inputs[0], memo, visiting)
		if err != nil {
			return nil, err
		}
		states = input
	}
	for _, field := range node.Meta().AvailableFields {
		if _, ok := states[field.Name]; !ok {
			states[field.Name] = derivedOutputField{logicalType: field.Type, nullability: OutputNullabilityUnknown}
		}
	}
	for _, metric := range node.Meta().AvailableMetrics {
		if _, ok := states[metric.Name]; !ok {
			states[metric.Name] = derivedOutputField{logicalType: metric.Type, nullability: OutputNullabilityUnknown}
		}
	}
	return states, nil
}

func deriveExpressionNullability(expression planir.ScalarExpr, fields map[string]derivedOutputField) OutputNullability {
	switch expression.Kind {
	case planir.ScalarMetricRef:
		if field, ok := fields[expression.Metric]; ok {
			return field.nullability
		}
		return OutputNullabilityUnknown
	case planir.ScalarLiteral:
		return OutputDefinitelyNonNull
	case planir.ScalarNeg, planir.ScalarPos:
		if len(expression.Children) != 1 {
			return OutputNullabilityUnknown
		}
		return deriveExpressionNullability(expression.Children[0], fields)
	case planir.ScalarAdd, planir.ScalarSub, planir.ScalarMul:
		return combineRequiredInputs(expression.Children, fields)
	case planir.ScalarDiv, planir.ScalarSafeDiv:
		return OutputNullable
	case planir.ScalarFunction:
		switch strings.ToLower(expression.Function) {
		case "coalesce":
			allNullable := len(expression.Children) != 0
			for _, child := range expression.Children {
				nullability := deriveExpressionNullability(child, fields)
				if nullability == OutputDefinitelyNonNull {
					return OutputDefinitelyNonNull
				}
				if nullability != OutputNullable {
					allNullable = false
				}
			}
			if allNullable {
				return OutputNullable
			}
			return OutputNullabilityUnknown
		case "nullif", "safe_divide":
			return OutputNullable
		case "abs", "round":
			if len(expression.Children) == 0 {
				return OutputNullabilityUnknown
			}
			return deriveExpressionNullability(expression.Children[0], fields)
		default:
			return OutputNullabilityUnknown
		}
	default:
		return OutputNullabilityUnknown
	}
}

func combineRequiredInputs(children []planir.ScalarExpr, fields map[string]derivedOutputField) OutputNullability {
	unknown := false
	for _, child := range children {
		switch deriveExpressionNullability(child, fields) {
		case OutputNullable:
			return OutputNullable
		case OutputNullabilityUnknown:
			unknown = true
		}
	}
	if unknown || len(children) == 0 {
		return OutputNullabilityUnknown
	}
	return OutputDefinitelyNonNull
}

func metricNullability(aggregation, empty string) OutputNullability {
	switch strings.ToUpper(strings.TrimSpace(aggregation)) {
	case "COUNT", "COUNT_STAR", "COUNT_DISTINCT", "COUNT_DISTINCT_PAIR":
		return OutputDefinitelyNonNull
	}
	switch strings.ToLower(strings.TrimSpace(empty)) {
	case "zero":
		return OutputDefinitelyNonNull
	case "null":
		return OutputNullable
	default:
		return OutputNullabilityUnknown
	}
}

func mergeOutputNullability(left, right OutputNullability) OutputNullability {
	if left == OutputNullable || right == OutputNullable {
		return OutputNullable
	}
	if left == OutputNullabilityUnknown || right == OutputNullabilityUnknown {
		return OutputNullabilityUnknown
	}
	return OutputDefinitelyNonNull
}

func outputFieldOrder(node planir.Node) ([]string, error) {
	switch value := node.(type) {
	case planir.SortLimit:
		aliases := make([]string, len(value.Projection))
		for index, projection := range value.Projection {
			aliases[index] = projection.Name
		}
		return aliases, nil
	case planir.TotalRows:
		return outputMetaNames(value.NodeMeta), nil
	default:
		return nil, fmt.Errorf("governed PlanIR output %q has no final projection descriptor", node.Meta().NodeID)
	}
}

func outputMetaNames(meta planir.NodeMeta) []string {
	values := make([]string, 0, len(meta.AvailableFields)+len(meta.AvailableMetrics))
	for _, field := range meta.AvailableFields {
		values = append(values, field.Name)
	}
	for _, metric := range meta.AvailableMetrics {
		values = append(values, metric.Name)
	}
	return values
}

func logicalTypeFromMeta(meta planir.NodeMeta, name string) string {
	for _, field := range meta.AvailableFields {
		if field.Name == name {
			return field.Type
		}
	}
	for _, metric := range meta.AvailableMetrics {
		if metric.Name == name {
			return metric.Type
		}
	}
	return ""
}

func cloneDerivedOutputFields(values map[string]derivedOutputField) map[string]derivedOutputField {
	out := make(map[string]derivedOutputField, len(values))
	for name, field := range values {
		field.provenance = cloneOutputProvenance(field.provenance)
		out[name] = field
	}
	return out
}

func cloneOutputProvenance(value OutputFieldProvenance) OutputFieldProvenance {
	value.relationshipPath = append([]string(nil), value.relationshipPath...)
	return value
}
