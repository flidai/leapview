package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresSnapshotCatalogPageScannerKeysetAndBounds(t *testing.T) {
	h := postgrestest.Start(t)
	role := h.EnsureRole(t, postgrestest.Role{Name: "snapshot_scanner", Password: "snapshot-scanner-secret", Login: true})
	db := h.NewDatabase(t, "snapshot_catalog_scanner_test")
	h.GrantDatabase(t, db.Name, role, "CONNECT")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)

	const schema = "catalog_metadata"
	if _, err := admin.Exec(t.Context(), `CREATE SCHEMA "catalog_metadata"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA "catalog_metadata" CASCADE`) })
	if _, err := admin.Exec(t.Context(), `
CREATE TABLE "catalog_metadata".ducklake_snapshot(
  snapshot_id BIGINT PRIMARY KEY,
  snapshot_time TIMESTAMPTZ,
  schema_version BIGINT,
  next_catalog_id BIGINT,
  next_file_id BIGINT
);
CREATE TABLE "catalog_metadata".ducklake_snapshot_changes(
  snapshot_id BIGINT PRIMARY KEY,
  changes_made VARCHAR,
  author VARCHAR,
  commit_message VARCHAR,
  commit_extra_info VARCHAR
);
INSERT INTO "catalog_metadata".ducklake_snapshot VALUES
  (1, '2026-09-02T00:00:01Z', 1, 10, 20),
  (2, '2026-09-02T00:00:02Z', 1, 11, 21),
  (3, '2026-09-02T00:00:03Z', 2, 12, 22);
INSERT INTO "catalog_metadata".ducklake_snapshot_changes VALUES
  (3, NULL, NULL, NULL, NULL);
GRANT USAGE ON SCHEMA "catalog_metadata" TO "snapshot_scanner";
GRANT SELECT ON ALL TABLES IN SCHEMA "catalog_metadata" TO "snapshot_scanner";
`); err != nil {
		t.Fatal(err)
	}
	marker, err := (catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion,
		DeliveryID:    "delivery", GenerationID: "generation", AttemptID: "attempt",
		LeaseEpoch: 7, RequestDigest: "sha256:" + strings.Repeat("a", 64),
		PlanDigest: "sha256:" + strings.Repeat("b", 64), Project: "project",
		Environment: "production", PhysicalPoolID: "pool",
	}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO "catalog_metadata".ducklake_snapshot_changes VALUES (1, 'insert', 'author', 'message', $1)`, marker); err != nil {
		t.Fatal(err)
	}

	pool, err := platformpostgres.Open(t.Context(), platformpostgres.Config{
		URL: db.URL(role), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: role.Name, Intent: platformpostgres.IntentReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	scanner, err := NewPostgresSnapshotCatalogPageScanner(pool, schema)
	if err != nil {
		t.Fatal(err)
	}

	first, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Done || first.CursorAfter != 2 || len(first.SnapshotIDs) != 2 || len(first.Evidence) != 2 {
		t.Fatalf("first page = %#v, want two rows and non-terminal cursor 2", first)
	}
	for _, id := range first.SnapshotIDs {
		if len(first.Evidence[id]) == 0 || len(first.Evidence[id]) > maxEvidence {
			t.Fatalf("evidence[%d] length = %d", id, len(first.Evidence[id]))
		}
	}
	second, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", first.CursorAfter, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Done || second.CursorAfter != 3 || len(second.SnapshotIDs) != 1 {
		t.Fatalf("second page = %#v, want one terminal row", second)
	}
	empty, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", second.CursorAfter, 2)
	if err != nil || !empty.Done || len(empty.SnapshotIDs) != 0 || empty.CursorAfter != second.CursorAfter {
		t.Fatalf("empty terminal page = %#v, err=%v", empty, err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO "catalog_metadata".ducklake_snapshot VALUES (4, '2026-09-02T00:00:04Z', 2, 13, 23); INSERT INTO "catalog_metadata".ducklake_snapshot_changes VALUES (4, 'insert', 'author', 'message', repeat('x', 5000));`); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", second.CursorAfter, 2); !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("oversized marker error = %v, want ErrSnapshotOrphanScanBounds", err)
	}

	if _, err := NewPostgresSnapshotCatalogPageScanner(pool, `catalog_metadata";DROP SCHEMA catalog_metadata;--`); !errors.Is(err, ErrInvalid) {
		t.Fatalf("injection schema error = %v, want ErrInvalid", err)
	}
	if _, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", 0, MaxSnapshotOrphanScanPageSize+1); !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("oversized page error = %v, want ErrSnapshotOrphanScanBounds", err)
	}
	if _, err := scanner.ScanSnapshotPage(t.Context(), "pool", "catalog", 0, 0); !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("zero page error = %v, want ErrSnapshotOrphanScanBounds", err)
	}

	// Ensure evidence remains a timestamped physical fact, not raw marker JSON.
	if !strings.Contains(string(first.Evidence[1]), `"snapshot_time":"2026-09-02T00:00:01Z"`) || !strings.Contains(string(first.Evidence[1]), `"commit_marker_digest":"sha256:`) || !strings.Contains(string(first.Evidence[1]), `"attempt_id":"attempt"`) {
		t.Fatalf("evidence[1] = %s", first.Evidence[1])
	}
}

func TestSnapshotCatalogPageEvidenceBoundAtMaximumPage(t *testing.T) {
	ids := make([]int64, 256)
	evidence := make(map[int64]json.RawMessage, len(ids))
	for i := range ids {
		ids[i] = int64(i + 1)
		item, err := snapshotCatalogEvidence(ids[i], time.Unix(int64(i), 0), 1, -1, pgtype.Text{})
		if err != nil {
			t.Fatal(err)
		}
		evidence[ids[i]] = item
	}
	if !pageEvidenceWithinBound(ids, evidence) {
		t.Fatal("256 compact snapshot evidence entries exceed 32 KiB page bound")
	}
}

func TestSnapshotCatalogPageEvidenceTrimsMarkerRichMaximumPage(t *testing.T) {
	marker, err := (catalogartifact.CommitMarker{
		SchemaVersion: catalogartifact.CommitMarkerSchemaVersion,
		DeliveryID:    "delivery", GenerationID: "generation", AttemptID: "attempt",
		LeaseEpoch: 7, RequestDigest: "sha256:" + strings.Repeat("a", 64),
		PlanDigest: "sha256:" + strings.Repeat("b", 64), Project: "project",
		Environment: "production", PhysicalPoolID: "pool",
	}).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	records := make([]snapshotCatalogRecord, 256)
	for i := range records {
		id := int64(i + 1)
		item, err := snapshotCatalogEvidence(id, time.Unix(int64(i), 0), 1, int64(len(marker)), pgtype.Text{String: marker, Valid: true})
		if err != nil {
			t.Fatal(err)
		}
		records[i] = snapshotCatalogRecord{id: id, evidence: item}
	}
	count := len(records)
	for count > 0 {
		ids, evidence := snapshotCatalogRecords(records[:count])
		if pageEvidenceWithinBound(ids, evidence) {
			break
		}
		count--
	}
	if count <= 0 || count >= len(records) {
		t.Fatalf("marker-rich page trim count = %d, want a positive count below 256", count)
	}
	ids, evidence := snapshotCatalogRecords(records[:count])
	if ids[len(ids)-1] != int64(count) || !pageEvidenceWithinBound(ids, evidence) {
		t.Fatalf("trimmed marker-rich page cursor/evidence invalid: cursor=%d count=%d", ids[len(ids)-1], count)
	}
}
