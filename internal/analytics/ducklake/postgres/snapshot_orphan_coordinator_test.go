package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// orphanSessionFake records the exact native snapshot IDs so the coordinator
// test can distinguish the orphan expiry call from the surrounding retention
// phases.
type orphanSessionFake struct {
	mu        sync.Mutex
	calls     []string
	expireIDs [][]int64
	verifyIDs [][]int64
	expireErr error
	verifyErr error
	expired   map[int64]bool
}

func (f *orphanSessionFake) ExpireSnapshots(_ context.Context, ids []int64, dryRun bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "expire")
	f.expireIDs = append(f.expireIDs, append([]int64(nil), ids...))
	err := f.expireErr
	if err == nil && !dryRun {
		if f.expired == nil {
			f.expired = make(map[int64]bool, len(ids))
		}
		for _, id := range ids {
			f.expired[id] = true
		}
	}
	f.mu.Unlock()
	return err
}

func (f *orphanSessionFake) VerifySnapshotsExpired(_ context.Context, ids []int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, "verify")
	f.verifyIDs = append(f.verifyIDs, append([]int64(nil), ids...))
	err := f.verifyErr
	if err == nil {
		for _, id := range ids {
			if !f.expired[id] {
				err = errors.New("snapshot remains")
				break
			}
		}
	}
	f.mu.Unlock()
	return err
}

func (f *orphanSessionFake) CleanupOldFiles(context.Context, time.Duration, bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "old-files")
	f.mu.Unlock()
	return nil
}
func (f *orphanSessionFake) DeleteOrphanedFiles(context.Context, time.Duration, bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "orphans")
	f.mu.Unlock()
	return nil
}
func (f *orphanSessionFake) Close() error { return nil }

func orphanCoordinatorForTest(t *testing.T, suffix string) (*Repository, RetentionMaintenanceFence, SnapshotOrphanMaintenanceRequest) {
	t.Helper()
	r, _, poolID, catalogID := retentionTestRepository(t, suffix)
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "orphan-coordinator-owner",
		LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.ReleaseRetentionMaintenanceFence(context.Background(), fence) })
	request := SnapshotOrphanMaintenanceRequest{
		ScanID:          SnapshotOrphanScanID(time.Now(), time.Hour, poolID, catalogID),
		PhysicalPoolID:  poolID,
		CatalogID:       catalogID,
		OwnerID:         fence.OwnerID,
		LeaseExpiresAt:  fence.LeaseExpiresAt,
		RequestEvidence: []byte(`{"adapter":"test"}`),
	}
	return r, fence, request
}

func TestSnapshotOrphanCoordinatorRejectsAboveMaximumPageBudget(t *testing.T) {
	coordinator := &SnapshotOrphanCoordinator{
		Control:  &Repository{},
		MaxPages: MaxSnapshotOrphanScanPages + 1,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			return nil, nil
		},
	}
	_, _, _, _, _, _, err := coordinator.normalize(SnapshotOrphanMaintenanceRequest{
		ScanID: "0198f2c0-7c7a-7f00-8a11-000000000099", PhysicalPoolID: "pool",
		CatalogID: "catalog", OwnerID: "owner", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrSnapshotOrphanScanBounds) {
		t.Fatalf("normalize error = %v, want %v", err, ErrSnapshotOrphanScanBounds)
	}
}

func TestPostgres18SnapshotOrphanCoordinatorBoundsAndExactNativeExpiry(t *testing.T) {
	r, fence, request := orphanCoordinatorForTest(t, "coordinator_bounds")
	request.GracePeriod = 500 * time.Millisecond
	scanner := &recordingOrphanScanner{pages: []SnapshotCatalogPage{
		{CursorAfter: 1001, SnapshotIDs: []int64{1001}, Evidence: map[int64]json.RawMessage{1001: json.RawMessage(`{"source":"catalog"}`)}, Done: false},
		{CursorAfter: 1002, SnapshotIDs: []int64{1002}, Evidence: map[int64]json.RawMessage{1002: json.RawMessage(`{"source":"catalog"}`)}, Done: true},
	}}
	session := &orphanSessionFake{}
	coordinator := &SnapshotOrphanCoordinator{
		Control: r, PageSize: 1, MaxPages: 2, MaxItems: 1,
		GracePeriod: request.GracePeriod,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			return scanner, nil
		},
	}
	result, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Claims != 0 || result.CleanupCompleted != 0 {
		t.Fatalf("initial grace-active result=%#v", result)
	}
	time.Sleep(550 * time.Millisecond)
	result, err = coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Scan.PagesScanned != 2 || result.Scan.SnapshotsScanned != 2 {
		t.Fatalf("scan counts=%#v, want two bounded pages/items", result.Scan)
	}
	if result.Claims != 1 || result.CleanupCompleted != 1 {
		t.Fatalf("bounded cleanup result=%#v", result)
	}
	if len(scanner.cursors) != 2 || scanner.cursors[0] != 0 || scanner.cursors[1] != 1001 {
		t.Fatalf("scanner cursors=%v, want [0 1001]", scanner.cursors)
	}
	if len(scanner.limits) != 2 || scanner.limits[0] != 1 || scanner.limits[1] != 1 {
		t.Fatalf("scanner limits=%v, want page-size bound", scanner.limits)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.expireIDs) != 1 || len(session.expireIDs[0]) != 1 || session.expireIDs[0][0] != 1001 {
		t.Fatalf("expire IDs=%v, want only max-items snapshot", session.expireIDs)
	}
	if len(session.verifyIDs) != 2 || len(session.verifyIDs[0]) != 1 || session.verifyIDs[0][0] != 1001 || len(session.verifyIDs[1]) != 1 || session.verifyIDs[1][0] != 1001 {
		t.Fatalf("verify IDs=%v, want exact expiry set", session.verifyIDs)
	}
	var state string
	if err := r.db.QueryRow(t.Context(), `SELECT state FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1002`, request.PhysicalPoolID, request.CatalogID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "quarantined" {
		t.Fatalf("unclaimed orphan state=%q, want quarantined", state)
	}
}

func TestPostgres18SnapshotOrphanCoordinatorGraceAndReplayAfterNativeFailure(t *testing.T) {
	r, fence, request := orphanCoordinatorForTest(t, "coordinator_replay")
	request.GracePeriod = 500 * time.Millisecond
	scanner := &recordingOrphanScanner{pages: []SnapshotCatalogPage{{
		CursorAfter: 1101, SnapshotIDs: []int64{1101}, Evidence: map[int64]json.RawMessage{1101: json.RawMessage(`{"source":"catalog"}`)}, Done: true,
	}}}
	session := &orphanSessionFake{expireErr: errors.New("simulated native failure")}
	coordinator := &SnapshotOrphanCoordinator{
		Control: r, PageSize: 1, MaxPages: 1, MaxItems: 1, GracePeriod: request.GracePeriod,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			return scanner, nil
		},
	}
	first, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatalf("unexpected first result error: %v", err)
	}
	if first.Claims != 0 || first.CleanupCompleted != 0 {
		t.Fatalf("grace-active result=%#v, want no cleanup claims", first)
	}
	var state string
	if err := r.db.QueryRow(t.Context(), `SELECT state FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1101`, request.PhysicalPoolID, request.CatalogID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "quarantined" {
		t.Fatalf("grace-active orphan state=%q", state)
	}
	time.Sleep(550 * time.Millisecond)
	session.expireErr = errors.New("simulated native failure")
	failed, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err == nil || failed.Claims != 1 || failed.CleanupCompleted != 0 {
		t.Fatalf("native failure result=%#v err=%v", failed, err)
	}
	var cleanupOwner *string
	if err := r.db.QueryRow(t.Context(), `SELECT cleanup_owner_id FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1101`, request.PhysicalPoolID, request.CatalogID).Scan(&cleanupOwner); err != nil {
		t.Fatal(err)
	}
	if cleanupOwner == nil || *cleanupOwner != request.OwnerID {
		t.Fatalf("failed cleanup claim owner=%v, want durable owner", cleanupOwner)
	}
	// Replaying the completed scan must resume the claimed orphan and issue the
	// exact expiry+verification pair before durable cleanup.
	session.expireErr = nil
	result, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Claims != 1 || result.CleanupCompleted != 1 {
		t.Fatalf("replay result=%#v", result)
	}
	if scanner.index != 1 {
		t.Fatalf("replay re-ran catalog scanner, index=%d", scanner.index)
	}
	var finalState string
	if err := r.db.QueryRow(t.Context(), `SELECT state FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1101`, request.PhysicalPoolID, request.CatalogID).Scan(&finalState); err != nil {
		t.Fatal(err)
	}
	if finalState != "cleanup-complete" {
		t.Fatalf("replayed orphan state=%q", finalState)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.expireIDs) != 2 || len(session.verifyIDs) != 3 {
		t.Fatalf("native replay calls expire=%v verify=%v", session.expireIDs, session.verifyIDs)
	}
	if len(session.expireIDs[0]) != 1 || session.expireIDs[0][0] != 1101 || len(session.expireIDs[1]) != 1 || session.expireIDs[1][0] != 1101 || session.verifyIDs[0][0] != 1101 || session.verifyIDs[1][0] != 1101 || session.verifyIDs[2][0] != 1101 {
		t.Fatalf("native IDs expire=%v verify=%v", session.expireIDs, session.verifyIDs)
	}
}

func TestPostgres18SnapshotOrphanCoordinatorReplaysAlreadyAbsentClaim(t *testing.T) {
	r, fence, request := orphanCoordinatorForTest(t, "coordinator_replay_absent")
	request.GracePeriod = 100 * time.Millisecond
	scanner := &recordingOrphanScanner{pages: []SnapshotCatalogPage{{
		CursorAfter: 1151, SnapshotIDs: []int64{1151}, Evidence: map[int64]json.RawMessage{1151: json.RawMessage(`{"source":"catalog"}`)}, Done: true,
	}}}
	session := &orphanSessionFake{verifyErr: errors.New("simulated catalog verification failure")}
	coordinator := &SnapshotOrphanCoordinator{
		Control: r, PageSize: 1, MaxPages: 1, MaxItems: 1, GracePeriod: request.GracePeriod,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			return scanner, nil
		},
	}
	first, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if first.Claims != 0 || first.CleanupCompleted != 0 {
		t.Fatalf("grace-active result=%#v, want no cleanup claims", first)
	}
	time.Sleep(150 * time.Millisecond)
	failed, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err == nil || failed.Claims != 1 || failed.CleanupCompleted != 0 {
		t.Fatalf("native verification failure result=%#v err=%v", failed, err)
	}
	session.mu.Lock()
	if !session.expired[1151] {
		t.Fatalf("failed native pass did not mark snapshot absent: expired=%v", session.expired)
	}
	session.verifyErr = nil
	session.mu.Unlock()
	var cleanupOwner *string
	if err := r.db.QueryRow(t.Context(), `SELECT cleanup_owner_id FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1151`, request.PhysicalPoolID, request.CatalogID).Scan(&cleanupOwner); err != nil {
		t.Fatal(err)
	}
	if cleanupOwner == nil || *cleanupOwner != request.OwnerID {
		t.Fatalf("failed cleanup claim owner=%v, want durable owner", cleanupOwner)
	}

	// Replaying after native expiry observes the absent catalog snapshot and
	// must complete durably without issuing ExpireSnapshots again.
	replay, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if replay.Claims != 1 || replay.CleanupCompleted != 1 {
		t.Fatalf("already-absent replay result=%#v", replay)
	}
	if scanner.index != 1 {
		t.Fatalf("replay re-ran catalog scanner, index=%d", scanner.index)
	}
	var state string
	var orphanEvidence []byte
	if err := r.db.QueryRow(t.Context(), `SELECT state,evidence FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1151`, request.PhysicalPoolID, request.CatalogID).Scan(&state, &orphanEvidence); err != nil {
		t.Fatal(err)
	}
	if state != "cleanup-complete" {
		t.Fatalf("already-absent replay state=%q", state)
	}
	var evidence struct {
		Cleanup struct {
			NativeResult string `json:"native_result"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(orphanEvidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Cleanup.NativeResult != "already-absent" {
		t.Fatalf("cleanup evidence=%s, want native_result=already-absent", orphanEvidence)
	}

	// An exact replay of the completed scan remains idempotent and does not
	// issue another verification or expiry call.
	session.mu.Lock()
	callsAfterCompletion := append([]string(nil), session.calls...)
	session.mu.Unlock()
	exact, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if exact.Claims != 0 || exact.CleanupCompleted != 0 {
		t.Fatalf("exact replay result=%#v, want no new cleanup", exact)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.calls) != len(callsAfterCompletion) {
		t.Fatalf("exact replay issued native calls=%v, want unchanged %v", session.calls, callsAfterCompletion)
	}
	if len(session.expireIDs) != 1 || len(session.verifyIDs) != 3 {
		t.Fatalf("native calls expire=%v verify=%v, want one expiry and three verifications", session.expireIDs, session.verifyIDs)
	}
	if len(session.expireIDs[0]) != 1 || session.expireIDs[0][0] != 1151 {
		t.Fatalf("expire IDs=%v, want only initial expiry", session.expireIDs)
	}
	for _, ids := range session.verifyIDs {
		if len(ids) != 1 || ids[0] != 1151 {
			t.Fatalf("verify IDs=%v, want exact snapshot", session.verifyIDs)
		}
	}
}

func TestPostgres18SnapshotOrphanCoordinatorDryRunSkipsNativeMutation(t *testing.T) {
	r, fence, request := orphanCoordinatorForTest(t, "coordinator_dry_run")
	request.DryRun = true
	request.GracePeriod = 500 * time.Millisecond
	scanner := &recordingOrphanScanner{pages: []SnapshotCatalogPage{{
		CursorAfter: 1201, SnapshotIDs: []int64{1201}, Evidence: map[int64]json.RawMessage{1201: json.RawMessage(`{"source":"catalog"}`)}, Done: true,
	}}}
	session := &orphanSessionFake{}
	coordinator := &SnapshotOrphanCoordinator{
		Control: r, PageSize: 1, MaxPages: 1, MaxItems: 1, GracePeriod: request.GracePeriod,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			return scanner, nil
		},
	}
	result, err := coordinator.Run(t.Context(), request, &fence, session, func(context.Context, *RetentionMaintenanceFence) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if result.Claims != 0 || result.CleanupCompleted != 0 {
		t.Fatalf("dry-run cleanup result=%#v", result)
	}
	session.mu.Lock()
	if len(session.calls) != 0 {
		t.Fatalf("dry-run issued native calls=%v", session.calls)
	}
	session.mu.Unlock()
	var state string
	if err := r.db.QueryRow(t.Context(), `SELECT state FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1201`, request.PhysicalPoolID, request.CatalogID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "quarantined" {
		t.Fatalf("dry-run orphan state=%q, want quarantine only", state)
	}
}

func TestPostgres18RetentionCoordinatorInvokesOrphansBeforeFilePhases(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "coordinator_integration")
	maintenanceID := "0198f2c0-7c7a-7f00-8a11-0000000000a1"
	scanID := SnapshotOrphanScanIDForMaintenance(maintenanceID, poolID, catalogID)
	seedFence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "orphan-seed-owner", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedScanner := &recordingOrphanScanner{pages: []SnapshotCatalogPage{{
		CursorAfter: 1301, SnapshotIDs: []int64{1301}, Evidence: map[int64]json.RawMessage{1301: json.RawMessage(`{"source":"catalog"}`)}, Done: true,
	}}}
	if _, err := r.RunSnapshotOrphanScan(t.Context(), BeginSnapshotOrphanScanInput{
		ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: seedFence.OwnerID,
		FencingEpoch: seedFence.FencingEpoch, PageSize: 1, GracePeriod: 500 * time.Millisecond,
		RequestEvidence: json.RawMessage(`{"seed":true}`),
	}, seedScanner, seedFence, 1); err != nil {
		_ = r.ReleaseRetentionMaintenanceFence(t.Context(), seedFence)
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), seedFence); err != nil {
		t.Fatal(err)
	}
	time.Sleep(550 * time.Millisecond)

	var opened SnapshotOrphanMaintenanceRequest
	var openedCount int
	session := &orphanSessionFake{}
	orphans := &SnapshotOrphanCoordinator{
		Control: r, PageSize: 1, MaxPages: 1, MaxItems: 1, GracePeriod: 500 * time.Millisecond,
		OpenScannerFor: func(_ context.Context, in SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			opened = in
			openedCount++
			return &recordingOrphanScanner{}, nil // completed scan replay must not call it
		},
	}
	request := RetentionMaintenanceRequest{
		MaintenanceID: maintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID,
		OwnerID: "retention-integration-owner", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
		OrphanGracePeriod: 500 * time.Millisecond, OrphanScanID: scanID, Evidence: json.RawMessage(`{"seed":true}`),
	}
	coordinator := &RetentionCoordinator{
		Control: r, Orphans: orphans,
		OpenSessionFor: func(_ context.Context, input RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
			if input.Request.OrphanScanID != scanID || input.Fence.PhysicalPoolID != poolID || input.Fence.CatalogID != catalogID {
				t.Fatalf("session input=%#v", input)
			}
			return session, nil
		},
	}
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if openedCount != 1 || opened.ScanID != scanID || opened.PhysicalPoolID != poolID || opened.CatalogID != catalogID || opened.OwnerID != request.OwnerID {
		t.Fatalf("orphan invocation count=%d request=%#v", openedCount, opened)
	}
	if result.Maintenance.State != "completed" || result.Maintenance.Phase != "completed" {
		t.Fatalf("retention completion=%#v", result.Maintenance)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	wantCalls := []string{"verify", "expire", "verify", "old-files", "orphans"}
	if len(session.calls) != len(wantCalls) {
		t.Fatalf("native phase calls=%v, want %v", session.calls, wantCalls)
	}
	for i := range wantCalls {
		if session.calls[i] != wantCalls[i] {
			t.Fatalf("native phase calls=%v, want %v", session.calls, wantCalls)
		}
	}
	if len(session.expireIDs) != 1 || len(session.expireIDs[0]) != 1 || session.expireIDs[0][0] != 1301 || len(session.verifyIDs) != 2 || session.verifyIDs[0][0] != 1301 || session.verifyIDs[1][0] != 1301 {
		t.Fatalf("orphan native IDs expire=%v verify=%v", session.expireIDs, session.verifyIDs)
	}
	var orphanState string
	if err := r.db.QueryRow(t.Context(), `SELECT state FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=1301`, poolID, catalogID).Scan(&orphanState); err != nil {
		t.Fatal(err)
	}
	if orphanState != "cleanup-complete" {
		t.Fatalf("orphan final state=%q", orphanState)
	}
}

func TestPostgres18RetentionCoordinatorDryRunSkipsOrphanCoordinator(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "coordinator_dry_run")
	var opened int
	orphans := &SnapshotOrphanCoordinator{
		Control: r,
		OpenScannerFor: func(context.Context, SnapshotOrphanMaintenanceRequest) (SnapshotCatalogPageScanner, error) {
			opened++
			return &recordingOrphanScanner{}, nil
		},
	}
	session := &orphanSessionFake{}
	coordinator := &RetentionCoordinator{
		Control: r, Orphans: orphans,
		OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
			return session, nil
		},
	}
	result, err := coordinator.Run(t.Context(), RetentionMaintenanceRequest{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-0000000000a2", PhysicalPoolID: poolID, CatalogID: catalogID,
		OwnerID: "retention-dry-run-owner", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
		OrphanScanID: "0198f2c0-7c7a-7f00-8a11-0000000000a3", DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || result.Maintenance.State != "completed" || opened != 0 {
		t.Fatalf("dry-run result=%#v orphan factory calls=%d", result, opened)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	for _, call := range session.calls {
		if call == "expire" || call == "verify" {
			t.Fatalf("dry-run issued snapshot expiry calls=%v", session.calls)
		}
	}
}

type recordingOrphanScanner struct {
	pages   []SnapshotCatalogPage
	index   int
	cursors []int64
	limits  []int
}

func (s *recordingOrphanScanner) ScanSnapshotPage(_ context.Context, _ string, _ string, cursor int64, limit int) (SnapshotCatalogPage, error) {
	s.cursors = append(s.cursors, cursor)
	s.limits = append(s.limits, limit)
	if s.index >= len(s.pages) {
		return SnapshotCatalogPage{CursorAfter: cursor, Done: true}, nil
	}
	page := s.pages[s.index]
	s.index++
	return page, nil
}
