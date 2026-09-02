package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

func digest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

const testCatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000001"

func TestValidationRejectsUnboundedOrCrossPoolIdentity(t *testing.T) {
	if err := validateCatalog(CatalogIdentity{PhysicalPoolID: " ", CatalogID: "catalog", MetadataSchema: "lake"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid catalog accepted: %v", err)
	}
	if err := validateBinding(GenerationBinding{DeliveryID: "delivery", GenerationID: "generation", AttemptID: "not-a-uuid", PhysicalPoolID: "pool", CatalogID: "catalog", SnapshotID: 1, RelationManifestDigest: digest('a'), CompatibilityDigest: digest('b'), ServingArtifactDigest: digest('c'), RequestDigest: digest('d'), PlanDigest: digest('e'), FencingEpoch: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid generation binding accepted: %v", err)
	}
	if _, err := canonicalEvidence(json.RawMessage(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate termination evidence accepted")
	}
}

func TestPostgres18MarkerQuarantineIsImmutableAndGatesAttempts(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_marker_quarantine_test")
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
	const poolID, catalogID = "quarantine-pool", "quarantine-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000701", MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000000702"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "quarantine-owner", FencingEpoch: 1, SessionIdentity: "quarantine-session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MarkAttemptIndeterminate(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "quarantine-owner", FencingEpoch: 1, Evidence: json.RawMessage(`{"indeterminate":true}`)}); err != nil {
		t.Fatal(err)
	}
	input := MarkerQuarantineInput{PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, Reason: MarkerQuarantineDigestMismatch, Evidence: json.RawMessage(`{"observed_snapshot_ids":[41],"reason":"digest_mismatch"}`), ObservedMarkerDigest: digest('d'), ObservedSnapshotIDs: []int64{41}}
	first, err := r.QuarantineMarker(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reason != MarkerQuarantineDigestMismatch || first.CreatedAt.IsZero() || len(first.ObservedSnapshotIDs) != 1 || first.ObservedSnapshotIDs[0] != 41 {
		t.Fatalf("quarantine row = %#v", first)
	}
	if replay, err := r.QuarantineMarker(t.Context(), input); err != nil || replay.CreatedAt.IsZero() || replay.Reason != first.Reason {
		t.Fatalf("exact quarantine replay = %#v, %v", replay, err)
	}
	changed := input
	changed.Evidence = json.RawMessage(`{"observed_snapshot_ids":[42],"reason":"digest_mismatch"}`)
	if _, err := r.QuarantineMarker(t.Context(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed quarantine replay error = %v, want ErrConflict", err)
	}
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: "0198f2c0-7c7a-7f00-0000-000000000703", RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "successor", FencingEpoch: 2, SessionIdentity: "successor-session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); !errors.Is(err, ErrMarkerQuarantined) {
		t.Fatalf("successor admission error = %v, want ErrMarkerQuarantined", err)
	}
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-quarantine", GenerationID: "generation-quarantine", AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project-quarantine", Environment: "prod", PhysicalPoolID: poolID}
	markerJSON, err := marker.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReconcileAttempt(t.Context(), ReconcileAttemptInput{AttemptID: attemptID, OwnerID: "quarantine-owner", FencingEpoch: 1, Snapshot: SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, CommitMarker: markerJSON, State: AttemptCommitted}); !errors.Is(err, ErrMarkerQuarantined) {
		t.Fatalf("quarantine recovery gate error = %v, want ErrMarkerQuarantined", err)
	}
}

func TestPostgres18MarkerQuarantineSerializesAdmissionAtPoolScope(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_marker_quarantine_lock_test")
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
	const poolID, catalogID = "quarantine-lock-pool", "quarantine-lock-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-0000-000000000711", MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-0000-000000000712"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "lock-owner", FencingEpoch: 1, SessionIdentity: "lock-session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	lockTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	if err := lockMarkerQuarantineScope(t.Context(), lockTx, poolID); err != nil {
		t.Fatal(err)
	}
	if _, err := QuarantineMarker(t.Context(), lockTx, MarkerQuarantineInput{PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, Reason: MarkerQuarantineDuplicate, Evidence: json.RawMessage(`{"anomaly":"duplicate"}`), ObservedMarkerDigest: digest('d'), ObservedSnapshotIDs: []int64{101}}); err != nil {
		t.Fatal(err)
	}
	admissionDone := make(chan error, 1)
	go func() {
		_, admissionErr := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: "0198f2c0-7c7a-0000-0000-000000000713", RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "successor", FencingEpoch: 2, SessionIdentity: "successor-session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)})
		admissionDone <- admissionErr
	}()
	select {
	case admissionErr := <-admissionDone:
		t.Fatalf("admission completed before quarantine commit: %v", admissionErr)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lockTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case admissionErr := <-admissionDone:
		if !errors.Is(admissionErr, ErrMarkerQuarantined) {
			t.Fatalf("serialized admission error = %v, want ErrMarkerQuarantined", admissionErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serialized admission did not complete after quarantine commit")
	}
}

func TestPostgres18BeginAttemptRejectsActiveRetentionOwner(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "begin_retention_busy")
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-owner", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence) })
	_, err = r.BeginAttempt(t.Context(), BeginAttemptInput{
		AttemptID: "0198f2c0-7c7a-0000-0000-000000000714", RequestDigest: digest('b'), PlanDigest: digest('c'),
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "build-owner", FencingEpoch: 1,
		SessionIdentity: "build-session", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrRetentionMaintenanceBusy) {
		t.Fatalf("admission with active retention owner = %v, want ErrRetentionMaintenanceBusy", err)
	}
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{
		AttemptID: "0198f2c0-7c7a-0000-0000-000000000715", RequestDigest: digest('b'), PlanDigest: digest('c'),
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "build-owner", FencingEpoch: 1,
		SessionIdentity: "build-session", LeaseExpiresAt: time.Now().Add(time.Minute),
	}); err == nil {
		t.Fatal("admission unexpectedly succeeded while retention owner remained active")
	}
}

func TestPostgres18MaintenanceFenceRejectsRunningAttempt(t *testing.T) {
	r, _, poolID, catalogID := retentionTestRepository(t, "begin_retention_replay")
	in := BeginAttemptInput{
		AttemptID:       "0198f2c0-7c7a-0000-0000-000000000723",
		RequestDigest:   digest('b'),
		PlanDigest:      digest('c'),
		PhysicalPoolID:  poolID,
		CatalogID:       catalogID,
		OwnerID:         "build-owner",
		FencingEpoch:    1,
		SessionIdentity: "build-session",
		LeaseExpiresAt:  time.Now().UTC().Add(time.Minute),
	}
	first, err := r.BeginAttempt(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "retention-owner",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}); !errors.Is(err, ErrRetentionMaintenanceBusy) {
		t.Fatalf("maintenance fence with running attempt = %v, want ErrRetentionMaintenanceBusy", err)
	}
	if _, err := r.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: first.AttemptID, OwnerID: first.OwnerID, FencingEpoch: first.FencingEpoch, Evidence: json.RawMessage(`{"aborted":"test"}`)}); err != nil {
		t.Fatalf("abort running attempt: %v", err)
	}
}

func TestPostgres18BeginAttemptWaitsOnRetentionRowLock(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "begin_retention_lock")
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-owner", LeaseExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}
	lockTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	if _, err := lockTx.Exec(t.Context(), `SELECT 1 FROM ducklake.pool_maintenance_fence WHERE physical_pool_id=$1 AND catalog_id=$2 FOR UPDATE`, poolID, catalogID); err != nil {
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, beginErr := r.BeginAttempt(shortCtx, BeginAttemptInput{
			AttemptID: "0198f2c0-7c7a-0000-0000-000000000716", RequestDigest: digest('b'), PlanDigest: digest('c'),
			PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "build-owner", FencingEpoch: 1,
			SessionIdentity: "build-session", LeaseExpiresAt: time.Now().Add(time.Minute),
		})
		done <- beginErr
	}()
	select {
	case beginErr := <-done:
		if !errors.Is(beginErr, context.DeadlineExceeded) {
			t.Fatalf("admission while retention row held = %v, want deadline", beginErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("admission did not honor lock timeout")
	}
}

func TestPostgres18RuntimeRoleCanUseMarkerQuarantineLockAndInsert(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-quarantine-secret", Login: true})
	db := h.NewDatabase(t, "ducklake_marker_quarantine_role_test")
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
	var runtimeIdentityUpdate bool
	if err := admin.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_runtime', 'ducklake.catalog_identity', 'UPDATE')`).Scan(&runtimeIdentityUpdate); err != nil {
		t.Fatal(err)
	}
	if runtimeIdentityUpdate {
		t.Fatal("runtime role retained catalog identity UPDATE privilege")
	}
	const poolID, catalogID = "quarantine-role-pool", "quarantine-role-catalog"
	adminRepo := New(admin)
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-0000-0000-000000000721", MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtimeRepo := New(runtimeDB)
	const attemptID = "0198f2c0-7c7a-0000-0000-000000000722"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := runtimeRepo.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "runtime-owner", FencingEpoch: 1, SessionIdentity: "runtime-session", LeaseExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeRepo.QuarantineMarker(t.Context(), MarkerQuarantineInput{PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, Reason: MarkerQuarantineIdentityMismatch, Evidence: json.RawMessage(`{"anomaly":"identity_mismatch"}`), ObservedMarkerDigest: digest('d'), ObservedSnapshotIDs: []int64{102}}); err != nil {
		t.Fatalf("runtime role marker quarantine insert = %v", err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE ducklake.catalog_identity SET catalog_id='tampered' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("runtime catalog identity mutation unexpectedly succeeded")
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
	identity := CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake"}
	compatibility := RuntimeCompatibility{RuntimeTuple: RuntimeTuple{DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:1.0.0", CatalogFormat: "ducklake:1.0"}, CompatibilityDigest: digest('a'), CatalogSchemaVersion: "ducklake-v1"}
	registered, registeredCompatibility, err := BootstrapCatalog(t.Context(), p, identity, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: compatibility}); !errors.Is(err, ErrRuntimeAttachIneligible) {
		t.Fatalf("unqualified bootstrap attach error = %v, want ErrRuntimeAttachIneligible", err)
	}
	qualifyBootstrap := func(input CatalogBootstrapQualificationInput) (CatalogRuntimeCompatibility, error) {
		tx, err := p.Begin(t.Context())
		if err != nil {
			return CatalogRuntimeCompatibility{}, err
		}
		qualified, qualifyErr := QualifyCatalogBootstrap(t.Context(), tx, input)
		if qualifyErr != nil {
			_ = tx.Rollback(t.Context())
			return CatalogRuntimeCompatibility{}, qualifyErr
		}
		if err := tx.Commit(t.Context()); err != nil {
			return CatalogRuntimeCompatibility{}, err
		}
		return qualified, nil
	}
	qualified, err := qualifyBootstrap(CatalogBootstrapQualificationInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "bootstrap-owner", Compatibility: compatibility,
		BeginEvidence:      []byte(`{"bootstrap":true,"drain_verified":true,"backup_verified":true}`),
		CompletionEvidence: []byte(`{"bootstrap":true,"catalog_registration_verified":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if qualified.CurrentMigrationID == "" {
		t.Fatal("bootstrap qualification did not establish a completed epoch")
	}
	if eligibility, err := r.CheckRuntimeAttachEligibility(t.Context(), RuntimeAttachInput{PhysicalPoolID: poolID, CatalogID: catalogID, Compatibility: compatibility}); err != nil || !eligibility.Eligible {
		t.Fatalf("qualified bootstrap attach eligibility = %#v, err %v", eligibility, err)
	}
	if replay, err := qualifyBootstrap(CatalogBootstrapQualificationInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "bootstrap-owner", Compatibility: compatibility,
		BeginEvidence:      []byte(`{"bootstrap":true,"drain_verified":true,"backup_verified":true}`),
		CompletionEvidence: []byte(`{"bootstrap":true,"catalog_registration_verified":true}`),
	}); err != nil || replay.CurrentMigrationID != qualified.CurrentMigrationID {
		t.Fatalf("bootstrap qualification replay = %#v, err %v", replay, err)
	}
	if _, err := qualifyBootstrap(CatalogBootstrapQualificationInput{
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "bootstrap-owner", Compatibility: compatibility,
		BeginEvidence:      []byte(`{"bootstrap":true,"drain_verified":true,"backup_verified":true}`),
		CompletionEvidence: []byte(`{"bootstrap":true,"catalog_registration_verified":true,"changed":true}`),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed bootstrap qualification replay error = %v, want ErrConflict", err)
	}
	if replayCatalog, replayCompatibility, replayErr := BootstrapCatalog(t.Context(), p, identity, compatibility); replayErr != nil || !sameCatalog(replayCatalog, registered) || !sameRuntimeCompatibility(replayCompatibility.RuntimeCompatibility, registeredCompatibility.RuntimeCompatibility) {
		t.Fatalf("exact catalog bootstrap replay = catalog %#v runtime %#v err %v", replayCatalog, replayCompatibility, replayErr)
	}
	for label, mutate := range map[string]func(*RuntimeCompatibility){
		"DuckDB runtime":     func(v *RuntimeCompatibility) { v.DuckDBRuntime = "duckdb:1.6.0" },
		"DuckLake extension": func(v *RuntimeCompatibility) { v.DuckLakeExtension = "ducklake:1.1.0" },
		"catalog format":     func(v *RuntimeCompatibility) { v.CatalogFormat = "ducklake:1.1" },
	} {
		conflict := compatibility
		mutate(&conflict)
		if _, _, conflictErr := BootstrapCatalog(t.Context(), p, identity, conflict); !errors.Is(conflictErr, ErrConflict) {
			t.Fatalf("changed bootstrap %s error = %v, want ErrConflict", label, conflictErr)
		}
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
		"database":        func(v *CatalogIdentity) { v.CatalogDatabase = "other_ducklake" },
		"catalog id":      func(v *CatalogIdentity) { v.CatalogID = "catalog-other" },
		"uuid":            func(v *CatalogIdentity) { v.CatalogUUID = "0198f2c0-7c7a-7f00-8a11-000000000099" },
		"metadata schema": func(v *CatalogIdentity) { v.MetadataSchema = "lake_other" },
	} {
		conflict := identity
		mutate(&conflict)
		if _, err := r.RegisterCatalog(t.Context(), conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("immutable catalog %s replay error = %v, want ErrConflict", label, err)
		}
	}
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: "pool-2", CatalogDatabase: "ducklake", CatalogID: "catalog-2", CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000002", MetadataSchema: "lake"}); err != nil {
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
	maintenanceFence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-test", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	maintenanceID := "0198f2c0-7c7a-7f00-0000-000000000091"
	operation, err := startAndPrepareRetentionMaintenance(t.Context(), r.db, RetentionMaintenance{MaintenanceID: maintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: maintenanceFence.OwnerID, FencingEpoch: maintenanceFence.FencingEpoch, State: "running", Phase: "expiry", FileGraceMicros: int64(time.Hour / time.Microsecond)})
	if err != nil {
		t.Fatal(err)
	}
	if operation.SnapshotSetDigest == "" {
		t.Fatal("fenced retention operation did not freeze snapshot set")
	}
	if err := r.ExpireSnapshotUnderMaintenanceFence(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 41}, json.RawMessage(`{"external_expiration":"verified"}`), time.Time{}, maintenanceID, maintenanceFence); err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), maintenanceFence); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAttemptRequiresClosedTerminationEvidenceAndProtectsIndeterminateState(t *testing.T) {
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
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000000010"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := r.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder", FencingEpoch: 1, SessionIdentity: "session", LeaseExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MarkAttemptIndeterminate(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, Evidence: json.RawMessage(`{"reason":"marker-outcome-unknown"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReconcileAttempt(t.Context(), ReconcileAttemptInput{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, SessionIdentity: "session", SessionTerminated: true, TerminationEvidence: json.RawMessage(`{"session":"terminated"}`), State: AttemptAborted}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("open termination evidence error = %v, want invalid", err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.attempt_evidence SET termination_evidence='{"tampered":true}'::jsonb,updated_at=clock_timestamp(),terminal_at=clock_timestamp() WHERE attempt_id=$1`, attemptID); err == nil {
		t.Fatal("indeterminate attempt evidence rewrite was accepted")
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.attempt_evidence SET snapshot_id=99 WHERE attempt_id=$1`, attemptID); err == nil {
		t.Fatal("indeterminate attempt accepted commit snapshot evidence")
	}

	evidence := json.RawMessage(`{"schema_version":1,"attempt_id":"` + attemptID + `","owner_id":"builder","fencing_epoch":1,"session_identity":"session","session_terminated":true}`)
	in := ReconcileAttemptInput{AttemptID: attemptID, OwnerID: "builder", FencingEpoch: 1, SessionIdentity: "session", SessionTerminated: true, TerminationEvidence: evidence, State: AttemptAborted}
	got, err := r.ReconcileAttempt(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AttemptAborted {
		t.Fatalf("positive termination evidence state = %s, want aborted", got.State)
	}
	if replay, err := r.ReconcileAttempt(t.Context(), in); err != nil || replay.State != AttemptAborted {
		t.Fatalf("exact reconciliation replay = %#v, %v", replay, err)
	}
	staleEvidence := json.RawMessage(`{"schema_version":1,"attempt_id":"` + attemptID + `","owner_id":"stale-builder","fencing_epoch":1,"session_identity":"session","session_terminated":true}`)
	if _, err := r.ReconcileAttempt(t.Context(), ReconcileAttemptInput{AttemptID: attemptID, OwnerID: "stale-builder", FencingEpoch: 1, SessionIdentity: "session", SessionTerminated: true, TerminationEvidence: staleEvidence, State: AttemptAborted}); !errors.Is(err, ErrStaleFence) {
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
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	ref := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 77}
	if err := ensureSnapshotLive(t.Context(), p, ref); err != nil {
		t.Fatal(err)
	}
	if err := r.RetireSnapshot(t.Context(), ref, time.Time{}); err != nil {
		t.Fatal(err)
	}
	maintenanceFence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "retention-test", LeaseExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	maintenanceID := "0198f2c0-7c7a-7f00-0000-000000000092"
	if _, err := startAndPrepareRetentionMaintenance(t.Context(), r.db, RetentionMaintenance{MaintenanceID: maintenanceID, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: maintenanceFence.OwnerID, FencingEpoch: maintenanceFence.FencingEpoch, State: "running", Phase: "expiry", FileGraceMicros: int64(time.Hour / time.Microsecond)}); err != nil {
		t.Fatal(err)
	}
	if err := r.ExpireSnapshotUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"expired":"verified"}`), time.Time{}, maintenanceID, maintenanceFence); err != nil {
		t.Fatal(err)
	}
	var dbNow time.Time
	if err := p.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&dbNow); err != nil {
		t.Fatal(err)
	}
	fenceA, err := r.ClaimSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, "cleanup-a", dbNow.Add(100*time.Millisecond), maintenanceID, maintenanceFence)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := r.ClaimSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, "cleanup-a", dbNow.Add(time.Second), maintenanceID, maintenanceFence); err != nil || replay.FencingEpoch != fenceA.FencingEpoch {
		t.Fatalf("same-owner claim replay=%#v err=%v", replay, err)
	}
	if _, err := r.ClaimSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, "cleanup-b", dbNow.Add(time.Second), maintenanceID, maintenanceFence); !errors.Is(err, ErrCleanupBusy) {
		t.Fatalf("active-owner contention err=%v", err)
	}
	time.Sleep(200 * time.Millisecond)
	fenceB, err := r.ClaimSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, "cleanup-b", time.Now().Add(time.Second), maintenanceID, maintenanceFence)
	if err != nil || fenceB.FencingEpoch <= fenceA.FencingEpoch {
		t.Fatalf("successor cleanup claim=%#v err=%v", fenceB, err)
	}
	if err := r.QuarantineSnapshotUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"worker":"b"}`), fenceA, maintenanceID, maintenanceFence); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale quarantine err=%v", err)
	}
	if err := r.QuarantineSnapshotUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"worker":"b"}`), fenceB, maintenanceID, maintenanceFence); err != nil {
		t.Fatal(err)
	}
	if err := r.CompleteSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceA, maintenanceID, maintenanceFence); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("stale completion err=%v", err)
	}
	if err := r.CompleteSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceB, maintenanceID, maintenanceFence); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := r.CompleteSnapshotCleanupUnderMaintenanceFence(t.Context(), ref, json.RawMessage(`{"worker":"b","deleted":true}`), fenceB, maintenanceID, maintenanceFence); err != nil {
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
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), maintenanceFence); err != nil {
		t.Fatal(err)
	}
}

func TestPostgres18FirstUseMaintenanceAndAttemptAdmissionAreSerialized(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "first_use_race")
	start := make(chan struct{})
	attemptDone := make(chan error, 1)
	fenceDone := make(chan struct {
		fence RetentionMaintenanceFence
		err   error
	}, 1)
	go func() {
		<-start
		_, err := r.BeginAttempt(t.Context(), BeginAttemptInput{
			AttemptID: "0198f2c0-7c7a-0000-0000-000000000724", RequestDigest: digest('b'), PlanDigest: digest('c'),
			PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "first-use-attempt", FencingEpoch: 1,
			SessionIdentity: "first-use-session", LeaseExpiresAt: time.Now().Add(time.Minute),
		})
		attemptDone <- err
	}()
	go func() {
		<-start
		fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
			PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "first-use-maintenance", LeaseExpiresAt: time.Now().Add(time.Minute),
		})
		fenceDone <- struct {
			fence RetentionMaintenanceFence
			err   error
		}{fence: fence, err: err}
	}()
	close(start)
	attemptErr := <-attemptDone
	fenceResult := <-fenceDone
	if (attemptErr == nil) == (fenceResult.err == nil) {
		t.Fatalf("first-use admission outcomes attempt=%v maintenance=%v; exactly one must win", attemptErr, fenceResult.err)
	}
	if attemptErr == nil {
		if _, err := r.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: "0198f2c0-7c7a-0000-0000-000000000724", OwnerID: "first-use-attempt", FencingEpoch: 1, Evidence: json.RawMessage(`{"aborted":"race"}`)}); err != nil {
			t.Fatalf("abort first-use attempt: %v", err)
		}
	}
	if fenceResult.err == nil {
		if err := r.ReleaseRetentionMaintenanceFence(t.Context(), fenceResult.fence); err != nil {
			t.Fatalf("release first-use fence: %v", err)
		}
	}
	var rows int
	if err := p.QueryRow(t.Context(), `SELECT count(*) FROM ducklake.pool_maintenance_fence WHERE physical_pool_id=$1 AND catalog_id=$2`, poolID, catalogID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("first-use maintenance row count=%d, want 1", rows)
	}
}

func TestPostgres18DuckLakeControlRoleGrants(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "readonly-secret", Login: true})
	_ = h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "maintenance-secret", Login: true})
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
	var publicSchema, publicTable, publicFunction, runtimeDelete, runtimeFunction, runtimeAdmissionFunction, runtimeOrphanInsert, runtimeOrphanUpdate, maintenanceAdmissionFunction, maintenanceOrphanInsert, readonlyAdmissionFunction, readonlyInsert, readonlyOrphanUpdate bool
	if err := admin.QueryRow(t.Context(), `
SELECT has_schema_privilege('public', 'ducklake', 'USAGE'),
       has_table_privilege('public', 'ducklake.catalog_identity', 'SELECT'),
       has_function_privilege('public', 'ducklake.reject_immutable_change()', 'EXECUTE'),
       has_table_privilege('leapview_control_runtime', 'ducklake.catalog_identity', 'DELETE'),
       has_function_privilege('leapview_control_runtime', 'ducklake.reject_immutable_change()', 'EXECUTE'),
       has_function_privilege('leapview_control_runtime', 'ducklake.assert_attempt_admission_fence(text,text)', 'EXECUTE'),
       has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_orphan', 'INSERT'),
       has_table_privilege('leapview_control_runtime', 'ducklake.snapshot_orphan', 'UPDATE'),
       has_function_privilege('leapview_control_maintenance', 'ducklake.assert_attempt_admission_fence(text,text)', 'EXECUTE'),
       has_table_privilege('leapview_control_maintenance', 'ducklake.snapshot_orphan', 'INSERT'),
       has_function_privilege('leapview_control_readonly', 'ducklake.assert_attempt_admission_fence(text,text)', 'EXECUTE'),
       has_table_privilege('leapview_control_readonly', 'ducklake.catalog_identity', 'INSERT'),
       has_table_privilege('leapview_control_readonly', 'ducklake.snapshot_orphan', 'UPDATE')`).
		Scan(&publicSchema, &publicTable, &publicFunction, &runtimeDelete, &runtimeFunction, &runtimeAdmissionFunction, &runtimeOrphanInsert, &runtimeOrphanUpdate, &maintenanceAdmissionFunction, &maintenanceOrphanInsert, &readonlyAdmissionFunction, &readonlyInsert, &readonlyOrphanUpdate); err != nil {
		t.Fatal(err)
	}
	if publicSchema || publicTable || publicFunction || runtimeDelete || runtimeFunction || !runtimeAdmissionFunction || runtimeOrphanInsert || runtimeOrphanUpdate || maintenanceAdmissionFunction || maintenanceOrphanInsert || readonlyAdmissionFunction || readonlyInsert || readonlyOrphanUpdate {
		t.Fatalf("DuckLake role grants leaked: public schema=%t table=%t function=%t runtime delete=%t function=%t admission=%t runtime orphan insert=%t update=%t maintenance admission=%t orphan insert=%t readonly admission=%t insert=%t update=%t", publicSchema, publicTable, publicFunction, runtimeDelete, runtimeFunction, runtimeAdmissionFunction, runtimeOrphanInsert, runtimeOrphanUpdate, maintenanceAdmissionFunction, maintenanceOrphanInsert, readonlyAdmissionFunction, readonlyInsert, readonlyOrphanUpdate)
	}
	var runtimeCatalogIdentitySelect, runtimeGenerationBindingSelect bool
	if err := admin.QueryRow(t.Context(), `
		SELECT has_table_privilege('leapview_control_runtime', 'ducklake.catalog_identity', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'ducklake.generation_binding', 'SELECT')`).
		Scan(&runtimeCatalogIdentitySelect, &runtimeGenerationBindingSelect); err != nil {
		t.Fatal(err)
	}
	if !runtimeCatalogIdentitySelect || !runtimeGenerationBindingSelect {
		t.Fatalf("DuckLake runtime identity reads missing: catalog_identity=%t generation_binding=%t", runtimeCatalogIdentitySelect, runtimeGenerationBindingSelect)
	}
	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtime := New(runtimeDB)
	const poolID, catalogID = "role-grant-pool", "role-grant-catalog"
	if _, err := runtime.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake"}); err != nil {
		t.Fatalf("runtime repository path: %v", err)
	}
	attemptID := "0198f2c0-7c7a-7f00-8a11-000000000099"
	requestDigest, planDigest := digest('b'), digest('c')
	if _, err := runtime.BeginAttempt(t.Context(), BeginAttemptInput{AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "runtime-worker", FencingEpoch: 1, SessionIdentity: "role-test", LeaseExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatalf("runtime begin attempt: %v", err)
	}
	marker := ducklake.CommitMarker{SchemaVersion: ducklake.CommitMarkerSchemaVersion, DeliveryID: "delivery-role-test", GenerationID: "generation-role-test", AttemptID: attemptID, LeaseEpoch: 1, RequestDigest: requestDigest, PlanDigest: planDigest, Project: "project-role-test", Environment: "prod", PhysicalPoolID: poolID}
	capture, err := NewSourceObservationCapture(attemptID, marker, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordSourceObservationCapture(t.Context(), capture); err != nil {
		t.Fatalf("runtime source observation insert: %v", err)
	}
	if _, err := runtime.AbortAttempt(t.Context(), TerminateAttemptInput{AttemptID: attemptID, OwnerID: "runtime-worker", FencingEpoch: 1, Evidence: json.RawMessage(`{"reason":"role-test"}`)}); err != nil {
		t.Fatalf("runtime terminate attempt: %v", err)
	}
	quarantineInput := MarkerQuarantineInput{PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, Reason: MarkerQuarantineIdentityMismatch, Evidence: json.RawMessage(`{"reason":"role-test"}`), ObservedMarkerDigest: digest('d'), ObservedSnapshotIDs: []int64{42}}
	if _, err := runtime.QuarantineMarker(t.Context(), quarantineInput); err != nil {
		t.Fatalf("runtime marker quarantine insert: %v", err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE ducklake.marker_quarantine SET evidence='{"tampered":true}' WHERE physical_pool_id=$1`, poolID); err == nil {
		t.Fatal("runtime marker quarantine update unexpectedly succeeded")
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
	if _, err := readonly.LoadSourceObservationCapture(t.Context(), attemptID); err != nil {
		t.Fatalf("readonly source observation select: %v", err)
	}
	if _, err := readonly.LoadMarkerQuarantine(t.Context(), poolID, catalogID, attemptID); err != nil {
		t.Fatalf("readonly marker quarantine select: %v", err)
	}
	if _, err := readonlyDB.Exec(t.Context(), `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema) VALUES ('readonly-pool','ducklake','readonly-catalog','0198f2c0-7c7a-7f00-8a11-000000000010','lake')`); err == nil {
		t.Fatal("readonly catalog insert unexpectedly succeeded")
	}
}
