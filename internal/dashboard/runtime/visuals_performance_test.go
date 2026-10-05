package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
)

type performanceInlineRuntime struct {
	snapshotDataRuntime
	ctx      context.Context
	entered  chan struct{}
	release  chan struct{}
	active   atomic.Int32
	maximum  atomic.Int32
	calls    atomic.Int32
	failures map[string]error
}

func (r *performanceInlineRuntime) Query(ctx context.Context, query reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	if ctx != r.ctx {
		return nil, errors.New("original query context was replaced")
	}
	r.calls.Add(1)
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		old := r.maximum.Load()
		if old >= active || r.maximum.CompareAndSwap(old, active) {
			break
		}
	}
	r.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
	}
	if failure := r.failures[query.Metrics[0].Field]; failure != nil {
		return nil, failure
	}
	row := reportdef.QueryRow{"label": "paid", "status": "paid", "value": "1"}
	if budget, ok := dataquery.ResultBudgetFromContext(ctx); ok {
		if err := budget.ConsumeRow(row); err != nil {
			return nil, err
		}
	}
	return reportdef.QueryRows{row}, nil
}

func performanceInlineFixture(t *testing.T, count int) (*VisualizationDataService, *dashboarddefinition.Definition, []string) {
	t.Helper()
	report := &dashboarddefinition.Definition{Visualizations: make(map[string]visualizationdefinition.Definition)}
	keys := make([]string, count)
	for index := range keys {
		keys[index] = fmt.Sprintf("chart_%d", index)
		report.Visualizations[keys[index]] = canonicalCartesian(t, keys[index])
	}
	return &VisualizationDataService{filters: &FilterService{}}, report, keys
}

func waitInlineQuery(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("visual query did not start")
	}
}

func TestWholePageInlineQueriesRespectSnapshotReadCapacity(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot int64
		capacity int
		consumer bool
		expected int32
	}{
		{"snapshot", 42, 2, false, 2},
		{"mutable", 0, 2, false, 1},
		{"single_connection", 42, 1, false, 1},
		{"consumer_already_scheduled", 42, 2, true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, report, keys := performanceInlineFixture(t, 5)
			ctx := dataquery.WithResultBudget(t.Context(), dataquery.ResultLimits{MaxRows: 5, MaxBytes: 4096})
			data := &performanceInlineRuntime{snapshotDataRuntime: snapshotDataRuntime{snapshotID: test.snapshot, readConcurrency: test.capacity}, ctx: ctx, entered: make(chan struct{}, len(keys)), release: make(chan struct{})}
			runtime := &modelRuntime{data: data}
			done := make(chan error, 1)
			go func() {
				var err error
				if test.consumer {
					_, err = service.visuals(ctx, runtime, report, dashboard.Filters{}, keys)
				} else {
					visuals, queryErr := service.pageVisuals(ctx, runtime, report, dashboard.Filters{}, keys)
					err = queryErr
					if err == nil && len(visuals) != len(keys) {
						err = fmt.Errorf("received %d visuals", len(visuals))
					}
				}
				done <- err
			}()
			for range int(test.expected) {
				waitInlineQuery(t, data.entered)
			}
			if data.maximum.Load() != test.expected {
				t.Errorf("parallel reads=%d want %d", data.maximum.Load(), test.expected)
			}
			close(data.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("visual workers did not finish")
			}
			if data.calls.Load() != 5 || data.maximum.Load() > test.expected || data.active.Load() != 0 {
				t.Fatalf("calls=%d maximum=%d active=%d", data.calls.Load(), data.maximum.Load(), data.active.Load())
			}
		})
	}
}

func TestWholePageInlineCancellationJoinsWorkers(t *testing.T) {
	service, report, keys := performanceInlineFixture(t, 5)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	data := &performanceInlineRuntime{snapshotDataRuntime: snapshotDataRuntime{snapshotID: 42, readConcurrency: 2}, ctx: ctx, entered: make(chan struct{}, len(keys)), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := service.pageVisuals(ctx, &modelRuntime{data: data}, report, dashboard.Filters{}, keys)
		done <- err
	}()
	waitInlineQuery(t, data.entered)
	waitInlineQuery(t, data.entered)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || data.active.Load() != 0 {
			t.Fatalf("canceled error=%v active=%d", err, data.active.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled workers remained active")
	}
}

func TestWholePageInlineQueriesRetainSharedResultBudget(t *testing.T) {
	service, report, keys := performanceInlineFixture(t, 2)
	ctx := dataquery.WithResultBudget(t.Context(), dataquery.ResultLimits{MaxRows: 1, MaxBytes: 4096})
	release := make(chan struct{})
	close(release)
	data := &performanceInlineRuntime{snapshotDataRuntime: snapshotDataRuntime{snapshotID: 42, readConcurrency: 2}, ctx: ctx, entered: make(chan struct{}, len(keys)), release: release}
	_, err := service.pageVisuals(ctx, &modelRuntime{data: data}, report, dashboard.Filters{}, keys)
	var limit *dataquery.ResultLimitError
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultRows || data.active.Load() != 0 {
		t.Fatalf("budget error=%v active=%d", err, data.active.Load())
	}
}

func TestWholePageInlineErrorsFollowVisualOrder(t *testing.T) {
	service, report, keys := performanceInlineFixture(t, 2)
	first, second := errors.New("first visual failed"), errors.New("second visual failed")
	for _, key := range keys {
		definition := report.Visualizations[key]
		definition.Query.Aggregate.Metrics[0].FieldID = key
		report.Visualizations[key] = definition
	}
	release := make(chan struct{})
	close(release)
	data := &performanceInlineRuntime{snapshotDataRuntime: snapshotDataRuntime{snapshotID: 42, readConcurrency: 2}, ctx: t.Context(), entered: make(chan struct{}, len(keys)), release: release, failures: map[string]error{keys[0]: first, keys[1]: second}}
	_, err := service.pageVisuals(data.ctx, &modelRuntime{data: data}, report, dashboard.Filters{}, keys)
	if !errors.Is(err, first) || data.calls.Load() != 2 || data.active.Load() != 0 {
		t.Fatalf("first error=%v queries=%d active=%d", err, data.calls.Load(), data.active.Load())
	}
}
