package report

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type captureDataQueryExecutor struct {
	query dataquery.Query
}

func (e *captureDataQueryExecutor) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	e.query = query
	return dataquery.Result{}, nil
}

func TestDataQueryServicePreservesStatisticalBindings(t *testing.T) {
	executor := &captureDataQueryExecutor{}
	service := NewDataQueryService(projectgraph.ResourceID("project:test"), "model:test", executor)
	minimum, maximum := 1.5, 9.5
	if _, err := service.Histogram(context.Background(), RawValueQuery{
		Dataset: "orders", Metric: QueryField{Field: "revenue", Alias: "revenue"},
		Histogram: &HistogramOptions{Domain: &HistogramDomain{Minimum: minimum, Maximum: maximum}, NullPolicy: "include", Approximation: "approximate"},
	}, 12); err != nil {
		t.Fatal(err)
	}
	if got := executor.query.Histogram; got == nil || got.DomainMinimum == nil || *got.DomainMinimum != minimum || got.DomainMaximum == nil || *got.DomainMaximum != maximum || got.NullPolicy != "include" || got.Approximation != "approximate" || executor.query.BinCount != 12 {
		t.Fatalf("histogram options were not preserved: %#v", got)
	}
	lower, upper := .1, .9
	if _, err := service.Distribution(context.Background(), RawValueQuery{
		Dataset: "orders", Metric: QueryField{Field: "revenue", Alias: "revenue"},
		Distribution: &DistributionOptions{Quantiles: []float64{.1, .5, .9}, Whiskers: &DistributionWhiskers{Lower: lower, Upper: upper}, Outliers: "omit", Approximation: "exact"},
	}, nil, 0); err != nil {
		t.Fatal(err)
	}
	if got := executor.query.Distribution; got == nil || len(got.Quantiles) != 3 || got.WhiskerLower == nil || *got.WhiskerLower != lower || got.WhiskerUpper == nil || *got.WhiskerUpper != upper || got.Outliers != "omit" || got.Approximation != "exact" {
		t.Fatalf("distribution options were not preserved: %#v", got)
	}
}
