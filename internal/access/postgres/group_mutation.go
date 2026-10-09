package postgres

import (
	"context"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// GroupByIDForUpdate retains the row lock in the audited mutation's
// transaction. An autocommit read cannot protect the subsequent revision check.
func (r *Repository) GroupByIDForUpdate(ctx context.Context, id string) (access.Group, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.Group{}, err
	}
	if _, ok := db.(pgx.Tx); !ok {
		return access.Group{}, errors.New("group revision locking requires a caller-owned transaction")
	}
	id, err = uuidID("group id", id)
	if err != nil {
		return access.Group{}, err
	}
	parsed, err := pgUUID(id)
	if err != nil {
		return access.Group{}, err
	}
	row, err := accessdb.New(db).GetGroupForUpdate(ctx, parsed)
	if err != nil {
		return access.Group{}, err
	}
	return access.Group{ID: principalUUID(row.ID), Provider: row.Provider, ExternalID: row.ExternalID,
		Name: row.Name, CreatedAt: principalTimestamp(row.CreatedAt)}, nil
}
