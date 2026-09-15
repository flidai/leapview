package report

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type captureDataQueryExecutor struct {
	query  dataquery.Query
	result dataquery.Result
}

func (e *captureDataQueryExecutor) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	e.query = query
	return e.result, nil
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

func TestDataQueryServiceDecodesDecimalHistogramBounds(t *testing.T) {
	executor := &captureDataQueryExecutor{result: dataquery.Result{Rows: []dataquery.Row{{
		"bucket": int64(2), "count": int64(4), "start": "12.50", "end": "13.75",
	}}}}
	service := NewDataQueryService(projectgraph.ResourceID("project:test"), "model:test", executor)

	bins, err := service.Histogram(context.Background(), RawValueQuery{
		Dataset: "orders", Metric: QueryField{Field: "revenue", Alias: "revenue"},
	}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(bins) != 1 {
		t.Fatalf("histogram bins = %#v, want one bin", bins)
	}
	got := bins[0]
	if got.Bucket != 2 || got.Count != 4 || got.Start != 12.5 || got.End != 13.75 {
		t.Fatalf("histogram bin = %#v, want bucket 2 count 4 bounds 12.5-13.75", got)
	}
}
