package query

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// CompiledMetricLineage is the physical lineage retained for a metric.  The
// planner does not use this to emit SQL yet; retaining it here means a later
// typed planner can consume the serving-state contract without reparsing the
// semantic model.
type CompiledMetricLineage struct {
	Entries []CompiledLineageEntry
}

// CompiledNamedFilter retains authored semantic identity beside its typed
// predicate so planning never needs to consult mutable authoring maps.
type CompiledNamedFilter struct {
	Name   string
	Filter Filter
}

// CompiledLineageEntry keeps semantic reference identity alongside the
// physical field and chosen route. In particular, two named filters may use
// the same physical field through different role-playing paths.
type CompiledLineageEntry struct {
	Role      string
	Reference string
	Field     string
	Path      []semanticmodel.Relationship
}

// CompiledAggregateMetric is the closed payload for an aggregate metric.
// Dataset, input, population filters, and empty-value behavior are all kept in
// this payload so a compiled metric cannot accidentally combine aggregate
// fields with a derived or ratio definition.
type CompiledAggregateMetric struct {
	Dataset       string
	Aggregation   string
	InputField    string
	NamedFilters  []CompiledNamedFilter
	Empty         string
	TimeDimension string
}

// CompiledDerivedMetric is the closed payload for a derived metric. The
// expression is parsed exactly once during activation.
type CompiledDerivedMetric struct {
	Expression semanticmodel.Expression
}

// CompiledRatioMetric is the closed payload for a ratio metric. Ratios retain
// numerator and denominator identity all the way to PlanIR; they are not
// represented as a synthesized scalar expression.
type CompiledRatioMetric struct {
	Numerator   string
	Denominator string
}

// CompiledMetric is one immutable node in the metric evaluation DAG. Type is
// the canonical semantic tag, and exactly one of Aggregate, Derived, or Ratio
// is populated. The remaining fields are common metadata shared by all metric
// kinds and are request-invariant after activation.
type CompiledMetric struct {
	Name         string
	Type         string
	Label        string
	Description  string
	Hidden       bool
	Dependencies []string
	RootDatasets []string
	Unit         string
	Format       string
	Lineage      CompiledMetricLineage

	Aggregate *CompiledAggregateMetric
	Derived   *CompiledDerivedMetric
	Ratio     *CompiledRatioMetric
}

// CompiledDataset is the immutable serving binding for one semantic alias.
// The physical project model name is retained separately from the detached
// executable table so aliases never become physical relation names by
// accident. Table returns a detached copy on every call.
type CompiledDataset struct {
	alias                string
	modelName            string
	table                semanticmodel.Table
	defaultTimeDimension string
	displayName          string
	description          string
}

func (d CompiledDataset) Alias() string                { return d.alias }
func (d CompiledDataset) ModelName() string            { return d.modelName }
func (d CompiledDataset) DefaultTimeDimension() string { return d.defaultTimeDimension }
func (d CompiledDataset) DisplayName() string          { return d.displayName }
func (d CompiledDataset) Description() string          { return d.description }

// Table returns a deep copy, preserving the read-only serving boundary.
func (d CompiledDataset) Table() semanticmodel.Table {
	if d.alias == "" {
		return semanticmodel.Table{}
	}
	return semanticmodel.CloneTable(d.table)
}

func (m CompiledMetric) validatePayload() error {
	count := 0
	if m.Aggregate != nil {
		count++
	}
	if m.Derived != nil {
		count++
	}
	if m.Ratio != nil {
		count++
	}
	if count != 1 {
		return fmt.Errorf("metric %q requires exactly one typed payload (got %d)", m.Name, count)
	}
	switch m.Type {
	case "aggregate":
		if m.Aggregate == nil {
			return fmt.Errorf("metric %q aggregate payload is missing", m.Name)
		}
	case "derived":
		if m.Derived == nil {
			return fmt.Errorf("metric %q derived payload is missing", m.Name)
		}
	case "ratio":
		if m.Ratio == nil {
			return fmt.Errorf("metric %q ratio payload is missing", m.Name)
		}
	default:
		return fmt.Errorf("metric %q has unsupported type %q", m.Name, m.Type)
	}
	return nil
}

// CompiledModel is immutable semantic metadata shared by every query in a
// serving-state runtime. Expressions, named predicates, defaults, and metric
// lineage are compiled once during activation.
type CompiledModel struct {
	model    *semanticmodel.Model
	datasets map[string]CompiledDataset
	// sourceFingerprint binds the immutable executable graph to the complete
	// semantic definition used during activation. Consumers can use it to
	// reject a stale planner paired with a different manifest without
	// recompiling that definition.
	sourceFingerprint string

	// The DAG is intentionally private. Returning detached nodes prevents a
	// consumer from mutating serving-state metadata after activation.
	metrics          map[string]CompiledMetric
	topologicalOrder []string
}

// CompileModel builds the complete metric evaluation DAG after validating the
// entire semantic graph. ValidateSemanticGraph intentionally does not require
// project sources or connection credentials, so activation cannot admit a
// malformed relationship, dimension, filter, or metric definition.
func CompileModel(model *semanticmodel.Model) (*CompiledModel, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	sourceFingerprint := semanticModelFingerprint(model)
	validated := model.ExecutionSnapshot()
	if validated == nil {
		return nil, fmt.Errorf("semantic model snapshot is required")
	}
	if err := validated.ValidateSemanticGraph(); err != nil {
		return nil, fmt.Errorf("validate semantic graph: %w", err)
	}
	model = validated
	datasets, err := compileDatasets(model)
	if err != nil {
		return nil, err
	}
	compiled := &CompiledModel{
		model: model, datasets: datasets, sourceFingerprint: sourceFingerprint,
		metrics: make(map[string]CompiledMetric, len(model.Metrics)),
	}

	names := compiledMetricNames(model.Metrics)
	for _, name := range names {
		metric := model.Metrics[name]
		metric.Name = name
		node, err := compileMetricNode(model, name, metric)
		if err != nil {
			return nil, err
		}
		if err := node.validatePayload(); err != nil {
			return nil, err
		}
		compiled.metrics[name] = node
	}

	state := make(map[string]uint8, len(names)) // 1=visiting, 2=visited
	order := make([]string, 0, len(names))
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("metric dependency cycle includes %q", name)
		case 2:
			return nil
		}
		node, ok := compiled.metrics[name]
		if !ok {
			return fmt.Errorf("unknown aggregate member %q", name)
		}
		state[name] = 1
		for _, dependency := range node.Dependencies {
			if _, ok := compiled.metrics[dependency]; !ok {
				return fmt.Errorf("metric %q: unknown aggregate member %q", name, dependency)
			}
			if err := visit(dependency); err != nil {
				return fmt.Errorf("metric %q: %w", name, err)
			}
		}
		state[name] = 2
		order = append(order, name)
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	compiled.topologicalOrder = append([]string(nil), order...)

	// Roots are resolved after the topological walk so every node has a stable
	// transitive dataset set, independent of map iteration order.
	for _, name := range order {
		node := compiled.metrics[name]
		roots := map[string]struct{}{}
		if node.Aggregate != nil {
			roots[node.Aggregate.Dataset] = struct{}{}
		}
		for _, dependency := range node.Dependencies {
			dependencyNode := compiled.metrics[dependency]
			for _, root := range dependencyNode.RootDatasets {
				roots[root] = struct{}{}
			}
			for _, entry := range dependencyNode.Lineage.Entries {
				node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, entry)
			}
		}
		if len(roots) == 0 {
			return nil, fmt.Errorf("metric %q has no root dataset", name)
		}
		resolved := sortedKeys(roots)
		node.RootDatasets = resolved
		node.Lineage.Entries = cloneLineageEntries(node.Lineage.Entries)
		compiled.metrics[name] = node
	}
	// The authoring maps are intentionally discarded from the runtime graph;
	// all semantic alias resolution goes through datasets above.
	compiled.model.Tables = nil
	compiled.model.Datasets = nil
	return compiled, nil
}

func compileDatasets(model *semanticmodel.Model) (map[string]CompiledDataset, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	if len(model.Datasets) == 0 {
		return nil, fmt.Errorf("semantic model %q has no datasets", model.Name)
	}
	if len(model.Tables) != len(model.Datasets) {
		return nil, fmt.Errorf("semantic model %q dataset/table bindings must be one-to-one", model.Name)
	}
	datasets := make(map[string]CompiledDataset, len(model.Datasets))
	for alias, spec := range model.Datasets {
		table, ok := model.Tables[alias]
		if !ok {
			return nil, fmt.Errorf("semantic dataset %q has no matching runtime table", alias)
		}
		modelName := strings.TrimSpace(spec.Model)
		if modelName == "" {
			return nil, fmt.Errorf("semantic dataset %q model is required", alias)
		}
		if table.ModelName != modelName {
			return nil, fmt.Errorf("semantic dataset %q model binding %q does not match runtime table model %q", alias, modelName, table.ModelName)
		}
		copyModel := (&semanticmodel.Model{Tables: map[string]semanticmodel.Table{alias: table}}).ExecutionSnapshot()
		datasets[alias] = CompiledDataset{
			alias: alias, modelName: modelName, table: copyModel.Tables[alias],
			defaultTimeDimension: spec.DefaultTimeDimension, displayName: spec.DisplayName,
			description: spec.Description,
		}
	}
	return datasets, nil
}

// CompileDatasetBindings compiles only the executable dataset binding. It is
// used by read-model projections that receive a lightweight model fixture but
// do not need the metric DAG; serving planners should use CompileModel.
func CompileDatasetBindings(model *semanticmodel.Model) (*CompiledModel, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	snapshot := model.ExecutionSnapshot()
	datasets, err := compileDatasets(snapshot)
	if err != nil {
		return nil, err
	}
	snapshot.Tables = nil
	snapshot.Datasets = nil
	return &CompiledModel{model: snapshot, datasets: datasets, sourceFingerprint: semanticModelFingerprint(model)}, nil
}

// MatchesModel reports whether this activation-owned compiled graph was built
// from the supplied semantic definition. The comparison uses a deterministic
// fingerprint of the full execution snapshot, including datasets, tables,
// relationships, dimensions, filters, metrics, types, and time metadata. It
// does not compile or mutate the supplied model.
func (c *CompiledModel) MatchesModel(model *semanticmodel.Model) bool {
	if c == nil || model == nil || c.sourceFingerprint == "" {
		return false
	}
	return c.sourceFingerprint == semanticModelFingerprint(model)
}

// SourceFingerprint returns the deterministic semantic definition fingerprint
// retained by this activation-owned graph.
func (c *CompiledModel) SourceFingerprint() string {
	if c == nil {
		return ""
	}
	return c.sourceFingerprint
}

// SemanticModelFingerprint returns the deterministic fingerprint used to bind
// an activation-owned compiled graph to its source definition. It performs no
// query compilation and does not mutate model.
func SemanticModelFingerprint(model *semanticmodel.Model) string {
	return semanticModelFingerprint(model)
}

func semanticModelFingerprint(model *semanticmodel.Model) string {
	if model == nil {
		return ""
	}
	snapshot := model.ExecutionSnapshot()
	if snapshot == nil {
		return ""
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (c *CompiledModel) dataset(name string) (CompiledDataset, bool) {
	if c == nil {
		return CompiledDataset{}, false
	}
	dataset, ok := c.datasets[name]
	return dataset, ok
}

// Dataset resolves one semantic alias to its immutable compiled binding.
func (c *CompiledModel) Dataset(name string) (CompiledDataset, bool) {
	return c.dataset(name)
}

// ResolveDimension resolves a qualified field through its compiled dataset
// binding and returns a detached dimension descriptor.
func (c *CompiledModel) ResolveDimension(ref string) (semanticmodel.MetricDimension, error) {
	parts := strings.Split(ref, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return semanticmodel.MetricDimension{}, fmt.Errorf("field %q must be qualified as dataset.field", ref)
	}
	dataset, ok := c.dataset(parts[0])
	if !ok {
		return semanticmodel.MetricDimension{}, fmt.Errorf("unknown dataset %q", parts[0])
	}
	table := dataset.table
	dimension, ok := table.Dimensions[parts[1]]
	if !ok {
		return semanticmodel.MetricDimension{}, fmt.Errorf("unknown field %q on dataset %q", parts[1], parts[0])
	}
	dimension.Field, dimension.Table, dimension.Name = ref, parts[0], parts[1]
	return dimension, nil
}

// DatasetNames returns semantic aliases in stable order.
func (c *CompiledModel) DatasetNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.datasets))
	for name := range c.datasets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ResolvePhysicalModelName validates a model transform dependency against the
// compiled physical Model namespace. Semantic dataset aliases are not accepted
// here: aliases are only valid for selecting a dataset, while transform SQL
// must retain global physical Model identity.
func (c *CompiledModel) ResolvePhysicalModelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("model dependency is required")
	}
	if c == nil {
		return "", fmt.Errorf("compiled model is required")
	}

	found := false
	for _, aliasName := range c.DatasetNames() {
		dataset, _ := c.dataset(aliasName)
		if dataset.ModelName() != name {
			continue
		}
		// Multiple aliases bound to one physical ModelName are intentionally
		// equivalent and therefore remain one valid physical dependency.
		found = true
	}
	if found {
		return name, nil
	}
	return "", fmt.Errorf("unknown model dependency %q", name)
}

// ForEachDataset iterates detached compiled bindings in stable alias order.
func (c *CompiledModel) ForEachDataset(fn func(CompiledDataset) error) error {
	if fn == nil {
		return fmt.Errorf("dataset iterator is required")
	}
	for _, name := range c.DatasetNames() {
		dataset, _ := c.dataset(name)
		if err := fn(dataset); err != nil {
			return err
		}
	}
	return nil
}

// CompiledModel returns the activation-owned immutable semantic graph.
func (p *Planner) CompiledModel() *CompiledModel {
	if p == nil {
		return nil
	}
	return p.compiled
}

// Dataset resolves a semantic alias through the activation-owned planner.
func (p *Planner) Dataset(name string) (CompiledDataset, bool) {
	if p == nil {
		return CompiledDataset{}, false
	}
	return p.compiled.Dataset(name)
}

func (c *CompiledModel) metric(name string) (CompiledMetric, bool) {
	if c == nil {
		return CompiledMetric{}, false
	}
	node, ok := c.metrics[name]
	if !ok {
		return CompiledMetric{}, false
	}
	return cloneCompiledMetric(node), true
}

func (c *CompiledModel) metricNames() []string {
	if c == nil {
		return nil
	}
	names := make([]string, 0, len(c.metrics))
	for name := range c.metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func cloneCompiledMetric(node CompiledMetric) CompiledMetric {
	node.Dependencies = append([]string(nil), node.Dependencies...)
	node.RootDatasets = append([]string(nil), node.RootDatasets...)
	if node.Aggregate != nil {
		aggregate := *node.Aggregate
		aggregate.NamedFilters = cloneCompiledNamedFilters(aggregate.NamedFilters)
		node.Aggregate = &aggregate
	}
	if node.Derived != nil {
		derived := *node.Derived
		node.Derived = &derived
	}
	if node.Ratio != nil {
		ratio := *node.Ratio
		node.Ratio = &ratio
	}
	node.Lineage.Entries = cloneLineageEntries(node.Lineage.Entries)
	return node
}

func appendLineageEntry(values []CompiledLineageEntry, entry CompiledLineageEntry) []CompiledLineageEntry {
	for _, existing := range values {
		if existing.Role == entry.Role && existing.Reference == entry.Reference && existing.Field == entry.Field && relationshipPathSignature(existing.Path) == relationshipPathSignature(entry.Path) {
			return values
		}
	}
	entry.Path = cloneRelationships(entry.Path)
	return append(values, entry)
}

func cloneLineageEntries(values []CompiledLineageEntry) []CompiledLineageEntry {
	if values == nil {
		return nil
	}
	result := make([]CompiledLineageEntry, len(values))
	for index, entry := range values {
		result[index] = entry
		result[index].Path = cloneRelationships(entry.Path)
	}
	return result
}

func cloneCompiledNamedFilters(values []CompiledNamedFilter) []CompiledNamedFilter {
	if values == nil {
		return nil
	}
	out := make([]CompiledNamedFilter, len(values))
	for index, value := range values {
		out[index] = CompiledNamedFilter{Name: value.Name, Filter: cloneFilters([]Filter{value.Filter})[0]}
	}
	return out
}

func compiledMetricNames(metrics map[string]semanticmodel.Metric) []string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func compileMetricNode(model *semanticmodel.Model, name string, metric semanticmodel.Metric) (CompiledMetric, error) {
	metricType := metric.Type
	node := CompiledMetric{
		Name: name, Type: metricType,
		Unit: metric.Unit, Format: metric.Format,
		Label: metric.Label, Description: metric.Description, Hidden: metric.Hidden,
		Lineage: CompiledMetricLineage{},
	}
	switch metricType {
	case "aggregate":
		if strings.TrimSpace(metric.Dataset) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate dataset is required", name)
		}
		if metric.Input == nil || strings.TrimSpace(metric.Input.Field) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input is required", name)
		}
		if _, ok := model.Tables[metric.Dataset]; !ok {
			return CompiledMetric{}, fmt.Errorf("metric %q references unknown dataset %q", name, metric.Dataset)
		}
		input := semanticmodel.MetricDimension{Table: metric.Dataset}
		var err error
		input, err = model.ResolveDimension(metric.Input.Field)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input: %w", name, err)
		}
		if input.Table != metric.Dataset {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input field %q is not owned by dataset %q", name, metric.Input.Field, metric.Dataset)
		}
		aggregate := &CompiledAggregateMetric{
			Dataset: metric.Dataset, Aggregation: metric.Aggregation,
			InputField: metric.Input.Field, Empty: metric.Empty,
			TimeDimension: metric.TimeDimension,
		}
		node.Aggregate = aggregate
		if aggregate.TimeDimension == "" {
			if dataset, ok := model.Datasets[metric.Dataset]; ok {
				aggregate.TimeDimension = dataset.DefaultTimeDimension
			}
		}
		if aggregate.TimeDimension != "" {
			if err := validateMetricTimeDimension(model, name, metric.Dataset, aggregate.TimeDimension); err != nil {
				return CompiledMetric{}, err
			}
			dimension := model.Dimensions[aggregate.TimeDimension]
			binding := dimension.Bindings[metric.Dataset]
			timeDimension, err := model.ResolveDimension(binding.Field)
			if err != nil {
				return CompiledMetric{}, fmt.Errorf("metric %q time dimension %q binding: %w", name, aggregate.TimeDimension, err)
			}
			timePath, err := model.ResolveBindingPath(metric.Dataset, binding)
			if err != nil {
				return CompiledMetric{}, fmt.Errorf("metric %q time dimension %q path: %w", name, aggregate.TimeDimension, err)
			}
			node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "time", Reference: aggregate.TimeDimension, Field: timeDimension.Field, Path: timePath})
		}
		inputPath := []semanticmodel.Relationship(nil)
		inputPath, err = model.SafeRelationshipPath(metric.Dataset, input.Table)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q aggregate input path: %w", name, err)
		}
		node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "input", Reference: "input:" + name, Field: metric.Input.Field, Path: inputPath})
		if err := compileMetricWhere(model, name, metric.Dataset, metric.Where, aggregate, &node); err != nil {
			return CompiledMetric{}, err
		}
	case "derived":
		expression, err := semanticmodel.ParseExpression(metric.Expression)
		if err != nil {
			return CompiledMetric{}, fmt.Errorf("metric %q: %w", name, err)
		}
		refs := expression.References()
		if len(refs) == 0 {
			return CompiledMetric{}, fmt.Errorf("metric %q has no root dataset", name)
		}
		node.Derived = &CompiledDerivedMetric{Expression: expression}
		node.Dependencies = sortedUnique(refs)
	case "ratio":
		if strings.TrimSpace(metric.Numerator) == "" || strings.TrimSpace(metric.Denominator) == "" {
			return CompiledMetric{}, fmt.Errorf("metric %q ratio requires numerator and denominator", name)
		}
		node.Ratio = &CompiledRatioMetric{Numerator: metric.Numerator, Denominator: metric.Denominator}
		node.Dependencies = sortedUnique([]string{metric.Numerator, metric.Denominator})
	default:
		return CompiledMetric{}, fmt.Errorf("metric %q has unsupported type %q", name, metric.Type)
	}
	return node, nil
}

func validateMetricTimeDimension(model *semanticmodel.Model, metric, dataset, name string) error {
	dimension, ok := model.Dimensions[name]
	if !ok {
		return fmt.Errorf("metric %q time dimension %q is unknown", metric, name)
	}
	if dimension.Type != "date" && dimension.Type != "timestamp" {
		return fmt.Errorf("metric %q time dimension %q is not temporal", metric, name)
	}
	if _, ok := dimension.Bindings[dataset]; !ok {
		return fmt.Errorf("metric %q time dimension %q has no binding for dataset %q", metric, name, dataset)
	}
	return nil
}

func compileMetricWhere(model *semanticmodel.Model, metric, dataset string, names []string, aggregate *CompiledAggregateMetric, node *CompiledMetric) error {
	if names != nil && len(names) == 0 {
		return fmt.Errorf("metric %q aggregate where requires a non-empty list", metric)
	}
	filters := make([]Filter, 0, len(names))
	for _, name := range names {
		compiled, err := compileNamedSemanticFilters(model, []string{name})
		if err != nil {
			return fmt.Errorf("metric %q: %w", metric, err)
		}
		if len(compiled) != 1 {
			return fmt.Errorf("metric %q: semantic filter %q compiled to %d filters", metric, name, len(compiled))
		}
		filter := scopeMetricWhereFilter(compiled[0], dataset)
		if err := compileFilterLineage(model, metric, dataset, &filter, node, "filter:"+name); err != nil {
			return err
		}
		filters = append(filters, filter)
	}
	aggregate.NamedFilters = make([]CompiledNamedFilter, len(filters))
	for index, filter := range filters {
		aggregate.NamedFilters[index] = CompiledNamedFilter{Name: names[index], Filter: cloneFilters([]Filter{filter})[0]}
	}
	return nil
}

func compileFilterLineage(model *semanticmodel.Model, metric, root string, filter *Filter, node *CompiledMetric, reference string) error {
	if filter.Field != "" {
		dimension, err := model.ResolveDimension(filter.Field)
		if err != nil {
			return fmt.Errorf("metric %q filter field %q: %w", metric, filter.Field, err)
		}
		path, err := model.ResolveBindingPath(root, semanticmodel.DimensionBinding{Field: filter.Field, Path: append([]string(nil), filter.Path...)})
		if err != nil {
			return fmt.Errorf("metric %q filter field %q path: %w", metric, filter.Field, err)
		}
		node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "filter", Reference: reference, Field: filter.Field, Path: path})
		_ = dimension
	}
	for groupIndex := range filter.Groups {
		for childIndex := range filter.Groups[groupIndex].Filters {
			childReference := fmt.Sprintf("%s.groups[%d].filters[%d]", reference, groupIndex, childIndex)
			if err := compileFilterLineage(model, metric, root, &filter.Groups[groupIndex].Filters[childIndex], node, childReference); err != nil {
				return err
			}
		}
	}
	if filter.Spatial != nil {
		for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
			if field == "" {
				continue
			}
			dimension, err := model.ResolveDimension(field)
			if err != nil {
				return fmt.Errorf("metric %q filter field %q: %w", metric, field, err)
			}
			path, err := model.SafeRelationshipPath(root, dimension.Table)
			if err != nil {
				return fmt.Errorf("metric %q filter field %q path: %w", metric, field, err)
			}
			node.Lineage.Entries = appendLineageEntry(node.Lineage.Entries, CompiledLineageEntry{Role: "filter", Reference: reference, Field: field, Path: path})
		}
	}
	return nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendRelationshipFields(values []string, path []semanticmodel.Relationship) []string {
	for _, relationship := range path {
		for _, from := range []bool{true, false} {
			table, fields, err := semanticmodel.RelationshipEndpoint(relationship, from)
			if err != nil {
				continue
			}
			for _, field := range fields {
				values = appendUnique(values, table+"."+field)
			}
		}
	}
	return values
}

func cloneRelationships(values []semanticmodel.Relationship) []semanticmodel.Relationship {
	out := make([]semanticmodel.Relationship, len(values))
	for index, value := range values {
		out[index] = value
		out[index].FromFields = append([]string(nil), value.FromFields...)
		out[index].ToFields = append([]string(nil), value.ToFields...)
	}
	return out
}

func cloneColumnSchemas(values []semanticmodel.ColumnSchema) []semanticmodel.ColumnSchema {
	if values == nil {
		return nil
	}
	out := make([]semanticmodel.ColumnSchema, len(values))
	for index, value := range values {
		out[index] = value
		if value.Nullable != nil {
			nullable := *value.Nullable
			out[index].Nullable = &nullable
		}
	}
	return out
}

func cloneFilters(values []Filter) []Filter {
	out := make([]Filter, len(values))
	for index, value := range values {
		out[index] = value
		out[index].Values = append([]any(nil), value.Values...)
		out[index].Path = append([]string(nil), value.Path...)
		out[index].Groups = make([]FilterGroup, len(value.Groups))
		for groupIndex, group := range value.Groups {
			out[index].Groups[groupIndex] = FilterGroup{Filters: cloneFilters(group.Filters)}
		}
		if value.Spatial != nil {
			spatial := *value.Spatial
			spatial.Points = append([]SpatialPoint(nil), value.Spatial.Points...)
			out[index].Spatial = &spatial
		}
	}
	return out
}

type PlannerOption func(*Planner) error

func WithTableRelation(relation TableRelation) PlannerOption {
	return func(planner *Planner) error {
		if relation == nil {
			return fmt.Errorf("table relation resolver is required")
		}
		planner.tableRelation = relation
		return nil
	}
}

func NewCompiledPlanner(model *semanticmodel.Model, options ...PlannerOption) (*Planner, error) {
	compiled, err := CompileModel(model)
	if err != nil {
		return nil, err
	}
	planner := &Planner{model: compiled.model, compiled: compiled}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("planner option is required")
		}
		if err := option(planner); err != nil {
			return nil, err
		}
	}
	return planner, nil
}
