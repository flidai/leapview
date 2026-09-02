package postgres

// The DuckLake snapshots() table function is deliberately not used here:
// DuckLake binds that function by materializing the complete snapshot list
// before applying an outer LIMIT. This adapter reads the physical PostgreSQL
// metadata tables directly so PostgreSQL can apply the keyset predicate and
// LIMIT while scanning the catalog.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgtype"
)

// SnapshotCatalogMarkerFieldBytes is the maximum number of bytes admitted
// from one DuckLake commit_extra_info field. The SQL query only returns this
// bounded prefix (plus its complete byte length); an oversized field is
// rejected rather than silently truncating reconciliation evidence.
const SnapshotCatalogMarkerFieldBytes = catalogartifact.MaxCommitMarkerBytes

// PostgresSnapshotCatalogPageScanner is the production adapter for the
// separately authenticated DuckLake PostgreSQL catalog. The pool must be
// opened with the dedicated DuckLake maintenance credentials by composition;
// this type owns no credentials or lifecycle.
type PostgresSnapshotCatalogPageScanner struct {
	pool           *platformpostgres.Pool
	metadataSchema string
}

type snapshotCatalogRecord struct {
	id       int64
	evidence json.RawMessage
}

// Leave headroom for PostgreSQL's jsonb textual representation and future
// harmless formatting changes while remaining below the schema's 32 KiB cap.
const snapshotCatalogPageEvidenceBudget = maxEvidence - 2048

var _ SnapshotCatalogPageScanner = (*PostgresSnapshotCatalogPageScanner)(nil)

// NewPostgresSnapshotCatalogPageScanner validates the physical metadata
// schema before storing it for identifier interpolation. Values used by the
// keyset predicate and LIMIT remain PostgreSQL parameters.
func NewPostgresSnapshotCatalogPageScanner(pool *platformpostgres.Pool, metadataSchema string) (SnapshotCatalogPageScanner, error) {
	if pool == nil || !validSchema(metadataSchema) {
		return nil, ErrInvalid
	}
	return &PostgresSnapshotCatalogPageScanner{pool: pool, metadataSchema: metadataSchema}, nil
}

// ScanSnapshotPage returns at most pageSize snapshots after cursor. It asks
// PostgreSQL for one extra row, allowing Done to be determined without a
// second unbounded COUNT query. A request for the maximum page therefore
// reads at most 257 bounded rows and returns at most 256 to the ledger.
func (s *PostgresSnapshotCatalogPageScanner) ScanSnapshotPage(ctx context.Context, physicalPoolID, catalogID string, cursor int64, pageSize int) (SnapshotCatalogPage, error) {
	if s == nil || s.pool == nil || !validSchema(s.metadataSchema) || !validID(physicalPoolID) || !validID(catalogID) || cursor < 0 {
		return SnapshotCatalogPage{}, ErrInvalid
	}
	if pageSize < 1 || pageSize > MaxSnapshotOrphanScanPageSize {
		return SnapshotCatalogPage{}, ErrSnapshotOrphanScanBounds
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// DuckLake's metadata schema is a validated, application-admitted
	// identifier. It cannot be a PostgreSQL parameter, so quote it after the
	// strict existing validation above. All values remain parameters.
	schema := quoteSQLIdentifier(s.metadataSchema)
	query := fmt.Sprintf(`
SELECT s.snapshot_id,
       s.snapshot_time,
       s.schema_version,
       s.next_catalog_id,
       s.next_file_id,
       COALESCE(octet_length(c.commit_extra_info), -1),
       left(c.commit_extra_info, %d)
FROM %s.ducklake_snapshot AS s
LEFT JOIN %s.ducklake_snapshot_changes AS c ON c.snapshot_id = s.snapshot_id
WHERE s.snapshot_id > $1
ORDER BY s.snapshot_id
LIMIT $2`, SnapshotCatalogMarkerFieldBytes, schema, schema)

	// sqlc-exception:dynamic-identifier -- the separately authenticated
	// DuckLake metadata schema is validated above and must be interpolated;
	// the keyset cursor and page bound remain PostgreSQL parameters.
	rows, err := s.pool.Query(ctx, query, cursor, pageSize+1)
	if err != nil {
		return SnapshotCatalogPage{}, fmt.Errorf("query DuckLake snapshot catalog page: %w", err)
	}
	defer rows.Close()

	records := make([]snapshotCatalogRecord, 0, pageSize+1)
	for rows.Next() {
		var (
			snapshotID                   int64
			snapshotTime                 pgtype.Timestamptz
			schemaVersion, nextCatalogID pgtype.Int8
			nextFileID                   pgtype.Int8
			markerBytes                  int64
			marker                       pgtype.Text
		)
		if err := rows.Scan(&snapshotID, &snapshotTime, &schemaVersion, &nextCatalogID, &nextFileID, &markerBytes, &marker); err != nil {
			return SnapshotCatalogPage{}, fmt.Errorf("scan DuckLake snapshot catalog page: %w", err)
		}
		if snapshotID <= cursor || snapshotID <= 0 || (len(records) > 0 && snapshotID <= records[len(records)-1].id) || !snapshotTime.Valid || !schemaVersion.Valid || !nextCatalogID.Valid || !nextFileID.Valid {
			return SnapshotCatalogPage{}, fmt.Errorf("%w: invalid DuckLake snapshot metadata row", ErrInvalid)
		}
		item, err := snapshotCatalogEvidence(snapshotID, snapshotTime.Time, schemaVersion.Int64, markerBytes, marker)
		if err != nil {
			return SnapshotCatalogPage{}, err
		}
		records = append(records, snapshotCatalogRecord{id: snapshotID, evidence: item})
	}
	if err := rows.Err(); err != nil {
		return SnapshotCatalogPage{}, fmt.Errorf("read DuckLake snapshot catalog page: %w", err)
	}
	if len(records) == 0 {
		return SnapshotCatalogPage{CursorAfter: cursor, Evidence: map[int64]json.RawMessage{}, Done: true}, nil
	}

	// The extra row proves another page exists. Start with the requested
	// number of rows, then trim further if marker identity fields would make
	// durable page evidence exceed the ledger's 32 KiB bound.
	hasExtra := len(records) > pageSize
	count := len(records)
	if hasExtra {
		count = pageSize
	}
	for count > 0 {
		ids, evidence := snapshotCatalogRecords(records[:count])
		if pageEvidenceWithinBound(ids, evidence) {
			done := !hasExtra && count == len(records)
			after := cursor
			if len(ids) > 0 {
				after = ids[len(ids)-1]
			}
			return SnapshotCatalogPage{CursorAfter: after, SnapshotIDs: ids, Evidence: evidence, Done: done}, nil
		}
		count--
	}
	return SnapshotCatalogPage{}, ErrSnapshotOrphanScanBounds
}

func snapshotCatalogRecords(records []snapshotCatalogRecord) ([]int64, map[int64]json.RawMessage) {
	ids := make([]int64, 0, len(records))
	evidence := make(map[int64]json.RawMessage, len(records))
	for _, record := range records {
		ids = append(ids, record.id)
		evidence[record.id] = record.evidence
	}
	return ids, evidence
}

func pageEvidenceWithinBound(ids []int64, evidence map[int64]json.RawMessage) bool {
	canonical, err := canonicalPageEvidence(ids, evidence)
	return err == nil && len(canonical) <= snapshotCatalogPageEvidenceBudget
}

type snapshotMarkerIdentity struct {
	AttemptID      string `json:"attempt_id"`
	PhysicalPoolID string `json:"physical_pool_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	RequestDigest  string `json:"request_digest"`
	PlanDigest     string `json:"plan_digest"`
	GenerationID   string `json:"generation_id"`
	DeliveryID     string `json:"delivery_id"`
}

func snapshotCatalogEvidence(snapshotID int64, snapshotTime time.Time, schemaVersion, markerBytes int64, marker pgtype.Text) (json.RawMessage, error) {
	if snapshotID <= 0 || markerBytes < -1 {
		return nil, fmt.Errorf("%w: invalid DuckLake snapshot evidence", ErrInvalid)
	}
	if markerBytes > SnapshotCatalogMarkerFieldBytes {
		return nil, fmt.Errorf("%w: snapshot %d commit_extra_info exceeds %d bytes", ErrSnapshotOrphanScanBounds, snapshotID, SnapshotCatalogMarkerFieldBytes)
	}
	base := map[string]any{
		"snapshot_time":  snapshotTime.UTC().Format(time.RFC3339Nano),
		"schema_version": schemaVersion,
	}
	if !marker.Valid || marker.String == "" {
		base["marker_status"] = "none"
		return json.Marshal(base)
	}
	parsed, parseErr := catalogartifact.DecodeCommitMarker([]byte(marker.String))
	if parseErr == nil {
		canonical, canonicalErr := parsed.CanonicalJSON()
		if canonicalErr == nil {
			base["marker_status"] = "valid"
			base["commit_marker_digest"] = digestBytes([]byte(canonical))
			base["marker_identity"] = snapshotMarkerIdentity{
				AttemptID: parsed.AttemptID, PhysicalPoolID: parsed.PhysicalPoolID,
				LeaseEpoch: parsed.LeaseEpoch, RequestDigest: parsed.RequestDigest,
				PlanDigest: parsed.PlanDigest, GenerationID: parsed.GenerationID,
				DeliveryID: parsed.DeliveryID,
			}
			return json.Marshal(base)
		}
	}
	// A non-empty value that is not a valid LeapView marker is retained only as
	// a digest and status. The raw commit_extra_info never enters evidence.
	base["marker_status"] = "malformed_or_non_leapview"
	base["commit_marker_digest"] = digestBytes([]byte(marker.String))
	base["commit_marker_bytes"] = markerBytes
	return json.Marshal(base)
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
