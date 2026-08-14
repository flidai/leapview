package app

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/workload"
)

type consumerForwardingMetrics struct {
	fakeMetrics
	calls    int
	governed bool
	admitter bool
}

func (m *consumerForwardingMetrics) ExecuteConsumersPage(ctx context.Context, request consumer.Request, publish consumer.Publisher) error {
	m.calls++
	_, m.governed = dataquery.GovernorFromContext(ctx)
	_, m.admitter = workload.FromContext(ctx)
	dataquery.ObservePhysicalQuery(ctx, dataquery.PhysicalQueryObservation{Count: 1})
	for _, target := range request.Targets {
		publish(consumer.Result{Target: target, Envelope: visualizationir.VisualizationEnvelope{VisualID: target.ID}, Queries: 1})
	}
	return nil
}

func (m *consumerForwardingMetrics) QueryCompiledFilterOptions(ctx context.Context, _ string, _ dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error) {
	m.calls++
	_, m.governed = dataquery.GovernorFromContext(ctx)
	_, m.admitter = workload.FromContext(ctx)
	return dashboardfilter.OptionResult{Complete: true}, nil
}

func TestProductionDashboardWrappersForwardGovernedConsumerPlan(t *testing.T) {
	underlying := &consumerForwardingMetrics{}
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatalf("new workload controller: %v", err)
	}
	t.Cleanup(controller.Close)
	metrics := dashboardmodule.WithQueryAudit(
		dashboardmodule.WithAdmission(queryauthz.New(underlying, queryauthz.Options{}), controller),
		nil, nil,
	)

	visuals := 0
	dashboardstream.TargetWork(metrics, dashboardstream.WorkRequest{
		DashboardID: "sales-dashboard",
		PageID:      "overview",
		Plan: command.RefreshPlan{Targets: []command.Target{
			{Kind: command.TargetVisual, ID: "orders"},
			{Kind: command.TargetVisual, ID: "revenue"},
		}},
	})(context.Background(), func(event dashboardstream.RefreshEvent) bool {
		if event.Type == dashboardstream.RefreshEventVisual {
			visuals++
		}
		return true
	})

	if underlying.calls != 1 || !underlying.governed || !underlying.admitter || visuals != 2 {
		t.Fatalf("calls=%d governed=%v admitter=%v visuals=%d", underlying.calls, underlying.governed, underlying.admitter, visuals)
	}
}

func TestProductionDashboardWrappersForwardGovernedFilterOptions(t *testing.T) {
	underlying := &consumerForwardingMetrics{}
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatalf("new workload controller: %v", err)
	}
	t.Cleanup(controller.Close)
	metrics := dashboardmodule.WithQueryAudit(
		dashboardmodule.WithAdmission(queryauthz.New(underlying, queryauthz.Options{}), controller),
		nil, nil,
	)

	provider, ok := metrics.(interface {
		QueryCompiledFilterOptions(context.Context, string, dashboardfilter.OptionQuery) (dashboardfilter.OptionResult, error)
	})
	if !ok {
		t.Fatalf("%T does not expose compiled filter options", metrics)
	}
	result, err := provider.QueryCompiledFilterOptions(context.Background(), "sales-dashboard", dashboardfilter.OptionQuery{Field: "orders.state"})
	if err != nil {
		t.Fatal(err)
	}
	if underlying.calls != 1 || !underlying.governed || !underlying.admitter || !result.Complete {
		t.Fatalf("calls=%d governed=%v admitter=%v complete=%v", underlying.calls, underlying.governed, underlying.admitter, result.Complete)
	}
}
