package http

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

var errDashboardSemanticAuthorityUnavailable = errors.New("semantic consumer authority is unavailable")

type dashboardSemanticModelProvider interface {
	SemanticModel(string) (*semanticmodel.Model, bool)
}

type dashboardSemanticTargetAuthorizer interface {
	AuthorizeSemanticTarget(context.Context, string, semanticquery.SemanticAccessTarget) error
}

type dashboardSemanticFieldAuthorizer interface {
	AuthorizeSemanticField(context.Context, string, string, string) error
}

type dashboardSemanticProjectionAuthorizer interface {
	AuthorizeSemanticModelProjection(context.Context, string) error
}

type dashboardSemanticConsumerProvider interface {
	SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
}

type dashboardSemanticConsumerContextKey struct{}

type dashboardSemanticConsumerBinding struct {
	modelID  string
	consumer *semanticquery.SemanticAccessConsumer
}

func dashboardSemanticModel(metrics Metrics, modelID string) (*semanticmodel.Model, bool) {
	provider, ok := any(metrics).(dashboardSemanticModelProvider)
	if !ok {
		return nil, false
	}
	model, ok := provider.SemanticModel(modelID)
	return model, ok && model != nil
}

func dashboardProtectedSemanticModel(metrics Metrics, modelID string) bool {
	model, ok := dashboardSemanticModel(metrics, modelID)
	return ok && !model.AccessPolicy.Empty()
}

func dashboardSemanticModelKnown(metrics Metrics, modelID string) bool {
	_, ok := dashboardSemanticModel(metrics, modelID)
	return ok
}

func dashboardSemanticConsumerForRequest(ctx context.Context, metrics Metrics, modelID string) (context.Context, error) {
	if binding, ok := ctx.Value(dashboardSemanticConsumerContextKey{}).(dashboardSemanticConsumerBinding); ok && binding.modelID == modelID && binding.consumer != nil {
		return ctx, nil
	}
	provider, ok := any(metrics).(dashboardSemanticConsumerProvider)
	if !ok {
		return ctx, nil
	}
	consumer, err := provider.SemanticConsumer(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil {
		return nil, errDashboardSemanticAuthorityUnavailable
	}
	model, ok := dashboardSemanticModel(metrics, modelID)
	if !ok || !consumer.Planner().CompiledModel().MatchesModel(model) {
		return nil, errDashboardSemanticAuthorityUnavailable
	}
	return context.WithValue(ctx, dashboardSemanticConsumerContextKey{}, dashboardSemanticConsumerBinding{modelID: modelID, consumer: consumer}), nil
}

func dashboardSemanticConsumerFromContext(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, bool) {
	binding, ok := ctx.Value(dashboardSemanticConsumerContextKey{}).(dashboardSemanticConsumerBinding)
	return binding.consumer, ok && binding.modelID == modelID && binding.consumer != nil
}

func authorizeDashboardSemanticConsumerField(consumer *semanticquery.SemanticAccessConsumer, dataset, field string) error {
	if consumer == nil || consumer.Planner() == nil {
		return errDashboardSemanticAuthorityUnavailable
	}
	_, err := consumer.Planner().PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
	return err
}

func authorizeDashboardSemanticConsumerProjection(consumer *semanticquery.SemanticAccessConsumer) error {
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil {
		return errDashboardSemanticAuthorityUnavailable
	}
	planner := consumer.Planner()
	compiled := planner.CompiledModel()
	model := compiled.SourceModel()
	if model == nil {
		return errDashboardSemanticAuthorityUnavailable
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

func authorizeDashboardSemanticProjection(ctx context.Context, metrics Metrics, modelID string) error {
	if consumer, ok := dashboardSemanticConsumerFromContext(ctx, modelID); ok {
		return authorizeDashboardSemanticConsumerProjection(consumer)
	}
	if scoped, err := dashboardSemanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(dashboardSemanticConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := dashboardSemanticConsumerFromContext(scoped, modelID); ok {
		return authorizeDashboardSemanticConsumerProjection(consumer)
	}
	if provider, ok := any(metrics).(dashboardSemanticProjectionAuthorizer); ok {
		return provider.AuthorizeSemanticModelProjection(ctx, modelID)
	}
	if !dashboardSemanticModelKnown(metrics, modelID) || dashboardProtectedSemanticModel(metrics, modelID) {
		return errDashboardSemanticAuthorityUnavailable
	}
	return nil
}

func authorizeDashboardSemanticTarget(ctx context.Context, metrics Metrics, modelID string, target semanticquery.SemanticAccessTarget) error {
	if consumer, ok := dashboardSemanticConsumerFromContext(ctx, modelID); ok {
		return consumer.Authorize(target)
	}
	if scoped, err := dashboardSemanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(dashboardSemanticConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := dashboardSemanticConsumerFromContext(scoped, modelID); ok {
		return consumer.Authorize(target)
	}
	if provider, ok := any(metrics).(dashboardSemanticTargetAuthorizer); ok {
		return provider.AuthorizeSemanticTarget(ctx, modelID, target)
	}
	if !dashboardSemanticModelKnown(metrics, modelID) || dashboardProtectedSemanticModel(metrics, modelID) {
		return errDashboardSemanticAuthorityUnavailable
	}
	return nil
}

func authorizeDashboardSemanticField(ctx context.Context, metrics Metrics, modelID, dataset, field string) error {
	if consumer, ok := dashboardSemanticConsumerFromContext(ctx, modelID); ok {
		return authorizeDashboardSemanticConsumerField(consumer, dataset, field)
	}
	if scoped, err := dashboardSemanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(dashboardSemanticConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := dashboardSemanticConsumerFromContext(scoped, modelID); ok {
		return authorizeDashboardSemanticConsumerField(consumer, dataset, field)
	}
	if model, ok := dashboardSemanticModel(metrics, modelID); ok {
		if _, isMetric := model.Metrics[field]; isMetric {
			return authorizeDashboardSemanticTarget(ctx, metrics, modelID, semanticquery.SemanticAccessTarget{Metric: field, Dataset: dataset})
		}
	}
	if provider, ok := any(metrics).(dashboardSemanticFieldAuthorizer); ok {
		return provider.AuthorizeSemanticField(ctx, modelID, dataset, field)
	}
	if !dashboardSemanticModelKnown(metrics, modelID) || dashboardProtectedSemanticModel(metrics, modelID) {
		return errDashboardSemanticAuthorityUnavailable
	}
	return nil
}

func authorizeDashboardFilterField(ctx context.Context, metrics Metrics, dashboardID, dataset, field string) error {
	modelID := metrics.ModelIDForDashboard(dashboardID)
	if strings.TrimSpace(dataset) == "" {
		// A compiled filter without a dataset is not a safe field-level
		// reference. Admit the already compiled semantic projection once so
		// public metadata keeps its historical behavior without asking the
		// planner to build an impossible dataset-less row query.
		return authorizeDashboardSemanticProjection(ctx, metrics, modelID)
	}
	return authorizeDashboardSemanticField(ctx, metrics, modelID, dataset, field)
}

func dashboardSemanticDenied(err error) bool {
	return strings.Contains(err.Error(), "semantic access denied") || strings.Contains(err.Error(), "lacks")
}

func dashboardSemanticUnavailable(err error) bool {
	return errors.Is(err, errDashboardSemanticAuthorityUnavailable) || strings.Contains(err.Error(), "semantic consumer authority is unavailable")
}

func authorizeDashboardVisual(ctx context.Context, metrics Metrics, dashboardID string, definition visualizationdefinition.Definition) error {
	query := definition.Query
	modelID := query.ModelID
	if modelID == "" {
		modelID = metrics.ModelIDForDashboard(dashboardID)
	}
	if scoped, err := dashboardSemanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		return err
	} else {
		ctx = scoped
	}
	return authorizeDashboardSemanticProjection(ctx, metrics, modelID)
}

func dashboardSemanticCacheAllowed(metrics Metrics, modelID string) bool {
	if provider, ok := any(metrics).(interface{ SemanticConsumerCacheAllowed(string) bool }); ok {
		return provider.SemanticConsumerCacheAllowed(modelID)
	}
	model, ok := dashboardSemanticModel(metrics, modelID)
	return ok && model != nil && model.AccessPolicy.Empty()
}

func authorizeDashboardReportVisuals(ctx context.Context, metrics Metrics, dashboardID string, report dashboarddefinition.Definition) error {
	ids := make([]string, 0, len(report.Visualizations))
	for id := range report.Visualizations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	contexts := make(map[string]context.Context)
	authorizedModels := make(map[string]bool)
	for _, id := range ids {
		definition := report.Visualizations[id]
		modelID := definition.Query.ModelID
		if modelID == "" {
			modelID = metrics.ModelIDForDashboard(dashboardID)
		}
		scoped, ok := contexts[modelID]
		if !ok {
			var err error
			scoped, err = dashboardSemanticConsumerForRequest(ctx, metrics, modelID)
			if err != nil {
				return err
			}
			contexts[modelID] = scoped
		}
		if !authorizedModels[modelID] {
			if err := authorizeDashboardSemanticProjection(scoped, metrics, modelID); err != nil {
				return err
			}
			authorizedModels[modelID] = true
		}
	}
	filterIDs := make([]string, 0, len(report.FilterDefinitions))
	for id := range report.FilterDefinitions {
		filterIDs = append(filterIDs, id)
	}
	sort.Strings(filterIDs)
	for _, id := range filterIDs {
		definition := report.FilterDefinitions[id]
		modelID := metrics.ModelIDForDashboard(dashboardID)
		scoped, ok := contexts[modelID]
		if !ok {
			var err error
			scoped, err = dashboardSemanticConsumerForRequest(ctx, metrics, modelID)
			if err != nil {
				return err
			}
			contexts[modelID] = scoped
		}
		if strings.TrimSpace(definition.Dataset) == "" {
			if !authorizedModels[modelID] {
				if err := authorizeDashboardSemanticProjection(scoped, metrics, modelID); err != nil {
					return err
				}
				authorizedModels[modelID] = true
			}
			continue
		}
		if err := authorizeDashboardSemanticField(scoped, metrics, modelID, definition.Dataset, definition.Field); err != nil {
			return err
		}
	}
	return nil
}

func dashboardSemanticAuthorizationStatus(err error) int {
	if dashboardSemanticUnavailable(err) {
		return 503
	}
	if dashboardSemanticDenied(err) {
		return 403
	}
	return 503
}

func requireDashboardSemanticAuthorization(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("dashboard semantic authorization: %w", err)
}
