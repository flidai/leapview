package http

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
)

var errSemanticConsumerUnavailable = queryauthz.ErrSemanticConsumerAuthorityUnavailable

func protectedSemanticModel(metrics Metrics, modelID string) bool {
	return semanticAuthorizationAdapter(metrics).ProtectedSemanticModel(modelID)
}

func semanticModelSnapshot(metrics Metrics, modelID string) (*semanticmodel.Model, bool) {
	if planner, ok := semanticPlanner(metrics, modelID); ok && planner != nil && planner.CompiledModel() != nil {
		if model := planner.CompiledModel().SourceModel(); model != nil {
			return model, true
		}
	}
	model := semanticModelForID(metrics, modelID)
	return model, model != nil
}

func semanticModelKnown(metrics Metrics, modelID string) bool {
	return semanticAuthorizationAdapter(metrics).SemanticModelKnown(modelID)
}

func semanticAuthorizationAdapter(metrics Metrics) queryauthz.SemanticAuthorizationAdapter {
	adapter := queryauthz.SemanticAuthorizationAdapter{
		Model: func(modelID string) (*semanticmodel.Model, bool) {
			return semanticModelSnapshot(metrics, modelID)
		},
		Planner: func(modelID string) (*semanticquery.Planner, bool) {
			return semanticPlanner(metrics, modelID)
		},
		RequireConsumerModelID: true,
	}
	if provider, ok := any(metrics).(interface {
		SemanticPlanner(context.Context, string) (*semanticquery.Planner, error)
	}); ok {
		adapter.ContextPlanner = provider.SemanticPlanner
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

func semanticConsumerFromContext(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, bool) {
	return queryauthz.SemanticConsumerFromContext(ctx, modelID)
}

func authorizeSemanticConsumerProjection(consumer *semanticquery.SemanticAccessConsumer) error {
	return queryauthz.AuthorizeSemanticConsumerProjection(consumer)
}

func semanticPlannerForRequest(ctx context.Context, metrics Metrics, modelID string) (*semanticquery.Planner, error) {
	return semanticAuthorizationAdapter(metrics).SemanticPlannerForRequest(ctx, modelID)
}

func authorizeSemanticTarget(ctx context.Context, metrics Metrics, modelID string, target semanticquery.SemanticAccessTarget) error {
	return semanticAuthorizationAdapter(metrics).AuthorizeSemanticTarget(ctx, modelID, target)
}

func authorizeSemanticField(ctx context.Context, metrics Metrics, modelID, dataset, field string) error {
	return semanticAuthorizationAdapter(metrics).AuthorizeSemanticField(ctx, modelID, dataset, field)
}

func authorizeSemanticModelProjection(ctx context.Context, metrics Metrics, modelID string) error {
	return semanticAuthorizationAdapter(metrics).AuthorizeSemanticModelProjection(ctx, modelID)
}

func semanticConsumerForRequest(ctx context.Context, metrics Metrics, modelID string) (context.Context, error) {
	return semanticAuthorizationAdapter(metrics).SemanticConsumerForRequest(ctx, modelID)
}

// authorizeSemanticRequest admits a query shape before either explain output
// or execution. The context-aware planner remains the final authority for
// complex dependency/filter paths; these explicit targets ensure simple
// dataset/member requests cannot expose a protected member first.
func authorizeSemanticRequest(ctx context.Context, metrics Metrics, modelID string, request any) error {
	var dataset string
	var dimensions []semanticquery.Field
	var metricsFields []semanticquery.Field
	var filters []semanticquery.Filter
	var timeField string
	switch value := request.(type) {
	case semanticquery.Request:
		dataset, dimensions, metricsFields, filters, timeField = value.Dataset, value.Dimensions, value.Metrics, value.Filters, value.Time.Field
	case semanticquery.RowRequest:
		dataset, dimensions, metricsFields, filters = value.Dataset, value.Dimensions, value.Metrics, value.Filters
	default:
		return fmt.Errorf("unsupported semantic request type %T", request)
	}
	if dataset != "" {
		if err := authorizeSemanticTarget(ctx, metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
	}
	for _, field := range dimensions {
		fieldDataset, fieldName := dataset, field.Field
		if fieldDataset == "" {
			fieldDataset, _ = qualifiedSemanticMember(field.Field)
			if fieldDataset == "" {
				continue
			}
		}
		if err := authorizeSemanticField(ctx, metrics, modelID, fieldDataset, fieldName); err != nil {
			return err
		}
	}
	if timeField != "" {
		fieldDataset, fieldName := dataset, timeField
		if fieldDataset == "" {
			fieldDataset, _ = qualifiedSemanticMember(timeField)
		}
		if fieldDataset != "" {
			if err := authorizeSemanticField(ctx, metrics, modelID, fieldDataset, fieldName); err != nil {
				return err
			}
		}
	}
	for _, field := range metricsFields {
		if err := authorizeSemanticTarget(ctx, metrics, modelID, semanticquery.SemanticAccessTarget{Metric: field.Field, Dataset: dataset}); err != nil {
			return err
		}
	}
	for _, filter := range filters {
		if err := authorizeSemanticFilter(ctx, metrics, modelID, filter); err != nil {
			return err
		}
	}
	return nil
}

func authorizeSemanticFilter(ctx context.Context, metrics Metrics, modelID string, filter semanticquery.Filter) error {
	dataset := filter.Dataset
	field := filter.Field
	if dataset == "" {
		dataset, _ = qualifiedSemanticMember(field)
	}
	if dataset != "" && field != "" {
		if err := authorizeSemanticField(ctx, metrics, modelID, dataset, field); err != nil {
			return err
		}
	}
	if filter.Spatial != nil {
		spatialDataset := filter.Spatial.Dataset
		for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
			if field == "" {
				continue
			}
			fieldDataset := spatialDataset
			if fieldDataset == "" {
				fieldDataset = dataset
			}
			if fieldDataset == "" {
				fieldDataset, _ = qualifiedSemanticMember(field)
			}
			if fieldDataset == "" {
				continue
			}
			if err := authorizeSemanticField(ctx, metrics, modelID, fieldDataset, field); err != nil {
				return err
			}
		}
	}
	for _, group := range filter.Groups {
		for _, child := range group.Filters {
			if err := authorizeSemanticFilter(ctx, metrics, modelID, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func qualifiedSemanticMember(value string) (string, string) {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, '.'); index > 0 && index < len(value)-1 {
		return value[:index], value[index+1:]
	}
	return "", value
}

func semanticAuthorizationUnavailable(err error) bool {
	return errors.Is(err, errSemanticConsumerUnavailable) || errors.Is(err, queryauthz.ErrSemanticConsumerAuthorityUnavailable)
}

func semanticAuthorizationStatus(err error) int {
	if semanticAuthorizationUnavailable(err) {
		return 503
	}
	return 403
}

func semanticRequestAuthorizationStatus(metrics Metrics, modelID string, err error) int {
	if semanticAuthorizationUnavailable(err) {
		return nethttp.StatusServiceUnavailable
	}
	if !protectedSemanticModel(metrics, modelID) {
		// Public semantic query shape errors retain the API's historical 400
		// contract; protected unknown members remain fail-closed as 403.
		return nethttp.StatusBadRequest
	}
	return semanticAuthorizationStatus(err)
}

func semanticMemberDenied(err error) bool {
	return queryauthz.IsDenied(err) || strings.Contains(err.Error(), "semantic access denied")
}

func authorizeSemanticRelationship(ctx context.Context, metrics Metrics, modelID string, relationship semanticmodel.Relationship) error {
	fromDataset, fromFields, err := semanticmodel.RelationshipEndpoint(relationship, true)
	if err != nil {
		return err
	}
	toDataset, toFields, err := semanticmodel.RelationshipEndpoint(relationship, false)
	if err != nil {
		return err
	}
	for _, dataset := range []string{fromDataset, toDataset} {
		if err := authorizeSemanticTarget(ctx, metrics, modelID, semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
			return err
		}
	}
	for index, fields := range [][]string{fromFields, toFields} {
		dataset := fromDataset
		if index == 1 {
			dataset = toDataset
		}
		for _, field := range fields {
			if err := authorizeSemanticField(ctx, metrics, modelID, dataset, field); err != nil {
				if semanticModelKnown(metrics, modelID) && !protectedSemanticModel(metrics, modelID) {
					continue
				}
				return err
			}
		}
	}
	return nil
}
