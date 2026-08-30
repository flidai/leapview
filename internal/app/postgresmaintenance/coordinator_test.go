package postgresmaintenance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeOperations struct{ calls *[]string }

func (f fakeOperations) Prune(context.Context, time.Time, int) (int64, error) {
	*f.calls = append(*f.calls, "operations")
	return 1, nil
}

type fakeCursorSigning struct{ calls *[]string }

func (f fakeCursorSigning) Prune(context.Context, int) (int64, error) {
	*f.calls = append(*f.calls, "cursor signing")
	return 2, nil
}

type fakeJobs struct{ calls *[]string }

func (f fakeJobs) Prune(context.Context, time.Time, int) (int64, error) {
	*f.calls = append(*f.calls, "jobs")
	return 3, nil
}

type fakeEvents struct {
	calls *[]string
	tx    bool
}

func (f *fakeEvents) Prune(_ context.Context, tx eventspostgres.Tx, _ time.Time) (int64, error) {
	*f.calls = append(*f.calls, "events")
	f.tx = tx != nil
	return 4, nil
}

type fakeEventTransactions struct {
	calls *[]string
}

func (f fakeEventTransactions) Run(_ context.Context, fn func(eventspostgres.Tx) error) error {
	*f.calls = append(*f.calls, "event transaction")
	return fn(fakeEventTx{})
}

type fakeEventTx struct{}

func (fakeEventTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (fakeEventTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (fakeEventTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }

type fakeCache struct{ calls *[]string }

func (f fakeCache) Prune(context.Context, cachepostgres.PruneOptions) (cachepostgres.PruneStats, error) {
	*f.calls = append(*f.calls, "cache")
	return cachepostgres.PruneStats{Invalidations: 5, ExpiredLeases: 6}, nil
}

type fakeDashboardSession struct{ calls *[]string }

func (f fakeDashboardSession) DeleteExpiredBatch(context.Context, int) (int64, error) {
	*f.calls = append(*f.calls, "dashboard session")
	return 7, nil
}

type fakeDashboardUsage struct{ calls *[]string }

func (f fakeDashboardUsage) DeleteBefore(context.Context, time.Time, int) (int64, error) {
	*f.calls = append(*f.calls, "dashboard usage")
	return 8, nil
}

type fakeDashboardStreams struct{ calls *[]string }

func (f fakeDashboardStreams) PruneExpired(context.Context, time.Time, time.Time, int32) error {
	*f.calls = append(*f.calls, "dashboard streams")
	return nil
}

type fakeManagedData struct{ calls *[]string }

func (f fakeManagedData) PruneUploadSessions(context.Context, time.Time, int) (int64, error) {
	*f.calls = append(*f.calls, "managed data")
	return 9, nil
}

type fakeAccessAudit struct{ calls *[]string }

func (f fakeAccessAudit) Prune(_ context.Context, class accesspostgres.RetentionClass, before time.Time, limit int) (accesspostgres.AuditRetentionResult, error) {
	*f.calls = append(*f.calls, "access audit "+string(class))
	return accesspostgres.AuditRetentionResult{RetentionClass: class, RequestedCutoff: before, Cutoff: before, RequestedLimit: limit, RemovedCount: 10, RetainedFloor: before}, nil
}

type fakeQueryAudit struct{ calls *[]string }

func (f fakeQueryAudit) Prune(_ context.Context, before time.Time, _ int) (queryauditpostgres.PruneResult, error) {
	*f.calls = append(*f.calls, "query audit")
	return queryauditpostgres.PruneResult{Before: before, Cutoff: before, FloorAt: before, Removed: 3}, nil
}

type fakeAgentHistory struct{ calls *[]string }

func (f fakeAgentHistory) Prune(_ context.Context, before time.Time, limit int) (agentpostgres.RetentionResult, error) {
	*f.calls = append(*f.calls, "agent history")
	return agentpostgres.RetentionResult{Before: before, Cutoff: before, RequestedLimit: limit, RunEventsDeleted: 4, ConversationsFloorAt: before, RunEventsFloorAt: before}, nil
}

func testOptions(calls *[]string, events *fakeEvents) Options {
	return Options{
		Operations:        fakeOperations{calls},
		CursorSigning:     fakeCursorSigning{calls},
		Jobs:              fakeJobs{calls},
		Events:            events,
		EventTransactions: fakeEventTransactions{calls},
		Cache:             fakeCache{calls},
		DashboardSession:  fakeDashboardSession{calls},
		DashboardUsage:    fakeDashboardUsage{calls},
		DashboardStreams:  fakeDashboardStreams{calls},
		ManagedData:       fakeManagedData{calls},
		AccessAudit:       fakeAccessAudit{calls},
		QueryAudit:        fakeQueryAudit{calls},
		AgentHistory:      fakeAgentHistory{calls},
	}
}

func testPolicy() Policy {
	cutoff := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return Policy{
		Operations:       OperationPolicy{Before: cutoff, Limit: 10},
		CursorSigning:    CursorSigningPolicy{Limit: 10},
		Jobs:             JobsPolicy{Before: cutoff, Limit: 10},
		Events:           EventsPolicy{Before: cutoff},
		Cache:            CachePolicy{Before: cutoff, Limit: 10},
		DashboardSession: DashboardSessionPolicy{Limit: 10},
		DashboardUsage:   DashboardUsagePolicy{Before: cutoff, Limit: 10},
		DashboardStreams: DashboardPublicationPolicy{Now: cutoff, Limit: 10},
		ManagedData:      ManagedDataPolicy{Before: cutoff, Limit: 10},
		AccessAudit: AccessAuditPolicy{
			Short:    RetentionWindow{Before: cutoff, Limit: 10},
			Standard: RetentionWindow{Before: cutoff, Limit: 10},
			Security: RetentionWindow{Before: cutoff, Limit: 10},
		},
		QueryAudit:   RetentionWindow{Before: cutoff, Limit: 10},
		AgentHistory: RetentionWindow{Before: cutoff, Limit: 10},
	}
}

func TestNewRejectsMissingAuthority(t *testing.T) {
	calls := []string{}
	events := &fakeEvents{calls: &calls}
	options := testOptions(&calls, events)
	options.Cache = nil
	if _, err := New(options); err == nil || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("New() error = %v, want missing cache authority", err)
	}
}

func TestRunInvokesEveryBoundedAuthorityAndPreservesEventTransaction(t *testing.T) {
	calls := []string{}
	events := &fakeEvents{calls: &calls}
	coordinator, err := New(testOptions(&calls, events))
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Run(context.Background(), testPolicy())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantCalls := []string{"operations", "cursor signing", "jobs", "event transaction", "events", "cache", "dashboard session", "dashboard usage", "dashboard streams", "managed data", "access audit short", "access audit standard", "access audit security", "query audit", "agent history"}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	if !events.tx {
		t.Fatal("events pruner did not receive caller-owned transaction")
	}
	cutoff := testPolicy().AccessAudit.Short.Before
	want := Result{
		OperationsRemoved: 1, CursorSigningRemoved: 2, JobsRemoved: 3, EventsRemoved: 4,
		Cache: cachepostgres.PruneStats{Invalidations: 5, ExpiredLeases: 6}, DashboardSessionsRemoved: 7,
		DashboardUsageRemoved: 8, DashboardPublicationBatchDone: true, ManagedDataUploadSessionsRemoved: 9,
		AccessAuditShort:    accesspostgres.AuditRetentionResult{RetentionClass: accesspostgres.RetentionShort, RequestedCutoff: cutoff, Cutoff: cutoff, RequestedLimit: 10, RemovedCount: 10, RetainedFloor: cutoff},
		AccessAuditStandard: accesspostgres.AuditRetentionResult{RetentionClass: accesspostgres.RetentionStandard, RequestedCutoff: cutoff, Cutoff: cutoff, RequestedLimit: 10, RemovedCount: 10, RetainedFloor: cutoff},
		AccessAuditSecurity: accesspostgres.AuditRetentionResult{RetentionClass: accesspostgres.RetentionSecurity, RequestedCutoff: cutoff, Cutoff: cutoff, RequestedLimit: 10, RemovedCount: 10, RetainedFloor: cutoff},
		QueryAudit:          queryauditpostgres.PruneResult{Before: cutoff, Cutoff: cutoff, FloorAt: cutoff, Removed: 3},
		AgentHistory:        agentpostgres.RetentionResult{Before: cutoff, Cutoff: cutoff, RequestedLimit: 10, RunEventsDeleted: 4, ConversationsFloorAt: cutoff, RunEventsFloorAt: cutoff},
	}
	if result != want {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestRunValidatesPolicyBeforeCallingAuthorities(t *testing.T) {
	calls := []string{}
	coordinator, err := New(testOptions(&calls, &fakeEvents{calls: &calls}))
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.Cache.Limit = 0
	if _, err := coordinator.Run(context.Background(), policy); err == nil || !strings.Contains(err.Error(), "cache") {
		t.Fatalf("Run() error = %v, want cache limit validation", err)
	}
	if len(calls) != 0 {
		t.Fatalf("authorities called during invalid policy: %v", calls)
	}
}

func TestPolicyRequiresIndependentEvidenceWindows(t *testing.T) {
	for _, mutate := range []func(*Policy){
		func(p *Policy) { p.AccessAudit.Short.Before = time.Time{} },
		func(p *Policy) { p.AccessAudit.Standard.Limit = 1001 },
		func(p *Policy) { p.AccessAudit.Security.Before = time.Time{} },
		func(p *Policy) { p.QueryAudit.Limit = 0 },
		func(p *Policy) { p.AgentHistory.Before = time.Time{} },
	} {
		policy := testPolicy()
		mutate(&policy)
		if err := policy.Validate(); err == nil {
			t.Fatalf("Validate() accepted invalid evidence policy %#v", policy)
		}
	}
}

func TestRunReturnsPartialResultOnAuthorityError(t *testing.T) {
	calls := []string{}
	coordinator, err := New(testOptions(&calls, &fakeEvents{calls: &calls}))
	if err != nil {
		t.Fatal(err)
	}
	coordinator.options.Jobs = failingJobs{}
	result, err := coordinator.Run(context.Background(), testPolicy())
	if err == nil || !strings.Contains(err.Error(), "prune jobs") {
		t.Fatalf("Run() error = %v, want jobs context", err)
	}
	if result.OperationsRemoved != 1 || result.CursorSigningRemoved != 2 || result.JobsRemoved != 0 {
		t.Fatalf("partial result = %#v", result)
	}
}

type failingJobs struct{}

func (failingJobs) Prune(context.Context, time.Time, int) (int64, error) {
	return 0, errors.New("job store unavailable")
}

func TestNewPgxEventTxRunnerRequiresBeginner(t *testing.T) {
	if _, err := NewPgxEventTxRunner(nil); err == nil {
		t.Fatal("NewPgxEventTxRunner(nil) unexpectedly succeeded")
	}
}
