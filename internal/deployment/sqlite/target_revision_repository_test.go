package sqlite

import (
	"testing"
	"time"
)

func TestBumpTargetRevisionPersistsComponentEvidenceAtomically(t *testing.T) {
	store, repo := openDeliveryRepository(t)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	plan := repoDeliveryPlan(t, now)
	if _, err := repo.CreatePlan(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	digest := repoDeliveryDigest('9')
	revision, err := repo.BumpTargetRevision(t.Context(), plan.TargetID, "semantic_binding", "semantic_model/revenue", digest, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("revision=%d, want 1", revision)
	}
	var kind, componentID, componentDigest, operation string
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT component_kind,component_id,component_digest,operation FROM delivery_target_revision_components WHERE target_id=? AND target_revision=?`, plan.TargetID, revision).Scan(&kind, &componentID, &componentDigest, &operation); err != nil {
		t.Fatal(err)
	}
	if kind != "semantic_binding" || componentID != "semantic_model/revenue" || componentDigest != digest || operation != "cas" {
		t.Fatalf("component evidence=%q/%q/%q/%q", kind, componentID, componentDigest, operation)
	}
}
