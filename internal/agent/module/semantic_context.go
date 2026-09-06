package module

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/agent"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// Agent turn references cannot widen semantic access. Resource authorization
// above this boundary still applies; query execution subsequently uses the
// same shared consumer/planner boundary rather than trusting turn metadata.
func authorizeSemanticExploration(ctx context.Context, metrics any, modelID, dataset string, model *semanticmodel.Model, exploration *agent.DataExploration) error {
	if model == nil {
		return errors.New("semantic context model is unavailable")
	}
	port, ok := metrics.(interface {
		SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	})
	if !ok {
		if model.AccessPolicy.Empty() {
			return nil
		}
		return errors.New("semantic context authority is unavailable")
	}
	consumer, err := port.SemanticConsumer(ctx, modelID)
	if err != nil {
		return err
	}
	if consumer == nil || consumer.Planner() == nil || consumer.Planner().CompiledModel() == nil || !consumer.Planner().CompiledModel().MatchesModel(model) {
		return errors.New("semantic context snapshot is stale")
	}
	if !model.AccessPolicy.Empty() && consumer.ModelID() != modelID {
		return errors.New("semantic context model identity does not match")
	}
	if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}); err != nil {
		return err
	}
	if exploration == nil {
		return nil
	}
	for _, metric := range exploration.Metrics {
		if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Metric: metric}); err != nil {
			return err
		}
	}
	fields := append([]string(nil), exploration.Dimensions...)
	for _, filter := range exploration.Filters {
		fields = append(fields, filter.Field)
	}
	if exploration.Time != nil {
		fields = append(fields, exploration.Time.Field)
	}
	for _, field := range fields {
		if _, err := consumer.Planner().PlanRows(semanticquery.RowRequest{Dataset: dataset, Dimensions: []semanticquery.Field{{Field: field}}, Limit: 1}); err != nil {
			return err
		}
	}
	return nil
}
