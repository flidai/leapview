package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
)

func (m runtimeMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	runtime, release, resolved, err := m.activeResolvedForDashboardRefresh(ctx, request.DashboardID)
	if err != nil {
		return err
	}
	defer release()
	if resolved.Source.Kind == dashboardresolver.SourceWorkspace {
		if port, ok := runtime.(definitionConsumerRuntime); ok {
			return port.ExecuteConsumersPageForDefinition(ctx, resolved.Definition, request, publish)
		}
		return fmt.Errorf("active runtime does not provide compiled dashboard consumer execution")
	}
	return executeConsumersFrom(ctx, runtime, request, publish)
}

func (m multiWorkspaceMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	return executeConsumersFrom(ctx, nil, request, publish)
}

func (m *dynamicRuntimeMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	return executeConsumersFrom(ctx, nil, request, publish)
}

func (m admittedMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	return executeConsumersFrom(m.readContext(ctx), m.Metrics, request, publish)
}

func executeConsumersFrom(ctx context.Context, metrics any, request consumer.Request, publish consumer.Publisher) error {
	port, ok := metrics.(consumer.Executor)
	if !ok {
		return fmt.Errorf("%T does not provide dashboard consumer execution", metrics)
	}
	return port.ExecuteConsumersPage(ctx, request, publish)
}
