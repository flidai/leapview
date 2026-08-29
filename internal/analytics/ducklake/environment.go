package ducklake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/extension"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/workload"
)

const catalogAlias = "lake"
const catalogFileMode = securefs.PrivateFileMode

var catalogWriteLocks sync.Map

// CredentialBootstrap lets a deployment target provision ephemeral connector
// credentials without exposing secret values to the DuckLake runtime config.
// It is called once for every pooled connector before that connector attaches
// its DuckLake catalog.
type CredentialBootstrap func(context.Context, driver.ExecerContext) error

type Config struct {
	RootDir     string
	CatalogPath string
	DataPath    string
	// PostgresCatalog selects the target-owned PostgreSQL metadata catalog
	// path. When set, CatalogPath is never opened or written; DuckDB attaches
	// the catalog through a separately provisioned DuckLake/PostgreSQL secret.
	// The legacy file-backed path remains the default when this is nil.
	PostgresCatalog *PostgresCatalogConfig
	// CommitMarker is durable attempt identity for a PostgreSQL writer. It is
	// written as DuckLake commit_extra_info inside every commit transaction and
	// used for exact lost-ACK reconciliation. File-backed callers retain the
	// legacy serving-state commit metadata when this is nil.
	CommitMarker *CommitMarker
	// PhysicalPoolID marks this catalog as a member of a shared LeapView
	// physical pool. Shared-pool catalogs may not invoke catalog-local cleanup.
	PhysicalPoolID string
	// SharedPool is retained for callers which have a pool identity in another
	// control-plane record. PhysicalPoolID implies SharedPool when non-empty,
	// but either shared-pool request requires PoolContract admission evidence.
	SharedPool    bool
	Compatibility CompatibilityTuple
	PoolContract  *PoolContract
	// ReadOnly attaches an existing DuckLake catalog with READ_ONLY and
	// CREATE_IF_NOT_EXISTS false. Read-only environments reject all writes at
	// the runtime boundary before reaching DuckDB.
	ReadOnly bool
	// CredentialBootstrap is invoked for every pooled connector after
	// persistent secrets are disabled and before DuckLake ATTACH. The callback
	// receives only the target-owned executor capability; credentials never
	// enter Config or runtime diagnostics.
	CredentialBootstrap CredentialBootstrap
	MaxConnections      int
	MemoryMaxBytes      int64
	TempMaxBytes        int64
	MaxThreads          int
	TempDir             string
	// ExtensionAdmission supplies exact, preverified absolute artifacts. When
	// present, DuckLake never invokes DuckDB INSTALL or automatic acquisition.
	ExtensionAdmission extension.Admission
}

type Layout struct {
	RootDir     string
	CatalogPath string
	DataPath    string
}

type Environment struct {
	db               *sql.DB
	connector        driver.Connector
	layout           Layout
	catalogIdentity  string
	catalogLock      string
	postgresCatalog  bool
	postgresMetadata string
	postgresSnapshot int64
	commitMarker     *CommitMarker
	// commitMarkerUsed is an atomic one-shot gate for marker-mode writers. A
	// marker identifies exactly one build attempt and therefore may qualify
	// at most one DuckLake transaction/snapshot from this environment. The
	// gate is consumed before opening a connection or invoking materialization
	// so callers cannot replay a marker after an indeterminate or failed call.
	commitMarkerUsed   atomic.Bool
	physicalPoolID     string
	sharedPool         bool
	compatibility      CompatibilityTuple
	extensionAdmission extension.Admission
	readOnly           bool
	readConcurrency    int
	extensionMu        sync.Mutex
	extensions         map[string]*extensionLoad
	fatalMu            sync.RWMutex
	fatalErr           error
	fatalOnce          sync.Once
	fatal              chan struct{}
	telemetryMu        sync.RWMutex
	sourceTotals       map[string]map[string]uint64
	scopeContention    map[string]uint64
	acquisitions       atomic.Uint64
	extensionOK        atomic.Uint64
	extensionFailed    atomic.Uint64
	commitRetries      atomic.Uint64
	cleanupOK          atomic.Uint64
	cleanupFailed      atomic.Uint64
	closed             atomic.Bool
	closeOnce          sync.Once
	closeErr           error
}

// borrowedConnector deliberately exposes only driver.Connector. Some concrete
// connectors (including DuckDB's) also implement io.Closer, and database/sql
// closes such a connector when its DB handle closes. A short-lived DB used for
// commit reconciliation must close its physical session without closing the
// environment-owned connector and the shared in-memory DuckDB instance.
type borrowedConnector struct{ inner driver.Connector }

func (c borrowedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	return c.inner.Connect(ctx)
}

func (c borrowedConnector) Driver() driver.Driver { return c.inner.Driver() }

type extensionLoad struct {
	done chan struct{}
	err  error
}

var approvedExtensions = func() map[string]struct{} {
	result := make(map[string]struct{})
	for _, name := range projectcontracts.RequiredExtensionNames() {
		result[name] = struct{}{}
	}
	return result
}()

var (
	ErrUnadmitted          = errors.New("DuckDB access requires workload admission")
	ErrConflictingLease    = errors.New("a different DuckDB environment is already leased")
	ErrEnvironmentClosed   = errors.New("ducklake environment is closed")
	ErrReadOnlyEnvironment = errors.New("ducklake environment is read-only")
	// ErrCommitMarkerAlreadyUsed indicates that a marker-mode environment has
	// already attempted its single qualified DuckLake commit. Callers must
	// create a fresh environment/marker rather than replaying materialization.
	ErrCommitMarkerAlreadyUsed = errors.New("DuckLake commit marker already used")
	// ErrCommitReconciliationRequired means the database reported an
	// indeterminate transaction outcome and no positive exact-marker evidence
	// was available. Marker-mode writers must stop rather than replaying
	// materialization and risking a duplicate snapshot.
	ErrCommitReconciliationRequired = errors.New("DuckLake commit reconciliation required")
)

type TransientCommitError struct{ Err error }

func (e *TransientCommitError) Error() string { return e.Err.Error() }
func (e *TransientCommitError) Unwrap() error { return e.Err }

// Lease pins one physical DuckDB client connection for a complete logical
// operation. The connection itself remains private to this package and is
// propagated through Context so analytical adapters cannot accidentally open
// another client in the middle of an operation.
type Lease = analyticsresource.Lease

type leaseContextKey struct{}

type leaseState struct {
	mu   sync.Mutex
	env  *Environment
	conn *sql.Conn
	refs int
}

type connectionLease struct {
	ctx   context.Context
	state *leaseState
	once  sync.Once
}

func (l *connectionLease) Context() context.Context { return l.ctx }

func (l *connectionLease) Release() {
	if l == nil || l.state == nil {
		return
	}
	l.once.Do(func() {
		l.state.mu.Lock()
		l.state.refs--
		last := l.state.refs == 0
		conn := l.state.conn
		if last {
			l.state.conn = nil
		}
		l.state.mu.Unlock()
		if last && conn != nil {
			_ = conn.Close()
		}
	})
}

// Acquire pins a DuckDB connection after the workload controller has admitted
// the operation. Nested acquisition reuses the same connection and is
// reference counted so release order cannot invalidate an active child call.
func (e *Environment) Acquire(ctx context.Context) (Lease, error) {
	if e == nil || e.db == nil {
		return nil, fmt.Errorf("ducklake environment is not initialized")
	}
	if e.closed.Load() {
		return nil, ErrEnvironmentClosed
	}
	if healthErr := e.Healthy(); healthErr != nil {
		return nil, fmt.Errorf("analytical environment is fatally unhealthy: %w", healthErr)
	}
	if _, _, ok := workload.Current(ctx); !ok {
		return nil, ErrUnadmitted
	}
	if current, ok := ctx.Value(leaseContextKey{}).(*leaseState); ok && current != nil {
		if current.env != e {
			return nil, ErrConflictingLease
		}
		current.mu.Lock()
		if current.conn == nil || current.refs <= 0 {
			current.mu.Unlock()
			return nil, fmt.Errorf("DuckDB lease is already released")
		}
		current.refs++
		current.mu.Unlock()
		return &connectionLease{ctx: ctx, state: current}, nil
	}
	started := time.Now()
	conn, err := e.db.Conn(ctx)
	dataquery.ObserveConnectionWait(ctx, time.Since(started))
	if err != nil {
		return nil, err
	}
	state := &leaseState{env: e, conn: conn, refs: 1}
	e.acquisitions.Add(1)
	leaseCtx := context.WithValue(ctx, leaseContextKey{}, state)
	return &connectionLease{ctx: leaseCtx, state: state}, nil
}

// Session returns the connection pinned by Acquire. Only analytical adapters
// use this capability; product packages never receive database/sql handles.
func (e *Environment) Session(ctx context.Context) (analyticsresource.Session, error) {
	current, ok := ctx.Value(leaseContextKey{}).(*leaseState)
	if !ok || current == nil {
		return nil, ErrUnadmitted
	}
	if current.env != e {
		return nil, ErrConflictingLease
	}
	current.mu.Lock()
	defer current.mu.Unlock()
	if current.conn == nil || current.refs <= 0 {
		return nil, fmt.Errorf("DuckDB lease is already released")
	}
	return current.conn, nil
}

type Snapshot struct {
	ID int64
}

func NewLayout(rootDir string) Layout {
	return Layout{
		RootDir:     rootDir,
		CatalogPath: filepath.Join(rootDir, "catalog.duckdb"),
		DataPath:    filepath.Join(rootDir, "data"),
	}
}

func Open(ctx context.Context, config Config) (*Environment, error) {
	if config.ExtensionAdmission == nil {
		return nil, fmt.Errorf("DuckLake extension admission is required")
	}
	if !nativeArrowEnabled {
		return nil, fmt.Errorf("LeapView analytical runtime requires the duckdb_arrow build tag")
	}
	postgresConfig := config.PostgresCatalog
	postgresMode := postgresConfig != nil
	var commitMarker *CommitMarker
	if config.CommitMarker != nil {
		normalized, err := config.CommitMarker.Normalize()
		if err != nil {
			return nil, fmt.Errorf("DuckLake commit marker: %w", err)
		}
		commitMarker = &normalized
	}
	if postgresMode {
		if strings.TrimSpace(config.CatalogPath) != "" {
			return nil, fmt.Errorf("PostgreSQL DuckLake runtime must not provide a file catalog path")
		}
		copy := *postgresConfig
		postgresConfig = &copy
		if err := postgresConfig.Validate(); err != nil {
			return nil, fmt.Errorf("PostgreSQL DuckLake catalog: %w", err)
		}
		if postgresConfig.PhysicalPoolID == "" || config.PoolContract == nil {
			return nil, fmt.Errorf("PostgreSQL DuckLake runtime requires admitted physical-pool contract")
		}
		if postgresConfig.Mode == PostgresCatalogWriter && commitMarker == nil {
			return nil, fmt.Errorf("PostgreSQL DuckLake writer requires an attempt commit marker")
		}
		if commitMarker != nil && commitMarker.PhysicalPoolID != postgresConfig.PhysicalPoolID {
			return nil, fmt.Errorf("DuckLake commit marker physical pool does not match PostgreSQL catalog")
		}
		if config.ReadOnly && postgresConfig.Mode != PostgresCatalogServing {
			return nil, fmt.Errorf("PostgreSQL DuckLake read-only environment requires serving mode")
		}
		if postgresConfig.Mode == PostgresCatalogServing {
			config.ReadOnly = true
		}
		if config.PhysicalPoolID != "" && config.PhysicalPoolID != postgresConfig.PhysicalPoolID {
			return nil, fmt.Errorf("PostgreSQL DuckLake physical pool ID does not match environment admission")
		}
		config.PhysicalPoolID = postgresConfig.PhysicalPoolID
		if config.PoolContract != nil {
			if err := config.PoolContract.Validate(); err != nil {
				return nil, fmt.Errorf("shared physical-pool admission: %w", err)
			}
			if config.PoolContract.Pool.ID.String() != postgresConfig.PhysicalPoolID {
				return nil, fmt.Errorf("PostgreSQL DuckLake physical pool ID does not match admitted pool")
			}
			config.Compatibility = config.PoolContract.Tuple
		}
	}
	sharedRequested := config.SharedPool || strings.TrimSpace(config.PhysicalPoolID) != "" || config.PoolContract != nil
	if sharedRequested {
		if config.PoolContract == nil {
			return nil, fmt.Errorf("shared physical-pool admission is required before DuckLake attach")
		}
		if err := config.PoolContract.Validate(); err != nil {
			return nil, fmt.Errorf("shared physical-pool admission: %w", err)
		}
		contractID := config.PoolContract.Pool.ID.String()
		if requestedID := strings.TrimSpace(config.PhysicalPoolID); requestedID != "" && requestedID != contractID {
			return nil, fmt.Errorf("shared physical-pool ID %q does not match admitted pool %q", requestedID, contractID)
		}
		if requestedTuple := config.Compatibility; requestedTuple != (CompatibilityTuple{}) && requestedTuple != config.PoolContract.Tuple {
			return nil, fmt.Errorf("shared physical-pool compatibility tuple does not match admitted pool")
		}
		config.PhysicalPoolID = contractID
		config.Compatibility = config.PoolContract.Tuple
		config.SharedPool = true
	}
	var layout Layout
	var err error
	if postgresMode {
		// A PostgreSQL metadata catalog has no serialized catalog file. Keep a
		// private root only for DuckDB's process-local temporary state.
		layout = Layout{RootDir: strings.TrimSpace(config.RootDir), DataPath: postgresConfig.DataPath}
		if layout.RootDir == "" {
			layout.RootDir = "."
		}
	} else {
		layout, err = config.layout()
		if err != nil {
			return nil, err
		}
	}
	if sharedRequested && (!postgresMode || strings.TrimSpace(layout.DataPath) != "") {
		if err := config.PoolContract.ValidateDataPathBinding(layout.DataPath); err != nil {
			return nil, fmt.Errorf("shared physical-pool DATA_PATH binding: %w", err)
		}
	}
	if !postgresMode && config.ReadOnly {
		if err := validateReadOnlyLayout(layout); err != nil {
			return nil, err
		}
	} else if !postgresMode {
		if err := prepareLayout(layout); err != nil {
			return nil, err
		}
	}
	if !postgresMode && !config.ReadOnly {
		migrated, err := migrateLegacySQLiteCatalog(ctx, layout.CatalogPath, config.ExtensionAdmission)
		if err != nil {
			return nil, err
		}
		if migrated {
			slog.InfoContext(ctx, "migrated legacy SQLite-backed DuckLake catalog", "catalog_path", layout.CatalogPath)
		}
	}
	if postgresMode && postgresConfig.Mode == PostgresCatalogInitialize && strings.TrimSpace(postgresConfig.DataPath) == "" {
		return nil, fmt.Errorf("PostgreSQL DuckLake initialization requires DATA_PATH")
	}
	if strings.TrimSpace(config.TempDir) != "" {
		if err := securefs.EnsurePrivateDir(config.TempDir); err != nil {
			return nil, err
		}
	}
	connections := config.MaxConnections
	if connections <= 0 {
		connections = 1
	}
	// Keep both process and attach defaults explicit. DuckLake persisted
	// global/schema/table options take precedence, so they are inspected by
	// DataInliningPolicy before a catalog is sealed.
	var attach string
	var postgresSecretSQL string
	if postgresMode {
		statements, statementsErr := postgresConfig.Statements()
		if statementsErr != nil {
			return nil, statementsErr
		}
		postgresSecretSQL, attach = statements[0], statements[1]
	} else {
		attachOptions := fmt.Sprintf("DATA_PATH '%s', DATA_INLINING_ROW_LIMIT 0", sqlLiteral(layout.DataPath))
		if config.ReadOnly {
			attachOptions += ", READ_ONLY, CREATE_IF_NOT_EXISTS false"
		}
		attach = fmt.Sprintf("ATTACH IF NOT EXISTS 'ducklake:%s' AS %s (%s)", sqlLiteral(layout.CatalogPath), catalogAlias, attachOptions)
	}
	if err != nil {
		return nil, err
	}
	var initializeOnce sync.Once
	var initializeErr error
	admissionCtx := ctx
	if admissionCtx == nil {
		admissionCtx = context.Background()
	}
	connector, err := duckdb.NewConnector(":memory:", func(execer driver.ExecerContext) error {
		initializeOnce.Do(func() {
			statements := []string{"SET allow_persistent_secrets = false", "SET ducklake_default_data_inlining_row_limit = 0"}
			admitted, admissionErr := config.ExtensionAdmission.AdmitExtension(admissionCtx, "ducklake")
			if admissionErr != nil {
				initializeErr = fmt.Errorf("admit ducklake extension: %w", admissionErr)
				return
			}
			if err := validateAdmittedExtension(admitted, "ducklake"); err != nil {
				initializeErr = err
				return
			}
			if config.MemoryMaxBytes > 0 {
				statements = append(statements, fmt.Sprintf("SET memory_limit = '%dB'", config.MemoryMaxBytes))
			}
			if config.TempMaxBytes > 0 {
				statements = append(statements, fmt.Sprintf("SET max_temp_directory_size = '%dB'", config.TempMaxBytes))
			}
			if config.MaxThreads > 0 {
				statements = append(statements, fmt.Sprintf("SET threads = %d", config.MaxThreads))
			}
			if strings.TrimSpace(config.TempDir) != "" {
				statements = append(statements, "SET temp_directory = '"+sqlLiteral(config.TempDir)+"'")
			}
			statements = append(statements,
				"SET autoinstall_known_extensions = false",
				"SET autoload_known_extensions = false",
			)
			statements = append(statements, "LOAD '"+sqlLiteral(admitted.Path)+"'")
			for _, statement := range statements {
				if _, err := execer.ExecContext(context.Background(), statement, nil); err != nil {
					initializeErr = err
					return
				}
			}
		})
		if initializeErr != nil {
			return initializeErr
		}
		if config.CredentialBootstrap != nil {
			if err := config.CredentialBootstrap(context.Background(), execer); err != nil {
				return fmt.Errorf("bootstrap DuckLake connector credentials: %w", err)
			}
		}
		if postgresMode {
			if _, err := execer.ExecContext(context.Background(), postgresSecretSQL, nil); err != nil {
				return fmt.Errorf("create temporary DuckLake PostgreSQL secret: %w", err)
			}
		}
		if _, err := execer.ExecContext(context.Background(), attach, nil); err != nil {
			return err
		}
		for _, statement := range []string{"USE " + catalogAlias} {
			if _, err := execer.ExecContext(context.Background(), statement, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var dbConnector driver.Connector = connector
	if config.SharedPool {
		dbConnector = &guardedConnector{inner: connector, guard: func(statement string) error {
			return rejectSharedPoolStatementText(statement)
		}}
	}
	db := sql.OpenDB(dbConnector)
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	catalogIdentity := layout.CatalogPath
	catalogLock := layout.CatalogPath
	postgresMetadata := ""
	postgresSnapshot := int64(0)
	if postgresMode {
		postgresMetadata = postgresConfig.MetadataSchema
		postgresSnapshot = postgresConfig.SnapshotVersion
		catalogIdentity = "postgres://" + postgresMetadata
		catalogLock = "postgres:" + postgresMetadata
	}
	env := &Environment{
		db: db, connector: dbConnector, layout: layout, catalogIdentity: catalogIdentity, catalogLock: catalogLock,
		postgresCatalog: postgresMode, postgresMetadata: postgresMetadata, postgresSnapshot: postgresSnapshot, commitMarker: commitMarker,
		readConcurrency: connections,
		physicalPoolID:  strings.TrimSpace(config.PhysicalPoolID), sharedPool: config.SharedPool || strings.TrimSpace(config.PhysicalPoolID) != "", compatibility: config.Compatibility, readOnly: config.ReadOnly,
		extensions: map[string]*extensionLoad{"ducklake": {done: closedSignal()}}, extensionAdmission: config.ExtensionAdmission, fatal: make(chan struct{}),
		sourceTotals: map[string]map[string]uint64{}, scopeContention: map[string]uint64{},
	}
	if err := warmConnections(ctx, db, connections); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize DuckDB node instance: %w", err)
	}
	// Configuration locking is GLOBAL_ONLY. Run every target bootstrap and
	// catalog ATTACH while warming the complete pool, then lock the shared
	// database instance exactly once so replacement connectors can still run
	// their target-owned bootstrap callback before attaching.
	if _, err := db.ExecContext(ctx, "SET lock_configuration = true"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("lock DuckDB configuration: %w", err)
	}
	if !postgresMode {
		if err := secureDuckDBCatalogFiles(layout.CatalogPath); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return env, nil
}

func (e *Environment) MarkFatal(err error) {
	if e == nil || err == nil {
		return
	}
	e.fatalMu.Lock()
	if e.fatal == nil {
		e.fatal = make(chan struct{})
	}
	e.fatalErr = errors.Join(e.fatalErr, err)
	e.fatalMu.Unlock()
	e.fatalOnce.Do(func() { close(e.fatal) })
}

func (e *Environment) Healthy() error {
	if e == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	e.fatalMu.RLock()
	defer e.fatalMu.RUnlock()
	return e.fatalErr
}

func (e *Environment) Fatal() <-chan struct{} {
	if e == nil || e.fatal == nil {
		return closedSignal()
	}
	return e.fatal
}

func closedSignal() chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

// EnsureExtension loads only an exact admitted artifact from the target supply.
// Concurrent first use is coalesced across projects.
func (e *Environment) EnsureExtension(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if _, ok := approvedExtensions[name]; !ok {
		return fmt.Errorf("DuckDB extension %q is not approved", name)
	}
	e.extensionMu.Lock()
	if current := e.extensions[name]; current != nil {
		e.extensionMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-current.done:
			return current.err
		}
	}
	load := &extensionLoad{done: make(chan struct{})}
	e.extensions[name] = load
	e.extensionMu.Unlock()

	session, err := e.Session(ctx)
	if err == nil {
		if e.extensionAdmission != nil {
			admitted, admissionErr := e.extensionAdmission.AdmitExtension(ctx, name)
			if admissionErr != nil {
				err = admissionErr
			} else if validationErr := validateAdmittedExtension(admitted, name); validationErr != nil {
				err = validationErr
			} else {
				_, err = session.ExecContext(ctx, "LOAD '"+sqlLiteral(admitted.Path)+"'")
			}
		} else {
			err = fmt.Errorf("DuckLake extension admission is required")
		}
	}
	if err != nil {
		err = fmt.Errorf("initializing approved DuckDB extension %s: %w", name, err)
		e.extensionFailed.Add(1)
	} else {
		e.extensionOK.Add(1)
	}
	e.extensionMu.Lock()
	load.err = err
	close(load.done)
	if err != nil {
		delete(e.extensions, name)
	}
	e.extensionMu.Unlock()
	return err
}

func validateAdmittedExtension(admitted extension.AdmittedExtension, requested string) error {
	if admitted.Name != requested || admitted.Path == "" || !filepath.IsAbs(admitted.Path) || filepath.Clean(admitted.Path) != admitted.Path || !strings.HasSuffix(filepath.Base(admitted.Path), ".duckdb_extension") {
		return fmt.Errorf("admitted extension %q has an invalid immutable path or name", requested)
	}
	if platformdigest.ValidateSHA256Identity(admitted.Digest) != nil {
		return fmt.Errorf("admitted extension %q has an invalid digest", requested)
	}
	return nil
}

// AnalyticalStats is an immutable bounded telemetry snapshot. Connector keys
// come only from LeapView's compiled registry; request and project identity
// never enter metric labels.
type AnalyticalStats struct {
	ConnectionAcquisitions uint64
	ExtensionSuccess       uint64
	ExtensionFailures      uint64
	CommitRetries          uint64
	CleanupSuccess         uint64
	CleanupFailures        uint64
	Fatal                  bool
	SourceTotals           map[string]map[string]uint64
	ScopeContention        map[string]uint64
}

func (e *Environment) AnalyticalStats() AnalyticalStats {
	if e == nil {
		return AnalyticalStats{}
	}
	stats := AnalyticalStats{
		ConnectionAcquisitions: e.acquisitions.Load(), ExtensionSuccess: e.extensionOK.Load(),
		ExtensionFailures: e.extensionFailed.Load(), CommitRetries: e.commitRetries.Load(),
		CleanupSuccess: e.cleanupOK.Load(), CleanupFailures: e.cleanupFailed.Load(),
		Fatal: e.Healthy() != nil, SourceTotals: map[string]map[string]uint64{}, ScopeContention: map[string]uint64{},
	}
	e.telemetryMu.RLock()
	defer e.telemetryMu.RUnlock()
	for connector, outcomes := range e.sourceTotals {
		stats.SourceTotals[connector] = map[string]uint64{}
		for outcome, count := range outcomes {
			stats.SourceTotals[connector][outcome] = count
		}
	}
	for connector, count := range e.scopeContention {
		stats.ScopeContention[connector] = count
	}
	return stats
}

func (e *Environment) ObserveSourceAcquisition(connector, outcome string) {
	if e == nil {
		return
	}
	e.telemetryMu.Lock()
	if e.sourceTotals[connector] == nil {
		e.sourceTotals[connector] = map[string]uint64{}
	}
	e.sourceTotals[connector][outcome]++
	e.telemetryMu.Unlock()
}

func (e *Environment) ObserveSecretScopeContention(connector string) {
	if e == nil {
		return
	}
	e.telemetryMu.Lock()
	e.scopeContention[connector]++
	e.telemetryMu.Unlock()
}

func (e *Environment) ObserveRefreshCleanup(success bool) {
	if e == nil {
		return
	}
	if success {
		e.cleanupOK.Add(1)
	} else {
		e.cleanupFailed.Add(1)
	}
}

func prepareLayout(layout Layout) error {
	for _, dir := range []string{layout.RootDir, filepath.Dir(layout.CatalogPath), layout.DataPath} {
		// DuckLake accepts object-store DATA_PATH values (s3://, gs://, ...).
		// They are provisioned by the connector's target credential bootstrap,
		// not as local private directories.
		if strings.Contains(dir, "://") {
			continue
		}
		if err := securefs.EnsurePrivateDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func validateReadOnlyLayout(layout Layout) error {
	catalogInfo, err := os.Stat(layout.CatalogPath)
	if err != nil || catalogInfo.IsDir() {
		return fmt.Errorf("read-only DuckLake catalog is unavailable")
	}
	if strings.Contains(layout.DataPath, "://") {
		return nil
	}
	dataInfo, err := os.Stat(layout.DataPath)
	if err != nil || !dataInfo.IsDir() {
		return fmt.Errorf("read-only DuckLake data path is unavailable")
	}
	return nil
}

func warmConnections(ctx context.Context, db *sql.DB, count int) error {
	connections := make([]*sql.Conn, 0, count)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range count {
		connection, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		connections = append(connections, connection)
	}
	return nil
}

func (c Config) layout() (Layout, error) {
	root := strings.TrimSpace(c.RootDir)
	if root == "" {
		if c.CatalogPath == "" && c.DataPath == "" {
			return Layout{}, fmt.Errorf("ducklake root dir is required")
		}
		root = filepath.Dir(firstNonEmpty(c.CatalogPath, c.DataPath))
	}
	layout := NewLayout(root)
	if c.CatalogPath != "" {
		layout.CatalogPath = c.CatalogPath
	}
	if c.DataPath != "" {
		layout.DataPath = c.DataPath
	}
	return layout, nil
}

func secureDuckDBCatalogFiles(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	for _, candidate := range []string{path, path + ".wal"} {
		if err := os.Chmod(candidate, catalogFileMode); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

var physicalTablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func QualifiedSnapshotRelation(snapshotID int64, table string) (string, error) {
	if snapshotID <= 0 {
		return "", fmt.Errorf("snapshot id must be positive")
	}
	if !physicalTablePattern.MatchString(table) {
		return "", fmt.Errorf("invalid physical table name %q", table)
	}
	// DuckLake's AT VERSION table syntax cannot be followed directly by an
	// alias. A parenthesized FROM-first subquery preserves normal planner alias
	// handling and is inlined by DuckDB.
	return fmt.Sprintf("(FROM %s.model.%s AT (VERSION => %d))", catalogAlias, table, snapshotID), nil
}

func SnapshotRelation(snapshotID int64, table string) string {
	relation, err := QualifiedSnapshotRelation(snapshotID, table)
	if err != nil {
		panic(err)
	}
	return relation
}

func (e *Environment) Commit(ctx context.Context, servingStateID string, extra map[string]string, fn func(*sql.Tx) error) (int64, error) {
	if e == nil || e.db == nil {
		return 0, fmt.Errorf("ducklake environment is not initialized")
	}
	if e.readOnly {
		return 0, ErrReadOnlyEnvironment
	}
	if fn == nil {
		return 0, fmt.Errorf("commit function is required")
	}
	if e.commitMarker != nil && !e.commitMarkerUsed.CompareAndSwap(false, true) {
		return 0, ErrCommitMarkerAlreadyUsed
	}
	unlock := lockCatalogWrites(e.catalogLockKey())
	defer unlock()
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return 0, err
	}
	released := false
	releaseConn := func() {
		if released {
			return
		}
		released = true
		release()
	}
	defer releaseConn()
	attemptID, err := newCommitAttemptID()
	if err != nil {
		return 0, err
	}
	metadata := make(map[string]string, len(extra)+1)
	for key, value := range extra {
		metadata[key] = value
	}
	metadata["refreshAttemptId"] = attemptID
	backoff := []time.Duration{50 * time.Millisecond, 200 * time.Millisecond}
	for attempt := 0; attempt < 3; attempt++ {
		tx, beginErr := conn.BeginTx(ctx, nil)
		if beginErr == nil {
			var messageErr error
			if e.commitMarker != nil {
				messageErr = SetCommitMarker(ctx, tx, *e.commitMarker)
			} else {
				messageErr = setCommitMessage(ctx, tx, servingStateID, metadata)
			}
			if messageErr != nil {
				beginErr = messageErr
			} else if materializeErr := fn(tx); materializeErr != nil {
				beginErr = materializeErr
			} else {
				commitErr := tx.Commit()
				if commitErr != nil && e.commitMarker != nil {
					// PostgreSQL-backed DuckLake can acknowledge a commit as
					// indeterminate after the server durably wrote the snapshot.
					// Release the transaction's physical session before opening a
					// fresh one for exact-marker reconciliation. This is required for
					// MaxConnections=1 and avoids trusting a broken ACK session.
					releaseConn()
					if resolved, reconcileErr := e.resolveCommittedMarkerFresh(ctx, *e.commitMarker); reconcileErr == nil {
						return resolved, nil
					} else {
						return 0, errors.Join(ErrCommitReconciliationRequired, commitErr, reconcileErr)
					}
				}
				beginErr = commitErr
			}
			if beginErr != nil {
				_ = tx.Rollback()
			}
		}
		if beginErr == nil {
			if e.commitMarker != nil {
				// A successful ACK still needs persistent marker evidence. Use a
				// fresh session after returning the transaction connection so a
				// stale connection-local pointer cannot qualify the build.
				releaseConn()
				resolved, reconcileErr := e.resolveCommittedMarkerFresh(ctx, *e.commitMarker)
				if reconcileErr != nil {
					return 0, errors.Join(ErrCommitReconciliationRequired, reconcileErr)
				}
				return resolved, nil
			}
			return committedSnapshotForAttempt(ctx, conn, attemptID)
		}
		beginErr = classifyCommitError(beginErr)
		if attempt == 2 || !retryableCommitError(beginErr) {
			return 0, beginErr
		}
		e.commitRetries.Add(1)
		timer := time.NewTimer(backoff[attempt])
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	return 0, fmt.Errorf("DuckLake commit retry exhausted")
}

// resolveCommittedMarkerFresh opens a new physical DuckDB session for marker
// reconciliation. The transaction session may be poisoned by an indeterminate
// Commit ACK; never reuse it for identity evidence.
func (e *Environment) resolveCommittedMarkerFresh(ctx context.Context, marker CommitMarker) (int64, error) {
	if e == nil || e.db == nil {
		return 0, fmt.Errorf("ducklake environment is not initialized")
	}
	// Open a short-lived DB handle from the original connector instead of
	// borrowing e.db's pool. This guarantees a new physical DuckDB session
	// even when e.db is configured with MaxConnections=1, while the caller has
	// already released the transaction connection.
	lookupDB := e.db
	closeLookupDB := func() {}
	if e.connector != nil {
		lookupDB = sql.OpenDB(borrowedConnector{inner: e.connector})
		lookupDB.SetMaxOpenConns(1)
		lookupDB.SetMaxIdleConns(0)
		closeLookupDB = func() { _ = lookupDB.Close() }
	}
	defer closeLookupDB()
	conn, err := lookupDB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return ResolveCommittedSnapshot(ctx, conn, marker)
}

// CommitTransaction exposes analytical publication through the narrow
// cross-capability transaction contract.
func (e *Environment) CommitTransaction(ctx context.Context, servingStateID string, extra map[string]string, fn func(transaction.Transaction) error) (int64, error) {
	if fn == nil {
		return 0, fmt.Errorf("commit function is required")
	}
	return e.Commit(ctx, servingStateID, extra, func(tx *sql.Tx) error {
		return fn(tx)
	})
}

func newCommitAttemptID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create refresh commit identity: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func committedSnapshotForAttempt(ctx context.Context, queryer SnapshotLookup, attemptID string) (int64, error) {
	if queryer == nil {
		return 0, errors.New("DuckLake snapshot lookup is nil")
	}
	pattern := "%\"refreshAttemptId\":\"" + attemptID + "\"%"
	// The connection-local value is useful immediate evidence, but it must be
	// checked against persistent commit metadata before being accepted.
	if ctx == nil {
		ctx = context.Background()
	}
	var last sql.NullInt64
	lastErr := queryer.QueryRowContext(ctx, "SELECT id FROM "+catalogAlias+".last_committed_snapshot()").Scan(&last)
	if lastErr != nil && !errors.Is(lastErr, sql.ErrNoRows) {
		return 0, fmt.Errorf("read DuckLake last committed snapshot: %w", lastErr)
	}
	if lastErr == nil && last.Valid && last.Int64 > 0 {
		var extra string
		verifyErr := queryer.QueryRowContext(ctx, "SELECT CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE snapshot_id = ?", last.Int64).Scan(&extra)
		if verifyErr != nil && !errors.Is(verifyErr, sql.ErrNoRows) {
			return 0, fmt.Errorf("verify DuckLake last committed snapshot: %w", verifyErr)
		}
		if verifyErr == nil && strings.Contains(extra, `"refreshAttemptId":"`+attemptID+`"`) {
			return last.Int64, nil
		}
	}
	// Restart reconciliation searches persistent markers without ordering by
	// snapshot recency. A duplicate marker is ambiguous and fails closed.
	rows, err := queryer.QueryContext(ctx, "SELECT snapshot_id, CAST(commit_extra_info AS VARCHAR) FROM "+catalogAlias+".snapshots() WHERE CAST(commit_extra_info AS VARCHAR) LIKE ?", pattern)
	if err != nil {
		return 0, fmt.Errorf("find DuckLake committed snapshot identity: %w", err)
	}
	defer rows.Close()
	var snapshot int64
	count := 0
	for rows.Next() {
		var id int64
		var extra string
		if err := rows.Scan(&id, &extra); err != nil {
			return 0, err
		}
		if strings.Contains(extra, `"refreshAttemptId":"`+attemptID+`"`) {
			snapshot = id
			count++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	switch count {
	case 0:
		return 0, fmt.Errorf("DuckLake committed snapshot identity was not found")
	case 1:
		if snapshot <= 0 {
			return 0, fmt.Errorf("DuckLake committed snapshot identity is invalid")
		}
		return snapshot, nil
	default:
		return 0, fmt.Errorf("multiple DuckLake snapshots match commit attempt %q", attemptID)
	}
}

func classifyCommitError(err error) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "transaction conflict") || strings.Contains(text, "conflict on") || strings.Contains(text, "database is locked") || strings.Contains(text, "database busy") {
		return &TransientCommitError{Err: err}
	}
	return err
}

func retryableCommitError(err error) bool {
	var transient *TransientCommitError
	return errors.As(err, &transient)
}

func (e *Environment) Snapshots(ctx context.Context) ([]Snapshot, error) {
	rows, err := e.db.QueryContext(ctx, "SELECT snapshot_id FROM "+catalogAlias+".snapshots() ORDER BY snapshot_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []Snapshot
	for rows.Next() {
		var snapshot Snapshot
		if err := rows.Scan(&snapshot.ID); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, rows.Err()
}

// SnapshotIDs exposes the narrow storage-retention view of the catalog.
func (e *Environment) SnapshotIDs(ctx context.Context) ([]int64, error) {
	snapshots, err := e.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(snapshots))
	for _, snapshot := range snapshots {
		ids = append(ids, snapshot.ID)
	}
	return ids, nil
}

func (e *Environment) ValidateSnapshot(ctx context.Context, snapshotID int64) error {
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	if snapshotID <= 0 {
		return fmt.Errorf("snapshot id must be positive")
	}
	if e.postgresSnapshot > 0 && snapshotID != e.postgresSnapshot {
		return fmt.Errorf("DuckLake snapshot %d is not the attached SNAPSHOT_VERSION %d", snapshotID, e.postgresSnapshot)
	}
	var present int
	err := e.db.QueryRowContext(ctx, "SELECT 1 FROM "+catalogAlias+".snapshots() WHERE snapshot_id = ?", snapshotID).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("DuckLake snapshot %d does not exist", snapshotID)
	}
	return err
}

func (e *Environment) RetentionCandidates(ctx context.Context, protected map[int64]struct{}) ([]int64, error) {
	snapshots, err := e.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	var candidates []int64
	for _, snapshot := range snapshots {
		if snapshot.ID == 0 {
			continue
		}
		if _, ok := protected[snapshot.ID]; ok {
			continue
		}
		candidates = append(candidates, snapshot.ID)
	}
	return candidates, nil
}

func (e *Environment) ExpireSnapshots(ctx context.Context, versions []int64, dryRun bool) error {
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	if e.readOnly {
		return ErrReadOnlyEnvironment
	}
	if len(versions) == 0 {
		return nil
	}
	unlock := lockCatalogWrites(e.catalogLockKey())
	defer unlock()
	_, err := e.db.ExecContext(ctx, fmt.Sprintf("CALL ducklake_expire_snapshots(%s, versions => %s, dry_run => %t)", sqlStringLiteral(catalogAlias), snapshotListLiteral(versions), dryRun))
	return err
}

func (e *Environment) CleanupOldFiles(ctx context.Context, dryRun bool) error {
	if e != nil && e.readOnly {
		return ErrReadOnlyEnvironment
	}
	if err := e.rejectSharedPoolMaintenance("ducklake_cleanup_old_files"); err != nil {
		return err
	}
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	unlock := lockCatalogWrites(e.catalogLockKey())
	defer unlock()
	_, err := e.db.ExecContext(ctx, fmt.Sprintf("CALL ducklake_cleanup_old_files(%s, dry_run => %t)", sqlStringLiteral(catalogAlias), dryRun))
	return err
}

func (e *Environment) DeleteOrphanedFiles(ctx context.Context, dryRun bool) error {
	if e != nil && e.readOnly {
		return ErrReadOnlyEnvironment
	}
	if err := e.rejectSharedPoolMaintenance("ducklake_delete_orphaned_files"); err != nil {
		return err
	}
	if e == nil || e.db == nil {
		return fmt.Errorf("ducklake environment is not initialized")
	}
	unlock := lockCatalogWrites(e.catalogLockKey())
	defer unlock()
	_, err := e.db.ExecContext(ctx, fmt.Sprintf("CALL ducklake_delete_orphaned_files(%s, dry_run => %t)", sqlStringLiteral(catalogAlias), dryRun))
	return err
}

func lockCatalogWrites(catalogPath string) func() {
	key := catalogLockKey(catalogPath)
	value, _ := catalogWriteLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func catalogLockKey(catalogPath string) string {
	clean := filepath.Clean(strings.TrimSpace(catalogPath))
	if abs, err := filepath.Abs(clean); err == nil {
		return abs
	}
	return clean
}

func setCommitMessage(ctx context.Context, tx *sql.Tx, servingStateID string, extra map[string]string) error {
	servingStateID = strings.TrimSpace(servingStateID)
	if servingStateID == "" {
		servingStateID = "unknown"
	}
	payload := map[string]string{"servingStateId": servingStateID}
	for key, value := range extra {
		payload[key] = value
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"CALL "+catalogAlias+".set_commit_message(?, ?, extra_info => ?)",
		"LeapView",
		"serving-state "+servingStateID,
		string(bytes),
	)
	return err
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// sqlDB is intentionally package-private. Production callers must use an
// admitted operation lease so one logical operation owns one connection.
func (e *Environment) sqlDB() *sql.DB {
	if e == nil {
		return nil
	}
	return e.db
}

func (e *Environment) ConnectionStats() sql.DBStats {
	if e == nil || e.db == nil {
		return sql.DBStats{}
	}
	return e.db.Stats()
}

func (e *Environment) ReadConcurrency() int {
	if e == nil || e.readConcurrency <= 0 {
		return 1
	}
	return e.readConcurrency
}

func (e *Environment) Path() string {
	if e == nil {
		return ""
	}
	if e.catalogIdentity != "" {
		return e.catalogIdentity
	}
	return e.layout.CatalogPath
}

// IsPostgresCatalog reports whether this environment attaches DuckLake
// metadata from PostgreSQL rather than a serialized local catalog file.
func (e *Environment) IsPostgresCatalog() bool {
	return e != nil && e.postgresCatalog
}

// PostgresMetadataSchema is the exact per-pool metadata namespace used by the
// attached PostgreSQL catalog. It is empty for file-backed environments.
func (e *Environment) PostgresMetadataSchema() string {
	if e == nil {
		return ""
	}
	return e.postgresMetadata
}

// PostgresSnapshotVersion returns the exact serving snapshot pinned at
// attachment time, or zero when the environment is not PostgreSQL-backed.
func (e *Environment) PostgresSnapshotVersion() int64 {
	if e == nil {
		return 0
	}
	return e.postgresSnapshot
}

func (e *Environment) catalogLockKey() string {
	if e == nil {
		return ""
	}
	if e.catalogLock != "" {
		return e.catalogLock
	}
	return e.layout.CatalogPath
}

func (e *Environment) Exec(ctx context.Context, statement string) error {
	if e != nil && e.readOnly {
		return ErrReadOnlyEnvironment
	}
	if err := e.rejectSharedPoolStatement(statement); err != nil {
		return err
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return err
	}
	defer release()
	_, err = conn.ExecContext(ctx, statement)
	return err
}

func (e *Environment) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return queryRows(ctx, conn, plan)
}

func queryRows(ctx context.Context, conn *sql.Conn, plan semanticquery.Plan) (semanticquery.Rows, error) {
	rows, err := conn.QueryContext(ctx, plan.SQL, plan.Args...)
	if err != nil {
		return nil, analyticsresource.Classify(err)
	}
	defer rows.Close()

	values := make([]any, len(plan.Columns))
	scans := make([]any, len(plan.Columns))
	for i := range values {
		scans[i] = &values[i]
	}
	result := semanticquery.Rows{}
	for rows.Next() {
		if err := rows.Scan(scans...); err != nil {
			return nil, err
		}
		row := semanticquery.Row{}
		for i, column := range plan.Columns {
			row[column] = cloneValue(values[i])
		}
		if budget, ok := dataquery.ResultBudgetFromContext(ctx); ok {
			if err := budget.ConsumeRow(row); err != nil {
				return nil, err
			}
		}
		result = append(result, row)
	}
	return result, analyticsresource.Classify(rows.Err())
}

func (e *Environment) Count(ctx context.Context, plan semanticquery.Plan) (int, error) {
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	var count int
	if err := conn.QueryRowContext(ctx, plan.SQL, plan.Args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (e *Environment) FloatBounds(ctx context.Context, plan semanticquery.Plan, valueColumn string) (semanticquery.FloatBounds, error) {
	if err := validateColumnAlias(valueColumn); err != nil {
		return semanticquery.FloatBounds{}, err
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return semanticquery.FloatBounds{}, err
	}
	defer release()
	return floatBounds(ctx, conn, plan, valueColumn)
}

func floatBounds(ctx context.Context, conn *sql.Conn, plan semanticquery.Plan, valueColumn string) (semanticquery.FloatBounds, error) {
	if err := validateColumnAlias(valueColumn); err != nil {
		return semanticquery.FloatBounds{}, err
	}
	query := "WITH raw AS (" + plan.SQL + ")\nSELECT MIN(" + valueColumn + "), MAX(" + valueColumn + ") FROM raw"
	var minValue, maxValue sql.NullFloat64
	if err := conn.QueryRowContext(ctx, query, plan.Args...).Scan(&minValue, &maxValue); err != nil {
		return semanticquery.FloatBounds{}, err
	}
	if !minValue.Valid || !maxValue.Valid {
		return semanticquery.FloatBounds{}, nil
	}
	return semanticquery.FloatBounds{Min: minValue.Float64, Max: maxValue.Float64, Valid: true}, nil
}

func (e *Environment) Histogram(ctx context.Context, plan semanticquery.Plan, spec semanticquery.HistogramSpec) ([]semanticquery.HistogramBin, error) {
	if err := validateColumnAlias(spec.ValueColumn); err != nil {
		return nil, err
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	bounds, err := floatBounds(ctx, conn, plan, spec.ValueColumn)
	if err != nil {
		return nil, err
	}
	if !bounds.Valid {
		return []semanticquery.HistogramBin{}, nil
	}
	if spec.BinCount <= 0 {
		return nil, fmt.Errorf("histogram bin count must be positive")
	}
	if bounds.Min == bounds.Max {
		var count int
		query := "WITH raw AS (" + plan.SQL + ")\nSELECT COUNT(*) FROM raw"
		if err := conn.QueryRowContext(ctx, query, plan.Args...).Scan(&count); err != nil {
			return nil, err
		}
		return []semanticquery.HistogramBin{{Bucket: 0, Count: count, Start: bounds.Min, End: bounds.Max}}, nil
	}

	bucketExpr := fmt.Sprintf("LEAST(%d, CAST(FLOOR(((%s - ?) / NULLIF(? - ?, 0)) * ?) AS INTEGER))", spec.BinCount-1, spec.ValueColumn)
	query := fmt.Sprintf(`WITH raw AS (%s)
SELECT %s AS bucket, COUNT(*) AS value
FROM raw
GROUP BY bucket
ORDER BY bucket ASC`, plan.SQL, bucketExpr)
	args := append(append([]any{}, plan.Args...), bounds.Min, bounds.Max, bounds.Min, spec.BinCount)
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	width := (bounds.Max - bounds.Min) / float64(spec.BinCount)
	bins := []semanticquery.HistogramBin{}
	for rows.Next() {
		var bucket int
		var count int
		if err := rows.Scan(&bucket, &count); err != nil {
			return nil, err
		}
		start := bounds.Min + float64(bucket)*width
		bins = append(bins, semanticquery.HistogramBin{
			Bucket: bucket,
			Count:  count,
			Start:  start,
			End:    start + width,
		})
	}
	return bins, rows.Err()
}

func (e *Environment) Distribution(ctx context.Context, plan semanticquery.Plan, spec semanticquery.DistributionSpec) (semanticquery.Rows, error) {
	if err := validateColumnAlias(spec.GroupColumn); err != nil {
		return nil, err
	}
	if err := validateColumnAlias(spec.ValueColumn); err != nil {
		return nil, err
	}
	orderBy, err := distributionOrderBy(spec.Sort)
	if err != nil {
		return nil, err
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	query := fmt.Sprintf(`WITH raw AS (%s)
SELECT %s AS label,
       MIN(%s) AS min,
       quantile_cont(%s, 0.25) AS q1,
       median(%s) AS median,
       quantile_cont(%s, 0.75) AS q3,
       MAX(%s) AS max
FROM raw
GROUP BY label
ORDER BY %s`, plan.SQL, spec.GroupColumn, spec.ValueColumn, spec.ValueColumn, spec.ValueColumn, spec.ValueColumn, spec.ValueColumn, orderBy)
	if spec.Limit > 0 {
		query += fmt.Sprintf("\nLIMIT %d", spec.Limit)
	}
	return queryRows(ctx, conn, semanticquery.Plan{
		SQL:     query,
		Args:    plan.Args,
		Columns: []string{"label", "min", "q1", "median", "q3", "max"},
	})
}

func (e *Environment) queryConnection(ctx context.Context) (*sql.Conn, func(), error) {
	if e == nil || e.db == nil {
		return nil, nil, fmt.Errorf("ducklake environment is not initialized")
	}
	if e.closed.Load() {
		return nil, nil, ErrEnvironmentClosed
	}
	if healthErr := e.Healthy(); healthErr != nil {
		return nil, nil, fmt.Errorf("analytical environment is fatally unhealthy: %w", healthErr)
	}
	if current, ok := ctx.Value(leaseContextKey{}).(*leaseState); ok && current != nil {
		if current.env != e {
			return nil, nil, ErrConflictingLease
		}
		current.mu.Lock()
		conn := current.conn
		current.mu.Unlock()
		if conn == nil {
			return nil, nil, fmt.Errorf("DuckDB lease is already released")
		}
		return conn, func() {}, nil
	}
	started := time.Now()
	conn, err := e.db.Conn(ctx)
	dataquery.ObserveConnectionWait(ctx, time.Since(started))
	if err != nil {
		return nil, nil, err
	}
	return conn, func() { _ = conn.Close() }, nil
}

func (e *Environment) Layout() Layout {
	if e == nil {
		return Layout{}
	}
	return e.layout
}

func (e *Environment) PhysicalPoolID() string {
	if e == nil {
		return ""
	}
	return e.physicalPoolID
}

func (e *Environment) SharedPool() bool {
	return e != nil && e.sharedPool
}

func (e *Environment) ReadOnly() bool {
	return e != nil && e.readOnly
}

func (e *Environment) Compatibility() CompatibilityTuple {
	if e == nil {
		return CompatibilityTuple{}
	}
	return e.compatibility
}

func (e *Environment) rejectSharedPoolMaintenance(capability string) error {
	if e != nil && e.sharedPool {
		return fmt.Errorf("%w: %s", ErrSharedPoolMaintenance, capability)
	}
	return nil
}

func (e *Environment) rejectSharedPoolStatement(statement string) error {
	if e == nil || !e.sharedPool {
		return nil
	}
	return rejectSharedPoolStatementText(statement)
}

func rejectSharedPoolStatementText(statement string) error {
	for _, token := range sqlTokens(statement) {
		switch token {
		case "DUCKLAKE_CLEANUP_OLD_FILES", "DUCKLAKE_DELETE_ORPHANED_FILES", "DUCKLAKE_MERGE_ADJACENT_FILES", "DUCKLAKE_REWRITE_DATA_FILES":
			return fmt.Errorf("%w: %s", ErrSharedPoolMaintenance, strings.ToLower(token))
		case "CHECKPOINT":
			// DuckLake's catalog-level CHECKPOINT currently invokes expiration,
			// compaction, old-file cleanup and orphan cleanup in sequence.
			return ErrUnsafeCheckpoint
		}
	}
	return nil
}

// sqlTokens is intentionally a small lexical scanner, not a SQL parser. It
// recognizes identifiers while skipping string literals, quoted identifiers,
// and both SQL comment forms. This keeps authored SQL containing words such as
// "checkpoint" from being mistaken for a capability invocation.
func sqlTokens(statement string) []string {
	var tokens []string
	for i := 0; i < len(statement); {
		switch statement[i] {
		case '\'', '"', '`', '[':
			i = skipSQLQuoted(statement, i)
			continue
		case '$':
			if next, ok := dollarQuoteDelimiter(statement, i); ok {
				if end := strings.Index(statement[i+len(next):], next); end >= 0 {
					i += len(next) + end + len(next)
				} else {
					i = len(statement)
				}
				continue
			}
		case '-':
			if i+1 < len(statement) && statement[i+1] == '-' {
				i += 2
				for i < len(statement) && statement[i] != '\n' {
					i++
				}
				continue
			}
		case '/':
			if i+1 < len(statement) && statement[i+1] == '*' {
				i += 2
				for i+1 < len(statement) && !(statement[i] == '*' && statement[i+1] == '/') {
					i++
				}
				if i+1 < len(statement) {
					i += 2
				}
				continue
			}
		}
		if !isSQLIdentifierByte(statement[i]) {
			i++
			continue
		}
		start := i
		for i < len(statement) && isSQLIdentifierByte(statement[i]) {
			i++
		}
		tokens = append(tokens, strings.ToUpper(statement[start:i]))
	}
	return tokens
}

func skipSQLQuoted(statement string, start int) int {
	delimiter := statement[start]
	i := start + 1
	for i < len(statement) {
		if delimiter == '[' {
			if statement[i] == ']' {
				return i + 1
			}
			i++
			continue
		}
		if statement[i] == delimiter {
			if i+1 < len(statement) && statement[i+1] == delimiter {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(statement)
}

func dollarQuoteDelimiter(statement string, start int) (string, bool) {
	end := start + 1
	for end < len(statement) && statement[end] != '$' {
		if !isSQLIdentifierByte(statement[end]) {
			return "", false
		}
		end++
	}
	if end >= len(statement) {
		return "", false
	}
	return statement[start : end+1], true
}

func isSQLIdentifierByte(value byte) bool {
	return value == '_' || value == '$' || value >= '0' && value <= '9' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func (e *Environment) Close() error {
	if e == nil || e.db == nil {
		return nil
	}
	e.closeOnce.Do(func() {
		e.closed.Store(true)
		e.closeErr = e.db.Close()
	})
	return e.closeErr
}

func extensionUnavailable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "extension") &&
		(strings.Contains(text, "not found") ||
			strings.Contains(text, "failed to download") ||
			strings.Contains(text, "failed to install") ||
			strings.Contains(text, "not be loaded"))
}

func sqlLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func sqlStringLiteral(value string) string {
	return "'" + sqlLiteral(value) + "'"
}

func snapshotListLiteral(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprint(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "."
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return typed
	default:
		return typed
	}
}

func validateColumnAlias(value string) error {
	if value == "" {
		return fmt.Errorf("empty column alias")
	}
	for i, r := range value {
		if i == 0 {
			if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && r != '_' {
				return fmt.Errorf("invalid column alias %q", value)
			}
			continue
		}
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return fmt.Errorf("invalid column alias %q", value)
		}
	}
	return nil
}

func distributionOrderBy(sorts []semanticquery.Sort) (string, error) {
	if len(sorts) == 0 {
		return "label ASC", nil
	}
	parts := make([]string, 0, len(sorts))
	for _, sortSpec := range sorts {
		field := sortSpec.Field
		if field == "" {
			field = "label"
		}
		switch field {
		case "label", "min", "q1", "median", "q3", "max":
		default:
			return "", fmt.Errorf("unsupported distribution sort field %q", sortSpec.Field)
		}
		direction := "ASC"
		if strings.EqualFold(sortSpec.Direction, "desc") {
			direction = "DESC"
		} else if sortSpec.Direction != "" && !strings.EqualFold(sortSpec.Direction, "asc") {
			return "", fmt.Errorf("unsupported sort direction %q", sortSpec.Direction)
		}
		parts = append(parts, field+" "+direction)
	}
	return strings.Join(parts, ", "), nil
}
