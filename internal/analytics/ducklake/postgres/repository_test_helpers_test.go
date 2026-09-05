package postgres

import (
	"context"
	"strings"
	"time"
)

func digest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

const testCatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000001"

// ensureSnapshotLive seeds the DuckLake retention gate used by upgrade and
// retention-coordinator tests. Build/serving lifecycle evidence is owned by
// canonical delivery; these tests only need the external snapshot row that
// maintenance advances after catalog verification.
func ensureSnapshotLive(ctx context.Context, db DBTX, ref SnapshotRef) error {
	if db == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	_, err := db.Exec(ctx, `
INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state)
VALUES ($1,$2,$3,'live')
ON CONFLICT (physical_pool_id,catalog_id,snapshot_id) DO NOTHING`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID)
	if err != nil {
		return err
	}
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID).Scan(&state); err != nil {
		return err
	}
	if state != string(RetentionLive) {
		return ErrNotLive
	}
	return nil
}

func retireSnapshot(ctx context.Context, db DBTX, ref SnapshotRef, retiredAt time.Time) error {
	if db == nil || !validSnapshotRef(ref) {
		return ErrInvalid
	}
	if retiredAt.IsZero() {
		retiredAt = time.Now().UTC()
	}
	_, err := db.Exec(ctx, `
UPDATE ducklake.snapshot_retention
SET state='retiring', retired_at=$4
WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3 AND state='live'`, ref.PhysicalPoolID, ref.CatalogID, ref.SnapshotID, retiredAt)
	return err
}
