package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	bootstrapdb "github.com/flidai/leapview/internal/platform/bootstrap/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/instanceidentity"
	"github.com/jackc/pgx/v5"
)

// ExistingInstanceID reads the already-persisted instance identity without
// creating one. Offline setup uses this when it must bind protected material
// to an initialized instance.
func (r *Repository) ExistingInstanceID(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	instanceID, err := bootstrapdb.New(r.db).GetExistingInstanceID(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !instanceidentity.Valid(instanceID) {
		return "", fmt.Errorf("stored instance identity is invalid: %w", ErrConflict)
	}
	return instanceID, nil
}

// CustomerOwner returns the immutable customer owner declared for this
// instance. It never infers ownership from an administrator or Project claim.
func (r *Repository) CustomerOwner(ctx context.Context) (string, error) {
	if !r.valid() {
		return "", ErrInvalid
	}
	instanceID, err := r.ExistingInstanceID(ctx)
	if err != nil {
		return "", err
	}
	row, err := bootstrapdb.New(r.db).GetInstanceCustomerOwner(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if row.InstanceID != instanceID || !canonicalCustomerOwnerID(row.OwnerID) || !row.DeclaredAt.Valid {
		return "", fmt.Errorf("stored customer owner declaration is invalid: %w", ErrConflict)
	}
	return row.OwnerID, nil
}

// DeclareCustomerOwner installs the immutable instance owner. A matching
// replay reports inserted=false; a different declaration is a conflict. The
// repository may be a WithTx view, so the caller retains transaction control.
func (r *Repository) DeclareCustomerOwner(ctx context.Context, ownerID string) (inserted bool, err error) {
	if !r.valid() || !canonicalCustomerOwnerID(ownerID) {
		return false, ErrInvalid
	}
	instanceID, err := r.ExistingInstanceID(ctx)
	if err != nil {
		return false, err
	}
	rows, err := bootstrapdb.New(r.db).InsertInstanceCustomerOwner(ctx, bootstrapdb.InsertInstanceCustomerOwnerParams{
		InstanceID: instanceID,
		OwnerID:    ownerID,
	})
	if err != nil {
		return false, err
	}
	if rows < 0 || rows > 1 {
		return false, fmt.Errorf("insert customer owner affected %d rows", rows)
	}
	storedOwner, err := r.CustomerOwner(ctx)
	if err != nil {
		return false, err
	}
	if storedOwner != ownerID {
		return false, fmt.Errorf("%w: customer owner is already declared", ErrConflict)
	}
	return rows == 1, nil
}

func canonicalCustomerOwnerID(ownerID string) bool {
	if ownerID == "" || len(ownerID) > 255 || !utf8.ValidString(ownerID) || strings.TrimSpace(ownerID) != ownerID {
		return false
	}
	return strings.IndexFunc(ownerID, func(value rune) bool {
		return unicode.IsSpace(value) || unicode.IsControl(value)
	}) < 0
}
