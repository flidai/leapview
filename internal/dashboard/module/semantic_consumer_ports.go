package module

import (
	"context"
	"fmt"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type semanticTargetAuthorizer interface {
	AuthorizeSemanticTarget(context.Context, string, semanticquery.SemanticAccessTarget) error
}

func semanticConsumer(ctx context.Context, metrics any, modelID string) (*semanticquery.SemanticAccessConsumer, error) {
	port, ok := metrics.(interface {
		SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error)
	})
	if !ok {
		return nil, fmt.Errorf("semantic consumer authority is unavailable")
	}
	return port.SemanticConsumer(ctx, modelID)
}

func (m auditedMetrics) SemanticConsumer(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, error) {
	return semanticConsumer(ctx, m.Metrics, modelID)
}

func (m admittedMetrics) SemanticConsumer(ctx context.Context, modelID string) (*semanticquery.SemanticAccessConsumer, error) {
	return semanticConsumer(ctx, m.Metrics, modelID)
}

type semanticModelProjectionAuthorizer interface {
	AuthorizeSemanticModelProjection(context.Context, string) error
}

func authorizeSemanticField(ctx context.Context, metrics any, modelID, dataset, field string) error {
	port, ok := metrics.(interface {
		AuthorizeSemanticField(context.Context, string, string, string) error
	})
	if !ok {
		return fmt.Errorf("semantic consumer authority is unavailable")
	}
	return port.AuthorizeSemanticField(ctx, modelID, dataset, field)
}

func (m auditedMetrics) AuthorizeSemanticField(ctx context.Context, modelID, dataset, field string) error {
	return authorizeSemanticField(ctx, m.Metrics, modelID, dataset, field)
}
func (m admittedMetrics) AuthorizeSemanticField(ctx context.Context, modelID, dataset, field string) error {
	return authorizeSemanticField(ctx, m.Metrics, modelID, dataset, field)
}

func semanticConsumerPlanner(ctx context.Context, metrics any, modelID string) (*semanticquery.Planner, error) {
	port, ok := metrics.(interface {
		SemanticPlanner(context.Context, string) (*semanticquery.Planner, error)
	})
	if !ok {
		return nil, fmt.Errorf("semantic consumer authority is unavailable")
	}
	return port.SemanticPlanner(ctx, modelID)
}

func (m auditedMetrics) SemanticPlanner(ctx context.Context, modelID string) (*semanticquery.Planner, error) {
	return semanticConsumerPlanner(ctx, m.Metrics, modelID)
}
func (m admittedMetrics) SemanticPlanner(ctx context.Context, modelID string) (*semanticquery.Planner, error) {
	return semanticConsumerPlanner(ctx, m.Metrics, modelID)
}

func authorizeSemanticTarget(ctx context.Context, metrics any, modelID string, target semanticquery.SemanticAccessTarget) error {
	port, ok := metrics.(semanticTargetAuthorizer)
	if !ok {
		return fmt.Errorf("semantic consumer authority is unavailable")
	}
	return port.AuthorizeSemanticTarget(ctx, modelID, target)
}

func semanticConsumerCacheAllowed(metrics any, modelID string) bool {
	port, ok := metrics.(interface{ SemanticConsumerCacheAllowed(string) bool })
	return ok && port.SemanticConsumerCacheAllowed(modelID)
}

func (m auditedMetrics) AuthorizeSemanticTarget(ctx context.Context, modelID string, target semanticquery.SemanticAccessTarget) error {
	return authorizeSemanticTarget(ctx, m.Metrics, modelID, target)
}
func (m admittedMetrics) AuthorizeSemanticTarget(ctx context.Context, modelID string, target semanticquery.SemanticAccessTarget) error {
	return authorizeSemanticTarget(ctx, m.Metrics, modelID, target)
}

func authorizeSemanticModelProjection(ctx context.Context, metrics any, modelID string) error {
	port, ok := metrics.(semanticModelProjectionAuthorizer)
	if !ok {
		return fmt.Errorf("semantic consumer authority is unavailable")
	}
	return port.AuthorizeSemanticModelProjection(ctx, modelID)
}

func (m auditedMetrics) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	return authorizeSemanticModelProjection(ctx, m.Metrics, modelID)
}

func (m admittedMetrics) AuthorizeSemanticModelProjection(ctx context.Context, modelID string) error {
	return authorizeSemanticModelProjection(ctx, m.Metrics, modelID)
}

func (m auditedMetrics) SemanticConsumerCacheAllowed(modelID string) bool {
	return semanticConsumerCacheAllowed(m.Metrics, modelID)
}
func (m admittedMetrics) SemanticConsumerCacheAllowed(modelID string) bool {
	return semanticConsumerCacheAllowed(m.Metrics, modelID)
}
