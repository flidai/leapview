package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/workload"
)

func TestBuildOwnsAdmissionLifecycle(t *testing.T) {
	module, err := Build(t.Context(), Config{Policy: workload.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := module.Acquire(t.Context(), ControlRequest("test"))
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	module.Close()
	module.Close()
	if _, err := module.Acquire(context.Background(), ControlRequest("after-close")); err == nil {
		t.Fatal("closed admission module accepted work")
	}
}

func TestSystemRequestsCarryIdentityAndMemoryEstimate(t *testing.T) {
	control := ControlRequest("control")
	maintenance := MaintenanceRequest("maintenance")
	background := Request{Class: BackgroundClass, PrincipalID: jobs.SystemPrincipalID, Operation: "background", EstimatedMemoryBytes: 64 << 20}
	for _, request := range []Request{control, maintenance, background} {
		if request.PrincipalID == "" || request.EstimatedMemoryBytes <= 0 {
			t.Fatalf("system request missing admission identity/estimate: %#v", request)
		}
	}
	if control.PrincipalID != jobs.SystemPrincipalID || maintenance.PrincipalID != jobs.SystemPrincipalID || background.PrincipalID != jobs.SystemPrincipalID {
		t.Fatalf("system jobs must use the reserved actor: control=%q maintenance=%q background=%q", control.PrincipalID, maintenance.PrincipalID, background.PrincipalID)
	}
}

func TestJobAdmitterMapsToSystemRequest(t *testing.T) {
	capture := &captureAdmitter{}
	adapter := JobAdmitter(capture)
	if _, err := adapter.Acquire(context.Background(), jobs.AdmissionRequest{Class: jobs.WorkloadClassBackground, PrincipalID: "principal-1", EstimatedMemoryBytes: 1, Operation: "job.run"}); err != nil {
		t.Fatal(err)
	}
	if capture.request.PrincipalID != "principal-1" || capture.request.EstimatedMemoryBytes != 1 || capture.request.Operation != "job.run" {
		t.Fatalf("job request was not mapped to explicit system admission: %#v", capture.request)
	}
}

type captureAdmitter struct{ request workload.Request }

func (a *captureAdmitter) Acquire(_ context.Context, request workload.Request) (workload.Lease, error) {
	a.request = request
	return captureLease{}, nil
}

type captureLease struct{}

func (captureLease) Context() context.Context { return context.Background() }
func (captureLease) QueueWait() time.Duration { return 0 }
func (captureLease) Release()                 {}

func TestBuildRejectsInvalidPolicy(t *testing.T) {
	policy := workload.DefaultConfig()
	policy.MaxRunning = 0
	if _, err := Build(t.Context(), Config{Policy: policy}); err == nil {
		t.Fatal("invalid workload policy was accepted")
	}
}
