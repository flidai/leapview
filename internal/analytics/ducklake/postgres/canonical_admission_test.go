package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidateBuildAdmissionTx must share the same global -> pool -> maintenance
// lock order as retention claims.  This integration test exercises the real
// PostgreSQL row locks (including first-use materialization of the pool scope)
// rather than only checking the returned error mapping.
func TestPostgres18CanonicalAdmissionSerializesRetentionFence(t *testing.T) {
	r, p, poolID, catalogID := retentionTestRepository(t, "canonical_admission_lock")
	fence, err := r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "retention-owner",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}

	// An active maintenance owner is observed under the locked row and blocks
	// candidate admission before any attempt lifecycle mutation can occur.
	fence, err = r.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "retention-owner-active",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	activeTx, err := p.Begin(t.Context())
	if err != nil {
		_ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence)
		t.Fatal(err)
	}
	if err := r.ValidateBuildAdmissionTx(t.Context(), activeTx, poolID, catalogID); !errors.Is(err, ErrRetentionMaintenanceBusy) {
		_ = activeTx.Rollback(t.Context())
		_ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence)
		t.Fatalf("admission with active retention fence = %v, want ErrRetentionMaintenanceBusy", err)
	}
	if err := activeTx.Rollback(t.Context()); err != nil {
		_ = r.ReleaseRetentionMaintenanceFence(t.Context(), fence)
		t.Fatal(err)
	}
	if err := r.ReleaseRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}

	// Keep the now-materialized maintenance row locked in another transaction.
	// Admission must wait on that row, proving first-use scope serialization.
	lockTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lockTx.Rollback(t.Context())
	if _, err := lockTx.Exec(t.Context(), `SELECT 1 FROM ducklake.pool_maintenance_fence WHERE physical_pool_id=$1 AND catalog_id=$2 FOR UPDATE`, poolID, catalogID); err != nil {
		t.Fatal(err)
	}
	admissionTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.ValidateBuildAdmissionTx(shortCtx, admissionTx, poolID, catalogID)
	}()
	select {
	case admissionErr := <-done:
		_ = admissionTx.Rollback(t.Context())
		if !errors.Is(admissionErr, context.DeadlineExceeded) {
			t.Fatalf("admission while maintenance row held = %v, want deadline", admissionErr)
		}
	case <-time.After(2 * time.Second):
		_ = admissionTx.Rollback(t.Context())
		t.Fatal("admission did not honor maintenance row lock timeout")
	}
}

func TestValidateBuildAdmissionTxUsesCanonicalAttemptAndQuarantine(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_canonical_admission_test")
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
	const poolID, catalogID = "canonical-admission-pool", "canonical-admission-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: testCatalogUUID, MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000009101"
	requestDigest, planDigest := digest('b'), digest('c')
	planID, candidateID, targetID := canonicalAttemptIDs(attemptID)
	if err := seedCanonicalDeliveryAttempt(t.Context(), p, canonicalDeliveryAttemptInput{PlanID: planID, CandidateID: candidateID, TargetID: targetID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "canonical-builder", FencingEpoch: 7}); err != nil {
		t.Fatal(err)
	}
	admissionTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateBuildAdmissionTx(t.Context(), admissionTx, poolID, catalogID); err != nil {
		_ = admissionTx.Rollback(t.Context())
		t.Fatalf("canonical running attempt admission = %v", err)
	}
	if err := admissionTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := QuarantineMarker(t.Context(), p, MarkerQuarantineInput{PhysicalPoolID: poolID, CatalogID: catalogID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, Reason: MarkerQuarantineDuplicate, Evidence: json.RawMessage(`{"anomaly":"canonical"}`), ObservedMarkerDigest: digest('d'), ObservedSnapshotIDs: []int64{11}}); err != nil {
		t.Fatal(err)
	}
	blockedTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blockedTx.Rollback(t.Context())
	if err := r.ValidateBuildAdmissionTx(t.Context(), blockedTx, poolID, catalogID); !errors.Is(err, ErrMarkerQuarantined) {
		t.Fatalf("quarantined canonical pool admission = %v, want ErrMarkerQuarantined", err)
	}
}

func TestAdmitSnapshotRetentionFromSealTxDerivesCanonicalIdentity(t *testing.T) {
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "ducklake_seal_retention_admission_test")
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
	const poolID, catalogID = "seal-retention-pool", "seal-retention-catalog"
	if _, err := r.RegisterCatalog(t.Context(), CatalogIdentity{PhysicalPoolID: poolID, CatalogDatabase: "ducklake", CatalogID: catalogID, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009201", MetadataSchema: "lake"}); err != nil {
		t.Fatal(err)
	}
	const attemptID, sealID = "0198f2c0-7c7a-7f00-8a11-000000009202", "0198f2c0-7c7a-7f00-8a11-000000009203"
	requestDigest, planDigest := digest('e'), digest('f')
	planID, candidateID, targetID := canonicalAttemptIDs(attemptID)
	attempt := canonicalDeliveryAttemptInput{PlanID: planID, CandidateID: candidateID, TargetID: targetID, AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest, PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "seal-builder", FencingEpoch: 3}
	if err := seedCanonicalDeliverySeal(t.Context(), p, attempt, canonicalDeliverySealInput{SealID: sealID, SnapshotID: 77, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009201"}); err != nil {
		t.Fatal(err)
	}
	admit := func() error {
		tx, err := p.Begin(t.Context())
		if err != nil {
			return err
		}
		if err := r.AdmitSnapshotRetentionFromSealTx(t.Context(), tx, sealID); err != nil {
			_ = tx.Rollback(t.Context())
			return err
		}
		return tx.Commit(t.Context())
	}
	if err := admit(); err != nil {
		t.Fatalf("initial seal retention admission = %v", err)
	}
	retention, err := r.LoadSnapshotRetention(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 77})
	if err != nil {
		t.Fatal(err)
	}
	if retention.State != RetentionLive {
		t.Fatalf("seal-derived retention = %#v, want live", retention)
	}
	if err := admit(); err != nil {
		t.Fatalf("exact seal retention replay = %v", err)
	}
	if _, err := p.Exec(t.Context(), `UPDATE ducklake.snapshot_retention SET state='retiring', retired_at=clock_timestamp() WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 77); err != nil {
		t.Fatal(err)
	}
	if err := admit(); !errors.Is(err, ErrNotLive) {
		t.Fatalf("retiring seal retention admission = %v, want ErrNotLive", err)
	}
	missingSealTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AdmitSnapshotRetentionFromSealTx(t.Context(), missingSealTx, "0198f2c0-7c7a-7f00-8a11-000000009299"); !errors.Is(err, ErrNotFound) {
		_ = missingSealTx.Rollback(t.Context())
		t.Fatalf("missing seal admission = %v, want ErrNotFound", err)
	}
	_ = missingSealTx.Rollback(t.Context())

	const missingCatalogPool, missingCatalogID = "seal-missing-catalog-pool", "seal-missing-catalog"
	const missingCatalogAttempt, missingCatalogSeal = "0198f2c0-7c7a-7f00-8a11-000000009204", "0198f2c0-7c7a-7f00-8a11-000000009205"
	missingPlan, missingCandidate, missingTarget := canonicalAttemptIDs(missingCatalogAttempt)
	missingAttempt := canonicalDeliveryAttemptInput{PlanID: missingPlan, CandidateID: missingCandidate, TargetID: missingTarget, AttemptID: missingCatalogAttempt, RequestDigest: digest('a'), PlanDigest: digest('b'), PhysicalPoolID: missingCatalogPool, CatalogID: missingCatalogID, OwnerID: "missing-catalog-builder", FencingEpoch: 1}
	if err := seedCanonicalDeliverySeal(t.Context(), p, missingAttempt, canonicalDeliverySealInput{SealID: missingCatalogSeal, SnapshotID: 88}); err != nil {
		t.Fatal(err)
	}
	missingCatalogTx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AdmitSnapshotRetentionFromSealTx(t.Context(), missingCatalogTx, missingCatalogSeal); !errors.Is(err, ErrNotFound) {
		_ = missingCatalogTx.Rollback(t.Context())
		t.Fatalf("missing catalog admission = %v, want ErrNotFound", err)
	}
	_ = missingCatalogTx.Rollback(t.Context())
}

// Runtime admission is exposed only through the seal-derived SECURITY
// DEFINER capability. The role can read retention state and execute the
// function, but cannot manufacture or mutate a retention row directly.
func TestPostgres18RuntimeRoleCanAdmitSnapshotRetentionFromSeal(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name:     "leapview_control_runtime",
		Password: "runtime-seal-retention-secret",
		Login:    true,
	})
	db := h.NewDatabase(t, "ducklake_runtime_seal_retention_role_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
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
	if _, err := admin.Exec(t.Context(), `
		GRANT USAGE ON SCHEMA delivery TO leapview_control_runtime;
		GRANT SELECT ON delivery.delivery_snapshot_seal TO leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}

	const poolID, catalogID = "runtime-seal-retention-pool", "runtime-seal-retention-catalog"
	adminRepo := New(admin)
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{
		PhysicalPoolID:  poolID,
		CatalogDatabase: "ducklake",
		CatalogID:       catalogID,
		CatalogUUID:     "0198f2c0-7c7a-7f00-8a11-000000009401",
		MetadataSchema:  "lake",
	}); err != nil {
		t.Fatal(err)
	}
	const attemptID, sealID = "0198f2c0-7c7a-7f00-8a11-000000009402", "0198f2c0-7c7a-7f00-8a11-000000009403"
	requestDigest, planDigest := digest('a'), digest('b')
	planID, candidateID, targetID := canonicalAttemptIDs(attemptID)
	if err := seedCanonicalDeliverySeal(t.Context(), admin, canonicalDeliveryAttemptInput{
		PlanID: planID, CandidateID: candidateID, TargetID: targetID,
		AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest,
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "runtime-seal-builder", FencingEpoch: 1,
	}, canonicalDeliverySealInput{
		SealID: sealID, SnapshotID: 123, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000009401",
	}); err != nil {
		t.Fatal(err)
	}
	// The seal's physical identity must remain coupled to the canonical build
	// attempt, even if an owner-level migration temporarily bypasses the
	// immutable-history trigger. This exercises the composite foreign key.
	if _, err := admin.Exec(t.Context(), `ALTER TABLE delivery.delivery_snapshot_seal DISABLE TRIGGER delivery_seal_history_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `
		UPDATE delivery.delivery_snapshot_seal SET physical_pool_id='forged-runtime-pool'
		WHERE seal_id=$1::uuid`, sealID); err == nil {
		t.Fatal("mismatched seal physical identity unexpectedly succeeded")
	}
	if _, err := admin.Exec(t.Context(), `ALTER TABLE delivery.delivery_snapshot_seal ENABLE TRIGGER delivery_seal_history_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `
		UPDATE delivery.delivery_build_attempt
		SET state='committed', snapshot_id=123, commit_marker='{"committed":true}'::jsonb,
		    finished_at=clock_timestamp(), updated_at=clock_timestamp()
		WHERE attempt_id=$1::uuid`, attemptID); err != nil {
		t.Fatal(err)
	}

	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	var canSelect, canInsert, canUpdate, canDelete, canExecute bool
	if err := runtimeDB.QueryRow(t.Context(), `
		SELECT has_table_privilege(current_user, 'ducklake.snapshot_retention', 'SELECT'),
		       has_table_privilege(current_user, 'ducklake.snapshot_retention', 'INSERT'),
		       has_table_privilege(current_user, 'ducklake.snapshot_retention', 'UPDATE'),
		       has_table_privilege(current_user, 'ducklake.snapshot_retention', 'DELETE'),
		       has_function_privilege(current_user, 'ducklake.admit_snapshot_retention_from_seal(uuid)', 'EXECUTE')`).Scan(&canSelect, &canInsert, &canUpdate, &canDelete, &canExecute); err != nil {
		t.Fatal(err)
	}
	if !canSelect || canInsert || canUpdate || canDelete || !canExecute {
		t.Fatalf("runtime retention capability select=%t insert=%t update=%t delete=%t execute=%t", canSelect, canInsert, canUpdate, canDelete, canExecute)
	}
	fence, err := adminRepo.AcquireRetentionMaintenanceFence(t.Context(), AcquireRetentionMaintenanceFenceInput{
		PhysicalPoolID: poolID,
		CatalogID:      catalogID,
		OwnerID:        "runtime-capability-fence",
		LeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeDB.Exec(t.Context(), `SELECT ducklake.admit_snapshot_retention_from_seal($1::uuid)`, sealID); err == nil {
		_ = adminRepo.ReleaseRetentionMaintenanceFence(t.Context(), fence)
		t.Fatal("direct runtime retention admission bypassed active maintenance fence")
	}
	if err := adminRepo.ReleaseRetentionMaintenanceFence(t.Context(), fence); err != nil {
		t.Fatal(err)
	}

	runtimeRepo := New(runtimeDB)
	runtimeTx, err := runtimeDB.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeRepo.AdmitSnapshotRetentionFromSealTx(t.Context(), runtimeTx, sealID); err != nil {
		_ = runtimeTx.Rollback(t.Context())
		t.Fatalf("runtime seal retention admission = %v", err)
	}
	if err := runtimeTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	retention, err := runtimeRepo.LoadSnapshotRetention(t.Context(), SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: 123})
	if err != nil {
		t.Fatal(err)
	}
	if retention.State != RetentionLive {
		t.Fatalf("runtime seal retention identity/state = %#v", retention)
	}
	if _, err := runtimeDB.Exec(t.Context(), `
		INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state)
		VALUES ('forged-runtime-pool','forged-runtime-catalog',999,'live')`); err == nil {
		t.Fatal("runtime direct snapshot_retention INSERT unexpectedly succeeded")
	}
	if _, err := runtimeDB.Exec(t.Context(), `
		UPDATE ducklake.snapshot_retention SET state='retiring'
		WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 123); err == nil {
		t.Fatal("runtime direct snapshot_retention UPDATE unexpectedly succeeded")
	}

	var state string
	if err := admin.QueryRow(t.Context(), `
		SELECT state FROM ducklake.snapshot_retention
		WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, 123).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(RetentionLive) {
		t.Fatalf("runtime-admitted retention state = %q, want live", state)
	}
}

// The runtime role owns the narrow marker-quarantine capability.  Seed the
// canonical delivery attempt as an administrator, then exercise the actual
// runtime pool so advisory scope locking, foreign-key admission, and the
// canonical read path are all covered together.
func TestPostgres18RuntimeRoleCanUseCanonicalMarkerQuarantine(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{
		Name:     "leapview_control_runtime",
		Password: "runtime-quarantine-secret",
		Login:    true,
	})
	db := h.NewDatabase(t, "ducklake_marker_quarantine_canonical_role_test")
	admin, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
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

	const poolID, catalogID = "quarantine-canonical-pool", "quarantine-canonical-catalog"
	adminRepo := New(admin)
	if _, err := adminRepo.RegisterCatalog(t.Context(), CatalogIdentity{
		PhysicalPoolID:  poolID,
		CatalogDatabase: "ducklake",
		CatalogID:       catalogID,
		CatalogUUID:     "0198f2c0-7c7a-7f00-8a11-000000009301",
		MetadataSchema:  "lake",
	}); err != nil {
		t.Fatal(err)
	}
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000009302"
	requestDigest, planDigest := digest('b'), digest('c')
	planID, candidateID, targetID := canonicalAttemptIDs(attemptID)
	if err := seedCanonicalDeliveryAttempt(t.Context(), admin, canonicalDeliveryAttemptInput{
		PlanID: planID, CandidateID: candidateID, TargetID: targetID,
		AttemptID: attemptID, RequestDigest: requestDigest, PlanDigest: planDigest,
		PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "runtime-owner", FencingEpoch: 1,
	}); err != nil {
		t.Fatal(err)
	}

	runtimeDB, err := pgxpool.New(t.Context(), db.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	runtimeRepo := New(runtimeDB)
	input := MarkerQuarantineInput{
		PhysicalPoolID:       poolID,
		CatalogID:            catalogID,
		AttemptID:            attemptID,
		RequestDigest:        requestDigest,
		PlanDigest:           planDigest,
		Reason:               MarkerQuarantineIdentityMismatch,
		Evidence:             json.RawMessage(`{"anomaly":"identity_mismatch"}`),
		ObservedMarkerDigest: digest('d'),
		ObservedSnapshotIDs:  []int64{102},
	}
	inserted, err := runtimeRepo.QuarantineMarker(t.Context(), input)
	if err != nil {
		t.Fatalf("runtime role marker quarantine insert = %v", err)
	}
	if inserted.PhysicalPoolID != poolID || inserted.CatalogID != catalogID || inserted.AttemptID != attemptID || inserted.Reason != input.Reason {
		t.Fatalf("runtime marker quarantine identity = %#v", inserted)
	}
	read, err := runtimeRepo.LoadMarkerQuarantine(t.Context(), poolID, catalogID, attemptID)
	if err != nil {
		t.Fatalf("runtime role marker quarantine read = %v", err)
	}
	if read.RequestDigest != requestDigest || read.PlanDigest != planDigest || string(read.Evidence) != string(inserted.Evidence) || len(read.ObservedSnapshotIDs) != 1 || read.ObservedSnapshotIDs[0] != 102 {
		t.Fatalf("runtime marker quarantine round trip = %#v", read)
	}
}
