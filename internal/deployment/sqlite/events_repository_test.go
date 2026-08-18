package sqlite

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
)

func TestDeliveryLifecycleProducersAppendRestatementLeaseRetirementAndGCEvents(t *testing.T) {
	ctx := t.Context()
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 14, 0, 0, 0, time.UTC)

	// Restatement planning emits a distinct immutable request event.
	restatement := repoDeliveryPlan(t, now)
	restatement.ID, restatement.TargetID, restatement.Environment = "restatement-plan", "restatement-target", "restatement"
	restatement.Operation = deployment.DeliveryOperationRestatement
	restatement.Digest = ""
	var err error
	restatement, err = deployment.NewDeliveryPlan(restatement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreatePlan(ctx, restatement); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeliveryEventByRequest(ctx, restatement.TargetID, restatement.Digest, "restatement_requested", "plan", restatement.ID); err != nil {
		t.Fatalf("restatement event missing: %v", err)
	}

	// Query-lease acquire/release/expire are all durable transitions.
	candidate, seal := readyEnumerationCandidate(t, store, repo, now.Add(time.Hour))
	if _, err := repo.CreateCandidateReady(ctx, candidate, seal, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// An activation acknowledgement can be lost after the control-plane
	// transaction.  Persist the indeterminate marker and verify that a restart
	// can discover it instead of inferring success from a stale pointer.
	repo.WithDeliveryClock(func() time.Time { return now.Add(90 * time.Minute) })
	publication, err := repo.CreatePublication(ctx, deployment.DeliveryPublication{
		ID: "event-indeterminate-publication", RequestDigest: deployment.CanonicalDeliveryDigest([]byte("event-indeterminate-request")),
		TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment,
		PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest, CandidateID: candidate.ID, GenerationID: "event-indeterminate-generation",
		ExpectedTargetRevision: 0, CreatedAt: now.Add(90 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create indeterminate publication: %v", err)
	}
	if _, err := repo.MarkPublicationIndeterminate(ctx, publication.ID, now.Add(91*time.Minute)); err != nil {
		t.Fatalf("mark publication indeterminate: %v", err)
	}
	if indeterminate, err := repo.HasIndeterminateDeliveryPublication(ctx, candidate.TargetID); err != nil || !indeterminate {
		t.Fatalf("indeterminate publication lookup=%v err=%v", indeterminate, err)
	}
	lease := deployment.DeliveryQueryLease{ID: "event-query-1", HolderID: "reader-principal", CandidateID: candidate.ID, CatalogDigest: candidate.CatalogDigest, PhysicalPoolID: candidate.PhysicalPoolID, CreatedAt: now.Add(2 * time.Hour), ExpiresAt: now.Add(3 * time.Hour)}
	if _, _, err := repo.AcquireQueryLeaseAgainstRoot(ctx, lease); err != nil {
		t.Fatal(err)
	}
	acquireDigest := deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:" + lease.ID))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, acquireDigest, "lease_acquired", "query_lease", lease.ID); err != nil {
		t.Fatalf("query lease acquire event missing: %v", err)
	}
	if _, err := repo.ReleaseQueryLease(ctx, lease.ID, lease.CreatedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	releaseDigest := deployment.CanonicalDeliveryDigest([]byte("query-lease-released:" + lease.ID))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, releaseDigest, "lease_released", "query_lease", lease.ID); err != nil {
		t.Fatalf("query lease release event missing: %v", err)
	}
	expiring := lease
	expiring.ID = "event-query-2"
	expiring.CreatedAt = now.Add(2 * time.Hour)
	expiring.ExpiresAt = now.Add(2*time.Hour + 10*time.Minute)
	if _, _, err := repo.AcquireQueryLeaseAgainstRoot(ctx, expiring); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ExpireQueryLease(ctx, expiring.ID, expiring.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	expireDigest := deployment.CanonicalDeliveryDigest([]byte("query-lease-expired:" + expiring.ID))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, expireDigest, "lease_expired", "query_lease", expiring.ID); err != nil {
		t.Fatalf("query lease expire event missing: %v", err)
	}

	// Retirement is fenced by the same candidate pool and leaves an immutable
	// event even after all query leases have been released/expired.
	if _, err := repo.RetireDeliveryCandidate(ctx, candidate.ID, now.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	retireDigest := deployment.CanonicalDeliveryDigest([]byte("candidate-retired:" + candidate.ID))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, retireDigest, "candidate_retired", "candidate", candidate.ID); err != nil {
		t.Fatalf("candidate retirement event missing: %v", err)
	}

	// GC transitions and each delete intent append events in their projection
	// transactions. Reuse the candidate's pool so the target scope is resolvable.
	cycle, err := repo.CreateGCCycle(ctx, deployment.DeliveryGCCycle{ID: "event-gc-cycle", ActorID: "maintenance-principal", PhysicalPoolID: candidate.PhysicalPoolID, Epoch: 1, RootRevision: 0, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if cycle, err = repo.MarkGCCycle(ctx, cycle.ID, deployment.CanonicalDeliveryDigest([]byte("event-mark"))); err != nil {
		t.Fatal(err)
	}
	if cycle, err = repo.BeginGCDelete(ctx, cycle.ID); err != nil {
		t.Fatal(err)
	}
	intent, err := repo.CreateGCDeleteIntent(ctx, deployment.DeliveryGCDeleteIntent{ID: "event-gc-delete", CycleID: cycle.ID, PhysicalPoolID: cycle.PhysicalPoolID, ObjectKey: "orphan/object", ObjectDigest: repoDeliveryDigest('e'), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CompleteGCDeleteIntent(ctx, intent.ID, deployment.DeliveryGCDeleteDeleted, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	deleteDigest := deployment.CanonicalDeliveryDigest([]byte("gc-deleted:" + intent.ID + ":" + string(deployment.DeliveryGCDeleteDeleted)))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, deleteDigest, "gc_deleted", "gc_cycle", intent.ID); err != nil {
		t.Fatalf("GC delete event missing: %v", err)
	}
	if _, err := repo.CompleteGCCycle(ctx, cycle.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	completeDigest := deployment.CanonicalDeliveryDigest([]byte("gc-complete:" + cycle.ID))
	if _, err := repo.DeliveryEventByRequest(ctx, candidate.TargetID, completeDigest, "cleanup_completed", "gc_cycle", cycle.ID); err != nil {
		t.Fatalf("GC completion event missing: %v", err)
	}
}

func TestDeliveryEventLedgerIsAppendOnlyAndCrashIdempotent(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	event := deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, plan.Digest, "plan_created", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: plan.ActorID, EventKind: "plan_created", ObjectKind: "plan", ObjectID: plan.ID,
		RequestDigest: plan.Digest, PlanDigest: plan.Digest, Outcome: "accepted",
		Details: map[string]any{"base_revision": plan.BaseTargetRevision}, CreatedAt: now,
	}
	first, err := repo.AppendDeliveryEvent(t.Context(), event)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repo.AppendDeliveryEvent(t.Context(), event)
	if err != nil || replayed.ID != first.ID || !replayed.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("event replay=%#v err=%v", replayed, err)
	}
	drifted := event
	drifted.ActorID = "different-actor"
	drifted.ResultDigest = repoDeliveryDigest('f')
	drifted.Details = map[string]any{"base_revision": int64(99)}
	if _, err := repo.AppendDeliveryEvent(t.Context(), drifted); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("drifted event replay error=%v, want conflict", err)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM delivery_events WHERE target_id = ?", plan.TargetID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("event count=%d, want 1", count)
	}
	var triggerCount int
	if err := store.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'delivery_events_%' ").Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if triggerCount != 2 {
		t.Fatalf("event trigger count=%d, want 2", triggerCount)
	}
	if result, err := store.SQLDB().ExecContext(t.Context(), "UPDATE delivery_events SET outcome='failed' WHERE id=?", first.ID); err == nil {
		rows, _ := result.RowsAffected()
		t.Fatalf("event update unexpectedly succeeded (rows=%d id=%q)", rows, first.ID)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), "DELETE FROM delivery_events WHERE id=?", first.ID); err == nil {
		t.Fatal("event delete unexpectedly succeeded")
	}
	if _, err := repo.DeliveryEventByRequest(t.Context(), plan.TargetID, plan.Digest, event.EventKind, event.ObjectKind, event.ObjectID); err != nil {
		t.Fatalf("event missing after rejected mutation: %v", err)
	}
}
