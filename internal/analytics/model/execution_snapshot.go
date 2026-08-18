package model

import (
	"encoding/json"
	"fmt"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

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

// RuntimeSnapshot returns a detached model copy for dashboard/runtime
// projections. Unlike ExecutionSnapshot, it retains the compiled source and
// connection bindings required to acquire managed data and model execution
// definitions required to materialize tables. Secrets and authoring context
// remain excluded from the copy.
func (m *Model) RuntimeSnapshot() (*Model, error) {
	clone := m.ExecutionSnapshot()
	if clone == nil {
		return nil, nil
	}
	connections, err := snapshotConnections(m.Connections)
	if err != nil {
		return nil, err
	}
	sources, err := snapshotSources(m.Sources)
	if err != nil {
		return nil, err
	}
	clone.Connections = connections
	clone.Sources = sources
	return clone, nil
}

func snapshotConnections(values map[string]Connection) (map[string]Connection, error) {
	if values == nil {
		return nil, nil
	}
	clone := make(map[string]Connection, len(values))
	for name, value := range values {
		// Target binding is injected after this projection is activated. Clear
		// every endpoint/credential field so target state cannot leak across
		// serving generations; authored kind/access/defaults remain portable.
		value.Auth = nil
		value.Credentials = ConnectionCredentials{}
		value.Host = ""
		value.Port = 0
		value.Database = ""
		value.Username = ""
		value.SSLMode = ""
		value.RuntimeOptions = ConnectionRuntimeOptions{}
		value.Path = ""
		value.Root = ""
		value.Scope = ""
		var err error
		value.ReaderDefaults, err = cloneReaderDefaults(value.ReaderDefaults)
		if err != nil {
			return nil, fmt.Errorf("connection %q reader defaults: %w", name, err)
		}
		clone[name] = value
	}
	return clone, nil
}

func cloneReaderDefaults(value *projectcontracts.ReaderDefaults) (*projectcontracts.ReaderDefaults, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clone projectcontracts.ReaderDefaults
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func snapshotSources(values map[string]Source) (map[string]Source, error) {
	if values == nil {
		return nil, nil
	}
	clone := make(map[string]Source, len(values))
	for name, value := range values {
		value.Fields = cloneSourceFields(value.Fields)
		var err error
		value.PathLocation, err = clonePathLocation(value.PathLocation)
		if err != nil {
			return nil, fmt.Errorf("source %q path location: %w", name, err)
		}
		value.EffectivePathLocation, err = clonePathLocation(value.EffectivePathLocation)
		if err != nil {
			return nil, fmt.Errorf("source %q effective path location: %w", name, err)
		}
		value.Freshness = cloneSourceFreshness(value.Freshness)
		value.Schema.Columns = snapshotColumnSchemas(value.Schema.Columns)
		clone[name] = value
	}
	return clone, nil
}

func cloneSourceFields(values map[string]SourceField) map[string]SourceField {
	if values == nil {
		return nil
	}
	clone := make(map[string]SourceField, len(values))
	for name, value := range values {
		if value.Nullable != nil {
			nullable := *value.Nullable
			value.Nullable = &nullable
		}
		clone[name] = value
	}
	return clone
}

func clonePathLocation(value *projectcontracts.PathSourceLocation) (*projectcontracts.PathSourceLocation, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clone projectcontracts.PathSourceLocation
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func cloneSourceFreshness(value *SourceFreshnessSpec) *SourceFreshnessSpec {
	if value == nil {
		return nil
	}
	clone := *value
	if value.RevisionAt != nil {
		revisionAt := *value.RevisionAt
		clone.RevisionAt = &revisionAt
	}
	if value.WarningAfter != nil {
		warningAfter := *value.WarningAfter
		clone.WarningAfter = &warningAfter
	}
	if value.ErrorAfter != nil {
		errorAfter := *value.ErrorAfter
		clone.ErrorAfter = &errorAfter
	}
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

// CloneTable returns a detached executable copy suitable for immutable
// serving-state accessors. It intentionally shares the same deep-copy rules as
// ExecutionSnapshot without requiring callers to construct a temporary Model.
func CloneTable(value Table) Table {
	clone := snapshotTables(map[string]Table{"_": value})
	return clone["_"]
}

// CloneMetricDimension returns a detached executable copy of one physical
// dimension. Descriptive authoring context is deliberately omitted because it
// is not part of the serving contract.
func CloneMetricDimension(value MetricDimension) MetricDimension {
	value.AIContext = nil
	return value
}

// CloneRelationship returns a detached executable copy of one relationship.
// Endpoint field slices are copied so callers cannot mutate the source model
// through a compiled relationship path.
func CloneRelationship(value Relationship) Relationship {
	value.AIContext = nil
	value.FromFields = append([]string(nil), value.FromFields...)
	value.ToFields = append([]string(nil), value.ToFields...)
	return value
}

// CloneRelationships returns detached executable copies of a relationship
// path, preserving nil input and copying each endpoint field slice.
func CloneRelationships(values []Relationship) []Relationship {
	if values == nil {
		return nil
	}
	clone := make([]Relationship, len(values))
	for index, value := range values {
		clone[index] = CloneRelationship(value)
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
		for dataset, binding := range bindings {
			binding.Path = append([]string(nil), binding.Path...)
			value.Bindings[dataset] = binding
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
		clone[name] = CloneMetricDimension(value)
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

func snapshotEntities(values map[string]EntityDefinition) map[string]EntityDefinition {
	if values == nil {
		return nil
	}
	clone := make(map[string]EntityDefinition, len(values))
	for name, value := range values {
		value.AIContext = nil
		value.Fields = append([]string(nil), value.Fields...)
		clone[name] = value
	}
	return clone
}

func snapshotRelationships(values []Relationship) []Relationship {
	return CloneRelationships(values)
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
