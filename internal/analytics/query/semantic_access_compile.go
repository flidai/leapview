package query

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/semanticvalue"
)

var (
	ErrSemanticAccessPolicyInvalid = errors.New("semantic access policy is invalid")
	canonicalSHA256                = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type CompiledSemanticAccessGrant struct {
	Name                string                        `json:"name"`
	DefinitionID        string                        `json:"definitionId"`
	DefinitionVersion   int64                         `json:"definitionVersion"`
	UserAttribute       string                        `json:"userAttribute"`
	Type                semanticvalue.Type            `json:"type"`
	Shape               access.SemanticAttributeShape `json:"shape"`
	AllowedValues       []string                      `json:"-"`
	AllowedValuesDigest string                        `json:"allowedValuesDigest"`
}

type CompiledSemanticAccessFilter struct {
	Dataset           string                        `json:"dataset"`
	Dimension         string                        `json:"dimension"`
	Field             string                        `json:"field"`
	DefinitionID      string                        `json:"definitionId"`
	DefinitionVersion int64                         `json:"definitionVersion"`
	UserAttribute     string                        `json:"userAttribute"`
	Type              semanticvalue.Type            `json:"type"`
	Shape             access.SemanticAttributeShape `json:"shape"`
}

type CompiledSemanticDatasetAccess struct {
	Dataset              string                         `json:"dataset"`
	RequiredAccessGrants []string                       `json:"requiredAccessGrants"`
	AccessFilters        []CompiledSemanticAccessFilter `json:"accessFilters"`
}

type CompiledSemanticMemberAccess struct {
	Member               string   `json:"member"`
	Datasets             []string `json:"datasets"`
	RequiredAccessGrants []string `json:"requiredAccessGrants"`
}

// CompiledSemanticAccessPolicy is target-qualified, immutable policy metadata.
// It owns no principal values and performs no planner placement; FAI-641 will
// consume its evaluated predicates when constructing security barriers.
type CompiledSemanticAccessPolicy struct {
	profile            string
	targetInstanceID   string
	semanticModelID    string
	semanticGeneration string
	registry           access.SemanticAttributeRegistryState
	digest             string
	definitionDigest   string
	canonical          []byte
	grants             map[string]CompiledSemanticAccessGrant
	datasets           map[string]CompiledSemanticDatasetAccess
	dimensions         map[string]map[string]CompiledSemanticMemberAccess
	metrics            map[string]CompiledSemanticMemberAccess
}

func (policy *CompiledSemanticAccessPolicy) Profile() string {
	if policy == nil {
		return ""
	}
	return policy.profile
}

func (policy *CompiledSemanticAccessPolicy) SemanticModelID() string {
	if policy == nil {
		return ""
	}
	return policy.semanticModelID
}

func (policy *CompiledSemanticAccessPolicy) TargetInstanceID() string {
	if policy == nil {
		return ""
	}
	return policy.targetInstanceID
}

func (policy *CompiledSemanticAccessPolicy) SemanticGeneration() string {
	if policy == nil {
		return ""
	}
	return policy.semanticGeneration
}

func (policy *CompiledSemanticAccessPolicy) RegistryState() access.SemanticAttributeRegistryState {
	if policy == nil {
		return access.SemanticAttributeRegistryState{}
	}
	return policy.registry
}

func (policy *CompiledSemanticAccessPolicy) Digest() string {
	if policy == nil {
		return ""
	}
	return policy.digest
}

// DefinitionDigest is the generation-independent identity of the compiled
// policy definition. Activation approval binds this digest before a serving
// generation exists; the final fence recomputes it from the generation-bound
// policy before cutover. Digest remains generation-qualified for request and
// cache isolation.
func (policy *CompiledSemanticAccessPolicy) DefinitionDigest() string {
	if policy == nil {
		return ""
	}
	return policy.definitionDigest
}

func (policy *CompiledSemanticAccessPolicy) Canonical() []byte {
	if policy == nil {
		return nil
	}
	return append([]byte(nil), policy.canonical...)
}

func (policy *CompiledSemanticAccessPolicy) Grant(name string) (CompiledSemanticAccessGrant, bool) {
	if policy == nil {
		return CompiledSemanticAccessGrant{}, false
	}
	grant, ok := policy.grants[name]
	grant.AllowedValues = append([]string(nil), grant.AllowedValues...)
	return grant, ok
}

func (policy *CompiledSemanticAccessPolicy) Dataset(name string) (CompiledSemanticDatasetAccess, bool) {
	if policy == nil {
		return CompiledSemanticDatasetAccess{}, false
	}
	dataset, ok := policy.datasets[name]
	dataset.RequiredAccessGrants = append([]string(nil), dataset.RequiredAccessGrants...)
	dataset.AccessFilters = append([]CompiledSemanticAccessFilter(nil), dataset.AccessFilters...)
	return dataset, ok
}

func (policy *CompiledSemanticAccessPolicy) Dimension(name, dataset string) (CompiledSemanticMemberAccess, bool) {
	if policy == nil {
		return CompiledSemanticMemberAccess{}, false
	}
	member, ok := policy.dimensions[name][dataset]
	member.Datasets = append([]string(nil), member.Datasets...)
	member.RequiredAccessGrants = append([]string(nil), member.RequiredAccessGrants...)
	return member, ok
}

func (policy *CompiledSemanticAccessPolicy) Metric(name string) (CompiledSemanticMemberAccess, bool) {
	if policy == nil {
		return CompiledSemanticMemberAccess{}, false
	}
	member, ok := policy.metrics[name]
	member.Datasets = append([]string(nil), member.Datasets...)
	member.RequiredAccessGrants = append([]string(nil), member.RequiredAccessGrants...)
	return member, ok
}

// CompileSemanticAccessPolicy qualifies the portable authored policy against
// one verified target registry snapshot. The query model is supplied
// separately so stale semantic lineage cannot be paired with a newer policy.
func CompileSemanticAccessPolicy(targetInstanceID, modelID, semanticGeneration string, model *semanticmodel.Model, compiled *CompiledModel, registry access.SemanticAttributeRegistrySnapshot) (*CompiledSemanticAccessPolicy, error) {
	if err := validateSemanticIdentity("target instance id", targetInstanceID); err != nil {
		return nil, err
	}
	if err := validateSemanticIdentity("semantic model id", modelID); err != nil {
		return nil, err
	}
	if err := validateSemanticIdentity("semantic generation", semanticGeneration); err != nil {
		return nil, err
	}
	if model == nil || compiled == nil || !compiled.MatchesModel(model) {
		return nil, fmt.Errorf("%w: semantic model and compiled lineage are missing or inconsistent", ErrSemanticAccessPolicyInvalid)
	}
	if err := validateRegistryState(registry.State); err != nil {
		return nil, err
	}
	registryDigest, err := access.SemanticAttributeRegistryDigest(registry.State.Profile, registry.Definitions)
	if err != nil || registryDigest != registry.State.Digest {
		return nil, fmt.Errorf("%w: registry snapshot digest does not match definitions", ErrSemanticAccessPolicyInvalid)
	}
	if err := access.ValidateSemanticAttributeRegistrySnapshot(registry); err != nil {
		return nil, fmt.Errorf("%w: registry snapshot is structurally invalid: %v", ErrSemanticAccessPolicyInvalid, err)
	}
	definitions, err := semanticAttributeDefinitions(registry.Definitions)
	if err != nil {
		return nil, err
	}
	policy := &CompiledSemanticAccessPolicy{
		profile: semanticvalue.Profile, targetInstanceID: targetInstanceID, semanticModelID: modelID, semanticGeneration: semanticGeneration,
		registry: registry.State, grants: map[string]CompiledSemanticAccessGrant{}, datasets: map[string]CompiledSemanticDatasetAccess{},
		dimensions: map[string]map[string]CompiledSemanticMemberAccess{}, metrics: map[string]CompiledSemanticMemberAccess{},
	}
	if err := policy.compileGrants(model.AccessPolicy, definitions); err != nil {
		return nil, err
	}
	if err := policy.compileDatasets(model, compiled, definitions); err != nil {
		return nil, err
	}
	if err := policy.compileDimensions(model, compiled); err != nil {
		return nil, err
	}
	if err := policy.compileMetrics(model, compiled); err != nil {
		return nil, err
	}
	canonical, err := policy.canonicalBytes()
	if err != nil {
		return nil, err
	}
	policy.canonical = canonical
	policy.digest = semanticAccessDigest(canonical)
	definitionCanonical, err := policy.canonicalBytesForGeneration("")
	if err != nil {
		return nil, err
	}
	policy.definitionDigest = semanticAccessDigest(definitionCanonical)
	return policy, nil
}

// SemanticAccessActivationPolicyIdentity names both identities of the policy
// compiled for activation. DefinitionDigest is approval-stable before a
// generation exists; GenerationDigest is the exact request/cache identity of
// the deployed generation.
type SemanticAccessActivationPolicyIdentity struct {
	DefinitionDigest string
	GenerationDigest string
}

// QualifySemanticAccessActivation is the deployment-facing, value-free
// compiler boundary. It proves that the exact authored model and registry can
// produce the qualified FAI-639 policy without exposing compiler internals or
// principal evidence to activation orchestration.
func QualifySemanticAccessActivation(targetInstanceID, modelID, semanticGeneration string, model *semanticmodel.Model, compiled *CompiledModel, registry access.SemanticAttributeRegistrySnapshot) (SemanticAccessActivationPolicyIdentity, error) {
	policy, err := CompileSemanticAccessPolicy(targetInstanceID, modelID, semanticGeneration, model, compiled, registry)
	if err != nil {
		return SemanticAccessActivationPolicyIdentity{}, err
	}
	return SemanticAccessActivationPolicyIdentity{DefinitionDigest: policy.DefinitionDigest(), GenerationDigest: policy.Digest()}, nil
}

func (policy *CompiledSemanticAccessPolicy) compileGrants(authored semanticmodel.SemanticAccessPolicy, definitions map[string]access.SemanticAttributeDefinition) error {
	for _, name := range sortedStringKeys(authored.AccessGrants) {
		spec := authored.AccessGrants[name]
		if err := semanticvalue.ValidateAttributeName(name); err != nil {
			return fmt.Errorf("%w: access grant name %q is invalid", ErrSemanticAccessPolicyInvalid, name)
		}
		if err := semanticvalue.ValidateAttributeName(spec.UserAttribute); err != nil {
			return fmt.Errorf("%w: access grant %q attribute name is invalid", ErrSemanticAccessPolicyInvalid, name)
		}
		definition, ok := definitions[spec.UserAttribute]
		if !ok {
			return fmt.Errorf("%w: access grant %q references unknown attribute %q", ErrSemanticAccessPolicyInvalid, name, spec.UserAttribute)
		}
		if err := validateReferencedDefinition(definition); err != nil {
			return fmt.Errorf("%w: access grant %q: %v", ErrSemanticAccessPolicyInvalid, name, err)
		}
		inputs := make([]any, len(spec.AllowedValues))
		for index, literal := range spec.AllowedValues {
			value, err := literal.Value()
			if err != nil {
				return fmt.Errorf("%w: access grant %q allowed value %d: %v", ErrSemanticAccessPolicyInvalid, name, index, err)
			}
			inputs[index] = value
		}
		set, err := semanticvalue.CanonicalizeSet(definition.Type, inputs)
		if err != nil {
			return fmt.Errorf("%w: access grant %q allowed values: %v", ErrSemanticAccessPolicyInvalid, name, err)
		}
		values := set.Values()
		if len(values) != len(inputs) {
			return fmt.Errorf("%w: access grant %q allowed values contain canonical duplicates", ErrSemanticAccessPolicyInvalid, name)
		}
		canonical := make([]string, len(values))
		for index := range values {
			canonical[index] = values[index].Canonical()
		}
		policy.grants[name] = CompiledSemanticAccessGrant{Name: name, DefinitionID: definition.ID, DefinitionVersion: definition.DefinitionVersion,
			UserAttribute: definition.Name, Type: definition.Type, Shape: definition.Shape, AllowedValues: canonical, AllowedValuesDigest: set.Digest()}
	}
	return nil
}

func (policy *CompiledSemanticAccessPolicy) compileDatasets(model *semanticmodel.Model, compiled *CompiledModel, definitions map[string]access.SemanticAttributeDefinition) error {
	for _, name := range compiled.DatasetNames() {
		authored := model.AccessPolicy.Datasets[name]
		if err := policy.validateAuthoredRequirements("dataset", name, authored.RequiredAccessGrants); err != nil {
			return err
		}
		dataset := CompiledSemanticDatasetAccess{Dataset: name, RequiredAccessGrants: sortedUnique(authored.RequiredAccessGrants)}
		filters := append([]semanticmodel.SemanticAccessFilterSpec(nil), authored.AccessFilters...)
		sort.Slice(filters, func(i, j int) bool {
			if filters[i].Field != filters[j].Field {
				return filters[i].Field < filters[j].Field
			}
			return filters[i].UserAttribute < filters[j].UserAttribute
		})
		for _, filter := range filters {
			definition, ok := definitions[filter.UserAttribute]
			if !ok {
				return fmt.Errorf("%w: dataset %q access filter references unknown attribute %q", ErrSemanticAccessPolicyInvalid, name, filter.UserAttribute)
			}
			if err := validateReferencedDefinition(definition); err != nil {
				return fmt.Errorf("%w: dataset %q access filter: %v", ErrSemanticAccessPolicyInvalid, name, err)
			}
			dimension, ok := compiled.SemanticDimension(filter.Field)
			if !ok {
				return fmt.Errorf("%w: dataset %q access filter references unknown semantic dimension %q", ErrSemanticAccessPolicyInvalid, name, filter.Field)
			}
			binding, ok := compiled.DimensionBinding(filter.Field, name)
			if !ok || len(binding.Path) != 0 || binding.Physical.Table != name {
				return fmt.Errorf("%w: dataset %q access filter dimension %q is not directly bound to that dataset", ErrSemanticAccessPolicyInvalid, name, filter.Field)
			}
			if !semanticAccessTypeCompatible(definition.Type, dimension.Datatype) {
				return fmt.Errorf("%w: dataset %q access filter dimension %q datatype %q is incompatible with attribute %q type %q", ErrSemanticAccessPolicyInvalid, name, filter.Field, dimension.Datatype, definition.Name, definition.Type)
			}
			dataset.AccessFilters = append(dataset.AccessFilters, CompiledSemanticAccessFilter{Dataset: name, Dimension: filter.Field, Field: binding.Physical.Field,
				DefinitionID: definition.ID, DefinitionVersion: definition.DefinitionVersion, UserAttribute: definition.Name, Type: definition.Type, Shape: definition.Shape})
		}
		policy.datasets[name] = dataset
	}
	for name := range model.AccessPolicy.Datasets {
		if _, ok := policy.datasets[name]; !ok {
			return fmt.Errorf("%w: access policy references unknown dataset %q", ErrSemanticAccessPolicyInvalid, name)
		}
	}
	return nil
}

func (policy *CompiledSemanticAccessPolicy) compileDimensions(model *semanticmodel.Model, compiled *CompiledModel) error {
	for name := range model.AccessPolicy.Dimensions {
		if _, ok := model.Dimensions[name]; !ok {
			return fmt.Errorf("%w: access policy references unknown dimension %q", ErrSemanticAccessPolicyInvalid, name)
		}
	}
	for _, name := range sortedStringKeys(model.Dimensions) {
		direct := model.AccessPolicy.Dimensions[name]
		if err := policy.validateAuthoredRequirements("dimension", name, direct); err != nil {
			return err
		}
		bindings := model.Dimensions[name].Bindings
		for _, dataset := range sortedStringKeys(bindings) {
			required := append([]string(nil), direct...)
			required = append(required, policy.datasets[dataset].RequiredAccessGrants...)
			datasets := []string{dataset}
			if binding, ok := compiled.DimensionBinding(name, dataset); ok {
				required = append(required, policy.requirementsForPath(binding.Path)...)
				datasets = append(datasets, semanticAccessPathDatasets(binding.Path)...)
			}
			if policy.dimensions[name] == nil {
				policy.dimensions[name] = map[string]CompiledSemanticMemberAccess{}
			}
			policy.dimensions[name][dataset] = CompiledSemanticMemberAccess{Member: name, Datasets: sortedUnique(datasets), RequiredAccessGrants: sortedUnique(required)}
		}
	}
	return nil
}

func (policy *CompiledSemanticAccessPolicy) compileMetrics(model *semanticmodel.Model, compiled *CompiledModel) error {
	for name := range model.AccessPolicy.Metrics {
		if _, ok := compiled.metrics[name]; !ok {
			return fmt.Errorf("%w: access policy references unknown metric %q", ErrSemanticAccessPolicyInvalid, name)
		}
		if err := policy.validateAuthoredRequirements("metric", name, model.AccessPolicy.Metrics[name]); err != nil {
			return err
		}
	}
	visiting := map[string]bool{}
	var compile func(string) (CompiledSemanticMemberAccess, error)
	compile = func(name string) (CompiledSemanticMemberAccess, error) {
		if member, ok := policy.metrics[name]; ok {
			return member, nil
		}
		if visiting[name] {
			return CompiledSemanticMemberAccess{}, fmt.Errorf("%w: metric dependency cycle at %q", ErrSemanticAccessPolicyInvalid, name)
		}
		metric, ok := compiled.metric(name)
		if !ok {
			return CompiledSemanticMemberAccess{}, fmt.Errorf("%w: unknown metric %q", ErrSemanticAccessPolicyInvalid, name)
		}
		visiting[name] = true
		required := append([]string(nil), model.AccessPolicy.Metrics[name]...)
		datasets := append([]string(nil), metric.RootDatasets...)
		for _, dataset := range metric.RootDatasets {
			required = append(required, policy.datasets[dataset].RequiredAccessGrants...)
		}
		for _, lineage := range metric.Lineage.Entries {
			required = append(required, policy.requirementsForPath(lineage.Path)...)
			datasets = append(datasets, semanticAccessPathDatasets(lineage.Path)...)
		}
		if metric.Aggregate != nil && metric.Aggregate.TimeDimension != "" {
			if dimension, ok := policy.dimensions[metric.Aggregate.TimeDimension][metric.Aggregate.Dataset]; ok {
				required = append(required, dimension.RequiredAccessGrants...)
				datasets = append(datasets, dimension.Datasets...)
			}
		}
		if metric.Aggregate != nil {
			for _, filterName := range model.Metrics[name].Where {
				for _, dimensionName := range semanticFilterDimensionReferences(model.Filters[filterName], model.Dimensions, metric.Aggregate.Dataset) {
					if dimension, ok := policy.dimensions[dimensionName][metric.Aggregate.Dataset]; ok {
						required = append(required, dimension.RequiredAccessGrants...)
						datasets = append(datasets, dimension.Datasets...)
					}
				}
			}
		}
		for _, dependency := range metric.Dependencies {
			member, err := compile(dependency)
			if err != nil {
				return CompiledSemanticMemberAccess{}, err
			}
			required = append(required, member.RequiredAccessGrants...)
			datasets = append(datasets, member.Datasets...)
		}
		delete(visiting, name)
		if err := policy.validateRequirements("metric", name, required); err != nil {
			return CompiledSemanticMemberAccess{}, err
		}
		member := CompiledSemanticMemberAccess{Member: name, Datasets: sortedUnique(datasets), RequiredAccessGrants: sortedUnique(required)}
		policy.metrics[name] = member
		return member, nil
	}
	for _, name := range compiled.metricNames() {
		if _, err := compile(name); err != nil {
			return err
		}
	}
	return nil
}

func (policy *CompiledSemanticAccessPolicy) requirementsForPath(path []semanticmodel.Relationship) []string {
	var required []string
	for _, relationship := range path {
		required = append(required, policy.datasets[relationship.FromDataset].RequiredAccessGrants...)
		required = append(required, policy.datasets[relationship.ToDataset].RequiredAccessGrants...)
	}
	return required
}

func semanticAccessPathDatasets(path []semanticmodel.Relationship) []string {
	var datasets []string
	for _, relationship := range path {
		datasets = append(datasets, relationship.FromDataset, relationship.ToDataset)
	}
	return sortedUnique(datasets)
}

func (policy *CompiledSemanticAccessPolicy) validateRequirements(kind, name string, required []string) error {
	for _, grant := range required {
		if _, ok := policy.grants[grant]; !ok {
			return fmt.Errorf("%w: %s %q references unknown access grant %q", ErrSemanticAccessPolicyInvalid, kind, name, grant)
		}
	}
	return nil
}

func (policy *CompiledSemanticAccessPolicy) validateAuthoredRequirements(kind, name string, required []string) error {
	if required != nil && len(required) == 0 {
		return fmt.Errorf("%w: %s %q has an empty required grant list", ErrSemanticAccessPolicyInvalid, kind, name)
	}
	seen := make(map[string]struct{}, len(required))
	for _, grant := range required {
		if _, duplicate := seen[grant]; duplicate {
			return fmt.Errorf("%w: %s %q repeats access grant %q", ErrSemanticAccessPolicyInvalid, kind, name, grant)
		}
		seen[grant] = struct{}{}
	}
	return policy.validateRequirements(kind, name, required)
}

func semanticAttributeDefinitions(values []access.SemanticAttributeDefinition) (map[string]access.SemanticAttributeDefinition, error) {
	byName := make(map[string]access.SemanticAttributeDefinition, len(values))
	byID := make(map[string]struct{}, len(values))
	for _, definition := range values {
		if err := semanticvalue.ValidateAttributeName(definition.Name); err != nil {
			return nil, fmt.Errorf("%w: registry definition name: %v", ErrSemanticAccessPolicyInvalid, err)
		}
		if _, duplicate := byName[definition.Name]; duplicate {
			return nil, fmt.Errorf("%w: registry contains duplicate attribute name %q", ErrSemanticAccessPolicyInvalid, definition.Name)
		}
		if definition.ID == "" {
			return nil, fmt.Errorf("%w: registry attribute %q has no stable id", ErrSemanticAccessPolicyInvalid, definition.Name)
		}
		if _, duplicate := byID[definition.ID]; duplicate {
			return nil, fmt.Errorf("%w: registry contains duplicate attribute id %q", ErrSemanticAccessPolicyInvalid, definition.ID)
		}
		byName[definition.Name] = definition
		byID[definition.ID] = struct{}{}
	}
	return byName, nil
}

func validateReferencedDefinition(definition access.SemanticAttributeDefinition) error {
	if definition.Profile != semanticvalue.Profile || definition.DefinitionVersion <= 0 {
		return fmt.Errorf("attribute %q has incompatible profile or version", definition.Name)
	}
	if !definition.Enabled || definition.LifecycleState != access.SemanticAttributeActive {
		return fmt.Errorf("attribute %q is disabled", definition.Name)
	}
	if !definition.Shape.Valid() || !semanticAccessTypeSupported(definition.Type) {
		return fmt.Errorf("attribute %q has unsupported type or shape", definition.Name)
	}
	owner := definition.Metadata.Owner
	if !owner.Kind.Valid() || owner.ID != strings.TrimSpace(owner.ID) ||
		(owner.Kind == access.SemanticAttributeOwnerInstance && owner.ID != "") ||
		(owner.Kind != access.SemanticAttributeOwnerInstance && owner.ID == "") {
		return fmt.Errorf("attribute %q has invalid stewardship ownership", definition.Name)
	}
	return nil
}

func semanticAccessTypeSupported(value semanticvalue.Type) bool {
	switch value {
	case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger, semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return true
	default:
		return false
	}
}

func semanticAccessTypeCompatible(attribute semanticvalue.Type, dimension semanticmodel.LogicalDataType) bool {
	return attribute == semanticvalue.TypeString && dimension == semanticmodel.DataTypeString ||
		attribute == semanticvalue.TypeBoolean && dimension == semanticmodel.DataTypeBoolean ||
		attribute == semanticvalue.TypeInteger && dimension == semanticmodel.DataTypeInteger ||
		attribute == semanticvalue.TypeDecimal && dimension == semanticmodel.DataTypeDecimal ||
		attribute == semanticvalue.TypeDate && dimension == semanticmodel.DataTypeDate ||
		attribute == semanticvalue.TypeTimestamp && dimension == semanticmodel.DataTypeDateTimeTZ
}

func validateRegistryState(state access.SemanticAttributeRegistryState) error {
	if state.Profile != semanticvalue.Profile || state.Revision < 0 || !canonicalSHA256.MatchString(state.Digest) {
		return fmt.Errorf("%w: registry state identity is invalid", ErrSemanticAccessPolicyInvalid)
	}
	return nil
}

func validateSemanticIdentity(label, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is required and must be canonical", ErrSemanticAccessPolicyInvalid, label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%w: %s contains a control character", ErrSemanticAccessPolicyInvalid, label)
		}
	}
	return nil
}

type semanticAccessPolicyWire struct {
	Profile            string                          `json:"profile"`
	TargetInstanceID   string                          `json:"targetInstanceId"`
	SemanticModelID    string                          `json:"semanticModelId"`
	SemanticGeneration string                          `json:"semanticGeneration"`
	RegistryProfile    string                          `json:"registryProfile"`
	RegistryRevision   int64                           `json:"registryRevision"`
	RegistryDigest     string                          `json:"registryDigest"`
	Grants             []CompiledSemanticAccessGrant   `json:"grants"`
	Datasets           []CompiledSemanticDatasetAccess `json:"datasets"`
	Dimensions         []CompiledSemanticMemberAccess  `json:"dimensions"`
	Metrics            []CompiledSemanticMemberAccess  `json:"metrics"`
}

func semanticFilterDimensionReferences(filter semanticmodel.SemanticFilterSpec, dimensions map[string]semanticmodel.SemanticDimension, dataset string) []string {
	result := []string{}
	if _, ok := dimensions[filter.Field]; ok {
		result = append(result, filter.Field)
	}
	for name, dimension := range dimensions {
		if binding, ok := dimension.Bindings[dataset]; ok && binding.Field == filter.Field && sameStringSlice(binding.Path, filter.Path) {
			result = append(result, name)
		}
	}
	for _, child := range filter.All {
		result = append(result, semanticFilterDimensionReferences(child, dimensions, dataset)...)
	}
	for _, child := range filter.Any {
		result = append(result, semanticFilterDimensionReferences(child, dimensions, dataset)...)
	}
	if filter.Not != nil {
		result = append(result, semanticFilterDimensionReferences(*filter.Not, dimensions, dataset)...)
	}
	return sortedUnique(result)
}

func (policy *CompiledSemanticAccessPolicy) canonicalBytes() ([]byte, error) {
	return policy.canonicalBytesForGeneration(policy.semanticGeneration)
}

func (policy *CompiledSemanticAccessPolicy) canonicalBytesForGeneration(semanticGeneration string) ([]byte, error) {
	wire := semanticAccessPolicyWire{Profile: policy.profile, TargetInstanceID: policy.targetInstanceID, SemanticModelID: policy.semanticModelID,
		SemanticGeneration: semanticGeneration, RegistryProfile: policy.registry.Profile,
		RegistryRevision: policy.registry.Revision, RegistryDigest: policy.registry.Digest}
	for _, name := range sortedStringKeys(policy.grants) {
		grant := policy.grants[name]
		grant.AllowedValues = append([]string(nil), grant.AllowedValues...)
		wire.Grants = append(wire.Grants, grant)
	}
	for _, name := range sortedStringKeys(policy.datasets) {
		dataset := policy.datasets[name]
		dataset.RequiredAccessGrants = append([]string(nil), dataset.RequiredAccessGrants...)
		dataset.AccessFilters = append([]CompiledSemanticAccessFilter(nil), dataset.AccessFilters...)
		wire.Datasets = append(wire.Datasets, dataset)
	}
	for _, name := range sortedStringKeys(policy.dimensions) {
		for _, dataset := range sortedStringKeys(policy.dimensions[name]) {
			wire.Dimensions = append(wire.Dimensions, policy.dimensions[name][dataset])
		}
	}
	for _, name := range sortedStringKeys(policy.metrics) {
		wire.Metrics = append(wire.Metrics, policy.metrics[name])
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("%w: encode compiled policy: %v", ErrSemanticAccessPolicyInvalid, err)
	}
	return encoded, nil
}

func semanticAccessDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sortedStringKeys[V any](values map[string]V) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
