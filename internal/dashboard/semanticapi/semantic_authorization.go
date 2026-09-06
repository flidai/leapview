package http

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
)

var errSemanticConsumerUnavailable = errors.New("semantic consumer authority is unavailable")

type semanticContextPlanner interface {
	SemanticPlanner(context.Context, string) (*semanticquery.Planner, error)
}

type semanticContextTargetAuthorizer interface {
	AuthorizeSemanticTarget(context.Context, string, semanticquery.SemanticAccessTarget) error
}

type semanticContextFieldAuthorizer interface {
	AuthorizeSemanticField(context.Context, string, string, string) error
}

type semanticContextProjectionAuthorizer interface {
	AuthorizeSemanticModelProjection(context.Context, string) error
}

type semanticContextConsumerProvider interface {
	SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
}

type semanticConsumerContextKey struct{}

type semanticConsumerBinding struct {
	modelID  string
	consumer *semanticquery.SemanticAccessConsumer
}

func protectedSemanticModel(metrics Metrics, modelID string) bool {
	model, ok := semanticModelSnapshot(metrics, modelID)
	return ok && !model.AccessPolicy.Empty()
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
	_, ok := semanticModelSnapshot(metrics, modelID)
	return ok
}

func semanticPlannerMatchesModel(metrics Metrics, modelID string, planner *semanticquery.Planner) bool {
	if planner == nil || planner.CompiledModel() == nil {
		return false
	}
	model := semanticModelForID(metrics, modelID)
	return model != nil && planner.CompiledModel().MatchesModel(model)
}

func semanticConsumerForRequest(ctx context.Context, metrics Metrics, modelID string) (context.Context, error) {
	if binding, ok := ctx.Value(semanticConsumerContextKey{}).(semanticConsumerBinding); ok && binding.modelID == modelID && binding.consumer != nil {
		return ctx, nil
	}
	provider, ok := any(metrics).(semanticContextConsumerProvider)
	if !ok {
		return ctx, nil
	}
	consumer, err := provider.SemanticConsumer(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if consumer == nil || !semanticPlannerMatchesModel(metrics, modelID, consumer.Planner()) {
		return nil, errSemanticConsumerUnavailable
	}
	// A compiled source fingerprint alone is not sufficient identity: two
	// semantic model resources may carry identical source. Protected consumers
	// therefore must also be bound to the requested resource ID before any
	// metadata is projected.
	if protectedSemanticModel(metrics, modelID) && consumer.ModelID() != modelID {
		return nil, errSemanticConsumerUnavailable
	}
	return context.WithValue(ctx, semanticConsumerContextKey{}, semanticConsumerBinding{modelID: modelID, consumer: consumer}), nil
}

func semanticConsumerFromContext(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, bool) {
	binding, ok := ctx.Value(semanticConsumerContextKey{}).(semanticConsumerBinding)
	return binding.consumer, ok && binding.modelID == modelID && binding.consumer != nil
}

func authorizeSemanticConsumerProjection(consumer *semanticquery.SemanticAccessConsumer) error {
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil {
		return errSemanticConsumerUnavailable
	}
	planner := consumer.Planner()
	compiled := planner.CompiledModel()
	model := compiled.SourceModel()
	if model == nil {
		return errSemanticConsumerUnavailable
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

func semanticPlannerForRequest(ctx context.Context, metrics Metrics, modelID string) (*semanticquery.Planner, error) {
	if consumer, ok := semanticConsumerFromContext(ctx, modelID); ok {
		planner := consumer.Planner()
		if planner == nil || !semanticPlannerMatchesModel(metrics, modelID, planner) {
			return nil, errSemanticConsumerUnavailable
		}
		return planner, nil
	}
	if provider, ok := any(metrics).(semanticContextPlanner); ok {
		planner, err := provider.SemanticPlanner(ctx, modelID)
		if err != nil {
			return nil, err
		}
		if planner != nil && semanticPlannerMatchesModel(metrics, modelID, planner) {
			return planner, nil
		}
		return nil, errSemanticConsumerUnavailable
	}
	if !semanticModelKnown(metrics, modelID) || protectedSemanticModel(metrics, modelID) {
		return nil, errSemanticConsumerUnavailable
	}
	planner, ok := semanticPlanner(metrics, modelID)
	if !ok || planner == nil || !semanticPlannerMatchesModel(metrics, modelID, planner) {
		return nil, fmt.Errorf("compiled semantic planner for model %q is unavailable", modelID)
	}
	return planner, nil
}

func authorizeSemanticTarget(ctx context.Context, metrics Metrics, modelID string, target semanticquery.SemanticAccessTarget) error {
	if consumer, ok := semanticConsumerFromContext(ctx, modelID); ok {
		return consumer.Authorize(target)
	}
	if scoped, err := semanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(semanticContextConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := semanticConsumerFromContext(scoped, modelID); ok {
		return consumer.Authorize(target)
	}
	if provider, ok := any(metrics).(semanticContextTargetAuthorizer); ok {
		return provider.AuthorizeSemanticTarget(ctx, modelID, target)
	}
	if !semanticModelKnown(metrics, modelID) || protectedSemanticModel(metrics, modelID) {
		return errSemanticConsumerUnavailable
	}
	return nil
}

func authorizeSemanticField(ctx context.Context, metrics Metrics, modelID, dataset, field string) error {
	if consumer, ok := semanticConsumerFromContext(ctx, modelID); ok {
		planner := consumer.Planner()
		if planner == nil {
			return errSemanticConsumerUnavailable
		}
		_, err := planner.PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
		return err
	}
	if scoped, err := semanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(semanticContextConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := semanticConsumerFromContext(scoped, modelID); ok {
		planner := consumer.Planner()
		if planner == nil {
			return errSemanticConsumerUnavailable
		}
		_, err := planner.PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1})
		return err
	}
	if provider, ok := any(metrics).(semanticContextFieldAuthorizer); ok {
		return provider.AuthorizeSemanticField(ctx, modelID, dataset, field)
	}
	if !semanticModelKnown(metrics, modelID) || protectedSemanticModel(metrics, modelID) {
		return errSemanticConsumerUnavailable
	}
	return nil
}

func authorizeSemanticModelProjection(ctx context.Context, metrics Metrics, modelID string) error {
	if consumer, ok := semanticConsumerFromContext(ctx, modelID); ok {
		return authorizeSemanticConsumerProjection(consumer)
	}
	if scoped, err := semanticConsumerForRequest(ctx, metrics, modelID); err != nil {
		if _, provider := any(metrics).(semanticContextConsumerProvider); provider {
			return err
		}
	} else if consumer, ok := semanticConsumerFromContext(scoped, modelID); ok {
		return authorizeSemanticConsumerProjection(consumer)
	}
	if provider, ok := any(metrics).(semanticContextProjectionAuthorizer); ok {
		return provider.AuthorizeSemanticModelProjection(ctx, modelID)
	}
	if !semanticModelKnown(metrics, modelID) || protectedSemanticModel(metrics, modelID) {
		return errSemanticConsumerUnavailable
	}
	return nil
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
			fieldDataset, fieldName = qualifiedSemanticMember(field.Field)
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
			fieldDataset, fieldName = qualifiedSemanticMember(timeField)
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
		dataset, field = qualifiedSemanticMember(field)
	}
	if dataset != "" && field != "" {
		if err := authorizeSemanticField(ctx, metrics, modelID, dataset, field); err != nil {
			return err
		}
	}
	if filter.Spatial != nil {
		spatialDataset := filter.Spatial.Dataset
		if spatialDataset == "" {
			spatialDataset = dataset
		}
		for _, field := range []string{filter.Spatial.LatitudeField, filter.Spatial.LongitudeField} {
			if field == "" || spatialDataset == "" {
				continue
			}
			if err := authorizeSemanticField(ctx, metrics, modelID, spatialDataset, field); err != nil {
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
