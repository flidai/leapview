package app

import (
	"testing"

	"github.com/flidai/leapview/internal/workload"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

func TestCandidatePreparationReusesOuterRefreshAdmission(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 2, Classes: map[workload.Class]workload.Policy{
		workload.Refresh: {MaximumRunning: 1},
		workload.Control: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	outer, err := controller.Acquire(t.Context(), workload.Request{Class: workload.Refresh, PrincipalID: "dev", Operation: "materialization.refresh", EstimatedMemoryBytes: 64 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer outer.Release()

	admitter := candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))
	preparation, err := admitter.AcquireCandidatePreparation(outer.Context())
	if err != nil {
		t.Fatalf("candidate preparation nested in refresh admission: %v", err)
	}
	if preparation.Context() != outer.Context() {
		t.Fatal("candidate preparation did not preserve the outer refresh context")
	}
	preparation.Release()
	stats := controller.Stats()
	if stats.Running != 1 || stats.Classes[workload.Refresh].Running != 1 || stats.Classes[workload.Control].Running != 0 {
		t.Fatalf("nested candidate preparation admission = %#v, want one outer refresh lease", stats)
	}
}

func TestStandaloneCandidatePreparationUsesControlAdmission(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Control: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	admitter := candidatePreparationAdmitter(controller, workloadmodule.ControlRequest("candidate.prepare"))
	preparation, err := admitter.AcquireCandidatePreparation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if class, _, ok := workload.Current(preparation.Context()); !ok || class != workload.Control {
		t.Fatalf("standalone candidate preparation class = %q, admitted=%t", class, ok)
	}
	preparation.Release()
}
