package materialize

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

func TestProtectedFilterOptionsRemainOutsideSharedResultCache(t *testing.T) {
	runtime, governor, _ := protectedConsumerFixtureWithModelModifier(t, true, func(model *semanticmodel.Model) {
		model.Tables["orders"] = clearExecutionSQL(model.Tables["orders"])
	})
	installSemanticCacheLifecycleEvidence(t, runtime)
	request := semanticCacheLifecycleRequest()
	request.Operation = dataquery.OperationDashboardFilterOptions
	ctx := dataquery.WithGovernor(context.Background(), governor)

	first, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatalf("first protected filter-option query: %v", err)
	}
	second, err := runtime.ExecuteDataQuery(ctx, request)
	if err != nil {
		t.Fatalf("second protected filter-option query: %v", err)
	}
	if first.CacheOutcome != "" || second.CacheOutcome != "" {
		t.Fatalf("protected filter-option cache outcomes = (%q, %q), want bypass", first.CacheOutcome, second.CacheOutcome)
	}
	if got := runtime.db.(*semanticConsumerArrowDatabase).queries.Load(); got != 2 {
		t.Fatalf("protected filter-option physical executions = %d, want 2", got)
	}
}
