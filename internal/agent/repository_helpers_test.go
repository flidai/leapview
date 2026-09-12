package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/pkg/jobs"
)

func TestPageByIDParity(t *testing.T) {
	rows := []struct{ ID string }{{"a"}, {"b"}, {"c"}, {"d"}}
	tests := []struct {
		name string
		page Page
		want []string
	}{
		{name: "default", want: []string{"a", "b", "c", "d"}},
		{name: "bounded", page: Page{Limit: 2}, want: []string{"a", "b"}},
		{name: "after", page: Page{After: "b", Limit: 2}, want: []string{"c", "d"}},
		{name: "missing cursor", page: Page{After: "missing", Limit: 2}, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PageByID(rows, tt.page, func(row struct{ ID string }) string { return row.ID })
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, row := range got {
				if row.ID != tt.want[i] {
					t.Fatalf("row %d = %q, want %q", i, row.ID, tt.want[i])
				}
			}
		})
	}
}

func TestLeaseUnexpired(t *testing.T) {
	if !LeaseUnexpired(time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)) {
		t.Fatal("future lease should be valid")
	}
	if LeaseUnexpired(time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)) {
		t.Fatal("expired lease should be invalid")
	}
	if LeaseUnexpired("not-a-time") {
		t.Fatal("malformed lease should be invalid")
	}
}

func TestValidRunLease(t *testing.T) {
	job := jobs.Job{
		Kind:            "agent.run",
		ResourceKind:    "agent_run",
		ResourceID:      "run-1",
		Status:          jobs.StatusRunning,
		LeaseOwner:      "worker",
		LeaseGeneration: 4,
		LeaseExpiresAt:  time.Now().Add(time.Minute).Format(time.RFC3339Nano),
	}
	fence := jobs.Fence{Owner: "worker", Generation: 4}
	if !ValidRunLease(job, "run-1", fence) {
		t.Fatal("valid lease rejected")
	}
	job.ResourceID = "run-2"
	if ValidRunLease(job, "run-1", fence) {
		t.Fatal("mismatched run accepted")
	}
	if err := VerifyRunLease(context.Background(), "run-1", "job-1", fence, func(context.Context, string) (jobs.Job, error) {
		return job, nil
	}); err == nil {
		t.Fatal("mismatched lease accepted")
	}
	lookupErr := errors.New("lookup failed")
	if err := VerifyRunLease(context.Background(), "run-1", "job-1", fence, func(context.Context, string) (jobs.Job, error) {
		return jobs.Job{}, lookupErr
	}); !errors.Is(err, lookupErr) {
		t.Fatalf("lookup error = %v, want %v", err, lookupErr)
	}
}
