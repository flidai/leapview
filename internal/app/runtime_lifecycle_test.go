package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type auditOutboxStatsStore struct {
	stats access.AuditOutboxStats
	err   error
}

func (s auditOutboxStatsStore) AuditOutboxStats(context.Context, time.Time) (access.AuditOutboxStats, error) {
	return s.stats, s.err
}

func TestAuditOutboxReadinessReportsOnlyAggregateFailures(t *testing.T) {
	tests := []struct {
		name  string
		stats access.AuditOutboxStats
		want  string
	}{
		{name: "healthy"},
		{name: "terminal", stats: access.AuditOutboxStats{Poison: 2, Quarantined: 1}, want: "poison=2 quarantined=1"},
		{name: "count", stats: access.AuditOutboxStats{Pending: auditOutboxReadinessMaxUndelivered + 1}, want: "count=10001"},
		{name: "capacity", stats: access.AuditOutboxStats{Pending: 4, Capacity: 4}, want: "capacity exhausted"},
		{name: "age", stats: access.AuditOutboxStats{OldestUndeliveredAge: auditOutboxReadinessMaxAge + time.Minute}, want: "age=1h1m0s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := auditOutboxReadiness(t.Context(), auditOutboxStatsStore{stats: test.stats})
			if test.want == "" {
				if err != nil {
					t.Fatalf("readiness error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readiness error = %v, want substring %q", err, test.want)
			}
			if strings.Contains(err.Error(), "event") || strings.Contains(err.Error(), "metadata") {
				t.Fatalf("readiness error exposed event payload details: %v", err)
			}
		})
	}
}

func TestAuditOutboxReadinessHidesStoreErrors(t *testing.T) {
	err := auditOutboxReadiness(t.Context(), auditOutboxStatsStore{err: context.DeadlineExceeded})
	if err == nil || err.Error() != "audit outbox unavailable" {
		t.Fatalf("readiness error = %v, want stable unavailable error", err)
	}
}
