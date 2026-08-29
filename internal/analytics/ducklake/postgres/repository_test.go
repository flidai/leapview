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
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: "pool-2", CatalogID: "catalog-2", MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
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
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogID: catalogID, MetadataSchema: "lake", CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}); err != nil {
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
