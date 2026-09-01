// Package adminpostgres adapts production Admin commands to native PostgreSQL
// control-plane authorities. Operations without a native owner remain
// delegated to the legacy offline implementation until they are migrated,
// except storage cleanup, which fails closed until a native physical cleanup
// owner is composed.
package adminpostgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	appadminoffline "github.com/flidai/leapview/internal/app/adminoffline"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/postgresbaseline"
	"github.com/flidai/leapview/internal/app/postgresmaintenance"
	dashboardusage "github.com/flidai/leapview/internal/dashboard/usage"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	instancelock "github.com/flidai/leapview/internal/platform/locking"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

const (
	// Every owner function is bounded. A later invocation can drain a larger
	// backlog without turning one operator command into an unbounded delete.
	maintenanceBatchLimit  = 1000
	defaultJobRetention    = 30 * 24 * time.Hour
	defaultCacheRetention  = 24 * time.Hour
	defaultUploadRetention = 30 * 24 * time.Hour
	// Event-log replay roots are independent of access-audit classes. When an
	// operator disables audit evidence pruning, retain the event log for the
	// normal audit default rather than disabling this safety boundary too.
	defaultEventRetention = 365 * 24 * time.Hour
)

// ErrNativeStorageCleanupUnavailable is returned when production Admin is
// asked to clean serving-state or DuckLake storage before a native physical
// cleanup owner has been composed. Keeping this as a stable sentinel prevents
// the embedded offline adapter from being selected accidentally.
var ErrNativeStorageCleanupUnavailable = errors.New("PostgreSQL native storage cleanup is unavailable")

// Native is the small execution surface needed by this CLI adapter. Keeping
// it separate from postgresmaintenance.Native makes preview/apply selection
// directly injectable in unit tests.
type Native interface {
	Preview(context.Context, postgresmaintenance.Policy) (postgresmaintenance.Result, error)
	Run(context.Context, postgresmaintenance.Policy) (postgresmaintenance.Result, error)
}

// MaintenancePool is the exact pool surface used by production maintenance:
// native SQL execution, one-connection transactions, schema verification,
// and ownership shutdown. It intentionally has no runtime or DuckLake pools.
type MaintenancePool interface {
	postgresmaintenance.NativeDB
	postgresbaseline.RevisionReader
	Close()
}

// AccessPool is the native control-plane surface needed by initialization.
// It deliberately carries only the pgx methods, transaction opener, baseline
// reader, and lifecycle close operation; it cannot migrate the schema.
type AccessPool interface {
	accesspostgres.DBTX
	postgresbaseline.RevisionReader
	Begin(context.Context) (pgx.Tx, error)
	Close()
}

// AccessInitializer is the audited, transactional Access bootstrap owned by
// the access PostgreSQL capability.
type AccessInitializer interface {
	Initialized(context.Context) (bool, error)
	InitializeInstance(context.Context, access.InstanceInitializationInput, func(access.InitialInstanceCredentials) error) (access.InitialInstanceCredentials, error)
}

// Bootstrap is the native platform authority used to check the initialization
// marker and permanently bind the instance environment.
type Bootstrap interface {
	InstanceEnvironment(context.Context) (string, error)
	BindInstanceEnvironment(context.Context, string) error
}

// Dependencies are injectable seams for the production adapter. The zero
// value uses the real config loader, native PostgreSQL opener, baseline
// verifier, and Native constructor.
type Dependencies struct {
	LoadConfig      func() (config.Config, error)
	OpenMaintenance func(context.Context, platformpostgres.Config) (MaintenancePool, error)
	OpenAccess      func(context.Context, platformpostgres.Config) (AccessPool, error)
	VerifyBaseline  func(context.Context, postgresbaseline.RevisionReader) error
	NewNative       func(postgresmaintenance.NativeDB) (Native, error)
	NewAccess       func(AccessPool, []byte) (AccessInitializer, error)
	NewBootstrap    func(AccessPool) Bootstrap
	BootstrapPool   func(context.Context, config.Config, adminoffline.PhysicalPoolBootstrapRequest) (adminoffline.PhysicalPoolBootstrapResult, error)
	AcquireLock     func(string) (adminoffline.Lock, error)
	Now             func() time.Time
}

// Operations preserves every existing offline Admin operation and overrides
// Maintenance and StorageCleanup. This keeps non-production behavior
// byte-for-byte on the legacy path while production operations use native
// owners or fail closed until one is composed.
type Operations struct {
	appadminoffline.Operations
	Dependencies Dependencies
}

func (d Dependencies) withDefaults() Dependencies {
	if d.LoadConfig == nil {
		d.LoadConfig = config.Load
	}
	if d.OpenMaintenance == nil {
		d.OpenMaintenance = func(ctx context.Context, cfg platformpostgres.Config) (MaintenancePool, error) {
			return platformpostgres.Open(ctx, cfg)
		}
	}
	if d.OpenAccess == nil {
		d.OpenAccess = func(ctx context.Context, cfg platformpostgres.Config) (AccessPool, error) {
			return platformpostgres.OpenControl(ctx, cfg)
		}
	}
	if d.VerifyBaseline == nil {
		d.VerifyBaseline = postgresbaseline.Verify
	}
	if d.NewNative == nil {
		d.NewNative = func(db postgresmaintenance.NativeDB) (Native, error) {
			return postgresmaintenance.NewNative(db)
		}
	}
	if d.NewAccess == nil {
		d.NewAccess = func(pool AccessPool, key []byte) (AccessInitializer, error) {
			return accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: key})
		}
	}
	if d.NewBootstrap == nil {
		d.NewBootstrap = func(pool AccessPool) Bootstrap {
			return platformbootstrap.New(pool)
		}
	}
	if d.BootstrapPool == nil {
		d.BootstrapPool = bootstrapNativePhysicalPool
	}
	if d.AcquireLock == nil {
		d.AcquireLock = func(home string) (adminoffline.Lock, error) {
			return instancelock.Acquire(home)
		}
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return d
}

// New returns production-aware Admin operations with explicit seams. Most
// callers can use Operations{} and receive the real dependencies.
func New(dependencies Dependencies) Operations {
	return Operations{Dependencies: dependencies.withDefaults()}
}

// StorageCleanup keeps the legacy SQLite/catalog-file cleanup adapter out of
// production Admin. A native physical cleanup owner is not composed yet, so
// production requests fail closed with a stable error. Non-production and
// isolated evaluation environments retain the existing offline behavior.
func (o Operations) StorageCleanup(ctx context.Context, request adminoffline.StorageCleanupRequest, out io.Writer) error {
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if cfg.Production && !cfg.EvaluationMode {
		return ErrNativeStorageCleanupUnavailable
	}
	return o.Operations.StorageCleanup(ctx, request, out)
}

// Maintenance executes native PostgreSQL retention in production. Preview is
// the default and always rolls back; --apply invokes the committing runner.
// Non-production and isolated evaluation targets delegate to the legacy
// SQLite-backed service.
func (o Operations) Maintenance(ctx context.Context, request adminoffline.MaintenanceRequest, out io.Writer) error {
	if err := validateRetentionDays(request); err != nil {
		return err
	}
	deps := o.Dependencies.withDefaults()
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if !cfg.Production || cfg.EvaluationMode {
		return o.Operations.Maintenance(ctx, request, out)
	}
	if out == nil {
		return errors.New("admin maintenance output is required")
	}
	now := deps.Now()
	if now.IsZero() {
		return errors.New("admin maintenance clock returned zero")
	}
	policy, err := MapMaintenanceRequest(request, now.UTC())
	if err != nil {
		return err
	}

	// Production maintenance deliberately opens exactly one independently
	// authenticated pool. It never calls OpenControlPlane, opens SQLite, or
	// retains runtime, migrator, readonly, or DuckLake credentials.
	maintenanceConfig := cfg.PostgresControlMaintenanceConfig()
	if strings.TrimSpace(maintenanceConfig.URL) == "" {
		return errors.New("production admin maintenance requires LEAPVIEW_POSTGRES_CONTROL_MAINTENANCE_URL")
	}
	pool, err := deps.OpenMaintenance(ctx, maintenanceConfig)
	if err != nil {
		return fmt.Errorf("open PostgreSQL maintenance pool: %w", err)
	}
	if nilMaintenancePool(pool) {
		return errors.New("open PostgreSQL maintenance pool returned nil pool")
	}
	defer pool.Close()
	if err := deps.VerifyBaseline(ctx, pool); err != nil {
		return fmt.Errorf("verify PostgreSQL control baseline before maintenance: %w", err)
	}
	native, err := deps.NewNative(pool)
	if err != nil {
		return fmt.Errorf("construct PostgreSQL native maintenance: %w", err)
	}
	if native == nil || (reflect.ValueOf(native).Kind() == reflect.Pointer && reflect.ValueOf(native).IsNil()) {
		return errors.New("construct PostgreSQL native maintenance returned nil runner")
	}
	mode := "preview"
	var result postgresmaintenance.Result
	if request.Apply {
		mode = "apply"
		result, err = native.Run(ctx, policy)
	} else {
		result, err = native.Preview(ctx, policy)
	}
	if err != nil {
		return fmt.Errorf("PostgreSQL maintenance %s: %w", mode, err)
	}
	return writeEvidence(out, mode, result)
}

func nilMaintenancePool(pool MaintenancePool) bool {
	if pool == nil {
		return true
	}
	rv := reflect.ValueOf(pool)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// MapMaintenanceRequest translates CLI retention flags to every native
// owner explicitly. A zero value disables only its requested evidence class;
// operational safety cleanup remains enabled with conservative defaults.
func MapMaintenanceRequest(request adminoffline.MaintenanceRequest, now time.Time) (postgresmaintenance.Policy, error) {
	if err := validateRetentionDays(request); err != nil {
		return postgresmaintenance.Policy{}, err
	}
	if now.IsZero() {
		return postgresmaintenance.Policy{}, errors.New("maintenance cutoff clock is required")
	}
	now = now.UTC()
	cutoff := func(days int) time.Time { return now.Add(-time.Duration(days) * 24 * time.Hour) }
	auditDisabled := request.AuditDays == 0
	eventsBefore := cutoff(request.AuditDays)
	if auditDisabled {
		eventsBefore = now.Add(-defaultEventRetention)
	}
	return postgresmaintenance.Policy{
		Operations:       postgresmaintenance.OperationPolicy{Before: now, Limit: maintenanceBatchLimit},
		CursorSigning:    postgresmaintenance.CursorSigningPolicy{Limit: maintenanceBatchLimit},
		Jobs:             postgresmaintenance.JobsPolicy{Before: now.Add(-defaultJobRetention), Limit: maintenanceBatchLimit},
		Events:           postgresmaintenance.EventsPolicy{Before: eventsBefore},
		Cache:            postgresmaintenance.CachePolicy{Before: now.Add(-defaultCacheRetention), Limit: maintenanceBatchLimit},
		DashboardSession: postgresmaintenance.DashboardSessionPolicy{Limit: maintenanceBatchLimit},
		DashboardUsage:   postgresmaintenance.DashboardUsagePolicy{Before: now.Add(-dashboardusage.RetentionWindow), Limit: maintenanceBatchLimit},
		DashboardStreams: postgresmaintenance.DashboardPublicationPolicy{Now: now, Limit: maintenanceBatchLimit},
		ManagedData:      postgresmaintenance.ManagedDataPolicy{Before: now.Add(-defaultUploadRetention), Limit: maintenanceBatchLimit},
		AccessAudit: postgresmaintenance.AccessAuditPolicy{
			Short:    retentionWindow(now, request.AuditDays, auditDisabled),
			Standard: retentionWindow(now, request.AuditDays, auditDisabled),
			Security: retentionWindow(now, request.AuditDays, auditDisabled),
		},
		AccessAuthState: retentionWindow(now, request.AuthStateDays, request.AuthStateDays == 0),
		QueryAudit:      retentionWindow(now, request.QueryDays, request.QueryDays == 0),
		AgentHistory:    retentionWindow(now, request.ArchivedAgentDays, request.ArchivedAgentDays == 0),
	}, nil
}

func validateRetentionDays(request adminoffline.MaintenanceRequest) error {
	if request.AuditDays < 0 || request.QueryDays < 0 || request.ArchivedAgentDays < 0 || request.AuthStateDays < 0 {
		return errors.New("retention days must be zero or greater")
	}
	return nil
}

func retentionWindow(now time.Time, days int, disabled bool) postgresmaintenance.RetentionWindow {
	return postgresmaintenance.RetentionWindow{Before: now.Add(-time.Duration(days) * 24 * time.Hour), Limit: maintenanceBatchLimit, Disabled: disabled}
}

// writeEvidence emits stable, payload-free operator evidence. Timestamps,
// SQL, IDs, and audit/query/agent payloads are intentionally excluded.
func writeEvidence(out io.Writer, mode string, result postgresmaintenance.Result) error {
	if out == nil {
		return errors.New("admin maintenance output is required")
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "mode: %s\n", mode)
	fmt.Fprintf(&builder, "operations removed: %d\n", result.OperationsRemoved)
	fmt.Fprintf(&builder, "cursor signing removed: %d\n", result.CursorSigningRemoved)
	fmt.Fprintf(&builder, "jobs removed: %d\n", result.JobsRemoved)
	fmt.Fprintf(&builder, "events removed: %d\n", result.EventsRemoved)
	fmt.Fprintf(&builder, "cache invalidations: %d\n", result.Cache.Invalidations)
	fmt.Fprintf(&builder, "cache expired leases: %d\n", result.Cache.ExpiredLeases)
	fmt.Fprintf(&builder, "dashboard sessions removed: %d\n", result.DashboardSessionsRemoved)
	fmt.Fprintf(&builder, "dashboard usage removed: %d\n", result.DashboardUsageRemoved)
	fmt.Fprintf(&builder, "dashboard publication batch complete: %t\n", result.DashboardPublicationBatchDone)
	fmt.Fprintf(&builder, "managed-data upload sessions removed: %d\n", result.ManagedDataUploadSessionsRemoved)
	fmt.Fprintf(&builder, "access audit short removed: %d\n", result.AccessAuditShort.RemovedCount)
	fmt.Fprintf(&builder, "access audit standard removed: %d\n", result.AccessAuditStandard.RemovedCount)
	fmt.Fprintf(&builder, "access audit security removed: %d\n", result.AccessAuditSecurity.RemovedCount)
	fmt.Fprintf(&builder, "access auth state removed: %d\n", authStateRemoved(result.AccessAuthState))
	fmt.Fprintf(&builder, "query audit removed: %d\n", result.QueryAudit.Removed)
	fmt.Fprintf(&builder, "agent conversations removed: %d\n", result.AgentHistory.ConversationsDeleted)
	fmt.Fprintf(&builder, "agent messages removed: %d\n", result.AgentHistory.MessagesDeleted)
	fmt.Fprintf(&builder, "agent runs removed: %d\n", result.AgentHistory.RunsDeleted)
	fmt.Fprintf(&builder, "agent run events removed: %d\n", result.AgentHistory.RunEventsDeleted)
	contents := builder.String()
	written, err := io.WriteString(out, contents)
	if err == nil && written != len(contents) {
		return io.ErrShortWrite
	}
	return err
}

func authStateRemoved(result accesspostgres.AuthRetentionResult) int64 {
	return result.SessionsDeleted + result.OAuthSessionsDeleted + result.OAuthAssertionsDeleted + result.DesktopCodesDeleted + result.DeviceAuthorizationsDeleted + result.APITokensDeleted + result.ServiceSecretsDeleted + result.AuthoringSessionsDeleted + result.AuthoringCredentialsDeleted
}
