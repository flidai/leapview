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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if _, err := tx.Exec(ctx, `LOCK TABLE platform.api_cursor_signing_keys IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return keyRing{}, err
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.api_cursor_signing_keys WHERE active AND verify_until IS NULL`).Scan(&active); err != nil {
		return keyRing{}, err
	}
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
		if _, err := tx.Exec(ctx, `INSERT INTO platform.api_cursor_signing_keys (key_id,secret,active,created_at) VALUES ($1,$2,true,clock_timestamp())`, id, secret); err != nil {
			return keyRing{}, err
		}
	}
	// Verification retention is bounded by verify_until; remove expired
	// secrets while holding the same table lock so the durable ring does not
	// grow without limit across repeated rotations.
	var ignored int64
	if err := tx.QueryRow(ctx, `SELECT platform.prune_expired_cursor_signing_keys($1)`, 1000).Scan(&ignored); err != nil {
		return keyRing{}, err
	}
	rows, err := tx.Query(ctx, `SELECT key_id,secret,active FROM platform.api_cursor_signing_keys WHERE verify_until IS NULL OR verify_until > clock_timestamp() ORDER BY created_at,key_id`)
	if err != nil {
		return keyRing{}, err
	}
	defer rows.Close()
	keys := make(map[string][]byte)
	current := ""
	for rows.Next() {
		var id string
		var secret []byte
		var active bool
		if err := rows.Scan(&id, &secret, &active); err != nil {
			return keyRing{}, err
		}
		keys[id] = append([]byte(nil), secret...)
		if active {
			current = id
		}
	}
	if err := rows.Err(); err != nil {
		return keyRing{}, err
	}
	if current == "" || len(keys) == 0 {
		return keyRing{}, errors.New("cursor signing key ring has no active key")
	}
	return keyRing{current: current, keys: keys}, nil
}

func (r *Repository) rotateTx(ctx context.Context, tx Tx) (string, error) {
	if _, err := tx.Exec(ctx, `LOCK TABLE platform.api_cursor_signing_keys IN SHARE ROW EXCLUSIVE MODE`); err != nil {
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
	if _, err := tx.Exec(ctx, `UPDATE platform.api_cursor_signing_keys SET active=false, verify_until=clock_timestamp()+$1::interval WHERE active`, verificationRetention.String()); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.api_cursor_signing_keys (key_id,secret,active,created_at) VALUES ($1,$2,true,clock_timestamp())`, id, secret); err != nil {
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
