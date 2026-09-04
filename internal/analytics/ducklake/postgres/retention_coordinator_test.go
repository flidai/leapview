package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type retentionQueryCounter struct{ count atomic.Int64 }

func (c *retentionQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.count.Add(1)
	return ctx
}

func (c *retentionQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

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

func TestPostgres18RetentionCoordinatorControlRoundTripsAreBatchBounded(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "round_trips")
	counter := &retentionQueryCounter{}
	config := p.Config()
	config.ConnConfig.Tracer = counter
	countedPool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer countedPool.Close()
	counted := New(countedPool)

	seed := func(snapshotID int64) {
		t.Helper()
		ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: snapshotID}
		if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
			t.Fatal(err)
		}
		if err := retireSnapshot(t.Context(), r.db, ref, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	run := func(maintenanceID string, ids []int64) int64 {
		t.Helper()
		for _, id := range ids {
			seed(id)
		}
		counter.count.Store(0)
		coordinator := &RetentionCoordinator{Control: counted, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
			return &retentionSessionFake{}, nil
		}}
		if _, err := coordinator.Run(t.Context(), RetentionMaintenanceRequest{
			MaintenanceID: maintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID,
			OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
		}); err != nil {
			t.Fatal(err)
		}
		return counter.count.Load()
	}

	one := run("0198f2c0-7c7a-7f00-8a11-000000000095", []int64{22001})
	many := run("0198f2c0-7c7a-7f00-8a11-000000000096", []int64{22002, 22003, 22004, 22005})
	if one == 0 || many != one {
		t.Fatalf("retention control round trips grew with child count: one=%d many=%d", one, many)
	}
}

func TestPostgres18RetentionCoordinatorProcessesFixedBatches(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "bounded_batches")
	const total = MaxRetentionMaintenanceSnapshots + 1
	if _, err := p.Exec(t.Context(), `
INSERT INTO ducklake.snapshot_retention(
    physical_pool_id,catalog_id,snapshot_id,state,retired_at,created_at
)
SELECT $1,$2,20000 + value,'retiring',statement_timestamp(),statement_timestamp()
FROM generate_series(1,$3::integer) AS value`, poolID, catalogID, total); err != nil {
		t.Fatal(err)
	}
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		return &retentionSessionFake{}, nil
	}}
	first, err := coordinator.Run(t.Context(), RetentionMaintenanceRequest{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000091", PhysicalPoolID: poolID,
		CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(first.Snapshots); got != MaxRetentionMaintenanceSnapshots {
		t.Fatalf("first bounded batch = %d snapshots, want %d", got, MaxRetentionMaintenanceSnapshots)
	}
	second, err := coordinator.Run(t.Context(), RetentionMaintenanceRequest{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000092", PhysicalPoolID: poolID,
		CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(second.Snapshots); got != 1 {
		t.Fatalf("second bounded batch = %d snapshots, want 1", got)
	}
}

func TestPostgres18RetentionCompletionReadFailureIsRetryable(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "completion_read")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 21001}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := retireSnapshot(t.Context(), r.db, ref, time.Now()); err != nil {
		t.Fatal(err)
	}
	request := RetentionMaintenanceRequest{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000093", PhysicalPoolID: poolID,
		CatalogID: catalogID, OwnerID: "retention-worker", LeaseExpiresAt: time.Now().Add(time.Minute), FileGrace: time.Hour,
	}
	opened := 0
	coordinator := &RetentionCoordinator{Control: r, OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
		opened++
		return &retentionSessionFake{}, nil
	}}
	readErr := errors.New("completion snapshot read failed")
	coordinator.listSnapshots = func(ctx context.Context, maintenanceID string) ([]RetentionMaintenanceSnapshot, error) {
		operation, err := loadRetentionMaintenance(ctx, r.db, maintenanceID)
		if err == nil && operation.State == "completed" {
			return nil, readErr
		}
		return r.ListRetentionMaintenanceSnapshots(ctx, maintenanceID)
	}
	if _, err := coordinator.Run(t.Context(), request); !errors.Is(err, readErr) {
		t.Fatalf("completion read error = %v, want %v", err, readErr)
	}
	persisted, err := loadRetentionMaintenance(t.Context(), r.db, request.MaintenanceID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != "completed" {
		t.Fatalf("persisted operation state = %q, want completed", persisted.State)
	}
	coordinator.listSnapshots = nil
	result, err := coordinator.Run(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Maintenance.State != "completed" || result.Maintenance.CompletedAt.IsZero() {
		t.Fatalf("replay operation = %#v", result.Maintenance)
	}
	if opened != 1 {
		t.Fatalf("catalog sessions opened = %d, want 1; completed replay must not repeat native work", opened)
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
	if err := applyDuckLakeTestSchemas(t.Context(), tx); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, ref, time.Time{}); err != nil {
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
		if err := retireSnapshot(t.Context(), r.db, ref, time.Now()); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, newRef, time.Now()); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, ref, time.Now()); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, ref, time.Time{}); err != nil {
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
		if err := retireSnapshot(t.Context(), r.db, ref, time.Time{}); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, ref, time.Now()); err != nil {
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
	if err := retireSnapshot(t.Context(), r.db, newRef, time.Now()); err != nil {
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

func TestPostgres18CommittedCanonicalAttemptDoesNotBlockRetentionAfterRootsDrain(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "committed_attempt_reachability")
	ctx := t.Context()
	retained := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 941}
	if err := ensureSnapshotLive(ctx, r.db, retained); err != nil {
		t.Fatal(err)
	}
	if err := retireSnapshot(ctx, r.db, retained, time.Now()); err != nil {
		t.Fatal(err)
	}

	seedCommittedAttempt := func(attemptID string, snapshotID int64, requestDigest, planDigest string) {
		t.Helper()
		planID, candidateID, targetID := canonicalAttemptIDs(attemptID)
		if err := seedCanonicalDeliveryAttempt(ctx, p, canonicalDeliveryAttemptInput{
			PlanID: planID, CandidateID: candidateID, TargetID: targetID, AttemptID: attemptID,
			RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID,
			CatalogID: catalogID, OwnerID: "committed-builder", FencingEpoch: 1,
		}); err != nil {
			t.Fatal(err)
		}
		marker := ducklake.CommitMarker{
			SchemaVersion: ducklake.CommitMarkerSchemaVersion,
			DeliveryID:    "delivery-committed-attempt", GenerationID: "generation-committed-attempt",
			AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest,
			Project: "project-retention", Environment: "prod", PhysicalPoolID: poolID,
		}
		canonicalMarker, err := marker.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := deploymentpostgres.New(p).CommitBuildAttempt(ctx, deploymentpostgres.CommitAttemptInput{
			AttemptID: attemptID, OwnerID: "committed-builder", FencingEpoch: 1,
			SnapshotID: snapshotID, CommitMarker: []byte(canonicalMarker),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The committed attempt has a physical snapshot identity but no delivery
	// seal/root or serving reader. Retention must therefore be eligible once
	// the snapshot itself is retired.
	seedCommittedAttempt("0198f2c0-7c7a-7f00-8a11-000000000941", retained.SnapshotID, digest('a'), digest('b'))
	dryFence, err := r.AcquireRetentionMaintenanceFence(ctx, AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-dry-run", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	dryOperation, err := startAndPrepareRetentionMaintenance(ctx, r.db, RetentionMaintenance{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000951", PhysicalPoolID: poolID, CatalogID: catalogID,
		OwnerID: dryFence.OwnerID, FencingEpoch: dryFence.FencingEpoch, State: "running", Phase: "expiry",
		DryRun: true, FileGraceMicros: int64(time.Hour / time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	dryItems, err := r.ListRetentionMaintenanceSnapshots(ctx, dryOperation.MaintenanceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dryItems) != 1 || dryItems[0].SnapshotID != retained.SnapshotID {
		t.Fatalf("dry-run snapshots=%#v, want committed attempt snapshot %d", dryItems, retained.SnapshotID)
	}
	retention, err := r.LoadSnapshotRetention(ctx, retained)
	if err != nil {
		t.Fatal(err)
	}
	if retention.State != RetentionRetiring {
		t.Fatalf("dry-run retention state=%s, want retiring", retention.State)
	}
	var claimID *string
	if err := p.QueryRow(ctx, `SELECT retention_claim_id::text FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, retained.SnapshotID).Scan(&claimID); err != nil {
		t.Fatal(err)
	}
	if claimID != nil {
		t.Fatalf("dry-run populated retention claim %q", *claimID)
	}
	if err := r.ReleaseRetentionMaintenanceFence(ctx, dryFence); err != nil {
		t.Fatal(err)
	}

	claimFence, err := r.AcquireRetentionMaintenanceFence(ctx, AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-claim", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation, err := startAndPrepareRetentionMaintenance(ctx, r.db, RetentionMaintenance{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000000952", PhysicalPoolID: poolID, CatalogID: catalogID,
		OwnerID: claimFence.OwnerID, FencingEpoch: claimFence.FencingEpoch, State: "running", Phase: "expiry",
		FileGraceMicros: int64(time.Hour / time.Microsecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	retention, err = r.LoadSnapshotRetention(ctx, retained)
	if err != nil {
		t.Fatal(err)
	}
	var retentionClaimID string
	if err := p.QueryRow(ctx, `SELECT retention_claim_id::text FROM ducklake.snapshot_retention WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, retained.SnapshotID).Scan(&retentionClaimID); err != nil {
		t.Fatal(err)
	}
	if retention.State != RetentionExpiring || retentionClaimID != operation.MaintenanceID {
		t.Fatalf("claimed retention=%#v claim=%s, want expiring claim %s", retention, retentionClaimID, operation.MaintenanceID)
	}
	evidence := maintenanceEvidence(RetentionMaintenanceRequest{MaintenanceID: operation.MaintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: claimFence.OwnerID}, "expiry", map[string]any{"snapshot_id": retained.SnapshotID, "dry_run": false})
	if err := r.ExpireSnapshotUnderMaintenanceFence(ctx, retained, evidence, time.Time{}, operation.MaintenanceID, claimFence); err != nil {
		t.Fatal(err)
	}
	retention, err = r.LoadSnapshotRetention(ctx, retained)
	if err != nil {
		t.Fatal(err)
	}
	if retention.State != RetentionExpired {
		t.Fatalf("expired retention state=%s, want expired", retention.State)
	}

	// An unsealed committed attempt remains authoritative for orphan safety.
	orphanSnapshotID := int64(942)
	seedCommittedAttempt("0198f2c0-7c7a-7f00-8a11-000000000942", orphanSnapshotID, digest('c'), digest('d'))
	scanID := "0198f2c0-7c7a-7f00-8a11-000000000953"
	if err := r.BeginSnapshotOrphanScan(ctx, BeginSnapshotOrphanScanInput{
		ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: claimFence.OwnerID,
		FencingEpoch: claimFence.FencingEpoch, PageSize: 1, GracePeriod: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := r.RecordSnapshotOrphanScanPage(ctx, RecordSnapshotOrphanScanPageInput{
		ScanID: scanID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: claimFence.OwnerID,
		FencingEpoch: claimFence.FencingEpoch, PageNumber: 1, CursorBefore: 0, CursorAfter: orphanSnapshotID,
		SnapshotIDs: []int64{orphanSnapshotID}, Evidence: map[int64]json.RawMessage{orphanSnapshotID: json.RawMessage(`{"source":"catalog"}`)}, Terminal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.OrphanCount != 0 {
		t.Fatalf("unsealed committed attempt classified as orphan: page=%#v", page)
	}
	var orphanCount int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM ducklake.snapshot_orphan WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, orphanSnapshotID).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != 0 {
		t.Fatalf("unsealed committed attempt produced orphan row: %d", orphanCount)
	}
	if err := r.CompleteSnapshotOrphanScan(ctx, scanID, claimFence, json.RawMessage(`{"done":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(ctx, claimFence); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres18RetentionExpiryTimestampAndClaimedExpiredFiltering(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "expiry_timestamp")
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 911}
	if err := ensureSnapshotLive(t.Context(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if err := retireSnapshot(t.Context(), r.db, ref, time.Time{}); err != nil {
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
	var eligible int
	if err := r.db.QueryRow(t.Context(), `
		SELECT count(*) FROM ducklake.snapshot_retention
		WHERE physical_pool_id=$1 AND catalog_id=$2 AND state='expired' AND retention_claim_id IS NULL`, poolID, catalogID).Scan(&eligible); err != nil {
		t.Fatal(err)
	}
	if eligible != 0 {
		t.Fatalf("claimed expired snapshot was listed as eligible: %d rows", eligible)
	}
}
