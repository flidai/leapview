// Package postgres persists and activates the process-wide cursor-signing
// key ring on PostgreSQL. The repository never stores cursor payloads.
package postgres

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	cursordb "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native PostgreSQL surface accepted by this capability.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

const verificationRetention = 24 * time.Hour

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return errors.New("cursor signing schema transaction is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception:schema-ddl. schema.sql owns the capability DDL, guards,
	// view, and grants; migration callers retain transaction ownership.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

type Repository struct{ db DBTX }

type keyRing struct {
	current string
	keys    map[string][]byte
}

func NewRepository(db DBTX) *Repository { return &Repository{db: db} }

// Configure loads the durable ring and installs it in cursorsigning. The
// first caller atomically creates a random active key; later callers only
// reload the existing ring. Configuration commits its durable changes before
// publishing the process-wide ring.
func Configure(ctx context.Context, db DBTX) error {
	return NewRepository(db).Configure(ctx)
}

func (r *Repository) Configure(ctx context.Context) error {
	if r == nil || isNilDB(r.db) {
		return errors.New("cursor signing repository is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return errors.New("cursor signing configuration requires a transaction-capable PostgreSQL DB")
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	ring, err := r.configureTx(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// Install only after commit. A failed transaction must never publish a
	// key ring that is absent from durable storage.
	return cursorsigning.Configure(ring.current, ring.keys)
}

func isNilDB(db DBTX) bool {
	if db == nil {
		return true
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// Rotate creates and activates a fresh key while retaining prior keys for
// verification. The activation and retirement happen under one row lock so a
// caller never observes two active signing keys. The returned ID is suitable
// for operational audit logs.
func (r *Repository) Rotate(ctx context.Context) (string, error) {
	if r == nil || isNilDB(r.db) {
		return "", errors.New("cursor signing repository is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if b, ok := r.db.(beginner); ok {
		tx, err := b.Begin(ctx)
		if err != nil {
			return "", err
		}
		id, err := r.rotateTx(ctx, tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		if err := r.Configure(ctx); err != nil {
			return "", err
		}
		return id, nil
	}
	return "", errors.New("cursor signing rotation requires a transaction-capable PostgreSQL DB")
}

func (r *Repository) configureTx(ctx context.Context, tx Tx) (keyRing, error) {
	if err := cursordb.New(tx).LockCursorSigningKeys(ctx); err != nil {
		return keyRing{}, err
	}
	var active int
	count, err := cursordb.New(tx).CountActiveCursorSigningKeys(ctx)
	if err != nil {
		return keyRing{}, err
	}
	active = int(count)
	if active == 0 {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return keyRing{}, fmt.Errorf("generate cursor signing key: %w", err)
		}
		idSuffix, err := randomID()
		if err != nil {
			return keyRing{}, err
		}
		id := "v1-" + idSuffix
		if err := cursordb.New(tx).InsertActiveCursorSigningKey(ctx, cursordb.InsertActiveCursorSigningKeyParams{KeyID: id, Secret: secret}); err != nil {
			return keyRing{}, err
		}
	}
	// Verification retention is bounded by verify_until; remove expired
	// secrets while holding the same table lock so the durable ring does not
	// grow without limit across repeated rotations.
	if _, err := cursordb.New(tx).PruneExpiredCursorSigningKeys(ctx, 1000); err != nil {
		return keyRing{}, err
	}
	rows, err := cursordb.New(tx).ListVerifiableCursorSigningKeys(ctx)
	if err != nil {
		return keyRing{}, err
	}
	keys := make(map[string][]byte)
	current := ""
	for _, row := range rows {
		keys[row.KeyID] = append([]byte(nil), row.Secret...)
		if row.Active {
			current = row.KeyID
		}
	}
	if current == "" || len(keys) == 0 {
		return keyRing{}, errors.New("cursor signing key ring has no active key")
	}
	return keyRing{current: current, keys: keys}, nil
}

func (r *Repository) rotateTx(ctx context.Context, tx Tx) (string, error) {
	if err := cursordb.New(tx).LockCursorSigningKeys(ctx); err != nil {
		return "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate cursor signing key: %w", err)
	}
	idSuffix, err := randomID()
	if err != nil {
		return "", err
	}
	id := "v1-" + idSuffix
	if err := cursordb.New(tx).RetireActiveCursorSigningKeys(ctx, pgtype.Interval{Microseconds: verificationRetention.Microseconds(), Valid: true}); err != nil {
		return "", err
	}
	if err := cursordb.New(tx).InsertActiveCursorSigningKey(ctx, cursordb.InsertActiveCursorSigningKeyParams{KeyID: id, Secret: secret}); err != nil {
		return "", err
	}
	return id, nil
}

func randomID() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate cursor signing key id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Ensure pgx is part of the native contract and prevent accidental
// database/sql adapters from being introduced in this package.
var _ pgx.Tx = (pgx.Tx)(nil)
