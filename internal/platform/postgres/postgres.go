// Package postgres contains the capability-neutral PostgreSQL connection
// boundary. It deliberately owns connection setup and readiness checks only;
// schema, migrations, and repositories remain owned by their capabilities.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	platformdb "github.com/flidai/leapview/internal/platform/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	// DefaultExpectedMajor is the initial PostgreSQL major supported by the
	// target architecture.
	DefaultExpectedMajor = 18

	defaultMinConns         int32 = 1
	defaultMaxConns         int32 = 8
	defaultAcquireTimeout         = 5 * time.Second
	defaultStatementTimeout       = 30 * time.Second
	defaultLockTimeout            = 5 * time.Second
	defaultIdleTxTimeout          = time.Minute
)

// Intent describes the access mode a pool is admitted for. Read-only pools
// enforce default_transaction_read_only at connection startup; read-write
// pools fail readiness when the server is a standby or reports a read-only
// default.
type Intent string

const (
	IntentReadWrite Intent = "read-write"
	IntentReadOnly  Intent = "read-only"

	// ReadWrite and ReadOnly are concise aliases for callers that prefer the
	// nouns over the Intent-prefixed constants.
	ReadWrite = IntentReadWrite
	ReadOnly  = IntentReadOnly
)

// Config is the explicit, independent connection policy for one PostgreSQL
// capability database. Control and DuckLake pools each receive their own
// Config; this package never dual-writes or coordinates transactions between
// them.
type Config struct {
	URL string

	// ExpectedMajor defaults to 18 when omitted. A value other than the
	// server's major version fails pool startup.
	ExpectedMajor int
	// RuntimeRole is compared with current_user during connection admission.
	RuntimeRole string
	Intent      Intent
	// RequireTLS rejects URLs whose sslmode permits a plaintext connection.
	// Production callers should set this true; conformance tests may use an
	// explicitly disabled local URL and leave it false.
	RequireTLS bool

	MinConns int32
	MaxConns int32

	AcquireTimeout         time.Duration
	StatementTimeout       time.Duration
	LockTimeout            time.Duration
	IdleTransactionTimeout time.Duration
}

// RuntimeConfig groups independent control and DuckLake connection policies.
// It is a configuration value only; OpenPools is optional and is not used by
// production composition until both capability owners are migrated.
type RuntimeConfig struct {
	Control  Config
	DuckLake Config
}

// ControlPlaneConfig describes the independently authenticated control-plane roles.
// The migrator pool is used only during startup schema application, the
// runtime pool serves normal read/write requests, maintenance is a required
// one-connection read/write pool for bounded maintenance operations, and the
// optional readonly pool is reserved for bounded reporting/backup reads.
// Every role receives its own URL and credentials; this type never derives
// one credential from another or falls back to a shared superuser connection.
type ControlPlaneConfig struct {
	Migrator    Config
	Runtime     Config
	Maintenance Config
	Readonly    *Config
}

// ControlPlanePools owns independently budgeted control-plane pools.  The
// migrator is retained so callers can run a startup migration transaction and
// then close it before serving traffic; Runtime, Maintenance and Readonly
// remain available for their respective authorities.
type ControlPlanePools struct {
	Migrator    *Pool
	Runtime     *Pool
	Maintenance *Pool
	Readonly    *Pool
}

// Close closes every configured pool.  It is safe to call on a nil value and
// deliberately closes the readonly/runtime pools before the migrator so a
// shutdown cannot race an in-flight startup migration.
func (p *ControlPlanePools) Close() {
	if p == nil {
		return
	}
	if p.Readonly != nil {
		p.Readonly.Close()
	}
	if p.Maintenance != nil {
		p.Maintenance.Close()
	}
	if p.Runtime != nil {
		p.Runtime.Close()
	}
	if p.Migrator != nil {
		p.Migrator.Close()
	}
}

// OpenControlPlane opens the required migrator, runtime, and maintenance
// pools and, when supplied, an explicit readonly pool. Opening is fail-closed:
// a failed later pool closes every pool already opened and returns the
// original contextual error. Migration execution remains the caller's
// responsibility so the pool package cannot accidentally hide a transaction
// boundary.
func OpenControlPlane(ctx context.Context, cfg ControlPlaneConfig) (*ControlPlanePools, error) {
	migrator, err := Open(ctx, cfg.Migrator)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL control migrator pool: %w", err)
	}
	pools, err := OpenServingControlPlane(ctx, cfg)
	if err != nil {
		migrator.Close()
		return nil, err
	}
	pools.Migrator = migrator
	return pools, nil
}

// OpenServingControlPlane opens only the runtime, maintenance, and optional
// readonly pools. Production serving uses this path so owner-capable migration
// credentials never enter the process and startup cannot mutate the schema.
func OpenServingControlPlane(ctx context.Context, cfg ControlPlaneConfig) (*ControlPlanePools, error) {
	if strings.TrimSpace(cfg.Maintenance.URL) == "" {
		return nil, errors.New("PostgreSQL control maintenance URL is required")
	}
	if cfg.Maintenance.Intent != "" && cfg.Maintenance.Intent != IntentReadWrite {
		return nil, errors.New("PostgreSQL control maintenance pool must be read-write")
	}
	if cfg.Maintenance.MinConns == 0 {
		cfg.Maintenance.MinConns = 1
	}
	if cfg.Maintenance.MaxConns == 0 {
		cfg.Maintenance.MaxConns = 1
	}
	if cfg.Maintenance.MinConns != 1 || cfg.Maintenance.MaxConns != 1 {
		return nil, errors.New("PostgreSQL control maintenance pool must use exactly one connection")
	}
	runtime, err := Open(ctx, cfg.Runtime)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL control runtime pool: %w", err)
	}
	maintenance, err := Open(ctx, cfg.Maintenance)
	if err != nil {
		runtime.Close()
		return nil, fmt.Errorf("open PostgreSQL control maintenance pool: %w", err)
	}
	var readonly *Pool
	if cfg.Readonly != nil {
		readonly, err = Open(ctx, *cfg.Readonly)
		if err != nil {
			maintenance.Close()
			runtime.Close()
			return nil, fmt.Errorf("open PostgreSQL control readonly pool: %w", err)
		}
	}
	return &ControlPlanePools{Runtime: runtime, Maintenance: maintenance, Readonly: readonly}, nil
}

// Pool is a bounded pgxpool with an acquisition deadline. The embedded pool
// is intentionally not exposed directly so callers use the same bounded
// Acquire contract for every capability.
type Pool struct {
	pool           *pgxpool.Pool
	acquireTimeout time.Duration
	config         Config
}

// SQLDB exposes the migration-only database/sql adapter required by Goose.
// Runtime repositories continue to use native pgx. The returned handle shares
// this pool's connections and must be closed by the caller before Pool.Close.
func (p *Pool) SQLDB() (*sql.DB, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("postgres pool is nil")
	}
	return stdlib.OpenDBFromPool(p.pool), nil
}

// NativePool exposes the admitted pgx pool only to infrastructure adapters
// whose mature package integration requires pgx's concrete transaction type
// (currently River). Product repositories continue to depend on Pool's
// bounded methods and must not use this as a general escape hatch.
func (p *Pool) NativePool() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.pool
}

// Extension is the installed PostgreSQL extension identity used by the
// post-migration production admission gate.
type Extension struct {
	Name   string
	Schema string
}

// PoolConfig returns the normalized policy used to construct the pool.
func (p *Pool) PoolConfig() Config {
	if p == nil {
		return Config{}
	}
	return p.config
}

// Acquire obtains one connection, bounded by the configured acquisition
// timeout in addition to the caller's context deadline.
func (p *Pool) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	if p == nil || p.pool == nil {
		return nil, errors.New("postgres pool is nil")
	}
	if p.acquireTimeout <= 0 {
		return p.pool.Acquire(ctx)
	}
	acquireCtx, cancel := context.WithTimeout(ctx, p.acquireTimeout)
	defer cancel()
	return p.pool.Acquire(acquireCtx)
}

// AcquireFunc runs fn with one bounded connection and releases it afterwards.
func (p *Pool) AcquireFunc(ctx context.Context, fn func(*pgxpool.Conn) error) error {
	if fn == nil {
		return errors.New("postgres acquire function is nil")
	}
	conn, err := p.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return fn(conn)
}

// Exec executes one statement using a bounded acquired connection. Transaction
// callers should use Acquire or AcquireFunc so the transaction remains on one
// connection while preserving the acquisition deadline.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	var tag pgconn.CommandTag
	err := p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		var err error
		tag, err = conn.Exec(ctx, sql, args...)
		return err
	})
	return tag, err
}

// Query keeps the explicitly acquired connection leased until the returned
// rows are closed or exhausted. Only acquisition uses AcquireTimeout; query
// execution remains governed by the caller context and PostgreSQL's session
// statement timeout.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		conn.Release()
		return nil, err
	}
	return &leasedRows{Rows: rows, conn: conn}, nil
}

// QueryRow leases one connection until Scan. Callers must call Scan exactly
// as they would for pgxpool.QueryRow; Scan releases the pool connection even
// when decoding fails.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, err := p.Acquire(ctx)
	if err != nil {
		return errorRow{err: err}
	}
	return &leasedRow{Row: conn.QueryRow(ctx, sql, args...), conn: conn}
}

// Begin starts a caller-owned transaction on a connection obtained through
// the bounded acquisition policy. Commit or Rollback releases the connection.
func (p *Pool) Begin(ctx context.Context) (pgx.Tx, error) {
	return p.BeginTx(ctx, pgx.TxOptions{})
}

// BeginTx starts a caller-owned transaction with an explicit transaction
// mode. It preserves the pool's bounded acquisition policy while allowing
// capability adapters to request an explicit isolation level rather than
// inheriting a server default.
func (p *Pool) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	conn, err := p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, options)
	if err != nil {
		conn.Release()
		return nil, err
	}
	return &leasedTx{Tx: tx, conn: conn}, nil
}

type leasedRows struct {
	pgx.Rows
	conn *pgxpool.Conn
	once sync.Once
}

func (r *leasedRows) Next() bool {
	if r == nil || r.Rows == nil {
		return false
	}
	ok := r.Rows.Next()
	if !ok {
		r.release()
	}
	return ok
}

func (r *leasedRows) Close() {
	if r == nil {
		return
	}
	if r.Rows != nil {
		r.Rows.Close()
	}
	r.release()
}

func (r *leasedRows) release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		if r.conn != nil {
			r.conn.Release()
		}
	})
}

type leasedRow struct {
	pgx.Row
	conn *pgxpool.Conn
	once sync.Once
}

func (r *leasedRow) Scan(dest ...any) error {
	if r == nil || r.Row == nil {
		return errors.New("postgres query row is nil")
	}
	defer r.once.Do(func() { r.conn.Release() })
	return r.Row.Scan(dest...)
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

type leasedTx struct {
	pgx.Tx
	conn *pgxpool.Conn
	once sync.Once
}

func (tx *leasedTx) Commit(ctx context.Context) error {
	if tx == nil || tx.Tx == nil {
		return pgx.ErrTxClosed
	}
	defer tx.release()
	return tx.Tx.Commit(ctx)
}

func (tx *leasedTx) Rollback(ctx context.Context) error {
	if tx == nil || tx.Tx == nil {
		return pgx.ErrTxClosed
	}
	defer tx.release()
	return tx.Tx.Rollback(ctx)
}

func (tx *leasedTx) release() {
	if tx == nil {
		return
	}
	tx.once.Do(func() {
		if tx.conn != nil {
			tx.conn.Release()
		}
	})
}

// Ping checks pool reachability after startup validation.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return errors.New("postgres pool is nil")
	}
	return p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		return conn.Conn().Ping(ctx)
	})
}

// CurrentDatabase returns PostgreSQL's authoritative database identity. The
// query is generated from the platform capability SQLC package so callers do
// not hand-roll a static query at the composition boundary.
func (p *Pool) CurrentDatabase(ctx context.Context) (string, error) {
	if p == nil || p.pool == nil {
		return "", errors.New("postgres pool is nil")
	}
	return platformdb.New(p).CurrentDatabase(ctx)
}

// RequiredExtension returns one exact installed extension and its owning
// schema. Missing extensions remain a query error so admission fails closed.
func (p *Pool) RequiredExtension(ctx context.Context, name string) (Extension, error) {
	if p == nil || p.pool == nil {
		return Extension{}, errors.New("postgres pool is nil")
	}
	record, err := platformdb.New(p).RequiredExtension(ctx, name)
	if err != nil {
		return Extension{}, err
	}
	return Extension{Name: record.ExtensionName, Schema: record.SchemaName}, nil
}

func (p *Pool) HasSchemaPrivilege(ctx context.Context, object, privilege string) (bool, error) {
	if p == nil || p.pool == nil {
		return false, errors.New("postgres pool is nil")
	}
	return platformdb.New(p).HasSchemaPrivilege(ctx, platformdb.HasSchemaPrivilegeParams{SchemaName: object, PrivilegeName: privilege})
}

func (p *Pool) HasTablePrivilege(ctx context.Context, object, privilege string) (bool, error) {
	if p == nil || p.pool == nil {
		return false, errors.New("postgres pool is nil")
	}
	return platformdb.New(p).HasTablePrivilege(ctx, platformdb.HasTablePrivilegeParams{TableName: object, PrivilegeName: privilege})
}

func (p *Pool) HasFunctionPrivilege(ctx context.Context, object, privilege string) (bool, error) {
	if p == nil || p.pool == nil {
		return false, errors.New("postgres pool is nil")
	}
	return platformdb.New(p).HasFunctionPrivilege(ctx, platformdb.HasFunctionPrivilegeParams{FunctionName: object, PrivilegeName: privilege})
}

func (p *Pool) HasCurrentDatabasePrivilege(ctx context.Context, privilege string) (bool, error) {
	if p == nil || p.pool == nil {
		return false, errors.New("postgres pool is nil")
	}
	return platformdb.New(p).HasCurrentDatabasePrivilege(ctx, privilege)
}

// Close releases all pooled connections. It is safe to call on a nil Pool.
func (p *Pool) Close() {
	if p != nil && p.pool != nil {
		p.pool.Close()
	}
}

// Stats exposes the underlying bounded pool statistics for operational
// telemetry without exposing mutable pool configuration.
func (p *Pool) Stats() *pgxpool.Stat {
	if p == nil || p.pool == nil {
		return nil
	}
	return p.pool.Stat()
}

// OpenControl opens and validates one control-plane pool.
func OpenControl(ctx context.Context, cfg Config) (*Pool, error) {
	return Open(ctx, cfg)
}

// OpenControlPool is an explicit alias retained for readability at call
// sites that construct more than one capability pool.
func OpenControlPool(ctx context.Context, cfg Config) (*Pool, error) {
	return OpenControl(ctx, cfg)
}

// OpenDuckLake opens and validates one DuckLake catalog pool. The caller must
// provide a separate URL, role, and policy from the control pool.
func OpenDuckLake(ctx context.Context, cfg Config) (*Pool, error) {
	return Open(ctx, cfg)
}

// OpenDuckLakePool is an explicit alias for OpenDuckLake.
func OpenDuckLakePool(ctx context.Context, cfg Config) (*Pool, error) {
	return OpenDuckLake(ctx, cfg)
}

// OpenPools opens independently configured pools. It is useful to test the
// split boundary, but production composition should adopt each capability
// independently and must not infer a dual-write transaction from this helper.
func OpenPools(ctx context.Context, cfg RuntimeConfig) (*Pools, error) {
	control, err := OpenControl(ctx, cfg.Control)
	if err != nil {
		return nil, fmt.Errorf("open control PostgreSQL pool: %w", err)
	}
	ducklake, err := OpenDuckLake(ctx, cfg.DuckLake)
	if err != nil {
		control.Close()
		return nil, fmt.Errorf("open DuckLake PostgreSQL pool: %w", err)
	}
	return &Pools{Control: control, DuckLake: ducklake}, nil
}

// Pools holds independent capability pools and closes both safely.
type Pools struct {
	Control  *Pool
	DuckLake *Pool
}

func (p *Pools) Close() {
	if p == nil {
		return
	}
	p.Control.Close()
	p.DuckLake.Close()
}

// Open parses the URL, applies bounded pool and session settings, then admits
// the server only after version, role, and read/write intent validation.
func Open(ctx context.Context, cfg Config) (*Pool, error) {
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, errors.New("PostgreSQL URL is malformed")
	}
	if err := ConfigurePool(poolConfig, cfg); err != nil {
		return nil, err
	}
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return Validate(ctx, conn, cfg)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	wrapped := &Pool{pool: pool, acquireTimeout: cfg.AcquireTimeout, config: cfg}
	if err := wrapped.Ping(ctx); err != nil {
		wrapped.Close()
		return nil, fmt.Errorf("validate PostgreSQL pool: %w", err)
	}
	return wrapped, nil
}

// ConfigurePool applies policy to a parsed pgx pool config. It is exported so
// unit tests and capability owners can verify settings without a live server.
func ConfigurePool(poolConfig *pgxpool.Config, cfg Config) error {
	if poolConfig == nil {
		return errors.New("PostgreSQL pool config is nil")
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	poolConfig.MinConns = cfg.MinConns
	poolConfig.MaxConns = cfg.MaxConns
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolConfig.ConnConfig.RuntimeParams["statement_timeout"] = milliseconds(cfg.StatementTimeout)
	poolConfig.ConnConfig.RuntimeParams["lock_timeout"] = milliseconds(cfg.LockTimeout)
	poolConfig.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = milliseconds(cfg.IdleTransactionTimeout)
	if cfg.Intent == IntentReadOnly {
		poolConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	} else {
		poolConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "off"
	}
	return nil
}

// Probe is the read-only query seam used by Validate.
type Probe interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// probeDBTX preserves the narrow Probe fake seam while satisfying sqlc's
// pgx/v5 DBTX interface. Validate only invokes QueryRow; the other methods are
// deliberately unreachable and return an explicit error if that changes.
type probeDBTX struct{ Probe }

func (p probeDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("PostgreSQL validation probe does not support Exec")
}

func (p probeDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("PostgreSQL validation probe does not support Query")
}

// Validate checks PostgreSQL major version, runtime role, and read/write
// intent. It performs no mutations and does not select a production backend.
func Validate(ctx context.Context, probe Probe, cfg Config) error {
	if probe == nil {
		return errors.New("PostgreSQL validation probe is nil")
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	row, err := platformdb.New(probeDBTX{Probe: probe}).Probe(ctx)
	if err != nil {
		return fmt.Errorf("probe PostgreSQL capabilities: %w", err)
	}
	versionNum, role, readOnly, recovery := row.ServerVersionNum, row.RuntimeRole, row.DefaultTransactionReadOnly, row.InRecovery
	major, err := parseMajor(versionNum)
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL server version %q: %w", versionNum, err)
	}
	if major != cfg.ExpectedMajor {
		return fmt.Errorf("unsupported PostgreSQL major %d (expected %d)", major, cfg.ExpectedMajor)
	}
	if strings.TrimSpace(role) != cfg.RuntimeRole {
		return fmt.Errorf("PostgreSQL runtime role mismatch: connected as %q, expected %q", strings.TrimSpace(role), cfg.RuntimeRole)
	}
	serverReadOnly, err := parseBoolSetting(readOnly)
	if err != nil {
		return fmt.Errorf("invalid PostgreSQL read-only setting %q: %w", readOnly, err)
	}
	switch cfg.Intent {
	case IntentReadWrite:
		if recovery || serverReadOnly {
			return errors.New("PostgreSQL read-write intent is unavailable on a read-only server")
		}
	case IntentReadOnly:
		if !recovery && !serverReadOnly {
			return errors.New("PostgreSQL read-only intent requires a read-only session")
		}
	default:
		return fmt.Errorf("unsupported PostgreSQL intent %q", cfg.Intent)
	}
	return nil
}

// ValidateConfig checks policy before any network connection is attempted.
func (c Config) Validate() error {
	c = c.withDefaults()
	if strings.TrimSpace(c.URL) == "" {
		return errors.New("PostgreSQL URL is required")
	}
	if c.RequireTLS {
		parsed, err := url.Parse(c.URL)
		if err != nil {
			return errors.New("PostgreSQL URL is malformed")
		}
		switch strings.ToLower(strings.TrimSpace(parsed.Query().Get("sslmode"))) {
		case "require", "verify-ca", "verify-full":
		default:
			return errors.New("PostgreSQL URL must set sslmode=require, verify-ca, or verify-full when TLS is required")
		}
	}
	if c.ExpectedMajor <= 0 {
		return errors.New("PostgreSQL expected major must be positive")
	}
	if strings.TrimSpace(c.RuntimeRole) == "" {
		return errors.New("PostgreSQL runtime role is required")
	}
	if c.Intent != IntentReadWrite && c.Intent != IntentReadOnly {
		return fmt.Errorf("unsupported PostgreSQL intent %q", c.Intent)
	}
	if c.MinConns < 0 {
		return errors.New("PostgreSQL minimum pool size must not be negative")
	}
	if c.MaxConns <= 0 {
		return errors.New("PostgreSQL maximum pool size must be positive")
	}
	if c.MinConns > c.MaxConns {
		return fmt.Errorf("PostgreSQL minimum pool size %d exceeds maximum %d", c.MinConns, c.MaxConns)
	}
	for name, value := range map[string]time.Duration{
		"acquire timeout":          c.AcquireTimeout,
		"statement timeout":        c.StatementTimeout,
		"lock timeout":             c.LockTimeout,
		"idle transaction timeout": c.IdleTransactionTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("PostgreSQL %s must be positive", name)
		}
	}
	return nil
}

func (c Config) withDefaults() Config {
	if c.ExpectedMajor == 0 {
		c.ExpectedMajor = DefaultExpectedMajor
	}
	if c.Intent == "" {
		c.Intent = IntentReadWrite
	}
	if c.MinConns == 0 {
		c.MinConns = defaultMinConns
	}
	if c.MaxConns == 0 {
		c.MaxConns = defaultMaxConns
	}
	if c.AcquireTimeout == 0 {
		c.AcquireTimeout = defaultAcquireTimeout
	}
	if c.StatementTimeout == 0 {
		c.StatementTimeout = defaultStatementTimeout
	}
	if c.LockTimeout == 0 {
		c.LockTimeout = defaultLockTimeout
	}
	if c.IdleTransactionTimeout == 0 {
		c.IdleTransactionTimeout = defaultIdleTxTimeout
	}
	return c
}

func milliseconds(value time.Duration) string {
	ms := value.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return strconv.FormatInt(ms, 10)
}

func parseMajor(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("empty version")
	}
	if numeric, err := strconv.Atoi(raw); err == nil {
		if numeric >= 10000 {
			return numeric / 10000, nil
		}
		return numeric, nil
	}
	parts := strings.SplitN(raw, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil || major <= 0 {
		return 0, errors.New("version is not numeric")
	}
	return major, nil
}

func parseBoolSetting(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "t", "1", "yes":
		return true, nil
	case "off", "false", "f", "0", "no":
		return false, nil
	default:
		return false, errors.New("expected on or off")
	}
}
