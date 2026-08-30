package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
)

func canonicalSQLiteDocument(id string) document.DashboardDocument {
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: id, Name: "sales", DisplayName: stringPointer("Sales")}, Spec: document.DashboardSpec{SemanticModel: "model:sales", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
}

func canonicalSQLiteInput(t *testing.T, project, id, revisionID string, operation authoring.CreateOperation) (authoring.CreateInput, authoring.Revision) {
	t.Helper()
	actor := operation.ActorID
	if actor == "" {
		actor = "actor"
	}
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: actor, ConversationID: operation.ConversationID, ToolCallID: operation.ToolCallID}
	documentValue := canonicalSQLiteDocument(id)
	revision, err := authoring.NewRevision(authoring.RevisionID(revisionID), authoring.DashboardID(id), 1, time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC), documentValue, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: graph.ResourceID(project), ID: authoring.DashboardID(id), OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "model:sales", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: authoring.DraftID("draft-" + id), DashboardID: authoring.DashboardID(id), Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	return authoring.CreateInput{ProjectID: graph.ResourceID(project), Lifecycle: lifecycle, Revision: revision, Operation: operation}, revision
}

func stringPointer(value string) *string { return &value }

func servingIdentity(t *testing.T, project, environment, generation string) graph.ServingIdentity {
	t.Helper()
	identity, err := graph.NewServingIdentity(graph.ResourceID(project), environment, generation)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func TestCanonicalSQLiteRepositoryRoundTripCommandPublishUsage(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	operation := authoring.CreateOperation{ProjectID: "project:sales", ActorID: "actor", Kind: "create", IdempotencyKey: "create-1", ConversationID: "conversation", ToolCallID: "tool", Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	input, revision := canonicalSQLiteInput(t, "project:sales", "dashboard:sales", "revision-1", operation)
	created, err := repository.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.Get(ctx, "project:sales", "dashboard:sales")
	if err != nil || got.Draft == nil || got.Draft.Revision != revision.Token() || got.Title != "Sales" {
		t.Fatalf("lifecycle round trip = %#v (%v)", got, err)
	}
	storedRevision, err := repository.GetRevision(ctx, "project:sales", "dashboard:sales", "revision-1")
	if err != nil || storedRevision.Document.Metadata.ID != "dashboard:sales" {
		t.Fatalf("revision round trip = %#v (%v)", storedRevision, err)
	}
	operationResult, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found || operationResult.DashboardID != created.ID {
		t.Fatalf("create operation = %#v found=%v err=%v", operationResult, found, err)
	}
	documentValue := canonicalSQLiteDocument("dashboard:sales")
	documentValue.Metadata.DisplayName = stringPointer("Sales v2")
	revision2, err := authoring.NewRevision("revision-2", "dashboard:sales", 2, time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC), documentValue, revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	next := got
	next.Title = "Sales v2"
	next.Draft = &authoring.Draft{ID: got.Draft.ID, DashboardID: got.ID, Revision: revision2.Token(), Provenance: revision2.Provenance}
	evidence := authoring.CommandEvidence{ID: "edit-1", Fingerprint: "fingerprint-1", Action: authoring.AuthorizationActionEdit, Provenance: revision.Provenance, OccurredAt: revision2.CreatedAt}
	if _, err := repository.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: "project:sales", DashboardID: got.ID, ExpectedDraftRevision: revision.Token(), Revision: revision2, Next: next, Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	commandResult, found, err := repository.LookupCommandResult(ctx, "project:sales", got.ID, evidence)
	if err != nil || !found || commandResult.Revision != revision2.Token() {
		t.Fatalf("command evidence = %#v found=%v err=%v", commandResult, found, err)
	}
	changedEvidence := evidence
	changedEvidence.Fingerprint = "different"
	if _, _, err := repository.LookupCommandResult(ctx, "project:sales", got.ID, changedEvidence); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("command reuse error = %v", err)
	}
	if _, err := repository.AppendDraft(ctx, authoring.AppendDraftInput{ProjectID: "project:sales", DashboardID: got.ID, ExpectedDraftRevision: revision.Token(), Revision: revision2, Next: next, Evidence: authoring.CommandEvidence{ID: "stale", Fingerprint: "stale", Action: authoring.AuthorizationActionEdit, Provenance: revision.Provenance, OccurredAt: revision2.CreatedAt}}); !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("stale append error = %v", err)
	}
	identity := servingIdentity(t, "project:sales", "test", "generation-1")
	compiledDefinition := dashboarddefinition.Definition{ID: "dashboard:sales", Title: "Sales v2", SemanticModel: "model:sales", Visualizations: map[string]visualizationdefinition.Definition{}}
	compiled, err := authoring.NewCompiledRevision("project:sales", "dashboard:sales", revision2.Token(), compiledDefinition, identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	publishEvidence := authoring.CommandEvidence{ID: "publish-1", Fingerprint: "publish-fingerprint", Action: authoring.AuthorizationActionPublish, Provenance: revision.Provenance, OccurredAt: compiled.CompiledAt}
	published, err := repository.Publish(ctx, authoring.PublishInput{ProjectID: "project:sales", DashboardID: got.ID, ExpectedDraftRevision: revision2.Token(), Published: authoring.Published{Revision: revision2.Token(), Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: revision.Provenance}, Compilation: compiled, Evidence: publishEvidence})
	if err != nil || published.Status != authoring.LifecycleStatusPublished {
		t.Fatalf("publish = %#v (%v)", published, err)
	}
	storedCompiled, err := repository.GetPublishedCompilation(ctx, "project:sales", got.ID)
	if err != nil || storedCompiled.DefinitionHash != compiled.DefinitionHash {
		t.Fatalf("compiled round trip = %#v (%v)", storedCompiled, err)
	}
	usage, err := repository.CountBySemanticModel(ctx, "project:sales")
	if err != nil || len(usage) != 1 || usage[0].Total != 1 {
		t.Fatalf("semantic usage = %#v (%v)", usage, err)
	}
}

func TestCanonicalSQLiteRepositoryRevalidationFailureRetainsPublishedEvidence(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "revalidation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	input, revision := canonicalSQLiteInput(t, "project:sales", "dashboard:revalidate", "revision-1", authoring.CreateOperation{})
	if _, err := repository.Create(ctx, input); err != nil {
		t.Fatal(err)
	}
	identity := servingIdentity(t, "project:sales", "test", "generation-1")
	definition := dashboarddefinition.Definition{ID: "dashboard:revalidate", Title: "Sales", SemanticModel: "model:sales"}
	compiled, err := authoring.NewCompiledRevision("project:sales", "dashboard:revalidate", revision.Token(), definition, identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Publish(ctx, authoring.PublishInput{ProjectID: "project:sales", DashboardID: "dashboard:revalidate", ExpectedDraftRevision: revision.Token(), Published: authoring.Published{Revision: revision.Token(), Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: revision.Provenance}, Compilation: compiled, Evidence: authoring.CommandEvidence{ID: "publish", Fingerprint: "publish", Action: authoring.AuthorizationActionPublish, Provenance: revision.Provenance, OccurredAt: compiled.CompiledAt}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status) VALUES ('generation-1', 'project:sales', 'test', 'active')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, generation_id) VALUES ('project:sales', 'test', 'generation-1')`); err != nil {
		t.Fatal(err)
	}
	projectGraph, err := graph.NewProjectGraph([]graph.Resource{{ID: "project:sales", Kind: graph.KindProject, Name: "sales-project"}, {ID: "dashboard:revalidate", Kind: graph.KindDashboard, Name: "revalidate-dashboard"}, {ID: "model:sales", Kind: graph.KindSemanticModel, Name: "sales-model"}}, []graph.Edge{{From: "dashboard:revalidate", To: "model:sales"}})
	if err != nil {
		t.Fatal(err)
	}
	authorizationSnapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, projectGraph, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	generation := authoring.RevalidationGeneration{Identity: identity, Graph: projectGraph, Authorization: authorizationSnapshot, ChangedIDs: []graph.ResourceID{"model:sales"}}
	lifecycle, err := repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil {
		t.Fatal(err)
	}
	failure := authoring.RevalidationFailure{Identity: identity, DependencyIDs: []graph.ResourceID{"model:sales"}, Code: "INVALID_DEPENDENCY", Message: "model changed", FailedAt: time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)}
	if err := repository.RecordRevalidationFailure(ctx, authoring.RevalidationFailureInput{AttemptID: "018f4f2e-0000-7000-8000-000000000013", Generation: generation, Dashboard: lifecycle, AuthoredRevision: revision, PriorCompilation: compiled.Token(), DependencyIDs: failure.DependencyIDs, Failure: failure}); err != nil {
		t.Fatal(err)
	}
	got, err := repository.Get(ctx, "project:sales", "dashboard:revalidate")
	if err != nil || got.Revalidation == nil || got.Revalidation.Code != failure.Code {
		t.Fatalf("revalidation failure projection = %#v (%v)", got.Revalidation, err)
	}
	retained, err := repository.GetPublishedCompilation(ctx, "project:sales", "dashboard:revalidate")
	if err != nil || retained.SemanticIdentity != identity {
		t.Fatalf("published evidence changed after failure = %#v (%v)", retained, err)
	}
}
