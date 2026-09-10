package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// Cache invalidation consumes these existing durable identities, not a second
// lifecycle counter. Returning to the same value cannot revive an old tuple.
func TestSemanticLifecycleCacheEvidencePostgreSQL18(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID, RequestID: "lifecycle-cache", CorrelationID: "lifecycle-evidence"}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{Name: "cache_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation})
	if err != nil {
		t.Fatal(err)
	}
	input := access.SemanticAttributeAssignmentInput{DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID}, Values: "private-region-one", Mutation: mutation}
	assignment, err := repo.SetSemanticAttributeAssignment(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	created := lifecycleControlState(t, repo)
	input.ExpectedVersion = assignment.AssignmentVersion
	if _, err := repo.SetSemanticAttributeAssignment(ctx, input); err != nil {
		t.Fatal(err)
	}
	if replay := lifecycleControlState(t, repo); replay != created {
		t.Fatalf("idempotent replay changed invalidation identity: %v != %v", replay, created)
	}
	input.Values = "private-region-two"
	updated, err := repo.SetSemanticAttributeAssignment(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	changed := lifecycleControlState(t, repo)
	if changed.Revision != created.Revision+1 || changed.Digest == created.Digest {
		t.Fatal("assignment update did not advance cache invalidation identity")
	}
	if _, err := repo.SetSemanticAttributeAssignment(ctx, input); !errors.Is(err, access.ErrSemanticAttributeAssignmentConflict) {
		t.Fatalf("stale write = %v, want version conflict", err)
	}
	if stale := lifecycleControlState(t, repo); stale != changed {
		t.Fatal("rejected stale write changed control identity")
	}
	if _, err := repo.TombstoneSemanticAttributeAssignment(ctx, updated.ID, updated.AssignmentVersion, mutation); err != nil {
		t.Fatal(err)
	}
	tombstone := lifecycleControlState(t, repo)
	if tombstone.Revision != changed.Revision+1 || tombstone.Digest == changed.Digest {
		t.Fatal("tombstone did not invalidate the prior control identity")
	}
	input.ExpectedVersion, input.Values = 0, "private-region-one"
	recreated, err := repo.SetSemanticAttributeAssignment(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	restored := lifecycleControlState(t, repo)
	if recreated.ID == assignment.ID || recreated.AssignmentVersion != 1 || restored.Revision != tombstone.Revision+1 || restored.Digest == created.Digest {
		t.Fatal("new incarnation resurrected an old assignment or cache identity")
	}
	before, err := repo.SemanticAttributeRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.SetSemanticAttributeEnabledExpected(ctx, definition.Name, false, definition.DefinitionVersion, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSemanticAttributeEnabledExpected(ctx, definition.Name, true, disabled.DefinitionVersion, mutation); err != nil {
		t.Fatal(err)
	}
	after, err := repo.SemanticAttributeRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.State.Revision != before.State.Revision+2 || after.State.Digest == before.State.Digest {
		t.Fatal("disable/re-enable resurrected the prior registry identity")
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PrincipalID: auditActorID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range events {
		if !strings.HasPrefix(event.Action, "semantic_attribute.") {
			continue
		}
		actions[event.Action] = true
		if event.RequestID != mutation.RequestID || event.CorrelationID != mutation.CorrelationID || event.PrincipalID != auditActorID {
			t.Fatal("lifecycle audit lost actor/request/source correlation")
		}
		if strings.Contains(event.MetadataJSON, "private-region-") || strings.Contains(event.MetadataJSON, "canonicalValues") {
			t.Fatal("lifecycle audit exposed attribute values")
		}
	}
	for _, action := range []string{access.SemanticAttributeAuditActionRegister, access.SemanticAttributeAuditActionAssignmentSet, access.SemanticAttributeAuditActionAssignmentReplay, access.SemanticAttributeAuditActionAssignmentTombstone, access.SemanticAttributeAuditActionDisable, access.SemanticAttributeAuditActionEnable} {
		if !actions[action] {
			t.Fatalf("required lifecycle audit missing: %s", action)
		}
	}
}

func lifecycleControlState(t *testing.T, repo *Repository) access.SemanticAttributeControlState {
	t.Helper()
	snapshot, err := repo.SemanticAttributeControl(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.State
}

func TestSemanticLifecycleCacheEvidenceConcurrentUpdatesPostgreSQL18(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	definition, err := repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{Name: "concurrent_cache_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation})
	if err != nil {
		t.Fatal(err)
	}
	input := access.SemanticAttributeAssignmentInput{DefinitionID: definition.ID, Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID}, Values: "original", Mutation: mutation}
	assignment, err := repo.SetSemanticAttributeAssignment(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	before := lifecycleControlState(t, repo)
	input.ExpectedVersion = assignment.AssignmentVersion
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, value := range []string{"winner-a", "winner-b"} {
		go func(value string) {
			update := input
			update.Values = value
			<-start
			_, err := repo.SetSemanticAttributeAssignment(t.Context(), update)
			results <- err
		}(value)
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, access.ErrSemanticAttributeAssignmentConflict):
			conflicted++
		default:
			t.Fatalf("concurrent mutation: %v", err)
		}
	}
	after := lifecycleControlState(t, repo)
	if succeeded != 1 || conflicted != 1 || after.Revision != before.Revision+1 || after.Digest == before.Digest {
		t.Fatalf("concurrent invalidation: success=%d conflict=%d before=%v after=%v", succeeded, conflicted, before, after)
	}
}
