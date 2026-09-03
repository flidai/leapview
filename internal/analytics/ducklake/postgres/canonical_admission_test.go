package postgres

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
