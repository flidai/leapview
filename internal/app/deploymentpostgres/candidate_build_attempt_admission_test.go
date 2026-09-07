package deploymentpostgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Existing delivery-focused tests use this no-op physical guard; integration
// coverage exercises the real seal/fence capability.
type candidatePhysicalAdmissionStub struct{}

func (candidatePhysicalAdmissionStub) Configured() bool { return true }
func (candidatePhysicalAdmissionStub) ValidateBuildAdmissionTx(context.Context, ducklakepostgres.Tx, string, string) error {
	return nil
}
func (candidatePhysicalAdmissionStub) AdmitSnapshotRetentionFromSealTx(ctx context.Context, tx ducklakepostgres.Tx, sealID string) error {
	// Generation-admission fixtures install the DuckLake ledger and use this
	// stub for the surrounding migration/fence proof. Mirror the real
	// admission's durable row so the delivery retention-root check exercises
	// the same physical gate. Older candidate-only fixtures intentionally omit
	// DuckLake; retain their isolated scope when the table is absent.
	var installed bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('ducklake.snapshot_retention') IS NOT NULL`).Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	var physicalPool, catalog, catalogUUID string
	var snapshot int64
	if err := tx.QueryRow(ctx, `SELECT physical_pool_id,catalog_id,catalog_uuid::text,ducklake_snapshot_id FROM delivery.delivery_snapshot_seal WHERE seal_id=$1::uuid`, sealID).Scan(&physicalPool, &catalog, &catalogUUID, &snapshot); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ducklake.catalog_identity(physical_pool_id,catalog_database,catalog_id,catalog_uuid,metadata_schema) VALUES($1,'ducklake',$2,$3::uuid,'lake') ON CONFLICT DO NOTHING`, physicalPool, catalog, catalogUUID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO ducklake.snapshot_retention(physical_pool_id,catalog_id,snapshot_id,state) VALUES($1,$2,$3,'live') ON CONFLICT DO NOTHING`, physicalPool, catalog, snapshot)
	return err
}

func candidateAdmissionDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func candidateAdmissionDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	db := h.NewDatabase(t, "candidate_build_attempt_admission_test")
	p, err := pgxpool.New(t.Context(), db.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	tx, err := p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := deploymentnative.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return p
}

type candidateAdmissionFixture struct {
	Input     CandidateBuildAttemptAdmissionInput
	Target    deploymentnative.TargetInput
	Plan      deploymentnative.PlanInput
	Candidate deploymentnative.CandidateInput
	ExpiresAt time.Time
}

func candidateAdmissionFixtureInput(t *testing.T) candidateAdmissionFixture {
	t.Helper()
	expires := time.Now().UTC().Add(time.Hour)
	const attemptID = "0198f2c0-7c7a-7f00-8a11-000000000303"
	const candidateID = "0198f2c0-7c7a-7f00-8a11-000000000304"
	plan := nativePlanFixture(t, deploymentnative.PlanInput{PlanID: "0198f2c0-7c7a-7f00-8a11-000000000302", TargetID: "target-candidate-admission", PlanRevision: 1, CompiledGraphDigest: candidateAdmissionDigest('d'), CompiledConfigDigest: candidateAdmissionDigest('e'), SecurityDomainFingerprint: candidateAdmissionDigest('f'), ArtifactDigest: candidateAdmissionDigest('c'), QualificationDigest: candidateAdmissionDigest('1')}, "project-candidate-admission")
	return candidateAdmissionFixture{
		Input: CandidateBuildAttemptAdmissionInput{
			Lease: deploymentnative.LeaseInput{
				LeaseID: "0198f2c0-7c7a-7f00-8a11-000000000301", TargetID: "target-candidate-admission", OwnerID: "builder-candidate-admission", ExpiresAt: expires,
			},
			Attempt: deploymentnative.BuildAttemptInput{
				AttemptID: attemptID, PlanID: "0198f2c0-7c7a-7f00-8a11-000000000302", CandidateID: candidateID,
				OwnerID: "builder-candidate-admission", PhysicalPoolID: "pool-candidate-admission", RequestDigest: candidateAdmissionDigest('b'), PlanDigest: plan.PlanDigest,
				SessionIdentity: "duckdb-session-candidate-admission", LeaseExpiresAt: expires,
			},
			Artifact:  CandidateBuildArtifactInput{ServingArtifactID: "artifact-" + strings.TrimPrefix(candidateAdmissionDigest('c'), "sha256:"), ServingArtifactDigest: candidateAdmissionDigest('c'), ServingStateID: "candidate-serving-state"},
			CatalogID: "catalog-candidate-admission",
		},
		Target:    deploymentnative.TargetInput{TargetID: "target-candidate-admission", ProjectID: "project-candidate-admission", Environment: "prod"},
		Plan:      plan,
		Candidate: deploymentnative.CandidateInput{CandidateID: "0198f2c0-7c7a-7f00-8a11-000000000304", TargetID: "target-candidate-admission", PlanID: "0198f2c0-7c7a-7f00-8a11-000000000302", CandidateRevision: 1, ArtifactDigest: candidateAdmissionDigest('c')},
		ExpiresAt: expires,
	}
}

func seedCandidateAdmissionFixture(t *testing.T, delivery *deploymentnative.Repository, fixture candidateAdmissionFixture) {
	t.Helper()
	ctx := t.Context()
	if _, err := delivery.CreateTarget(ctx, fixture.Target); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreatePlan(ctx, fixture.Plan); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateCandidate(ctx, fixture.Candidate); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateBuildAttemptAdmissionPostgresAtomicSuccessReplayAndRollback(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	if _, err := NewCandidateBuildAttemptAdmission(nil, nil); err == nil {
		t.Fatal("candidate admission accepted an unconfigured delivery authority")
	}
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}

	fixture := candidateAdmissionFixtureInput(t)
	seedCandidateAdmissionFixture(t, delivery, fixture)
	first, err := admission.AdmitCandidateBuildAttempt(t.Context(), fixture.Input)
	if err != nil {
		t.Fatalf("admit candidate build attempt: %v", err)
	}
	if first.Lease.State != "active" || first.Attempt.State != deploymentnative.AttemptRunning || first.Artifact.AttemptID != first.Attempt.AttemptID {
		t.Fatalf("admission result = %#v", first)
	}

	replayed, err := admission.AdmitCandidateBuildAttempt(t.Context(), fixture.Input)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if replayed.Lease != first.Lease || !reflect.DeepEqual(replayed.Attempt, first.Attempt) || replayed.Artifact != first.Artifact {
		t.Fatalf("exact replay drifted: first=%#v replay=%#v", first, replayed)
	}
	drift := fixture.Input
	drift.Artifact.ServingArtifactDigest = candidateAdmissionDigest('4')
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), drift); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("artifact replay drift error = %v, want delivery conflict", err)
	}

	rollback := candidateAdmissionFixtureInput(t)
	rollback.Input.Lease.LeaseID = "0198f2c0-7c7a-7f00-8a11-000000000311"
	rollback.Input.Attempt.AttemptID = "0198f2c0-7c7a-7f00-8a11-000000000313"
	rollback.Input.Attempt.PlanID = "0198f2c0-7c7a-7f00-8a11-000000000312"
	rollback.Input.Attempt.CandidateID = "0198f2c0-7c7a-7f00-8a11-000000000314"
	rollback.Input.Lease.TargetID = "target-candidate-admission-rollback"
	rollback.Input.Attempt.SessionIdentity = "duckdb-session-candidate-admission-rollback"
	rollback.Input.Attempt.PhysicalPoolID = "pool-candidate-admission-rollback"
	rollback.Target.TargetID = rollback.Input.Lease.TargetID
	rollback.Target.ProjectID = "project-candidate-admission-rollback"
	rollback.Plan.PlanID = rollback.Input.Attempt.PlanID
	rollback.Plan.TargetID = rollback.Target.TargetID
	rollback.Candidate.CandidateID = rollback.Input.Attempt.CandidateID
	rollback.Candidate.PlanID = rollback.Plan.PlanID
	rollback.Candidate.TargetID = rollback.Target.TargetID
	rollback.Plan = nativePlanFixture(t, rollback.Plan, rollback.Target.ProjectID)
	rollback.Input.Attempt.PlanDigest = rollback.Plan.PlanDigest
	seedCandidateAdmissionFixture(t, delivery, rollback)
	// Reuse an existing candidate with a different plan identity. Candidate
	// admission acquires the lease before validating that relationship, so this
	// proves the transaction rolls the lease back when the delivery check fails.
	rollback.Input.Attempt.CandidateID = fixture.Input.Attempt.CandidateID
	rollback.Input.Lease.OwnerID = "different-owner"
	rollback.Input.Attempt.OwnerID = "different-owner"
	if _, err := admission.AdmitCandidateBuildAttempt(t.Context(), rollback.Input); err == nil {
		t.Fatal("conflicting candidate admission unexpectedly succeeded")
	}
	if _, err := delivery.Lease(t.Context(), rollback.Input.Lease.LeaseID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("rollback retained delivery lease, err=%v", err)
	}
	if _, err := delivery.BuildAttempt(t.Context(), rollback.Input.Attempt.AttemptID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("rollback retained delivery attempt, err=%v", err)
	}
	if _, err := delivery.BuildArtifactBinding(t.Context(), rollback.Input.Attempt.AttemptID); !errors.Is(err, deploymentnative.ErrNotFound) {
		t.Fatalf("rollback retained artifact binding, err=%v", err)
	}
}

func TestCandidateBuildAttemptAdmissionTxComposesAdjacentMutation(t *testing.T) {
	p := candidateAdmissionDB(t)
	delivery := deploymentnative.New(p)
	admission, err := NewCandidateBuildAttemptAdmission(delivery, candidatePhysicalAdmissionStub{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := candidateAdmissionFixtureInput(t)
	seedCandidateAdmissionFixture(t, delivery, fixture)

	tx, err := delivery.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := admission.AdmitCandidateBuildAttemptTx(t.Context(), tx, fixture.Input)
	if err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admit candidate build attempt in caller transaction: %v", err)
	}
	if result.Attempt.AttemptID != fixture.Input.Attempt.AttemptID {
		_ = tx.Rollback(t.Context())
		t.Fatalf("admission result = %#v", result)
	}
	adjacent := deploymentnative.TargetInput{TargetID: "target-candidate-admission-adjacent", ProjectID: "project-candidate-admission", Environment: "staging"}
	if _, err := delivery.CreateTargetTx(t.Context(), tx, adjacent); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatalf("adjacent mutation after caller-owned admission: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatalf("commit composed transaction: %v", err)
	}
	if _, err := delivery.Target(t.Context(), adjacent.TargetID); err != nil {
		t.Fatalf("adjacent mutation was not committed with admission: %v", err)
	}
}

func TestNormalizeCandidateBuildAttemptAdmissionRejectsExpiryDrift(t *testing.T) {
	fixture := candidateAdmissionFixtureInput(t)
	fixture.Input.Attempt.LeaseExpiresAt = fixture.Input.Lease.ExpiresAt.Add(time.Second)
	if _, err := normalizeCandidateBuildAttemptAdmissionInput(fixture.Input); !errors.Is(err, deploymentnative.ErrConflict) {
		t.Fatalf("expiry drift error = %v, want delivery conflict", err)
	}
}

func TestNormalizeCandidateBuildAttemptAdmissionRequiresCandidateID(t *testing.T) {
	fixture := candidateAdmissionFixtureInput(t)
	fixture.Input.Attempt.CandidateID = ""
	if _, err := normalizeCandidateBuildAttemptAdmissionInput(fixture.Input); !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("missing candidate id error = %v, want delivery invalid", err)
	}
}

func TestNormalizeCandidateBuildAttemptAdmissionRejectsRelationNamespaceDrift(t *testing.T) {
	fixture := candidateAdmissionFixtureInput(t)
	fixture.Input.Attempt.Namespace = "_not-the-canonical-namespace"
	if _, err := normalizeCandidateBuildAttemptAdmissionInput(fixture.Input); !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("caller-authored namespace error = %v, want delivery invalid", err)
	}
}

func TestNormalizeCandidateBuildAttemptAdmissionRejectsCallerFencingEpoch(t *testing.T) {
	fixture := candidateAdmissionFixtureInput(t)
	fixture.Input.Attempt.FencingEpoch = 1
	if _, err := normalizeCandidateBuildAttemptAdmissionInput(fixture.Input); !errors.Is(err, deploymentnative.ErrInvalid) {
		t.Fatalf("caller-authored fencing epoch error = %v, want delivery invalid", err)
	}
}

func TestNormalizeCandidateBuildAttemptAdmissionRejectsOversizedArtifactIDs(t *testing.T) {
	for name, mutate := range map[string]func(*CandidateBuildAttemptAdmissionInput){
		"artifact id": func(in *CandidateBuildAttemptAdmissionInput) {
			in.Artifact.ServingArtifactID = strings.Repeat("a", 256)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := candidateAdmissionFixtureInput(t)
			mutate(&fixture.Input)
			if _, err := normalizeCandidateBuildAttemptAdmissionInput(fixture.Input); !errors.Is(err, deploymentnative.ErrInvalid) {
				t.Fatalf("oversized %s error = %v, want delivery invalid", name, err)
			}
		})
	}
}
