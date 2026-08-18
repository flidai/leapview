package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/project/graph"
)

type canonicalAuthorizer struct {
	denied bool
	calls  []service.AuthorizationRequest
}

func (a *canonicalAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	a.calls = append(a.calls, request)
	if a.denied {
		return errors.New("denied")
	}
	return nil
}

type canonicalCompiler struct {
	calls int
}

func (c *canonicalCompiler) Compile(_ context.Context, projectID, _ graph.ResourceID, authored document.DashboardDocument) (service.Compilation, error) {
	c.calls++
	title := authored.Metadata.Name
	if authored.Metadata.DisplayName != nil {
		title = *authored.Metadata.DisplayName
	}
	identity, _ := graph.NewServingIdentity(projectID, "test", "generation-test")
	return service.Compilation{Definition: dashboarddefinition.Definition{ID: authored.Metadata.ID, Title: title, SemanticModel: authored.Spec.SemanticModel, Visualizations: map[string]visualizationdefinition.Definition{}}, SemanticIdentity: identity}, nil
}

type canonicalRepository struct {
	lifecycle authoring.DashboardLifecycle
	revisions map[authoring.RevisionID]authoring.Revision
	commands  map[authoring.CommandID]struct {
		fingerprint string
		token       authoring.RevisionToken
	}
	operations           map[string]authoring.CreateOperationResult
	compiled             authoring.CompiledRevision
	createCalls          int
	getCalls             int
	getRevisionCalls     int
	lookupOperationCalls int
}

func newCanonicalRepository() *canonicalRepository {
	return &canonicalRepository{revisions: map[authoring.RevisionID]authoring.Revision{}, commands: map[authoring.CommandID]struct {
		fingerprint string
		token       authoring.RevisionToken
	}{}, operations: map[string]authoring.CreateOperationResult{}}
}

func operationKey(value authoring.CreateOperation) string {
	return value.ProjectID.String() + "|" + value.ActorID + "|" + value.Kind + "|" + value.IdempotencyKey
}

func (r *canonicalRepository) Create(_ context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	r.createCalls++
	if input.Operation.Enabled() {
		key := operationKey(input.Operation)
		if prior, ok := r.operations[key]; ok {
			if prior.Fingerprint != input.Operation.Fingerprint {
				return authoring.DashboardLifecycle{}, authoring.ErrCommandReuse
			}
			return r.lifecycle, nil
		}
		r.operations[key] = authoring.CreateOperationResult{DashboardID: input.Lifecycle.ID, Revision: input.Revision.Token(), Fingerprint: input.Operation.Fingerprint}
	}
	r.lifecycle = input.Lifecycle
	r.revisions[input.Revision.ID] = input.Revision
	return r.lifecycle, nil
}

func (r *canonicalRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	r.getCalls++
	if r.lifecycle.ID == "" {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return r.lifecycle, nil
}
func (r *canonicalRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	return []authoring.DashboardLifecycle{r.lifecycle}, nil
}
func (r *canonicalRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *canonicalRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionCalls++
	value, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return value, nil
}
func (r *canonicalRepository) LookupCreateOperation(_ context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	r.lookupOperationCalls++
	value, ok := r.operations[operationKey(operation)]
	return value, ok, nil
}
func (r *canonicalRepository) LookupCommandResult(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	value, ok := r.commands[evidence.ID]
	if !ok {
		return authoring.CommandResult{}, false, nil
	}
	if value.fingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	return authoring.CommandResult{Revision: value.token}, true, nil
}
func (r *canonicalRepository) AppendDraft(_ context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.Revision{}, authoring.ErrStaleRevision
	}
	r.lifecycle = input.Next
	r.revisions[input.Revision.ID] = input.Revision
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{input.Evidence.Fingerprint, input.Revision.Token()}
	return input.Revision, nil
}
func (r *canonicalRepository) Publish(_ context.Context, input authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.DashboardLifecycle{}, authoring.ErrStaleRevision
	}
	r.lifecycle.Status = authoring.LifecycleStatusPublished
	r.lifecycle.Published = &input.Published
	r.compiled = input.Compilation
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{input.Evidence.Fingerprint, input.Published.Revision}
	return r.lifecycle, nil
}
func (r *canonicalRepository) Archive(_ context.Context, input authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	if current := currentLifecycleToken(r.lifecycle); current != input.ExpectedCurrentRevision {
		return authoring.DashboardLifecycle{}, authoring.ErrStaleRevision
	}
	r.lifecycle.Status = authoring.LifecycleStatusArchived
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{input.Evidence.Fingerprint, input.ExpectedCurrentRevision}
	return r.lifecycle, nil
}
func (r *canonicalRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	if r.compiled.DashboardID == "" {
		return authoring.CompiledRevision{}, authoring.ErrNotFound
	}
	return r.compiled, nil
}

func currentLifecycleToken(value authoring.DashboardLifecycle) authoring.RevisionToken {
	if value.Draft != nil {
		return value.Draft.Revision
	}
	if value.Published != nil {
		return value.Published.Revision
	}
	return authoring.RevisionToken{}
}

func newCanonicalService(t *testing.T, repository *canonicalRepository, authorizer *canonicalAuthorizer, compiler *canonicalCompiler, ids ...string) *service.Service {
	t.Helper()
	index := 0
	next := func() (string, error) {
		if index >= len(ids) {
			return "", errors.New("id generator exhausted")
		}
		value := ids[index]
		index++
		return value, nil
	}
	svc, err := service.NewService(service.Options{Repository: repository, Authorizer: authorizer, Compiler: compiler, Now: func() time.Time { return time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC) }, NewDashboardID: func() (authoring.DashboardID, error) { value, err := next(); return authoring.DashboardID(value), err }, NewDraftID: func() (authoring.DraftID, error) { value, err := next(); return authoring.DraftID(value), err }, NewRevisionID: func() (authoring.RevisionID, error) { value, err := next(); return authoring.RevisionID(value), err }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestCanonicalServiceCreateEditPublishArchiveAndAuthorization(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-created", "draft-created", "revision-created", "revision-edited")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-1"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Lifecycle.Status != authoring.LifecycleStatusDraft || created.Lifecycle.Draft == nil || created.Revision.Number != 1 {
		t.Fatalf("created result = %#v", created)
	}
	title := "Orders v2"
	command := authoring.Command{ID: "edit-1", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID, ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Metadata: &authoring.MetadataPatch{Title: &title}}
	edited, err := svc.Execute(t.Context(), "project:test", command)
	if err != nil || edited.Revision.Number != 2 || edited.Lifecycle.Title != title {
		t.Fatalf("edited result = %#v (%v)", edited, err)
	}
	if replay, err := svc.Execute(t.Context(), "project:test", command); err != nil || replay.Revision != edited.Revision {
		t.Fatalf("edit replay = %#v (%v)", replay, err)
	}
	stale := command
	stale.ID = "edit-stale"
	if _, err := svc.Execute(t.Context(), "project:test", stale); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale edit error = %v", err)
	}
	publish := authoring.Command{ID: "publish-1", DashboardID: edited.Lifecycle.ID, DraftID: edited.Lifecycle.Draft.ID, ExpectedRevision: edited.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}
	published, err := svc.Execute(t.Context(), "project:test", publish)
	if err != nil || published.Lifecycle.Status != authoring.LifecycleStatusPublished || compiler.calls != 1 {
		t.Fatalf("published result = %#v (%v), compiler calls=%d", published, err, compiler.calls)
	}
	archive := authoring.Command{ID: "archive-1", DashboardID: published.Lifecycle.ID, ExpectedRevision: published.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Archive: &authoring.ArchivePayload{}}
	archived, err := svc.Execute(t.Context(), "project:test", archive)
	if err != nil || archived.Lifecycle.Status != authoring.LifecycleStatusArchived {
		t.Fatalf("archived result = %#v (%v)", archived, err)
	}
	if len(authorizer.calls) < 4 || authorizer.calls[0].Action != authoring.AuthorizationActionEdit || authorizer.calls[len(authorizer.calls)-1].Action != authoring.AuthorizationActionArchive {
		t.Fatalf("authorization calls = %#v", authorizer.calls)
	}
}

func TestCanonicalServiceCreateIdempotencyAndFork(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-source", "draft-source", "revision-source", "revision-edited", "dashboard-fork", "draft-fork", "revision-fork")
	request := service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Source", Slug: "source", SemanticModel: "model:test", Origin: authoring.OriginAgent, IdempotencyKey: "retry-create"}
	first, err := svc.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Create(t.Context(), request)
	if err != nil || replay.Lifecycle.ID != first.Lifecycle.ID || repository.createCalls != 1 {
		t.Fatalf("create replay = %#v (%v), calls=%d", replay, err, repository.createCalls)
	}
	changed := request
	changed.Title = "Changed"
	if _, err := svc.Create(t.Context(), changed); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed create replay error = %v", err)
	}
	title := "Published"
	edit := authoring.Command{ID: "edit-source", DashboardID: first.Lifecycle.ID, DraftID: first.Lifecycle.Draft.ID, ExpectedRevision: first.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Metadata: &authoring.MetadataPatch{Title: &title}}
	updated, err := svc.Execute(t.Context(), "project:test", edit)
	if err != nil {
		t.Fatal(err)
	}
	publish := authoring.Command{ID: "publish-source", DashboardID: updated.Lifecycle.ID, DraftID: updated.Lifecycle.Draft.ID, ExpectedRevision: updated.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}
	if _, err := svc.Execute(t.Context(), "project:test", publish); err != nil {
		t.Fatal(err)
	}
	forked, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project:test", SourceDashboardID: first.Lifecycle.ID, ActorID: "actor", OwnerPrincipalID: "fork-owner", Title: "Forked", Slug: "forked", Origin: authoring.OriginUI, IdempotencyKey: "retry-fork"})
	if err != nil {
		t.Fatal(err)
	}
	if forked.Lifecycle.ID != "dashboard-fork" || forked.Lifecycle.Status != authoring.LifecycleStatusDraft || forked.Lifecycle.Draft == nil {
		t.Fatalf("forked result = %#v", forked)
	}
	if got := repository.revisions[first.Revision.RevisionID].CreatedAt; !got.Equal(time.Date(2026, 8, 18, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("deterministic create clock = %v", got)
	}
	forkRevision := repository.revisions[forked.Revision.RevisionID]
	forkRevision.Document.Spec.Filters = append(forkRevision.Document.Spec.Filters, document.DashboardFilter{ID: "region", Label: "Region", Dimension: "region"})
	forkRevision.Provenance.ForkedFrom.Instance.SourceRevision.Number = 99
	repository.revisions[forked.Revision.RevisionID] = forkRevision
	if len(repository.revisions[first.Revision.RevisionID].Document.Spec.Filters) != 0 {
		t.Fatal("fork document aliases source document")
	}
	if repository.revisions[first.Revision.RevisionID].Provenance.ForkedFrom != nil {
		t.Fatal("source provenance unexpectedly received fork evidence")
	}
	if repository.revisions[forked.Revision.RevisionID].Provenance.ForkedFrom.Instance.SourceRevision.Number != 99 {
		t.Fatal("fork provenance mutation did not stay local")
	}
}

func TestCanonicalServiceIdempotencyAuthorizesBeforeReuseDisclosure(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-created", "draft-created", "revision-created")
	request := service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", SemanticModel: "model:test", IdempotencyKey: "retry-auth"}
	if _, err := svc.Create(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.Title = "Changed"
	authorizer.denied = true
	if _, err := svc.Create(t.Context(), changed); err == nil || err.Error() != "denied" {
		t.Fatalf("unauthorized reused key error = %v", err)
	}
	if repository.lookupOperationCalls == 0 || len(authorizer.calls) == 0 || authorizer.calls[len(authorizer.calls)-1].Action != authoring.AuthorizationActionEdit {
		t.Fatalf("authorization/reuse ordering calls=%d auth=%#v", repository.lookupOperationCalls, authorizer.calls)
	}
}

func TestCanonicalServiceForkRejectsIncompleteSourceAndInvalidClock(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-source", "draft-source", "revision-source", "revision-edited")
	if _, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project:test", SourceDashboardID: "missing", ActorID: "actor", IdempotencyKey: "fork-missing"}); !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("missing source error = %v", err)
	}
	invalidClock, err := service.NewService(service.Options{Repository: repository, Authorizer: authorizer, Compiler: compiler, Now: func() time.Time { return time.Time{} }, NewDashboardID: func() (authoring.DashboardID, error) { return "dashboard-fork", nil }, NewDraftID: func() (authoring.DraftID, error) { return "draft-fork", nil }, NewRevisionID: func() (authoring.RevisionID, error) { return "revision-fork", nil }})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Source", Slug: "source", SemanticModel: "model:test", IdempotencyKey: "create-source"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Published"
	edit := authoring.Command{ID: "edit-source", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID, ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Metadata: &authoring.MetadataPatch{Title: &title}}
	updated, err := svc.Execute(t.Context(), "project:test", edit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(t.Context(), "project:test", authoring.Command{ID: "publish-source", DashboardID: updated.Lifecycle.ID, DraftID: updated.Lifecycle.Draft.ID, ExpectedRevision: updated.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := invalidClock.Fork(t.Context(), service.ForkRequest{ProjectID: "project:test", SourceDashboardID: created.Lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-invalid-clock"}); err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("invalid fork clock error = %v", err)
	}
}

func TestCanonicalServiceForkAuthorizesBeforeRevisionDisclosure(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-source", "draft-source", "revision-source", "revision-edited")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Source", Slug: "source", SemanticModel: "model:test", IdempotencyKey: "create-source"})
	if err != nil {
		t.Fatal(err)
	}
	title := "Published"
	updated, err := svc.Execute(t.Context(), "project:test", authoring.Command{ID: "edit-source", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID, ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Metadata: &authoring.MetadataPatch{Title: &title}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(t.Context(), "project:test", authoring.Command{ID: "publish-source", DashboardID: updated.Lifecycle.ID, DraftID: updated.Lifecycle.Draft.ID, ExpectedRevision: updated.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}); err != nil {
		t.Fatal(err)
	}
	repository.getRevisionCalls = 0
	authorizer.calls = nil
	authorizer.denied = true
	if _, err := svc.Fork(t.Context(), service.ForkRequest{ProjectID: "project:test", SourceDashboardID: created.Lifecycle.ID, ActorID: "actor", IdempotencyKey: "fork-denied"}); err == nil || err.Error() != "denied" {
		t.Fatalf("source authorization error = %v", err)
	}
	if repository.getRevisionCalls != 0 {
		t.Fatalf("source revision was disclosed before authorization: reads=%d", repository.getRevisionCalls)
	}
}
