package postgres

import (
	"testing"

	jobdb "github.com/flidai/leapview/internal/platform/jobs/postgres/internal/db"
	"github.com/riverqueue/river/rivertype"
)

func TestRefreshOrphanReclaimableFailsClosed(t *testing.T) {
	riverID := int64(41)
	history := jobdb.LockJobForRefreshOrphanRescueRow{Kind: refreshPipelineKind, ResourceKind: refreshPipelineResourceKind, ResourceID: "run-1", Status: "running", AttemptCount: 1, RiverJobID: &riverID}
	riverJob := jobdb.LockRiverJobForRefreshOrphanRescueRow{ID: riverID, Kind: refreshPipelineKind, State: string(rivertype.JobStateRunning), Attempt: 1, AttemptedBy: []string{"dead-node"}, MaxAttempts: 3}
	if !refreshOrphanReclaimable(history, riverJob, "run-1", "dead-node", 1) {
		t.Fatal("valid expired refresh orphan was rejected")
	}
	tests := map[string]func(*jobdb.LockJobForRefreshOrphanRescueRow, *jobdb.LockRiverJobForRefreshOrphanRescueRow){
		"wrong kind": func(h *jobdb.LockJobForRefreshOrphanRescueRow, _ *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			h.Kind = "release.finalize"
		},
		"product not running": func(h *jobdb.LockJobForRefreshOrphanRescueRow, _ *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			h.Status = "succeeded"
		},
		"river not running": func(_ *jobdb.LockJobForRefreshOrphanRescueRow, r *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			r.State = string(rivertype.JobStateRetryable)
		},
		"attempt changed": func(_ *jobdb.LockJobForRefreshOrphanRescueRow, r *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			r.Attempt = 2
		},
		"owner absent": func(_ *jobdb.LockJobForRefreshOrphanRescueRow, r *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			r.AttemptedBy = nil
		},
		"owner changed": func(_ *jobdb.LockJobForRefreshOrphanRescueRow, r *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			r.AttemptedBy = []string{"new-owner"}
		},
		"attempts exhausted": func(_ *jobdb.LockJobForRefreshOrphanRescueRow, r *jobdb.LockRiverJobForRefreshOrphanRescueRow) {
			r.MaxAttempts = 1
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			h, r := history, riverJob
			change(&h, &r)
			if refreshOrphanReclaimable(h, r, "run-1", "dead-node", 1) {
				t.Fatal("incompatible orphan evidence was accepted")
			}
		})
	}
}
