package platform

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/db"
	"github.com/flidai/leapview/internal/platform/filesystem"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	databaseFileMode = securefs.PrivateFileMode
)

type Store struct {
	db        *sql.DB
	q         *db.Queries
	closeOnce sync.Once
	closeErr  error
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	conn, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(0)
	store := &Store{db: conn, q: db.New(conn)}
	if err := store.migrate(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := chmodDatabaseFile(path); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		// Normal operation intentionally keeps no idle SQLite connections. At
		// shutdown, retain the barrier connection so Ping cannot return while a
		// database/sql opener is still completing in the background. DB.Close
		// then closes that sole idle connection before this method returns.
		s.db.SetMaxIdleConns(1)
		if err := s.db.PingContext(context.Background()); err != nil {
			s.closeErr = fmt.Errorf("drain platform db connections: %w", err)
		}
		s.closeErr = errors.Join(s.closeErr, s.db.Close())
	})
	return s.closeErr
}

func (s *Store) SQLDB() *sql.DB {
	return s.db
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("platform store is not open")
	}
	return s.db.PingContext(ctx)
}

func chmodDatabaseFile(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	if strings.Contains(path, "?") {
		path = strings.SplitN(path, "?", 2)[0]
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, databaseFileMode); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	return s.q.GetPlatformSetting(ctx, key)
}

func (s *Store) UpsertSetting(ctx context.Context, key, value string) error {
	return s.q.UpsertPlatformSetting(ctx, db.UpsertPlatformSettingParams{Key: key, Value: value})
}

func (s *Store) migrate(ctx context.Context) error {
	for _, stmt := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, s.db, "migrations"); err != nil {
		return fmt.Errorf("migrating platform db: %w", err)
	}
	return nil
}
