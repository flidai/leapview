package authz

import (
	"context"
	"errors"
	"sort"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// ErrSemanticConsumerAuthorityUnavailable is not a resource-not-found result:
// a consumer without authoritative evidence must fail closed.
var ErrSemanticConsumerAuthorityUnavailable = errors.New("semantic consumer authority is unavailable")

func (m Metrics) bindSemanticQuery(ctx context.Context, request dataquery.Query) (context.Context, error) {
	if request.Kind == dataquery.KindModelRows {
		return ctx, nil
	}
	return m.BindSemanticConsumer(ctx, request.ModelID)
}

// SemanticConsumer is the common discovery port for consumers projecting an
// already selected immutable model. The returned source fingerprint must
// match that projection before any member metadata is released.
func (m Metrics) SemanticConsumer(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, error) {
	return m.semanticConsumer(ctx, modelID)
}

// BindSemanticConsumer is also an optional governor capability. Dashboard
// execution selects its model below the resource governor, so materialize may
// request this binding there without accepting query-supplied authority.
func (m Metrics) BindSemanticConsumer(ctx context.Context, modelID string) (context.Context, error) {
	if m.Metrics == nil {
		return ctx, ErrSemanticConsumerAuthorityUnavailable
	}
	model, ok := m.Metrics.SemanticModel(modelID)
	if !ok || model == nil {
		return ctx, ErrSemanticConsumerAuthorityUnavailable
	}
	if model.AccessPolicy.Empty() {
		if planner, available := m.concretePlanner(modelID); available && (planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model)) {
			return ctx, ErrSemanticConsumerAuthorityUnavailable
		}
		return ctx, nil
	}
	consumer, err := m.semanticConsumer(ctx, modelID)
	if err != nil {
		return ctx, err
	}
	snapshot, err := m.snapshotFromContext(ctx)
	if err != nil || snapshot.ValidateBound() != nil {
		return ctx, ErrSemanticConsumerAuthorityUnavailable
	}
	principal, ok := m.principalFromContext(ctx)
	if !ok || principal.DevBypass {
		return ctx, ErrSemanticConsumerAuthorityUnavailable
	}
	identity := snapshot.Identity()
	bound := semanticquery.WithSemanticAccessConsumer(ctx, consumer, semanticquery.SemanticAccessConsumerBinding{
		InstanceID: m.instanceID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		Generation: identity.GenerationID, ModelID: modelID, PrincipalID: principal.ID,
	})
	if _, valid := semanticquery.SemanticAccessConsumerContextFromContext(bound); !valid {
		return ctx, ErrSemanticConsumerAuthorityUnavailable
	}
	return bound, nil
}

// semanticConsumer binds the activation-owned model to Access-owned evidence.
// Browser query fields, workload admission identities and document sharing do
// not supply principals, assignments or policy authority.
func (m Metrics) semanticConsumer(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, error) {
	if m.Metrics == nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	planner, ok := m.concretePlanner(modelID)
	model, found := m.Metrics.SemanticModel(modelID)
	if !ok || !found || model == nil || planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model) {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	if model.AccessPolicy.Empty() {
		return semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{})
	}
	plannerPort, available := m.Metrics.(interface {
		SemanticPlannerSnapshot(context.Context, string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error)
	})
	if !available {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	planner, snapshot, err := plannerPort.SemanticPlannerSnapshot(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if planner == nil || planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model) {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	if m.instanceID == "" || m.snapshotFromContext == nil || m.resolveSemanticAttributes == nil || m.principalFromContext == nil {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	principal, ok := m.principalFromContext(ctx)
	if !ok || principal.ID == "" || principal.DevBypass {
		return nil, DeniedError{Capability: access.CapabilityResourceUse}
	}
	if err := snapshot.ValidateBound(); err != nil {
		return nil, err
	}
	identity := snapshot.Identity()
	provider := func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
		var attributes semanticquery.SemanticAccessAttributeSnapshot
		var authority semanticquery.SemanticAccessAuthority
		currentPrincipal, authenticated := m.principalFromContext(ctx)
		if !authenticated || currentPrincipal.ID != principal.ID || currentPrincipal.DevBypass {
			return attributes, authority, ErrSemanticConsumerAuthorityUnavailable
		}
		current, err := m.snapshotFromContext(ctx)
		if err != nil || current.ValidateBound() != nil || current.Identity() != identity {
			return attributes, authority, ErrSemanticConsumerAuthorityUnavailable
		}
		resolved, err := m.resolveSemanticAttributes(ctx)
		if err != nil {
			return attributes, authority, err
		}
		if resolved.Subject.Kind != access.SubjectKindPrincipal || resolved.Subject.ID != principal.ID {
			return attributes, authority, ErrSemanticConsumerAuthorityUnavailable
		}
		return semanticquery.SemanticAccessResolutionSnapshot(m.instanceID, principal.ID, resolved)
	}
	return semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
		InstanceID: m.instanceID, ModelID: modelID, Generation: identity.GenerationID,
		PrincipalID: principal.ID, Authority: provider,
	})
}

// SemanticPlanner is the context-aware explain and query-shape port. Planner
// remains activation-owned; no caller may mutate its shared authorization.
func (m Metrics) SemanticPlanner(ctx context.Context, modelID string) (*semanticquery.Planner, error) {
	consumer, err := m.semanticConsumer(ctx, modelID)
	if err != nil {
		return nil, err
	}
	return consumer.Planner(), nil
}

func (m Metrics) AuthorizeSemanticTarget(ctx context.Context, modelID string, target semanticquery.SemanticAccessTarget) error {
	consumer, err := m.semanticConsumer(ctx, modelID)
	if err != nil {
		return err
	}
	return consumer.Authorize(target)
}

// Whole-document projections have no partial-member contract. Admit every
// canonical member through one consumer, or deny the projection as a whole.
func (m Metrics) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	consumer, err := m.semanticConsumer(ctx, modelID)
	if err != nil {
		return err
	}
	model := consumer.Planner().CompiledModel().SourceModel()
	for _, dataset := range consumer.Planner().CompiledModel().DatasetNames() {
		if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
		dimensions := make([]string, 0, len(model.Dimensions))
		for name := range model.Dimensions {
			dimensions = append(dimensions, name)
		}
		sort.Strings(dimensions)
		for _, name := range dimensions {
			if _, bound := consumer.Planner().CompiledModel().DimensionBinding(name, dataset); bound {
				if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: name}); err != nil {
					return err
				}
			}
		}
	}
	metrics := make([]string, 0, len(model.Metrics))
	for name := range model.Metrics {
		metrics = append(metrics, name)
	}
	sort.Strings(metrics)
	for _, name := range metrics {
		if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Metric: name}); err != nil {
			return err
		}
	}
	return nil
}

// AuthorizeSemanticField also covers physical field names resolved by the
// planner. It builds no data result and cannot authorize a presentation alias.
func (m Metrics) AuthorizeSemanticField(ctx context.Context, modelID, dataset, field string) error {
	planner, err := m.SemanticPlanner(ctx, modelID)
	if err != nil {
		return err
	}
	_, err = planner.PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
	return err
}

// Protected reuse is unavailable until FAI-645 supplies lifecycle-bound cache
// evidence. This is a denial boundary, not a new cache implementation.
func (m Metrics) SemanticConsumerCacheAllowed(modelID string) bool {
	if m.Metrics == nil {
		return false
	}
	model, found := m.Metrics.SemanticModel(modelID)
	planner, available := m.concretePlanner(modelID)
	return found && model != nil && available && planner.CompiledModel() != nil && planner.CompiledModel().MatchesModel(model) && model.AccessPolicy.Empty()
}
