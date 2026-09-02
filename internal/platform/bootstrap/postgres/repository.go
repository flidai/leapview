// Package postgres persists the small set of platform values required before
// the rest of the control plane can serve traffic.  It intentionally owns no
// sibling capability storage and never opens a second database transaction.
package postgres

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	bootstrapdb "github.com/flidai/leapview/internal/platform/bootstrap/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/instanceidentity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DBTX is the native pgx surface accepted by reads and by caller-owned
// transactions.  pgx pools, connections, and transactions satisfy it.
type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Tx is a strict caller-owned transaction. Repository methods never commit or
// roll back this value, allowing bootstrap state to share a larger mutation.
type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

// Repository owns only platform bootstrap tables.
type Repository struct{ db DBTX }

// ProjectClaim is the platform-neutral representation of the singleton
// instance claim. Capability adapters (for example deployment) translate it
// into their domain contract without making platform depend on sibling code.
type ProjectClaim struct {
	ProjectID   string
	Environment string
	ClaimedBy   string
	ClaimedAt   time.Time
}

type ProjectClaimInput = ProjectClaim

var resourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

func (claim ProjectClaim) Validate() error {
	if !resourcePattern.MatchString(claim.ProjectID) || claim.ProjectID != strings.TrimSpace(claim.ProjectID) || len(claim.ProjectID) > 255 {
		return fmt.Errorf("%w: project ID is invalid", ErrInvalid)
	}
	if !resourcePattern.MatchString(claim.Environment) || claim.Environment != strings.TrimSpace(claim.Environment) || len(claim.Environment) > 255 {
		return fmt.Errorf("%w: environment is invalid", ErrInvalid)
	}
	if claim.ClaimedBy == "" || claim.ClaimedBy != strings.TrimSpace(claim.ClaimedBy) || len(claim.ClaimedBy) > 256 || strings.IndexFunc(claim.ClaimedBy, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: actor is invalid", ErrInvalid)
	}
	if claim.ClaimedAt.IsZero() {
		return fmt.Errorf("%w: claim time is required", ErrInvalid)
	}
	return nil
}

var (
	ErrInvalid     = errors.New("invalid platform bootstrap value")
	ErrConflict    = errors.New("platform bootstrap value conflicts with existing identity")
	ErrNotFound    = errors.New("platform bootstrap value not found")
	ErrEnvironment = errors.New("instance environment conflict")
)

//go:embed schema.sql
var schemaSQL string

// SchemaSQL returns the capability-owned DDL, including immutable guards and
// least-privilege role grants.
func SchemaSQL() string { return schemaSQL }

// ApplySchema executes this capability's DDL in a caller-owned transaction.
func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	_, err := tx.Exec(contextOrBackground(ctx), schemaSQL) // sqlc-exception: schema-ddl
	return err
}

func New(db DBTX) *Repository           { return &Repository{db: db} }
func NewRepository(db DBTX) *Repository { return New(db) }

// WithTx returns a repository sharing the caller's transaction. The caller
// retains commit and rollback ownership.
func (r *Repository) WithTx(tx Tx) *Repository {
	if r == nil {
		return &Repository{}
	}
	return &Repository{db: tx}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (r *Repository) valid() bool { return r != nil && r.db != nil }

// GetSetting reads a startup setting by canonical key.
func (r *Repository) GetSetting(ctx context.Context, key string) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	key, err := normalizeSettingKey(key)
	if err != nil {
		return "", err
	}
	value, err := bootstrapdb.New(r.db).GetSetting(contextOrBackground(ctx), key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

// UpsertSetting writes a mutable startup setting. It is deliberately not an
// identity upsert: settings are operational configuration, while identity and
// claims use immutable insert-if-missing methods below.
func (r *Repository) UpsertSetting(ctx context.Context, key, value string) error {
	if !r.valid() {
		return ErrInvalid
	}
	key, value, err := normalizeSetting(key, value)
	if err != nil {
		return err
	}
	return bootstrapdb.New(r.db).UpsertSetting(contextOrBackground(ctx), bootstrapdb.UpsertSettingParams{Key: key, Value: value})
}

// SetSetting is an alias retained for callers using setter vocabulary.
func (r *Repository) SetSetting(ctx context.Context, key, value string) error {
	return r.UpsertSetting(ctx, key, value)
}

// InsertSettingIfMissing installs a setting only when absent and reports
// whether this call won the insert race.
func (r *Repository) InsertSettingIfMissing(ctx context.Context, key, value string) (bool, error) {
	if !r.valid() {
		return false, ErrInvalid
	}
	key, value, err := normalizeSetting(key, value)
	if err != nil {
		return false, err
	}
	rows, err := bootstrapdb.New(r.db).InsertSettingIfMissing(contextOrBackground(ctx), bootstrapdb.InsertSettingIfMissingParams{Key: key, Value: value})
	return rows == 1, err
}

// InstanceID returns the durable instance identity, generating it once when
// this is the first process to initialize the database. Concurrent callers
// converge on the row inserted by whichever process wins the primary-key
// race.
func (r *Repository) InstanceID(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	return instanceID(ctx, r.db)
}

// EnsureInstanceID inserts a caller-supplied identity if absent. A different
// identity is an immutable conflict; exact replays are successful.
func (r *Repository) EnsureInstanceID(ctx context.Context, id string) error {
	if !r.valid() {
		return ErrInvalid
	}
	return ensureInstanceID(contextOrBackground(ctx), r.db, id)
}

// InstanceIDTx is the caller-owned transaction form of InstanceID. The
// generated identity is persisted in tx and becomes visible only on commit.
func (r *Repository) InstanceIDTx(ctx context.Context, tx Tx) (string, error) {
	if tx == nil {
		return "", ErrInvalid
	}
	return instanceID(contextOrBackground(ctx), tx)
}

func (r *Repository) EnsureInstanceIDTx(ctx context.Context, tx Tx, id string) error {
	if tx == nil {
		return ErrInvalid
	}
	return ensureInstanceID(contextOrBackground(ctx), tx, id)
}

// InstanceEnvironment reads the permanent environment binding.
func (r *Repository) InstanceEnvironment(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	row, err := bootstrapdb.New(r.db).GetInstanceEnvironment(contextOrBackground(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return row.Environment, nil
}

// BindInstanceEnvironment permanently associates this instance with one
// canonical serving environment. Replaying the same environment is a no-op;
// changing it fails closed without mutating durable state.
func (r *Repository) BindInstanceEnvironment(ctx context.Context, environment string) error {
	if !r.valid() {
		return ErrInvalid
	}
	return bindEnvironment(contextOrBackground(ctx), r.db, environment)
}

func (r *Repository) BindInstanceEnvironmentTx(ctx context.Context, tx Tx, environment string) error {
	if tx == nil {
		return ErrInvalid
	}
	return bindEnvironment(contextOrBackground(ctx), tx, environment)
}

// ClaimProject persists the singleton instance project claim. The claim is
// immutable and idempotent: actor and timestamp are evidence from the winner,
// while an exact project/environment replay returns that evidence.
func (r *Repository) ClaimProject(ctx context.Context, input ProjectClaimInput) (ProjectClaim, error) {
	if !r.valid() {
		return ProjectClaim{}, ErrInvalid
	}
	return claimProject(contextOrBackground(ctx), r.db, input)
}

func (r *Repository) ClaimProjectTx(ctx context.Context, tx Tx, input ProjectClaimInput) (ProjectClaim, error) {
	if tx == nil {
		return ProjectClaim{}, ErrInvalid
	}
	return claimProject(contextOrBackground(ctx), tx, input)
}

func (r *Repository) GetProjectClaim(ctx context.Context) (ProjectClaim, error) {
	if !r.valid() {
		return ProjectClaim{}, ErrInvalid
	}
	return getProjectClaim(contextOrBackground(ctx), r.db)
}

func instanceID(ctx context.Context, db DBTX) (string, error) {
	row, err := bootstrapdb.New(db).GetInstanceIdentity(ctx)
	if err == nil {
		if !instanceidentity.Valid(row.InstanceID) {
			return "", fmt.Errorf("stored instance identity is invalid: %w", ErrConflict)
		}
		return row.InstanceID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var entropy [24]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate instance identity: %w", err)
	}
	candidate := "lvinst_" + base64.RawURLEncoding.EncodeToString(entropy[:])
	// InstanceID generates a best-effort candidate. A concurrent caller may
	// win the singleton insert with a different candidate; ON CONFLICT DO
	// NOTHING lets this caller converge on that canonical row below. Keep the
	// strict conflict behavior in EnsureInstanceID for caller-supplied IDs.
	if _, err := bootstrapdb.New(db).InsertInstanceIdentity(ctx, candidate); err != nil {
		return "", err
	}
	row, err = bootstrapdb.New(db).GetInstanceIdentity(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !instanceidentity.Valid(row.InstanceID) {
		return "", fmt.Errorf("stored instance identity is invalid: %w", ErrConflict)
	}
	return row.InstanceID, nil
}

func ensureInstanceID(ctx context.Context, db DBTX, id string) error {
	if id == "" || id != strings.TrimSpace(id) || !instanceidentity.Valid(id) {
		return fmt.Errorf("%w: invalid instance ID", ErrInvalid)
	}
	if _, err := bootstrapdb.New(db).InsertInstanceIdentity(ctx, id); err != nil {
		return err
	}
	row, err := bootstrapdb.New(db).GetInstanceIdentity(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.InstanceID != id {
		return fmt.Errorf("%w: instance identity is already bound to %q", ErrConflict, row.InstanceID)
	}
	return nil
}

func bindEnvironment(ctx context.Context, db DBTX, environment string) error {
	if environment == "" || environment != strings.TrimSpace(environment) || len(environment) > 255 || !resourcePattern.MatchString(environment) {
		return fmt.Errorf("%w: environment is invalid", ErrInvalid)
	}
	if _, err := bootstrapdb.New(db).InsertInstanceEnvironment(ctx, environment); err != nil {
		return err
	}
	row, err := bootstrapdb.New(db).GetInstanceEnvironment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Environment != environment {
		return fmt.Errorf("%w: instance is bound to environment %q, not %q", ErrEnvironment, row.Environment, environment)
	}
	return nil
}

func claimProject(ctx context.Context, db DBTX, input ProjectClaimInput) (ProjectClaim, error) {
	if input.ClaimedAt.IsZero() {
		input.ClaimedAt = time.Now().UTC()
	}
	if err := input.Validate(); err != nil {
		return ProjectClaim{}, err
	}
	if _, err := bootstrapdb.New(db).InsertProjectClaim(ctx, bootstrapdb.InsertProjectClaimParams{
		ProjectID: input.ProjectID, Environment: input.Environment,
		ClaimedBy: input.ClaimedBy, ClaimedAt: pgtype.Timestamptz{Time: input.ClaimedAt.UTC(), Valid: true},
	}); err != nil {
		return ProjectClaim{}, err
	}
	existing, err := getProjectClaim(ctx, db)
	if err != nil {
		return ProjectClaim{}, err
	}
	if existing.ProjectID != input.ProjectID || existing.Environment != input.Environment {
		return ProjectClaim{}, fmt.Errorf("%w: existing claim is %s/%s, requested %s/%s", ErrConflict, existing.ProjectID, existing.Environment, input.ProjectID, input.Environment)
	}
	return existing, nil
}

func getProjectClaim(ctx context.Context, db DBTX) (ProjectClaim, error) {
	row, err := bootstrapdb.New(db).GetProjectClaim(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectClaim{}, ErrNotFound
	}
	if err != nil {
		return ProjectClaim{}, err
	}
	if !resourcePattern.MatchString(row.ProjectID) {
		return ProjectClaim{}, fmt.Errorf("stored project claim project is invalid")
	}
	environment := row.Environment
	if !row.ClaimedAt.Valid {
		return ProjectClaim{}, fmt.Errorf("stored project claim time is invalid")
	}
	claim := ProjectClaim{ProjectID: row.ProjectID, Environment: environment, ClaimedBy: row.ClaimedBy, ClaimedAt: row.ClaimedAt.Time.UTC()}
	if err := claim.Validate(); err != nil {
		return ProjectClaim{}, fmt.Errorf("stored project claim: %w", err)
	}
	return claim, nil
}

func normalizeSettingKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 255 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: setting key is invalid", ErrInvalid)
	}
	return key, nil
}

func normalizeSetting(key, value string) (string, string, error) {
	key, err := normalizeSettingKey(key)
	if err != nil {
		return "", "", err
	}
	if len(value) > 65536 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", "", fmt.Errorf("%w: setting value is invalid", ErrInvalid)
	}
	return key, value, nil
}
