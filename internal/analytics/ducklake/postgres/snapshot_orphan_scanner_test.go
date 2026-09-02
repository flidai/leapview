package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scannerPageFake struct {
	pages []SnapshotCatalogPage
	index int
	calls int
}

func (f *scannerPageFake) ScanSnapshotPage(_ context.Context, _ string, _ string, _ int64, _ int) (SnapshotCatalogPage, error) {
	f.calls++
	if f.index >= len(f.pages) {
		return SnapshotCatalogPage{Done: true}, nil
	}
	page := f.pages[f.index]
	f.index++
	return page, nil
}

func TestPostgres18SnapshotOrphanScanBoundedReplayAndFencedRole(t *testing.T) {
	h := postgrestest.Start(t)
	maintenanceRole := postgrestest.Role{Name: "leapview_control_maintenance", Password: "scanner-maintenance", Login: true}
	h.EnsureRole(t, maintenanceRole)
	db := h.NewDatabase(t, "ducklake_orphan_scanner_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	const poolID, catalogID = "scanner-pool", "scanner-catalog"
	adminRepo := New(admin)
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000901", MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	fence, err := adminRepo.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "scanner-owner", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state) VALUES ($1,$2,2,'live')`, poolID, catalogID); err != nil {
		t.Fatal(err)
	}
	maintenanceDB, err := pgxpool.New(t.Context(), db.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenanceDB.Close)
	maintRepo := New(maintenanceDB)
	scanID := "0198f2c0-7c7a-7f00-8a11-000000000902"
	begin := BeginSnapshotOrphanScanInput{ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, PageSize: 2, GracePeriod: 100 * time.Millisecond, RequestEvidence: json.RawMessage(`{"adapter":"test"}`)}
	if err := maintRepo.BeginSnapshotOrphanScan(t.Context(), begin); err != nil {
		t.Fatal(err)
	}
	pageInput := RecordSnapshotOrphanScanPageInput{ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, PageNumber: 1, CursorBefore: 0, CursorAfter: 2, SnapshotIDs: []int64{1, 2}, Evidence: map[int64]json.RawMessage{1: json.RawMessage(`{"source":"catalog"}`), 2: json.RawMessage(`{"source":"catalog"}`)}, Terminal: true}
	page, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), pageInput)
	if err != nil || page.OrphanCount != 1 {
		t.Fatalf("scan page=%#v err=%v", page, err)
	}
	if replay, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), pageInput); err != nil || replay.OrphanCount != page.OrphanCount {
		t.Fatalf("page replay=%#v err=%v", replay, err)
	}
	changed := pageInput
	changed.Evidence = map[int64]json.RawMessage{1: json.RawMessage(`{"source":"changed"}`), 2: json.RawMessage(`{"source":"catalog"}`)}
	if _, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), changed); !errors.Is(err, ErrSnapshotOrphanScanConflict) {
		t.Fatalf("changed page replay err=%v", err)
	}
	var orphanCount int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1`, poolID, catalogID).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 1 {
		t.Fatalf("orphan count=%d", orphanCount)
	}
	// The server recomputes the digest over PostgreSQL's canonical JSONB text;
	// a forged caller digest must be rejected before any page mutation.
	if _, err := maintenanceDB.Exec(t.Context(), `SELECT ducklake.record_snapshot_orphan_scan_page($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12)`, scanID, poolID, catalogID, fence.OwnerID, fence.FencingEpoch, 99, int64(2), int64(3), []int64{3}, "sha256:"+strings.Repeat("0", 64), `{"3":{"source":"catalog"}}`, true); err == nil || !strings.Contains(strings.ToLower(err.Error()), "digest mismatch") {
		t.Fatalf("forged page digest err=%v", err)
	}
	// Empty terminal pages are valid only when they preserve the cursor.
	if _, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), RecordSnapshotOrphanScanPageInput{ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, PageNumber: 2, CursorBefore: 2, CursorAfter: 3, Terminal: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty terminal cursor err=%v", err)
	}
	if _, err := maintenanceDB.Exec(t.Context(), `INSERT INTO ducklake.snapshot_orphan_scan(scan_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,page_size,state) VALUES ('0198f2c0-7c7a-7f00-0000-000000000903',$1,$2,'forged',1,1,'running')`, poolID, catalogID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("maintenance direct DML err=%v", err)
	}
	// Release and reacquire the exact pool fence to prove a successor can
	// resume the persisted cursor while the stale owner is fenced out.
	if err := adminRepo.ReleaseRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	successorFence, err := adminRepo.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "scanner-successor", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	takeover := begin
	takeover.OwnerID, takeover.FencingEpoch = successorFence.OwnerID, successorFence.FencingEpoch
	if err := maintRepo.BeginSnapshotOrphanScan(t.Context(), takeover); err != nil {
		t.Fatalf("scan takeover: %v", err)
	}
	var takeoverOwner, takeoverState string
	var takeoverEpoch int64
	if err := admin.QueryRow(t.Context(), `SELECT owner_id,fencing_epoch,state FROM ducklake.snapshot_orphan_scan WHERE scan_id=$1`, scanID).Scan(&takeoverOwner, &takeoverEpoch, &takeoverState); err != nil {
		t.Fatal(err)
	}
	if takeoverOwner != successorFence.OwnerID || takeoverEpoch != successorFence.FencingEpoch || takeoverState != "running" {
		t.Fatalf("takeover persisted owner=%s epoch=%d state=%s expected=%s/%d/running", takeoverOwner, takeoverEpoch, takeoverState, successorFence.OwnerID, successorFence.FencingEpoch)
	}
	stalePage := pageInput
	if _, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), stalePage); !errors.Is(err, ErrRetentionMaintenanceExpired) {
		t.Fatalf("stale scan page err=%v", err)
	}
	pageInput.OwnerID, pageInput.FencingEpoch = successorFence.OwnerID, successorFence.FencingEpoch
	if _, err := maintRepo.RecordSnapshotOrphanScanPage(t.Context(), pageInput); err != nil {
		t.Fatalf("takeover page replay: %v", err)
	}
	if err := maintRepo.CompleteSnapshotOrphanScan(t.Context(), scanID, successorFence, json.RawMessage(`{"done":true}`)); err != nil {
		t.Fatal(err)
	}
	completedAdapter := &scannerPageFake{pages: []SnapshotCatalogPage{{CursorAfter: 99, SnapshotIDs: []int64{99}, Evidence: map[int64]json.RawMessage{99: json.RawMessage(`{"source":"must-not-run"}`)}, Done: true}}}
	completedRun := takeover
	if _, err := maintRepo.RunSnapshotOrphanScan(t.Context(), completedRun, completedAdapter, successorFence, 1); err != nil {
		t.Fatalf("completed scanner replay: %v", err)
	}
	if completedAdapter.calls != 0 {
		t.Fatalf("completed scanner replay called adapter %d times", completedAdapter.calls)
	}
	// A fresh scan observing the same quarantined orphan is idempotent, while
	// an empty non-terminal page is rejected instead of spinning forever.
	freshID := "0198f2c0-7c7a-7f00-0000-000000000904"
	fresh := begin
	fresh.ScanID, fresh.OwnerID, fresh.FencingEpoch = freshID, successorFence.OwnerID, successorFence.FencingEpoch
	if _, err := maintRepo.RunSnapshotOrphanScan(t.Context(), fresh, &scannerPageFake{pages: []SnapshotCatalogPage{{CursorAfter: 1, SnapshotIDs: []int64{1}, Evidence: map[int64]json.RawMessage{1: json.RawMessage(`{"source":"catalog"}`)}, Done: true}}}, successorFence, 2); err != nil {
		t.Fatalf("repeat scanner run: %v", err)
	}
	boundID := "0198f2c0-7c7a-7f00-0000-000000000905"
	bounded := begin
	bounded.ScanID, bounded.OwnerID, bounded.FencingEpoch = boundID, successorFence.OwnerID, successorFence.FencingEpoch
	if _, err := maintRepo.RunSnapshotOrphanScan(t.Context(), bounded, &scannerPageFake{pages: []SnapshotCatalogPage{{CursorAfter: 0, Done: false}}}, successorFence, 1); !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("empty non-terminal scanner err=%v", err)
	}
	// A running attempt blocks acquisition of a new pool maintenance fence;
	// admission and maintenance serialize on the same authority rows.
	if err := adminRepo.ReleaseRetentionMaintenanceFence(t.Context(), successorFence); err != nil {
		t.Fatal(err)
	}
	attemptID := "0198f2c0-7c7a-7f00-0000-000000000906"
	attempt, err := adminRepo.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: digest('b'), PlanDigest: digest('c'), PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "running-attempt", FencingEpoch: successorFence.FencingEpoch, SessionIdentity: "scanner-race", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("begin running attempt: %v", err)
	}
	if _, err := adminRepo.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "scanner-race-maintenance", LeaseExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrRetentionMaintenanceBusy) {
		t.Fatalf("running attempt fence err=%v", err)
	}
	if _, err := adminRepo.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: attempt.AttemptID, OwnerID: attempt.OwnerID, FencingEpoch: attempt.FencingEpoch, Evidence: json.RawMessage(`{"aborted":"test"}`), TerminatedAt: time.Now()}); err != nil {
		t.Fatalf("abort running attempt: %v", err)
	}
	successorFence, err = adminRepo.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "scanner-successor", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("reacquire maintenance fence: %v", err)
	}
	if _, err := maintRepo.ClaimSnapshotOrphanCleanupUnderPoolFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 1}, "cleanup-owner", time.Now().Add(time.Minute), successorFence); !errors.Is(err, ErrSnapshotOrphanCleanupGrace) {
		t.Fatalf("early cleanup claim err=%v", err)
	}
	if eligible, err := maintRepo.ListSnapshotOrphanCleanupEligible(t.Context(), poolID, catalogID, 0, MaxSnapshotOrphanScanPageSize); err != nil || len(eligible) != 0 {
		t.Fatalf("early cleanup candidates=%#v err=%v", eligible, err)
	}
	time.Sleep(120 * time.Millisecond)
	eligible, err := maintRepo.ListSnapshotOrphanCleanupEligible(t.Context(), poolID, catalogID, 0, MaxSnapshotOrphanScanPageSize)
	if err != nil || len(eligible) != 1 || eligible[0].SnapshotID != 1 {
		t.Fatalf("eligible cleanup candidates=%#v err=%v", eligible, err)
	}
	cleanup, err := maintRepo.ClaimSnapshotOrphanCleanupUnderPoolFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 1}, "cleanup-owner", time.Now().Add(time.Minute), successorFence)
	if err != nil {
		t.Fatal(err)
	}
	if err := maintRepo.CompleteSnapshotOrphanCleanupUnderPoolFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 1}, json.RawMessage(`{"deleted":true}`), cleanup, successorFence); err != nil {
		t.Fatal(err)
	}
	if err := maintRepo.CompleteSnapshotOrphanCleanupUnderPoolFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 1}, json.RawMessage(`{"deleted":true}`), cleanup, successorFence); err != nil {
		t.Fatalf("cleanup replay: %v", err)
	}
	var orphanEvidence []byte
	if err := admin.QueryRow(t.Context(), `SELECT evidence FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1`, poolID, catalogID).Scan(&orphanEvidence); err != nil {
		t.Fatal(err)
	}
	var evidenceObject map[string]json.RawMessage
	if err := json.Unmarshal(orphanEvidence, &evidenceObject); err != nil {
		t.Fatal(err)
	}
	if _, ok := evidenceObject["catalog"]; !ok {
		t.Fatalf("cleanup replaced discovery evidence: %s", orphanEvidence)
	}
	if _, ok := evidenceObject["cleanup"]; !ok {
		t.Fatalf("cleanup evidence missing: %s", orphanEvidence)
	}
	// Seed an aged completed ledger row to exercise bounded page pruning and
	// verify that only the page payload is removed while summary/audit metadata
	// remains durable.
	pruneScanID := "0198f2c0-7c7a-7f00-0000-000000000908"
	old := time.Now().UTC().Add(-48 * time.Hour)
	if _, err := admin.Exec(t.Context(), `INSERT INTO ducklake.snapshot_orphan_scan(scan_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,page_size,grace_micros,cursor_snapshot_id,pages_scanned,snapshots_scanned,orphans_recorded,state,request_evidence,completion_evidence,cleanup_not_before,started_at,updated_at,completed_at) VALUES ($1,$2,$3,$4,$5,1,100000,1,1,1,0,'completed','{}','{"done":true}',$6,$6,$6,$6)`, pruneScanID, poolID, catalogID, successorFence.OwnerID, successorFence.FencingEpoch, old); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO ducklake.snapshot_orphan_scan_page(scan_id,physical_pool_id,catalog_id,page_number,cursor_before,cursor_after,snapshot_ids,orphan_count,terminal,page_digest,evidence,created_at) VALUES ($1,$2,$3,1,0,1,ARRAY[1]::bigint[],0,true,$4,'{"1":{"source":"catalog"}}',$5)`, pruneScanID, poolID, catalogID, "sha256:"+strings.Repeat("0", 64), old); err != nil {
		t.Fatal(err)
	}
	if pruned, err := maintRepo.PruneSnapshotOrphanScanPages(t.Context(), successorFence, 24*time.Hour, 1); err != nil || pruned != 1 {
		t.Fatalf("pruned scans=%d err=%v", pruned, err)
	}
	var remainingPages, prunedPageCount int
	var prunedDigest string
	if err := admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM ducklake.snapshot_orphan_scan_page WHERE scan_id=$1),pruned_page_count,pruned_page_digest FROM ducklake.snapshot_orphan_scan WHERE scan_id=$1`, pruneScanID).Scan(&remainingPages, &prunedPageCount, &prunedDigest); err != nil {
		t.Fatal(err)
	}
	if remainingPages != 0 || prunedPageCount != 1 || !strings.HasPrefix(prunedDigest, "sha256:") {
		t.Fatalf("prune summary pages=%d count=%d digest=%q", remainingPages, prunedPageCount, prunedDigest)
	}
	// A new authority appearing after the grace window is still caught by the
	// claim-time recheck; enumeration is advisory and never grants deletion.
	protectedScanID := "0198f2c0-7c7a-7f00-0000-000000000907"
	protected := begin
	protected.ScanID, protected.OwnerID, protected.FencingEpoch = protectedScanID, successorFence.OwnerID, successorFence.FencingEpoch
	if _, err := maintRepo.RunSnapshotOrphanScan(t.Context(), protected, &scannerPageFake{pages: []SnapshotCatalogPage{{CursorAfter: 3, SnapshotIDs: []int64{3}, Evidence: map[int64]json.RawMessage{3: json.RawMessage(`{"source":"catalog"}`)}, Done: true}}}, successorFence, 2); err != nil {
		t.Fatalf("protected scanner run: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err := admin.Exec(t.Context(), `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state) VALUES ($1,$2,3,'live')`, poolID, catalogID); err != nil {
		t.Fatal(err)
	}
	if _, err := maintRepo.ClaimSnapshotOrphanCleanupUnderPoolFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 3}, "cleanup-owner-3", time.Now().Add(time.Minute), successorFence); err == nil || !strings.Contains(strings.ToLower(err.Error()), "protected") {
		t.Fatalf("protected cleanup claim err=%v", err)
	}
}

func TestSnapshotOrphanScanPruneBounds(t *testing.T) {
	if _, err := PruneSnapshotOrphanScanPages(context.Background(), nil, RetentionMaintenanceFence{}, 24*time.Hour, 1); !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("nil prune transaction err=%v", err)
	}
	if validateBeginSnapshotOrphanScan(BeginSnapshotOrphanScanInput{ScanID: "0198f2c0-7c7a-7f00-0000-000000000999", PhysicalPoolID: "pool", CatalogID: "catalog", OwnerID: "owner", FencingEpoch: 1, PageSize: 1, GracePeriod: 0}) != ErrSnapshotOrphanScanBounds {
		t.Fatal("zero grace unexpectedly accepted")
	}
}
