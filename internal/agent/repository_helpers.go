package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/pkg/jobs"
)

// PageByID applies the bounded cursor semantics shared by native repository
// adapters after they have mapped backend rows into agent records.
func PageByID[T any](rows []T, page Page, id func(T) string) []T {
	limit := page.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	start := 0
	if after := strings.TrimSpace(page.After); after != "" {
		start = len(rows)
		for i, row := range rows {
			if id(row) == after {
				start = i + 1
				break
			}
		}
	}
	if start >= len(rows) {
		return []T{}
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	return append([]T(nil), rows[start:end]...)
}

// LeaseUnexpired reports whether a persisted lease timestamp is valid and in
// the future. The accepted layouts match both native repository backends.
func LeaseUnexpired(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.After(time.Now())
		}
	}
	return false
}

// ValidRunLease checks the backend-neutral portion of an agent run lease
// claim. Repository adapters retain ownership of fetching the durable job and
// choosing their backend-specific error behavior.
func ValidRunLease(job jobs.Job, runID string, fence jobs.Fence) bool {
	return job.Kind == "agent.run" &&
		job.ResourceKind == "agent_run" &&
		job.ResourceID == runID &&
		job.Status == jobs.StatusRunning &&
		job.Fence() == fence &&
		LeaseUnexpired(job.LeaseExpiresAt)
}

// VerifyRunLease performs the shared lease-fencing contract after a backend
// adapter has supplied its own job lookup. Passing the lookup keeps SQL and
// transaction ownership in the adapter while maintaining one validation
// implementation.
func VerifyRunLease(ctx context.Context, runID, jobID string, fence jobs.Fence, get func(context.Context, string) (jobs.Job, error)) error {
	job, err := get(ctx, jobID)
	if err != nil {
		return err
	}
	if !ValidRunLease(job, runID, fence) {
		return errors.New("stale durable job claim")
	}
	return nil
}
