package authz

import (
	"context"
	"fmt"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// SemanticAuthorizationAdapter is the narrow surface adapter for the shared
// semantic authorization implementation. HTTP and semantic metadata APIs may
// provide different metrics façades, but authorization decisions must be made
// by the same code below.
//
// Model is required for fail-closed checks. The remaining providers are
// optional compatibility ports for surfaces whose composition exposes them.
type SemanticAuthorizationAdapter struct {
	Model                    func(modelID string) (*semanticmodel.Model, bool)
	Planner                  func(modelID string) (*semanticquery.Planner, bool)
	ContextPlanner           func(context.Context, string) (*semanticquery.Planner, error)
	Consumer                 func(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	TargetAuthorizer         func(context.Context, string, semanticquery.SemanticAccessTarget) error
	FieldAuthorizer          func(context.Context, string, string, string) error
	ProjectionAuthorizer     func(context.Context, string) error
	AllowMetricFieldFallback bool
	RequireConsumerModelID   bool
}

type semanticAuthorizationContextKey struct{}

type semanticAuthorizationBinding struct {
	modelID  string
	consumer *semanticquery.SemanticAccessConsumer
}

// SemanticModel returns the adapter's authoritative model projection.
func (a SemanticAuthorizationAdapter) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if a.Model == nil {
		return nil, false
	}
	model, ok := a.Model(modelID)
	return model, ok && model != nil
}

// ProtectedSemanticModel reports whether the selected model has an access
// policy and therefore cannot fall back to unbound public behavior.
func (a SemanticAuthorizationAdapter) ProtectedSemanticModel(modelID string) bool {
	model, ok := a.SemanticModel(modelID)
	return ok && !model.AccessPolicy.Empty()
}

// SemanticModelKnown reports whether authoritative model metadata is present.
func (a SemanticAuthorizationAdapter) SemanticModelKnown(modelID string) bool {
	_, ok := a.SemanticModel(modelID)
	return ok
}

// SemanticConsumerFromContext retrieves the shared request binding.
func SemanticConsumerFromContext(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, bool) {
	if ctx == nil {
		return nil, false
	}
	binding, ok := ctx.Value(semanticAuthorizationContextKey{}).(semanticAuthorizationBinding)
	return binding.consumer, ok && binding.modelID == modelID && binding.consumer != nil
}

// SemanticConsumerForRequest binds a metrics-provided consumer after checking
// that its compiled model is the authoritative model selected by the caller.
func (a SemanticAuthorizationAdapter) SemanticConsumerForRequest(ctx context.Context, modelID string) (context.Context, error) {
	if _, ok := SemanticConsumerFromContext(ctx, modelID); ok {
		return ctx, nil
	}
	if a.Consumer == nil {
		return ctx, nil
	}
	consumer, err := a.Consumer(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if consumer == nil || !a.plannerMatchesModel(modelID, consumer.Planner()) {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	if a.RequireConsumerModelID && a.ProtectedSemanticModel(modelID) && consumer.ModelID() != modelID {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	return context.WithValue(ctx, semanticAuthorizationContextKey{}, semanticAuthorizationBinding{modelID: modelID, consumer: consumer}), nil
}

// AuthorizeSemanticConsumerField checks a field through an already-bound
// semantic consumer's planner.
func AuthorizeSemanticConsumerField(consumer *semanticquery.SemanticAccessConsumer, dataset, field string) error {
	if consumer == nil || consumer.Planner() == nil {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	_, err := consumer.Planner().PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
	return err
}

// AuthorizeSemanticConsumerProjection checks every dataset, bound dimension,
// and metric exposed by a consumer in deterministic order.
func AuthorizeSemanticConsumerProjection(consumer *semanticquery.SemanticAccessConsumer) error {
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	planner := consumer.Planner()
	compiled := planner.CompiledModel()
	model := compiled.SourceModel()
	if model == nil {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	for _, dataset := range compiled.DatasetNames() {
		if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
		dimensions := make([]string, 0, len(model.Dimensions))
		for name := range model.Dimensions {
			dimensions = append(dimensions, name)
		}
		sort.Strings(dimensions)
		for _, name := range dimensions {
			if _, bound := compiled.DimensionBinding(name, dataset); !bound {
				continue
			}
			if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset, Dimension: name}); err != nil {
				return err
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

// SemanticPlannerForRequest resolves the activation planner for explain and
// metadata requests, preserving authority-provider errors verbatim.
func (a SemanticAuthorizationAdapter) SemanticPlannerForRequest(ctx context.Context, modelID string) (*semanticquery.Planner, error) {
	if consumer, ok := SemanticConsumerFromContext(ctx, modelID); ok {
		planner := consumer.Planner()
		if planner == nil || !a.plannerMatchesModel(modelID, planner) {
			return nil, ErrSemanticConsumerAuthorityUnavailable
		}
		return planner, nil
	}
	if a.ContextPlanner != nil {
		planner, err := a.ContextPlanner(ctx, modelID)
		if err != nil {
			return nil, err
		}
		if planner != nil && a.plannerMatchesModel(modelID, planner) {
			return planner, nil
		}
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	if !a.SemanticModelKnown(modelID) || a.ProtectedSemanticModel(modelID) {
		return nil, ErrSemanticConsumerAuthorityUnavailable
	}
	planner, ok := a.planner(modelID)
	if !ok || planner == nil || !a.plannerMatchesModel(modelID, planner) {
		return nil, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return planner, nil
}

// AuthorizeSemanticTarget authorizes a dataset or metric target through the
// bound consumer, then compatible provider ports, and finally public fallback.
func (a SemanticAuthorizationAdapter) AuthorizeSemanticTarget(ctx context.Context, modelID string, target semanticquery.SemanticAccessTarget) error {
	if consumer, ok := SemanticConsumerFromContext(ctx, modelID); ok {
		return consumer.Authorize(target)
	}
	if a.Consumer != nil {
		scoped, err := a.SemanticConsumerForRequest(ctx, modelID)
		if err != nil {
			return err
		}
		if consumer, ok := SemanticConsumerFromContext(scoped, modelID); ok {
			return consumer.Authorize(target)
		}
	}
	if a.TargetAuthorizer != nil {
		return a.TargetAuthorizer(ctx, modelID, target)
	}
	if !a.SemanticModelKnown(modelID) || a.ProtectedSemanticModel(modelID) {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	return nil
}

// AuthorizeSemanticField authorizes a physical field through the same
// consumer/provider chain as targets. Metric fallback is enabled only for the
// dashboard HTTP adapter, whose legacy public surface used metric targets for
// this path.
func (a SemanticAuthorizationAdapter) AuthorizeSemanticField(ctx context.Context, modelID, dataset, field string) error {
	if consumer, ok := SemanticConsumerFromContext(ctx, modelID); ok {
		return AuthorizeSemanticConsumerField(consumer, dataset, field)
	}
	if a.Consumer != nil {
		scoped, err := a.SemanticConsumerForRequest(ctx, modelID)
		if err != nil {
			return err
		}
		if consumer, ok := SemanticConsumerFromContext(scoped, modelID); ok {
			return AuthorizeSemanticConsumerField(consumer, dataset, field)
		}
	}
	if a.AllowMetricFieldFallback {
		if model, ok := a.SemanticModel(modelID); ok {
			if _, isMetric := model.Metrics[field]; isMetric {
				return a.AuthorizeSemanticTarget(ctx, modelID, semanticquery.SemanticAccessTarget{Metric: field, Dataset: dataset})
			}
		}
	}
	if a.FieldAuthorizer != nil {
		return a.FieldAuthorizer(ctx, modelID, dataset, field)
	}
	if !a.SemanticModelKnown(modelID) || a.ProtectedSemanticModel(modelID) {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	return nil
}

// AuthorizeSemanticModelProjection authorizes a complete model projection.
func (a SemanticAuthorizationAdapter) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	if consumer, ok := SemanticConsumerFromContext(ctx, modelID); ok {
		return AuthorizeSemanticConsumerProjection(consumer)
	}
	if a.Consumer != nil {
		scoped, err := a.SemanticConsumerForRequest(ctx, modelID)
		if err != nil {
			return err
		}
		if consumer, ok := SemanticConsumerFromContext(scoped, modelID); ok {
			return AuthorizeSemanticConsumerProjection(consumer)
		}
	}
	if a.ProjectionAuthorizer != nil {
		return a.ProjectionAuthorizer(ctx, modelID)
	}
	if !a.SemanticModelKnown(modelID) || a.ProtectedSemanticModel(modelID) {
		return ErrSemanticConsumerAuthorityUnavailable
	}
	return nil
}

func (a SemanticAuthorizationAdapter) planner(modelID string) (*semanticquery.Planner, bool) {
	if a.Planner == nil {
		return nil, false
	}
	return a.Planner(modelID)
}

func (a SemanticAuthorizationAdapter) plannerMatchesModel(modelID string, planner *semanticquery.Planner) bool {
	model, ok := a.SemanticModel(modelID)
	return ok && planner != nil && planner.CompiledModel() != nil && planner.CompiledModel().MatchesModel(model)
}
