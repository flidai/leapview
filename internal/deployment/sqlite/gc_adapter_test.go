package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/platform"
)

func TestQuarantineRootReactivatesRetiredHold(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "quarantine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	pool := "sha256:" + strings.Repeat("a", 64)
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO physical_pools (id,identity_digest,storage_location,storage_namespace,storage_implementation,object_naming_contract,isolation_boundary,retention_authority,retention_policy_json) VALUES (?,?,?,?,?,?,?,?,?)`, pool, pool, "s3://quarantine", "q", "s3", "names-v1", "q", "q", `{}`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepositoryWithHooks(store.SQLDB(), ActivationHooks{})
	now := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)
	root := deployment.DeliveryRoot{PhysicalPoolID: pool, Kind: "candidate", SourceID: "candidate-1", CatalogDigest: "sha256:" + strings.Repeat("b", 64), ObjectKey: "catalog.duckdb", CreatedAt: now}
	if err := repo.QuarantineRoot(ctx, root, "corrupt", now); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM delivery_root_registry WHERE root_kind='quarantined'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("status=%s", status)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE delivery_root_registry SET status='retired',retired_at=? WHERE root_kind='quarantined'`, now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := repo.QuarantineRoot(ctx, root, "corrupt again", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM delivery_root_registry WHERE root_kind='quarantined'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("reactivated status=%s", status)
	}
}

func TestQuarantineRootCommitsOperatorAuditWithProjection(t *testing.T) {
	ctx := context.Background()
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('8')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-quarantine-audit", AttemptID: "attempt-quarantine-audit", PhysicalPoolID: pool, OwnerID: "offline-admin", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, IdempotencyKey: "quarantine-audit-build", PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(ctx, lease, attempt); err != nil {
		t.Fatal(err)
	}
	for n, state := range []deployment.DeliveryBuildAttemptStatus{deployment.DeliveryBuildNormalizing, deployment.DeliveryBuildValidating, deployment.DeliveryBuildSealing} {
		if _, err := repo.TransitionBuildAttempt(ctx, attempt.ID, int64(n+1), state, now.Add(time.Duration(n+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	seal, err := repo.PrepareCatalogSeal(ctx, deployment.CatalogSeal{ID: "seal-quarantine-audit", AttemptID: attempt.ID, PlanID: plan.ID, PlanDigest: plan.Digest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, CatalogDigest: repoDeliveryDigest('c'), CompatibilityDigest: repoDeliveryDigest('d'), ServingArtifactID: "artifact-quarantine-audit", ServingArtifactDigest: repoDeliveryDigest('e'), ServingStateID: "state-quarantine-audit", ObjectKey: "catalogs/quarantine-audit", ObjectSize: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkCatalogSealUploaded(ctx, seal.ID); err != nil {
		t.Fatal(err)
	}
	if seal, err = repo.VerifyCatalogSeal(ctx, seal.ID, repoDeliveryDigest('f'), repoDeliveryDigest('0'), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidate, err := repo.CreateCandidateReady(ctx, deployment.DeliveryCandidate{ID: "candidate-quarantine-audit", PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseTargetRevision: 0, SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-quarantine-audit", CreatedAt: now}, seal, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	root := deployment.DeliveryRoot{PhysicalPoolID: pool, Kind: "candidate", SourceID: candidate.ID, CandidateID: candidate.ID, CatalogDigest: candidate.CatalogDigest, ObjectKey: candidate.CatalogObjectKey, Status: "active", CreatedAt: candidate.CreatedAt}
	if err := repo.QuarantineRootWithActor(ctx, root, "operator_repair_quarantine", "offline-admin", now.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var candidateStatus, rootStatus, actor, eventKind, objectKind, outcome, details string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM delivery_candidates WHERE id=?`, candidate.ID).Scan(&candidateStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM delivery_root_registry WHERE root_kind='quarantined' AND physical_pool_id=?`, pool).Scan(&rootStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT actor_id,event_kind,object_kind,outcome,details_json FROM delivery_events WHERE target_id=? AND event_kind='gc_aborted' AND object_id LIKE 'quarantine-%'`, plan.TargetID).Scan(&actor, &eventKind, &objectKind, &outcome, &details); err != nil {
		t.Fatal(err)
	}
	if candidateStatus != string(deployment.DeliveryCandidateFailed) || rootStatus != "active" || actor != "offline-admin" || eventKind != "gc_aborted" || objectKind != "gc_cycle" || outcome != "accepted" || !strings.Contains(details, `"reason_code":"operator_repair_quarantine"`) || !strings.Contains(details, `"status":"quarantined"`) {
		t.Fatalf("projection candidate=%q root=%q event actor=%q kind=%q object=%q outcome=%q details=%q", candidateStatus, rootStatus, actor, eventKind, objectKind, outcome, details)
	}
}

func TestBuildRootQuarantineResolvesSealPlanAuditScope(t *testing.T) {
	ctx := context.Background()
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	pool := repoDeliveryDigest('7')
	insertDeliveryPool(t, store, pool)
	lease := deployment.DeliveryWriterLease{ID: "writer-build-quarantine", AttemptID: "attempt-build-quarantine", PhysicalPoolID: pool, OwnerID: "builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: lease.AttemptID, PlanID: plan.ID, IdempotencyKey: "build-quarantine", PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, WriterLeaseID: lease.ID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(ctx, lease, attempt); err != nil {
		t.Fatal(err)
	}
	for n, state := range []deployment.DeliveryBuildAttemptStatus{deployment.DeliveryBuildNormalizing, deployment.DeliveryBuildValidating, deployment.DeliveryBuildSealing} {
		if _, err := repo.TransitionBuildAttempt(ctx, attempt.ID, int64(n+1), state, now.Add(time.Duration(n+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	seal, err := repo.PrepareCatalogSeal(ctx, deployment.CatalogSeal{ID: "seal-build-quarantine", AttemptID: attempt.ID, PlanID: plan.ID, PlanDigest: plan.Digest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, CatalogDigest: repoDeliveryDigest('c'), CompatibilityDigest: repoDeliveryDigest('d'), ServingArtifactID: "artifact-build-quarantine", ServingArtifactDigest: repoDeliveryDigest('e'), ServingStateID: "state-build-quarantine", ObjectKey: "catalogs/build-quarantine", ObjectSize: 1, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	root := deployment.DeliveryRoot{PhysicalPoolID: pool, Kind: "build", SourceID: seal.ID, CatalogDigest: seal.CatalogDigest, ObjectKey: seal.ObjectKey, Status: "active", CreatedAt: seal.CreatedAt}
	if err := repo.QuarantineRootWithActor(ctx, root, "operator_repair_quarantine", "offline-admin", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sealStatus, actor, eventKind, objectKind, outcome, details string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM delivery_catalog_seals WHERE id=?`, seal.ID).Scan(&sealStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT actor_id,event_kind,object_kind,outcome,details_json FROM delivery_events WHERE target_id=? AND event_kind='gc_aborted' AND object_id LIKE 'quarantine-%'`, plan.TargetID).Scan(&actor, &eventKind, &objectKind, &outcome, &details); err != nil {
		t.Fatal(err)
	}
	if sealStatus != string(deployment.CatalogSealFailed) || actor != "offline-admin" || eventKind != "gc_aborted" || objectKind != "gc_cycle" || outcome != "accepted" || !strings.Contains(details, `"status":"quarantined"`) {
		t.Fatalf("seal=%q event actor=%q kind=%q object=%q outcome=%q details=%q", sealStatus, actor, eventKind, objectKind, outcome, details)
	}
}
