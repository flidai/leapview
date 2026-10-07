package saved

import (
	"errors"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestAggregateVersionAndArchiveTransitionsUseCompleteCAS(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	first, err := NewRevision("revision-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}
	record, err := NewSavedExploration(NewInput{
		ProjectID:        projectgraph.ResourceID("project:sales"),
		ID:               "exploration-1",
		OwnerPrincipalID: "owner",
		Title:            "Orders",
		Slug:             "orders",
		Visibility:       VisibilityPrivate,
		SemanticModelID:  "semantic:sales",
		CreatedAt:        now,
		Revision:         first,
	})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if record.Title != "Orders" {
		t.Fatalf("title = %q, want canonical trimmed title", record.Title)
	}

	if _, err := AppendVersion(record, RevisionToken{RevisionID: "revision-0", Number: 1, ContentHash: first.Metadata.ContentHash}, first, now.Add(time.Minute)); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale version error = %v, want stale revision", err)
	}

	next, err := NewRevision("revision-2", 2, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatalf("next revision: %v", err)
	}
	updated, err := AppendVersion(record, record.Revision.Token(), next, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("append version: %v", err)
	}
	if updated.ID != record.ID || updated.ProjectID != record.ProjectID || updated.Slug != record.Slug || updated.Revision.Token() != next.Token() {
		t.Fatal("version transition changed stable identity or token")
	}
	metadataUpdated, err := AppendVersionWithMetadata(record, record.Revision.Token(), next, now.Add(time.Minute), "Orders v2", "orders-v2", VisibilityOrganization, "semantic:sales")
	if err != nil {
		t.Fatalf("append version with metadata: %v", err)
	}
	if metadataUpdated.Title != "Orders v2" || metadataUpdated.Slug != "orders-v2" || metadataUpdated.Visibility != VisibilityOrganization {
		t.Fatal("version transition did not apply explicit next metadata")
	}
	opened, err := Open(metadataUpdated.Lifecycle(), next)
	if err != nil {
		t.Fatalf("open metadata plus exact revision: %v", err)
	}
	if opened.Lifecycle.CurrentRevision.Token() != next.Token() {
		t.Fatal("open result did not preserve exact lifecycle revision token")
	}
	mismatchedGeneration := next.Clone()
	mismatchedGeneration.Metadata.ServingIdentity.GenerationID = "generation-2"
	if _, err := Open(metadataUpdated.Lifecycle(), mismatchedGeneration); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("same-token generation mismatch error = %v, want stale revision", err)
	}
	mismatchedCreator := next.Clone()
	mismatchedCreator.Metadata.CreatedBy = "another-owner"
	if _, err := Open(metadataUpdated.Lifecycle(), mismatchedCreator); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("same-token creator mismatch error = %v, want stale revision", err)
	}

	archived, err := Archive(updated, updated.Revision.Token(), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if !archived.IsArchived() || archived.Revision.Token() != updated.Revision.Token() {
		t.Fatal("archive changed revision or did not retain archive state")
	}
	if _, err := AppendVersion(archived, archived.Revision.Token(), next, now.Add(3*time.Minute)); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived update error = %v, want archived", err)
	}
	if _, err := Archive(archived, archived.Revision.Token(), now.Add(3*time.Minute)); !errors.Is(err, ErrArchived) {
		t.Fatalf("repeat archive error = %v, want archived", err)
	}
}

func TestCreateRequiresFirstRevisionAndCanonicalTitle(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	second, err := NewRevision("revision-2", 2, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-2", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-2", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: second,
	}); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("revision 2 create error = %v, want invalid revision", err)
	}
	first, err := NewRevision("revision-1-title", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-title", OwnerPrincipalID: "owner", Title: " Orders ", Slug: "orders-title", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: first,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("noncanonical title error = %v, want invalid", err)
	}
	invalidUTF8Title := string([]byte{'O', 'r', 'd', 'e', 'r', '\xff'})
	if _, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-invalid-title", OwnerPrincipalID: "owner", Title: invalidUTF8Title, Slug: "orders-invalid-title", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: first,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 title error = %v, want invalid", err)
	}
}

func TestAppendVersionRejectsBackwardTimeAndSupportsModelSwitch(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	first, err := NewRevision("revision-append-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}
	record, err := NewSavedExploration(NewInput{ProjectID: "project:sales", ID: "exploration-append", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-append", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: first})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	next, err := NewRevision("revision-append-2", 2, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatalf("next revision: %v", err)
	}
	if _, err := AppendVersionWithMetadata(record, record.Revision.Token(), next, now.Add(-time.Minute), "Orders", "orders-append", VisibilityPrivate, "semantic:sales"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("backward timestamp error = %v, want invalid", err)
	}
	otherModel := testSpec()
	otherModel.ModelID = "semantic:other"
	otherPayload, err := NewExplorationSpecPayload(otherModel)
	if err != nil {
		t.Fatalf("other model payload: %v", err)
	}
	otherNext, err := NewRevision("revision-append-3", 2, now.Add(time.Minute), "owner", otherPayload, identity)
	if err != nil {
		t.Fatalf("other model revision: %v", err)
	}
	switched, err := AppendVersionWithMetadata(record, record.Revision.Token(), otherNext, now.Add(time.Minute), "Other Orders", "other-orders", VisibilityRestricted, "semantic:other")
	if err != nil {
		t.Fatalf("model switch: %v", err)
	}
	if switched.SemanticModelID != "semantic:other" {
		t.Fatalf("semantic model = %q, want semantic:other", switched.SemanticModelID)
	}
}

func TestAggregateRejectsInvalidProjectSlugAndTimestamp(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	revision, err := NewRevision("revision-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	base := NewInput{ProjectID: "project:sales", ID: "exploration-1", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision}
	for _, test := range []struct {
		name   string
		mutate func(*NewInput)
	}{
		{name: "invalid project", mutate: func(input *NewInput) { input.ProjectID = "project with spaces" }},
		{name: "invalid slug", mutate: func(input *NewInput) { input.Slug = "Orders" }},
		{name: "non-UTC timestamp", mutate: func(input *NewInput) { input.CreatedAt = now.In(time.FixedZone("offset", 3600)) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			if _, err := NewSavedExploration(input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: projectgraph.ResourceID("project:sales"), ID: "exploration-1", OwnerPrincipalID: "tenant/user@example.com",
		Title: "Orders", Slug: "orders", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision,
	}); err != nil {
		t.Fatalf("accepted subject-style owner ID: %v", err)
	}
	otherProjectIdentity, err := projectgraph.NewServingIdentity("project:other", "production", "generation-1")
	if err != nil {
		t.Fatalf("other project identity: %v", err)
	}
	otherRevision, err := NewRevision("revision-other", 1, now, "owner", payload, otherProjectIdentity)
	if err != nil {
		t.Fatalf("other project revision: %v", err)
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-other", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-other", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: otherRevision,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("identity project mismatch error = %v, want invalid", err)
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-model-other", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-model", Visibility: VisibilityPrivate, SemanticModelID: "semantic:other", CreatedAt: now, Revision: revision,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("semantic model mismatch error = %v, want invalid", err)
	}
	if _, err := NewRevision("revision-empty-owner", 1, now, "", payload, identity); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("empty revision creator error = %v, want invalid revision", err)
	}
	longProject := projectgraph.ResourceID("project:" + strings.Repeat("x", 150))
	longIdentity, err := projectgraph.NewServingIdentity(longProject, "production", "generation-1")
	if err != nil {
		t.Fatalf("long project identity: %v", err)
	}
	longRevision, err := NewRevision("revision-long-project", 1, now, "owner", payload, longIdentity)
	if err != nil {
		t.Fatalf("long project revision: %v", err)
	}
	if _, err := NewSavedExploration(NewInput{
		ProjectID: longProject, ID: "exploration-long-project", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-long-project", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: longRevision,
	}); err != nil {
		t.Fatalf("project ID was capped beyond graph validation: %v", err)
	}
}

func testIdentity(t *testing.T) projectgraph.ServingIdentity {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity("project:sales", "production", "generation-1")
	if err != nil {
		t.Fatalf("serving identity: %v", err)
	}
	return identity
}

func TestPublicNotFoundHidesUnauthorized(t *testing.T) {
	if PublicNotFound(ErrUnauthorized) != ErrNotFound {
		t.Fatal("unauthorized error was distinguishable")
	}
	if PublicNotFound(ErrNotFound) != ErrNotFound {
		t.Fatal("not found error was not preserved")
	}
	other := errors.New("database unavailable")
	if PublicNotFound(other) != other {
		t.Fatal("unrelated error was hidden")
	}
}
