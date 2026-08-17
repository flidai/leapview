package model

// ExecutionSnapshot returns an isolated, executable copy of the semantic
// model. Authoring context, sources, and credentials are deliberately omitted
// because governed planning never needs them. All mutable executable slices
// and maps are detached so callers may continue mutating the authored model
// after activation.
func (m *Model) ExecutionSnapshot() *Model {
	if m == nil {
		return nil
	}
	clone := *m
	clone.AIContext = nil
	clone.Connections = nil
	clone.Sources = nil
	clone.DefaultConnection = ""
	clone.Tables = snapshotTables(m.Tables)
	clone.Datasets = snapshotDatasets(m.Datasets)
	// StructuredRelationships is authoring-only state. The lowered runtime
	// graph is represented by canonical Relationships; retaining the authored
	// map would let execution consumers accidentally depend on an unvalidated
	// second representation (and would retain descriptive authoring context).
	clone.StructuredRelationships = nil
	clone.Relationships = snapshotRelationships(m.Relationships)
	clone.Dimensions = snapshotSemanticDimensions(m.Dimensions)
	clone.Filters = snapshotSemanticFilters(m.Filters)
	clone.Metrics = snapshotMetrics(m.Metrics)
	return &clone
}

func snapshotTables(values map[string]Table) map[string]Table {
	if values == nil {
		return nil
	}
	clone := make(map[string]Table, len(values))
	for name, table := range values {
		copyTable := table
		copyTable.AIContext = nil
		copyTable.Sources = append([]string(nil), table.Sources...)
		copyTable.SourceReads = snapshotStringSliceMap(table.SourceReads)
		copyTable.SourceDependencies = append([]string(nil), table.SourceDependencies...)
		copyTable.ModelDependencies = append([]string(nil), table.ModelDependencies...)
		copyTable.Dimensions = snapshotMetricDimensions(table.Dimensions)
		copyTable.Columns = snapshotModelColumns(table.Columns)
		copyTable.Entities = snapshotEntities(table.Entities)
		copyTable.Schema.Columns = snapshotColumnSchemas(table.Schema.Columns)
		clone[name] = copyTable
	}
	return clone
}

func snapshotDatasets(values map[string]SemanticDatasetSpec) map[string]SemanticDatasetSpec {
	if values == nil {
		return nil
	}
	clone := make(map[string]SemanticDatasetSpec, len(values))
	for name, value := range values {
		value.AIContext = nil
		clone[name] = value
	}
	return clone
}

func snapshotSemanticDimensions(values map[string]SemanticDimension) map[string]SemanticDimension {
	if values == nil {
		return nil
	}
	clone := make(map[string]SemanticDimension, len(values))
	for name, value := range values {
		value.AIContext = nil
		value.Grains = append([]string(nil), value.Grains...)
		bindings := value.Bindings
		value.Bindings = make(map[string]DimensionBinding, len(bindings))
		for fact, binding := range bindings {
			binding.Path = append([]string(nil), binding.Path...)
			value.Bindings[fact] = binding
		}
		clone[name] = value
	}
	return clone
}

func snapshotSemanticFilters(values map[string]SemanticFilterSpec) map[string]SemanticFilterSpec {
	if values == nil {
		return nil
	}
	clone := make(map[string]SemanticFilterSpec, len(values))
	for name, value := range values {
		clone[name] = snapshotSemanticFilter(value)
	}
	return clone
}

func snapshotSemanticFilter(value SemanticFilterSpec) SemanticFilterSpec {
	value.AIContext = nil
	value.Path = append([]string(nil), value.Path...)
	value.Value = snapshotLiteral(value.Value)
	if value.All != nil {
		children := value.All
		value.All = make([]SemanticFilterSpec, len(children))
		for index, child := range children {
			value.All[index] = snapshotSemanticFilter(child)
		}
	}
	if value.Any != nil {
		children := value.Any
		value.Any = make([]SemanticFilterSpec, len(children))
		for index, child := range children {
			value.Any[index] = snapshotSemanticFilter(child)
		}
	}
	if value.Not != nil {
		child := snapshotSemanticFilter(*value.Not)
		value.Not = &child
	}
	return value
}

func snapshotMetrics(values map[string]Metric) map[string]Metric {
	if values == nil {
		return nil
	}
	clone := make(map[string]Metric, len(values))
	for name, value := range values {
		value.AIContext = nil
		if value.Input != nil {
			input := *value.Input
			value.Input = &input
		}
		value.Where = append([]string(nil), value.Where...)
		clone[name] = value
	}
	return clone
}

func snapshotMetricDimensions(values map[string]MetricDimension) map[string]MetricDimension {
	if values == nil {
		return nil
	}
	clone := make(map[string]MetricDimension, len(values))
	for name, value := range values {
		value.AIContext = nil
		clone[name] = value
	}
	return clone
}

func snapshotModelColumns(values map[string]ModelColumn) map[string]ModelColumn {
	if values == nil {
		return nil
	}
	clone := make(map[string]ModelColumn, len(values))
	for name, value := range values {
		value.AIContext = nil
		clone[name] = value
	}
	return clone
}

func snapshotEntities(values map[string]ModelEntitySpec) map[string]ModelEntitySpec {
	if values == nil {
		return nil
	}
	clone := make(map[string]ModelEntitySpec, len(values))
	for name, value := range values {
		value.AIContext = nil
		value.Fields = append([]string(nil), value.Fields...)
		clone[name] = value
	}
	return clone
}

func snapshotRelationships(values []Relationship) []Relationship {
	if values == nil {
		return nil
	}
	clone := make([]Relationship, len(values))
	for index, value := range values {
		value.AIContext = nil
		value.FromFields = append([]string(nil), value.FromFields...)
		value.ToFields = append([]string(nil), value.ToFields...)
		clone[index] = value
	}
	return clone
}

func snapshotColumnSchemas(values []ColumnSchema) []ColumnSchema {
	if values == nil {
		return nil
	}
	clone := make([]ColumnSchema, len(values))
	for index, value := range values {
		clone[index] = value
		if value.Nullable != nil {
			nullable := *value.Nullable
			clone[index].Nullable = &nullable
		}
	}
	return clone
}

func snapshotStringSliceMap(values map[string][]string) map[string][]string {
	if values == nil {
		return nil
	}
	clone := make(map[string][]string, len(values))
	for key, value := range values {
		clone[key] = append([]string(nil), value...)
	}
	return clone
}

func snapshotLiteral(value any) any {
	switch value := value.(type) {
	case []any:
		clone := make([]any, len(value))
		for index, item := range value {
			clone[index] = snapshotLiteral(item)
		}
		return clone
	case []string:
		return append([]string(nil), value...)
	case []int:
		return append([]int(nil), value...)
	case []int64:
		return append([]int64(nil), value...)
	case []float64:
		return append([]float64(nil), value...)
	case []bool:
		return append([]bool(nil), value...)
	default:
		return value
	}
}
