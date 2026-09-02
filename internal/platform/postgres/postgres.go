// Package postgres contains the capability-neutral PostgreSQL connection
// boundary. It deliberately owns connection setup and readiness checks only;
// schema, migrations, and repositories remain owned by their capabilities.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	platformdb "github.com/flidai/leapview/internal/platform/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

// Pool is a bounded pgxpool with an acquisition deadline. The embedded pool
// is intentionally not exposed directly so callers use the same bounded
// Acquire contract for every capability.
type Pool struct {
	pool           *pgxpool.Pool
	acquireTimeout time.Duration
	config         Config
}

// SchemaRevision is the platform-owned migration readiness record. Generated
// sqlc rows remain internal to this package.
type SchemaRevision struct {
	Revision    int64
	MigrationID string
	Checksum    string
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
	if ctx == nil {
		ctx = context.Background()
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

// Ping checks pool reachability after startup validation.
func (p *Pool) Ping(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return errors.New("postgres pool is nil")
	}
	return p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		return conn.Conn().Ping(ctx)
	})
}

// SchemaRevision returns the exact durable identity for one baseline
// revision, allowing application composition to fail closed on checksum drift.
func (p *Pool) SchemaRevision(ctx context.Context, revision int64) (SchemaRevision, error) {
	if p == nil || p.pool == nil {
		return SchemaRevision{}, errors.New("postgres pool is nil")
	}
	var record platformdb.GetSchemaRevisionRow
	err := p.AcquireFunc(ctx, func(conn *pgxpool.Conn) error {
		var err error
		record, err = platformdb.New(conn).GetSchemaRevision(ctx, revision)
		return err
	})
	if err != nil {
		return SchemaRevision{}, err
	}
	return SchemaRevision{
		Revision:    record.Revision,
		MigrationID: record.MigrationID,
		Checksum:    record.Checksum,
	}, nil
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
		return nil, fmt.Errorf("parse PostgreSQL URL: %w", err)
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

// ValidateProbeSQL is the single startup probe used to establish the server
// capability tuple. Keeping it exported lets conformance tests assert the
// exact probe through a fake without requiring a live database.
const ValidateProbeSQL = `SELECT current_setting('server_version_num'), current_user, current_setting('default_transaction_read_only'), pg_is_in_recovery()`

// Probe is the read-only query seam used by Validate.
type Probe interface {
	QueryRow(context.Context, string, ...any) pgx.Row
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
	var versionNum, role, readOnly string
	var recovery bool
	if err := probe.QueryRow(ctx, ValidateProbeSQL).Scan(&versionNum, &role, &readOnly, &recovery); err != nil {
		return fmt.Errorf("probe PostgreSQL capabilities: %w", err)
	}
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
			return fmt.Errorf("parse PostgreSQL URL for TLS validation: %w", err)
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
