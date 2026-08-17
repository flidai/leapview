package observability

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/workload"
	"github.com/prometheus/client_golang/prometheus"
)

func TestObserverUsesOnlyBoundedMetricLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer := New(registry)
	controller, err := workload.New(workload.Config{MaxRunning: 1, MaximumQueued: 1, Classes: map[workload.Class]workload.Policy{
		workload.Interactive: {MaximumRunning: 1, MaximumQueued: 1},
	}}, workload.WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire(context.Background(), workload.Request{
		Class: workload.Interactive, PrincipalID: "principal-with-sensitive-identity", GroupIDs: []string{"group-sensitive"}, Operation: "request-id-must-not-be-a-label", EstimatedMemoryBytes: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	controller.Close()

	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"leapview_workload_running": false, "leapview_workload_queued": false,
		"leapview_workload_memory_bytes": false, "leapview_workload_borrowed": false,
		"leapview_workload_admissions_total": false, "leapview_workload_queue_wait_seconds": false,
		"leapview_workload_execution_duration_seconds": false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; !ok {
			continue
		}
		want[family.GetName()] = true
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "workspace" || label.GetName() == "principal" || label.GetName() == "group" || label.GetName() == "operation" || label.GetName() == "request_id" || label.GetValue() == "request-id-must-not-be-a-label" || label.GetValue() == "principal-with-sensitive-identity" || label.GetValue() == "group-sensitive" {
					t.Fatalf("unbounded workload metric label: %s=%s", label.GetName(), label.GetValue())
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("metric %s was not registered", name)
		}
	}
}
