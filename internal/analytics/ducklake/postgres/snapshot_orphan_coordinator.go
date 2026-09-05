package postgres

// Snapshot-orphan reconciliation composes the bounded control-ledger scanner
// with the dedicated native catalog maintenance session. Discovery and cleanup
// are deliberately one fenced sequence: a successor cannot expire a snapshot
// whose orphan claim was admitted by an older pool fence.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	dbgen "github.com/flidai/leapview/internal/analytics/ducklake/postgres/internal/db"
	"github.com/google/uuid"
)

const (
	defaultSnapshotOrphanGracePeriod = 24 * time.Hour
	defaultSnapshotOrphanPageSize    = MaxSnapshotOrphanScanPageSize
	defaultSnapshotOrphanMaxPages    = DefaultSnapshotOrphanScanPages
	defaultSnapshotOrphanMaxItems    = MaxSnapshotOrphanScanPageSize
	defaultSnapshotOrphanPruneAge    = 24 * time.Hour
	defaultSnapshotOrphanPruneScans  = 8
)

// SnapshotOrphanMaintenanceRequest identifies one deterministic bounded scan.
// ScanID is replayed after a process crash; callers should derive it with
// SnapshotOrphanScanIDForMaintenance rather than generating a fresh random
// UUID per pass.
type SnapshotOrphanMaintenanceRequest struct {
	MaintenanceID   string
	ScanID          string
	PhysicalPoolID  string
	CatalogID       string
	OwnerID         string
	LeaseExpiresAt  time.Time
	GracePeriod     time.Duration
	DryRun          bool
	RequestEvidence json.RawMessage
}

// SnapshotOrphanScannerFactory opens the separately authenticated catalog
// adapter for one exact control identity. Production implementations use the
// dedicated DuckLake maintenance pool and the catalog's admitted metadata
// schema; the scanner owns neither credential nor pool lifecycle.
type SnapshotOrphanScannerFactory func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error)

// SnapshotOrphanCoordinator bounds catalog pages and cleanup claims for each
// retention pass. All mutable state remains in Repository's fenced functions.
type SnapshotOrphanCoordinator struct {
	Control        *Repository
	OpenScannerFor SnapshotOrphanScannerFactory
	GracePeriod    time.Duration
	PageSize       int
	MaxPages       int
	MaxItems       int
	PruneAge       time.Duration
	MaxPruneScans  int
}

type snapshotOrphanClaim struct {
	candidate SnapshotOrphanCleanupCandidate
	fence     CleanupFence
}

// SnapshotOrphanMaintenanceResult records bounded work completed by one pass.
// It intentionally reports counts rather than exposing mutable ledger rows.
type SnapshotOrphanMaintenanceResult struct {
	Scan             SnapshotOrphanScan
	Claims           int
	CleanupCompleted int
	PagesPruned      int
}

// SnapshotOrphanScanID returns a replay-stable UUIDv7 whose timestamp is the
// interval bucket and whose entropy is derived from the exact pool/catalog
// identity. UUIDv7 ordering is useful for audit while deterministic entropy
// ensures a retry addresses the same durable scan row. Production should use
// SnapshotOrphanScanIDForMaintenance so the scan is cryptographically bound to
// the retention operation that owns it.
func SnapshotOrphanScanID(now time.Time, interval time.Duration, physicalPoolID, catalogID string) string {
	if interval <= 0 {
		interval = time.Hour
	}
	bucket := now.UTC().Truncate(interval)
	digest := sha256.Sum256([]byte(fmt.Sprintf("ducklake-snapshot-orphan:%s:%s:%d", physicalPoolID, catalogID, bucket.UnixNano())))
	var id uuid.UUID
	copy(id[:], digest[:16])
	millis := uint64(bucket.UnixMilli()) & ((uint64(1) << 48) - 1)
	id[0] = byte(millis >> 40)
	id[1] = byte(millis >> 32)
	id[2] = byte(millis >> 24)
	id[3] = byte(millis >> 16)
	id[4] = byte(millis >> 8)
	id[5] = byte(millis)
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// SnapshotOrphanScanIDForMaintenance derives a UUIDv7 scan identity from the
// exact UUIDv7 maintenance operation and pool/catalog tuple. Replays that
// supply a different operation or identity therefore cannot address a fresh
// scan row. An empty result means the maintenance ID is not a UUIDv7.
func SnapshotOrphanScanIDForMaintenance(maintenanceID, physicalPoolID, catalogID string) string {
	if !validUUID(maintenanceID) || !validID(physicalPoolID) || !validID(catalogID) {
		return ""
	}
	maintenance, err := uuid.Parse(maintenanceID)
	if err != nil || maintenance[6]>>4 != 7 {
		return ""
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("ducklake-snapshot-orphan:%s:%s:%s", maintenanceID, physicalPoolID, catalogID)))
	var id uuid.UUID
	copy(id[:], digest[:16])
	copy(id[:6], maintenance[:6])
	id[6] = (id[6] & 0x0f) | 0x70
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

func (c *SnapshotOrphanCoordinator) normalize(in SnapshotOrphanMaintenanceRequest) (SnapshotOrphanMaintenanceRequest, int, int, int, time.Duration, int, error) {
	if c == nil || c.Control == nil || c.OpenScannerFor == nil || !validUUID(in.ScanID) || !validID(in.PhysicalPoolID) || !validID(in.CatalogID) || !validID(in.OwnerID) {
		return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrInvalid
	}
	if in.MaintenanceID != "" && !validUUID(in.MaintenanceID) {
		return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrInvalid
	}
	if in.MaintenanceID != "" {
		derived := SnapshotOrphanScanIDForMaintenance(in.MaintenanceID, in.PhysicalPoolID, in.CatalogID)
		if derived == "" {
			return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrInvalid
		}
		if in.ScanID != derived {
			return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, fmt.Errorf("%w: snapshot orphan scan ID is not bound to maintenance ID", ErrConflict)
		}
	}
	if in.LeaseExpiresAt.IsZero() {
		in.LeaseExpiresAt = time.Now().Add(time.Minute)
	}
	grace := in.GracePeriod
	if grace <= 0 {
		grace = c.GracePeriod
	}
	if grace <= 0 {
		grace = defaultSnapshotOrphanGracePeriod
	}
	grace = grace.Truncate(time.Microsecond)
	if grace <= 0 || grace > MaxSnapshotOrphanScanGrace {
		return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrSnapshotOrphanScanBounds
	}
	pageSize := c.PageSize
	if pageSize == 0 {
		pageSize = defaultSnapshotOrphanPageSize
	}
	maxPages := c.MaxPages
	if maxPages == 0 {
		maxPages = defaultSnapshotOrphanMaxPages
	}
	maxItems := c.MaxItems
	if maxItems == 0 {
		maxItems = defaultSnapshotOrphanMaxItems
	}
	if pageSize < 1 || pageSize > MaxSnapshotOrphanScanPageSize || maxPages < 1 || maxPages > MaxSnapshotOrphanScanPages || maxItems < 1 {
		return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrSnapshotOrphanScanBounds
	}
	pruneAge := c.PruneAge
	if pruneAge == 0 {
		pruneAge = defaultSnapshotOrphanPruneAge
	}
	maxPruneScans := c.MaxPruneScans
	if maxPruneScans == 0 {
		maxPruneScans = defaultSnapshotOrphanPruneScans
	}
	if pruneAge < 24*time.Hour || pruneAge > MaxSnapshotOrphanPruneAge || maxPruneScans < 1 || maxPruneScans > 64 {
		return SnapshotOrphanMaintenanceRequest{}, 0, 0, 0, 0, 0, ErrSnapshotOrphanScanBounds
	}
	in.GracePeriod = grace
	return in, pageSize, maxPages, maxItems, pruneAge, maxPruneScans, nil
}

// renewFenceBeforeNative renews the durable pool fence and copies the
// PostgreSQL-issued deadline back into the in-memory fence. The latter is
// important because cleanup claims carry that deadline; retaining the
// pre-scan value could hand a valid claim an already-expired lease.
func (c *SnapshotOrphanCoordinator) renewFenceBeforeNative(ctx context.Context, fence *RetentionMaintenanceFence) error {
	if c == nil || c.Control == nil || fence == nil {
		return ErrInvalid
	}
	if err := c.Control.RenewRetentionMaintenanceFenceFor(ctx, *fence, maxRetentionMaintenanceLease-time.Minute); err != nil {
		return err
	}
	row, err := querygen(c.Control.db).GetPoolMaintenanceFence(ctx, dbgen.GetPoolMaintenanceFenceParams{PhysicalPoolID: fence.PhysicalPoolID, CatalogID: fence.CatalogID})
	if err != nil {
		return mapRetentionAuthorityError(err)
	}
	if row.OwnerID == nil || *row.OwnerID != fence.OwnerID || row.FencingEpoch != fence.FencingEpoch || !row.LeaseExpiresAt.Valid {
		return ErrRetentionMaintenanceFenceStale
	}
	fence.LeaseExpiresAt = row.LeaseExpiresAt.Time.UTC()
	return c.Control.CheckRetentionMaintenanceFence(ctx, *fence)
}

// Run performs bounded direct catalog scanning, durable completion, bounded
// orphan claims, exact native expiry plus verification, and durable cleanup.
// beforeNative is the retention coordinator's fence renewal/check seam; it is
// invoked immediately before the native expiry call and again before each
// durable completion batch. A nil seam uses the repository's own check.
func (c *SnapshotOrphanCoordinator) Run(ctx context.Context, in SnapshotOrphanMaintenanceRequest, poolFence *RetentionMaintenanceFence, session RetentionCatalogSession, beforeNative func(context.Context, *RetentionMaintenanceFence) error) (SnapshotOrphanMaintenanceResult, error) {
	// Orphan reconciliation is a lifecycle boundary: normalize once across
	// bounded catalog scans, native expiry, and durable ledger completion.
	if ctx == nil {
		ctx = context.Background()
	}
	if poolFence == nil || nilRetentionSession(session) {
		return SnapshotOrphanMaintenanceResult{}, ErrInvalid
	}
	in, pageSize, maxPages, maxItems, pruneAge, maxPruneScans, err := c.normalize(in)
	if err != nil {
		return SnapshotOrphanMaintenanceResult{}, err
	}
	fence := poolFence
	if fence.PhysicalPoolID != in.PhysicalPoolID || fence.CatalogID != in.CatalogID || fence.OwnerID != in.OwnerID {
		return SnapshotOrphanMaintenanceResult{}, ErrInvalid
	}
	if beforeNative == nil {
		beforeNative = c.renewFenceBeforeNative
	}
	// Pruning is itself fenced and bounded. It runs before the new scan so an
	// aged page payload cannot accumulate indefinitely when native work is idle.
	pruned, err := c.Control.PruneSnapshotOrphanScanPages(ctx, *fence, pruneAge, maxPruneScans)
	if err != nil {
		return SnapshotOrphanMaintenanceResult{}, err
	}
	scanner, err := c.OpenScannerFor(ctx, in)
	if err != nil {
		return SnapshotOrphanMaintenanceResult{PagesPruned: pruned}, err
	}
	if scanner == nil {
		return SnapshotOrphanMaintenanceResult{PagesPruned: pruned}, ErrInvalid
	}
	scan, err := c.Control.RunSnapshotOrphanScan(ctx, BeginSnapshotOrphanScanInput{
		ScanID: in.ScanID, PhysicalPoolID: in.PhysicalPoolID, CatalogID: in.CatalogID,
		OwnerID: in.OwnerID, FencingEpoch: fence.FencingEpoch, PageSize: pageSize,
		GracePeriod: in.GracePeriod, RequestEvidence: in.RequestEvidence,
	}, scanner, *fence, maxPages)
	if err != nil {
		return SnapshotOrphanMaintenanceResult{Scan: scan, PagesPruned: pruned}, err
	}
	result := SnapshotOrphanMaintenanceResult{Scan: scan, PagesPruned: pruned}
	if in.DryRun {
		return result, nil
	}
	// Refresh the exact pool fence before issuing cleanup claims. The default
	// renewal path updates the caller-visible deadline so each claim receives a
	// live lease rather than the pre-scan expiry captured at acquisition.
	if err := beforeNative(ctx, fence); err != nil {
		return result, err
	}
	claims := make([]snapshotOrphanClaim, 0, maxItems)
	cursor := int64(0)
	for len(claims) < maxItems {
		limit := pageSize
		if remaining := maxItems - len(claims); remaining < limit {
			limit = remaining
		}
		candidates, listErr := c.Control.ListSnapshotOrphanCleanupEligible(ctx, in.PhysicalPoolID, in.CatalogID, cursor, limit)
		if listErr != nil {
			return result, listErr
		}
		if len(candidates) == 0 {
			break
		}
		for _, candidate := range candidates {
			if candidate.SnapshotID <= cursor {
				return result, fmt.Errorf("%w: orphan cleanup cursor regressed", ErrConflict)
			}
			cursor = candidate.SnapshotID
			cleanupFence, claimErr := c.Control.ClaimSnapshotOrphanCleanupUnderPoolFence(ctx, SnapshotRef{PhysicalPoolID: candidate.PhysicalPoolID, CatalogID: candidate.CatalogID, SnapshotID: candidate.SnapshotID}, in.OwnerID, fence.LeaseExpiresAt, *fence)
			if claimErr != nil {
				return result, claimErr
			}
			claims = append(claims, snapshotOrphanClaim{candidate: candidate, fence: cleanupFence})
			if len(claims) == maxItems {
				break
			}
		}
		if len(candidates) < limit {
			break
		}
	}
	result.Claims = len(claims)
	if len(claims) == 0 {
		return result, nil
	}
	verifier, ok := session.(retentionCatalogSnapshotVerifier)
	if !ok {
		return result, fmt.Errorf("%w: catalog session cannot verify orphan snapshot expiry", ErrInvalid)
	}
	// Marker-first replay closes the crash window where native expiry succeeded
	// but durable orphan completion did not. An absent explicit version is
	// already expired and must not be re-issued; a present version (or any
	// verification error) falls through to the exact expiry+verify pair.
	if err := beforeNative(ctx, fence); err != nil {
		return result, err
	}
	pendingIDs := make([]int64, 0, len(claims))
	alreadyAbsent := make(map[int64]bool, len(claims))
	for _, claim := range claims {
		if err := verifier.VerifySnapshotsExpired(ctx, []int64{claim.candidate.SnapshotID}); err != nil {
			pendingIDs = append(pendingIDs, claim.candidate.SnapshotID)
		} else {
			alreadyAbsent[claim.candidate.SnapshotID] = true
		}
	}
	if len(pendingIDs) > 0 {
		if err := beforeNative(ctx, fence); err != nil {
			return result, err
		}
		if err := session.ExpireSnapshots(ctx, pendingIDs, false); err != nil {
			return result, fmt.Errorf("expire DuckLake orphan snapshots: %w", err)
		}
		if err := verifier.VerifySnapshotsExpired(ctx, pendingIDs); err != nil {
			return result, err
		}
	}
	for _, claim := range claims {
		nativeResult := "already-absent"
		if !alreadyAbsent[claim.candidate.SnapshotID] {
			nativeResult = "expired-now"
		}
		evidence, _ := json.Marshal(map[string]any{
			"phase": "snapshot-orphan-cleanup", "scan_id": in.ScanID,
			"snapshot_id": claim.candidate.SnapshotID, "native_result": nativeResult,
		})
		if err := c.Control.CompleteSnapshotOrphanCleanupUnderPoolFence(ctx, SnapshotRef{PhysicalPoolID: claim.candidate.PhysicalPoolID, CatalogID: claim.candidate.CatalogID, SnapshotID: claim.candidate.SnapshotID}, evidence, claim.fence, *fence); err != nil {
			return result, err
		}
		result.CleanupCompleted++
	}
	return result, nil
}
