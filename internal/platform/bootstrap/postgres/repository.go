// Package postgres owns the small platform bootstrap authority required by
// native Admin: one immutable instance identity and environment binding.
// Access initialization markers remain owned by internal/access.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/flidai/leapview/internal/platform/instanceidentity"
	platformmigrations "github.com/flidai/leapview/internal/platform/postgres/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is caller-owned. Repository methods never commit or roll back it.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

type Repository struct{ db DBTX }

var (
	ErrInvalid     = errors.New("invalid platform bootstrap value")
	ErrConflict    = errors.New("platform bootstrap value conflicts with existing identity")
	ErrNotFound    = errors.New("platform bootstrap value not found")
	ErrEnvironment = errors.New("instance environment conflict")
)

var environmentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// SchemaSQL returns the sole authored migration for this capability. The
// migration package owns revision ordering and checksum recording.
func SchemaSQL() string { return platformmigrations.PlatformBootstrapAuthoritySQL() }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_, err := tx.Exec(ctx, SchemaSQL())
	return err
}

func New(db DBTX) *Repository { return &Repository{db: db} }

func NewRepository(db DBTX) *Repository { return New(db) }

func (r *Repository) WithTx(tx Tx) *Repository {
	if tx == nil {
		return &Repository{}
	}
	return &Repository{db: tx}
}

func (r *Repository) valid() bool { return r != nil && r.db != nil }

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// InstanceID returns the durable identity, generating it once when absent.
// Concurrent callers converge on the singleton row inserted by the winner.
func (r *Repository) InstanceID(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	var id string
	err := r.db.QueryRow(ctx, `SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1`).Scan(&id)
	if err == nil {
		return validateStoredID(id)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate instance identity: %w", err)
	}
	candidate := "lvinst_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	if _, err := r.db.Exec(ctx, `
		INSERT INTO platform.instance_identity(singleton_id, instance_id)
		VALUES (1, $1) ON CONFLICT (singleton_id) DO NOTHING`, candidate); err != nil {
		return "", err
	}
	if err := r.db.QueryRow(ctx, `SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1`).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return validateStoredID(id)
}

func (r *Repository) EnsureInstanceID(ctx context.Context, id string) error {
	if !r.valid() {
		return ErrInvalid
	}
	if id == "" || id != strings.TrimSpace(id) || !instanceidentity.Valid(id) {
		return fmt.Errorf("%w: instance ID is invalid", ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	if _, err := r.db.Exec(ctx, `
		INSERT INTO platform.instance_identity(singleton_id, instance_id)
		VALUES (1, $1) ON CONFLICT (singleton_id) DO NOTHING`, id); err != nil {
		return err
	}
	var stored string
	if err := r.db.QueryRow(ctx, `SELECT instance_id FROM platform.instance_identity WHERE singleton_id = 1`).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if stored != id {
		return fmt.Errorf("%w: instance identity is already bound to %q", ErrConflict, stored)
	}
	return nil
}

func (r *Repository) InstanceIDTx(ctx context.Context, tx Tx) (string, error) {
	if tx == nil {
		return "", ErrInvalid
	}
	return (&Repository{db: tx}).InstanceID(ctx)
}

func (r *Repository) EnsureInstanceIDTx(ctx context.Context, tx Tx, id string) error {
	if tx == nil {
		return ErrInvalid
	}
	return (&Repository{db: tx}).EnsureInstanceID(ctx, id)
}

func (r *Repository) InstanceEnvironment(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	var environment string
	err := r.db.QueryRow(contextOrBackground(ctx), `SELECT environment FROM platform.instance_environment WHERE singleton_id = 1`).Scan(&environment)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return environment, nil
}

func (r *Repository) ExistingEnvironment(ctx context.Context) (string, bool, error) {
	environment, err := r.InstanceEnvironment(ctx)
	if errors.Is(err, ErrNotFound) {
		return "", false, nil
	}
	return environment, err == nil, err
}

func (r *Repository) BindInstanceEnvironment(ctx context.Context, environment string) error {
	if !r.valid() {
		return ErrInvalid
	}
	if environment != strings.TrimSpace(environment) || !environmentPattern.MatchString(environment) || len(environment) > 255 {
		return fmt.Errorf("%w: environment is invalid", ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	if _, err := r.db.Exec(ctx, `
		INSERT INTO platform.instance_environment(singleton_id, environment)
		VALUES (1, $1) ON CONFLICT (singleton_id) DO NOTHING`, environment); err != nil {
		return err
	}
	stored, err := r.InstanceEnvironment(ctx)
	if err != nil {
		return err
	}
	if stored != environment {
		return fmt.Errorf("%w: instance is bound to environment %q, not %q", ErrEnvironment, stored, environment)
	}
	return nil
}

func (r *Repository) BindEnvironment(ctx context.Context, environment string) error {
	return r.BindInstanceEnvironment(ctx, environment)
}

func (r *Repository) BindInstanceEnvironmentTx(ctx context.Context, tx Tx, environment string) error {
	if tx == nil {
		return ErrInvalid
	}
	return (&Repository{db: tx}).BindInstanceEnvironment(ctx, environment)
}

func validateStoredID(id string) (string, error) {
	if !instanceidentity.Valid(id) {
		return "", fmt.Errorf("%w: stored instance identity is invalid", ErrInvalid)
	}
	return id, nil
}
