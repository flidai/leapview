package postgres

import (
	"context"
	"errors"
	"time"

	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// HistoricalCommittedPublication returns the immutable committed publication
// for a generation. It is a read-only lifecycle helper for operator status;
// unlike CommittedPublication it intentionally ignores the active pointer so
// prior generations retain their activation timestamp after a cutover.
func (r *Repository) HistoricalCommittedPublication(ctx context.Context, generationID string) (DeliveryPublication, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryPublication{}, err
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return DeliveryPublication{}, err
	}
	id, err := depdb.New(db).FindHistoricalCommittedPublication(ctx, dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryPublication{}, ErrNotFound
	}
	if err != nil {
		return DeliveryPublication{}, err
	}
	return loadPublication(ctx, db, id)
}

// GenerationRetentionRoot returns the latest generation retention root. The
// root is the native authority's durable retirement evidence; it is exposed
// through a narrow point-read helper so the module can project timestamps
// without adding a collection API or synthesizing lifecycle state.
func (r *Repository) GenerationRetentionRoot(ctx context.Context, generationID string) (DeliveryRetentionRoot, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	row, err := depdb.New(db).FindGenerationRetentionRoot(ctx, dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryRetentionRoot{}, ErrNotFound
	}
	if err != nil {
		return DeliveryRetentionRoot{}, err
	}
	return loadRetentionRoot(ctx, db, row)
}

// GenerationRollbackUntil returns the latest live explicit rollback-retention
// horizon for a generation. ErrNotFound is the truthful result when no
// authority-owned window exists or every prior window has elapsed.
func (r *Repository) GenerationRollbackUntil(ctx context.Context, generationID string) (time.Time, error) {
	db, err := requireDB(r)
	if err != nil {
		return time.Time{}, err
	}
	generation, err := uuidID(generationID, "generation id", false)
	if err != nil {
		return time.Time{}, err
	}
	row, err := depdb.New(db).FindGenerationRollbackUntil(ctx, dbUUID(generation))
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	if !row.Valid {
		return time.Time{}, ErrNotFound
	}
	return row.Time.UTC(), nil
}
