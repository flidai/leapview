// Package postgresmaintenance composes the bounded retention authorities that
// run on the PostgreSQL control-plane maintenance role. It deliberately owns
// orchestration only: capability schemas and retention semantics remain in
// their respective packages.
package postgresmaintenance

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	cursorsigningpostgres "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/jackc/pgx/v5"
)

const maxBatchLimit = 1000

// OperationPruner is the narrow destructive surface retained from the
// operation capability.
type OperationPruner interface {
	Prune(context.Context, time.Time, int) (int64, error)
}

// CursorSigningPruner is the narrow destructive surface retained from the
// cursor-signing capability.
type CursorSigningPruner interface {
	Prune(context.Context, int) (int64, error)
}

// JobsPruner is the bounded terminal-job retention surface.
type JobsPruner interface {
	Prune(context.Context, time.Time, int) (int64, error)
}

// EventsPruner receives a caller-owned transaction. This keeps event-floor
// advancement and event deletion in the same transaction boundary selected by
// the orchestration layer.
type EventsPruner interface {
	Prune(context.Context, eventspostgres.Tx, time.Time) (int64, error)
}

// EventTxRunner owns begin/commit/rollback for one event-prune batch.
type EventTxRunner interface {
	Run(context.Context, func(eventspostgres.Tx) error) error
}

// CachePruner is the cache coordination retention surface.
type CachePruner interface {
	Prune(context.Context, cachepostgres.PruneOptions) (cachepostgres.PruneStats, error)
}

// DashboardSessionPruner is the bounded expired-session cleanup surface.
type DashboardSessionPruner interface {
	DeleteExpiredBatch(context.Context, int) (int64, error)
}

// DashboardUsagePruner is the bounded viewer-day cleanup surface.
type DashboardUsagePruner interface {
	DeleteBefore(context.Context, time.Time, int) (int64, error)
}

// DashboardPublicationPruner is the bounded publication-stream cleanup
// surface. The capability currently reports only success/failure, not a row
// count, so the result records whether the batch completed.
type DashboardPublicationPruner interface {
	PruneExpired(context.Context, time.Time, time.Time, int32) error
}

// ManagedDataPruner is the bounded upload-session cleanup surface.
type ManagedDataPruner interface {
	PruneUploadSessions(context.Context, time.Time, int) (int64, error)
}

// AccessAuditPruner owns bounded retention for the three policy classes
// encoded in access-audit envelopes. Unknown classes remain preserved by the
// capability owner function.
type AccessAuditPruner interface {
	Prune(context.Context, accesspostgres.RetentionClass, time.Time, int) (accesspostgres.AuditRetentionResult, error)
}

// AccessAuthStatePruner owns bounded cleanup of expired or revoked
// authentication state. It is independent from immutable audit evidence and
// therefore receives its own policy cutoff and result.
type AccessAuthStatePruner interface {
	PruneAuthState(context.Context, time.Time, int) (accesspostgres.AuthRetentionResult, error)
}

// QueryAuditPruner is the bounded query-evidence retention surface.
type QueryAuditPruner interface {
	Prune(context.Context, time.Time, int) (queryauditpostgres.PruneResult, error)
}

// AgentHistoryPruner is the bounded archived-conversation and terminal-run
// retention surface.
type AgentHistoryPruner interface {
	Prune(context.Context, time.Time, int) (agentpostgres.RetentionResult, error)
}

// Options contains only capability-owned retention ports. Every port must be
// backed by the separately authenticated control-plane maintenance role;
// runtime-backed repositories intentionally fail their SQL privilege checks
// for these operations. A nil port is a construction error; the coordinator
// never silently skips an authority.
type Options struct {
	Operations        OperationPruner
	CursorSigning     CursorSigningPruner
	Jobs              JobsPruner
	Events            EventsPruner
	EventTransactions EventTxRunner
	Cache             CachePruner
	DashboardSession  DashboardSessionPruner
	DashboardUsage    DashboardUsagePruner
	DashboardStreams  DashboardPublicationPruner
	ManagedData       ManagedDataPruner
	AccessAudit       AccessAuditPruner
	AccessAuthState   AccessAuthStatePruner
	QueryAudit        QueryAuditPruner
	AgentHistory      AgentHistoryPruner
}

// Coordinator invokes one bounded batch for each configured authority. A
// caller can schedule subsequent runs to drain a larger backlog without
// turning one maintenance invocation into an unbounded transaction.
type Coordinator struct {
	options Options
}

// New validates that every retention authority is present. The concrete
// imports below document the native implementations, but composition must
// construct destructive authorities with the bounded maintenance pool rather
// than reuse runtime-role repositories from the application graph.
func New(options Options) (*Coordinator, error) {
	missing := []struct {
		name  string
		value any
	}{
		{"operations", options.Operations},
		{"cursor signing", options.CursorSigning},
		{"jobs", options.Jobs},
		{"events", options.Events},
		{"event transaction runner", options.EventTransactions},
		{"cache", options.Cache},
		{"dashboard session", options.DashboardSession},
		{"dashboard usage", options.DashboardUsage},
		{"dashboard publication streams", options.DashboardStreams},
		{"managed data", options.ManagedData},
		{"access audit", options.AccessAudit},
		{"access auth state", options.AccessAuthState},
		{"query audit", options.QueryAudit},
		{"agent history", options.AgentHistory},
	}
	for _, item := range missing {
		if nilAuthority(item.value) {
			return nil, fmt.Errorf("postgres retention authority %s is required", item.name)
		}
	}
	return &Coordinator{options: options}, nil
}

func nilAuthority(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// OperationPolicy controls one bounded operation-history batch.
type OperationPolicy struct {
	Before time.Time
	Limit  int
}

// CursorSigningPolicy controls one bounded retired-key batch.
type CursorSigningPolicy struct{ Limit int }

// JobsPolicy controls one bounded terminal-job batch.
type JobsPolicy struct {
	Before time.Time
	Limit  int
}

// EventsPolicy controls one bounded event-log batch. The event capability
// owns its batch size (currently 1000) because the floor update is part of
// the owner function.
type EventsPolicy struct{ Before time.Time }

// CachePolicy controls one bounded coordination-row batch.
type CachePolicy struct {
	Before time.Time
	Limit  int
}

// DashboardSessionPolicy controls one bounded expired-session batch.
type DashboardSessionPolicy struct{ Limit int }

// DashboardUsagePolicy controls one bounded viewer-day batch.
type DashboardUsagePolicy struct {
	Before time.Time
	Limit  int
}

// DashboardPublicationPolicy controls one bounded expired-stream batch.
type DashboardPublicationPolicy struct {
	Now   time.Time
	Limit int
}

// ManagedDataPolicy controls one bounded upload-session batch.
type ManagedDataPolicy struct {
	Before time.Time
	Limit  int
}

// RetentionWindow controls one bounded time-based evidence batch.
type RetentionWindow struct {
	Before   time.Time
	Limit    int
	Disabled bool
}

// AccessAuditPolicy keeps every known audit class explicit. Security evidence
// therefore cannot accidentally inherit the shorter operational cutoff.
type AccessAuditPolicy struct {
	Short    RetentionWindow
	Standard RetentionWindow
	Security RetentionWindow
}

// Policy is explicit per capability so retention cutoffs cannot be
// accidentally inferred from a sibling store's clock or policy.
type Policy struct {
	Operations       OperationPolicy
	CursorSigning    CursorSigningPolicy
	Jobs             JobsPolicy
	Events           EventsPolicy
	Cache            CachePolicy
	DashboardSession DashboardSessionPolicy
	DashboardUsage   DashboardUsagePolicy
	DashboardStreams DashboardPublicationPolicy
	ManagedData      ManagedDataPolicy
	AccessAudit      AccessAuditPolicy
	AccessAuthState  RetentionWindow
	QueryAudit       RetentionWindow
	AgentHistory     RetentionWindow
}

func (p Policy) Validate() error {
	if p.Operations.Before.IsZero() {
		return errors.New("operations retention cutoff is required")
	}
	if err := validateLimit("operations", p.Operations.Limit); err != nil {
		return err
	}
	if err := validateLimit("cursor signing", p.CursorSigning.Limit); err != nil {
		return err
	}
	if p.Jobs.Before.IsZero() {
		return errors.New("jobs retention cutoff is required")
	}
	if err := validateLimit("jobs", p.Jobs.Limit); err != nil {
		return err
	}
	if p.Events.Before.IsZero() {
		return errors.New("events retention cutoff is required")
	}
	if p.Cache.Before.IsZero() {
		return errors.New("cache retention cutoff is required")
	}
	if err := validateLimit("cache", p.Cache.Limit); err != nil {
		return err
	}
	if err := validateLimit("dashboard session", p.DashboardSession.Limit); err != nil {
		return err
	}
	if p.DashboardUsage.Before.IsZero() {
		return errors.New("dashboard usage retention cutoff is required")
	}
	if err := validateLimit("dashboard usage", p.DashboardUsage.Limit); err != nil {
		return err
	}
	if p.DashboardStreams.Now.IsZero() {
		return errors.New("dashboard publication retention clock is required")
	}
	if err := validateLimit("dashboard publication", p.DashboardStreams.Limit); err != nil {
		return err
	}
	if p.ManagedData.Before.IsZero() {
		return errors.New("managed-data retention cutoff is required")
	}
	if err := validateLimit("managed-data", p.ManagedData.Limit); err != nil {
		return err
	}
	for _, window := range []struct {
		name   string
		policy RetentionWindow
	}{
		{"access audit short", p.AccessAudit.Short},
		{"access audit standard", p.AccessAudit.Standard},
		{"access audit security", p.AccessAudit.Security},
		{"access auth state", p.AccessAuthState},
		{"query audit", p.QueryAudit},
		{"agent history", p.AgentHistory},
	} {
		if window.policy.Disabled {
			continue
		}
		if window.policy.Before.IsZero() {
			return fmt.Errorf("%s retention cutoff is required", window.name)
		}
		if err := validateLimit(window.name, window.policy.Limit); err != nil {
			return err
		}
	}
	return nil
}

func validateLimit(name string, limit int) error {
	if limit < 1 || limit > maxBatchLimit {
		return fmt.Errorf("%s retention batch limit must be between 1 and %d", name, maxBatchLimit)
	}
	return nil
}

// Result reports each capability's bounded batch independently. A caller can
// persist or emit this evidence before scheduling the next batch.
type Result struct {
	OperationsRemoved                int64
	CursorSigningRemoved             int64
	JobsRemoved                      int64
	EventsRemoved                    int64
	Cache                            cachepostgres.PruneStats
	DashboardSessionsRemoved         int64
	DashboardUsageRemoved            int64
	DashboardPublicationBatchDone    bool
	ManagedDataUploadSessionsRemoved int64
	AccessAuditShort                 accesspostgres.AuditRetentionResult
	AccessAuditStandard              accesspostgres.AuditRetentionResult
	AccessAuditSecurity              accesspostgres.AuditRetentionResult
	AccessAuthState                  accesspostgres.AuthRetentionResult
	QueryAudit                       queryauditpostgres.PruneResult
	AgentHistory                     agentpostgres.RetentionResult
}

// Run executes one bounded batch per capability in a stable order. It stops
// at the first error and returns the partial result so callers retain evidence
// of work that committed before the failure.
func (c *Coordinator) Run(ctx context.Context, policy Policy) (Result, error) {
	if c == nil {
		return Result{}, errors.New("postgres retention coordinator is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := policy.Validate(); err != nil {
		return Result{}, err
	}
	result := Result{}
	var err error
	if result.OperationsRemoved, err = c.options.Operations.Prune(ctx, policy.Operations.Before, policy.Operations.Limit); err != nil {
		return result, fmt.Errorf("prune operations: %w", err)
	}
	if result.CursorSigningRemoved, err = c.options.CursorSigning.Prune(ctx, policy.CursorSigning.Limit); err != nil {
		return result, fmt.Errorf("prune cursor signing keys: %w", err)
	}
	if result.JobsRemoved, err = c.options.Jobs.Prune(ctx, policy.Jobs.Before, policy.Jobs.Limit); err != nil {
		return result, fmt.Errorf("prune jobs: %w", err)
	}
	if result.EventsRemoved, err = pruneEvents(ctx, c.options.EventTransactions, c.options.Events, policy.Events.Before); err != nil {
		return result, fmt.Errorf("prune events: %w", err)
	}
	if result.Cache, err = c.options.Cache.Prune(ctx, cachepostgres.PruneOptions{Before: policy.Cache.Before, Limit: policy.Cache.Limit}); err != nil {
		return result, fmt.Errorf("prune cache coordination: %w", err)
	}
	if result.DashboardSessionsRemoved, err = c.options.DashboardSession.DeleteExpiredBatch(ctx, policy.DashboardSession.Limit); err != nil {
		return result, fmt.Errorf("prune dashboard sessions: %w", err)
	}
	if result.DashboardUsageRemoved, err = c.options.DashboardUsage.DeleteBefore(ctx, policy.DashboardUsage.Before, policy.DashboardUsage.Limit); err != nil {
		return result, fmt.Errorf("prune dashboard usage: %w", err)
	}
	if err = c.options.DashboardStreams.PruneExpired(ctx, policy.DashboardStreams.Now, policy.DashboardStreams.Now, int32(policy.DashboardStreams.Limit)); err != nil {
		return result, fmt.Errorf("prune dashboard publication streams: %w", err)
	}
	result.DashboardPublicationBatchDone = true
	if result.ManagedDataUploadSessionsRemoved, err = c.options.ManagedData.PruneUploadSessions(ctx, policy.ManagedData.Before, policy.ManagedData.Limit); err != nil {
		return result, fmt.Errorf("prune managed-data upload sessions: %w", err)
	}
	if !policy.AccessAudit.Short.Disabled {
		if result.AccessAuditShort, err = c.options.AccessAudit.Prune(ctx, accesspostgres.RetentionShort, policy.AccessAudit.Short.Before, policy.AccessAudit.Short.Limit); err != nil {
			return result, fmt.Errorf("prune short access audit: %w", err)
		}
	}
	if !policy.AccessAudit.Standard.Disabled {
		if result.AccessAuditStandard, err = c.options.AccessAudit.Prune(ctx, accesspostgres.RetentionStandard, policy.AccessAudit.Standard.Before, policy.AccessAudit.Standard.Limit); err != nil {
			return result, fmt.Errorf("prune standard access audit: %w", err)
		}
	}
	if !policy.AccessAudit.Security.Disabled {
		if result.AccessAuditSecurity, err = c.options.AccessAudit.Prune(ctx, accesspostgres.RetentionSecurity, policy.AccessAudit.Security.Before, policy.AccessAudit.Security.Limit); err != nil {
			return result, fmt.Errorf("prune security access audit: %w", err)
		}
	}
	if !policy.AccessAuthState.Disabled {
		if result.AccessAuthState, err = c.options.AccessAuthState.PruneAuthState(ctx, policy.AccessAuthState.Before, policy.AccessAuthState.Limit); err != nil {
			return result, fmt.Errorf("prune access auth state: %w", err)
		}
	}
	if !policy.QueryAudit.Disabled {
		if result.QueryAudit, err = c.options.QueryAudit.Prune(ctx, policy.QueryAudit.Before, policy.QueryAudit.Limit); err != nil {
			return result, fmt.Errorf("prune query audit: %w", err)
		}
	}
	if !policy.AgentHistory.Disabled {
		if result.AgentHistory, err = c.options.AgentHistory.Prune(ctx, policy.AgentHistory.Before, policy.AgentHistory.Limit); err != nil {
			return result, fmt.Errorf("prune agent history: %w", err)
		}
	}
	return result, nil
}

func pruneEvents(ctx context.Context, runner EventTxRunner, pruner EventsPruner, before time.Time) (int64, error) {
	var removed int64
	err := runner.Run(ctx, func(tx eventspostgres.Tx) error {
		var err error
		removed, err = pruner.Prune(ctx, tx, before)
		return err
	})
	return removed, err
}

// PgxEventTxRunner adapts a native pgx transaction beginner to EventTxRunner.
// It is intentionally separate from Coordinator so tests can supply a fake
// runner without constructing a pgx transaction.
type PgxEventTxRunner struct {
	begin func(context.Context) (pgx.Tx, error)
}

// NewPgxEventTxRunner constructs a begin/commit/rollback boundary for event
// retention. A nil beginner is rejected rather than deferred to a panic.
func NewPgxEventTxRunner(begin func(context.Context) (pgx.Tx, error)) (*PgxEventTxRunner, error) {
	if begin == nil {
		return nil, errors.New("event transaction beginner is required")
	}
	return &PgxEventTxRunner{begin: begin}, nil
}

func (r *PgxEventTxRunner) Run(ctx context.Context, fn func(eventspostgres.Tx) error) error {
	if r == nil || r.begin == nil {
		return errors.New("event transaction beginner is unavailable")
	}
	if fn == nil {
		return errors.New("event transaction callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := r.begin(ctx)
	if err != nil {
		return err
	}
	if nilAuthority(tx) {
		return errors.New("event transaction beginner returned nil transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// Keep the imports/types above tied to the concrete native authorities while
// retaining narrow interfaces for tests and future maintenance-role
// composition.
var (
	_ OperationPruner            = (*operationpostgres.Maintenance)(nil)
	_ CursorSigningPruner        = (*cursorsigningpostgres.Maintenance)(nil)
	_ JobsPruner                 = (*jobspostgres.Maintenance)(nil)
	_ EventsPruner               = (*eventspostgres.Repository)(nil)
	_ CachePruner                = (*cachepostgres.Maintenance)(nil)
	_ DashboardSessionPruner     = (*dashboardsessionpostgres.Maintenance)(nil)
	_ DashboardUsagePruner       = (*dashboardusagepostgres.Maintenance)(nil)
	_ DashboardPublicationPruner = (*dashboardpublicationpostgres.Maintenance)(nil)
	_ ManagedDataPruner          = (*manageddatapostgres.Maintenance)(nil)
	_ AccessAuditPruner          = (*accesspostgres.Maintenance)(nil)
	_ AccessAuthStatePruner      = (*accesspostgres.Maintenance)(nil)
	_ QueryAuditPruner           = (*queryauditpostgres.Maintenance)(nil)
	_ AgentHistoryPruner         = (*agentpostgres.Maintenance)(nil)
	_ EventTxRunner              = (*PgxEventTxRunner)(nil)
)
