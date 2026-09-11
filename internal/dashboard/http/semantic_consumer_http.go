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
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

var errDashboardSemanticAuthorityUnavailable = queryauthz.ErrSemanticConsumerAuthorityUnavailable

func dashboardSemanticAuthority(metrics Metrics) queryauthz.SemanticAuthorizationAdapter {
	adapter := queryauthz.SemanticAuthorizationAdapter{AllowMetricFieldFallback: true}
	if provider, ok := any(metrics).(interface {
		SemanticModel(string) (*semanticmodel.Model, bool)
	}); ok {
		adapter.Model = provider.SemanticModel
	}
	if provider, ok := any(metrics).(interface {
		SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	}); ok {
		adapter.Consumer = provider.SemanticConsumer
	}
	if provider, ok := any(metrics).(interface {
		AuthorizeSemanticTarget(context.Context, string, semanticquery.SemanticAccessTarget) error
	}); ok {
		adapter.TargetAuthorizer = provider.AuthorizeSemanticTarget
	}
	if provider, ok := any(metrics).(interface {
		AuthorizeSemanticField(context.Context, string, string, string) error
	}); ok {
		adapter.FieldAuthorizer = provider.AuthorizeSemanticField
	}
	if provider, ok := any(metrics).(interface {
		AuthorizeSemanticModelProjection(context.Context, string) error
	}); ok {
		adapter.ProjectionAuthorizer = provider.AuthorizeSemanticModelProjection
	}
	return adapter
}

func dashboardSemanticModel(metrics Metrics, modelID string) (*semanticmodel.Model, bool) {
	return dashboardSemanticAuthority(metrics).SemanticModel(modelID)
}

func dashboardProtectedSemanticModel(metrics Metrics, modelID string) bool {
	return dashboardSemanticAuthority(metrics).ProtectedSemanticModel(modelID)
}

func dashboardSemanticModelKnown(metrics Metrics, modelID string) bool {
	return dashboardSemanticAuthority(metrics).SemanticModelKnown(modelID)
}

func dashboardSemanticConsumerForRequest(ctx context.Context, metrics Metrics, modelID string) (context.Context, error) {
	return dashboardSemanticAuthority(metrics).SemanticConsumerForRequest(ctx, modelID)
}

func dashboardSemanticConsumerFromContext(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, bool) {
	return queryauthz.SemanticConsumerFromContext(ctx, modelID)
}

func authorizeDashboardSemanticConsumerField(consumer *semanticquery.SemanticAccessConsumer, dataset, field string) error {
	return queryauthz.AuthorizeSemanticConsumerField(consumer, dataset, field)
}

func authorizeDashboardSemanticConsumerProjection(consumer *semanticquery.SemanticAccessConsumer) error {
	return queryauthz.AuthorizeSemanticConsumerProjection(consumer)
}

func authorizeDashboardSemanticProjection(ctx context.Context, metrics Metrics, modelID string) error {
	return dashboardSemanticAuthority(metrics).AuthorizeSemanticModelProjection(ctx, modelID)
}

func authorizeDashboardSemanticTarget(ctx context.Context, metrics Metrics, modelID string, target semanticquery.SemanticAccessTarget) error {
	return dashboardSemanticAuthority(metrics).AuthorizeSemanticTarget(ctx, modelID, target)
}

func authorizeDashboardSemanticField(ctx context.Context, metrics Metrics, modelID, dataset, field string) error {
	return dashboardSemanticAuthority(metrics).AuthorizeSemanticField(ctx, modelID, dataset, field)
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
		} else if err := authorizeDashboardSemanticField(scoped, metrics, modelID, definition.Dataset, definition.Field); err != nil {
			return err
		}
		if optionDataset := strings.TrimSpace(definition.Options.Dataset); optionDataset != "" && optionDataset != strings.TrimSpace(definition.Dataset) {
			if err := authorizeDashboardSemanticField(scoped, metrics, modelID, optionDataset, definition.Field); err != nil {
				return err
			}
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
