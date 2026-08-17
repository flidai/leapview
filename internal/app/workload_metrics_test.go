package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/workload"
)

func TestWorkloadMetricsBoundsDataQueries(t *testing.T) {
	inner := &blockingQueryMetrics{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	controller, err := workload.New(workload.Config{MaxRunning: 1, MaximumQueued: 2, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1, MaximumQueued: 1, QueueTimeout: time.Second},
	}})
	if err != nil {
		t.Fatal(err)
	}
	metrics := dashboardmodule.WithAdmission(inner, controller)

	go func() {
		_, _ = metrics.ExecuteDataQuery(context.Background(), workloadTestQuery())
	}()
	<-inner.started
	secondDone := make(chan error, 1)
	go func() {
		_, err := metrics.ExecuteDataQuery(context.Background(), workloadTestQuery())
		secondDone <- err
	}()

	select {
	case <-inner.started:
		t.Fatal("queued query executed before read capacity opened")
	case <-time.After(50 * time.Millisecond):
	}

	_, err = metrics.ExecuteDataQuery(context.Background(), workloadTestQuery())
	if reason, ok := workload.ReasonOf(err); !ok || reason != workload.ClassQueueFull {
		t.Fatalf("third query error = %v, want class queue rejection", err)
	}

	close(inner.release)
	if err := <-secondDone; err != nil {
		t.Fatalf("queued query error = %v", err)
	}
}

func TestWorkloadMetricsDoesNotAdmitWholeDashboardReads(t *testing.T) {
	inner := &blockingQueryMetrics{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	controller, err := workload.New(workload.Config{MaxRunning: 1, MaximumQueued: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1, MaximumQueued: 1, QueueTimeout: time.Second},
	}})
	if err != nil {
		t.Fatal(err)
	}
	metrics := dashboardmodule.WithAdmission(inner, controller)

	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := metrics.QueryDashboardPage(context.Background(), "sales", "overview", dashboard.Filters{})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-inner.started:
		case <-time.After(time.Second):
			t.Fatal("dashboard request was admitted as a whole instead of entering its physical query planning")
		}
	}
	close(inner.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("dashboard query error = %v", err)
		}
	}
}

func TestWorkloadMetricsClassifiesAgentAndReleasesFailedQueries(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{workload.Background: {MaximumRunning: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("query failed")
	inner := errorQueryMetrics{err: wantErr, inspect: func() {
		stats := controller.Stats()
		if stats.Classes[workload.Background].Running != 1 || stats.Classes[workload.Interactive].Running != 0 {
			t.Fatalf("agent admission stats = %#v", stats)
		}
	}}
	metrics := dashboardmodule.WithAdmission(inner, controller)
	request := workloadTestQuery()
	request.Surface = dataquery.SurfaceAgent
	request.Operation = dataquery.OperationAgentQuery
	if _, err := metrics.ExecuteDataQuery(context.Background(), request); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if stats := controller.Stats(); stats.Running != 0 || stats.Queued != 0 {
		t.Fatalf("permit leaked after failure: %#v", stats)
	}
}

func TestWorkloadMetricsReusesInteractivePhysicalQueryAdmission(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	metrics := dashboardmodule.WithAdmission(nestedAdmissionMetrics{controller: controller}, controller)
	if _, err := metrics.ExecuteDataQuery(t.Context(), workloadTestQuery()); err != nil {
		t.Fatalf("physical query admission should reuse the outer interactive lease: %v", err)
	}
}

func workloadTestQuery() dataquery.Query {
	request := dataquery.SemanticRows("sales", "orders", []dataquery.Field{{Field: "orders.id"}}, nil, nil, nil, 0, 1, false)
	request.ProjectID = "project:sales"
	return request
}

type blockingQueryMetrics struct {
	fakeMetrics
	started chan struct{}
	release chan struct{}
}

type errorQueryMetrics struct {
	fakeMetrics
	err     error
	inspect func()
}

type nestedAdmissionMetrics struct {
	fakeMetrics
	controller *workload.Controller
}

func (m nestedAdmissionMetrics) ExecuteDataQuery(ctx context.Context, _ dataquery.Query) (dataquery.Result, error) {
	lease, err := m.controller.Acquire(ctx, workload.Request{
		Class: workload.Interactive, PrincipalID: "system:query", Operation: "physical_query", EstimatedMemoryBytes: 64 << 20,
	})
	if err != nil {
		return dataquery.Result{}, err
	}
	defer lease.Release()
	return dataquery.Result{}, nil
}

func (m errorQueryMetrics) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	if m.inspect != nil {
		m.inspect()
	}
	return dataquery.Result{}, m.err
}

func (m *blockingQueryMetrics) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	m.started <- struct{}{}
	<-m.release
	return dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"id"}), Rows: []dataquery.Row{{"id": "1"}}}, nil
}

func (m *blockingQueryMetrics) QueryDashboardPage(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	m.started <- struct{}{}
	<-m.release
	return dashboard.Patch{}, nil
}

func (m *blockingQueryMetrics) QuerySemantic(context.Context, string, reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	m.started <- struct{}{}
	<-m.release
	return nil, nil
}
