package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/workload"
)

type nilRuntimeHostStartupLeaseAdmitter struct{}

func (nilRuntimeHostStartupLeaseAdmitter) Acquire(context.Context, workload.Request) (workload.Lease, error) {
	return nil, nil
}

func TestRuntimeHostStartupUsesAndReleasesControlAdmission(t *testing.T) {
	controller, err := workload.New(workload.Config{MaxRunning: 1, Classes: map[workload.Class]workload.Policy{
		workload.Control: {MaximumRunning: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	wantErr := errors.New("build failed")
	err = withRuntimeHostStartupAdmission(t.Context(), controller, func(ctx context.Context) error {
		class, principalID, admitted := workload.Current(ctx)
		if !admitted || class != workload.Control || principalID == "" {
			t.Fatalf("startup admission = class %q, principal %q, admitted %t", class, principalID, admitted)
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("startup error = %v, want %v", err, wantErr)
	}
	stats := controller.Stats()
	if stats.Running != 0 || stats.Classes[workload.Control].Running != 0 {
		t.Fatalf("startup admission was not released: %#v", stats)
	}
}

func TestRuntimeHostStartupRejectsNilAdmissionLease(t *testing.T) {
	err := withRuntimeHostStartupAdmission(t.Context(), nilRuntimeHostStartupLeaseAdmitter{}, func(context.Context) error {
		t.Fatal("builder called without a workload lease")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "returned nil lease") {
		t.Fatalf("startup error = %v, want nil lease error", err)
	}
}
