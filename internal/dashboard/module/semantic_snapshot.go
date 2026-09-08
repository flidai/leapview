package module

import (
	"context"
	"fmt"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// SemanticPlannerSnapshot pairs physical table bindings and authorization
// generation in one lease. Separate Planner/SemanticModel calls cannot prove
// this pairing when identical source is activated in a new generation.
func (m runtimeMetrics) SemanticPlannerSnapshot(ctx context.Context, modelID string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error) {
	var empty accesssnapshot.AuthorizationSnapshot
	if m.provider == nil {
		return nil, empty, fmt.Errorf("semantic runtime provider is unavailable")
	}
	lease, err := m.provider.Acquire(ctx)
	if err != nil {
		return nil, empty, err
	}
	if lease == nil {
		return nil, empty, fmt.Errorf("semantic runtime lease is unavailable")
	}
	defer lease.Release()
	identity, err := m.identityForLease(lease)
	if err != nil {
		return nil, empty, err
	}
	authorization, ok := lease.(interface {
		AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
	})
	if !ok {
		return nil, empty, fmt.Errorf("semantic lease authorization is unavailable")
	}
	snapshot := authorization.AuthorizationSnapshot()
	if snapshot.ValidateBound() != nil || snapshot.Identity() != identity {
		return nil, empty, fmt.Errorf("semantic lease identity is inconsistent")
	}
	port, ok := lease.Runtime().(semanticPlannerRuntime)
	if !ok {
		return nil, empty, fmt.Errorf("semantic runtime planner is unavailable")
	}
	value, ok := port.Planner(modelID)
	if !ok {
		return nil, empty, fmt.Errorf("semantic model planner is unavailable")
	}
	planner, ok := concretePlanner(value)
	if !ok || planner.CompiledModel() == nil {
		return nil, empty, fmt.Errorf("compiled semantic planner is unavailable")
	}
	return planner, snapshot, nil
}

func semanticPlannerSnapshot(ctx context.Context, metrics any, modelID string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error) {
	port, ok := metrics.(interface {
		SemanticPlannerSnapshot(context.Context, string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error)
	})
	if !ok {
		return nil, accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("semantic planner snapshot is unavailable")
	}
	return port.SemanticPlannerSnapshot(ctx, modelID)
}

func (m admittedMetrics) SemanticPlannerSnapshot(ctx context.Context, modelID string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error) {
	return semanticPlannerSnapshot(ctx, m.Metrics, modelID)
}

func (m auditedMetrics) SemanticPlannerSnapshot(ctx context.Context, modelID string) (*semanticquery.Planner, accesssnapshot.AuthorizationSnapshot, error) {
	return semanticPlannerSnapshot(ctx, m.Metrics, modelID)
}
