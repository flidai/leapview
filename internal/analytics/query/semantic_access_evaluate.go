package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/semanticvalue"
)

var ErrSemanticAccessSnapshotInvalid = errors.New("semantic access attribute snapshot is invalid")

// SemanticAccessAuthority is the current registry/control identity observed at
// authorization time. It is distinct from the identity captured with effective
// values so a stale or mixed read fails closed.
type SemanticAccessAuthority struct {
	InstanceID string
	Registry   access.SemanticAttributeRegistrySnapshot
	Control    access.SemanticAttributeControlSnapshot
	ObservedAt time.Time
}

type SemanticAccessAttributeSnapshot struct {
	InstanceID               string
	PrincipalID              string
	ActorID                  string
	Registry                 access.SemanticAttributeRegistrySnapshot
	Control                  access.SemanticAttributeControlSnapshot
	EffectiveAttributes      []access.EffectiveSemanticAttribute
	EffectiveAttributeDigest string
	DirectAssignmentEvidence access.SemanticAttributeDirectEvidence
	TrustedClaimEvidence     access.SemanticAttributeClaimEvidence
}

type SemanticAccessGrantDecision struct {
	Name         string `json:"name"`
	DefinitionID string `json:"definitionId"`
	Allowed      bool   `json:"allowed"`
}

type SemanticAccessFilterEvidence struct {
	Dataset       string `json:"dataset"`
	Dimension     string `json:"dimension"`
	DefinitionID  string `json:"definitionId"`
	ValueDigest   string `json:"valueDigest"`
	PredicateKind string `json:"predicateKind"`
}

type SemanticAccessObjectDecision struct {
	Name           string
	Dataset        string
	Allowed        bool
	DeniedGrants   []string
	DeniedDatasets []string
	Predicate      *planir.Predicate
	// DatasetPredicates carries every governed dataset predicate needed by
	// this object, including relationship/dependency datasets. Consumers must
	// not infer that Predicate alone covers a multi-dataset object.
	DatasetPredicates map[string]planir.Predicate
}

type SemanticAccessDecision struct {
	Profile                        string
	InstanceID                     string
	SemanticModelID                string
	SemanticGeneration             string
	PrincipalID                    string
	ActorID                        string
	Registry                       access.SemanticAttributeRegistryState
	Control                        access.SemanticAttributeControlState
	EffectiveAttributeDigest       string
	DirectAssignmentEvidenceDigest string
	TrustedClaimEvidenceDigest     string
	PolicyDigest                   string
	IdentityDigest                 string
	GrantResults                   []SemanticAccessGrantDecision
	FilterEvidence                 []SemanticAccessFilterEvidence
	datasets                       map[string]SemanticAccessObjectDecision
	dimensions                     map[string]map[string]SemanticAccessObjectDecision
	metrics                        map[string]SemanticAccessObjectDecision
}

func (decision *SemanticAccessDecision) Dataset(name string) (SemanticAccessObjectDecision, bool) {
	if decision == nil {
		return SemanticAccessObjectDecision{}, false
	}
	value, ok := decision.datasets[name]
	return cloneSemanticAccessObjectDecision(value), ok
}

func (decision *SemanticAccessDecision) Dimension(name, dataset string) (SemanticAccessObjectDecision, bool) {
	if decision == nil {
		return SemanticAccessObjectDecision{}, false
	}
	value, ok := decision.dimensions[name][dataset]
	return cloneSemanticAccessObjectDecision(value), ok
}

func (decision *SemanticAccessDecision) Metric(name string) (SemanticAccessObjectDecision, bool) {
	if decision == nil {
		return SemanticAccessObjectDecision{}, false
	}
	value, ok := decision.metrics[name]
	return cloneSemanticAccessObjectDecision(value), ok
}

func cloneSemanticAccessObjectDecision(value SemanticAccessObjectDecision) SemanticAccessObjectDecision {
	value.DeniedGrants = append([]string(nil), value.DeniedGrants...)
	value.DeniedDatasets = append([]string(nil), value.DeniedDatasets...)
	if value.Predicate != nil {
		clone := clonePlanIRPredicate(*value.Predicate)
		value.Predicate = &clone
	}
	value.DatasetPredicates = cloneSemanticAccessDatasetPredicates(value.DatasetPredicates)
	return value
}

func cloneSemanticAccessDatasetPredicates(values map[string]planir.Predicate) map[string]planir.Predicate {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]planir.Predicate, len(values))
	for dataset, predicate := range values {
		result[dataset] = clonePlanIRPredicate(predicate)
	}
	return result
}

func clonePlanIRPredicate(value planir.Predicate) planir.Predicate {
	value.Values = append([]planir.Literal(nil), value.Values...)
	value.Children = append([]planir.Predicate(nil), value.Children...)
	for index := range value.Children {
		value.Children[index] = clonePlanIRPredicate(value.Children[index])
	}
	if value.Spatial != nil {
		spatial := *value.Spatial
		spatial.Points = append([]planir.SpatialPoint(nil), value.Spatial.Points...)
		value.Spatial = &spatial
	}
	return value
}

// EffectiveSemanticAttributeDigest returns the ordered, profile-qualified
// projection used in authorization identity. It includes value digests but not
// raw canonical values.
func EffectiveSemanticAttributeDigest(attributes []access.EffectiveSemanticAttribute) (string, error) {
	validated, err := validateEffectiveSemanticAttributes(attributes)
	if err != nil {
		return "", err
	}
	type attributeWire struct {
		DefinitionID      string                        `json:"definitionId"`
		DefinitionVersion int64                         `json:"definitionVersion"`
		Type              semanticvalue.Type            `json:"type"`
		Shape             access.SemanticAttributeShape `json:"shape"`
		ValueDigest       string                        `json:"valueDigest"`
		Source            string                        `json:"source"`
	}
	wire := struct {
		Profile    string          `json:"profile"`
		Attributes []attributeWire `json:"attributes"`
	}{Profile: semanticvalue.Profile, Attributes: make([]attributeWire, len(validated))}
	for index, attribute := range validated {
		wire.Attributes[index] = attributeWire{DefinitionID: attribute.DefinitionID,
			DefinitionVersion: attribute.DefinitionVersion, Type: attribute.Type, Shape: attribute.Shape,
			ValueDigest: attribute.ValueDigest, Source: attribute.Source}
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", fmt.Errorf("%w: encode effective attribute identity: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	return semanticAccessDigest(encoded), nil
}

// EvaluateSemanticAccess evaluates every authored grant and lowers every
// satisfiable dataset filter to a closed typed PlanIR predicate. Denied access
// is represented in the returned decision; malformed/stale authority returns
// an error and never an unfiltered decision.
func EvaluateSemanticAccess(policy *CompiledSemanticAccessPolicy, snapshot SemanticAccessAttributeSnapshot, current SemanticAccessAuthority) (*SemanticAccessDecision, error) {
	if policy == nil || policy.Digest() == "" {
		return nil, fmt.Errorf("%w: compiled policy is required", ErrSemanticAccessSnapshotInvalid)
	}
	attributes, directEvidenceDigest, trustedClaimEvidenceDigest, err := validateSemanticAccessSnapshot(policy, snapshot, current)
	if err != nil {
		return nil, err
	}
	decision := &SemanticAccessDecision{Profile: semanticvalue.Profile, InstanceID: snapshot.InstanceID,
		SemanticModelID: policy.semanticModelID, SemanticGeneration: policy.semanticGeneration,
		PrincipalID: snapshot.PrincipalID, ActorID: snapshot.ActorID, Registry: snapshot.Registry.State, Control: snapshot.Control.State,
		EffectiveAttributeDigest: snapshot.EffectiveAttributeDigest, DirectAssignmentEvidenceDigest: directEvidenceDigest,
		TrustedClaimEvidenceDigest: trustedClaimEvidenceDigest, PolicyDigest: policy.digest,
		datasets: map[string]SemanticAccessObjectDecision{}, dimensions: map[string]map[string]SemanticAccessObjectDecision{}, metrics: map[string]SemanticAccessObjectDecision{}}

	grantAllowed := make(map[string]bool, len(policy.grants))
	for _, name := range sortedStringKeys(policy.grants) {
		grant := policy.grants[name]
		attribute, present := attributes[grant.DefinitionID]
		if present {
			if err := validateEffectiveAttributeReference(attribute, grant.DefinitionID, grant.UserAttribute, grant.DefinitionVersion, grant.Type, grant.Shape); err != nil {
				return nil, err
			}
		}
		allowed := present && effectiveAttributeMatchesGrant(attribute, grant)
		grantAllowed[name] = allowed
		decision.GrantResults = append(decision.GrantResults, SemanticAccessGrantDecision{Name: name, DefinitionID: grant.DefinitionID, Allowed: allowed})
	}

	for _, name := range sortedStringKeys(policy.datasets) {
		compiled := policy.datasets[name]
		object := semanticAccessRequirementDecision(name, name, compiled.RequiredAccessGrants, grantAllowed)
		var predicates []planir.Predicate
		for _, filter := range compiled.AccessFilters {
			attribute, present := attributes[filter.DefinitionID]
			if !present {
				object.Allowed = false
				object.DeniedDatasets = appendUniqueSorted(object.DeniedDatasets, name)
				continue
			}
			if err := validateEffectiveAttributeReference(attribute, filter.DefinitionID, filter.UserAttribute, filter.DefinitionVersion, filter.Type, filter.Shape); err != nil {
				return nil, err
			}
			predicate, err := semanticAccessFilterPredicate(filter, attribute)
			if err != nil {
				return nil, err
			}
			predicates = append(predicates, predicate)
			decision.FilterEvidence = append(decision.FilterEvidence, SemanticAccessFilterEvidence{Dataset: name, Dimension: filter.Dimension,
				DefinitionID: filter.DefinitionID, ValueDigest: attribute.ValueDigest, PredicateKind: string(predicate.Kind)})
		}
		if object.Allowed {
			object.Predicate = combinedSemanticAccessPredicate(predicates)
		}
		decision.datasets[name] = object
	}

	for _, name := range sortedStringKeys(policy.dimensions) {
		decision.dimensions[name] = map[string]SemanticAccessObjectDecision{}
		for _, dataset := range sortedStringKeys(policy.dimensions[name]) {
			compiled := policy.dimensions[name][dataset]
			object := semanticAccessRequirementDecision(name, dataset, compiled.RequiredAccessGrants, grantAllowed)
			inheritDatasetDecisions(&object, compiled.Datasets, decision.datasets)
			decision.dimensions[name][dataset] = object
		}
	}
	for _, name := range sortedStringKeys(policy.metrics) {
		compiled := policy.metrics[name]
		object := semanticAccessRequirementDecision(name, "", compiled.RequiredAccessGrants, grantAllowed)
		object.DeniedDatasets = deniedSemanticDatasets(compiled.Datasets, decision.datasets)
		if len(object.DeniedDatasets) != 0 {
			object.Allowed = false
		}
		if object.Allowed {
			object.DatasetPredicates = semanticAccessDatasetPredicates(compiled.Datasets, decision.datasets)
		}
		decision.metrics[name] = object
	}

	identity, err := semanticAccessDecisionIdentity(decision)
	if err != nil {
		return nil, err
	}
	decision.IdentityDigest = semanticAccessDigest(identity)
	return decision, nil
}

func validateSemanticAccessSnapshot(policy *CompiledSemanticAccessPolicy, snapshot SemanticAccessAttributeSnapshot, current SemanticAccessAuthority) (map[string]access.EffectiveSemanticAttribute, string, string, error) {
	if err := validateSemanticIdentity("instance id", snapshot.InstanceID); err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	if err := validateSemanticIdentity("principal id", snapshot.PrincipalID); err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	if snapshot.ActorID != "" {
		if err := validateSemanticIdentity("actor id", snapshot.ActorID); err != nil {
			return nil, "", "", fmt.Errorf("%w: %v", ErrSemanticAccessSnapshotInvalid, err)
		}
	}
	if snapshot.InstanceID != policy.targetInstanceID {
		return nil, "", "", fmt.Errorf("%w: target instance does not match compiled policy", ErrSemanticAccessSnapshotInvalid)
	}
	if current.InstanceID != policy.targetInstanceID || current.InstanceID != snapshot.InstanceID {
		return nil, "", "", fmt.Errorf("%w: current authority target instance is inconsistent", ErrSemanticAccessSnapshotInvalid)
	}
	if current.ObservedAt.IsZero() || !current.ObservedAt.Equal(current.ObservedAt.UTC()) {
		return nil, "", "", fmt.Errorf("%w: current authority observation time is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	if err := validateRegistrySnapshot(snapshot.Registry); err != nil {
		return nil, "", "", fmt.Errorf("%w: captured registry identity is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	if err := validateRegistrySnapshot(current.Registry); err != nil {
		return nil, "", "", fmt.Errorf("%w: current registry identity is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	if err := validateControlSnapshot(snapshot.Control); err != nil {
		return nil, "", "", err
	}
	if err := validateControlSnapshot(current.Control); err != nil {
		return nil, "", "", err
	}
	if err := validateSemanticAccessAuthorityRows(snapshot.Registry, snapshot.Control); err != nil {
		return nil, "", "", err
	}
	if err := validateSemanticAccessAuthorityRows(current.Registry, current.Control); err != nil {
		return nil, "", "", err
	}
	if !sameRegistryState(policy.registry, snapshot.Registry.State) || !sameRegistryState(snapshot.Registry.State, current.Registry.State) {
		return nil, "", "", fmt.Errorf("%w: registry identity is stale or inconsistent", ErrSemanticAccessSnapshotInvalid)
	}
	if !sameControlState(snapshot.Control.State, current.Control.State) {
		return nil, "", "", fmt.Errorf("%w: control identity is stale or inconsistent", ErrSemanticAccessSnapshotInvalid)
	}
	digest, err := EffectiveSemanticAttributeDigest(snapshot.EffectiveAttributes)
	if err != nil {
		return nil, "", "", err
	}
	if snapshot.EffectiveAttributeDigest == "" || digest != snapshot.EffectiveAttributeDigest {
		return nil, "", "", fmt.Errorf("%w: effective attribute digest mismatch", ErrSemanticAccessSnapshotInvalid)
	}
	directEvidenceDigest, err := validateSemanticAccessDirectAssignments(snapshot)
	if err != nil {
		return nil, "", "", err
	}
	trustedClaimEvidenceDigest, err := validateSemanticAccessTrustedClaims(snapshot, current.ObservedAt)
	if err != nil {
		return nil, "", "", err
	}
	attributes := make(map[string]access.EffectiveSemanticAttribute, len(snapshot.EffectiveAttributes))
	for _, attribute := range snapshot.EffectiveAttributes {
		attributes[attribute.DefinitionID] = attribute
	}
	return attributes, directEvidenceDigest, trustedClaimEvidenceDigest, nil
}

func validateSemanticAccessDirectAssignments(snapshot SemanticAccessAttributeSnapshot) (string, error) {
	direct := false
	for _, attribute := range snapshot.EffectiveAttributes {
		if attribute.Source == "direct" || attribute.Source == "direct+trusted_claim" {
			direct = true
			break
		}
	}
	if !direct {
		if snapshot.DirectAssignmentEvidence.Digest() != "" {
			return "", fmt.Errorf("%w: direct assignment evidence is present without direct attributes", ErrSemanticAccessSnapshotInvalid)
		}
		return "", nil
	}
	if !snapshot.DirectAssignmentEvidence.Matches(snapshot.InstanceID, snapshot.PrincipalID, snapshot.Control.State, snapshot.EffectiveAttributes) {
		return "", fmt.Errorf("%w: verified direct assignment evidence does not match effective attributes", ErrSemanticAccessSnapshotInvalid)
	}
	return snapshot.DirectAssignmentEvidence.Digest(), nil
}

func validateSemanticAccessTrustedClaims(snapshot SemanticAccessAttributeSnapshot, observedAt time.Time) (string, error) {
	claimDerived := false
	for _, attribute := range snapshot.EffectiveAttributes {
		if attribute.Source == "trusted_claim" || attribute.Source == "direct+trusted_claim" {
			claimDerived = true
			break
		}
	}
	if !claimDerived {
		if snapshot.TrustedClaimEvidence.Digest() != "" {
			return "", fmt.Errorf("%w: trusted claim evidence is present without claim-derived attributes", ErrSemanticAccessSnapshotInvalid)
		}
		return "", nil
	}
	if !snapshot.TrustedClaimEvidence.Matches(snapshot.InstanceID, snapshot.PrincipalID, snapshot.Control.State, snapshot.EffectiveAttributes) {
		return "", fmt.Errorf("%w: verified trusted claim evidence does not match effective attributes", ErrSemanticAccessSnapshotInvalid)
	}
	if !snapshot.TrustedClaimEvidence.ValidAt(observedAt) {
		return "", fmt.Errorf("%w: trusted claim evidence is expired, future, or otherwise invalid", ErrSemanticAccessSnapshotInvalid)
	}
	return snapshot.TrustedClaimEvidence.Digest(), nil
}

func validateRegistrySnapshot(snapshot access.SemanticAttributeRegistrySnapshot) error {
	if err := access.ValidateSemanticAttributeRegistrySnapshot(snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	return nil
}

func validateControlSnapshot(snapshot access.SemanticAttributeControlSnapshot) error {
	if err := access.ValidateSemanticAttributeControlSnapshot(snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	return nil
}

func validateSemanticAccessAuthorityRows(registry access.SemanticAttributeRegistrySnapshot, control access.SemanticAttributeControlSnapshot) error {
	definitions := make(map[string]access.SemanticAttributeDefinition, len(registry.Definitions))
	for _, definition := range registry.Definitions {
		definitions[definition.ID] = definition
	}
	validateReference := func(id, name string, version int64, typeName semanticvalue.Type, shape access.SemanticAttributeShape, active bool) error {
		definition, ok := definitions[id]
		if !ok || definition.Name != name || version <= 0 || version > definition.DefinitionVersion || definition.Type != typeName || definition.Shape != shape {
			return fmt.Errorf("%w: control row does not match registry definition %q", ErrSemanticAccessSnapshotInvalid, name)
		}
		if active && (!definition.Enabled || definition.LifecycleState != access.SemanticAttributeActive) {
			return fmt.Errorf("%w: active control row references disabled definition %q", ErrSemanticAccessSnapshotInvalid, name)
		}
		return nil
	}
	for _, assignment := range control.Assignments {
		if err := validateReference(assignment.DefinitionID, assignment.DefinitionName, assignment.DefinitionVersion, assignment.Type, assignment.Shape, !assignment.Tombstoned); err != nil {
			return err
		}
	}
	for _, mapping := range control.Mappings {
		if err := validateReference(mapping.DefinitionID, mapping.DefinitionName, mapping.DefinitionVersion, mapping.Type, mapping.Shape, !mapping.Tombstoned); err != nil {
			return err
		}
	}
	return nil
}

func validateControlState(state access.SemanticAttributeControlState) error {
	if state.Profile != semanticvalue.Profile || state.Revision < 0 || !canonicalSHA256.MatchString(state.Digest) {
		return fmt.Errorf("%w: control state identity is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	return nil
}

func sameRegistryState(left, right access.SemanticAttributeRegistryState) bool {
	return left.Profile == right.Profile && left.Revision == right.Revision && left.Digest == right.Digest
}

func sameControlState(left, right access.SemanticAttributeControlState) bool {
	return left.Profile == right.Profile && left.Revision == right.Revision && left.Digest == right.Digest
}

func validateEffectiveSemanticAttributes(values []access.EffectiveSemanticAttribute) ([]access.EffectiveSemanticAttribute, error) {
	result := append([]access.EffectiveSemanticAttribute(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i].DefinitionID < result[j].DefinitionID })
	names := map[string]struct{}{}
	for index := range result {
		attribute := &result[index]
		if attribute.DefinitionID == "" || attribute.DefinitionVersion <= 0 || !semanticAccessTypeSupported(attribute.Type) || !attribute.Shape.Valid() {
			return nil, fmt.Errorf("%w: effective attribute definition identity is invalid", ErrSemanticAccessSnapshotInvalid)
		}
		if err := semanticvalue.ValidateAttributeName(attribute.DefinitionName); err != nil {
			return nil, fmt.Errorf("%w: effective attribute name is invalid", ErrSemanticAccessSnapshotInvalid)
		}
		if index > 0 && result[index-1].DefinitionID == attribute.DefinitionID {
			return nil, fmt.Errorf("%w: duplicate effective attribute definition id", ErrSemanticAccessSnapshotInvalid)
		}
		if _, duplicate := names[attribute.DefinitionName]; duplicate {
			return nil, fmt.Errorf("%w: duplicate effective attribute name", ErrSemanticAccessSnapshotInvalid)
		}
		names[attribute.DefinitionName] = struct{}{}
		if attribute.Source != "direct" && attribute.Source != "trusted_claim" && attribute.Source != "direct+trusted_claim" {
			return nil, fmt.Errorf("%w: effective attribute source is invalid", ErrSemanticAccessSnapshotInvalid)
		}
		canonical, digest, err := validateCanonicalEffectiveValues(*attribute)
		if err != nil {
			return nil, err
		}
		if digest != attribute.ValueDigest {
			return nil, fmt.Errorf("%w: effective attribute value digest mismatch", ErrSemanticAccessSnapshotInvalid)
		}
		attribute.CanonicalValues = canonical
	}
	return result, nil
}

func validateCanonicalEffectiveValues(attribute access.EffectiveSemanticAttribute) ([]string, string, error) {
	if attribute.Shape == access.SemanticAttributeScalar && len(attribute.CanonicalValues) != 1 {
		return nil, "", fmt.Errorf("%w: scalar effective attribute requires one value", ErrSemanticAccessSnapshotInvalid)
	}
	if attribute.Shape == access.SemanticAttributeList && (len(attribute.CanonicalValues) == 0 || len(attribute.CanonicalValues) > semanticvalue.MaxSetValues) {
		return nil, "", fmt.Errorf("%w: list effective attribute cardinality is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	inputs := make([]any, len(attribute.CanonicalValues))
	for index, value := range attribute.CanonicalValues {
		input, err := canonicalSemanticAccessInput(attribute.Type, value)
		if err != nil {
			return nil, "", fmt.Errorf("%w: effective attribute value is invalid", ErrSemanticAccessSnapshotInvalid)
		}
		inputs[index] = input
	}
	if attribute.Shape == access.SemanticAttributeScalar {
		value, err := semanticvalue.Canonicalize(attribute.Type, inputs[0])
		if err != nil || value.Canonical() != attribute.CanonicalValues[0] {
			return nil, "", fmt.Errorf("%w: scalar effective attribute is not canonical", ErrSemanticAccessSnapshotInvalid)
		}
		return []string{value.Canonical()}, value.Digest(), nil
	}
	set, err := semanticvalue.CanonicalizeSet(attribute.Type, inputs)
	if err != nil {
		return nil, "", fmt.Errorf("%w: list effective attribute is invalid", ErrSemanticAccessSnapshotInvalid)
	}
	values := set.Values()
	canonical := make([]string, len(values))
	for index := range values {
		canonical[index] = values[index].Canonical()
	}
	if len(canonical) != len(attribute.CanonicalValues) {
		return nil, "", fmt.Errorf("%w: list effective attribute contains duplicates", ErrSemanticAccessSnapshotInvalid)
	}
	for index := range canonical {
		if canonical[index] != attribute.CanonicalValues[index] {
			return nil, "", fmt.Errorf("%w: list effective attribute is not canonically ordered", ErrSemanticAccessSnapshotInvalid)
		}
	}
	return canonical, set.Digest(), nil
}

func canonicalSemanticAccessInput(typeName semanticvalue.Type, value string) (any, error) {
	switch typeName {
	case semanticvalue.TypeString, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return value, nil
	case semanticvalue.TypeBoolean:
		if value != "true" && value != "false" {
			return nil, fmt.Errorf("invalid boolean")
		}
		return value == "true", nil
	case semanticvalue.TypeInteger, semanticvalue.TypeDecimal:
		return json.Number(value), nil
	default:
		return nil, fmt.Errorf("unsupported type")
	}
}

func effectiveAttributeMatchesGrant(attribute access.EffectiveSemanticAttribute, grant CompiledSemanticAccessGrant) bool {
	if validateEffectiveAttributeReference(attribute, grant.DefinitionID, grant.UserAttribute, grant.DefinitionVersion, grant.Type, grant.Shape) != nil {
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

func validateEffectiveAttributeReference(attribute access.EffectiveSemanticAttribute, definitionID, name string, version int64, typeName semanticvalue.Type, shape access.SemanticAttributeShape) error {
	if attribute.DefinitionID != definitionID || attribute.DefinitionName != name || attribute.DefinitionVersion != version || attribute.Type != typeName || attribute.Shape != shape {
		return fmt.Errorf("%w: effective attribute does not match compiled definition identity", ErrSemanticAccessSnapshotInvalid)
	}
	return nil
}

func semanticAccessFilterPredicate(filter CompiledSemanticAccessFilter, attribute access.EffectiveSemanticAttribute) (planir.Predicate, error) {
	values := make([]planir.Literal, len(attribute.CanonicalValues))
	for index, value := range attribute.CanonicalValues {
		literal, err := semanticAccessPlanIRLiteral(attribute.Type, value)
		if err != nil {
			return planir.Predicate{}, err
		}
		values[index] = literal
	}
	if attribute.Shape == access.SemanticAttributeScalar {
		return planir.Predicate{Kind: planir.PredicateCompare, Field: filter.Field, Operator: "=", Value: values[0]}, nil
	}
	return planir.Predicate{Kind: planir.PredicateIn, Field: filter.Field, Values: values}, nil
}

func semanticAccessPlanIRLiteral(typeName semanticvalue.Type, value string) (planir.Literal, error) {
	switch typeName {
	case semanticvalue.TypeString, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return planir.Literal{Kind: planir.LiteralString, String: value}, nil
	case semanticvalue.TypeBoolean:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return planir.Literal{}, fmt.Errorf("%w: invalid canonical boolean", ErrSemanticAccessSnapshotInvalid)
		}
		return planir.Literal{Kind: planir.LiteralBool, Bool: parsed}, nil
	case semanticvalue.TypeInteger:
		return planir.Literal{Kind: planir.LiteralNumber, NumberText: value, NumberKind: planir.NumberInteger}, nil
	case semanticvalue.TypeDecimal:
		return planir.Literal{Kind: planir.LiteralNumber, NumberText: value, NumberKind: planir.NumberDecimal}, nil
	default:
		return planir.Literal{}, fmt.Errorf("%w: unsupported predicate value type", ErrSemanticAccessSnapshotInvalid)
	}
}

func combinedSemanticAccessPredicate(values []planir.Predicate) *planir.Predicate {
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 {
		value := values[0]
		return &value
	}
	return &planir.Predicate{Kind: planir.PredicateAnd, Children: append([]planir.Predicate(nil), values...)}
}

func semanticAccessRequirementDecision(name, dataset string, required []string, allowed map[string]bool) SemanticAccessObjectDecision {
	decision := SemanticAccessObjectDecision{Name: name, Dataset: dataset, Allowed: true}
	for _, grant := range required {
		if !allowed[grant] {
			decision.Allowed = false
			decision.DeniedGrants = append(decision.DeniedGrants, grant)
		}
	}
	return decision
}

func inheritDatasetDecisions(object *SemanticAccessObjectDecision, required []string, datasets map[string]SemanticAccessObjectDecision) {
	if !object.Allowed {
		return
	}
	object.DeniedDatasets = deniedSemanticDatasets(required, datasets)
	if len(object.DeniedDatasets) != 0 {
		object.Allowed = false
		return
	}
	dataset, ok := datasets[object.Dataset]
	if !ok {
		object.Allowed = false
		object.DeniedDatasets = appendUniqueSorted(object.DeniedDatasets, object.Dataset)
		return
	}
	if dataset.Predicate != nil {
		predicate := clonePlanIRPredicate(*dataset.Predicate)
		object.Predicate = &predicate
	}
	object.DatasetPredicates = semanticAccessDatasetPredicates(required, datasets)
}

func semanticAccessDatasetPredicates(required []string, datasets map[string]SemanticAccessObjectDecision) map[string]planir.Predicate {
	result := map[string]planir.Predicate{}
	for _, name := range required {
		if dataset, ok := datasets[name]; ok && dataset.Predicate != nil {
			result[name] = clonePlanIRPredicate(*dataset.Predicate)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func deniedSemanticDatasets(names []string, datasets map[string]SemanticAccessObjectDecision) []string {
	var denied []string
	for _, name := range names {
		if dataset, ok := datasets[name]; !ok || !dataset.Allowed {
			denied = append(denied, name)
		}
	}
	return sortedUnique(denied)
}

func appendUniqueSorted(values []string, value string) []string {
	values = append(values, value)
	return sortedUnique(values)
}

func semanticAccessDecisionIdentity(decision *SemanticAccessDecision) ([]byte, error) {
	type objectWire struct {
		Kind           string   `json:"kind"`
		Name           string   `json:"name"`
		Dataset        string   `json:"dataset,omitempty"`
		Allowed        bool     `json:"allowed"`
		DeniedGrants   []string `json:"deniedGrants,omitempty"`
		DeniedDatasets []string `json:"deniedDatasets,omitempty"`
	}
	objects := []objectWire{}
	for _, name := range sortedStringKeys(decision.datasets) {
		value := decision.datasets[name]
		objects = append(objects, objectWire{Kind: "dataset", Name: name, Allowed: value.Allowed, DeniedGrants: value.DeniedGrants, DeniedDatasets: value.DeniedDatasets})
	}
	for _, name := range sortedStringKeys(decision.dimensions) {
		for _, dataset := range sortedStringKeys(decision.dimensions[name]) {
			value := decision.dimensions[name][dataset]
			objects = append(objects, objectWire{Kind: "dimension", Name: name, Dataset: dataset, Allowed: value.Allowed, DeniedGrants: value.DeniedGrants, DeniedDatasets: value.DeniedDatasets})
		}
	}
	for _, name := range sortedStringKeys(decision.metrics) {
		value := decision.metrics[name]
		objects = append(objects, objectWire{Kind: "metric", Name: name, Allowed: value.Allowed, DeniedGrants: value.DeniedGrants, DeniedDatasets: value.DeniedDatasets})
	}
	wire := struct {
		Profile                        string                         `json:"profile"`
		InstanceID                     string                         `json:"instanceId"`
		SemanticModelID                string                         `json:"semanticModelId"`
		SemanticGeneration             string                         `json:"semanticGeneration"`
		PrincipalID                    string                         `json:"principalId"`
		ActorID                        string                         `json:"actorId,omitempty"`
		RegistryProfile                string                         `json:"registryProfile"`
		RegistryRevision               int64                          `json:"registryRevision"`
		RegistryDigest                 string                         `json:"registryDigest"`
		ControlProfile                 string                         `json:"controlProfile"`
		ControlRevision                int64                          `json:"controlRevision"`
		ControlDigest                  string                         `json:"controlDigest"`
		EffectiveAttributeDigest       string                         `json:"effectiveAttributeDigest"`
		DirectAssignmentEvidenceDigest string                         `json:"directAssignmentEvidenceDigest,omitempty"`
		TrustedClaimEvidenceDigest     string                         `json:"trustedClaimEvidenceDigest,omitempty"`
		PolicyDigest                   string                         `json:"policyDigest"`
		Grants                         []SemanticAccessGrantDecision  `json:"grants"`
		Filters                        []SemanticAccessFilterEvidence `json:"filters"`
		Objects                        []objectWire                   `json:"objects"`
	}{Profile: decision.Profile, InstanceID: decision.InstanceID, SemanticModelID: decision.SemanticModelID,
		SemanticGeneration: decision.SemanticGeneration, PrincipalID: decision.PrincipalID, ActorID: decision.ActorID,
		RegistryProfile: decision.Registry.Profile, RegistryRevision: decision.Registry.Revision, RegistryDigest: decision.Registry.Digest,
		ControlProfile: decision.Control.Profile, ControlRevision: decision.Control.Revision, ControlDigest: decision.Control.Digest,
		EffectiveAttributeDigest: decision.EffectiveAttributeDigest, DirectAssignmentEvidenceDigest: decision.DirectAssignmentEvidenceDigest,
		TrustedClaimEvidenceDigest: decision.TrustedClaimEvidenceDigest,
		PolicyDigest:               decision.PolicyDigest,
		Grants:                     decision.GrantResults, Filters: decision.FilterEvidence, Objects: objects}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("%w: encode decision identity: %v", ErrSemanticAccessSnapshotInvalid, err)
	}
	return encoded, nil
}
