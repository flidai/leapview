package query

import (
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

type aggregateDimension struct {
	Name      string
	Alias     string
	Type      string
	Datatype  semanticmodel.LogicalDataType
	Grain     string
	Timezone  string
	Calendar  string
	WeekStart string
	Semantic  bool
	Physical  semanticmodel.MetricDimension
}

type aggregateMember struct {
	Name  string
	Alias string
	Kind  string
}

type physicalFieldBinding struct {
	Field string
	Path  []semanticmodel.Relationship
}

type pathAliasSet struct {
	BaseTable string
	ByPath    map[string]tableAlias
	Ordered   []tableAlias
}

type aggregateResolution struct {
	Dimensions []aggregateDimension
	Members    []aggregateMember
	Aggregates map[string]resolvedAggregateMetric
	// Metrics contains parsed expressions for derived metrics only. Ratios
	// remain in CompiledMetric.Ratio and are lowered as ComputeRatio PlanIR
	// nodes; they must never be reintroduced as synthesized expressions here.
	Metrics      map[string]semanticmodel.Expression
	Datasets     []string
	MultiDataset bool
	Masks        columnMaskSet
}

// planAggregate preserves dataset grain by compiling every dataset through only safe
// relationship paths, aggregating each dataset independently, and stitching the
// resulting grouped rows. Datasets are never joined to each other before their
// metrics have been reduced to the requested conformed dimensions.
func (p *Planner) planAggregate(request Request) (Plan, error) {
	resolved, err := p.resolveAggregate(request)
	if err != nil {
		return Plan{}, err
	}
	if err := p.validateAggregateFilters(request.Filters, resolved); err != nil {
		return Plan{}, err
	}
	if request.SpatialBucket != nil {
		if resolved.MultiDataset {
			return Plan{}, fmt.Errorf("spatial tile buckets require a single dataset query")
		}
		if err := validateAggregateSpatialBucket(*request.SpatialBucket, resolved.Dimensions); err != nil {
			return Plan{}, err
		}
	}
	return p.renderAggregatePlanIR(request, resolved)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (p *Planner) renderAggregatePlanIR(request Request, resolved aggregateResolution) (Plan, error) {
	irGraph, err := p.buildAggregatePlanIR(request, resolved)
	if err != nil {
		return Plan{}, err
	}
	if err := irGraph.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate aggregate plan IR: %w", err)
	}
	rendered, err := planir.RenderDuckDB(irGraph)
	if err != nil {
		return Plan{}, fmt.Errorf("render aggregate plan IR: %w", err)
	}
	lineage, err := irGraph.Dependencies()
	if err != nil {
		return Plan{}, fmt.Errorf("derive aggregate dependencies: %w", err)
	}
	mode := "single_dataset"
	if resolved.MultiDataset {
		mode = "multi_dataset"
	}
	stitchDimensions := []string{}
	if resolved.MultiDataset {
		for _, dimension := range resolved.Dimensions {
			stitchDimensions = append(stitchDimensions, dimension.Name)
		}
	}
	columnSet := map[string]bool{}
	for _, column := range rendered.Columns {
		columnSet[column] = true
	}
	return Plan{SQL: rendered.SQL, Args: rendered.Args, Columns: rendered.Columns, Mode: mode,
		Datasets: append([]string{}, resolved.Datasets...), StitchDimensions: stitchDimensions,
		PhysicalDependencies: uniqueStrings(append(append([]string(nil), lineage.Datasets...), lineage.PhysicalFields...)),
		RelationshipPaths:    lineage.RelationshipPaths, EffectiveOrdering: effectiveOrderSorts(request.Sort, columnSet), IR: irGraph}, nil
}

func (p *Planner) resolveAggregate(request Request) (aggregateResolution, error) {
	if p == nil || p.compiled == nil {
		return aggregateResolution{}, fmt.Errorf("planner is not compiled")
	}
	masks, err := columnMaskMap(request.ColumnMasks)
	if err != nil {
		return aggregateResolution{}, err
	}
	resolved := aggregateResolution{
		Aggregates: map[string]resolvedAggregateMetric{},
		Metrics:    map[string]semanticmodel.Expression{},
		Masks:      masks,
	}
	visiting := map[string]bool{}
	var addMetric func(string) error
	addAggregate := func(name string) error {
		node, ok := p.compiled.metric(name)
		if !ok || node.Aggregate == nil {
			return fmt.Errorf("metric %q is not aggregate", name)
		}
		resolvedMetric, err := p.resolvedAggregateMetric(name)
		if err != nil {
			return err
		}
		if masks.matchesMetric(name, resolvedMetric) {
			return fmt.Errorf("metric %q depends on a masked field", name)
		}
		resolved.Aggregates[name] = resolvedMetric
		return nil
	}
	addMetric = func(name string) error {
		if _, ok := resolved.Aggregates[name]; ok {
			return nil
		}
		if _, ok := resolved.Metrics[name]; ok {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("metric dependency cycle includes %q", name)
		}
		metric, ok := p.compiled.metric(name)
		if !ok {
			return fmt.Errorf("unknown metric %q", name)
		}
		if metric.Aggregate != nil {
			if err := addAggregate(name); err != nil {
				return err
			}
			return nil
		}
		visiting[name] = true
		var refs []string
		var expression semanticmodel.Expression
		if metric.Derived != nil {
			expression = metric.Derived.Expression
			refs = expression.References()
		} else if metric.Ratio != nil {
			refs = append([]string(nil), metric.Dependencies...)
		} else {
			return fmt.Errorf("metric %q has no compiled payload", name)
		}
		for _, ref := range refs {
			if metricRef, ok := p.compiled.metric(ref); ok && metricRef.Aggregate != nil {
				if err := addAggregate(ref); err != nil {
					return err
				}
				continue
			}
			if _, ok := p.compiled.metric(ref); ok {
				if err := addMetric(ref); err != nil {
					return err
				}
				continue
			}
			if err := addMetric(ref); err != nil {
				return err
			}
		}
		delete(visiting, name)
		if metric.Derived != nil {
			resolved.Metrics[name] = expression
		}
		return nil
	}

	for _, item := range request.Metrics {
		name := strings.TrimSpace(item.Field)
		if name == "" {
			return aggregateResolution{}, fmt.Errorf("selected metric is required")
		}
		alias, err := outputAlias(item)
		if err != nil {
			return aggregateResolution{}, err
		}
		metric, ok := p.compiled.metric(name)
		if ok {
			if _, masked := masks[strings.ToLower(name)]; masked {
				return aggregateResolution{}, fmt.Errorf("metric %q is masked", name)
			}
			if metric.Aggregate != nil {
				if err := addAggregate(name); err != nil {
					return aggregateResolution{}, err
				}
				resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "aggregate"})
				continue
			}
			if err := addMetric(name); err != nil {
				return aggregateResolution{}, err
			}
			resolved.Members = append(resolved.Members, aggregateMember{Name: name, Alias: alias, Kind: "metric"})
			continue
		}
		return aggregateResolution{}, fmt.Errorf("unknown metric %q", name)
	}

	datasetSet := map[string]struct{}{}
	for _, metric := range resolved.Aggregates {
		datasetSet[metric.Dataset] = struct{}{}
	}
	if request.Dataset != "" {
		if _, ok := p.compiled.dataset(request.Dataset); !ok {
			return aggregateResolution{}, fmt.Errorf("unknown dataset %q", request.Dataset)
		}
		for dataset := range datasetSet {
			if dataset != request.Dataset {
				return aggregateResolution{}, fmt.Errorf("dataset-scoped query for %q selects dependency from dataset %q", request.Dataset, dataset)
			}
		}
		datasetSet = map[string]struct{}{request.Dataset: {}}
	}

	dimensionFields := append([]Field{}, request.Dimensions...)
	if request.Time.Field != "" {
		if !allowedTimeGrain(request.Time.Grain) {
			return aggregateResolution{}, fmt.Errorf("unsupported time grain %q", request.Time.Grain)
		}
		// Request.Time is retained for callers that predate per-dimension grain,
		// but canonical requests carry grain on the dimension reference itself.
		dimensionFields = append(dimensionFields, Field{Field: request.Time.Field, Alias: request.Time.Alias, Grain: request.Time.Grain})
	}
	for index, item := range dimensionFields {
		alias, err := outputAlias(item)
		if err != nil {
			return aggregateResolution{}, err
		}
		grain := item.Grain
		// Preserve the old Request.Time positional behavior only when the
		// reference did not already carry a grain. Canonical lowering never uses
		// Request.Time, so independent temporal dimensions remain independent.
		if grain == "" && request.Time.Field != "" && index == len(dimensionFields)-1 {
			grain = request.Time.Grain
		}
		if grain != "" && !allowedTimeGrain(grain) {
			return aggregateResolution{}, fmt.Errorf("unsupported time grain %q", grain)
		}
		if dimension, ok := p.compiled.SemanticDimension(item.Field); ok {
			if grain != "" && !containsString(dimension.Grains, grain) {
				return aggregateResolution{}, fmt.Errorf("semantic dimension %q does not support grain %q", item.Field, grain)
			}
			resolved.Dimensions = append(resolved.Dimensions, aggregateDimension{
				Name: item.Field, Alias: alias, Type: dimension.Type, Datatype: dimension.Datatype, Grain: grain, Timezone: dimension.Timezone, Calendar: dimension.Calendar, WeekStart: dimension.WeekStart, Semantic: true,
			})
			continue
		}
		physical, err := p.resolveDimension(item.Field)
		if err != nil {
			return aggregateResolution{}, err
		}
		if grain != "" && physical.Type != "date" && physical.Type != "timestamp" {
			return aggregateResolution{}, fmt.Errorf("time field %q is not date or timestamp", item.Field)
		}
		resolved.Dimensions = append(resolved.Dimensions, aggregateDimension{
			Name: item.Field, Alias: alias, Type: physical.Type, Datatype: physical.Datatype, Grain: grain, Timezone: "UTC", Calendar: "gregorian", WeekStart: "sunday", Physical: physical,
		})
	}

	if len(datasetSet) == 0 {
		if len(resolved.Dimensions) == 0 {
			return aggregateResolution{}, fmt.Errorf("aggregate query requires a metric or dimension")
		}
		for _, dataset := range p.compiled.DatasetNames() {
			compatible := true
			for _, dimension := range resolved.Dimensions {
				if !dimension.Semantic {
					compatible = false
					break
				}
				if _, ok := p.compiled.DimensionBinding(dimension.Name, dataset); !ok {
					compatible = false
					break
				}
			}
			if compatible && !p.datasetSupportsInferredFilters(request.Filters, dataset) {
				compatible = false
			}
			if compatible {
				datasetSet[dataset] = struct{}{}
			}
		}
	}
	for dataset := range datasetSet {
		resolved.Datasets = append(resolved.Datasets, dataset)
	}
	sort.Strings(resolved.Datasets)
	if len(resolved.Datasets) == 0 {
		return aggregateResolution{}, fmt.Errorf("no dataset is compatible with the selected dimensions")
	}
	resolved.MultiDataset = len(resolved.Datasets) > 1
	for _, dimension := range resolved.Dimensions {
		if !dimension.Semantic {
			if resolved.MultiDataset {
				return aggregateResolution{}, fmt.Errorf("qualified local dimension %q is invalid in a multi-dataset query", dimension.Name)
			}
			if _, err := p.relationshipPath(resolved.Datasets[0], dimension.Physical.Table); err != nil {
				return aggregateResolution{}, err
			}
			continue
		}
		for _, dataset := range resolved.Datasets {
			if _, ok := p.compiled.DimensionBinding(dimension.Name, dataset); !ok {
				return aggregateResolution{}, fmt.Errorf("semantic dimension %q has no binding for dataset %q", dimension.Name, dataset)
			}
		}
	}
	return resolved, nil
}

// Dimension-only aggregates infer their participating datasets from the selected
// dimensions. Unscoped semantic filters must participate in that inference as
// well: a conformed filter cannot be applied to a dataset that has no binding for
// it. Queries with selected metrics keep their metric-owned dataset set and are
// validated normally, so a missing conformed binding remains an error there.
func (p *Planner) datasetSupportsInferredFilters(filters []Filter, dataset string) bool {
	var supports func(Filter) bool
	supports = func(filter Filter) bool {
		if filter.Field != "" && filter.Dataset == "" {
			if _, ok := p.compiled.SemanticDimension(filter.Field); ok {
				if _, bound := p.compiled.DimensionBinding(filter.Field, dataset); !bound {
					return false
				}
			}
		}
		if filter.Spatial != nil && filter.Spatial.Dataset == "" {
			for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				if _, ok := p.compiled.SemanticDimension(field); ok {
					if _, bound := p.compiled.DimensionBinding(field, dataset); !bound {
						return false
					}
				}
			}
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				if !supports(child) {
					return false
				}
			}
		}
		return true
	}
	for _, filter := range filters {
		if !supports(filter) {
			return false
		}
	}
	return true
}

func (p *Planner) validateAggregateFilters(filters []Filter, resolved aggregateResolution) error {
	datasetSet := map[string]bool{}
	for _, dataset := range resolved.Datasets {
		datasetSet[dataset] = true
	}
	for _, filter := range filters {
		scopes := []string{}
		collectField := func(field, filterDataset string) error {
			if _, semantic := p.compiled.SemanticDimension(field); semantic && filterDataset == "" {
				scopes = append(scopes, "conformed")
				return nil
			}
			dataset := filterDataset
			if dataset == "" {
				if resolved.MultiDataset {
					return fmt.Errorf("dataset-local filter %q requires dataset in a multi-dataset query", field)
				}
				dataset = resolved.Datasets[0]
			}
			if !datasetSet[dataset] {
				return fmt.Errorf("filter dataset %q is not a participating dataset", dataset)
			}
			scopes = append(scopes, dataset)
			return nil
		}
		var collect func(Filter) error
		collect = func(item Filter) error {
			if item.Field != "" {
				if err := collectField(item.Field, item.Dataset); err != nil {
					return err
				}
			}
			if item.Spatial != nil {
				if err := collectField(item.Spatial.LatitudeField, item.Spatial.Dataset); err != nil {
					return err
				}
				if err := collectField(item.Spatial.LongitudeField, item.Spatial.Dataset); err != nil {
					return err
				}
			}
			for _, group := range item.Groups {
				for _, child := range group.Filters {
					if err := collect(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := collect(filter); err != nil {
			return err
		}
		if len(scopes) == 0 {
			continue
		}
		first := scopes[0]
		for _, scope := range scopes[1:] {
			if scope != first {
				return fmt.Errorf("boolean filter group must be entirely conformed or resolve to one dataset")
			}
		}
	}
	return nil
}

func (p *Planner) datasetFilterFields(filters []Filter, resolved aggregateResolution, dataset string) ([]physicalFieldBinding, error) {
	bindings := []physicalFieldBinding{}
	var walk func(Filter) error
	walk = func(filter Filter) error {
		if filter.Spatial != nil {
			for _, ref := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
				field, path, applies, err := p.resolveDatasetFilterField(Filter{Field: ref, Dataset: filter.Spatial.Dataset}, resolved, dataset)
				if err != nil {
					return err
				}
				if applies {
					bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
				}
			}
		}
		if filter.Field != "" {
			field, path, applies, err := p.resolveDatasetFilterField(filter, resolved, dataset)
			if err != nil {
				return err
			}
			if applies {
				bindings = append(bindings, physicalFieldBinding{Field: field, Path: path})
			}
		}
		for _, group := range filter.Groups {
			for _, child := range group.Filters {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, filter := range filters {
		if err := walk(filter); err != nil {
			return nil, err
		}
	}
	return bindings, nil
}

func (p *Planner) resolveDatasetFilterField(filter Filter, resolved aggregateResolution, dataset string) (string, []semanticmodel.Relationship, bool, error) {
	if _, ok := p.compiled.SemanticDimension(filter.Field); ok {
		if filter.Dataset != "" && filter.Dataset != dataset {
			return "", nil, false, nil
		}
		binding, ok := p.compiled.DimensionBinding(filter.Field, dataset)
		if !ok {
			return "", nil, false, fmt.Errorf("semantic dimension %q has no binding for dataset %q", filter.Field, dataset)
		}
		if len(filter.Path) > 0 {
			path, err := p.compiled.ResolveBindingPath(dataset, binding.Physical.Field, filter.Path)
			return binding.Physical.Field, path, true, err
		}
		return binding.Physical.Field, binding.Path, true, nil
	}
	target := filter.Dataset
	if target == "" {
		if resolved.MultiDataset {
			return "", nil, false, fmt.Errorf("dataset-local filter %q requires dataset in a multi-dataset query", filter.Field)
		}
		target = resolved.Datasets[0]
	}
	if target != dataset {
		return "", nil, false, nil
	}
	physical, err := p.resolveDimension(filter.Field)
	if err != nil {
		return "", nil, false, err
	}
	var path []semanticmodel.Relationship
	if len(filter.Path) > 0 {
		path, err = p.compiled.ResolveBindingPath(dataset, filter.Field, filter.Path)
	} else {
		path, err = p.relationshipPath(dataset, physical.Table)
	}
	return filter.Field, path, true, err
}

func (p *Planner) aggregateDimensionBinding(dataset string, dimension aggregateDimension) (string, []semanticmodel.Relationship, error) {
	if !dimension.Semantic {
		if p == nil || p.compiled == nil {
			return "", nil, fmt.Errorf("planner is not compiled")
		}
		if binding, ok := p.compiled.FieldBinding(dataset, dimension.Name); ok {
			return binding.Physical.Field, binding.Path, nil
		}
		return "", nil, fmt.Errorf("compiled field binding for %q from dataset %q is missing", dimension.Name, dataset)
	}
	if p == nil || p.compiled == nil {
		return "", nil, fmt.Errorf("planner is not compiled")
	}
	binding, ok := p.compiled.DimensionBinding(dimension.Name, dataset)
	if !ok {
		return "", nil, fmt.Errorf("semantic dimension %q has no binding for dataset %q", dimension.Name, dataset)
	}
	return binding.Physical.Field, binding.Path, nil
}

func (p *Planner) aliasesForDataset(dataset string, bindings []physicalFieldBinding) (pathAliasSet, error) {
	aliases := pathAliasSet{
		BaseTable: dataset,
		ByPath:    map[string]tableAlias{"": {Table: dataset, Alias: "t0"}},
	}
	paths := map[string]tablePath{}
	for _, binding := range bindings {
		table, _, err := splitField(binding.Field)
		if err != nil {
			return pathAliasSet{}, err
		}
		path := binding.Path
		if path == nil && table != dataset {
			path, err = p.relationshipPath(dataset, table)
			if err != nil {
				return pathAliasSet{}, err
			}
		}
		steps := pathTables(dataset, path)
		if len(steps) != len(path) {
			return pathAliasSet{}, fmt.Errorf("invalid relationship path from %q for field %q", dataset, binding.Field)
		}
		if len(steps) == 0 && table != dataset {
			return pathAliasSet{}, fmt.Errorf("relationship path for field %q does not leave dataset %q", binding.Field, dataset)
		}
		if len(steps) > 0 && steps[len(steps)-1].Table != table {
			return pathAliasSet{}, fmt.Errorf("relationship path for field %q ends at table %q", binding.Field, steps[len(steps)-1].Table)
		}
		for _, step := range steps {
			signature := relationshipPathSignature(step.Path)
			if existing, ok := paths[signature]; ok && existing.Table != step.Table {
				return pathAliasSet{}, fmt.Errorf("relationship path %q resolves to both %q and %q", signature, existing.Table, step.Table)
			}
			paths[signature] = step
		}
	}
	orderedPaths := make([]tablePath, 0, len(paths))
	for _, path := range paths {
		orderedPaths = append(orderedPaths, path)
	}
	sort.Slice(orderedPaths, func(i, j int) bool {
		if len(orderedPaths[i].Path) != len(orderedPaths[j].Path) {
			return len(orderedPaths[i].Path) < len(orderedPaths[j].Path)
		}
		return relationshipPathSignature(orderedPaths[i].Path) < relationshipPathSignature(orderedPaths[j].Path)
	})
	for index, path := range orderedPaths {
		alias := tableAlias{Table: path.Table, Alias: fmt.Sprintf("t%d", index+1), Path: path.Path}
		aliases.ByPath[relationshipPathSignature(path.Path)] = alias
		aliases.Ordered = append(aliases.Ordered, alias)
	}
	return aliases, nil
}

func (a pathAliasSet) context(path []semanticmodel.Relationship) (map[string]tableAlias, error) {
	base, ok := a.ByPath[""]
	if !ok {
		return nil, fmt.Errorf("missing base alias for dataset %q", a.BaseTable)
	}
	context := map[string]tableAlias{a.BaseTable: base}
	steps := pathTables(a.BaseTable, path)
	if len(steps) != len(path) {
		return nil, fmt.Errorf("invalid relationship path from %q", a.BaseTable)
	}
	for _, step := range steps {
		signature := relationshipPathSignature(step.Path)
		alias, ok := a.ByPath[signature]
		if !ok {
			return nil, fmt.Errorf("missing alias for relationship path %q", signature)
		}
		context[step.Table] = alias
	}
	return context, nil
}

func relationshipPathSignature(path []semanticmodel.Relationship) string {
	ids := make([]string, 0, len(path))
	for _, relationship := range path {
		ids = append(ids, relationship.ID)
	}
	return strings.Join(ids, "/")
}

func canonicalDimensionExpr(expr, dimensionType string) string {
	sqlType := map[string]string{
		"string": "VARCHAR", "number": "DOUBLE", "boolean": "BOOLEAN", "date": "DATE", "timestamp": "TIMESTAMP",
	}[dimensionType]
	if sqlType == "" {
		return expr
	}
	return "CAST(" + expr + " AS " + sqlType + ")"
}

// applyTimeSemantics executes the authored temporal contract at the SQL
// boundary. Timezone conversion happens before truncation so a UTC instant is
// grouped by its local wall-clock date/hour; Sunday week starts are normalized
// around DuckDB's ISO-Monday DATE_TRUNC implementation.
func applyTimeSemantics(expr string, dimension aggregateDimension) string {
	if dimension.Grain == "" {
		return expr
	}
	if dimension.Datatype == semanticmodel.DataTypeDateTimeTZ && dimension.Timezone != "" {
		tz := strings.ReplaceAll(dimension.Timezone, "'", "''")
		expr = "timezone('" + tz + "', CAST(" + expr + " AS TIMESTAMPTZ))"
	}
	if dimension.Grain == "week" && dimension.WeekStart == "sunday" {
		return "DATE_TRUNC('week', " + expr + " + INTERVAL 1 DAY) - INTERVAL 1 DAY"
	}
	return "DATE_TRUNC('" + dimension.Grain + "', " + expr + ")"
}

func sortedAggregateMetricNames(metrics map[string]resolvedAggregateMetric) []string {
	names := make([]string, 0, len(metrics))
	for name := range metrics {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hasDatasetAggregates(metrics map[string]resolvedAggregateMetric, dataset string) bool {
	for _, metric := range metrics {
		if metric.Dataset == dataset {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func addPathDependencies(dependencies map[string]struct{}, path []semanticmodel.Relationship) {
	for _, relationship := range path {
		for _, field := range relationshipPhysicalFields(relationship) {
			dependencies[field] = struct{}{}
		}
	}
}
