package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// PrincipalByIDForUpdate retains the row lock in the audited mutation's
// transaction. An autocommit read would release it before the revision check.
func (r *Repository) PrincipalByIDForUpdate(ctx context.Context, id string) (access.Principal, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Principal{}, err
	}
	if _, ok := db.(pgx.Tx); !ok {
		return access.Principal{}, errors.New("principal revision locking requires a caller-owned transaction")
	}
	id, err = uuidID("principal id", id)
	if err != nil {
		return access.Principal{}, err
	}
	parsed, err := pgUUID(id)
	if err != nil {
		return access.Principal{}, err
	}
	row, err := accessdb.New(db).GetPrincipalForUpdate(ctx, parsed)
	if err != nil {
		return access.Principal{}, err
	}
	return principalFromGenerated(accessdb.GetPrincipalRow(row)), nil
}
