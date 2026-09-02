package ducklake

// PostgreSQL DuckLake catalog-upgrade session construction.
//
// Catalog upgrades run through an explicitly owned DuckDB connection.  The
// session deliberately stops after loading the admitted DuckLake extension and
// provisioning target-owned credentials; the fenced postgres coordinator then
// performs the migration ATTACH/DETACH sequence through Conn().  Keeping the
// attachment outside this constructor prevents an ordinary runtime path from
// accidentally enabling automatic migration.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
)

// ErrPostgresCatalogUpgradeSession indicates an invalid or unavailable
// dedicated catalog-upgrade session.
var ErrPostgresCatalogUpgradeSession = errors.New("PostgreSQL DuckLake catalog upgrade session is invalid")

// PostgresCatalogUpgradeSessionConfig describes the bounded local DuckDB
// session used by the fenced catalog-upgrade coordinator.  PostgreSQL and
// object-store credentials are intentionally opaque: callers provide a
// CredentialBootstrap that provisions them on the connection, while this
// package never parses a URL or accepts a database pool.
type PostgresCatalogUpgradeSessionConfig struct {
	DataPath            string
	TempDir             string
	MemoryMaxBytes      int64
	TempMaxBytes        int64
	MaxThreads          int
	ExtensionAdmission  extension.Admission
	CredentialBootstrap CredentialBootstrap
}

// PostgresCatalogUpgradeSession owns one DuckDB SQL connection and the
// connector/database that created it.  Conn is suitable for both Exec and
// Query fields of postgres.SQLCatalogExecutor.  Callers must Close the
// session after the fenced operation; Close is idempotent.
type PostgresCatalogUpgradeSession struct {
	db   *sql.DB
	conn *sql.Conn
	once sync.Once
	err  error
}

// Validate checks the upgrade-session resource and admission contract without
// opening a DuckDB connection.  A data path is required because it forms the
// canonical external-access allow-list together with the optional private
// temporary directory.
func (c PostgresCatalogUpgradeSessionConfig) Validate() error {
	if c.MemoryMaxBytes <= 0 || c.TempMaxBytes <= 0 || c.MaxThreads <= 0 {
		return fmt.Errorf("%w: positive memory, temporary-storage, and thread limits are required", ErrPostgresCatalogUpgradeSession)
	}
	if strings.TrimSpace(c.DataPath) == "" {
		return fmt.Errorf("%w: canonical DuckLake data path is required", ErrPostgresCatalogUpgradeSession)
	}
	if strings.ContainsAny(c.DataPath, "\x00\r\n") {
		return fmt.Errorf("%w: canonical DuckLake data path contains a control character", ErrPostgresCatalogUpgradeSession)
	}
	if _, err := CanonicalDataPath(c.DataPath); err != nil {
		return fmt.Errorf("%w: canonical DuckLake data path is invalid", ErrPostgresCatalogUpgradeSession)
	}
	if strings.TrimSpace(c.TempDir) != "" && c.TempDir != strings.TrimSpace(c.TempDir) {
		return fmt.Errorf("%w: temporary directory is not normalized", ErrPostgresCatalogUpgradeSession)
	}
	if strings.ContainsAny(c.TempDir, "\x00\r\n") {
		return fmt.Errorf("%w: temporary directory contains a control character", ErrPostgresCatalogUpgradeSession)
	}
	if c.ExtensionAdmission == nil {
		return fmt.Errorf("%w: extension admission is required", ErrPostgresCatalogUpgradeSession)
	}
	if c.CredentialBootstrap == nil {
		return fmt.Errorf("%w: credential bootstrap is required", ErrPostgresCatalogUpgradeSession)
	}
	return nil
}

// OpenPostgresCatalogUpgradeSession opens and pins exactly one in-memory
// DuckDB connection.  The connector initializer captures the caller context
// because DuckDB's callback does not receive one; extension admission,
// credential bootstrap, and every initializer statement therefore observe the
// same cancellation/deadline boundary.
func OpenPostgresCatalogUpgradeSession(ctx context.Context, c PostgresCatalogUpgradeSessionConfig) (*PostgresCatalogUpgradeSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogUpgradeSession, err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	canonicalDataPath, err := CanonicalDataPath(c.DataPath)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical DuckLake data path", ErrPostgresCatalogUpgradeSession)
	}
	c.DataPath = canonicalDataPath
	if strings.TrimSpace(c.TempDir) != "" {
		tempDir, err := filepath.Abs(c.TempDir)
		if err != nil {
			return nil, fmt.Errorf("%w: canonical temporary directory", ErrPostgresCatalogUpgradeSession)
		}
		c.TempDir = filepath.Clean(tempDir)
		if err := securefs.EnsurePrivateDir(c.TempDir); err != nil {
			return nil, fmt.Errorf("%w: prepare temporary directory", ErrPostgresCatalogUpgradeSession)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogUpgradeSession, err)
	}
	allowedDirectories, err := upgradeAllowedDirectories(c)
	if err != nil {
		return nil, err
	}
	initCtx := ctx
	connector, err := duckdb.NewConnector(":memory:", func(execer driver.ExecerContext) error {
		if err := initCtx.Err(); err != nil {
			return fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		for _, statement := range upgradeResourceStatements(c) {
			if _, err := execer.ExecContext(initCtx, statement, nil); err != nil {
				return fmt.Errorf("%w: configure bounded DuckDB upgrade session: %w", ErrPostgresCatalogUpgradeSession, err)
			}
		}
		admitted, err := c.ExtensionAdmission.AdmitExtension(initCtx, "ducklake")
		if err != nil {
			return fmt.Errorf("%w: admit DuckLake extension: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		if err := validateAdmittedExtension(admitted, "ducklake"); err != nil {
			return fmt.Errorf("%w: DuckLake extension admission: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		if strings.ContainsAny(admitted.Path, "\x00\r\n") {
			return fmt.Errorf("%w: DuckLake extension path contains a control character", ErrPostgresCatalogUpgradeSession)
		}
		if _, err := execer.ExecContext(initCtx, "LOAD '"+sqlLiteral(admitted.Path)+"'", nil); err != nil {
			return fmt.Errorf("%w: load DuckLake extension: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		if err := c.CredentialBootstrap(initCtx, execer); err != nil {
			return fmt.Errorf("%w: bootstrap DuckLake connector credentials: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		// CredentialBootstrap is intentionally opaque and receives an executor
		// so it can load target-owned scanners/secrets. Re-apply the bounded
		// resource policy after that callback to ensure its setup cannot weaken
		// the limits before configuration is locked below.
		for _, statement := range upgradeResourceLimitStatements(c) {
			if _, err := execer.ExecContext(initCtx, statement, nil); err != nil {
				return fmt.Errorf("%w: restore bounded DuckDB upgrade session policy: %w", ErrPostgresCatalogUpgradeSession, err)
			}
		}
		// Do not attach a catalog here. The coordinator's SQLCatalogExecutor
		// performs the explicit migration ATTACH with AUTOMATIC_MIGRATION=true
		// only after it has acquired and renewed the durable fences.
		if _, err := execer.ExecContext(initCtx, allowedDirectories, nil); err != nil {
			return fmt.Errorf("%w: restrict DuckDB external directories: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		if _, err := execer.ExecContext(initCtx, "SET lock_configuration = true", nil); err != nil {
			return fmt.Errorf("%w: lock DuckDB upgrade configuration: %w", ErrPostgresCatalogUpgradeSession, err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create connector: %w", ErrPostgresCatalogUpgradeSession, err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: initialize connection: %w", ErrPostgresCatalogUpgradeSession, err)
	}
	return &PostgresCatalogUpgradeSession{db: db, conn: conn}, nil
}

// upgradeResourceStatements returns the positive, bounded DuckDB limits used
// by catalog migration.  The operation has no pool and never borrows a
// runtime Environment connection.
func upgradeResourceStatements(c PostgresCatalogUpgradeSessionConfig) []string {
	statements := []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
	}
	return append(statements, upgradeResourceLimitStatements(c)...)
}

// upgradeResourceLimitStatements contains settings that remain writable by a
// credential bootstrap (for example, a provider may install its own secret
// policy). Reapplying only these limits after the opaque callback preserves
// its one-time security setup while ensuring final resource bounds cannot be
// weakened.
func upgradeResourceLimitStatements(c PostgresCatalogUpgradeSessionConfig) []string {
	statements := []string{
		fmt.Sprintf("SET memory_limit = '%dB'", c.MemoryMaxBytes),
		fmt.Sprintf("SET max_temp_directory_size = '%dB'", c.TempMaxBytes),
		fmt.Sprintf("SET threads = %d", c.MaxThreads),
	}
	if strings.TrimSpace(c.TempDir) != "" {
		statements = append(statements, "SET temp_directory = '"+sqlLiteral(c.TempDir)+"'")
	}
	return statements
}

func upgradeAllowedDirectories(c PostgresCatalogUpgradeSessionConfig) (string, error) {
	// Keep the same canonicalization used by the dedicated physical
	// maintenance session without importing or invoking its role validator.
	allowed, err := maintenanceAllowedDirectories(PostgresCatalogMaintenanceSessionConfig{DataPath: c.DataPath, TempDir: c.TempDir})
	if err != nil {
		return "", fmt.Errorf("%w: canonical allowed directories: %w", ErrPostgresCatalogUpgradeSession, err)
	}
	return allowed, nil
}

// Conn returns the session-owned *sql.Conn.  It is intentionally not a pool
// and remains valid until Close is called.
func (s *PostgresCatalogUpgradeSession) Conn() *sql.Conn {
	if s == nil {
		return nil
	}
	return s.conn
}

// Close releases the pinned connection and connector.  Repeated calls return
// the same result and never close either resource more than once.
func (s *PostgresCatalogUpgradeSession) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.conn != nil {
			s.err = s.conn.Close()
		}
		if s.db != nil {
			s.err = errors.Join(s.err, s.db.Close())
		}
	})
	return s.err
}
