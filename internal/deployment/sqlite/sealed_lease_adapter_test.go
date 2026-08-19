package sqlite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/deployment"
	deploymentgc "github.com/flidai/leapview/internal/deployment/gc"
	"github.com/flidai/leapview/internal/platform"
)

type sealedLeaseGCStore struct {
	objects map[string]deploymentgc.Object
	deleted []string
}

func (s *sealedLeaseGCStore) Open(_ context.Context, key string) (deploymentgc.CatalogObject, error) {
	if _, ok := s.objects[key]; !ok {
		return deploymentgc.CatalogObject{}, os.ErrNotExist
	}
	body := []byte(key)
	return deploymentgc.CatalogObject{Body: io.NopCloser(bytes.NewReader(body)), Size: int64(len(body)), Metadata: map[string]string{}}, nil
}

func (s *sealedLeaseGCStore) ListPoolObjects(context.Context, string) ([]deploymentgc.Object, error) {
	objects := make([]deploymentgc.Object, 0, len(s.objects))
	for _, object := range s.objects {
		objects = append(objects, object)
	}
	return objects, nil
}

func (s *sealedLeaseGCStore) DeleteConditional(_ context.Context, request deploymentgc.DeleteRequest) (deploymentgc.DeleteResponse, error) {
	object, ok := s.objects[request.Key]
	if !ok {
		return deploymentgc.DeleteResponse{NotFound: true}, nil
	}
	if object.Digest != request.Digest {
		return deploymentgc.DeleteResponse{}, errors.New("object digest changed")
	}
	delete(s.objects, request.Key)
	s.deleted = append(s.deleted, request.Key)
	return deploymentgc.DeleteResponse{Deleted: true}, nil
}

func (s *sealedLeaseGCStore) Stat(_ context.Context, _ string, key string) (deploymentgc.Object, error) {
	object, ok := s.objects[key]
	if !ok {
		return deploymentgc.Object{}, os.ErrNotExist
	}
	return object, nil
}

type sealedLeaseGCInspector map[string]deploymentgc.CatalogReachability

func (i sealedLeaseGCInspector) Inspect(_ context.Context, root deployment.DeliveryRoot) (deploymentgc.CatalogReachability, error) {
	reachability, ok := i[root.ObjectKey]
	if !ok {
		return deploymentgc.CatalogReachability{}, errors.New("catalog root is unavailable")
	}
	return reachability, nil
}

type adapterArtifact struct {
	seal      deployment.CatalogSeal
	candidate deployment.DeliveryCandidate
}

func createAdapterArtifact(t *testing.T, repo *Repository, plan deployment.DeliveryPlan, pool string, now time.Time, attemptID, writerID, sealID, candidateID string, catalogByte byte, baseGeneration, baseDigest string, baseRevision int64) adapterArtifact {
	t.Helper()
	lease := deployment.DeliveryWriterLease{ID: writerID, AttemptID: attemptID, PhysicalPoolID: pool, OwnerID: "adapter-builder", Epoch: 1, CreatedAt: now, ExpiresAt: now.Add(4 * time.Hour)}
	attempt := deployment.DeliveryBuildAttempt{ID: attemptID, PlanID: plan.ID, PlanDigest: plan.Digest, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseGenerationID: baseGeneration, BaseCatalogDigest: baseDigest, BasePhysicalPoolID: func() string {
		if baseGeneration == "" {
			return ""
		}
		return pool
	}(), PhysicalPoolID: pool, WriterLeaseID: writerID, CreatedAt: now}
	if _, _, err := repo.CreateWriterLeaseAndBuildAttempt(t.Context(), lease, attempt); err != nil {
		t.Fatal(err)
	}
	for revision, status := range []deployment.DeliveryBuildAttemptStatus{deployment.DeliveryBuildNormalizing, deployment.DeliveryBuildValidating, deployment.DeliveryBuildSealing} {
		if _, err := repo.TransitionBuildAttempt(t.Context(), attemptID, int64(revision+1), status, now.Add(time.Duration(revision+1)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	digest := repoDeliveryDigest(catalogByte)
	seal, err := repo.PrepareCatalogSeal(t.Context(), deployment.CatalogSeal{ID: sealID, AttemptID: attemptID, PlanID: plan.ID, PlanDigest: plan.Digest, ExecutionDigest: plan.ExecutionDigest, PhysicalPoolID: pool, CatalogDigest: digest, BaseCatalogDigest: baseDigest, ServingArtifactID: "artifact-" + candidateID, ServingArtifactDigest: repoDeliveryDigest('7'), BasePhysicalPoolID: func() string {
		if baseGeneration == "" {
			return ""
		}
		return pool
	}(), CompatibilityDigest: repoDeliveryDigest('d'), ServingStateID: "state-" + candidateID, ObjectKey: catalogseal.CanonicalObjectKey(digest), ObjectSize: 37, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if seal, err = repo.MarkCatalogSealUploaded(t.Context(), seal.ID); err != nil {
		t.Fatal(err)
	}
	if seal, err = repo.VerifyCatalogSeal(t.Context(), seal.ID, repoDeliveryDigest('e'), repoDeliveryDigest('f'), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidate, err := repo.CreateCandidateReady(t.Context(), deployment.DeliveryCandidate{ID: candidateID, PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest, ExecutionDigest: plan.ExecutionDigest, BaseGenerationID: baseGeneration, BaseTargetRevision: baseRevision, BaseCatalogDigest: baseDigest, BasePhysicalPoolID: func() string {
		if baseGeneration == "" {
			return ""
		}
		return pool
	}(), SealID: seal.ID, CatalogDigest: seal.CatalogDigest, CompatibilityDigest: seal.CompatibilityDigest, CatalogObjectKey: seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: "state-" + candidateID, CreatedAt: now, ResolvedInputs: sqliteResolvedInputs(t, plan, candidateID)}, seal, now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return adapterArtifact{seal: seal, candidate: candidate}
}

func adapterPlan(t *testing.T, base deployment.DeliveryPlan, id string, now time.Time, baseGeneration string, baseRevision int64, source byte) deployment.DeliveryPlan {
	t.Helper()
	plan := base
	plan.ID = id
	plan.BaseGenerationID = baseGeneration
	plan.BaseTargetRevision = baseRevision
	plan.SourceDigest = repoDeliveryDigest(source)
	plan.Execution.SourceArtifactDigest = plan.SourceDigest
	plan.CreatedAt = now
	plan.Governance.ExpiresAt = now.Add(3 * time.Hour)
	plan, err := deployment.NewDeliveryPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func adapterLeaseInput(artifact adapterArtifact, generationID, leaseID string, created time.Time) catalogartifact.LeaseInput {
	return catalogartifact.LeaseInput{ID: leaseID, HolderID: "adapter-reader", GenerationID: generationID, SealID: artifact.seal.ID, CatalogDigest: artifact.seal.CatalogDigest, ObjectKey: artifact.seal.ObjectKey, ObjectSize: artifact.seal.ObjectSize, ClosureDigest: repoDeliveryDigest('e'), QualificationDigest: repoDeliveryDigest('f'), PhysicalPoolID: artifact.seal.PhysicalPoolID, CreatedAt: created, ExpiresAt: created.Add(time.Hour)}
}

func TestSealedCatalogLeaseAdapterKeepsOldLeaseAcrossPublication(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sealed-lease.db")
	store1, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store2, err := platform.Open(ctx, path)
	if err != nil {
		_ = store1.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close(); _ = store1.Close() })
	repo1 := NewRepositoryWithHooks(store1.SQLDB(), ActivationHooks{})
	repo2 := NewRepositoryWithHooks(store2.SQLDB(), ActivationHooks{})
	now := time.Now().UTC().Truncate(time.Second)
	pool := repoDeliveryDigest('9')
	insertDeliveryPool(t, store1, pool)
	basePlan := repoDeliveryPlan(t, now)
	basePlan.Evidence.Rollback.RetentionWindow = "2h"
	oldPlan, err := deployment.NewDeliveryPlan(basePlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo1.CreatePlan(ctx, oldPlan); err != nil {
		t.Fatal(err)
	}
	old := createAdapterArtifact(t, repo1, oldPlan, pool, now, "attempt-adapter-old", "writer-adapter-old", "seal-adapter-old", "candidate-adapter-old", 'a', "", "", 0)
	oldGeneration := deployment.DeliveryGeneration{ID: "generation-adapter-old", CandidateID: old.candidate.ID, PlanID: oldPlan.ID, PlanDigest: oldPlan.Digest, TargetID: oldPlan.TargetID, ProjectID: oldPlan.ProjectID, Environment: oldPlan.Environment, CatalogDigest: old.seal.CatalogDigest, CatalogObjectKey: old.seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: old.candidate.ServingArtifactID, ServingArtifactDigest: old.candidate.ServingArtifactDigest, ServingStateID: old.candidate.ServingStateID, CompatibilityDigest: old.candidate.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe, RollbackUntil: now.Add(2 * time.Hour), CreatedAt: now}
	oldPublication := deployment.DeliveryPublication{ID: "publication-adapter-old", RequestDigest: repoDeliveryDigest('1'), TargetID: oldPlan.TargetID, ProjectID: oldPlan.ProjectID, Environment: oldPlan.Environment, PlanID: oldPlan.ID, PlanDigest: oldPlan.Digest, CandidateID: old.candidate.ID, GenerationID: oldGeneration.ID, ExpectedTargetRevision: 0, CreatedAt: now.Add(6 * time.Minute)}
	if _, err := repo1.CreatePublication(ctx, oldPublication, oldGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := repo1.CommitPublication(ctx, oldPublication.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	adapter := SealedCatalogLeaseAdapter{Repository: repo2, Now: func() time.Time { return now.Add(30 * time.Minute) }}
	oldInput := adapterLeaseInput(old, oldGeneration.ID, "query-adapter-old", now.Add(10*time.Minute))
	if _, err := adapter.AcquireQueryLease(ctx, oldInput); err != nil {
		t.Fatal(err)
	}
	if retry, err := adapter.AcquireQueryLease(ctx, oldInput); err != nil || retry.ID != oldInput.ID {
		t.Fatalf("idempotent old lease=%#v err=%v", retry, err)
	}
	wrong := oldInput
	wrong.GenerationID = "generation-adapter-old"
	wrong.ObjectKey = catalogseal.CanonicalObjectKey(repoDeliveryDigest('b'))
	if _, err := adapter.AcquireQueryLease(ctx, wrong); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("substituted old seal err=%v, want conflict", err)
	}
	newPlan := adapterPlan(t, basePlan, "plan-adapter-new", now.Add(10*time.Minute), oldGeneration.ID, 1, 'b')
	newPlan.Evidence.Rollback.RetentionWindow = "2h50m"
	newPlan, err = deployment.NewDeliveryPlan(newPlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo1.CreatePlan(ctx, newPlan); err != nil {
		t.Fatal(err)
	}
	new := createAdapterArtifact(t, repo1, newPlan, pool, now.Add(10*time.Minute), "attempt-adapter-new", "writer-adapter-new", "seal-adapter-new", "candidate-adapter-new", 'b', oldGeneration.ID, old.seal.CatalogDigest, 1)
	newGeneration := deployment.DeliveryGeneration{ID: "generation-adapter-new", CandidateID: new.candidate.ID, PlanID: newPlan.ID, PlanDigest: newPlan.Digest, TargetID: newPlan.TargetID, ProjectID: newPlan.ProjectID, Environment: newPlan.Environment, CatalogDigest: new.seal.CatalogDigest, CatalogObjectKey: new.seal.ObjectKey, PhysicalPoolID: pool, ServingArtifactID: new.candidate.ServingArtifactID, ServingArtifactDigest: new.candidate.ServingArtifactDigest, ServingStateID: new.candidate.ServingStateID, CompatibilityDigest: new.candidate.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe, RollbackUntil: now.Add(3 * time.Hour), CreatedAt: now.Add(10 * time.Minute)}
	newPublication := deployment.DeliveryPublication{ID: "publication-adapter-new", RequestDigest: repoDeliveryDigest('2'), TargetID: newPlan.TargetID, ProjectID: newPlan.ProjectID, Environment: newPlan.Environment, PlanID: newPlan.ID, PlanDigest: newPlan.Digest, CandidateID: new.candidate.ID, GenerationID: newGeneration.ID, ExpectedBaseGenerationID: oldGeneration.ID, ExpectedTargetRevision: 1, CreatedAt: now.Add(16 * time.Minute)}
	if _, err := repo1.CreatePublication(ctx, newPublication, newGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := repo1.CommitPublication(ctx, newPublication.ID, now.Add(17*time.Minute)); err != nil {
		t.Fatal(err)
	}
	activeGeneration, err := repo2.DeliveryGenerationByID(ctx, newGeneration.ID)
	if err != nil {
		t.Fatal(err)
	}
	retiredGeneration, err := repo2.DeliveryGenerationByID(ctx, oldGeneration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if activeGeneration.Status != deployment.DeliveryGenerationActive || retiredGeneration.Status != deployment.DeliveryGenerationRetired {
		t.Fatalf("published lifecycle active=%s retired=%s", activeGeneration.Status, retiredGeneration.Status)
	}
	oldLease, err := repo2.DeliveryQueryLeaseByID(ctx, oldInput.ID)
	if err != nil {
		t.Fatal(err)
	}
	if oldLease.Status != deployment.DeliveryLeaseActive || oldLease.GenerationID != oldGeneration.ID {
		t.Fatalf("old reader lease=%#v", oldLease)
	}
	roots, err := repo2.EnumerateRoots(ctx, pool, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var leaseRoot *deployment.DeliveryRoot
	for i := range roots.Roots {
		if roots.Roots[i].Kind == "lease" && roots.Roots[i].SourceID == oldInput.ID {
			leaseRoot = &roots.Roots[i]
			break
		}
	}
	if leaseRoot == nil || leaseRoot.GenerationID != oldGeneration.ID || leaseRoot.CatalogDigest != old.seal.CatalogDigest {
		t.Fatalf("long-held reader root=%#v, roots=%#v", leaseRoot, roots.Roots)
	}
	candidateInput := adapterLeaseInput(old, oldGeneration.ID, "query-adapter-candidate", now.Add(10*time.Minute))
	candidateInput.GenerationID = ""
	candidateInput.CandidateID = old.candidate.ID
	if _, err := adapter.AcquireQueryLease(ctx, candidateInput); err != nil {
		t.Fatalf("candidate-bound reader lease: %v", err)
	}
	if _, err := repo2.RetireDeliveryCandidate(ctx, old.candidate.ID, now.Add(time.Hour)); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("candidate retirement with active reader err=%v, want conflict", err)
	}
	if err := adapter.ReleaseQueryLease(ctx, candidateInput.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo2.RetireDeliveryCandidate(ctx, old.candidate.ID, now.Add(90*time.Minute)); err != nil {
		t.Fatalf("candidate retirement after candidate reader release: %v", err)
	}
	newInput := adapterLeaseInput(new, newGeneration.ID, "query-adapter-new", now.Add(20*time.Minute))
	if _, err := adapter.AcquireQueryLease(ctx, newInput); err != nil {
		t.Fatal(err)
	}
	if err := adapter.ReleaseQueryLease(ctx, newInput.ID); err != nil {
		t.Fatal(err)
	}
	gcNow := now.Add(5 * time.Hour)
	if _, err := repo2.HeartbeatQueryLease(ctx, oldInput.ID, now.Add(30*time.Minute), gcNow.Add(time.Hour)); err != nil {
		t.Fatalf("extend long-held reader lease before GC: %v", err)
	}
	gcRoots, err := repo2.EnumerateRootsWithGrace(ctx, pool, gcNow, time.Hour)
	if err != nil {
		t.Fatalf("enumerate roots before GC: %v", err)
	}
	oldLeaseRoot := false
	for _, root := range gcRoots.Roots {
		if root.CatalogDigest != old.seal.CatalogDigest {
			continue
		}
		if root.Kind == "lease" && root.SourceID == oldInput.ID {
			oldLeaseRoot = true
			continue
		}
		if root.Kind == "candidate" || root.Kind == "rollback" || root.Kind == "published" {
			t.Fatalf("retired old catalog retained by non-reader root: %#v", root)
		}
	}
	if !oldLeaseRoot {
		t.Fatalf("old generation query lease missing from GC roots: %#v", gcRoots.Roots)
	}
	gcStore := &sealedLeaseGCStore{objects: map[string]deploymentgc.Object{
		old.seal.ObjectKey: {Key: old.seal.ObjectKey, Digest: old.seal.CatalogDigest, CreatedAt: gcNow.Add(-time.Hour)},
		new.seal.ObjectKey: {Key: new.seal.ObjectKey, Digest: new.seal.CatalogDigest, CreatedAt: gcNow.Add(-time.Hour)},
		"orphan/object":    {Key: "orphan/object", Digest: repoDeliveryDigest('f'), CreatedAt: gcNow.Add(-time.Hour)},
	}}
	gcInspector := sealedLeaseGCInspector{
		old.seal.ObjectKey: {CatalogKey: old.seal.ObjectKey, CatalogDigest: old.seal.CatalogDigest},
		new.seal.ObjectKey: {CatalogKey: new.seal.ObjectKey, CatalogDigest: new.seal.CatalogDigest},
	}
	gcResult, err := (deploymentgc.Collector{
		Control: repo2, Store: gcStore, Inspector: gcInspector, Quarantiner: repo2,
		Config: deploymentgc.Config{PhysicalPoolID: pool, HolderID: "sealed-reader-gc", CycleID: "sealed-reader-gc-cycle", Now: func() time.Time { return gcNow }, OrphanGrace: time.Minute, ReaderGrace: time.Hour},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("GC with long-held reader root: %v", err)
	}
	if gcResult.Deleted != 1 || len(gcStore.deleted) != 1 || gcStore.deleted[0] != "orphan/object" {
		t.Fatalf("GC result=%#v deleted=%#v, want only orphan deletion", gcResult, gcStore.deleted)
	}
	if _, ok := gcStore.objects[old.seal.ObjectKey]; !ok {
		t.Fatal("GC deleted old catalog protected by long-held reader lease")
	}
	if err := adapter.ReleaseQueryLease(ctx, oldInput.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{oldInput.ID, candidateInput.ID, newInput.ID} {
		lease, err := repo2.DeliveryQueryLeaseByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if lease.Status != deployment.DeliveryLeaseReleased {
			t.Fatalf("lease %s status=%s", id, lease.Status)
		}
	}
	roots, err = repo2.EnumerateRoots(ctx, pool, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots.Roots {
		if root.Kind == "lease" && root.SourceID == oldInput.ID {
			t.Fatalf("released reader lease remained GC root: %#v", root)
		}
	}
}
