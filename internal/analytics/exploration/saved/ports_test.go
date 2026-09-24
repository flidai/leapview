package saved

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestUpdateVersionInputRejectsCrossProjectServingIdentity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	otherIdentity, err := servingIdentity("project:other")
	if err != nil {
		t.Fatalf("other identity: %v", err)
	}
	next, err := NewRevision("revision-cross-project", 2, now.Add(time.Minute), "owner", payload, otherIdentity)
	if err != nil {
		t.Fatalf("next revision: %v", err)
	}
	evidence := testEvidence(t, MutationActionUpdate)
	input := UpdateVersionInput{
		ProjectID: "project:sales", ID: "exploration-1", ExpectedRevision: RevisionToken{RevisionID: "revision-1", Number: 1, ContentHash: payload.ContentHash()},
		Revision: next, Title: "Orders", Slug: "orders", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", UpdatedAt: now.Add(time.Minute), Evidence: evidence,
	}
	if err := input.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-project update error = %v, want invalid", err)
	}
}

func TestDuplicateInputRejectsDifferentDestinationPayload(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("source payload: %v", err)
	}
	otherSpec := testSpec()
	otherSpec.Limit = 99
	destinationPayload, err := NewExplorationSpecPayload(otherSpec)
	if err != nil {
		t.Fatalf("destination payload: %v", err)
	}
	identity := testIdentity(t)
	sourceRevision, err := NewRevision("revision-source", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("source revision: %v", err)
	}
	destinationRevision, err := NewRevision("revision-destination", 1, now, "owner", destinationPayload, identity)
	if err != nil {
		t.Fatalf("destination revision: %v", err)
	}
	input := DuplicateInput{
		ProjectID: "project:sales", SourceID: "exploration-source", ExpectedSourceRevision: sourceRevision.Token(), Evidence: testEvidence(t, MutationActionDuplicate),
		Destination: CreateInput{ProjectID: "project:sales", ID: "exploration-copy", OwnerPrincipalID: "owner", Title: "Orders Copy", Slug: "orders-copy", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: destinationRevision},
	}
	if err := input.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("different duplicate payload error = %v, want conflict", err)
	}
}

func TestMutationInputsRequireValidatedRetryEvidence(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	revision, err := NewRevision("revision-evidence", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	base := CreateInput{ProjectID: "project:sales", ID: "exploration-evidence", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-evidence", Visibility: VisibilityPrivate, SemanticModelID: "semantic:sales", CreatedAt: now, Revision: revision}
	if err := base.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing create evidence error = %v, want invalid", err)
	}
	base.Evidence = testEvidence(t, MutationActionCreate)
	if err := base.Validate(); err != nil {
		t.Fatalf("valid create evidence rejected: %v", err)
	}
	invalid := base
	invalid.Evidence.Fingerprint = "fingerprint-not-a-digest"
	if err := invalid.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid fingerprint error = %v, want invalid", err)
	}
	wrongAction := base
	wrongAction.Evidence.Action = MutationActionArchive
	if err := wrongAction.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong action error = %v, want invalid", err)
	}
	lookup := MutationLookupInput{ProjectID: "project:sales", ActorID: base.Evidence.ActorID, Action: base.Evidence.Action, IdempotencyKey: base.Evidence.IdempotencyKey, Fingerprint: base.Evidence.Fingerprint}
	if err := lookup.Validate(); err != nil {
		t.Fatalf("valid lookup rejected: %v", err)
	}
}

func TestPersistenceInputsBindOwnerAndRevisionCreatorToActor(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	first, err := NewRevision("revision-actor-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}
	create := CreateInput{
		ProjectID: "project:sales", ID: "exploration-actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders-actor", Visibility: VisibilityPrivate,
		SemanticModelID: "semantic:sales", CreatedAt: now, Revision: first, Evidence: testEvidence(t, MutationActionCreate),
	}
	if err := create.Validate(); err != nil {
		t.Fatalf("valid create rejected: %v", err)
	}
	ownerMismatch := create
	ownerMismatch.OwnerPrincipalID = "actor-b"
	if err := ownerMismatch.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("owner mismatch error = %v, want invalid", err)
	}
	creatorMismatch := create
	creatorMismatch.Revision.Metadata.CreatedBy = "actor-b"
	if err := creatorMismatch.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("creator mismatch error = %v, want invalid", err)
	}

	next, err := NewRevision("revision-actor-2", 2, now.Add(time.Minute), "actor-b", payload, identity)
	if err != nil {
		t.Fatalf("next revision: %v", err)
	}
	update := UpdateVersionInput{
		ProjectID: "project:sales", ID: create.ID, ExpectedRevision: first.Token(), Revision: next, Title: create.Title, Slug: create.Slug,
		Visibility: create.Visibility, SemanticModelID: create.SemanticModelID, UpdatedAt: now.Add(time.Minute), Evidence: testEvidence(t, MutationActionUpdate),
	}
	if err := update.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update creator mismatch error = %v, want invalid", err)
	}

	destination := create
	destination.ID = "exploration-actor-copy"
	destination.Evidence = MutationEvidence{}
	destination.OwnerPrincipalID = "actor-b"
	duplicate := DuplicateInput{
		ProjectID: "project:sales", SourceID: create.ID, ExpectedSourceRevision: first.Token(), Destination: destination,
		Evidence: testEvidence(t, MutationActionDuplicate),
	}
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate actor mismatch error = %v, want invalid", err)
	}
	destination.OwnerPrincipalID = duplicate.Evidence.ActorID
	creatorMismatchDestination := destination
	creatorMismatchDestination.Revision.Metadata.CreatedBy = "actor-b"
	duplicate.Destination = creatorMismatchDestination
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate creator mismatch error = %v, want invalid", err)
	}
	destination.Revision.Metadata.CreatedBy = duplicate.Evidence.ActorID
	duplicate.Destination = destination
	if err := duplicate.Validate(); err != nil {
		t.Fatalf("valid duplicate actor binding rejected: %v", err)
	}
}

func TestMutationEvidenceBoundariesAndUTF8(t *testing.T) {
	base := testEvidence(t, MutationActionCreate)
	tests := []struct {
		name string
		max  int
		set  func(*MutationEvidence, string)
	}{
		{name: "idempotency key", max: MaxIdempotencyKeyLength, set: func(e *MutationEvidence, value string) { e.IdempotencyKey = value }},
		{name: "request id", max: MaxRequestIDLength, set: func(e *MutationEvidence, value string) { e.RequestID = value }},
		{name: "correlation id", max: MaxCorrelationIDLength, set: func(e *MutationEvidence, value string) { e.CorrelationID = value }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			atLimit := base
			test.set(&atLimit, strings.Repeat("x", test.max))
			if err := atLimit.Validate(); err != nil {
				t.Fatalf("value at limit rejected: %v", err)
			}
			overLimit := base
			test.set(&overLimit, strings.Repeat("x", test.max+1))
			if err := overLimit.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("value over limit error = %v, want invalid", err)
			}
			invalidUTF8 := base
			test.set(&invalidUTF8, string([]byte{'x', '\xff'}))
			if err := invalidUTF8.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid UTF-8 error = %v, want invalid", err)
			}
		})
	}

	adminAtLimit := base
	adminAtLimit.AdminOverride = true
	adminAtLimit.AdminReason = strings.Repeat("x", MaxAdminReasonLength)
	if err := adminAtLimit.Validate(); err != nil {
		t.Fatalf("admin reason at limit rejected: %v", err)
	}
	adminOverLimit := adminAtLimit
	adminOverLimit.AdminReason = strings.Repeat("x", MaxAdminReasonLength+1)
	if err := adminOverLimit.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("admin reason over limit error = %v, want invalid", err)
	}
	adminInvalidUTF8 := adminAtLimit
	adminInvalidUTF8.AdminReason = string([]byte{'x', '\xff'})
	if err := adminInvalidUTF8.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("admin reason invalid UTF-8 error = %v, want invalid", err)
	}
}

func TestAuditIntentContextCarriesTypedAccessIntent(t *testing.T) {
	intent := access.AuditIntent{EventID: "saved-exploration:event-1", PrincipalID: "actor-a", RequestID: "request-1"}
	ctx := WithAuditIntent(t.Context(), intent)
	value, ok := AuditIntentFromContext(ctx)
	if !ok {
		t.Fatal("audit intent was not carried")
	}
	if value != intent {
		t.Fatal("audit intent value changed")
	}
}

func TestMutationResultValidatesActionRevisionAndModelConsistency(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	first, err := NewRevision("revision-result-1", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	record, err := NewSavedExploration(NewInput{
		ProjectID: "project:sales", ID: "exploration-result", OwnerPrincipalID: "owner",
		Title: "Orders", Slug: "orders-result", Visibility: VisibilityPrivate,
		SemanticModelID: "semantic:sales", CreatedAt: now, Revision: first,
	})
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	revision := record.Revision.Clone()
	createResult := MutationResult{
		Lifecycle: record.Lifecycle(), Revision: &revision, AppliedRevision: revision.Token(),
		Evidence: testEvidence(t, MutationActionCreate),
	}
	if err := createResult.Validate(); err != nil {
		t.Fatalf("valid create result: %v", err)
	}
	replayMetadata := MutationReplayMetadata{Lifecycle: record.Lifecycle(), AppliedRevision: record.Revision.Token(), Evidence: createResult.Evidence}
	if err := replayMetadata.Validate(); err != nil {
		t.Fatalf("valid replay metadata: %v", err)
	}
	wrongReplayToken := replayMetadata
	wrongReplayToken.AppliedRevision.RevisionID = "revision-other"
	if err := wrongReplayToken.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay token mismatch error = %v, want conflict", err)
	}
	wrongReplayOwner := replayMetadata
	wrongReplayOwner.Lifecycle.OwnerPrincipalID = "actor-other"
	if err := wrongReplayOwner.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("replay owner mismatch error = %v, want invalid", err)
	}

	createWithoutRevision := createResult
	createWithoutRevision.Revision = nil
	if err := createWithoutRevision.Validate(); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("create without revision error = %v, want invalid revision", err)
	}
	createWithWrongActor := createResult
	createWithWrongActor.Evidence.ActorID = "actor:other"
	if err := createWithWrongActor.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create actor mismatch error = %v, want invalid", err)
	}
	createWithWrongModel := createResult
	createWithWrongModel.Lifecycle.SemanticModelID = "semantic:other"
	if err := createWithWrongModel.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create model mismatch error = %v, want invalid", err)
	}

	archived, err := Archive(record, record.Revision.Token(), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	archiveResult := MutationResult{
		Lifecycle: archived.Lifecycle(), AppliedRevision: archived.Revision.Token(), ConcurrencyRevision: record.Revision.Token(),
		Evidence: testEvidence(t, MutationActionArchive),
	}
	if err := archiveResult.Validate(); err != nil {
		t.Fatalf("valid archive result: %v", err)
	}
	archiveMetadata := MutationReplayMetadata{Lifecycle: archived.Lifecycle(), AppliedRevision: archived.Revision.Token(), Evidence: archiveResult.Evidence}
	if err := archiveMetadata.Validate(); err != nil {
		t.Fatalf("valid archive replay metadata: %v", err)
	}
	archiveMetadata.Lifecycle.Status = StatusActive
	if err := archiveMetadata.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archive replay active lifecycle error = %v, want invalid", err)
	}
	archiveWithRevision := archiveResult
	archiveRevision := archived.Revision.Clone()
	archiveWithRevision.Revision = &archiveRevision
	if err := archiveWithRevision.Validate(); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("archive with revision error = %v, want invalid revision", err)
	}
	archiveWithActiveLifecycle := archiveResult
	archiveWithActiveLifecycle.Lifecycle = record.Lifecycle()
	archiveWithActiveLifecycle.AppliedRevision = record.Revision.Token()
	if err := archiveWithActiveLifecycle.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archive active lifecycle error = %v, want invalid", err)
	}
}

func TestUpdateVersionInputRequiresNextRevisionIdentity(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	payload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	identity := testIdentity(t)
	first, err := NewRevision("revision-update-expected", 1, now, "owner", payload, identity)
	if err != nil {
		t.Fatalf("first revision: %v", err)
	}
	evidence := testEvidence(t, MutationActionUpdate)
	makeInput := func(revision Revision) UpdateVersionInput {
		return UpdateVersionInput{
			ProjectID: "project:sales", ID: "exploration-update", ExpectedRevision: first.Token(),
			Revision: revision, Title: "Orders", Slug: "orders-update", Visibility: VisibilityPrivate,
			SemanticModelID: "semantic:sales", UpdatedAt: now.Add(time.Minute), Evidence: evidence,
		}
	}

	sameNumber, err := NewRevision("revision-update-next", 1, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatalf("same-number revision: %v", err)
	}
	if err := makeInput(sameNumber).Validate(); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("same-number update error = %v, want invalid revision", err)
	}
	sameID, err := NewRevision("revision-update-expected", 2, now.Add(time.Minute), "owner", payload, identity)
	if err != nil {
		t.Fatalf("same-id revision: %v", err)
	}
	if err := makeInput(sameID).Validate(); !errors.Is(err, ErrInvalidRevision) {
		t.Fatalf("same-id update error = %v, want invalid revision", err)
	}
}

func TestServiceRequestValidationBindsActorAndAuthoredSources(t *testing.T) {
	validPayload, err := NewExplorationSpecPayload(testSpec())
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	createEvidence := testEvidence(t, MutationActionCreate)
	create := CreateRequest{
		ProjectID: "project:sales", ID: "exploration-request", ActorID: "owner", Title: "Orders",
		Slug: "orders-request", Visibility: VisibilityPrivate, Payload: validPayload, Evidence: createEvidence,
	}
	if err := create.Validate(); err != nil {
		t.Fatalf("valid create request: %v", err)
	}
	fromSpec := create
	fromSpec.Payload = ExplorationSpecPayload{}
	fromSpec.Spec = testSpec()
	if err := fromSpec.Validate(); err != nil {
		t.Fatalf("spec-only create request: %v", err)
	}
	bothSources := create
	bothSources.Spec = testSpec()
	if err := bothSources.Validate(); err != nil {
		t.Fatalf("equal create sources: %v", err)
	}
	conflictingSpec := testSpec()
	conflictingSpec.Limit = 99
	conflicting := create
	conflicting.Spec = conflictingSpec
	if err := conflicting.Validate(); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting create sources error = %v, want conflict", err)
	}
	if _, err := (CreateRequest{ProjectID: "project:sales", ID: "exploration-empty", ActorID: "owner", Title: "Orders", Slug: "orders-empty", Visibility: VisibilityPrivate, Evidence: createEvidence}).ValidatedPayload(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("missing authored source error = %v, want invalid payload", err)
	}

	actorMismatch := create
	actorMismatch.ActorID = "actor:other"
	if err := actorMismatch.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("create actor mismatch error = %v, want invalid", err)
	}

	updateEvidence := testEvidence(t, MutationActionUpdate)
	update := UpdateVersionRequest{
		ProjectID: "project:sales", ID: "exploration-request", ActorID: "owner",
		ExpectedRevision: RevisionToken{RevisionID: "revision-1", Number: 1, ContentHash: validPayload.ContentHash()},
		Title:            "Orders", Slug: "orders-request", Visibility: VisibilityPrivate,
		Payload: validPayload, Evidence: updateEvidence,
	}
	if err := update.Validate(); err != nil {
		t.Fatalf("valid update request: %v", err)
	}
	update.ActorID = "actor:other"
	if err := update.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("update actor mismatch error = %v, want invalid", err)
	}

	duplicate := DuplicateRequest{
		ProjectID: "project:sales", SourceID: "exploration-request", ExpectedSourceRevision: update.ExpectedRevision,
		ID: "exploration-copy", ActorID: "owner", Title: "Orders Copy", Slug: "orders-copy",
		Visibility: VisibilityPrivate, Evidence: testEvidence(t, MutationActionDuplicate),
	}
	if err := duplicate.Validate(); err != nil {
		t.Fatalf("valid duplicate request: %v", err)
	}
	duplicate.ActorID = "actor:other"
	if err := duplicate.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate actor mismatch error = %v, want invalid", err)
	}

	archive := ArchiveRequest{
		ProjectID: "project:sales", ID: "exploration-request", ActorID: "owner",
		ExpectedRevision: update.ExpectedRevision, Evidence: testEvidence(t, MutationActionArchive),
	}
	if err := archive.Validate(); err != nil {
		t.Fatalf("valid archive request: %v", err)
	}
	archive.ActorID = "actor:other"
	if err := archive.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("archive actor mismatch error = %v, want invalid", err)
	}
}

func TestReadAndListRequestsValidateActorAndProjectBoundaries(t *testing.T) {
	read := ReadRequest{ProjectID: "project:sales", ID: "exploration-1", ActorID: "owner"}
	if err := read.Validate(); err != nil {
		t.Fatalf("valid read request: %v", err)
	}
	read.ActorID = ""
	if err := read.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty read actor error = %v, want invalid", err)
	}
	list := ListRequest{ProjectID: "project:sales", ActorID: "owner"}
	if err := list.Validate(); err != nil {
		t.Fatalf("valid list request: %v", err)
	}
	list.ProjectID = "project with spaces"
	if err := list.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid list project error = %v, want invalid", err)
	}
}

func testEvidence(t *testing.T, action MutationAction) MutationEvidence {
	t.Helper()
	fingerprint, err := CanonicalFingerprint(struct {
		Action string `json:"action"`
		Value  string `json:"value"`
	}{Action: string(action), Value: "orders"})
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	evidence, err := NewMutationEvidence("owner", action, "request-1", fingerprint, "request-1", "correlation-1", time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	return evidence
}

func servingIdentity(projectID projectgraph.ResourceID) (projectgraph.ServingIdentity, error) {
	return projectgraph.NewServingIdentity(projectID, "production", "generation-1")
}
