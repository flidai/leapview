package postgres

// Durable bounded snapshot-orphan scanning. DuckLake's metadata catalog is a
// separate database, so discovery is supplied by a small page adapter. The
// control-side ledger below owns pagination, classification, evidence, and
// fencing; no method ever loads an unbounded catalog snapshot list.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	MaxSnapshotOrphanScanPageSize  = 256
	DefaultSnapshotOrphanScanPages = 64
	MaxSnapshotOrphanScanPages     = 256
	MaxSnapshotOrphanScanGrace     = 30 * 24 * time.Hour
	MaxSnapshotOrphanPruneAge      = 30 * 24 * time.Hour
)

var (
	ErrSnapshotOrphanScanBusy     = errors.New("DuckLake snapshot orphan scan is owned by another worker")
	ErrSnapshotOrphanScanConflict = errors.New("DuckLake snapshot orphan scan conflict")
	ErrSnapshotOrphanScanBounds   = errors.New("DuckLake snapshot orphan scan bound exceeded")
	ErrSnapshotOrphanCleanupGrace = errors.New("DuckLake snapshot orphan cleanup grace is active")
)

// SnapshotCatalogPage is one deterministic keyset page returned by the
// separate DuckLake catalog adapter. SnapshotIDs must be strictly increasing
// and each ID must have a bounded object evidence entry.
type SnapshotCatalogPage struct {
	CursorAfter int64
	SnapshotIDs []int64
	Evidence    map[int64]json.RawMessage
	Done        bool
}

// SnapshotCatalogPageScanner is the exact adapter seam for the separately
// provisioned DuckLake metadata catalog. Implementations must issue a bounded
// keyset query (snapshot_id > cursor, ORDER BY snapshot_id LIMIT limit).
type SnapshotCatalogPageScanner interface {
	ScanSnapshotPage(context.Context, string, string, int64, int) (SnapshotCatalogPage, error)
}

type SnapshotOrphanScan struct {
	ScanID             string
	PhysicalPoolID     string
	CatalogID          string
	OwnerID            string
	FencingEpoch       int64
	PageSize           int
	CursorSnapshotID   int64
	PagesScanned       int
	SnapshotsScanned   int64
	OrphansRecorded    int64
	GracePeriod        time.Duration
	CleanupNotBefore   time.Time
	State              string
	RequestEvidence    json.RawMessage
	CompletionEvidence json.RawMessage
	PrunedAt           time.Time
	PrunedPageCount    int
	PrunedPageDigest   string
	StartedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        time.Time
}

type SnapshotOrphanScanPage struct {
	ScanID         string
	PhysicalPoolID string
	CatalogID      string
	PageNumber     int
	CursorBefore   int64
	CursorAfter    int64
	SnapshotIDs    []int64
	OrphanCount    int
	PageDigest     string
	Evidence       json.RawMessage
	CreatedAt      time.Time
}

// SnapshotOrphanCleanupCandidate is a bounded, keyset-ordered orphan row
// ready for a cleanup attempt. ClaimSnapshotOrphanCleanupUnderPoolFence still
// performs the authoritative atomic protection recheck before mutation.
type SnapshotOrphanCleanupCandidate struct {
	OrphanID              string
	PhysicalPoolID        string
	CatalogID             string
	SnapshotID            int64
	State                 string
	CleanupOwnerID        string
	CleanupFencingEpoch   int64
	CleanupLeaseExpiresAt time.Time
	Evidence              json.RawMessage
	DiscoveredAt          time.Time
	CleanupNotBefore      time.Time
	ResolvedAt            time.Time
}

type BeginSnapshotOrphanScanInput struct {
	ScanID          string
	PhysicalPoolID  string
	CatalogID       string
	OwnerID         string
	FencingEpoch    int64
	PageSize        int
	GracePeriod     time.Duration
	RequestEvidence json.RawMessage
}

type RecordSnapshotOrphanScanPageInput struct {
	ScanID         string
	PhysicalPoolID string
	CatalogID      string
	OwnerID        string
	FencingEpoch   int64
	PageNumber     int
	CursorBefore   int64
	CursorAfter    int64
	SnapshotIDs    []int64
	Evidence       map[int64]json.RawMessage
	Terminal       bool
}

func canonicalScanEvidence(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage("{}"), nil
	}
	canonical, err := canonicalOptionalEvidence(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: scan evidence", ErrInvalid)
	}
	return json.RawMessage(canonical), nil
}

func canonicalPageEvidence(ids []int64, evidence map[int64]json.RawMessage) (json.RawMessage, error) {
	if len(ids) > MaxSnapshotOrphanScanPageSize {
		return nil, ErrSnapshotOrphanScanBounds
	}
	obj := make(map[string]json.RawMessage, len(ids))
	prev := int64(0)
	for _, id := range ids {
		if id <= 0 || id <= prev {
			return nil, fmt.Errorf("%w: snapshot IDs must be strictly increasing", ErrInvalid)
		}
		item, ok := evidence[id]
		if !ok {
			return nil, fmt.Errorf("%w: evidence missing for snapshot %d", ErrInvalid, id)
		}
		canonical, err := canonicalEvidence(item)
		if err != nil {
			return nil, fmt.Errorf("%w: snapshot %d evidence", ErrInvalid, id)
		}
		obj[fmt.Sprintf("%d", id)] = json.RawMessage(canonical)
		prev = id
	}
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func snapshotOrphanPageDigest(ctx context.Context, tx DBTX, evidence json.RawMessage) (string, error) {
	digest, err := querygen(tx).ComputeSnapshotOrphanScanPageDigest(ctx, []byte(evidence))
	if err != nil {
		return "", err
	}
	if platformdigest.ValidateSHA256Identity(digest) != nil {
		return "", fmt.Errorf("%w: invalid server page digest", ErrInvalid)
	}
	return digest, nil
}

func validateBeginSnapshotOrphanScan(in BeginSnapshotOrphanScanInput) error {
	if !validUUID(in.ScanID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 {
		return ErrInvalid
	}
	if in.PageSize < 1 || in.PageSize > MaxSnapshotOrphanScanPageSize {
		return ErrSnapshotOrphanScanBounds
	}
	if in.GracePeriod <= 0 || in.GracePeriod > MaxSnapshotOrphanScanGrace || in.GracePeriod%time.Microsecond != 0 {
		return ErrSnapshotOrphanScanBounds
	}
	return nil
}

func BeginSnapshotOrphanScan(ctx context.Context, tx DBTX, in BeginSnapshotOrphanScanInput) error {
	if tx == nil {
		return ErrInvalid
	}
	if err := validateBeginSnapshotOrphanScan(in); err != nil {
		return err
	}
	evidence, err := canonicalScanEvidence(in.RequestEvidence)
	if err != nil {
		return err
	}
	err = querygen(tx).BeginSnapshotOrphanScan(ctx, dbgen.BeginSnapshotOrphanScanParams{
		ScanID: pgUUID(in.ScanID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID,
		OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, PageSize: int32(in.PageSize), GraceMicros: int64(in.GracePeriod / time.Microsecond), RequestEvidence: []byte(evidence),
	})
	if err != nil {
		message := err.Error()
		switch {
		case containsAny(message, "owned by another", "fence stale"):
			return ErrSnapshotOrphanScanBusy
		case containsAny(message, "scan conflict"):
			return ErrSnapshotOrphanScanConflict
		default:
			return mapRetentionAuthorityError(err)
		}
	}
	return nil
}

func (r *Repository) BeginSnapshotOrphanScan(ctx context.Context, in BeginSnapshotOrphanScanInput) error {
	if r == nil {
		return ErrInvalid
	}
	return BeginSnapshotOrphanScan(ctx, r.db, in)
}

// ListSnapshotOrphanCleanupEligible returns at most limit quarantined orphans
// for one exact pool/catalog whose durable grace has elapsed. The cursor is a
// snapshot ID; callers pass the last returned ID to continue the keyset walk.
func ListSnapshotOrphanCleanupEligible(ctx context.Context, db DBTX, physicalPoolID, catalogID string, cursorSnapshotID int64, limit int) ([]SnapshotOrphanCleanupCandidate, error) {
	if db == nil || !validID(physicalPoolID) || !validID(catalogID) || cursorSnapshotID < 0 || limit < 1 || limit > MaxSnapshotOrphanScanPageSize {
		return nil, ErrInvalid
	}
	rows, err := querygen(db).ListSnapshotOrphanCleanupEligible(ctx, dbgen.ListSnapshotOrphanCleanupEligibleParams{
		PhysicalPoolID: physicalPoolID, CatalogID: catalogID, CursorSnapshotID: cursorSnapshotID, PageLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotOrphanCleanupCandidate, 0, len(rows))
	for _, row := range rows {
		candidate := SnapshotOrphanCleanupCandidate{
			OrphanID: row.OrphanID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID,
			SnapshotID: row.SnapshotID, State: row.State, CleanupFencingEpoch: row.CleanupFencingEpoch,
			Evidence: append(json.RawMessage(nil), row.Evidence...), DiscoveredAt: tsTime(row.DiscoveredAt),
			CleanupNotBefore: tsTime(row.CleanupNotBefore), ResolvedAt: tsTime(row.ResolvedAt),
		}
		if row.CleanupOwnerID != nil {
			candidate.CleanupOwnerID = *row.CleanupOwnerID
		}
		if row.CleanupLeaseExpiresAt.Valid {
			candidate.CleanupLeaseExpiresAt = row.CleanupLeaseExpiresAt.Time.UTC()
		}
		out = append(out, candidate)
	}
	return out, nil
}

func (r *Repository) ListSnapshotOrphanCleanupEligible(ctx context.Context, physicalPoolID, catalogID string, cursorSnapshotID int64, limit int) ([]SnapshotOrphanCleanupCandidate, error) {
	if r == nil {
		return nil, ErrInvalid
	}
	return ListSnapshotOrphanCleanupEligible(ctx, r.db, physicalPoolID, catalogID, cursorSnapshotID, limit)
}

func RecordSnapshotOrphanScanPage(ctx context.Context, tx DBTX, in RecordSnapshotOrphanScanPageInput) (SnapshotOrphanScanPage, error) {
	if tx == nil || !validUUID(in.ScanID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) || in.FencingEpoch <= 0 || in.PageNumber <= 0 || in.CursorBefore < 0 || in.CursorAfter < in.CursorBefore {
		return SnapshotOrphanScanPage{}, ErrInvalid
	}
	if len(in.SnapshotIDs) > MaxSnapshotOrphanScanPageSize {
		return SnapshotOrphanScanPage{}, ErrSnapshotOrphanScanBounds
	}
	if len(in.SnapshotIDs) > 0 && in.CursorAfter != in.SnapshotIDs[len(in.SnapshotIDs)-1] {
		return SnapshotOrphanScanPage{}, fmt.Errorf("%w: cursor must equal final snapshot", ErrInvalid)
	}
	if len(in.SnapshotIDs) == 0 && in.CursorAfter != in.CursorBefore {
		return SnapshotOrphanScanPage{}, fmt.Errorf("%w: empty page must preserve cursor", ErrInvalid)
	}
	evidence, err := canonicalPageEvidence(in.SnapshotIDs, in.Evidence)
	if err != nil {
		return SnapshotOrphanScanPage{}, err
	}
	digest, err := snapshotOrphanPageDigest(ctx, tx, evidence)
	if err != nil {
		return SnapshotOrphanScanPage{}, err
	}
	row, err := querygen(tx).RecordSnapshotOrphanScanPage(ctx, dbgen.RecordSnapshotOrphanScanPageParams{
		ScanID: pgUUID(in.ScanID), PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID,
		FencingEpoch: in.FencingEpoch, PageNumber: int32(in.PageNumber), CursorBefore: in.CursorBefore, CursorAfter: in.CursorAfter,
		SnapshotIds: in.SnapshotIDs, PageDigest: digest, Evidence: []byte(evidence), Terminal: in.Terminal,
	})
	if err != nil {
		message := err.Error()
		switch {
		case containsAny(message, "page conflict", "evidence conflict"):
			return SnapshotOrphanScanPage{}, ErrSnapshotOrphanScanConflict
		case containsAny(message, "empty non-terminal", "exceeds bound"):
			return SnapshotOrphanScanPage{}, ErrSnapshotOrphanScanBounds
		case containsAny(message, "cursor mismatch", "terminal", "fence stale"):
			return SnapshotOrphanScanPage{}, ErrSnapshotOrphanScanBusy
		default:
			return SnapshotOrphanScanPage{}, mapRetentionAuthorityError(err)
		}
	}
	return SnapshotOrphanScanPage{ScanID: in.ScanID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID,
		PageNumber: in.PageNumber, CursorBefore: in.CursorBefore, CursorAfter: row.NextCursor,
		SnapshotIDs: append([]int64(nil), in.SnapshotIDs...), OrphanCount: int(row.OrphanCount), PageDigest: digest, Evidence: evidence}, nil
}

func (r *Repository) RecordSnapshotOrphanScanPage(ctx context.Context, in RecordSnapshotOrphanScanPageInput) (SnapshotOrphanScanPage, error) {
	if r == nil {
		return SnapshotOrphanScanPage{}, ErrInvalid
	}
	return RecordSnapshotOrphanScanPage(ctx, r.db, in)
}

func CompleteSnapshotOrphanScan(ctx context.Context, tx DBTX, scanID string, pool RetentionMaintenanceFence, evidence json.RawMessage) error {
	if tx == nil || !validUUID(scanID) || validateRetentionFence(pool) != nil {
		return ErrInvalid
	}
	canonical, err := canonicalScanEvidence(evidence)
	if err != nil {
		return err
	}
	err = querygen(tx).CompleteSnapshotOrphanScan(ctx, dbgen.CompleteSnapshotOrphanScanParams{ScanID: pgUUID(scanID), PhysicalPoolID: pool.PhysicalPoolID, CatalogID: pool.CatalogID, OwnerID: pool.OwnerID, FencingEpoch: pool.FencingEpoch, CompletionEvidence: []byte(canonical)})
	if err != nil {
		if containsAny(err.Error(), "completion conflict") {
			return ErrSnapshotOrphanScanConflict
		}
		return mapRetentionAuthorityError(err)
	}
	return nil
}

func (r *Repository) CompleteSnapshotOrphanScan(ctx context.Context, scanID string, pool RetentionMaintenanceFence, evidence json.RawMessage) error {
	if r == nil {
		return ErrInvalid
	}
	return CompleteSnapshotOrphanScan(ctx, r.db, scanID, pool, evidence)
}

// PruneSnapshotOrphanScanPages removes aged page payloads while retaining the
// completed scan summary and a server-computed digest for audit. A minimum
// one-day age and bounded scan count prevent an operator from pruning an
// in-flight or freshly completed ledger.
func PruneSnapshotOrphanScanPages(ctx context.Context, tx DBTX, pool RetentionMaintenanceFence, minAge time.Duration, maxScans int) (int, error) {
	if tx == nil || validateRetentionFence(pool) != nil || minAge < 24*time.Hour || minAge > MaxSnapshotOrphanPruneAge || maxScans < 1 || maxScans > 64 {
		return 0, ErrSnapshotOrphanScanBounds
	}
	row, err := querygen(tx).PruneSnapshotOrphanScanPages(ctx, dbgen.PruneSnapshotOrphanScanPagesParams{
		PhysicalPoolID: pool.PhysicalPoolID, CatalogID: pool.CatalogID, OwnerID: pool.OwnerID,
		FencingEpoch: pool.FencingEpoch, MinAgeMicros: int64(minAge / time.Microsecond), MaxScans: int32(maxScans),
	})
	if err != nil {
		return 0, mapRetentionAuthorityError(err)
	}
	return int(row), nil
}

func (r *Repository) PruneSnapshotOrphanScanPages(ctx context.Context, pool RetentionMaintenanceFence, minAge time.Duration, maxScans int) (int, error) {
	if r == nil {
		return 0, ErrInvalid
	}
	return PruneSnapshotOrphanScanPages(ctx, r.db, pool, minAge, maxScans)
}

// ClaimSnapshotOrphanCleanupUnderPoolFence is the maintenance-role cleanup
// capability for an orphan discovered by the scanner. It deliberately uses
// the catalog-wide fence (rather than a caller-owned UPDATE) so a stale
// worker cannot clean a successor's identity.
func ClaimSnapshotOrphanCleanupUnderPoolFence(ctx context.Context, tx DBTX, ref SnapshotRef, ownerID string, leaseExpiresAt time.Time, pool RetentionMaintenanceFence) (CleanupFence, error) {
	if tx == nil || !validSnapshotRef(ref) || !validID(ownerID) || validateRetentionFence(pool) != nil {
		return CleanupFence{}, ErrInvalid
	}
	now, err := databaseClock(ctx, tx)
	if err != nil {
		return CleanupFence{}, err
	}
	lease, err := retentionFenceLease(now, leaseExpiresAt)
	if err != nil {
		return CleanupFence{}, err
	}
	epoch, err := querygen(tx).ClaimSnapshotOrphanCleanupUnderPoolFence(ctx, dbgen.ClaimSnapshotOrphanCleanupUnderPoolFenceParams{
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		OwnerID: ownerID, CleanupLeaseExpiresAt: pgtype.Timestamptz{Time: lease, Valid: true}, FenceOwnerID: pool.OwnerID, FencingEpoch: pool.FencingEpoch,
	})
	if err != nil {
		message := err.Error()
		switch {
		case containsAny(message, "cleanup busy"):
			return CleanupFence{}, ErrCleanupBusy
		case containsAny(message, "grace is active"):
			return CleanupFence{}, ErrSnapshotOrphanCleanupGrace
		case containsAny(message, "fence expired"):
			return CleanupFence{}, ErrRetentionMaintenanceExpired
		case containsAny(message, "not found"):
			return CleanupFence{}, ErrNotFound
		default:
			return CleanupFence{}, err
		}
	}
	if epoch <= 0 {
		return CleanupFence{}, ErrConflict
	}
	return CleanupFence{OwnerID: ownerID, FencingEpoch: epoch, LeaseExpiresAt: lease}, nil
}

func (r *Repository) ClaimSnapshotOrphanCleanupUnderPoolFence(ctx context.Context, ref SnapshotRef, ownerID string, leaseExpiresAt time.Time, pool RetentionMaintenanceFence) (CleanupFence, error) {
	if r == nil {
		return CleanupFence{}, ErrInvalid
	}
	return ClaimSnapshotOrphanCleanupUnderPoolFence(ctx, r.db, ref, ownerID, leaseExpiresAt, pool)
}

func CompleteSnapshotOrphanCleanupUnderPoolFence(ctx context.Context, tx DBTX, ref SnapshotRef, evidence json.RawMessage, cleanup CleanupFence, pool RetentionMaintenanceFence) error {
	if tx == nil || !validSnapshotRef(ref) || !validID(cleanup.OwnerID) || cleanup.FencingEpoch <= 0 || validateRetentionFence(pool) != nil {
		return ErrInvalid
	}
	canonical, err := canonicalEvidence(evidence)
	if err != nil {
		return fmt.Errorf("%w: cleanup evidence is required", ErrInvalid)
	}
	err = querygen(tx).CompleteSnapshotOrphanCleanupUnderPoolFence(ctx, dbgen.CompleteSnapshotOrphanCleanupUnderPoolFenceParams{
		PhysicalPoolID: ref.PhysicalPoolID, CatalogID: ref.CatalogID, SnapshotID: ref.SnapshotID,
		OwnerID: cleanup.OwnerID, FencingEpoch: cleanup.FencingEpoch, Evidence: []byte(canonical), FenceOwnerID: pool.OwnerID, PoolFencingEpoch: pool.FencingEpoch,
	})
	if err != nil {
		message := err.Error()
		switch {
		case containsAny(message, "evidence conflict"):
			return ErrConflict
		case containsAny(message, "fence stale"):
			return ErrStaleFence
		case containsAny(message, "fence expired"):
			return ErrRetentionMaintenanceExpired
		default:
			return err
		}
	}
	return nil
}

func (r *Repository) CompleteSnapshotOrphanCleanupUnderPoolFence(ctx context.Context, ref SnapshotRef, evidence json.RawMessage, cleanup CleanupFence, pool RetentionMaintenanceFence) error {
	if r == nil {
		return ErrInvalid
	}
	return CompleteSnapshotOrphanCleanupUnderPoolFence(ctx, r.db, ref, evidence, cleanup, pool)
}

func LoadSnapshotOrphanScan(ctx context.Context, db DBTX, scanID string) (SnapshotOrphanScan, error) {
	if db == nil || !validUUID(scanID) {
		return SnapshotOrphanScan{}, ErrInvalid
	}
	row, err := querygen(db).GetSnapshotOrphanScan(ctx, pgUUID(scanID))
	if errors.Is(err, pgx.ErrNoRows) {
		return SnapshotOrphanScan{}, ErrNotFound
	}
	if err != nil {
		return SnapshotOrphanScan{}, err
	}
	var prunedAt time.Time
	if row.PrunedAt.Valid {
		prunedAt = row.PrunedAt.Time.UTC()
	}
	return SnapshotOrphanScan{ScanID: row.ScanID, PhysicalPoolID: row.PhysicalPoolID, CatalogID: row.CatalogID, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, PageSize: int(row.PageSize), GracePeriod: time.Duration(row.GraceMicros) * time.Microsecond, CursorSnapshotID: row.CursorSnapshotID, PagesScanned: int(row.PagesScanned), SnapshotsScanned: row.SnapshotsScanned, OrphansRecorded: row.OrphansRecorded, State: row.State, RequestEvidence: append(json.RawMessage(nil), row.RequestEvidence...), CompletionEvidence: append(json.RawMessage(nil), row.CompletionEvidence...), CleanupNotBefore: tsTime(row.CleanupNotBefore), PrunedAt: prunedAt, PrunedPageCount: int(row.PrunedPageCount), PrunedPageDigest: row.PrunedPageDigest, StartedAt: tsTime(row.StartedAt), UpdatedAt: tsTime(row.UpdatedAt), CompletedAt: tsTime(row.CompletedAt)}, nil
}

func (r *Repository) LoadSnapshotOrphanScan(ctx context.Context, scanID string) (SnapshotOrphanScan, error) {
	if r == nil {
		return SnapshotOrphanScan{}, ErrInvalid
	}
	return LoadSnapshotOrphanScan(ctx, r.db, scanID)
}

// RunSnapshotOrphanScan executes at most MaxPages bounded adapter calls. A
// successor can resume by reusing the same scan ID and reading its persisted
// cursor before issuing the next page.
func (r *Repository) RunSnapshotOrphanScan(ctx context.Context, in BeginSnapshotOrphanScanInput, adapter SnapshotCatalogPageScanner, fence RetentionMaintenanceFence, maxPages int) (SnapshotOrphanScan, error) {
	if r == nil || adapter == nil || validateRetentionFence(fence) != nil || in.PhysicalPoolID != fence.PhysicalPoolID || in.CatalogID != fence.CatalogID || in.OwnerID != fence.OwnerID || in.FencingEpoch != fence.FencingEpoch {
		return SnapshotOrphanScan{}, ErrInvalid
	}
	if maxPages <= 0 {
		maxPages = DefaultSnapshotOrphanScanPages
	}
	if err := r.BeginSnapshotOrphanScan(ctx, in); err != nil {
		return SnapshotOrphanScan{}, err
	}
	scan, err := r.LoadSnapshotOrphanScan(ctx, in.ScanID)
	if err != nil {
		return SnapshotOrphanScan{}, err
	}
	if scan.State == "completed" {
		return scan, nil
	}
	if scan.State != "running" {
		return scan, ErrSnapshotOrphanScanConflict
	}
	for i := 0; i < maxPages; i++ {
		page, err := adapter.ScanSnapshotPage(ctx, in.PhysicalPoolID, in.CatalogID, scan.CursorSnapshotID, in.PageSize)
		if err != nil {
			return scan, err
		}
		if len(page.SnapshotIDs) > in.PageSize || len(page.SnapshotIDs) > MaxSnapshotOrphanScanPageSize {
			return scan, ErrSnapshotOrphanScanBounds
		}
		if _, err := r.RecordSnapshotOrphanScanPage(ctx, RecordSnapshotOrphanScanPageInput{ScanID: in.ScanID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID, OwnerID: in.OwnerID, FencingEpoch: in.FencingEpoch, PageNumber: scan.PagesScanned + 1, CursorBefore: scan.CursorSnapshotID, CursorAfter: page.CursorAfter, SnapshotIDs: page.SnapshotIDs, Evidence: page.Evidence, Terminal: page.Done}); err != nil {
			return scan, err
		}
		scan, err = r.LoadSnapshotOrphanScan(ctx, in.ScanID)
		if err != nil {
			return scan, err
		}
		if page.Done {
			completionEvidence, _ := json.Marshal(map[string]any{
				"pages_scanned":      scan.PagesScanned,
				"snapshots_scanned":  scan.SnapshotsScanned,
				"orphans_recorded":   scan.OrphansRecorded,
				"cursor_snapshot_id": scan.CursorSnapshotID,
			})
			if err := r.CompleteSnapshotOrphanScan(ctx, in.ScanID, fence, completionEvidence); err != nil {
				return scan, err
			}
			return r.LoadSnapshotOrphanScan(ctx, in.ScanID)
		}
	}
	return scan, ErrSnapshotOrphanScanBounds
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(strings.ToLower(value), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
