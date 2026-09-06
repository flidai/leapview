package authz

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// ErrSemanticConsumerAuthorityUnavailable identifies a protected semantic
// consumer path that cannot obtain its request-bound authority. Callers map
// this separately from a normal target denial so missing composition fails
// closed without pretending that the resource is unknown.
var ErrSemanticConsumerAuthorityUnavailable = errors.New("semantic consumer authority is unavailable")

var (
	errSemanticPlannerUnavailable   = errors.New("semantic planner is unavailable")
	errSemanticPlannerSnapshotStale = errors.New("semantic execution snapshot is stale")
)

// SemanticPlanner supplies request-bound planning for consumer explain and
// pagination shape checks. The ordinary Planner port stays activation-owned.
func (m Metrics) SemanticPlanner(ctx context.Context, modelID string) (*semanticquery.Planner, error) {
	if m.Metrics == nil {
		return nil, errSemanticPlannerUnavailable
	}
	planner, ok := m.concretePlanner(modelID)
	if !ok || planner.CompiledModel() == nil {
		return nil, errSemanticPlannerUnavailable
	}
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok {
		return nil, errSemanticPlannerSnapshotStale
	}
	return m.bindSemanticPlanner(ctx, planner, model)
}

// bindSemanticPlanner retains the already selected activation planner. Whole
// projections must not resolve a second generation while inspecting members.
func (m Metrics) bindSemanticPlanner(ctx context.Context, planner *semanticquery.Planner, model *semanticmodel.Model) (*semanticquery.Planner, error) {
	if planner == nil || planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model) {
		return nil, errSemanticPlannerSnapshotStale
	}
	policy := planner.CompiledModel().SemanticAccessPolicy()
	if policy == nil || !policy.Protected() {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			return nil, fmt.Errorf("compiled semantic policy is unavailable")
		}
		return planner, nil
	}
	if m.resolveSemanticAttributes == nil || m.snapshotFromContext == nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	resolved, err := m.resolveSemanticAttributes(ctx)
	if err != nil || resolved.Subject.Kind != access.SubjectKindPrincipal {
		return nil, DeniedError{Capability: access.CapabilityResourceUse}
	}
	snapshot, err := m.snapshotFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := snapshot.ValidateBound(); err != nil {
		return nil, err
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessEvaluationContext{
		RegistryState: resolved.Registry.State, ControlState: resolved.ControlState, Attributes: resolved.Attributes,
	}, resolved.Subject.ID, snapshot.Identity().GenerationID)
	if err != nil {
		return nil, DeniedError{Capability: access.CapabilityResourceUse}
	}
	return consumer.Planner(), nil
}

// AuthorizeSemanticModelProjection admits a complete semantic model
// projection using one request-bound planner. It intentionally performs no
// execution: PlanRows and Plan only build and secure throwaway plans. The
// projection boundary is whole-model and therefore conservative; any denied
// dataset, dimension binding, metric, or transitive metric dependency denies
// the complete projection because this consumer has no member-level spec
// projection.
func (m Metrics) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	denied := DeniedError{Capability: access.CapabilityResourceUse}
	if m.Metrics == nil {
		return denied
	}
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return denied
	}
	authoredProtected := semanticquery.ModelRequiresSemanticAccess(model)
	activation, available := m.concretePlanner(modelID)
	if !available {
		if authoredProtected {
			return ErrSemanticConsumerAuthorityUnavailable
		}
		return nil
	}
	compiled := activation.CompiledModel()
	if compiled == nil || !compiled.MatchesModel(model) {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	compiledProtected := compiled.SemanticAccessPolicy().Protected()
	if authoredProtected != compiledProtected {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	if !compiledProtected {
		return nil
	}
	planner, err := m.bindSemanticPlanner(ctx, activation, model)
	if err != nil {
		return err
	}

	// PlanRows validates each semantic dimension in each binding and also
	// forces the root dataset's security barrier. A dataset without a semantic
	// dimension still gets a representative physical field so its dataset
	// policy is evaluated. The compiled graph supplies the field facts; model
	// maps are used only for stable semantic-member names after the fingerprint
	// match above.
	datasetNames := compiled.DatasetNames()
	dimensionNames := make([]string, 0, len(model.Dimensions))
	for name := range model.Dimensions {
		dimensionNames = append(dimensionNames, name)
	}
	sort.Strings(dimensionNames)
	for _, dataset := range datasetNames {
		boundDimension := false
		for _, dimension := range dimensionNames {
			if _, ok := compiled.DimensionBinding(dimension, dataset); !ok {
				continue
			}
			boundDimension = true
			if _, err := planner.PlanRows(semanticquery.RowRequest{
				Dataset: dataset, Dimensions: []semanticquery.Field{{Field: dimension}}, Limit: 1,
			}); err != nil {
				return denied
			}
		}
		if boundDimension {
			continue
		}
		datasetFacts, ok := compiled.Dataset(dataset)
		if !ok {
			return denied
		}
		table := datasetFacts.Table()
		fieldNames := make([]string, 0, len(table.Dimensions))
		for field := range table.Dimensions {
			fieldNames = append(fieldNames, field)
		}
		sort.Strings(fieldNames)
		if len(fieldNames) == 0 {
			return denied
		}
		if _, err := planner.PlanRows(semanticquery.RowRequest{
			Dataset: dataset, Dimensions: []semanticquery.Field{{Field: dataset + "." + fieldNames[0]}}, Limit: 1,
		}); err != nil {
			return denied
		}
	}

	metricNames := make([]string, 0, len(model.Metrics))
	for name := range model.Metrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	for _, metric := range metricNames {
		if _, err := planner.Plan(semanticquery.Request{Metrics: []semanticquery.Field{{Field: metric}}}); err != nil {
			return denied
		}
	}
	return nil
}

// SemanticConsumerCacheAllowed does not grant access. It prevents consumers
// above materialization from reusing protected results before FAI-645 binds
// lifecycle evidence to those existing cache identities.
func (m Metrics) SemanticConsumerCacheAllowed(modelID string) bool {
	if m.Metrics == nil {
		return false
	}
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return false
	}
	// If an activation planner is available, its compiled snapshot is the
	// authority. A model can be mutated after activation; falling back to the
	// authored policy in that case would let a protected compiled policy use a
	// shared cache under an apparently ordinary model.
	if planner, available := m.concretePlanner(modelID); available {
		compiled := planner.CompiledModel()
		if compiled == nil || !compiled.MatchesModel(model) {
			return false
		}
		policy := compiled.SemanticAccessPolicy()
		return policy == nil || !policy.Protected()
	}
	return !semanticquery.ModelRequiresSemanticAccess(model)
}

// AuthorizeSemanticField checks a suggestion/static-option field through the
// same planner used for data. The representative row plan is never executed.
func (m Metrics) AuthorizeSemanticField(ctx context.Context, modelID, dataset, field string) error {
	planner, err := m.SemanticPlanner(ctx, modelID)
	if err != nil {
		return err
	}
	_, err = planner.PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
	if err != nil {
		return DeniedError{Capability: access.CapabilityResourceUse}
	}
	return nil
}

// AuthorizeSemanticTarget is the context-aware discovery port. It consults
// the same activation policy as execution and never mutates the model or
// manufactures an authorized planner from a filtered model projection.
func (m Metrics) AuthorizeSemanticTarget(ctx context.Context, modelID string, target semanticquery.SemanticAccessTarget) error {
	denied := DeniedError{Capability: access.CapabilityResourceUse}
	if m.Metrics == nil {
		return denied
	}
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return denied
	}
	planner, ok := m.concretePlanner(modelID)
	if !ok {
		// Ordinary, unprotected models retain their existing discovery path when
		// no activation planner port is installed. Protected models fail closed.
		if !semanticquery.ModelRequiresSemanticAccess(model) {
			return nil
		}
		return denied
	}
	compiled := planner.CompiledModel()
	if compiled == nil || !compiled.MatchesModel(model) {
		return denied
	}
	policy := compiled.SemanticAccessPolicy()
	if policy == nil || !policy.Protected() {
		if !semanticquery.ModelRequiresSemanticAccess(model) {
			return nil
		}
		return denied
	}
	if m.resolveSemanticAttributes == nil {
		return denied
	}
	resolution, err := m.resolveSemanticAttributes(ctx)
	if err != nil || resolution.Subject.Kind != access.SubjectKindPrincipal || resolution.Subject.ID == "" {
		return denied
	}
	evaluation := semanticquery.SemanticAccessEvaluationContext{
		RegistryState: resolution.Registry.State, ControlState: resolution.ControlState, Attributes: resolution.Attributes,
	}
	if !policy.Allows(target, evaluation) {
		return denied
	}
	return nil
}
