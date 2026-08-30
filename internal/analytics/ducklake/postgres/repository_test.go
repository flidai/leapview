package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

func digest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

const testCatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000001"

// sqliteSnapshotLookup adapts a tiny in-memory SQL fixture to DuckLake's
// SnapshotLookup interface. Rewriting only the DuckLake table-function names
// keeps the production resolver's query shape intact while exercising its
// missing-marker path without requiring a DuckDB extension.
type sqliteSnapshotLookup struct{ db *sql.DB }

func (s sqliteSnapshotLookup) rewrite(query string) string {
	return strings.NewReplacer(
		"lake.last_committed_snapshot()", "fake_last_committed_snapshot",
		"lake.snapshots()", "fake_snapshots",
		"CAST(commit_extra_info AS VARCHAR)", "commit_extra_info",
	).Replace(query)
}

func (s sqliteSnapshotLookup) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.rewrite(query), args...)
}

func (s sqliteSnapshotLookup) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.rewrite(query), args...)
}

func TestValidationRejectsUnboundedOrCrossPoolIdentity(t *testing.T) {
	if err := validateCatalog(CatalogIdentity{PhysicalPoolID: " ", CatalogID: "catalog", MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "v1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid catalog accepted: %v", err)
	}
	if err := validateBinding(GenerationBinding{DeliveryID: "delivery", GenerationID: "generation", AttemptID: "not-a-uuid", PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 1, RelationManifestDigest: digest('a'), CompatibilityDigest: digest('b'), ServingArtifactDigest: digest('c'), RequestDigest: digest('d'), PlanDigest: digest('e'), FencingEpoch: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid generation binding accepted: %v", err)
	}
	if _, err := canonicalEvidence(json.RawMessage(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate termination evidence accepted")
	}
}

// This is the focused PostgreSQL 18 integration contract. It is skipped by
// default when Docker is unavailable; CI sets the conformance-required flag.
func TestPostgres18CatalogAttemptGenerationAndSnapshotLeaseLifecycle(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_identity_test")
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
	const poolID, catalogID = "pool-1", "catalog-1"
	identity := CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}
	registered, err := r.RegisterCatalog(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := r.LoadCatalog(t.Context(), poolID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CatalogDatabase != identity.CatalogDatabase || loaded.CatalogUUID != identity.CatalogUUID || !sameCatalog(loaded, identity) {
		t.Fatalf("catalog identity round trip = %#v, want %#v", loaded, identity)
	}
	if registered.CatalogDatabase != identity.CatalogDatabase || registered.CatalogUUID != identity.CatalogUUID {
		t.Fatalf("registered catalog identity = %#v, want database/uuid %q/%q", registered, identity.CatalogDatabase, identity.CatalogUUID)
	}
	for label, mutate := range map[string]func(*CatalogIdentity){
		"database": func(v *CatalogIdentity) { v.CatalogDatabase = "other_ducklake" },
		"uuid":     func(v *CatalogIdentity) { v.CatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000099" },
	} {
		conflict := identity
		mutate(&conflict)
		if _, err := r.RegisterCatalog(t.Context(), conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("immutable catalog %s replay error = %v, want ErrConflict", label, err)
		}
	}
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: "pool-2", CatalogDatabase: "ducklake", CatalogID: "catalog-2", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000002", MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateSnapshotRoot(t.Context(), SnapshotRootInput{RootID: "0198f2c0-7c7a-7f00-8a11-000000000005", PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 500, Kind: RootCandidate}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("root created without qualified retention, err=%v", err)
	}
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000001"
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: digest('b'), PlanDigest: digest('c'), PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder-a", FencingEpoch: 7, SessionIdentity: "duckdb-session-1", LeaseExpiresAt: time.Now().UTC().Add(250 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	// A lost acknowledgement can arrive after the control lease expires. The
	// exact DuckLake marker, not lease timeout, remains authoritative evidence.
	time.Sleep(300 * time.Millisecond)
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-1", GenerationID: "generation-1", AttemptID: attemptID, LeaseEpoch: 7, RequestDigest: digest('b'), PlanDigest: digest('c'), Project: "project-1", Environment: "prod", PhysicalPoolID: poolID}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	crossPoolMarker := marker
	crossPoolMarker.PhysicalPoolID = "pool-2"
	crossPoolMarkerJSON, err := crossPoolMarker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitAttempt(t.Context(), CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-a", FencingEpoch: 7, Snapshot: SnapshotRef{PhysicalPoolID: "pool-2", CatalogID: "catalog-2", SnapshotID: 41}, CommitMarker: crossPoolMarkerJSON}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-pool commit error = %v", err)
	}
	if _, err := r.CommitAttempt(t.Context(), CommitAttemptInput{AttemptID: attemptID, OwnerID: "builder-a", FencingEpoch: 7, Snapshot: SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, CommitMarker: markerJSON}); err != nil {
		t.Fatal(err)
	}
	binding := GenerationBinding{DeliveryID: "delivery-1", GenerationID: "generation-1", AttemptID: attemptID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41, RelationManifestDigest: digest('d'), CompatibilityDigest: digest('a'), ServingArtifactDigest: digest('e'), RequestDigest: digest('b'), PlanDigest: digest('c'), FencingEpoch: 7}
	staleBinding := binding
	staleBinding.GenerationID = "generation-successor"
	if _, err := r.BindGeneration(t.Context(), staleBinding); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale successor binding error = %v", err)
	}
	if _, err := r.BindGeneration(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	acquiredAt := time.Now().UTC()
	if _, err := r.AcquireSnapshotLease(t.Context(), AcquireLeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000003", DeliveryID: binding.DeliveryID, GenerationID: binding.GenerationID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41, OwnerID: "reader-a", FencingEpoch: 7, AcquiredAt: acquiredAt, ExpiresAt: acquiredAt.Add(25 * time.Hour)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded snapshot lease error = %v", err)
	}
	leaseID := "0198f2c0-7c7a-7f00-8a11-000000000002"
	lease, err := r.AcquireSnapshotLease(t.Context(), AcquireLeaseInput{LeaseID: leaseID, DeliveryID: binding.DeliveryID, GenerationID: binding.GenerationID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41, OwnerID: "reader-a", FencingEpoch: 7, AcquiredAt: acquiredAt, ExpiresAt: acquiredAt.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if lease.State != LeaseActive || lease.SnapshotID != 41 {
		t.Fatalf("lease = %#v", lease)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_lease SET snapshot_id=42 WHERE lease_id=$1`, leaseID); err == nil {
		t.Fatal("snapshot lease identity mutation was accepted")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.attempt_evidence SET plan_digest=$2 WHERE attempt_id=$1`, attemptID, digest('f')); err == nil {
		t.Fatal("attempt identity mutation was accepted")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET protected_until='epoch' WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 41); err == nil {
		t.Fatal("snapshot protection horizon moved backwards")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_lease SET expires_at=$2 WHERE lease_id=$1`, leaseID, acquiredAt.Add(30*time.Second)); err == nil {
		t.Fatal("active snapshot lease expiry moved backwards")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.attempt_evidence SET state='running' WHERE attempt_id=$1`, attemptID); err == nil {
		t.Fatal("terminal attempt lifecycle reopened")
	}
	if err := RenewSnapshotLease(t.Context(), p, LeaseFence{LeaseID: leaseID, OwnerID: "reader-a", FencingEpoch: 7}, acquiredAt.Add(3*time.Minute), acquiredAt.Add(2*time.Minute)); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("renew after lease expiry error = %v", err)
	}
	if err := r.RenewSnapshotLease(t.Context(), LeaseFence{LeaseID: leaseID, OwnerID: "reader-stale", FencingEpoch: 7}, acquiredAt.Add(2*time.Minute)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale renewal error = %v", err)
	}
	if err := r.RetireSnapshot(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, acquiredAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("retire with generation root error = %v", err)
	}
	// Releasing the durable generation root permits retirement even while an
	// existing query lease is still draining. Retirement blocks new leases;
	// expiration waits for the active lease to release.
	if err := r.ReleaseSnapshotRoot(t.Context(), attemptID, acquiredAt.Add(500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_root SET state='live',expired_at=NULL WHERE root_id=$1`, attemptID); err == nil {
		t.Fatal("expired snapshot root lifecycle reopened")
	}
	if err := r.RetireSnapshot(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, acquiredAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='expired',expired_at=$4 WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 41, acquiredAt.Add(1500*time.Millisecond)); err == nil {
		t.Fatal("snapshot expired while query lease remained active")
	}
	if _, err := r.AcquireSnapshotLease(t.Context(), AcquireLeaseInput{LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000003", DeliveryID: binding.DeliveryID, GenerationID: binding.GenerationID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41, OwnerID: "reader-b", FencingEpoch: 7, AcquiredAt: acquiredAt, ExpiresAt: acquiredAt.Add(time.Minute)}); !errors.Is(err, ErrNotLive) {
		t.Fatalf("lease after retirement error = %v", err)
	}
	if err := r.ReleaseSnapshotLease(t.Context(), LeaseFence{LeaseID: leaseID, OwnerID: "reader-a", FencingEpoch: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_lease SET expires_at=$2 WHERE lease_id=$1`, leaseID, acquiredAt.Add(5*time.Minute)); err == nil {
		t.Fatal("terminal snapshot lease was rewritten")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='live',retired_at=NULL WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 41); err == nil {
		t.Fatal("retiring snapshot lifecycle reopened")
	}
	// Root creation and retirement serialize on the retention row lock. The
	// retire transaction must wait for the uncommitted root, then observe it
	// and reject retirement instead of racing a live root onto a retiring row.
	lockRef := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 42}
	if err := ensureSnapshotLive(t.Context(), p, lockRef); err != nil {
		t.Fatal(err)
	}
	txRoot, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rootID := "0198f2c0-7c7a-7f00-8a11-000000000004"
	if err := createSnapshotRoot(t.Context(), txRoot, SnapshotRootInput{RootID: rootID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 42, Kind: RootCandidate, CreatedAt: acquiredAt}); err != nil {
		_ = txRoot.Rollback(t.Context())
		t.Fatal(err)
	}
	txRetire, err := p.Begin(t.Context())
	if err != nil {
		_ = txRoot.Rollback(t.Context())
		t.Fatal(err)
	}
	retireDone := make(chan error, 1)
	go func() {
		retireErr := RetireSnapshot(t.Context(), txRetire, lockRef, acquiredAt)
		if retireErr != nil {
			_ = txRetire.Rollback(t.Context())
		} else {
			retireErr = txRetire.Commit(t.Context())
		}
		retireDone <- retireErr
	}()
	select {
	case err := <-retireDone:
		_ = txRoot.Rollback(t.Context())
		t.Fatalf("retirement raced uncommitted root, err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := txRoot.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-retireDone; !errors.Is(err, ErrConflict) {
		t.Fatalf("retirement after serialized root err=%v", err)
	}
	if err := r.ReleaseSnapshotRoot(t.Context(), rootID, acquiredAt.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), lockRef, acquiredAt.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := r.ExpireSnapshot(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, json.RawMessage(`{"external_expiration":"verified"}`), acquiredAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileRequiresPositiveTerminationEvidence(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_reconcile_test")
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
	const poolID, catalogID = "pool-reconcile", "catalog-reconcile"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	lookupDB, err := sql.Open("sqlite", "file:ducklake_reconcile_lookup?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lookupDB.Close() })
	if _, err := lookupDB.ExecContext(t.Context(), `CREATE TABLE fake_last_committed_snapshot (id INTEGER); CREATE TABLE fake_snapshots (snapshot_id INTEGER, commit_extra_info TEXT);`); err != nil {
		t.Fatal(err)
	}
	lookup := sqliteSnapshotLookup{db: lookupDB}

	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000010"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder", FencingEpoch: 1, SessionIdentity: "session", LeaseExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-reconcile", GenerationID: "generation-reconcile", AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project", Environment: "prod", PhysicalPoolID: poolID}
	got, err := r.ReconcileExternalAttempt(t.Context(), ExternalAttemptReconciliation{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, Marker: marker, Snapshot: SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 99}, Local: lookup, SessionTerminated: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AttemptIndeterminate {
		t.Fatalf("missing termination evidence state = %s, want indeterminate", got.State)
	}

	attemptID = "0198f2c0-7c7a-7f00-8a11-000000000011"
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder", FencingEpoch: 1, SessionIdentity: "session-2", LeaseExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	marker.AttemptID = attemptID
	got, err = r.ReconcileExternalAttempt(t.Context(), ExternalAttemptReconciliation{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, Marker: marker, Snapshot: SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 100}, Local: lookup, SessionTerminated: true, TerminationEvidence: json.RawMessage(`{"session":"terminated","exit_code":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AttemptAborted {
		t.Fatalf("positive termination evidence state = %s, want aborted", got.State)
	}
	if _, err := r.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, Evidence: json.RawMessage(`{"session":"different"}`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal evidence rewrite error = %v", err)
	}
	if _, err := r.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "stale-builder", FencingEpoch: 1, Evidence: json.RawMessage(`{"session":"terminated","exit_code":1}`)}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("terminal stale owner error = %v", err)
	}
}

func TestPostgres18CleanupClaimFencesStaleWorkers(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_cleanup_claim_test")
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
	const poolID, catalogID = "cleanup-pool", "cleanup-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 77}
	if err := ensureSnapshotLive(t.Context(), p, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.ExpireSnapshot(t.Context(), ref, json.RawMessage(`{"expired":"verified"}`), time.Time{}); err != nil {
		t.Fatal(err)
	}
	var dbNow time.Time
	if err := p.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	fenceA, err := r.ClaimSnapshotCleanup(t.Context(), ref, "cleanup-a", dbNow.Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := r.ClaimSnapshotCleanup(t.Context(), ref, "cleanup-a", dbNow.Add(time.Second)); err != nil || replay.FencingEpoch != fenceA.FencingEpoch {
		t.Fatalf("same-owner claim replay=%#v err=%v", replay, err)
	}
	if _, err := r.ClaimSnapshotCleanup(t.Context(), ref, "cleanup-b", dbNow.Add(time.Second)); !errors.Is(err, ErrCleanupBusy) {
		t.Fatalf("active-owner contention err=%v", err)
	}
	time.Sleep(200 * time.Millisecond)
	fenceB, err := r.ClaimSnapshotCleanup(t.Context(), ref, "cleanup-b", time.Now().Add(time.Second))
	if err != nil || fenceB.FencingEpoch <= fenceA.FencingEpoch {
		t.Fatalf("successor cleanup claim=%#v err=%v", fenceB, err)
	}
	if err := r.QuarantineSnapshot(t.Context(), ref, json.RawMessage(`{"worker":"b"}`), fenceA); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale quarantine err=%v", err)
	}
	if err := r.QuarantineSnapshot(t.Context(), ref, json.RawMessage(`{"worker":"b"}`), fenceB); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteSnapshotCleanup(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceA); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale completion err=%v", err)
	}
	if err := r.CompleteSnapshotCleanup(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := r.CompleteSnapshotCleanup(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceB); err != nil {
		t.Fatalf("exact completion replay err=%v", err)
	}
	retained, err := r.LoadSnapshotRetention(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if retained.State != RetentionCleanupComplete || retained.QuarantinedAt.IsZero() || retained.CleanupCompletedAt.IsZero() {
		t.Fatalf("retention=%#v", retained)
	}
	if !evidenceEqual(retained.Evidence, `{"expired":"verified"}`) || !evidenceEqual(retained.QuarantineEvidence, `{"worker":"b"}`) || !evidenceEqual(retained.CleanupEvidence, `{"worker":"b","deleted":true}`) {
		t.Fatalf("phase evidence was not retained: expiration=%s (%v) quarantine=%s (%v) cleanup=%s (%v)", retained.Evidence, evidenceEqual(retained.Evidence, `{"expired":"verified"}`), retained.QuarantineEvidence, evidenceEqual(retained.QuarantineEvidence, `{"worker":"b"}`), retained.CleanupEvidence, evidenceEqual(retained.CleanupEvidence, `{"worker":"b","deleted":true}`))
	}
}

func TestPostgres18OrphanCleanupClaimFencesStaleWorkers(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_orphan_cleanup_claim_test")
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
	const poolID, catalogID = "orphan-cleanup-pool", "orphan-cleanup-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	const orphanID = "0198f2c0-7c7a-7f00-8a11-000000000077"
	orphan, err := r.RecordSnapshotOrphan(t.Context(), SnapshotOrphanInput{OrphanID: orphanID, PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 88, Evidence: json.RawMessage(`{"source":"scan"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if orphan.State != "quarantined" || orphan.CleanupFencingEpoch != 0 {
		t.Fatalf("orphan=%#v", orphan)
	}
	var dbNow time.Time
	if err := p.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	fenceA, err := r.ClaimSnapshotOrphanCleanup(t.Context(), orphanID, "orphan-cleanup-a", dbNow.Add(100*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := r.ClaimSnapshotOrphanCleanup(t.Context(), orphanID, "orphan-cleanup-a", dbNow.Add(time.Second)); err != nil || replay.FencingEpoch != fenceA.FencingEpoch {
		t.Fatalf("same-owner orphan claim replay=%#v err=%v", replay, err)
	}
	if _, err := r.ClaimSnapshotOrphanCleanup(t.Context(), orphanID, "orphan-cleanup-b", dbNow.Add(time.Second)); !errors.Is(err, ErrCleanupBusy) {
		t.Fatalf("active-owner orphan contention err=%v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	fenceB, err := r.ClaimSnapshotOrphanCleanup(t.Context(), orphanID, "orphan-cleanup-b", dbNow.Add(time.Second))
	if err != nil || fenceB.FencingEpoch <= fenceA.FencingEpoch {
		t.Fatalf("successor orphan cleanup claim=%#v err=%v", fenceB, err)
	}
	if err := r.CompleteSnapshotOrphanCleanup(t.Context(), orphanID, json.RawMessage(`{"worker":"b","deleted":true}`), time.Time{}, fenceA); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale orphan completion err=%v", err)
	}
	if err := r.CompleteSnapshotOrphanCleanup(t.Context(), orphanID, json.RawMessage(`{"worker":"b","deleted":true}`), time.Time{}, fenceB); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := r.CompleteSnapshotOrphanCleanup(t.Context(), orphanID, json.RawMessage(`{"worker":"b","deleted":true}`), time.Time{}, fenceB); err != nil {
		t.Fatalf("exact orphan completion replay err=%v", err)
	}
	orphans, err := r.ListSnapshotOrphans(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].State != "cleanup-complete" || orphans[0].CleanupOwnerID != fenceB.OwnerID || orphans[0].CleanupFencingEpoch != fenceB.FencingEpoch || !evidenceEqual(orphans[0].Evidence, `{"worker":"b","deleted":true}`) {
		t.Fatalf("orphans=%#v", orphans)
	}
}

func TestPostgres18DuckLakeControlRoleGrants(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_control_role_test")
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
	var publicSchema, publicTable, publicFunction, runtimeDelete, runtimeFunction, readonlyInsert bool
	if err := admin.QueryRow(t.Context(), `
SELECT has_schema_privilege('public', 'ducklake', 'USAGE'),
       has_table_privilege('public', 'ducklake.catalog_identity', 'SELECT'),
       has_function_privilege('public', 'ducklake.reject_immutable_change()', 'EXECUTE'),
       has_table_privilege('leapview_control_runtime', 'ducklake.catalog_identity', 'DELETE'),
       has_function_privilege('leapview_control_runtime', 'ducklake.reject_immutable_change()', 'EXECUTE'),
       has_table_privilege('leapview_control_readonly', 'ducklake.catalog_identity', 'INSERT')`).
		Scan(&publicSchema, &publicTable, &publicFunction, &runtimeDelete, &runtimeFunction, &readonlyInsert); err != nil {
		t.Fatal(err)
	}
	if publicSchema || publicTable || publicFunction || runtimeDelete || runtimeFunction || readonlyInsert {
		t.Fatalf("DuckLake role grants leaked: public schema=%t table=%t function=%t runtime delete=%t function=%t readonly insert=%t", publicSchema, publicTable, publicFunction, runtimeDelete, runtimeFunction, readonlyInsert)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtime := New(runtimeDB)
	const poolID, catalogID = "role-grant-pool", "role-grant-catalog"
	if _, err := runtime.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatalf("runtime repository path: %v", err)
	}
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000099"
	if _, err := runtime.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: digest('b'), PlanDigest: digest('c'), PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "runtime-worker", FencingEpoch: 1, SessionIdentity: "role-test", LeaseExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatalf("runtime begin attempt: %v", err)
	}
	if _, err := runtime.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "runtime-worker", FencingEpoch: 1, Evidence: json.RawMessage(`{"reason":"role-test"}`)}); err != nil {
		t.Fatalf("runtime terminate attempt: %v", err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE ducklake.catalog_identity SET catalog_id='tampered' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("runtime immutable identity update unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `DELETE FROM ducklake.catalog_identity WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("runtime catalog identity delete unexpectedly succeeded")
	}
	rows, err := runtimeDB.Query(t.Context(), `SELECT lease_id FROM ducklake.snapshot_reader_drain LIMIT 0`)
	if err != nil {
		t.Fatalf("runtime drain view select: %v", err)
	}
	rows.Close()
	readonlyDB, err := pgxpool.New(t.Context(), db.URL(readonlyRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonlyDB.Close)
	readonly := New(readonlyDB)
	if _, err := readonly.LoadCatalog(t.Context(), poolID); err != nil {
		t.Fatalf("readonly catalog select: %v", err)
	}
	if _, err := readonlyDB.Exec(t.Context(), `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema,compatibility_digest,catalog_schema_version) VALUES ('readonly-pool','ducklake','readonly-catalog','0198f2c0-7c7a-7f00-8a11-000000000010','lake',$1,'ducklake-v1')`, digest('d')); err == nil {
		t.Fatal("readonly catalog insert unexpectedly succeeded")
	}
}
