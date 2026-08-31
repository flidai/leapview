package ducklake

// PostgreSQL-backed DuckLake physical maintenance.
//
// Runtime environments deliberately cannot invoke DuckLake's file-deleting
// functions when they are attached to a shared physical pool.  This file is
// the lower-level operation boundary for the separately provisioned catalog
// maintenance connection.  Composition owns opening that connection and
// acquiring the control-plane lease/fence; this package validates the
// resulting contract again immediately before every destructive statement.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

var (
	// ErrPostgresCatalogMaintenance is returned for malformed or incomplete
	// catalog maintenance contracts.  Callers must stop rather than infer a
	// catalog, pool, role, or lease from ambient runtime state.
	ErrPostgresCatalogMaintenance = errors.New("PostgreSQL DuckLake physical maintenance contract is invalid")
	// ErrPostgresCatalogMaintenanceRole means the supplied credential is not a
	// dedicated catalog maintenance role.  Runtime and migrator credentials
	// are intentionally not accepted by this operation boundary.
	ErrPostgresCatalogMaintenanceRole = errors.New("PostgreSQL DuckLake physical maintenance requires a dedicated role")
	// ErrPostgresCatalogMaintenanceLease means the lease or fence is missing,
	// expired, or internally inconsistent.
	ErrPostgresCatalogMaintenanceLease = errors.New("PostgreSQL DuckLake physical maintenance lease/fence is invalid")
	// ErrPostgresCatalogMaintenanceConnection means a runtime pool (database/sql
	// DB) was supplied instead of one explicitly leased catalog session.
	ErrPostgresCatalogMaintenanceConnection = errors.New("PostgreSQL DuckLake physical maintenance requires a dedicated catalog connection")
)

const (
	defaultDuckLakeRuntimeRole  = "leapview_ducklake_runtime"
	defaultDuckLakeMigratorRole = "leapview_ducklake_migrator"
)

// CatalogMaintenanceConnection is intentionally narrower than a database
// pool.  Production supplies one *sql.Conn (or transaction-owned equivalent)
// whose DuckLake attachment was created with the dedicated maintenance
// PostgreSQL secret.  ExecContext preserves cancellation through DuckDB and
// its PostgreSQL metadata connection.
type CatalogMaintenanceConnection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// PostgresCatalogMaintenanceLease is the control-plane lease held by the
// maintenance worker.  LeaseID is opaque evidence; OwnerID and FencingEpoch
// must match the active fence below.
type PostgresCatalogMaintenanceLease struct {
	LeaseID   string
	OwnerID   string
	ExpiresAt time.Time
}

// PostgresCatalogMaintenanceFence is the pool fence observed when the lease
// was acquired.  Verify, when supplied, must perform a live CAS/lease check
// against the control-plane authority.  The callback is invoked before every
// native mutation, so a lost fence stops the sequence before the next call.
type PostgresCatalogMaintenanceFence struct {
	OwnerID        string
	FencingEpoch   int64
	LeaseExpiresAt time.Time
	Verify         func(context.Context) error
}

// PostgresCatalogMaintenanceContract identifies one and only one PostgreSQL
// DuckLake catalog and its dedicated maintenance authority.  CatalogAlias is
// required even though the current runtime convention is "lake"; leaving it
// blank would permit an unqualified or ambient catalog call.  RuntimePool and
// SharedRuntimePool are admission evidence used to reject the ordinary
// shared runtime path before any SQL is sent.
type PostgresCatalogMaintenanceContract struct {
	// Catalog is the exact attach contract used by composition.  The flattened
	// fields below are retained to make the operation boundary easy to inspect
	// and to avoid requiring callers to reconstruct identity from SQL.
	Catalog PostgresCatalogConfig

	CatalogAlias   string
	CatalogID      string
	PhysicalPoolID string
	MetadataSchema string
	DataPath       string

	MaintenanceRole string
	RuntimeRole     string

	SharedRuntimePool bool
	RuntimePool       *Environment

	Lease PostgresCatalogMaintenanceLease
	Fence PostgresCatalogMaintenanceFence
}

// PostgresCatalogMaintenanceRequest controls one bounded native maintenance
// sequence.  SnapshotIDs are explicit: age-based defaults are intentionally
// unavailable because retention roots live in a separate PostgreSQL control
// database.  FileGrace is also explicit so cleanup cannot silently become
// cleanup_all.  An empty SnapshotIDs list skips expiry while still allowing
// the two physical-file phases.
type PostgresCatalogMaintenanceRequest struct {
	SnapshotIDs []int64
	// Versions mirrors DuckLake's `versions => [...]` terminology. Supplying
	// both fields is allowed only when they normalize to the same IDs.
	Versions  []int64
	FileGrace time.Duration
	DryRun    bool
}

// PostgresCatalogMaintenanceResult reports which phases completed.  A phase
// is true only after its ExecContext call returned nil; errors stop the
// sequence and never claim later phases ran.
type PostgresCatalogMaintenanceResult struct {
	CatalogID      string
	PhysicalPoolID string
	SnapshotIDs    []int64
	FileGrace      time.Duration
	DryRun         bool
	Snapshots      bool
	OldFiles       bool
	Orphans        bool
}

// PostgresCatalogMaintenance owns one explicitly supplied catalog session.
// It never opens a PostgreSQL URL, creates a DuckDB pool, or invokes the
// legacy SQLite migration path.
type PostgresCatalogMaintenance struct {
	conn     CatalogMaintenanceConnection
	contract PostgresCatalogMaintenanceContract
	gate     chan struct{}
}

// NewPostgresCatalogMaintenance validates the immutable operation contract
// and retains the caller-owned connection without taking ownership of it.
// A *sql.DB is rejected because it is a pool and cannot prove that the
// DuckLake attachment is present on the session receiving a statement.
func NewPostgresCatalogMaintenance(conn CatalogMaintenanceConnection, contract PostgresCatalogMaintenanceContract) (*PostgresCatalogMaintenance, error) {
	if nilMaintenanceValue(conn) {
		return nil, fmt.Errorf("%w: connection is required", ErrPostgresCatalogMaintenanceConnection)
	}
	if _, shared := conn.(*sql.DB); shared {
		return nil, fmt.Errorf("%w: runtime database pool supplied", ErrPostgresCatalogMaintenanceConnection)
	}
	if err := contract.validate(); err != nil {
		return nil, err
	}
	return &PostgresCatalogMaintenance{conn: conn, contract: contract, gate: make(chan struct{}, 1)}, nil
}

// Contract returns a value copy of the validated identity and fencing
// contract.  It exposes no SQL or credential handle.
func (m *PostgresCatalogMaintenance) Contract() PostgresCatalogMaintenanceContract {
	if m == nil {
		return PostgresCatalogMaintenanceContract{}
	}
	return m.contract
}

// Run executes explicit snapshot expiry followed by scheduled-file cleanup
// and orphan cleanup.  The dedicated connection is serialized and the lease
// fence is checked before each phase.  Context cancellation is checked before
// admission, before each call, and delegated to the SQL driver's context.
func (m *PostgresCatalogMaintenance) Run(ctx context.Context, request PostgresCatalogMaintenanceRequest) (PostgresCatalogMaintenanceResult, error) {
	if m == nil || nilMaintenanceValue(m.conn) {
		return PostgresCatalogMaintenanceResult{}, fmt.Errorf("%w: executor is unavailable", ErrPostgresCatalogMaintenanceConnection)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return PostgresCatalogMaintenanceResult{}, err
	}
	if err := request.validate(); err != nil {
		return PostgresCatalogMaintenanceResult{}, err
	}
	if err := m.acquire(ctx); err != nil {
		return PostgresCatalogMaintenanceResult{}, err
	}
	defer m.release()

	snapshotIDs := request.snapshotIDs()
	result := PostgresCatalogMaintenanceResult{
		CatalogID: m.contract.CatalogID, PhysicalPoolID: m.contract.PhysicalPoolID,
		SnapshotIDs: append([]int64(nil), snapshotIDs...),
		FileGrace:   request.FileGrace, DryRun: request.DryRun,
	}
	if len(snapshotIDs) > 0 {
		if err := m.beforePhase(ctx); err != nil {
			return result, err
		}
		statement := fmt.Sprintf("CALL ducklake_expire_snapshots(%s, versions => %s, dry_run => %t)", sqlStringLiteral(m.contract.CatalogAlias), snapshotListLiteral(snapshotIDs), request.DryRun)
		if _, err := m.conn.ExecContext(ctx, statement); err != nil {
			return result, fmt.Errorf("expire DuckLake snapshots: %w", err)
		}
		result.Snapshots = true
	}
	if err := m.beforePhase(ctx); err != nil {
		return result, err
	}
	olderThan := duckLakeOlderThan(request.FileGrace)
	statement := fmt.Sprintf("CALL ducklake_cleanup_old_files(%s, older_than => %s, dry_run => %t)", sqlStringLiteral(m.contract.CatalogAlias), olderThan, request.DryRun)
	if _, err := m.conn.ExecContext(ctx, statement); err != nil {
		return result, fmt.Errorf("cleanup DuckLake old files: %w", err)
	}
	result.OldFiles = true

	if err := m.beforePhase(ctx); err != nil {
		return result, err
	}
	statement = fmt.Sprintf("CALL ducklake_delete_orphaned_files(%s, older_than => %s, dry_run => %t)", sqlStringLiteral(m.contract.CatalogAlias), olderThan, request.DryRun)
	if _, err := m.conn.ExecContext(ctx, statement); err != nil {
		return result, fmt.Errorf("delete DuckLake orphaned files: %w", err)
	}
	result.Orphans = true
	return result, nil
}

// ExpireSnapshots runs only the explicit expiry phase.  It is useful when a
// scheduler persists phase progress separately; the same lease/fence and
// connection checks apply.
func (m *PostgresCatalogMaintenance) ExpireSnapshots(ctx context.Context, snapshotIDs []int64, dryRun bool) error {
	request := PostgresCatalogMaintenanceRequest{SnapshotIDs: snapshotIDs, FileGrace: time.Nanosecond, DryRun: dryRun}
	if err := request.validateSnapshotsOnly(); err != nil {
		return err
	}
	if len(snapshotIDs) == 0 {
		return nil
	}
	if err := m.runPhase(ctx, func(ctx context.Context) error {
		_, err := m.conn.ExecContext(ctx, fmt.Sprintf("CALL ducklake_expire_snapshots(%s, versions => %s, dry_run => %t)", sqlStringLiteral(m.contract.CatalogAlias), snapshotListLiteral(normalizeSnapshotIDs(snapshotIDs)), dryRun))
		return err
	}); err != nil {
		return fmt.Errorf("expire DuckLake snapshots: %w", err)
	}
	return nil
}

// CleanupOldFiles runs scheduled-file cleanup with an explicit grace period.
func (m *PostgresCatalogMaintenance) CleanupOldFiles(ctx context.Context, fileGrace time.Duration, dryRun bool) error {
	return m.runFilePhase(ctx, "ducklake_cleanup_old_files", fileGrace, dryRun)
}

// DeleteOrphanedFiles runs orphan cleanup with an explicit grace period.
func (m *PostgresCatalogMaintenance) DeleteOrphanedFiles(ctx context.Context, fileGrace time.Duration, dryRun bool) error {
	return m.runFilePhase(ctx, "ducklake_delete_orphaned_files", fileGrace, dryRun)
}

func (m *PostgresCatalogMaintenance) runFilePhase(ctx context.Context, function string, fileGrace time.Duration, dryRun bool) error {
	if m == nil || nilMaintenanceValue(m.conn) {
		return fmt.Errorf("%w: executor is unavailable", ErrPostgresCatalogMaintenanceConnection)
	}
	if fileGrace < time.Microsecond {
		return fmt.Errorf("%w: file grace must be at least one microsecond", ErrPostgresCatalogMaintenance)
	}
	if err := m.runPhase(ctx, func(ctx context.Context) error {
		statement := fmt.Sprintf("CALL %s(%s, older_than => %s, dry_run => %t)", function, sqlStringLiteral(m.contract.CatalogAlias), duckLakeOlderThan(fileGrace), dryRun)
		_, err := m.conn.ExecContext(ctx, statement)
		return err
	}); err != nil {
		return fmt.Errorf("%s: %w", function, err)
	}
	return nil
}

func (m *PostgresCatalogMaintenance) runPhase(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.acquire(ctx); err != nil {
		return err
	}
	defer m.release()
	if err := m.beforePhase(ctx); err != nil {
		return err
	}
	if err := fn(ctx); err != nil {
		return err
	}
	return nil
}

func (m *PostgresCatalogMaintenance) beforePhase(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.contract.validateLease(time.Now()); err != nil {
		return err
	}
	if verify := m.contract.Fence.Verify; verify != nil {
		if err := verify(ctx); err != nil {
			return fmt.Errorf("verify PostgreSQL DuckLake maintenance fence: %w", err)
		}
	}
	return ctx.Err()
}

func (m *PostgresCatalogMaintenance) acquire(ctx context.Context) error {
	select {
	case m.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *PostgresCatalogMaintenance) release() {
	select {
	case <-m.gate:
	default:
	}
}

func (c PostgresCatalogMaintenanceContract) validate() error {
	if strings.TrimSpace(c.CatalogAlias) != catalogAlias {
		return fmt.Errorf("%w: catalog alias must be explicitly %q", ErrPostgresCatalogMaintenance, catalogAlias)
	}
	if !maintenanceID(c.CatalogID) || !maintenanceID(c.PhysicalPoolID) {
		return fmt.Errorf("%w: catalog and physical-pool identities are required", ErrPostgresCatalogMaintenance)
	}
	if err := validateCatalogIdentifier("metadata schema", c.MetadataSchema); err != nil {
		return fmt.Errorf("%w: %v", ErrPostgresCatalogMaintenance, err)
	}
	if c.MetadataSchema != MetadataSchemaForPool(c.PhysicalPoolID) {
		return fmt.Errorf("%w: metadata schema is not qualified for physical pool", ErrPostgresCatalogMaintenance)
	}
	if _, err := CanonicalDataPath(c.DataPath); err != nil {
		return fmt.Errorf("%w: data path: %v", ErrPostgresCatalogMaintenance, err)
	}
	if c.RuntimePool != nil && c.RuntimePool.SharedPool() {
		return fmt.Errorf("%w: shared runtime pool supplied", ErrSharedPoolMaintenance)
	}
	if c.SharedRuntimePool {
		return fmt.Errorf("%w: shared runtime pool supplied", ErrSharedPoolMaintenance)
	}
	if err := validateMaintenanceRole(c.MaintenanceRole, c.RuntimeRole); err != nil {
		return err
	}
	if err := c.validateLease(time.Now()); err != nil {
		return err
	}
	if c.Catalog == (PostgresCatalogConfig{}) {
		return fmt.Errorf("%w: PostgreSQL catalog attach contract is required", ErrPostgresCatalogMaintenance)
	}
	if err := c.Catalog.Validate(); err != nil {
		return fmt.Errorf("%w: catalog attach contract: %v", ErrPostgresCatalogMaintenance, err)
	}
	if c.Catalog.Mode != PostgresCatalogWriter || c.Catalog.PhysicalPoolID != c.PhysicalPoolID || c.Catalog.MetadataSchema != c.MetadataSchema {
		return fmt.Errorf("%w: catalog attach contract does not match maintenance identity", ErrPostgresCatalogMaintenance)
	}
	return nil
}

func (c PostgresCatalogMaintenanceContract) validateLease(now time.Time) error {
	if !maintenanceID(c.Lease.LeaseID) || !maintenanceID(c.Lease.OwnerID) || !maintenanceID(c.Fence.OwnerID) || c.Lease.OwnerID != c.Fence.OwnerID || c.Fence.FencingEpoch <= 0 || c.Lease.ExpiresAt.IsZero() || c.Fence.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: owner, epoch, and lease expiry are required", ErrPostgresCatalogMaintenanceLease)
	}
	leaseExpiry := c.Lease.ExpiresAt.UTC()
	fenceExpiry := c.Fence.LeaseExpiresAt.UTC()
	if !leaseExpiry.After(now.UTC()) || !fenceExpiry.After(now.UTC()) || !leaseExpiry.Equal(fenceExpiry) {
		return fmt.Errorf("%w: lease/fence is expired or has mismatched expiry", ErrPostgresCatalogMaintenanceLease)
	}
	return nil
}

func validateMaintenanceRole(role, runtimeRole string) error {
	role = strings.TrimSpace(role)
	if !maintenanceID(role) {
		return fmt.Errorf("%w: role is required", ErrPostgresCatalogMaintenanceRole)
	}
	if runtimeRole == "" {
		runtimeRole = defaultDuckLakeRuntimeRole
	}
	if role == runtimeRole || role == defaultDuckLakeRuntimeRole || role == defaultDuckLakeMigratorRole {
		return fmt.Errorf("%w: role %q is not dedicated", ErrPostgresCatalogMaintenanceRole, role)
	}
	return nil
}

func (r PostgresCatalogMaintenanceRequest) validate() error {
	if err := r.validateSnapshotsOnly(); err != nil {
		return err
	}
	if r.FileGrace < time.Microsecond {
		return fmt.Errorf("%w: file grace must be at least one microsecond", ErrPostgresCatalogMaintenance)
	}
	return nil
}

func (r PostgresCatalogMaintenanceRequest) validateSnapshotsOnly() error {
	for _, id := range append(append([]int64(nil), r.SnapshotIDs...), r.Versions...) {
		if id <= 0 {
			return fmt.Errorf("%w: snapshot IDs must be positive", ErrPostgresCatalogMaintenance)
		}
	}
	if len(r.SnapshotIDs) > 0 && len(r.Versions) > 0 {
		a, b := normalizeSnapshotIDs(r.SnapshotIDs), normalizeSnapshotIDs(r.Versions)
		if len(a) != len(b) {
			return fmt.Errorf("%w: SnapshotIDs and Versions differ", ErrPostgresCatalogMaintenance)
		}
		for i := range a {
			if a[i] != b[i] {
				return fmt.Errorf("%w: SnapshotIDs and Versions differ", ErrPostgresCatalogMaintenance)
			}
		}
	}
	return nil
}

func (r PostgresCatalogMaintenanceRequest) snapshotIDs() []int64 {
	if len(r.SnapshotIDs) != 0 {
		return normalizeSnapshotIDs(r.SnapshotIDs)
	}
	return normalizeSnapshotIDs(r.Versions)
}

func normalizeSnapshotIDs(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}
	copyValues := append([]int64(nil), values...)
	sortInt64s(copyValues)
	result := copyValues[:0]
	for _, value := range copyValues {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func sortInt64s(values []int64) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func duckLakeOlderThan(grace time.Duration) string {
	return fmt.Sprintf("now() - INTERVAL '%d microseconds'", grace.Microseconds())
}

func maintenanceID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 255 || value != strings.TrimSpace(value) {
		return false
	}
	for i, r := range value {
		if i == 0 {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
			continue
		}
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func nilMaintenanceValue(value any) bool {
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
