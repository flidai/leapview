// Package duckdbsession contains the small, process-local pieces shared by
// DuckDB-backed target runtimes and catalog operations.  It deliberately does
// not know about a catalog, a connector role, or a fencing protocol.
package duckdbsession

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// ErrClosed is returned when an operation is attempted after a pinned session
// has been closed.
var ErrClosed = errors.New("pinned DuckDB session is closed")

// PinnedSession owns one database/sql handle and exactly one pinned physical
// connection.  The owner closes the connection before the database handle and
// makes Close idempotent; callers never need to manage either resource.
//
// The connector is supplied by the caller so extension admission and
// role/attach/fencing policy remain with the target-specific package.
type PinnedSession struct {
	db   *sql.DB
	conn *sql.Conn

	mu       sync.RWMutex
	closed   bool
	once     sync.Once
	closeErr error
}

// OpenPinned opens a database/sql handle around connector, limits it to one
// physical connection, and immediately pins that connection.  A nil context
// is treated as context.Background so all callers get the same lifecycle
// semantics.
func OpenPinned(ctx context.Context, connector driver.Connector) (*PinnedSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if connector == nil {
		return nil, errors.New("DuckDB connector is required")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &PinnedSession{db: db, conn: conn}, nil
}

// NewPinned adopts an already-open database/sql handle and connection.  It is
// intentionally narrow and exists for compatibility with tests and callers
// which must supply a connector-created connection themselves.  Production
// callers should use OpenPinned, which enforces the one-connection limits.
func NewPinned(db *sql.DB, conn *sql.Conn) *PinnedSession {
	return &PinnedSession{db: db, conn: conn}
}

func (s *PinnedSession) current() (*sql.Conn, error) {
	if s == nil {
		return nil, ErrClosed
	}
	s.mu.RLock()
	conn := s.conn
	closed := s.closed
	s.mu.RUnlock()
	if conn == nil || closed {
		return nil, ErrClosed
	}
	return conn, nil
}

// Conn returns the owned connection for APIs that need the full SQL Conn
// surface (for example a catalog-upgrade executor).  It returns nil after
// Close; callers that only execute statements should use ExecContext instead.
func (s *PinnedSession) Conn() *sql.Conn {
	conn, _ := s.current()
	return conn
}

func (s *PinnedSession) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	conn, err := s.current()
	if err != nil {
		return nil, err
	}
	return conn.ExecContext(ctx, query, args...)
}

func (s *PinnedSession) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	conn, err := s.current()
	if err != nil {
		return nil, err
	}
	return conn.QueryContext(ctx, query, args...)
}

func (s *PinnedSession) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if s == nil {
		return nil
	}
	conn, err := s.current()
	if err != nil {
		// sql.Row has no constructor for an immediate error. Keep the original
		// *sql.Conn until after Close so the driver returns its ErrConnDone row.
		s.mu.RLock()
		closedConn := s.conn
		s.mu.RUnlock()
		if closedConn != nil {
			return closedConn.QueryRowContext(ctx, query, args...)
		}
		return nil
	}
	return conn.QueryRowContext(ctx, query, args...)
}

// Close releases the pinned connection and its owning database handle. It is
// safe to call repeatedly; every call returns the original close result.
func (s *PinnedSession) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.mu.Lock()
		conn, db := s.conn, s.db
		s.closed = true
		s.db = nil
		s.mu.Unlock()
		if conn != nil {
			s.closeErr = conn.Close()
		}
		if db != nil {
			s.closeErr = errors.Join(s.closeErr, db.Close())
		}
	})
	return s.closeErr
}

// ResourcePolicy describes the process-local settings that bound a DuckDB
// session. AllowedDirectories and DisableExternalAccess are optional because
// target probes and migration attach need external access until their
// target-specific attach phase has completed.
type ResourcePolicy struct {
	MemoryMaxBytes int64
	TempMaxBytes   int64
	MaxThreads     int
	TempDir        string

	AllowedDirectories    []string
	DisableExternalAccess bool
	LockConfiguration     bool
}

// Validate checks the values that are interpolated into DuckDB settings. Path
// canonicalization belongs to the owning domain (DuckLake's DATA_PATH has
// URL-specific rules); this builder only rejects ambiguous whitespace and
// control characters before quoting.
func (p ResourcePolicy) Validate() error {
	if p.MemoryMaxBytes <= 0 || p.TempMaxBytes <= 0 || p.MaxThreads <= 0 {
		return errors.New("positive DuckDB memory, temporary-storage, and thread limits are required")
	}
	if p.TempDir != "" {
		if p.TempDir != strings.TrimSpace(p.TempDir) {
			return errors.New("DuckDB temporary directory is not normalized")
		}
		if containsControl(p.TempDir) {
			return errors.New("DuckDB temporary directory contains a control character")
		}
	}
	for _, directory := range p.AllowedDirectories {
		if directory == "" || directory != strings.TrimSpace(directory) {
			return errors.New("DuckDB allowed directory is not normalized")
		}
		if containsControl(directory) {
			return errors.New("DuckDB allowed directory contains a control character")
		}
	}
	return nil
}

// BoundedStatements builds the common baseline settings. It is used before a
// connector-specific extension/secret/attach sequence.
func (p ResourcePolicy) BoundedStatements() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	statements := []string{
		"SET allow_persistent_secrets = false",
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
		"SET memory_limit = '" + strconv.FormatInt(p.MemoryMaxBytes, 10) + "B'",
		"SET max_temp_directory_size = '" + strconv.FormatInt(p.TempMaxBytes, 10) + "B'",
		"SET threads = " + strconv.Itoa(p.MaxThreads),
	}
	if p.TempDir != "" {
		statements = append(statements, "SET temp_directory = '"+sqlLiteral(p.TempDir)+"'")
	}
	return statements, nil
}

// SecurityStatements builds the optional post-attach lockdown settings. It
// intentionally does not require resource limits so callers can apply the
// same allow-list/lock after an opaque credential bootstrap.
func (p ResourcePolicy) SecurityStatements() ([]string, error) {
	if p.TempDir != "" && containsControl(p.TempDir) {
		return nil, errors.New("DuckDB temporary directory contains a control character")
	}
	for _, directory := range p.AllowedDirectories {
		if directory == "" || directory != strings.TrimSpace(directory) || containsControl(directory) {
			return nil, errors.New("DuckDB allowed directory is invalid")
		}
	}
	statements := make([]string, 0, 3)
	if len(p.AllowedDirectories) > 0 {
		quoted := make([]string, len(p.AllowedDirectories))
		for i, directory := range p.AllowedDirectories {
			quoted[i] = "'" + sqlLiteral(directory) + "'"
		}
		statements = append(statements, "SET allowed_directories = ["+strings.Join(quoted, ", ")+"]")
	}
	if p.DisableExternalAccess {
		statements = append(statements, "SET enable_external_access = false")
	}
	if p.LockConfiguration {
		statements = append(statements, "SET lock_configuration = true")
	}
	return statements, nil
}

// Statements combines the bounded baseline and optional lockdown settings.
func (p ResourcePolicy) Statements() ([]string, error) {
	bounded, err := p.BoundedStatements()
	if err != nil {
		return nil, err
	}
	security, err := p.SecurityStatements()
	if err != nil {
		return nil, err
	}
	return append(bounded, security...), nil
}

func containsControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func sqlLiteral(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}
