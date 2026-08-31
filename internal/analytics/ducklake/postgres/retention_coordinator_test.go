package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

type retentionSessionFake struct {
	mu        sync.Mutex
	calls     []string
	expireErr error
	verifyErr error
	oldErr    error
	dryRuns   []bool
}

func TestRetentionSnapshotSetDigestIsOrderIndependent(t *testing.T) {
	first := retentionSnapshotSetDigest([]SnapshotRef{{PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 2}, {PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 1}})
	second := retentionSnapshotSetDigest([]SnapshotRef{{PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 1}, {PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 2}})
	if first != second {
		t.Fatalf("digest depends on enumeration order: %s != %s", first, second)
	}
}

func (f *retentionSessionFake) ExpireSnapshots(_ context.Context, _ []int64, dryRun bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "expire")
	f.dryRuns = append(f.dryRuns, dryRun)
	f.mu.Unlock()
	return f.expireErr
}
func (f *retentionSessionFake) VerifySnapshotsExpired(_ context.Context, _ []int64) error {
	f.mu.Lock()
	f.calls = append(f.calls, "verify")
	f.mu.Unlock()
	return f.verifyErr
}
func (f *retentionSessionFake) CleanupOldFiles(_ context.Context, _ time.Duration, dryRun bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "old-files")
	f.dryRuns = append(f.dryRuns, dryRun)
	f.mu.Unlock()
	return f.oldErr
}
func (f *retentionSessionFake) DeleteOrphanedFiles(_ context.Context, _ time.Duration, dryRun bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "orphans")
	f.dryRuns = append(f.dryRuns, dryRun)
	f.mu.Unlock()
	return nil
}
func (f *retentionSessionFake) Close() error { return nil }

func retentionTestRepository(t *testing.T, suffix string) (*Repository, *pgxpool.Pool, string, string) {
	t.Helper()
	h := postgrestest.Start(t)
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "retention-maintenance", Login: true})
	db := h.NewDatabase(t, "ducklake_retention_"+suffix)
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
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
	r := New(p)
	poolID, catalogID := "retention-"+suffix, "catalog-"+suffix
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{
		PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID,
		CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000041", MetadataSchema: "lake",
		CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1",
	}); err != nil {
		t.Fatal(err)
	}
	return r, p, poolID, catalogID
}

func TestPostgres18RetentionFenceInterlocksAndFences(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "fence")
	var maintenanceUpdate bool
	if err := p.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_maintenance', 'ducklake.snapshot_retention', 'UPDATE')`).Scan(&maintenanceUpdate); err != nil {
		t.Fatal(err)
	}
	if maintenanceUpdate {
		t.Fatal("maintenance role must not have direct snapshot_retention UPDATE privilege")
	}
	first, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-a", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-b", LeaseExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrRetentionMaintenanceBusy) {
		t.Fatalf("concurrent maintenance owner err=%v, want busy", err)
	}
	if _, err := r.AcquireMigrationFence(t.Context(), AcquireMigrationFenceInput{Scope: MigrationFencePool, PhysicalPoolID: poolID, OwnerID: "migration", LeaseExpiresAt: time.Now().Add(time.Minute)}); !errors.Is(err, ErrMigrationBusy) {
		t.Fatalf("migration during retention err=%v, want busy", err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	successor, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-b", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if successor.FencingEpoch <= first.FencingEpoch {
		t.Fatalf("successor epoch=%d, first=%d", successor.FencingEpoch, first.FencingEpoch)
	}
	if err := r.CheckRetentionMaintenanceFence(t.Context(), first); !errors.Is(err, ErrRetentionMaintenanceFenceStale) {
		t.Fatalf("old epoch check err=%v, want stale", err)
	}
	if err := r.RenewRetentionMaintenanceFence(t.Context(), first, time.Now().Add(time.Minute)); !errors.Is(err, ErrRetentionMaintenanceFenceStale) {
		t.Fatalf("old epoch renewal err=%v, want stale", err)
	}
	_ = r.ReleaseRetentionMaintenanceFence(t.Context(), successor)
	// Hold the global migration row in one PostgreSQL transaction, then race a
	// maintenance acquisition against it with a bounded timeout. The shared
	// global→pool→maintenance order blocks safely; releasing the lock lets the
	// maintenance worker complete without deadlock.
	lockTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.Exec(t.Context(), `SELECT 1 FROM ducklake.migration_fence WHERE scope='global' AND physical_pool_id='' FOR UPDATE`); err != nil {
		_ = lockTx.Rollback(t.Context())
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, acquireErr := r.AcquireRetentionMaintenanceFence(shortCtx, AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-concurrent", LeaseExpiresAt: time.Now().Add(time.Minute)})
		done <- acquireErr
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked maintenance acquisition err=%v, want deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked maintenance acquisition did not honor timeout")
	}
	if err := lockTx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	acquired, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-after-lock", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	_ = r.ReleaseRetentionMaintenanceFence(t.Context(), acquired)
}

func TestPostgres18RetentionMaintenanceRoleCapabilities(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "role_capability")
	role := postgrestest.Role{Name: "leapview_control_maintenance", Password: "retention-maintenance", Login: true}
	assertDenied := func(statement string) {
		t.Helper()
		tx, err := p.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `SET ROLE leapview_control_maintenance`); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), statement); err == nil {
			_ = tx.Rollback(t.Context())
			t.Fatalf("raw maintenance DML unexpectedly succeeded: %s", statement)
		}
		_ = tx.Rollback(t.Context())
	}
	assertDenied(`INSERT INTO ducklake.retention_maintenance (maintenance_id,physical_pool_id,catalog_id,owner_id,fencing_epoch,state,phase,dry_run,file_grace_micros) VALUES ('0198f2c0-7c7a-7f00-0000-000000000081','` + poolID + `','` + catalogID + `','raw',1,'running','expiry',false,1)`)
	assertDenied(`UPDATE ducklake.retention_maintenance SET phase='orphans'`)
	assertDenied(`INSERT INTO ducklake.retention_maintenance_snapshot (maintenance_id,physical_pool_id,catalog_id,snapshot_id,phase) VALUES ('0198f2c0-7c7a-7f00-0000-000000000081','` + poolID + `','` + catalogID + `',1,'eligible')`)
	assertDenied(`UPDATE ducklake.retention_maintenance_snapshot SET phase='expired'`)
	assertDenied(`UPDATE ducklake.snapshot_retention SET state='retiring'`)
	assertDenied(`SELECT ducklake.update_retention_maintenance_snapshot('0198f2c0-7c7a-7f00-0000-000000000081','` + poolID + `','` + catalogID + `',1,'raw',1,'cleanup-complete','{}'::jsonb,'{}'::jsonb,'{}'::jsonb)`)

	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 811}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SET ROLE `+role.Name); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	fence, err := AcquireRetentionMaintenanceFence(t.Context(), tx, AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "role-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	request := RetentionMaintenance{MaintenanceID: "0198f2c0-7c7a-7f00-0000-000000000082", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "role-worker", FencingEpoch: fence.FencingEpoch, State: "running", Phase: "expiry", FileGraceMicros: int64(time.Hour / time.Microsecond)}
	operation, err := StartRetentionMaintenance(t.Context(), tx, request)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := prepareRetentionSnapshots(t.Context(), tx, operation); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	items, err := r.ListRetentionMaintenanceSnapshots(t.Context(), request.MaintenanceID)
	if err != nil || len(items) != 1 || items[0].SnapshotID != ref.SnapshotID {
		t.Fatalf("SECURITY DEFINER coordinator path items=%#v err=%v", items, err)
	}
}

func TestPostgres18RetentionCoordinatorReplayAndExactSnapshotVerification(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "replay")
	for _, snapshotID := range []int64{701, 702} {
		ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: snapshotID}
		if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
			t.Fatal(err)
		}
		if err := r.RetireSnapshot(t.Context(), ref, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	failing := &retentionSessionFake{oldErr: errors.New("simulated crash")}
	request := RetentionMaintenanceRequest{MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000051", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour}
	var firstInput RetentionCatalogSessionInput
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(_ context.Context, input RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		firstInput = input
		return failing, nil
	}}
	if _, err := coordinator.Run(t.Context(), request); err == nil {
		t.Fatal("first run unexpectedly completed")
	} else {
		t.Logf("first run failed as expected: %v", err)
	}
	if firstInput.Request.MaintenanceID != request.MaintenanceID || firstInput.Request.PhysicalPoolID != request.PhysicalPoolID || firstInput.Request.CatalogID != request.CatalogID || firstInput.Request.OwnerID != request.OwnerID || firstInput.Fence.PhysicalPoolID != request.PhysicalPoolID || firstInput.Fence.CatalogID != request.CatalogID || firstInput.Fence.OwnerID != request.OwnerID {
		t.Fatalf("session opener received wrong control binding: %#v", firstInput)
	}
	items, err := r.ListRetentionMaintenanceSnapshots(t.Context(), request.MaintenanceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Phase != "quarantined" {
			t.Fatalf("replay child phase=%s, want quarantined", item.Phase)
		}
	}
	// A new retirement after the first worker crashed must not be appended to
	// this operation's frozen set during replay.
	newRef := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 703}
	if err := ensureSnapshotLive(t.Context(), r.db, newRef); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), newRef, time.Now()); err != nil {
		t.Fatal(err)
	}
	succeeded := &retentionSessionFake{}
	coordinator.OpenSessionFor = func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return succeeded, nil
	}
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Maintenance.State != "completed" || result.Maintenance.Phase != "completed" {
		t.Fatalf("replay operation=%#v", result.Maintenance)
	}
	for _, item := range result.Snapshots {
		if item.Phase != "cleanup-complete" {
			t.Fatalf("replay child phase=%s, want cleanup-complete", item.Phase)
		}
	}
	if retention, err := r.LoadSnapshotRetention(t.Context(), newRef); err != nil || retention.State != RetentionRetiring {
		t.Fatalf("new snapshot was appended during replay: %#v, err=%v", retention, err)
	}
	succeeded.mu.Lock()
	defer succeeded.mu.Unlock()
	for _, call := range succeeded.calls {
		if call == "expire" || call == "verify" {
			t.Fatalf("replay re-issued expiry call %q", call)
		}
	}
}

func TestPostgres18RetentionReplayAfterExternalExpiryUsesFrozenClaim(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "expiry_crash")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 801}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := RetentionMaintenanceRequest{MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000061", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour}
	first := &retentionSessionFake{verifyErr: errors.New("simulated crash after external expiry")}
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return first, nil
	}}
	if _, err := coordinator.Run(t.Context(), request); err == nil {
		t.Fatal("first run unexpectedly completed")
	}
	items, err := r.ListRetentionMaintenanceSnapshots(t.Context(), request.MaintenanceID)
	if err != nil || len(items) != 1 || items[0].Phase != "eligible" {
		t.Fatalf("frozen child after crash = %#v, err=%v", items, err)
	}
	retention, err := r.LoadSnapshotRetention(t.Context(), ref)
	if err != nil || retention.State != RetentionExpiring {
		t.Fatalf("claimed retention after crash = %#v, err=%v", retention, err)
	}
	second := &retentionSessionFake{}
	coordinator.OpenSessionFor = func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return second, nil
	}
	if _, err := coordinator.Run(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	second.mu.Lock()
	defer second.mu.Unlock()
	if len(second.calls) == 0 || second.calls[0] != "expire" {
		t.Fatalf("replay calls=%v, expected frozen expiry reconciliation", second.calls)
	}
}

func TestPostgres18RetentionReplayReconcilesAdvancedStateBeforeChildEvidence(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "state_child_crash")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 851}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
		t.Fatal(err)
	}
	request := RetentionMaintenanceRequest{MaintenanceID: "0198f2c0-7c7a-7f00-0000-000000000086", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour}
	first := &retentionSessionFake{verifyErr: errors.New("simulated crash")}
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return first, nil
	}}
	if _, err := coordinator.Run(t.Context(), request); err == nil {
		t.Fatal("first run unexpectedly completed")
	}
	// Simulate a legacy crash window: the retention transition committed, but
	// the per-operation child evidence did not. The replay must reconcile this
	// exact durable mismatch without issuing native expiry again.
	expiryEvidence := maintenanceEvidence(request, "expiry", map[string]any{"snapshot_id": ref.SnapshotID, "dry_run": false})
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='expired',expired_at=clock_timestamp(),evidence=$1::jsonb WHERE physical_pool_id=$2 AND catalog_id=$3 AND snapshot_id=$4`, expiryEvidence, poolID, catalogID, ref.SnapshotID); err != nil {
		t.Fatal(err)
	}
	second := &retentionSessionFake{}
	coordinator.OpenSessionFor = func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return second, nil
	}
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Maintenance.State != "completed" || len(result.Snapshots) != 1 || result.Snapshots[0].Phase != "cleanup-complete" {
		t.Fatalf("reconciled result=%#v snapshots=%#v", result.Maintenance, result.Snapshots)
	}
	second.mu.Lock()
	defer second.mu.Unlock()
	for _, call := range second.calls {
		if call == "expire" || call == "verify" {
			t.Fatalf("state/evidence replay re-issued native expiry: %q", call)
		}
	}
}

func TestPostgres18RetentionReplayReconcilesQuarantinedAndCleanupCompleteChildEvidence(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "advanced_child_states")
	const quarantinedID, cleanupCompleteID = int64(861), int64(862)
	for _, snapshotID := range []int64{quarantinedID, cleanupCompleteID} {
		ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: snapshotID}
		if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
			t.Fatal(err)
		}
		if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	request := RetentionMaintenanceRequest{MaintenanceID: "0198f2c0-7c7a-7f00-0000-000000000087", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour}
	first := &retentionSessionFake{verifyErr: errors.New("simulated crash")}
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return first, nil
	}}
	if _, err := coordinator.Run(t.Context(), request); err == nil {
		t.Fatal("first run unexpectedly completed")
	}
	items, err := r.ListRetentionMaintenanceSnapshots(t.Context(), request.MaintenanceID)
	if err != nil || len(items) != 2 {
		t.Fatalf("frozen children=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Phase != "eligible" {
			t.Fatalf("child phase after simulated crash=%s, want eligible", item.Phase)
		}
	}

	// Model legacy crash windows in which the retention row advanced through
	// quarantine (and, for one row, cleanup-complete) before child evidence was
	// persisted. The trigger permits these monotonic retention transitions while
	// the replay must project the durable evidence through the child phases.
	expiryEvidence := maintenanceEvidence(request, "expiry", map[string]any{"snapshot_id": quarantinedID, "dry_run": false})
	quarantineEvidence := []byte(`{"phase":"quarantined"}`)
	cleanupEvidence := []byte(`{"phase":"cleanup-complete"}`)
	for _, snapshotID := range []int64{quarantinedID, cleanupCompleteID} {
		if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='expired',expired_at=clock_timestamp(),evidence=$1::jsonb WHERE physical_pool_id=$2 AND catalog_id=$3 AND snapshot_id=$4`, expiryEvidence, poolID, catalogID, snapshotID); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET cleanup_owner_id=$1,cleanup_fencing_epoch=1,cleanup_lease_expires_at=clock_timestamp()+interval '1 hour' WHERE physical_pool_id=$2 AND catalog_id=$3 AND snapshot_id=$4`, request.OwnerID, poolID, catalogID, snapshotID); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='quarantined',quarantined_at=clock_timestamp(),quarantine_evidence=$1::jsonb WHERE physical_pool_id=$2 AND catalog_id=$3 AND snapshot_id=$4`, quarantineEvidence, poolID, catalogID, snapshotID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='cleanup-complete',cleanup_completed_at=clock_timestamp(),cleanup_evidence=$1::jsonb WHERE physical_pool_id=$2 AND catalog_id=$3 AND snapshot_id=$4`, cleanupEvidence, poolID, catalogID, cleanupCompleteID); err != nil {
		t.Fatal(err)
	}

	second := &retentionSessionFake{}
	coordinator.OpenSessionFor = func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return second, nil
	}
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Maintenance.State != "completed" || len(result.Snapshots) != 2 {
		t.Fatalf("reconciled result=%#v snapshots=%#v", result.Maintenance, result.Snapshots)
	}
	for _, item := range result.Snapshots {
		if item.Phase != "cleanup-complete" {
			t.Fatalf("reconciled child phase=%s, want cleanup-complete", item.Phase)
		}
	}
	second.mu.Lock()
	defer second.mu.Unlock()
	for _, call := range second.calls {
		if call == "expire" || call == "verify" {
			t.Fatalf("advanced-state replay re-issued native expiry: %q", call)
		}
	}
}

func TestPostgres18RetentionDryRunDoesNotClaimOrMutateRetention(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "dry_run")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 901}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := RetentionMaintenanceRequest{MaintenanceID: "0198f2c0-7c7a-7f00-0000-000000000071", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour, DryRun: true}
	first := &retentionSessionFake{}
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return first, nil
	}}
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DryRun || !result.Maintenance.DryRun || result.Maintenance.State != "completed" {
		t.Fatalf("dry-run result=%#v", result)
	}
	retention, err := r.LoadSnapshotRetention(t.Context(), ref)
	if err != nil || retention.State != RetentionRetiring {
		t.Fatalf("dry-run retention=%#v, err=%v", retention, err)
	}
	var claimID *string
	if err := r.db.QueryRow(t.Context(), `SELECT retention_claim_id::text FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, ref.SnapshotID).Scan(&claimID); err != nil {
		t.Fatal(err)
	}
	if claimID != nil {
		t.Fatalf("dry-run populated retention claim %q", *claimID)
	}
	first.mu.Lock()
	for _, dryRun := range first.dryRuns {
		if !dryRun {
			t.Fatalf("native dry-run call had dry_run=false: %#v", first.dryRuns)
		}
	}
	first.mu.Unlock()
	newRef := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 902}
	if err := ensureSnapshotLive(t.Context(), r.db, newRef); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), newRef, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := &retentionSessionFake{}
	coordinator.OpenSessionFor = func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return second, nil
	}
	result, err = coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshots) != 1 || result.Snapshots[0].SnapshotID != ref.SnapshotID {
		t.Fatalf("dry-run replay changed frozen set: %#v", result.Snapshots)
	}
}

func TestPostgres18RetentionExpiryTimestampAndClaimedExpiredFiltering(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "expiry_timestamp")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 911}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
		t.Fatal(err)
	}
	retiring, err := r.LoadSnapshotRetention(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence) }()
	operation, err := startAndPrepareRetentionMaintenance(t.Context(), r.db, RetentionMaintenance{MaintenanceID: "0198f2c0-7c7a-7f00-0000-000000000091", PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: fence.OwnerID, FencingEpoch: fence.FencingEpoch, State: "running", Phase: "expiry", FileGraceMicros: int64(time.Hour / time.Microsecond)})
	if err != nil {
		t.Fatal(err)
	}
	expiryEvidence := maintenanceEvidence(RetentionMaintenanceRequest{MaintenanceID: operation.MaintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: fence.OwnerID}, "expiry", map[string]any{"snapshot_id": ref.SnapshotID, "dry_run": false})
	expiredAt := retiring.RetiredAt.Add(2 * time.Second).UTC().Truncate(time.Microsecond)
	if err := r.ExpireSnapshotUnderMaintenanceFence(t.Context(), ref, expiryEvidence, expiredAt, operation.MaintenanceID, fence); err != nil {
		t.Fatal(err)
	}
	retention, err := r.LoadSnapshotRetention(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !retention.ExpiredAt.Equal(expiredAt) {
		t.Fatalf("expired_at=%s, want explicit timestamp %s", retention.ExpiredAt, expiredAt)
	}
	eligible, err := r.ListExpiryEligibleSnapshots(t.Context(), poolID, catalogID)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("claimed expired snapshot was listed as eligible: %#v", eligible)
	}
}
