package query

// This file contains the semantic-access compiler/evaluator boundary.  It is
// deliberately independent from the legacy access-policy package: the
// authored SemanticModel contract is resolved against the typed attribute
// registry, while runtime decisions consume only effective, already-resolved
// semantic attributes.  Query consumers may use the returned predicates in a
// later planner boundary; this package does not build an authorized Plan.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticAccessCompileContext is the explicit activation context required by
// a protected SemanticModel. Registry is a complete FAI-636 snapshot, not a
// live lookup: changing the control plane cannot mutate an installed policy.
type SemanticAccessCompileContext struct {
	Registry access.SemanticAttributeRegistrySnapshot
}

// SemanticAccessEvaluationContext is the runtime input to the unified grant
// and access-filter evaluator. Attributes must have been resolved by the
// FAI-637 control-plane boundary; callers may not supply raw claims here.
type SemanticAccessEvaluationContext struct {
	RegistryState access.SemanticAttributeRegistryState
	ControlState  access.SemanticAttributeControlState
	Attributes    []access.EffectiveSemanticAttribute
}

// SemanticAccessTarget selects one governed semantic object. Dataset is
// required for a dimension because a dimension may have several bindings.
// Metric targets include every transitive metric dependency and root dataset.
type SemanticAccessTarget struct {
	Dataset   string
	Dimension string
	Metric    string
}

// CompiledSemanticAccessGrant is an immutable, typed grant condition. Values
// are canonical semanticvalue spellings; they are never interpreted as SQL.
type CompiledSemanticAccessGrant struct {
	Name                       string
	UserAttribute              string
	AttributeDefinitionID      string
	AttributeDefinitionVersion int64
	Type                       semanticvalue.Type
	AllowedValues              []string
}

// CompiledSemanticAccessFilter is the activation-owned form of one dataset
// access filter. Predicate is intentionally built at evaluation time because
// its parameter values come from the effective runtime attribute set.
type CompiledSemanticAccessFilter struct {
	Dataset                    string
	Dimension                  string
	UserAttribute              string
	Identity                   string
	AttributeDefinitionID      string
	AttributeDefinitionVersion int64
	PhysicalField              string
	Type                       semanticvalue.Type
	Shape                      access.SemanticAttributeShape
	Route                      []planir.RelationshipRoute
}

// SemanticAccessRequirements is a stable transitive grant list attached to a
// semantic member. The list is sorted and duplicate-free.
type SemanticAccessRequirements struct {
	Grants []string
}

// SemanticAccessDecision is the result of evaluating a target. Predicates are
// closed PlanIR values and therefore remain parameter-only; this result is
// not an authorized query plan and contains no SQL or renderer state.
type SemanticAccessDecision struct {
	Allowed        bool
	Grants         []string
	GrantOutcomes  []SemanticAccessGrantEvidence
	Predicates     []planir.Predicate
	AppliedFilters []SemanticAccessFilterEvidence
	Reason         string
}

// SemanticAccessGrantEvidence records the stable grant/definition identity and
// Boolean result without copying principal attribute values into evidence.
type SemanticAccessGrantEvidence struct {
	Grant                      string
	UserAttribute              string
	AttributeDefinitionID      string
	AttributeDefinitionVersion int64
	Satisfied                  bool
}

// SemanticAccessFilterEvidence is a redacted outcome projection. It binds an
// applied filter to stable dataset/attribute identities without carrying the
// effective value or its predicate parameters.
type SemanticAccessFilterEvidence struct {
	Dataset                    string
	Dimension                  string
	UserAttribute              string
	Identity                   string
	AttributeDefinitionID      string
	AttributeDefinitionVersion int64
	Applied                    bool
}

// CompiledSemanticAccessPolicy is the immutable policy projection captured at
// activation. Its maps are private; accessors return detached values so an
// installed serving generation cannot be changed through a getter.
type CompiledSemanticAccessPolicy struct {
	grants      map[string]CompiledSemanticAccessGrant
	datasets    map[string]SemanticAccessRequirements
	dimensions  map[string]map[string]SemanticAccessRequirements
	metrics     map[string]SemanticAccessRequirements
	metricRoots map[string][]string
	// dimensionDatasets and metricDatasets are activation-owned traversal
	// scopes. They include every semantic dataset touched by a compiled
	// dimension/metric route, not only the selected root dataset.
	dimensionDatasets map[string]map[string][]string
	metricDatasets    map[string][]string
	filters           map[string][]CompiledSemanticAccessFilter
	definitions       map[string]access.SemanticAttributeDefinition
	definitionsByID   map[string]access.SemanticAttributeDefinition
	registryState     access.SemanticAttributeRegistryState
	protected         bool
}

// CompileSemanticAccessPolicy compiles the runtime policy fields of model
// against one complete FAI-636 registry snapshot. It does not compile or
// expose a query plan.
func CompileSemanticAccessPolicy(model *semanticmodel.Model, registry access.SemanticAttributeRegistrySnapshot) (*CompiledSemanticAccessPolicy, error) {
	compiled, err := compileModel(model, &registry)
	if err != nil {
		return nil, err
	}
	return compiled.semanticAccess.Clone(), nil
}

// CompileModelWithSemanticAccess compiles the complete semantic model with
// the explicit registry context required by protected models.
func CompileModelWithSemanticAccess(model *semanticmodel.Model, context SemanticAccessCompileContext) (*CompiledModel, error) {
	return compileModel(model, &context.Registry)
}

// Grants returns all compiled grant definitions in stable name order.
func (policy *CompiledSemanticAccessPolicy) Grants() []CompiledSemanticAccessGrant {
	if policy == nil {
		return nil
	}
	names := make([]string, 0, len(policy.grants))
	for name := range policy.grants {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]CompiledSemanticAccessGrant, 0, len(names))
	for _, name := range names {
		grant := policy.grants[name]
		grant.AllowedValues = append([]string(nil), grant.AllowedValues...)
		result = append(result, grant)
	}
	return result
}

// Grant resolves one compiled grant by name.
func (policy *CompiledSemanticAccessPolicy) Grant(name string) (CompiledSemanticAccessGrant, bool) {
	if policy == nil {
		return CompiledSemanticAccessGrant{}, false
	}
	grant, ok := policy.grants[name]
	grant.AllowedValues = append([]string(nil), grant.AllowedValues...)
	return grant, ok
}

// DatasetRequirements returns the requirements attached to a dataset.
func (policy *CompiledSemanticAccessPolicy) DatasetRequirements(dataset string) (SemanticAccessRequirements, bool) {
	return policy.requirements(policy.datasets, dataset)
}

// DimensionRequirements returns the transitive dimension+dataset grants for a
// dimension binding.
func (policy *CompiledSemanticAccessPolicy) DimensionRequirements(dataset, dimension string) (SemanticAccessRequirements, bool) {
	if policy == nil {
		return SemanticAccessRequirements{}, false
	}
	byDataset, ok := policy.dimensions[dimension]
	if !ok {
		return SemanticAccessRequirements{}, false
	}
	requirements, ok := byDataset[dataset]
	requirements.Grants = append([]string(nil), requirements.Grants...)
	return requirements, ok
}

// MetricRequirements returns metric, dependency, and root-dataset grants in
// stable order.
func (policy *CompiledSemanticAccessPolicy) MetricRequirements(metric string) (SemanticAccessRequirements, bool) {
	return policy.requirements(policy.metrics, metric)
}

// Filters returns access filters for a dataset in stable semantic order.
func (policy *CompiledSemanticAccessPolicy) Filters(dataset string) []CompiledSemanticAccessFilter {
	if policy == nil {
		return nil
	}
	values := append([]CompiledSemanticAccessFilter(nil), policy.filters[dataset]...)
	for index := range values {
		values[index].Route = clonePlanIRRoutes(values[index].Route)
	}
	return values
}

// Clone returns a detached policy projection.
func (policy *CompiledSemanticAccessPolicy) Clone() *CompiledSemanticAccessPolicy {
	if policy == nil {
		return nil
	}
	clone := &CompiledSemanticAccessPolicy{
		grants:            make(map[string]CompiledSemanticAccessGrant, len(policy.grants)),
		datasets:          make(map[string]SemanticAccessRequirements, len(policy.datasets)),
		dimensions:        make(map[string]map[string]SemanticAccessRequirements, len(policy.dimensions)),
		metrics:           make(map[string]SemanticAccessRequirements, len(policy.metrics)),
		metricRoots:       make(map[string][]string, len(policy.metricRoots)),
		dimensionDatasets: make(map[string]map[string][]string, len(policy.dimensionDatasets)),
		metricDatasets:    make(map[string][]string, len(policy.metricDatasets)),
		filters:           make(map[string][]CompiledSemanticAccessFilter, len(policy.filters)),
		definitions:       make(map[string]access.SemanticAttributeDefinition, len(policy.definitions)),
		definitionsByID:   make(map[string]access.SemanticAttributeDefinition, len(policy.definitionsByID)),
		registryState:     policy.registryState,
		protected:         policy.protected,
	}
	for name, grant := range policy.grants {
		grant.AllowedValues = append([]string(nil), grant.AllowedValues...)
		clone.grants[name] = grant
	}
	for name, requirements := range policy.datasets {
		clone.datasets[name] = cloneRequirements(requirements)
	}
	for dimension, byDataset := range policy.dimensions {
		clone.dimensions[dimension] = make(map[string]SemanticAccessRequirements, len(byDataset))
		for dataset, requirements := range byDataset {
			clone.dimensions[dimension][dataset] = cloneRequirements(requirements)
		}
	}
	for name, requirements := range policy.metrics {
		clone.metrics[name] = cloneRequirements(requirements)
	}
	for name, roots := range policy.metricRoots {
		clone.metricRoots[name] = append([]string(nil), roots...)
	}
	for dimension, byDataset := range policy.dimensionDatasets {
		clone.dimensionDatasets[dimension] = make(map[string][]string, len(byDataset))
		for dataset, datasets := range byDataset {
			clone.dimensionDatasets[dimension][dataset] = append([]string(nil), datasets...)
		}
	}
	for name, datasets := range policy.metricDatasets {
		clone.metricDatasets[name] = append([]string(nil), datasets...)
	}
	for dataset, filters := range policy.filters {
		clone.filters[dataset] = make([]CompiledSemanticAccessFilter, len(filters))
		for index, filter := range filters {
			clone.filters[dataset][index] = filter
			clone.filters[dataset][index].Route = clonePlanIRRoutes(filter.Route)
		}
	}
	for name, definition := range policy.definitions {
		clone.definitions[name] = definition
	}
	for id, definition := range policy.definitionsByID {
		clone.definitionsByID[id] = definition
	}
	return clone
}

func (policy *CompiledSemanticAccessPolicy) requirements(values map[string]SemanticAccessRequirements, name string) (SemanticAccessRequirements, bool) {
	if policy == nil {
		return SemanticAccessRequirements{}, false
	}
	requirements, ok := values[name]
	requirements.Grants = append([]string(nil), requirements.Grants...)
	return requirements, ok
}

func cloneRequirements(value SemanticAccessRequirements) SemanticAccessRequirements {
	value.Grants = append([]string(nil), value.Grants...)
	return value
}

func compileSemanticAccessPolicy(model *semanticmodel.Model, registry access.SemanticAttributeRegistrySnapshot, compiled *CompiledModel) (*CompiledSemanticAccessPolicy, error) {
	protected := semanticAccessPolicyPresent(model)
	policy := &CompiledSemanticAccessPolicy{
		grants:            make(map[string]CompiledSemanticAccessGrant),
		datasets:          make(map[string]SemanticAccessRequirements),
		dimensions:        make(map[string]map[string]SemanticAccessRequirements),
		metrics:           make(map[string]SemanticAccessRequirements),
		metricRoots:       make(map[string][]string),
		dimensionDatasets: make(map[string]map[string][]string),
		metricDatasets:    make(map[string][]string),
		filters:           make(map[string][]CompiledSemanticAccessFilter),
		definitions:       make(map[string]access.SemanticAttributeDefinition),
		definitionsByID:   make(map[string]access.SemanticAttributeDefinition),
		registryState:     registry.State,
		protected:         protected,
	}
	grantSpecs := semanticAccessGrantSpecs(model)
	datasetSpecs := semanticAccessDatasetSpecs(model)
	dimensionSpecs := semanticAccessDimensionSpecs(model)
	metricSpecs := semanticAccessMetricSpecs(model)
	definitions, byID, err := indexSemanticAttributeRegistry(registry)
	if err != nil {
		return nil, err
	}
	if !protected {
		for dataset := range datasetSpecs {
			policy.datasets[dataset] = SemanticAccessRequirements{}
		}
		for dimension, spec := range dimensionSpecs {
			byDataset := make(map[string]SemanticAccessRequirements, len(spec.Bindings))
			for dataset := range spec.Bindings {
				byDataset[dataset] = SemanticAccessRequirements{}
				if policy.dimensionDatasets[dimension] == nil {
					policy.dimensionDatasets[dimension] = make(map[string][]string, len(spec.Bindings))
				}
				policy.dimensionDatasets[dimension][dataset] = compiledDimensionTraversal(compiled, dimension, dataset)
			}
			policy.dimensions[dimension] = byDataset
		}
		for metric := range metricSpecs {
			node, ok := compiled.metrics[metric]
			if !ok {
				return nil, fmt.Errorf("metric %q is missing from compiled metric DAG", metric)
			}
			policy.metrics[metric] = SemanticAccessRequirements{}
			policy.metricRoots[metric] = append([]string(nil), node.RootDatasets...)
			policy.metricDatasets[metric] = compiledMetricTraversal(compiled, metric)
		}
		return policy, nil
	}
	if compiled == nil {
		return nil, fmt.Errorf("compiled model is required for semantic access policy compilation")
	}
	policy.definitions, policy.definitionsByID = definitions, byID

	grantNames := make([]string, 0, len(grantSpecs))
	for name := range grantSpecs {
		grantNames = append(grantNames, name)
	}
	sort.Strings(grantNames)
	for _, name := range grantNames {
		grant, err := compileSemanticAccessGrant(name, grantSpecs[name], definitions)
		if err != nil {
			return nil, err
		}
		policy.grants[name] = grant
	}

	datasetNames := sortedModelDatasetNames(datasetSpecs)
	for _, dataset := range datasetNames {
		requirements, err := compileGrantReferences("dataset "+dataset, datasetSpecs[dataset].RequiredAccessGrants, policy.grants)
		if err != nil {
			return nil, err
		}
		policy.datasets[dataset] = SemanticAccessRequirements{Grants: requirements}
		filters, err := compileSemanticAccessFilters(model, compiled, dataset, datasetSpecs[dataset].AccessFilters, definitions)
		if err != nil {
			return nil, err
		}
		policy.filters[dataset] = filters
	}

	dimensionNames := make([]string, 0, len(dimensionSpecs))
	for name := range dimensionSpecs {
		dimensionNames = append(dimensionNames, name)
	}
	sort.Strings(dimensionNames)
	dimensionGrants := make(map[string][]string, len(dimensionNames))
	for _, dimension := range dimensionNames {
		refs, err := compileGrantReferences("dimension "+dimension, dimensionSpecs[dimension].RequiredAccessGrants, policy.grants)
		if err != nil {
			return nil, err
		}
		byDataset := make(map[string]SemanticAccessRequirements)
		for dataset := range dimensionSpecs[dimension].Bindings {
			traversal := compiledDimensionTraversal(compiled, dimension, dataset)
			grants := append([]string(nil), refs...)
			for _, traversedDataset := range traversal {
				grants = mergeSortedGrantNames(grants, policy.datasets[traversedDataset].Grants)
			}
			byDataset[dataset] = SemanticAccessRequirements{Grants: grants}
			if policy.dimensionDatasets[dimension] == nil {
				policy.dimensionDatasets[dimension] = make(map[string][]string, len(dimensionSpecs[dimension].Bindings))
			}
			policy.dimensionDatasets[dimension][dataset] = traversal
		}
		policy.dimensions[dimension] = byDataset
		dimensionGrants[dimension] = refs
	}

	metricNames := make([]string, 0, len(metricSpecs))
	for name := range metricSpecs {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	metricDependencies := make(map[string][]string, len(metricNames))
	for _, name := range metricNames {
		node, ok := compiled.metrics[name]
		if !ok {
			return nil, fmt.Errorf("metric %q is missing from compiled metric DAG", name)
		}
		metricDependencies[name] = append([]string(nil), node.Dependencies...)
	}
	metricState := make(map[string]uint8, len(metricNames))
	var metricRequirements func(string) (SemanticAccessRequirements, []string, error)
	metricRequirements = func(name string) (SemanticAccessRequirements, []string, error) {
		switch metricState[name] {
		case 1:
			return SemanticAccessRequirements{}, nil, fmt.Errorf("metric %q access requirement cycle", name)
		case 2:
			requirements := policy.metrics[name]
			return cloneRequirements(requirements), append([]string(nil), policy.metricRoots[name]...), nil
		}
		metric, ok := metricSpecs[name]
		if !ok {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("metric %q references unknown metric", name)
		}
		metricState[name] = 1
		refs, err := compileGrantReferences("metric "+name, metric.RequiredAccessGrants, policy.grants)
		if err != nil {
			return SemanticAccessRequirements{}, nil, err
		}
		roots := map[string]struct{}{}
		node, nodeOK := compiled.metrics[name]
		if !nodeOK {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("metric %q is missing from compiled metric DAG", name)
		}
		for _, dimension := range compiledMetricLineageDimensions(compiled, node) {
			refs = mergeSortedGrantNames(refs, dimensionGrants[dimension])
		}
		for _, root := range node.RootDatasets {
			roots[root] = struct{}{}
		}
		for _, dependency := range metricDependencies[name] {
			dependencyRequirements, dependencyRoots, dependencyErr := metricRequirements(dependency)
			if dependencyErr != nil {
				return SemanticAccessRequirements{}, nil, fmt.Errorf("metric %q: %w", name, dependencyErr)
			}
			refs = mergeSortedGrantNames(refs, dependencyRequirements.Grants)
			for _, root := range dependencyRoots {
				roots[root] = struct{}{}
			}
		}
		rootNames := make([]string, 0, len(roots))
		for root := range roots {
			rootNames = append(rootNames, root)
		}
		sort.Strings(rootNames)
		if len(rootNames) == 0 {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("metric %q has no root dataset", name)
		}
		traversal := compiledMetricTraversal(compiled, name)
		for _, dataset := range traversal {
			refs = mergeSortedGrantNames(refs, policy.datasets[dataset].Grants)
		}
		policy.metrics[name] = SemanticAccessRequirements{Grants: refs}
		policy.metricRoots[name] = rootNames
		policy.metricDatasets[name] = traversal
		metricState[name] = 2
		return cloneRequirements(policy.metrics[name]), append([]string(nil), rootNames...), nil
	}
	for _, name := range metricNames {
		if _, _, err := metricRequirements(name); err != nil {
			return nil, err
		}
	}
	return policy, nil
}

// appendCompiledDatasetAliases maps a compiled physical endpoint back to all
// semantic dataset aliases governed by policy. Whether callers retain a
// project-model name or one of its aliases, every alias for that model is
// included so naming ambiguity cannot select a weaker policy.
func appendCompiledDatasetAliases(compiled *CompiledModel, seen map[string]struct{}, name string) {
	if compiled == nil {
		seen[name] = struct{}{}
		return
	}
	modelName := name
	if dataset, ok := compiled.datasets[name]; ok {
		modelName = dataset.modelName
	}
	found := false
	for alias, dataset := range compiled.datasets {
		if dataset.modelName == modelName {
			seen[alias] = struct{}{}
			found = true
		}
	}
	if !found {
		seen[name] = struct{}{}
	}
}

func appendCompiledRelationshipDatasets(compiled *CompiledModel, seen map[string]struct{}, path []semanticmodel.Relationship) {
	for _, relationship := range path {
		for _, from := range []bool{true, false} {
			dataset, _, err := semanticmodel.RelationshipEndpoint(relationship, from)
			if err != nil {
				continue
			}
			appendCompiledDatasetAliases(compiled, seen, dataset)
		}
	}
}

// compiledDimensionTraversal returns the activation-selected datasets touched
// by a semantic dimension binding, including its physical endpoint and every
// relationship endpoint in the route.
func compiledDimensionTraversal(compiled *CompiledModel, dimension, dataset string) []string {
	seen := map[string]struct{}{dataset: {}}
	if compiled == nil {
		return sortedKeys(seen)
	}
	binding, ok := compiled.dimensionBindings[dimension][dataset]
	if !ok {
		return sortedKeys(seen)
	}
	appendCompiledDatasetAliases(compiled, seen, binding.Physical.Table)
	appendCompiledRelationshipDatasets(compiled, seen, binding.Path)
	return sortedKeys(seen)
}

// compiledMetricTraversal returns the activation-selected datasets touched by
// a metric's transitive roots and retained physical lineage. Metric lineage is
// already closed over the dependency DAG during CompileModel.
func compiledMetricTraversal(compiled *CompiledModel, name string) []string {
	seen := map[string]struct{}{}
	if compiled == nil {
		return nil
	}
	node, ok := compiled.metrics[name]
	if !ok {
		return nil
	}
	for _, dataset := range node.RootDatasets {
		appendCompiledDatasetAliases(compiled, seen, dataset)
	}
	if node.Aggregate != nil {
		appendCompiledDatasetAliases(compiled, seen, node.Aggregate.Dataset)
	}
	for _, entry := range node.Lineage.Entries {
		if entry.Physical.Table != "" {
			appendCompiledDatasetAliases(compiled, seen, entry.Physical.Table)
		}
		appendCompiledRelationshipDatasets(compiled, seen, entry.Path)
	}
	return sortedKeys(seen)
}

// compiledMetricLineageDimensions identifies semantic dimensions retained by
// a metric's closed lineage. References cover named time dimensions and
// semantic filter fields; physical/path matching also covers dimensions that
// share a physical field with an aggregate input or a spatial lineage entry.
func compiledMetricLineageDimensions(compiled *CompiledModel, node CompiledMetric) []string {
	if compiled == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, entry := range node.Lineage.Entries {
		if _, ok := compiled.semanticDimensions[entry.Reference]; ok {
			seen[entry.Reference] = struct{}{}
		}
		if _, ok := compiled.semanticDimensions[entry.Field]; ok {
			seen[entry.Field] = struct{}{}
		}
		for name, byDataset := range compiled.dimensionBindings {
			for _, binding := range byDataset {
				if binding.Physical.Field == "" || binding.Physical.Field != entry.Physical.Field || binding.Physical.Table != entry.Physical.Table {
					continue
				}
				if relationshipPathSignature(binding.Path) == relationshipPathSignature(entry.Path) {
					seen[name] = struct{}{}
				}
			}
		}
	}
	return sortedKeys(seen)
}

func indexSemanticAttributeRegistry(registry access.SemanticAttributeRegistrySnapshot) (map[string]access.SemanticAttributeDefinition, map[string]access.SemanticAttributeDefinition, error) {
	if registry.State.Profile != semanticvalue.Profile {
		return nil, nil, fmt.Errorf("semantic attribute registry profile %q does not match %q", registry.State.Profile, semanticvalue.Profile)
	}
	if registry.State.Revision <= 0 || registry.State.Digest == "" {
		return nil, nil, fmt.Errorf("semantic attribute registry state is invalid")
	}
	definitions := make(map[string]access.SemanticAttributeDefinition, len(registry.Definitions))
	byID := make(map[string]access.SemanticAttributeDefinition, len(registry.Definitions))
	for _, definition := range registry.Definitions {
		if err := semanticvalue.ValidateAttributeName(definition.Name); err != nil {
			return nil, nil, fmt.Errorf("semantic attribute definition %q: %w", definition.Name, err)
		}
		if definition.Profile != semanticvalue.Profile {
			return nil, nil, fmt.Errorf("semantic attribute %q profile %q does not match %q", definition.Name, definition.Profile, semanticvalue.Profile)
		}
		if definition.ID == "" || definition.DefinitionVersion <= 0 {
			return nil, nil, fmt.Errorf("semantic attribute %q has invalid identity or definition version", definition.Name)
		}
		if definition.LifecycleState != access.SemanticAttributeActive && definition.LifecycleState != access.SemanticAttributeDisabled {
			return nil, nil, fmt.Errorf("semantic attribute %q has invalid lifecycle state %q", definition.Name, definition.LifecycleState)
		}
		if !definition.Shape.Valid() {
			return nil, nil, fmt.Errorf("semantic attribute %q has invalid shape %q", definition.Name, definition.Shape)
		}
		switch definition.Type {
		case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger,
			semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		default:
			return nil, nil, fmt.Errorf("semantic attribute %q has unsupported type %q", definition.Name, definition.Type)
		}
		if _, exists := definitions[definition.Name]; exists {
			return nil, nil, fmt.Errorf("semantic attribute registry contains duplicate name %q", definition.Name)
		}
		if definition.ID != "" {
			if _, exists := byID[definition.ID]; exists {
				return nil, nil, fmt.Errorf("semantic attribute registry contains duplicate id %q", definition.ID)
			}
			byID[definition.ID] = definition
		}
		definitions[definition.Name] = definition
	}
	return definitions, byID, nil
}

func compileSemanticAccessGrant(name string, spec semanticmodel.SemanticAccessGrantSpec, definitions map[string]access.SemanticAttributeDefinition) (CompiledSemanticAccessGrant, error) {
	if err := semanticvalue.ValidateAttributeName(name); err != nil {
		return CompiledSemanticAccessGrant{}, fmt.Errorf("access grant %q: invalid name: %w", name, err)
	}
	if err := semanticvalue.ValidateAttributeName(spec.UserAttribute); err != nil {
		return CompiledSemanticAccessGrant{}, fmt.Errorf("access grant %q user attribute: %w", name, err)
	}
	definition, ok := definitions[spec.UserAttribute]
	if !ok {
		return CompiledSemanticAccessGrant{}, fmt.Errorf("access grant %q references unknown semantic attribute %q", name, spec.UserAttribute)
	}
	if !definition.Enabled || definition.LifecycleState != access.SemanticAttributeActive {
		return CompiledSemanticAccessGrant{}, fmt.Errorf("access grant %q references disabled semantic attribute %q", name, spec.UserAttribute)
	}
	values, err := canonicalGrantValues(definition.Type, spec.AllowedValues)
	if err != nil {
		return CompiledSemanticAccessGrant{}, fmt.Errorf("access grant %q allowed values: %w", name, err)
	}
	return CompiledSemanticAccessGrant{
		Name: name, UserAttribute: spec.UserAttribute,
		AttributeDefinitionID: definition.ID, AttributeDefinitionVersion: definition.DefinitionVersion,
		Type: definition.Type, AllowedValues: values,
	}, nil
}

func canonicalGrantValues(typeName semanticvalue.Type, input []any) ([]string, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("allowed values must be non-empty")
	}
	set, err := semanticvalue.CanonicalizeSet(typeName, input)
	if err != nil {
		return nil, err
	}
	values := set.Values()
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Canonical()
	}
	return result, nil
}

func compileGrantReferences(scope string, references []string, grants map[string]CompiledSemanticAccessGrant) ([]string, error) {
	if references != nil && len(references) == 0 {
		return nil, fmt.Errorf("%s required access grants must be non-empty when present", scope)
	}
	seen := make(map[string]struct{}, len(references))
	result := make([]string, 0, len(references))
	for _, name := range references {
		if name == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("%s references invalid access grant %q", scope, name)
		}
		if _, ok := grants[name]; !ok {
			return nil, fmt.Errorf("%s references unknown access grant %q", scope, name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("%s repeats access grant %q", scope, name)
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func mergeSortedGrantNames(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for _, name := range left {
		seen[name] = struct{}{}
	}
	for _, name := range right {
		seen[name] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func compileSemanticAccessFilters(model *semanticmodel.Model, compiled *CompiledModel, dataset string, specs []semanticmodel.SemanticAccessFilterSpec, definitions map[string]access.SemanticAttributeDefinition) ([]CompiledSemanticAccessFilter, error) {
	result := make([]CompiledSemanticAccessFilter, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for index, spec := range specs {
		if spec.Field == "" || spec.Field != strings.TrimSpace(spec.Field) {
			return nil, fmt.Errorf("dataset %q access filter %d field is invalid", dataset, index)
		}
		if spec.UserAttribute == "" || spec.UserAttribute != strings.TrimSpace(spec.UserAttribute) {
			return nil, fmt.Errorf("dataset %q access filter %d user attribute is invalid", dataset, index)
		}
		identity := spec.Field + "\x00" + spec.UserAttribute
		if _, ok := seen[identity]; ok {
			return nil, fmt.Errorf("dataset %q repeats access filter dimension %q and semantic attribute %q", dataset, spec.Field, spec.UserAttribute)
		}
		seen[identity] = struct{}{}
		dimension, ok := semanticAccessDimensionSpecs(model)[spec.Field]
		if !ok {
			return nil, fmt.Errorf("dataset %q access filter references unknown dimension %q", dataset, spec.Field)
		}
		_, ok = dimension.Bindings[dataset]
		if !ok {
			return nil, fmt.Errorf("dataset %q access filter dimension %q is not bound to the dataset", dataset, spec.Field)
		}
		compiledBinding, ok := compiled.DimensionBinding(spec.Field, dataset)
		if !ok {
			return nil, fmt.Errorf("dataset %q access filter dimension %q has no compiled binding", dataset, spec.Field)
		}
		physical := compiledBinding.Physical
		attribute, ok := definitions[spec.UserAttribute]
		if !ok {
			return nil, fmt.Errorf("dataset %q access filter references unknown semantic attribute %q", dataset, spec.UserAttribute)
		}
		if !attribute.Enabled || attribute.LifecycleState != access.SemanticAttributeActive {
			return nil, fmt.Errorf("dataset %q access filter references disabled semantic attribute %q", dataset, spec.UserAttribute)
		}
		fieldType, ok := semanticValueType(physical.Datatype)
		if !ok || fieldType != attribute.Type {
			return nil, fmt.Errorf("dataset %q access filter dimension %q type %q is incompatible with attribute %q type %q", dataset, spec.Field, physical.Datatype, spec.UserAttribute, attribute.Type)
		}
		convertedRoute := make([]planir.RelationshipPath, 0, len(compiledBinding.Path))
		currentDataset := dataset
		for _, relationship := range compiledBinding.Path {
			convertedRelationship := planIRRelationshipPath(relationship)
			switch {
			case convertedRelationship.FromDataset == currentDataset:
			case convertedRelationship.ToDataset == currentDataset && relationship.Cardinality == "one_to_one":
				convertedRelationship = reversePlanIRRelationshipPath(convertedRelationship)
			default:
				return nil, fmt.Errorf("dataset %q access filter dimension %q has a relationship route that does not continue from %q", dataset, spec.Field, currentDataset)
			}
			convertedRoute = append(convertedRoute, convertedRelationship)
			currentDataset = convertedRelationship.ToDataset
		}
		result = append(result, CompiledSemanticAccessFilter{
			Dataset: dataset, Dimension: spec.Field, UserAttribute: spec.UserAttribute,
			Identity:              dataset + "." + spec.Field + "@" + spec.UserAttribute,
			AttributeDefinitionID: attribute.ID, AttributeDefinitionVersion: attribute.DefinitionVersion,
			PhysicalField: physical.Field, Type: attribute.Type, Shape: attribute.Shape,
			Route: planIRRelationshipRoutes(dataset, [][]planir.RelationshipPath{convertedRoute}),
		})
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Dimension != result[right].Dimension {
			return result[left].Dimension < result[right].Dimension
		}
		return result[left].UserAttribute < result[right].UserAttribute
	})
	return result, nil
}

func semanticValueType(datatype semanticmodel.LogicalDataType) (semanticvalue.Type, bool) {
	switch datatype {
	case semanticmodel.DataTypeString:
		return semanticvalue.TypeString, true
	case semanticmodel.DataTypeBoolean:
		return semanticvalue.TypeBoolean, true
	case semanticmodel.DataTypeInteger:
		return semanticvalue.TypeInteger, true
	case semanticmodel.DataTypeDecimal:
		return semanticvalue.TypeDecimal, true
	case semanticmodel.DataTypeDate:
		return semanticvalue.TypeDate, true
	case semanticmodel.DataTypeDateTimeTZ:
		return semanticvalue.TypeTimestamp, true
	default:
		return "", false
	}
}

func sortedModelDatasetNames(datasets map[string]semanticmodel.SemanticDatasetSpec) []string {
	result := make([]string, 0, len(datasets))
	for name := range datasets {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func semanticAccessPolicyPresent(model *semanticmodel.Model) bool {
	if model == nil {
		return false
	}
	if len(semanticAccessGrantSpecs(model)) > 0 {
		return true
	}
	for _, dataset := range semanticAccessDatasetSpecs(model) {
		if dataset.RequiredAccessGrants != nil || len(dataset.AccessFilters) > 0 {
			return true
		}
	}
	for _, dimension := range semanticAccessDimensionSpecs(model) {
		if dimension.RequiredAccessGrants != nil {
			return true
		}
	}
	for _, metric := range semanticAccessMetricSpecs(model) {
		if metric.RequiredAccessGrants != nil {
			return true
		}
	}
	return false
}

func (policy *CompiledSemanticAccessPolicy) evaluate(target SemanticAccessTarget, context SemanticAccessEvaluationContext) (SemanticAccessDecision, error) {
	if policy == nil {
		return SemanticAccessDecision{}, fmt.Errorf("semantic access policy is required")
	}
	requirements, datasets, err := policy.targetRequirements(target)
	if err != nil {
		return SemanticAccessDecision{}, err
	}
	decision := SemanticAccessDecision{Grants: append([]string(nil), requirements.Grants...)}
	if !policy.protected {
		decision.Allowed = true
		return decision, nil
	}
	attributes, reason := policy.runtimeAttributes(context)
	if reason != "" {
		decision.Reason = reason
		return decision, nil
	}
	grantsSatisfied := true
	for _, grantName := range requirements.Grants {
		grant := policy.grants[grantName]
		attribute, ok := attributes[grant.UserAttribute]
		satisfied := ok && matchesGrant(grant, attribute)
		decision.GrantOutcomes = append(decision.GrantOutcomes, SemanticAccessGrantEvidence{
			Grant: grant.Name, UserAttribute: grant.UserAttribute,
			AttributeDefinitionID: grant.AttributeDefinitionID, AttributeDefinitionVersion: grant.AttributeDefinitionVersion,
			Satisfied: satisfied,
		})
		if !satisfied {
			grantsSatisfied = false
		}
	}
	if !grantsSatisfied {
		decision.Reason = "required access grant is not satisfied"
		return decision, nil
	}
	filters := make([]CompiledSemanticAccessFilter, 0)
	seenDataset := map[string]struct{}{}
	for _, dataset := range datasets {
		if _, ok := seenDataset[dataset]; ok {
			continue
		}
		seenDataset[dataset] = struct{}{}
		filters = append(filters, policy.filters[dataset]...)
	}
	sort.SliceStable(filters, func(left, right int) bool {
		if filters[left].Dataset != filters[right].Dataset {
			return filters[left].Dataset < filters[right].Dataset
		}
		if filters[left].Dimension != filters[right].Dimension {
			return filters[left].Dimension < filters[right].Dimension
		}
		return filters[left].UserAttribute < filters[right].UserAttribute
	})
	predicates := make([]planir.Predicate, 0, len(filters))
	appliedFilters := make([]SemanticAccessFilterEvidence, 0, len(filters))
	for _, filter := range filters {
		attribute, ok := attributes[filter.UserAttribute]
		if !ok || !attributeMatchesFilter(filter, attribute) {
			decision.Reason = "required access filter attribute is unavailable"
			return decision, nil
		}
		predicate, predicateErr := accessFilterPredicate(filter, attribute)
		if predicateErr != nil {
			decision.Reason = "required access filter attribute is invalid"
			return decision, nil
		}
		predicates = append(predicates, predicate)
		appliedFilters = append(appliedFilters, SemanticAccessFilterEvidence{
			Dataset: filter.Dataset, Dimension: filter.Dimension, UserAttribute: filter.UserAttribute,
			AttributeDefinitionID: filter.AttributeDefinitionID, AttributeDefinitionVersion: filter.AttributeDefinitionVersion,
			Identity: filter.Identity, Applied: true,
		})
	}
	decision.Predicates = predicates
	decision.AppliedFilters = appliedFilters
	decision.Allowed = true
	return decision, nil
}

// Evaluate applies one unified AND evaluator to grants and filters for a
// target. Denials are represented by Allowed=false; malformed target shape is
// an error because no semantic object can be selected safely.
func (policy *CompiledSemanticAccessPolicy) Evaluate(target SemanticAccessTarget, context SemanticAccessEvaluationContext) (SemanticAccessDecision, error) {
	return policy.evaluate(target, context)
}

// EvaluateDataset evaluates the dataset grant/filter boundary.
func (policy *CompiledSemanticAccessPolicy) EvaluateDataset(dataset string, context SemanticAccessEvaluationContext) (SemanticAccessDecision, error) {
	return policy.evaluate(SemanticAccessTarget{Dataset: dataset}, context)
}

// EvaluateDimension evaluates a dimension together with its selected dataset
// and every access filter on that dataset.
func (policy *CompiledSemanticAccessPolicy) EvaluateDimension(dataset, dimension string, context SemanticAccessEvaluationContext) (SemanticAccessDecision, error) {
	return policy.evaluate(SemanticAccessTarget{Dataset: dataset, Dimension: dimension}, context)
}

// EvaluateMetric evaluates a metric and all transitive dependencies.
func (policy *CompiledSemanticAccessPolicy) EvaluateMetric(metric string, context SemanticAccessEvaluationContext) (SemanticAccessDecision, error) {
	return policy.evaluate(SemanticAccessTarget{Metric: metric}, context)
}

// Allows is a convenience fail-closed boolean projection of Evaluate.
func (policy *CompiledSemanticAccessPolicy) Allows(target SemanticAccessTarget, context SemanticAccessEvaluationContext) bool {
	decision, err := policy.Evaluate(target, context)
	return err == nil && decision.Allowed
}

func (policy *CompiledSemanticAccessPolicy) targetRequirements(target SemanticAccessTarget) (SemanticAccessRequirements, []string, error) {
	if policy == nil {
		return SemanticAccessRequirements{}, nil, fmt.Errorf("semantic access policy is required")
	}
	forms := 0
	if target.Dataset != "" {
		forms++
	}
	if target.Dimension != "" {
		forms++
	}
	if target.Metric != "" {
		forms++
	}
	if forms == 0 || forms > 2 || (target.Dimension != "" && target.Dataset == "") || (target.Metric != "" && target.Dataset != "") {
		return SemanticAccessRequirements{}, nil, fmt.Errorf("semantic access target must select one dataset, one dataset dimension, or one metric")
	}
	if target.Metric != "" {
		requirements, ok := policy.metrics[target.Metric]
		if !ok {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("unknown semantic metric %q", target.Metric)
		}
		datasets := append([]string(nil), policy.metricDatasets[target.Metric]...)
		if len(datasets) == 0 {
			// Keep policies compiled before traversal metadata was introduced
			// fail-closed at the same root-dataset boundary.
			datasets = append([]string(nil), policy.metricRoots[target.Metric]...)
		}
		return cloneRequirements(requirements), datasets, nil
	}
	if target.Dimension != "" {
		byDataset, ok := policy.dimensions[target.Dimension]
		if !ok {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("unknown semantic dimension %q", target.Dimension)
		}
		requirements, ok := byDataset[target.Dataset]
		if !ok {
			return SemanticAccessRequirements{}, nil, fmt.Errorf("semantic dimension %q is not bound to dataset %q", target.Dimension, target.Dataset)
		}
		datasets := append([]string(nil), policy.dimensionDatasets[target.Dimension][target.Dataset]...)
		if len(datasets) == 0 {
			datasets = []string{target.Dataset}
		}
		return cloneRequirements(requirements), datasets, nil
	}
	requirements, ok := policy.datasets[target.Dataset]
	if !ok {
		return SemanticAccessRequirements{}, nil, fmt.Errorf("unknown semantic dataset %q", target.Dataset)
	}
	return cloneRequirements(requirements), []string{target.Dataset}, nil
}

func (policy *CompiledSemanticAccessPolicy) runtimeAttributes(context SemanticAccessEvaluationContext) (map[string]access.EffectiveSemanticAttribute, string) {
	if context.RegistryState.Profile != policy.registryState.Profile || context.RegistryState.Revision != policy.registryState.Revision || context.RegistryState.Digest != policy.registryState.Digest {
		return nil, "semantic attribute registry state does not match compiled policy"
	}
	if context.ControlState.Profile != semanticvalue.Profile {
		return nil, "semantic attribute control state is unavailable"
	}
	if context.ControlState.Revision <= 0 || context.ControlState.Digest == "" {
		return nil, "semantic attribute control state is invalid"
	}
	byName := make(map[string]access.EffectiveSemanticAttribute, len(context.Attributes))
	byID := make(map[string]struct{}, len(context.Attributes))
	for _, attribute := range context.Attributes {
		definition, ok := policy.definitions[attribute.DefinitionName]
		if !ok && attribute.DefinitionID != "" {
			definition, ok = policy.definitionsByID[attribute.DefinitionID]
		}
		if !ok || definition.Name != attribute.DefinitionName && attribute.DefinitionName != "" {
			return nil, "effective semantic attribute is unknown"
		}
		if attribute.DefinitionID == "" || definition.ID == "" || attribute.DefinitionID != definition.ID {
			return nil, "effective semantic attribute identity is invalid"
		}
		if attribute.DefinitionName == "" || attribute.DefinitionName != definition.Name || attribute.DefinitionVersion != definition.DefinitionVersion {
			return nil, "effective semantic attribute definition is stale"
		}
		if attribute.Type != definition.Type || attribute.Shape != definition.Shape || !definition.Enabled || definition.LifecycleState != access.SemanticAttributeActive {
			return nil, "effective semantic attribute type or lifecycle is invalid"
		}
		if attribute.Source != "direct" && attribute.Source != "trusted_claim" && attribute.Source != "direct+trusted_claim" {
			return nil, "effective semantic attribute source is untrusted"
		}
		canonical, digest, ok := validateEffectiveValues(attribute.Type, attribute.Shape, attribute.CanonicalValues)
		if !ok {
			return nil, "effective semantic attribute values are invalid"
		}
		if attribute.ValueDigest == "" || attribute.ValueDigest != digest {
			return nil, "effective semantic attribute digest is invalid"
		}
		if attribute.DefinitionID != "" {
			if _, exists := byID[attribute.DefinitionID]; exists {
				return nil, "effective semantic attribute source is conflicting"
			}
			byID[attribute.DefinitionID] = struct{}{}
		}
		if _, exists := byName[definition.Name]; exists {
			return nil, "effective semantic attribute source is conflicting"
		}
		attribute.DefinitionName = definition.Name
		attribute.CanonicalValues = canonical
		byName[definition.Name] = attribute
	}
	return byName, ""
}

func validateEffectiveValues(typeName semanticvalue.Type, shape access.SemanticAttributeShape, values []string) ([]string, string, bool) {
	if len(values) == 0 || len(values) > semanticvalue.MaxSetValues {
		return nil, "", false
	}
	inputs := make([]any, len(values))
	for index, value := range values {
		input, ok := canonicalInput(typeName, value)
		if !ok {
			return nil, "", false
		}
		inputs[index] = input
	}
	if shape == access.SemanticAttributeScalar {
		if len(values) != 1 {
			return nil, "", false
		}
		canonical, err := semanticvalue.Canonicalize(typeName, inputs[0])
		if err != nil || canonical.Canonical() != values[0] {
			return nil, "", false
		}
		return []string{values[0]}, canonical.Digest(), true
	}
	if shape != access.SemanticAttributeList {
		return nil, "", false
	}
	set, err := semanticvalue.CanonicalizeSet(typeName, inputs)
	if err != nil || set.Len() != len(values) {
		return nil, "", false
	}
	canonical := set.Values()
	result := make([]string, len(canonical))
	for index, value := range canonical {
		result[index] = value.Canonical()
		if result[index] != values[index] {
			return nil, "", false
		}
	}
	return result, set.Digest(), true
}

func canonicalInput(typeName semanticvalue.Type, value string) (any, bool) {
	switch typeName {
	case semanticvalue.TypeString, semanticvalue.TypeDate, semanticvalue.TypeTimestamp, semanticvalue.TypeDecimal:
		return value, true
	case semanticvalue.TypeBoolean:
		switch value {
		case "true":
			return true, true
		case "false":
			return false, true
		default:
			return nil, false
		}
	case semanticvalue.TypeInteger:
		return json.Number(value), true
	default:
		return nil, false
	}
}

func matchesGrant(grant CompiledSemanticAccessGrant, attribute access.EffectiveSemanticAttribute) bool {
	if grant.Type != attribute.Type || len(attribute.CanonicalValues) == 0 {
		return false
	}
	allowed := make(map[string]struct{}, len(grant.AllowedValues))
	for _, value := range grant.AllowedValues {
		allowed[value] = struct{}{}
	}
	for _, value := range attribute.CanonicalValues {
		if _, ok := allowed[value]; ok {
			return true
		}
	}
	return false
}

func attributeMatchesFilter(filter CompiledSemanticAccessFilter, attribute access.EffectiveSemanticAttribute) bool {
	return filter.Type == attribute.Type && filter.Shape == attribute.Shape && len(attribute.CanonicalValues) > 0
}

func accessFilterPredicate(filter CompiledSemanticAccessFilter, attribute access.EffectiveSemanticAttribute) (planir.Predicate, error) {
	values := make([]planir.Literal, len(attribute.CanonicalValues))
	for index, value := range attribute.CanonicalValues {
		literal, err := semanticPlanIRLiteral(filter.Type, value)
		if err != nil {
			return planir.Predicate{}, err
		}
		values[index] = literal
	}
	if filter.Shape == access.SemanticAttributeScalar {
		return planir.Predicate{Kind: planir.PredicateCompare, Field: filter.PhysicalField, Operator: "=", Value: values[0]}, nil
	}
	if filter.Shape == access.SemanticAttributeList {
		return planir.Predicate{Kind: planir.PredicateIn, Field: filter.PhysicalField, Values: values}, nil
	}
	return planir.Predicate{}, fmt.Errorf("unsupported semantic attribute shape %q", filter.Shape)
}

func semanticPlanIRLiteral(typeName semanticvalue.Type, value string) (planir.Literal, error) {
	switch typeName {
	case semanticvalue.TypeString, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return planir.Literal{Kind: planir.LiteralString, String: value}, nil
	case semanticvalue.TypeBoolean:
		if value != "true" && value != "false" {
			return planir.Literal{}, fmt.Errorf("invalid boolean semantic value")
		}
		return planir.Literal{Kind: planir.LiteralBool, Bool: value == "true"}, nil
	case semanticvalue.TypeInteger:
		if _, err := semanticvalue.Canonicalize(typeName, json.Number(value)); err != nil {
			return planir.Literal{}, err
		}
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberInteger, NumberText: value}, nil
	case semanticvalue.TypeDecimal:
		if _, err := semanticvalue.Canonicalize(typeName, value); err != nil {
			return planir.Literal{}, err
		}
		return planir.Literal{Kind: planir.LiteralNumber, NumberKind: planir.NumberDecimal, NumberText: value}, nil
	default:
		return planir.Literal{}, fmt.Errorf("unsupported semantic value type %q", typeName)
	}
}

func clonePlanIRPredicates(values []planir.Predicate) []planir.Predicate {
	if values == nil {
		return nil
	}
	result := make([]planir.Predicate, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Values = append([]planir.Literal(nil), value.Values...)
		result[index].Children = clonePlanIRPredicates(value.Children)
		if value.Spatial != nil {
			spatial := *value.Spatial
			spatial.Points = append([]planir.SpatialPoint(nil), value.Spatial.Points...)
			result[index].Spatial = &spatial
		}
	}
	return result
}

func clonePlanIRRoutes(values []planir.RelationshipRoute) []planir.RelationshipRoute {
	if values == nil {
		return nil
	}
	result := make([]planir.RelationshipRoute, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Edges = make([]planir.RelationshipPath, len(value.Edges))
		for edgeIndex, edge := range value.Edges {
			result[index].Edges[edgeIndex] = edge
			result[index].Edges[edgeIndex].JoinKeys = append([]planir.JoinKey(nil), edge.JoinKeys...)
		}
	}
	return result
}
