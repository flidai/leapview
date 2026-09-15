package postgres

import (
	"context"
	"testing"
	"time"

	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
)

func TestBND06CrossProjectPhysicalRetentionProtectsLiveSnapshot(t *testing.T) {
	r, pool, poolID, catalogID := retentionTestRepository(t, "bnd06_cross_project")
	ctx := t.Context()
	delivery := deploymentpostgres.New(pool)

	type projectFixture struct {
		attempt    canonicalDeliveryAttemptInput
		seal       canonicalDeliverySealInput
		generation string
		root       string
	}
	projects := []projectFixture{
		{
			attempt: canonicalDeliveryAttemptInput{
				PlanID: "0198f2c0-7c7a-7f00-8a11-000000001001", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000001002",
				TargetID: "project-a-target", AttemptID: "0198f2c0-7c7a-7f00-8a11-000000001003", RequestDigest: digest('a'),
				PlanDigest: digest('a'), PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder-a", FencingEpoch: 1,
			},
			seal:       canonicalDeliverySealInput{SealID: "0198f2c0-7c7a-7f00-8a11-000000001004", SnapshotID: 31001, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000041"},
			generation: "0198f2c0-7c7a-7f00-8a11-000000001005", root: "0198f2c0-7c7a-7f00-8a11-000000001006",
		},
		{
			attempt: canonicalDeliveryAttemptInput{
				PlanID: "0198f2c0-7c7a-7f00-8a11-000000002001", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000002002",
				TargetID: "project-b-target", AttemptID: "0198f2c0-7c7a-7f00-8a11-000000002003", RequestDigest: digest('c'),
				PlanDigest: digest('c'), PhysicalPoolID: poolID, CatalogID: catalogID, OwnerID: "builder-b", FencingEpoch: 1,
			},
			seal:       canonicalDeliverySealInput{SealID: "0198f2c0-7c7a-7f00-8a11-000000002004", SnapshotID: 31002, CatalogUUID: "0198f2c0-7c7a-7f00-8a11-000000000041"},
			generation: "0198f2c0-7c7a-7f00-8a11-000000002005", root: "0198f2c0-7c7a-7f00-8a11-000000002006",
		},
	}

	for index := range projects {
		fixture := &projects[index]
		if err := seedCanonicalDeliveryAttempt(ctx, pool, fixture.attempt); err != nil {
			t.Fatal(err)
		}
		if _, err := delivery.AbortBuildAttempt(ctx, deploymentpostgres.TerminateAttemptInput{
			AttemptID: fixture.attempt.AttemptID, OwnerID: fixture.attempt.OwnerID, FencingEpoch: fixture.attempt.FencingEpoch,
			Evidence: []byte(`{"qualification":"BND-06","reason":"fixture build complete"}`),
		}); err != nil {
			t.Fatal(err)
		}
		if err := seedCanonicalDeliverySeal(ctx, pool, fixture.attempt, fixture.seal); err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.AdmitSnapshotRetentionFromSealTx(ctx, tx, fixture.seal.SealID); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := delivery.CreateGeneration(ctx, deploymentpostgres.GenerationInput{
			GenerationID: fixture.generation, TargetID: fixture.attempt.TargetID, CandidateID: fixture.attempt.CandidateID,
			SnapshotSealID: fixture.seal.SealID, PlanID: fixture.attempt.PlanID, PlanDigest: fixture.attempt.PlanDigest,
			ArtifactRoot: "/tmp/ducklake-test-artifacts", ArtifactRootDigest: fixture.attempt.RequestDigest,
			ServingArtifactDigest: fixture.attempt.RequestDigest, CompiledGraphDigest: fixture.attempt.RequestDigest,
			CompiledConfigDigest: fixture.attempt.RequestDigest, SecurityDomainFingerprint: fixture.attempt.RequestDigest,
			GenerationRevision: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := delivery.CreateRetentionRoot(ctx, deploymentpostgres.DeliveryRetentionRoot{
		RootID: projects[0].root, TargetID: projects[0].attempt.TargetID, CandidateID: projects[0].attempt.CandidateID,
		GenerationID: projects[0].generation, SnapshotSealID: projects[0].seal.SealID, RootKind: "generation", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := delivery.CreateRetentionRoot(ctx, deploymentpostgres.DeliveryRetentionRoot{
		RootID: projects[1].root, TargetID: projects[1].attempt.TargetID, CandidateID: projects[1].attempt.CandidateID,
		GenerationID: projects[1].generation, SnapshotSealID: projects[1].seal.SealID, RootKind: "candidate", State: "live",
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	drained, err := deploymentpostgres.NewMaintenance(pool).Drain(ctx, poolID, catalogID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if drained.Retired != 1 || drained.Expired != 1 {
		t.Fatalf("retention drain = %#v, want exactly Project B retired and expired", drained)
	}

	projectARef := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: projects[0].seal.SnapshotID}
	if err := retireSnapshot(ctx, pool, projectARef, time.Now().UTC()); err == nil {
		t.Fatal("Project A snapshot retired despite its live delivery root")
	}
	projectBRef := SnapshotRef{PhysicalPoolID: poolID, CatalogID: catalogID, SnapshotID: projects[1].seal.SnapshotID}
	if err := retireSnapshot(ctx, pool, projectBRef, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	coordinator := &RetentionCoordinator{
		Control: r,
		OpenSessionFor: func(context.Context, RetentionCatalogSessionInput) (RetentionCatalogSession, error) {
			// The native DuckLake catalog call is the external effect. PostgreSQL
			// root draining, eligibility, claiming, and completion remain real.
			return &retentionSessionFake{}, nil
		},
	}
	result, err := coordinator.Run(ctx, RetentionMaintenanceRequest{
		MaintenanceID: "0198f2c0-7c7a-7f00-8a11-000000003001", PhysicalPoolID: poolID, CatalogID: catalogID,
		OwnerID: "bnd06-maintenance", LeaseExpiresAt: time.Now().UTC().Add(time.Minute), FileGrace: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Snapshots) != 1 || result.Snapshots[0].SnapshotID != projectBRef.SnapshotID {
		t.Fatalf("claimed snapshots = %#v, want only Project B snapshot %d", result.Snapshots, projectBRef.SnapshotID)
	}

	var projectAState string
	var projectAClaim *string
	if err := pool.QueryRow(ctx, `
SELECT state, retention_claim_id::text
FROM ducklake.snapshot_retention
WHERE physical_pool_id=$1 AND catalog_id=$2 AND snapshot_id=$3`, poolID, catalogID, projectARef.SnapshotID).Scan(&projectAState, &projectAClaim); err != nil {
		t.Fatal(err)
	}
	if projectAState != string(RetentionLive) || projectAClaim != nil {
		t.Fatalf("Project A retention = state %q claim %v, want live and unclaimed", projectAState, projectAClaim)
	}
}
