package query

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// SemanticAccessConsumerConfig binds a query consumer to one complete serving
// target and one authenticated subject. Protected consumers require every
// identity field; public consumers may omit the authority and principal.
// Authority is deliberately a function: each admission/discovery operation
// obtains a fresh coherent registry/control and effective-attribute observation.
type SemanticAccessConsumerConfig struct {
	InstanceID  string
	ProjectID   string
	Environment string
	ModelID     string
	Generation  string
	PrincipalID string
	Authority   func() (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error)
}

// SemanticAccessResolutionSnapshot projects one coherent Access-owned
// resolution into the query evaluator's detached snapshot/authority pair.
// Keeping this adapter here prevents consumers from independently assembling
// evidence or accidentally pairing values with a different control state.
func SemanticAccessResolutionSnapshot(instanceID, principalID string, resolved access.SemanticAttributeResolution) (SemanticAccessAttributeSnapshot, SemanticAccessAuthority, error) {
	if err := validateConsumerIdentity(instanceID, "instance ID"); err != nil {
		return SemanticAccessAttributeSnapshot{}, SemanticAccessAuthority{}, err
	}
	if err := validateConsumerIdentity(principalID, "principal ID"); err != nil {
		return SemanticAccessAttributeSnapshot{}, SemanticAccessAuthority{}, err
	}
	if resolved.Subject.Kind != access.SubjectKindPrincipal || resolved.Subject.ID != principalID {
		return SemanticAccessAttributeSnapshot{}, SemanticAccessAuthority{}, fmt.Errorf("semantic access resolution subject does not match principal")
	}
	digest, err := EffectiveSemanticAttributeDigest(resolved.Attributes)
	if err != nil {
		return SemanticAccessAttributeSnapshot{}, SemanticAccessAuthority{}, err
	}
	attributes := make([]access.EffectiveSemanticAttribute, len(resolved.Attributes))
	for index, attribute := range resolved.Attributes {
		attributes[index] = attribute
		attributes[index].CanonicalValues = append([]string(nil), attribute.CanonicalValues...)
	}
	snapshot := SemanticAccessAttributeSnapshot{InstanceID: instanceID, PrincipalID: principalID, ActorID: principalID,
		Registry: resolved.Registry, Control: resolved.Control, EffectiveAttributes: attributes, EffectiveAttributeDigest: digest}
	hasDirect := false
	for _, attribute := range attributes {
		if attribute.Source == "direct" || attribute.Source == "direct+trusted_claim" {
			hasDirect = true
			break
		}
	}
	if hasDirect {
		snapshot.DirectAssignmentEvidence, err = access.NewSemanticAttributeDirectEvidence(instanceID, principalID, resolved.Subjects, resolved.Control, resolved.Attributes)
		if err != nil {
			return SemanticAccessAttributeSnapshot{}, SemanticAccessAuthority{}, err
		}
	}
	authority := SemanticAccessAuthority{InstanceID: instanceID, Registry: resolved.Registry, Control: resolved.Control, ObservedAt: resolved.ObservedAt}
	return snapshot, authority, nil
}

// SemanticAccessTarget identifies a typed semantic asset. Dataset may qualify
// a dimension binding or a metric; a bare metric is also a valid target.
type SemanticAccessTarget struct {
	Dataset   string `json:"dataset,omitempty"`
	Dimension string `json:"dimension,omitempty"`
	Metric    string `json:"metric,omitempty"`
}

// SemanticAccessPolicyIdentity is detached policy provenance attached to
// every discovery asset. It contains identities only; no principal values or
// attribute values are exposed.
type SemanticAccessPolicyIdentity struct {
	Profile          string `json:"profile,omitempty"`
	InstanceID       string `json:"instanceId,omitempty"`
	ModelID          string `json:"modelId,omitempty"`
	Generation       string `json:"generation,omitempty"`
	PolicyDigest     string `json:"policyDigest,omitempty"`
	RegistryProfile  string `json:"registryProfile,omitempty"`
	RegistryRevision int64  `json:"registryRevision,omitempty"`
	RegistryDigest   string `json:"registryDigest,omitempty"`
}

// SemanticAccessOwnership describes registry stewardship behind one required
// grant or dataset access filter. Grant is empty for filter-only ownership;
// ownership is metadata, not a grant decision.
type SemanticAccessOwnership struct {
	Grant        string `json:"grant,omitempty"`
	DefinitionID string `json:"definitionId,omitempty"`
	Attribute    string `json:"attribute,omitempty"`
	Kind         string `json:"kind,omitempty"`
	ID           string `json:"id,omitempty"`
}

// SemanticAccessAsset is the detached discovery projection shared by query
// consumers. Only authorized protected assets are returned. Public models
// return the same typed projection with empty protection identity.
type SemanticAccessAsset struct {
	Kind                 string                       `json:"kind"`
	Name                 string                       `json:"name"`
	Dataset              string                       `json:"dataset,omitempty"`
	Dimension            string                       `json:"dimension,omitempty"`
	Metric               string                       `json:"metric,omitempty"`
	DependencyDatasets   []string                     `json:"dependencyDatasets,omitempty"`
	FilterDatasets       []string                     `json:"filterDatasets,omitempty"`
	RequiredAccessGrants []string                     `json:"requiredAccessGrants,omitempty"`
	Ownership            []SemanticAccessOwnership    `json:"ownership,omitempty"`
	PolicyIdentity       SemanticAccessPolicyIdentity `json:"policyIdentity"`
}

type semanticAccessPlanAdmission struct {
	canonical      []byte
	policyDigest   string
	decisionDigest string
}

// SemanticAccessConsumer is a request-bound semantic authorization and
// discovery boundary. Its admitted graph map is intentionally private and
// process-local; it is not a durable capability or serialized plan field.
type SemanticAccessConsumer struct {
	planner        *Planner
	config         SemanticAccessConsumerConfig
	policy         *CompiledSemanticAccessPolicy
	decisionDigest string
	protected      bool
	admissions     map[*planir.Graph]semanticAccessPlanAdmission
	mu             sync.RWMutex
}

// ModelRequiresSemanticAccess reports whether authored source contains a
// policy-bearing semantic access contract. A nil model is never protected.
func ModelRequiresSemanticAccess(model *semanticmodel.Model) bool {
	return model != nil && !model.AccessPolicy.Empty()
}

// NewSemanticAccessConsumer validates the target identity and captures a
// target-qualified FAI-639 policy for protected authored source. Public source
// retains ordinary planner behavior and does not require a principal or
// authority callback.
func NewSemanticAccessConsumer(planner *Planner, config SemanticAccessConsumerConfig) (*SemanticAccessConsumer, error) {
	if planner == nil || planner.compiled == nil {
		return nil, fmt.Errorf("compiled semantic planner is required")
	}
	model := planner.compiled.SourceModel()
	if model == nil {
		return nil, fmt.Errorf("compiled semantic model snapshot is required")
	}
	protected := !model.AccessPolicy.Empty()
	consumer := &SemanticAccessConsumer{config: config, protected: protected,
		admissions: make(map[*planir.Graph]semanticAccessPlanAdmission)}
	if !protected {
		requestPlanner := *planner
		requestPlanner.semanticAccessAdmissionHook = consumer.captureAdmission
		consumer.planner = &requestPlanner
		return consumer, nil
	}
	if err := validateConsumerIdentity(config.InstanceID, "instance ID"); err != nil {
		return nil, err
	}
	if err := validateConsumerIdentity(config.ProjectID, "project ID"); err != nil {
		return nil, err
	}
	if err := validateConsumerIdentity(config.Environment, "environment"); err != nil {
		return nil, err
	}
	if err := validateConsumerIdentity(config.ModelID, "model ID"); err != nil {
		return nil, err
	}
	if err := validateConsumerIdentity(config.Generation, "generation"); err != nil {
		return nil, err
	}
	if err := validateConsumerIdentity(config.PrincipalID, "principal ID"); err != nil {
		return nil, err
	}
	if config.Authority == nil {
		return nil, fmt.Errorf("protected semantic model requires semantic access authority")
	}
	snapshot, authority, err := config.Authority()
	if err != nil {
		return nil, fmt.Errorf("semantic access authority: %w", err)
	}
	if snapshot.PrincipalID != config.PrincipalID {
		return nil, fmt.Errorf("semantic access authority subject does not match consumer principal")
	}
	policy, decision, err := evaluateConsumerAuthority(planner.compiled, model, config, snapshot, authority, nil)
	if err != nil {
		return nil, err
	}
	if decision.PrincipalID != config.PrincipalID || decision.InstanceID != config.InstanceID {
		return nil, fmt.Errorf("semantic access authority identity does not match consumer")
	}
	consumer.policy = policy
	consumer.decisionDigest = decision.IdentityDigest
	requestPlanner := *planner
	requestPlanner.semanticAccessPolicy = policy
	requestPlanner.semanticAccessProvider = config.Authority
	requestPlanner.semanticAccessExpectedDecisionDigest = decision.IdentityDigest
	requestPlanner.semanticAccessAdmissionHook = consumer.captureAdmission
	consumer.planner = &requestPlanner
	return consumer, nil
}

// NewSemanticAccessDiscovery binds only an activation-owned compiled graph.
// Its planner deliberately has no executable table relation, so it can
// project and authorize metadata without becoming a query execution
// capability.
func NewSemanticAccessDiscovery(compiled *CompiledModel, config SemanticAccessConsumerConfig) (*SemanticAccessConsumer, error) {
	if compiled == nil {
		return nil, fmt.Errorf("compiled semantic model is required")
	}
	planner := &Planner{compiled: compiled, tableRelation: func(string) (string, error) {
		return "", fmt.Errorf("semantic access discovery planner cannot execute queries")
	}}
	return NewSemanticAccessConsumer(planner, config)
}

func validateConsumerIdentity(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s is required and must be canonical", label)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

// Planner returns a fresh request planner carrying this consumer's compiled
// policy, authority provider, and private admission hook.
func (consumer *SemanticAccessConsumer) Planner() *Planner {
	if consumer == nil || consumer.planner == nil {
		return nil
	}
	planner := *consumer.planner
	return &planner
}

// PlanRowsCount plans the effective population of a row request as a
// count-only graph. The request-bound planner carries the same compiled named
// filters as PlanRows and admits the resulting graph through this consumer's
// private provenance hook, so ValidatePlan can prove the exact barriers and
// renderer output before execution.
func (consumer *SemanticAccessConsumer) PlanRowsCount(request RowRequest) (Plan, error) {
	if consumer == nil || consumer.planner == nil {
		return Plan{}, fmt.Errorf("semantic access consumer is required")
	}
	return consumer.planner.planRowsCount(request)
}

// PrincipalID returns the authenticated principal bound to a protected
// consumer. Public consumers intentionally have no synthetic principal.
func (consumer *SemanticAccessConsumer) PrincipalID() string {
	if consumer == nil {
		return ""
	}
	return consumer.config.PrincipalID
}

// ModelID returns the immutable semantic model identity bound to this
// consumer. It is used by discovery adapters to reject a consumer composed
// for another model before projecting any protected metadata.
func (consumer *SemanticAccessConsumer) ModelID() string {
	if consumer == nil {
		return ""
	}
	return consumer.config.ModelID
}

// Assets returns a stable, detached, authorization-filtered asset projection.
func (consumer *SemanticAccessConsumer) Assets() ([]SemanticAccessAsset, error) {
	if consumer == nil || consumer.planner == nil || consumer.planner.compiled == nil {
		return nil, fmt.Errorf("semantic access consumer is required")
	}
	policy, decision, registry, err := consumer.authority()
	if err != nil {
		return nil, err
	}
	identity := consumer.policyIdentity(policy)
	ownership := semanticAccessOwnership(policy, registry)
	filterDatasets := semanticAccessFilterDatasetSet(policy)
	assets := make([]SemanticAccessAsset, 0)
	compiled := consumer.planner.compiled

	for _, dataset := range compiled.DatasetNames() {
		allowed, err := consumer.allowedDataset(policy, decision, dataset)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		spec, _ := policy.Dataset(dataset)
		asset := makeSemanticAccessAsset("dataset", dataset, "", []string{dataset}, filterDatasetsFor([]string{dataset}, filterDatasets), spec.RequiredAccessGrants, identity, ownership)
		appendSemanticAccessOwnership(&asset, semanticAccessFilterOwnership(policy, []string{dataset}, registry))
		assets = append(assets, asset)
	}

	dimensionNames := consumer.planner.compiled.SemanticDimensionNames()
	for _, dimension := range dimensionNames {
		datasets := consumer.planner.compiled.DatasetNames()
		for _, dataset := range datasets {
			if _, ok := compiled.DimensionBinding(dimension, dataset); !ok {
				continue
			}
			allowed, err := consumer.allowedDimension(policy, decision, dimension, dataset)
			if err != nil {
				return nil, err
			}
			if !allowed {
				continue
			}
			member, _ := policy.Dimension(dimension, dataset)
			if !consumer.protected {
				member.Datasets = []string{dataset}
			}
			asset := makeSemanticAccessAsset("dimension", dimension, dataset, member.Datasets, filterDatasetsFor(member.Datasets, filterDatasets), member.RequiredAccessGrants, identity, ownership)
			appendSemanticAccessOwnership(&asset, semanticAccessFilterOwnership(policy, member.Datasets, registry))
			assets = append(assets, asset)
		}
	}

	metricNames := consumer.planner.compiled.MetricNames()
	for _, metric := range metricNames {
		if _, ok := compiled.Metric(metric); !ok {
			continue
		}
		allowed, err := consumer.allowedMetric(policy, decision, metric)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		member, _ := policy.Metric(metric)
		if !consumer.protected {
			compiledMetric, _ := compiled.Metric(metric)
			member.Datasets = append([]string(nil), compiledMetric.RootDatasets...)
		}
		asset := makeSemanticAccessAsset("metric", metric, "", member.Datasets, filterDatasetsFor(member.Datasets, filterDatasets), member.RequiredAccessGrants, identity, ownership)
		appendSemanticAccessOwnership(&asset, semanticAccessFilterOwnership(policy, member.Datasets, registry))
		assets = append(assets, asset)
	}
	return assets, nil
}

// Authorize applies the same typed FAI-639 decision used by discovery and
// planner admission. Malformed combinations and unknown state fail closed.
func (consumer *SemanticAccessConsumer) Authorize(target SemanticAccessTarget) error {
	if consumer == nil || consumer.planner == nil || consumer.planner.compiled == nil {
		return fmt.Errorf("semantic access consumer is required")
	}
	target, err := canonicalSemanticAccessTarget(target)
	if err != nil {
		return err
	}
	policy, decision, _, err := consumer.authority()
	if err != nil {
		return err
	}
	if !consumer.protected {
		return consumer.authorizePublic(target)
	}
	if target.Dataset != "" {
		if _, ok := consumer.planner.compiled.Dataset(target.Dataset); !ok {
			return fmt.Errorf("semantic access target dataset %q is unknown", target.Dataset)
		}
		if allowed, err := consumer.allowedDataset(policy, decision, target.Dataset); err != nil || !allowed {
			if err != nil {
				return err
			}
			return fmt.Errorf("semantic access denied for dataset %q", target.Dataset)
		}
	}
	if target.Dimension != "" {
		if target.Dataset == "" {
			return fmt.Errorf("semantic access dimension requires dataset")
		}
		if _, ok := consumer.planner.compiled.SemanticDimension(target.Dimension); !ok {
			return fmt.Errorf("semantic access target dimension %q is unknown", target.Dimension)
		}
		if _, ok := consumer.planner.compiled.DimensionBinding(target.Dimension, target.Dataset); !ok {
			return fmt.Errorf("semantic access target dimension %q is not bound to dataset %q", target.Dimension, target.Dataset)
		}
		allowed, err := consumer.allowedDimension(policy, decision, target.Dimension, target.Dataset)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("semantic access denied for dimension %q", target.Dimension)
		}
	}
	if target.Metric != "" {
		if _, ok := consumer.planner.compiled.Metric(target.Metric); !ok {
			return fmt.Errorf("semantic access target metric %q is unknown", target.Metric)
		}
		member, ok := policy.Metric(target.Metric)
		if !ok {
			return fmt.Errorf("semantic access policy has no metric %q", target.Metric)
		}
		if target.Dataset != "" && !semanticConsumerContainsString(member.Datasets, target.Dataset) {
			return fmt.Errorf("semantic access metric %q does not depend on dataset %q", target.Metric, target.Dataset)
		}
		allowed, err := consumer.allowedMetric(policy, decision, target.Metric)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("semantic access denied for metric %q", target.Metric)
		}
	}
	return nil
}

func canonicalSemanticAccessTarget(target SemanticAccessTarget) (SemanticAccessTarget, error) {
	for _, field := range []struct {
		label string
		value *string
	}{{"dataset", &target.Dataset}, {"dimension", &target.Dimension}, {"metric", &target.Metric}} {
		label, value := field.label, *field.value
		if value != strings.TrimSpace(value) {
			return SemanticAccessTarget{}, fmt.Errorf("semantic access target %s must be canonical", label)
		}
		if value != "" {
			if err := validateConsumerIdentity(value, "semantic access target "+label); err != nil {
				return SemanticAccessTarget{}, err
			}
		}
	}
	if target.Dataset == "" && target.Dimension == "" && target.Metric == "" {
		return SemanticAccessTarget{}, fmt.Errorf("semantic access target is empty")
	}
	if target.Dimension != "" && target.Metric != "" {
		return SemanticAccessTarget{}, fmt.Errorf("semantic access target cannot combine dimension and metric")
	}
	if target.Dimension != "" && target.Dataset == "" {
		return SemanticAccessTarget{}, fmt.Errorf("semantic access dimension requires dataset")
	}
	return target, nil
}

func (consumer *SemanticAccessConsumer) authorizePublic(target SemanticAccessTarget) error {
	compiled := consumer.planner.compiled
	if target.Dataset != "" {
		if _, ok := compiled.Dataset(target.Dataset); !ok {
			return fmt.Errorf("semantic access target dataset %q is unknown", target.Dataset)
		}
	}
	if target.Dimension != "" {
		if target.Dataset == "" {
			return fmt.Errorf("semantic access dimension requires dataset")
		}
		if _, ok := compiled.SemanticDimension(target.Dimension); !ok {
			return fmt.Errorf("semantic access target dimension %q is unknown", target.Dimension)
		}
		if _, ok := compiled.DimensionBinding(target.Dimension, target.Dataset); !ok {
			return fmt.Errorf("semantic access target dimension %q is not bound to dataset %q", target.Dimension, target.Dataset)
		}
	}
	if target.Metric != "" {
		metric, ok := compiled.Metric(target.Metric)
		if !ok {
			return fmt.Errorf("semantic access target metric %q is unknown", target.Metric)
		}
		if target.Dataset != "" && !containsString(metric.RootDatasets, target.Dataset) {
			return fmt.Errorf("semantic access metric %q does not depend on dataset %q", target.Metric, target.Dataset)
		}
	}
	return nil
}

func (consumer *SemanticAccessConsumer) authority() (*CompiledSemanticAccessPolicy, *SemanticAccessDecision, access.SemanticAttributeRegistrySnapshot, error) {
	if !consumer.protected {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, nil
	}
	if consumer.config.Authority == nil {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("protected semantic model requires semantic access authority")
	}
	snapshot, current, err := consumer.config.Authority()
	if err != nil {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("semantic access authority: %w", err)
	}
	if snapshot.PrincipalID != consumer.config.PrincipalID {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("semantic access authority subject does not match consumer principal")
	}
	model := consumer.planner.compiled.SourceModel()
	if model == nil {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("compiled semantic model snapshot is required")
	}
	policy, decision, err := evaluateConsumerAuthority(consumer.planner.compiled, model, consumer.config, snapshot, current, consumer.policy)
	if err != nil {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, err
	}
	if consumer.decisionDigest != "" && decision.IdentityDigest != consumer.decisionDigest {
		return nil, nil, access.SemanticAttributeRegistrySnapshot{}, fmt.Errorf("semantic access authority decision is stale or inconsistent")
	}
	return policy, decision, current.Registry, nil
}

func evaluateConsumerAuthority(compiled *CompiledModel, model *semanticmodel.Model, config SemanticAccessConsumerConfig, snapshot SemanticAccessAttributeSnapshot, current SemanticAccessAuthority, expected *CompiledSemanticAccessPolicy) (*CompiledSemanticAccessPolicy, *SemanticAccessDecision, error) {
	if snapshot.InstanceID != config.InstanceID || current.InstanceID != config.InstanceID {
		return nil, nil, fmt.Errorf("semantic access authority instance does not match consumer")
	}
	policy, err := CompileSemanticAccessPolicy(config.InstanceID, config.ModelID, config.Generation, model, compiled, current.Registry)
	if err != nil {
		return nil, nil, fmt.Errorf("compile semantic access policy: %w", err)
	}
	if expected != nil && policy.Digest() != expected.Digest() {
		return nil, nil, fmt.Errorf("semantic access policy identity is stale or inconsistent")
	}
	decision, err := EvaluateSemanticAccess(policy, snapshot, current)
	if err != nil {
		return nil, nil, fmt.Errorf("evaluate semantic access: %w", err)
	}
	return policy, decision, nil
}

func (consumer *SemanticAccessConsumer) allowedDataset(policy *CompiledSemanticAccessPolicy, decision *SemanticAccessDecision, name string) (bool, error) {
	if !consumer.protected {
		return true, nil
	}
	if _, ok := policy.Dataset(name); !ok {
		return false, fmt.Errorf("semantic access policy has no dataset %q", name)
	}
	object, ok := decision.Dataset(name)
	return ok && object.Allowed, nil
}

func (consumer *SemanticAccessConsumer) allowedDimension(policy *CompiledSemanticAccessPolicy, decision *SemanticAccessDecision, name, dataset string) (bool, error) {
	if !consumer.protected {
		return true, nil
	}
	if _, ok := policy.Dimension(name, dataset); !ok {
		return false, fmt.Errorf("semantic access policy has no dimension binding %q on dataset %q", name, dataset)
	}
	object, ok := decision.Dimension(name, dataset)
	return ok && object.Allowed, nil
}

func (consumer *SemanticAccessConsumer) allowedMetric(policy *CompiledSemanticAccessPolicy, decision *SemanticAccessDecision, name string) (bool, error) {
	if !consumer.protected {
		return true, nil
	}
	if _, ok := policy.Metric(name); !ok {
		return false, fmt.Errorf("semantic access policy has no metric %q", name)
	}
	object, ok := decision.Metric(name)
	return ok && object.Allowed, nil
}

func (consumer *SemanticAccessConsumer) policyIdentity(policy *CompiledSemanticAccessPolicy) SemanticAccessPolicyIdentity {
	if !consumer.protected || policy == nil {
		return SemanticAccessPolicyIdentity{}
	}
	registry := policy.RegistryState()
	return SemanticAccessPolicyIdentity{Profile: policy.Profile(), InstanceID: policy.TargetInstanceID(), ModelID: policy.SemanticModelID(), Generation: policy.SemanticGeneration(), PolicyDigest: policy.Digest(), RegistryProfile: registry.Profile, RegistryRevision: registry.Revision, RegistryDigest: registry.Digest}
}

func makeSemanticAccessAsset(kind, name, dataset string, dependencies, filterDatasets, grants []string, identity SemanticAccessPolicyIdentity, ownership map[string]SemanticAccessOwnership) SemanticAccessAsset {
	grants = append([]string(nil), grants...)
	sort.Strings(grants)
	asset := SemanticAccessAsset{Kind: kind, Name: name, Dataset: dataset, DependencyDatasets: append([]string(nil), dependencies...), FilterDatasets: append([]string(nil), filterDatasets...), RequiredAccessGrants: append([]string(nil), grants...), PolicyIdentity: identity}
	switch kind {
	case "dimension":
		asset.Dimension = name
	case "metric":
		asset.Metric = name
	}
	for _, grant := range grants {
		if owner, ok := ownership[grant]; ok {
			asset.Ownership = append(asset.Ownership, owner)
		}
	}
	return asset
}

func semanticAccessOwnership(policy *CompiledSemanticAccessPolicy, registry access.SemanticAttributeRegistrySnapshot) map[string]SemanticAccessOwnership {
	result := map[string]SemanticAccessOwnership{}
	definitions := make(map[string]access.SemanticAttributeDefinition, len(registry.Definitions))
	for _, definition := range registry.Definitions {
		definitions[definition.ID] = definition
	}
	if policy == nil {
		return result
	}
	for _, grantName := range sortedStringKeys(policy.grants) {
		grant, _ := policy.Grant(grantName)
		definition := definitions[grant.DefinitionID]
		result[grantName] = SemanticAccessOwnership{Grant: grantName, DefinitionID: grant.DefinitionID, Attribute: grant.UserAttribute, Kind: string(definition.Metadata.Owner.Kind), ID: definition.Metadata.Owner.ID}
	}
	return result
}

func semanticAccessFilterOwnership(policy *CompiledSemanticAccessPolicy, datasets []string, registry access.SemanticAttributeRegistrySnapshot) []SemanticAccessOwnership {
	definitions := make(map[string]access.SemanticAttributeDefinition, len(registry.Definitions))
	for _, definition := range registry.Definitions {
		definitions[definition.Name] = definition
	}
	seen := map[string]bool{}
	result := make([]SemanticAccessOwnership, 0)
	for _, dataset := range datasets {
		spec, ok := policy.Dataset(dataset)
		if !ok {
			continue
		}
		for _, filter := range spec.AccessFilters {
			definition, ok := definitions[filter.UserAttribute]
			if !ok || seen[definition.ID] {
				continue
			}
			seen[definition.ID] = true
			result = append(result, SemanticAccessOwnership{DefinitionID: definition.ID, Attribute: definition.Name, Kind: string(definition.Metadata.Owner.Kind), ID: definition.Metadata.Owner.ID})
		}
	}
	return result
}

func appendSemanticAccessOwnership(asset *SemanticAccessAsset, owners []SemanticAccessOwnership) {
	if asset == nil {
		return
	}
	for _, owner := range owners {
		duplicate := false
		for _, existing := range asset.Ownership {
			if existing.Grant == owner.Grant && existing.DefinitionID == owner.DefinitionID && existing.Attribute == owner.Attribute {
				duplicate = true
				break
			}
		}
		if !duplicate {
			asset.Ownership = append(asset.Ownership, owner)
		}
	}
	sort.Slice(asset.Ownership, func(i, j int) bool {
		if asset.Ownership[i].Grant != asset.Ownership[j].Grant {
			return asset.Ownership[i].Grant < asset.Ownership[j].Grant
		}
		if asset.Ownership[i].Attribute != asset.Ownership[j].Attribute {
			return asset.Ownership[i].Attribute < asset.Ownership[j].Attribute
		}
		return asset.Ownership[i].DefinitionID < asset.Ownership[j].DefinitionID
	})
}

func semanticAccessFilterDatasetSet(policy *CompiledSemanticAccessPolicy) map[string]bool {
	result := map[string]bool{}
	if policy == nil {
		return result
	}
	for _, dataset := range sortedStringKeys(policy.datasets) {
		spec, _ := policy.Dataset(dataset)
		if len(spec.AccessFilters) != 0 {
			result[dataset] = true
		}
	}
	return result
}

func filterDatasetsFor(dependencies []string, filters map[string]bool) []string {
	result := make([]string, 0)
	for _, dataset := range dependencies {
		if filters[dataset] {
			result = append(result, dataset)
		}
	}
	return result
}

func semanticConsumerContainsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// captureAdmission records the exact canonical graph and renderer-independent
// identity emitted by one consumer planner. The map key is pointer identity,
// preventing copies or a second consumer from satisfying validation.
func (consumer *SemanticAccessConsumer) captureAdmission(graph *planir.Graph, admission semanticAccessAdmission) error {
	if graph == nil {
		return fmt.Errorf("semantic access plan graph is nil")
	}
	canonical, err := graph.Canonical()
	if err != nil {
		return fmt.Errorf("canonicalize admitted semantic plan: %w", err)
	}
	consumer.mu.Lock()
	consumer.admissions[graph] = semanticAccessPlanAdmission{canonical: append([]byte(nil), canonical...), policyDigest: admission.PolicyDigest, decisionDigest: admission.DecisionDigest}
	consumer.mu.Unlock()
	return nil
}

// ValidatePlan proves origin, unchanged graph identity, current authority,
// and exact renderer SQL/arguments/columns immediately before execution.
func (consumer *SemanticAccessConsumer) ValidatePlan(plan Plan) error {
	if consumer == nil || consumer.planner == nil {
		return fmt.Errorf("semantic access consumer is required")
	}
	if plan.IR == nil {
		return fmt.Errorf("semantic access plan has no PlanIR")
	}
	consumer.mu.RLock()
	admission, ok := consumer.admissions[plan.IR]
	consumer.mu.RUnlock()
	if !ok {
		return fmt.Errorf("plan was not admitted by this semantic access consumer")
	}
	canonical, err := plan.IR.Canonical()
	if err != nil {
		return fmt.Errorf("validate semantic access PlanIR: %w", err)
	}
	if !bytes.Equal(canonical, admission.canonical) {
		return fmt.Errorf("semantic access plan graph changed after admission")
	}
	if consumer.protected {
		_, decision, _, err := consumer.authority()
		if err != nil {
			return err
		}
		if decision.IdentityDigest != admission.decisionDigest {
			return fmt.Errorf("semantic access authority changed after admission")
		}
	}
	rendered, err := planir.RenderDuckDB(plan.IR)
	if err != nil {
		return fmt.Errorf("render semantic access PlanIR: %w", err)
	}
	if plan.SQL != rendered.SQL {
		return fmt.Errorf("plan SQL does not match its PlanIR renderer output")
	}
	if !reflect.DeepEqual(plan.Args, rendered.Args) {
		return fmt.Errorf("plan arguments do not match its PlanIR renderer output")
	}
	if !reflect.DeepEqual(plan.Columns, rendered.Columns) {
		return fmt.Errorf("plan columns do not match its PlanIR renderer output")
	}
	return nil
}
