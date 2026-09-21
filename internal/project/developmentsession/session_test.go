package developmentsession

import (
	"context"
	"errors"
	"strings"
	"testing"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func testKey(owner string) Key {
	return Key{OwnerID: owner, CheckoutID: "checkout_1", WorktreeID: "worktree_1", ProjectID: projectgraph.ResourceID("project_1"), TargetID: "target_1", Environment: "development"}
}

func testIdentity(candidate string) Identity {
	return Identity{CandidateID: candidate, ArtifactDigest: "sha256:" + strings.Repeat("a", 64), GraphDigest: "sha256:" + strings.Repeat("b", 64)}
}

func TestStableKeyAndOwnerIsolation(t *testing.T) {
	first := testKey("owner_1")
	second := testKey("owner_2")
	if first.ID() == second.ID() || first.ID() != first.ID() {
		t.Fatal("session IDs are not stable and owner-scoped")
	}
	store := NewMemoryStore()
	record, err := store.Save(context.Background(), Record{Key: first, Attempted: testIdentity("candidate_1")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if record.Revision != 1 {
		t.Fatalf("revision = %d, want 1", record.Revision)
	}
	if _, err := store.Resolve(context.Background(), second); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner isolation error = %v", err)
	}
	if _, err := store.Save(context.Background(), Record{Key: first, Attempted: testIdentity("candidate_2")}, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale create error = %v", err)
	}
	updated, err := store.Save(context.Background(), Record{Key: first, Attempted: testIdentity("candidate_2")}, record.Revision)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("CAS update = %#v, %v", updated, err)
	}
}

func TestDiagnosticsAreRedactedAndBounded(t *testing.T) {
	message := RedactText("request failed password=hunter2 bearer abc123 https://user:secret@example.test")
	if strings.Contains(message, "hunter2") || strings.Contains(message, "abc123") || strings.Contains(message, "user:secret") {
		t.Fatalf("diagnostic leaked credential: %q", message)
	}
	if _, err := (Diagnostic{Code: "SYNC", Message: "password=secret"}).normalized(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Record{Key: testKey("owner"), LastValid: Identity{ArtifactDigest: "sha256:" + strings.Repeat("a", 64)}}).Normalize(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial identity error = %v", err)
	}
}
