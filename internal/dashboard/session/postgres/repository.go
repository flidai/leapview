// Package postgres implements the native dashboard session authority.
package postgres

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/dashboard/session"
	db "github.com/flidai/leapview/internal/dashboard/session/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var ErrUnavailable = errors.New("dashboard PostgreSQL session store is unavailable")

const (
	defaultExpiredBatch = 1000
	maxExpiredBatch     = 10000
)

type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrUnavailable
	}
	_, err := tx.Exec(ctxOrBackground(ctx), schemaSQL) // sqlc-exception: schema-ddl
	return err
}

// Store is a native PostgreSQL implementation of session.Store.
type Store struct {
	db     DBTX
	ttl    time.Duration
	clock  func() time.Time
	native bool
}

func New(db DBTX) (*Store, error) { return NewWithTTL(db, 5*time.Minute) }

func NewWithTTL(db DBTX, ttl time.Duration) (*Store, error) {
	if db == nil {
		return nil, ErrUnavailable
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("dashboard session TTL must be positive")
	}
	if _, ok := db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	}); !ok {
		return nil, fmt.Errorf("dashboard session PostgreSQL handle must support transactions")
	}
	return &Store{db: db, ttl: ttl, clock: time.Now, native: true}, nil
}

// IsNative reports whether the store came from NewWithTTL rather than a
// zero-value or test-constructed struct. The marker is private and cannot be
// forged outside this package.
func (s *Store) IsNative() bool { return s != nil && s.native }

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	_, err := db.New(s.db).Ping(ctxOrBackground(ctx))
	return err
}

func (s *Store) Create(ctx context.Context, key session.Key, state session.State) (session.Record, error) {
	if s == nil || s.db == nil {
		return session.Record{}, ErrUnavailable
	}
	if err := key.Validate(); err != nil {
		return session.Record{}, err
	}
	keyJSON, stateJSON, err := encode(key, state)
	if err != nil {
		return session.Record{}, err
	}
	expires := s.expiry()
	changed, err := db.New(s.db).Create(ctxOrBackground(ctx), db.CreateParams{ID: key.ID(), ProjectID: key.ProjectID.String(), PublicationID: key.PublicationID, PrincipalOrClient: key.PrincipalOrClient, DashboardID: key.DashboardID.String(), ServingStateID: key.ServingStateID, StreamInstanceID: key.StreamInstanceID, KeyJson: []byte(keyJSON), StateJson: []byte(stateJSON), ExpiresAt: expires})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return session.Record{}, session.ErrConflict
		}
		return session.Record{}, err
	}
	if changed != 1 {
		return session.Record{}, session.ErrConflict
	}
	return session.Record{Key: key, Version: 1, State: state, ExpiresAt: expires}, nil
}

func (s *Store) Load(ctx context.Context, key session.Key) (session.Record, error) {
	if s == nil || s.db == nil {
		return session.Record{}, ErrUnavailable
	}
	row, err := db.New(s.db).GetActive(ctxOrBackground(ctx), db.GetActiveParams{ID: key.ID(), Now: s.clock().UTC()})
	if errors.Is(err, pgx.ErrNoRows) {
		return session.Record{}, session.ErrNotFound
	}
	if err != nil {
		return session.Record{}, err
	}
	return decodeRecord(key, row.ProjectID, row.PublicationID, row.PrincipalOrClient, row.DashboardID, row.ServingStateID, row.StreamInstanceID, row.KeyJson, row.StateJson, row.Version, row.ExpiresAt)
}

func (s *Store) CompareAndSwap(ctx context.Context, key session.Key, version uint64, state session.State) (session.Record, error) {
	if s == nil || s.db == nil {
		return session.Record{}, ErrUnavailable
	}
	_, stateJSON, err := encode(key, state)
	if err != nil {
		return session.Record{}, err
	}
	expires := s.expiry()
	changed, err := db.New(s.db).CompareAndSwap(ctxOrBackground(ctx), db.CompareAndSwapParams{StateJson: []byte(stateJSON), ExpiresAt: expires, ID: key.ID(), Version: int64(version), Now: s.clock().UTC()})
	if err != nil {
		return session.Record{}, err
	}
	if changed != 1 {
		if _, loadErr := s.Load(ctx, key); loadErr == nil {
			return session.Record{}, session.ErrConflict
		}
		return session.Record{}, session.ErrNotFound
	}
	return session.Record{Key: key, Version: version + 1, State: state, ExpiresAt: expires}, nil
}

func (s *Store) Touch(ctx context.Context, key session.Key) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	changed, err := db.New(s.db).Touch(ctxOrBackground(ctx), db.TouchParams{ExpiresAt: s.expiry(), ID: key.ID(), Now: s.clock().UTC()})
	if err != nil {
		return err
	}
	if changed != 1 {
		return session.ErrNotFound
	}
	return nil
}

func (s *Store) DeleteExpired(ctx context.Context) error {
	_, err := s.DeleteExpiredBatch(ctx, defaultExpiredBatch)
	return err
}

// DeleteExpiredBatch removes at most batchSize expired sessions and returns
// the number removed. Maintenance callers should repeat until the count is
// less than batchSize; request paths never invoke this operation.
func (s *Store) DeleteExpiredBatch(ctx context.Context, batchSize int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrUnavailable
	}
	if batchSize <= 0 || batchSize > maxExpiredBatch {
		return 0, fmt.Errorf("dashboard session expiry batch size must be between 1 and %d", maxExpiredBatch)
	}
	return db.New(s.db).DeleteExpired(ctxOrBackground(ctx), db.DeleteExpiredParams{Now: s.clock().UTC(), BatchSize: int32(batchSize)})
}

func (s *Store) expiry() time.Time { return s.clock().UTC().Add(s.ttl) }

func encode(key session.Key, state session.State) (string, string, error) {
	k, err := json.Marshal(key)
	if err != nil {
		return "", "", err
	}
	v, err := json.Marshal(state)
	if err != nil {
		return "", "", err
	}
	return string(k), string(v), nil
}

func decodeRecord(key session.Key, projectID, publicationID, principalOrClient, dashboardID, servingStateID, streamInstanceID, keyJSON, stateJSON string, version int64, expires time.Time) (session.Record, error) {
	if projectID != key.ProjectID.String() || publicationID != key.PublicationID || principalOrClient != key.PrincipalOrClient || dashboardID != key.DashboardID.String() || servingStateID != key.ServingStateID || streamInstanceID != key.StreamInstanceID {
		return session.Record{}, fmt.Errorf("dashboard session relational identity mismatch")
	}
	var storedKey session.Key
	if err := json.Unmarshal([]byte(keyJSON), &storedKey); err != nil {
		return session.Record{}, fmt.Errorf("decode dashboard session key: %w", err)
	}
	if storedKey != key {
		return session.Record{}, fmt.Errorf("dashboard session key digest collision")
	}
	var state session.State
	if err := json.Unmarshal([]byte(stateJSON), &state); err != nil {
		return session.Record{}, fmt.Errorf("decode dashboard session state: %w", err)
	}
	return session.Record{Key: storedKey, Version: uint64(version), State: state, ExpiresAt: expires}, nil
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ session.Store = (*Store)(nil)
