package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/project/graph"
)

type fakeAuthorizer struct {
	requests   []service.AuthorizationRequest
	err        error
	denyAction authoring.AuthorizationAction
}

func (a *fakeAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	if a.denyAction != "" {
		if request.Action != a.denyAction {
			return nil
		}
		if a.err == nil {
			return errors.New("authorization denied")
		}
	}
	return a.err
}

type fakeCompiler struct {
	calls             int
	err               error
	semanticState     string
	invalidDefinition bool
}

func (c *fakeCompiler) Compile(_ context.Context, _ graph.ResourceID, _ graph.ResourceID, document authoring.Dashboard) (service.Compilation, error) {
	c.calls++
	if c.err != nil {
		return service.Compilation{}, c.err
	}
	id, semantic := document.ID, document.SemanticModel
	if c.invalidDefinition {
		id = "other"
	}
	state := c.semanticState
	if state == "" {
		state = "state-1"
	}
	return service.Compilation{Definition: dashboarddefinition.Definition{ID: id.String(), SemanticModel: semantic.String(), Title: document.Title, Pages: document.Pages, Visualizations: map[string]visualizationdefinition.Definition{}}, SemanticServingStateID: state}, nil
}

type fakeRepository struct {
	lifecycle authoring.DashboardLifecycle
	revisions map[authoring.RevisionID]authoring.Revision
	commands  map[authoring.CommandID]struct {
		fingerprint string
		token       authoring.RevisionToken
	}
	publishCalls int
	archiveCalls int
	createCalls  int
	created      []authoring.CreateInput
	lastPublish  authoring.PublishInput
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{revisions: map[authoring.RevisionID]authoring.Revision{}, commands: map[authoring.CommandID]struct {
		fingerprint string
		token       authoring.RevisionToken
	}{}}
}

func (r *fakeRepository) Create(_ context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	r.createCalls++
	r.created = append(r.created, input)
	r.lifecycle = input.Lifecycle
	r.revisions[input.Revision.ID] = input.Revision
	return r.lifecycle, nil
}
func (r *fakeRepository) Get(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *fakeRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	return []authoring.DashboardLifecycle{r.lifecycle}, nil
}
func (r *fakeRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *fakeRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *fakeRepository) LookupCommandResult(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	result, ok := r.commands[evidence.ID]
	if !ok {
		return authoring.CommandResult{}, false, nil
	}
	if result.fingerprint != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	return authoring.CommandResult{Revision: result.token}, true, nil
}
func (r *fakeRepository) AppendDraft(_ context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.Revision{}, errors.Join(authoring.ErrConflict, authoring.ErrStaleRevision)
	}
	r.lifecycle = input.Next
	r.revisions[input.Revision.ID] = input.Revision
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{fingerprint: input.Evidence.Fingerprint, token: input.Revision.Token()}
	return input.Revision, nil
}
func (r *fakeRepository) Publish(_ context.Context, input authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	r.publishCalls++
	r.lastPublish = input
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.DashboardLifecycle{}, errors.Join(authoring.ErrConflict, authoring.ErrStaleRevision)
	}
	r.lifecycle.Status = authoring.LifecycleStatusPublished
	r.lifecycle.Published = &input.Published
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{fingerprint: input.Evidence.Fingerprint, token: input.Published.Revision}
	return r.lifecycle, nil
}
func (r *fakeRepository) Archive(_ context.Context, input authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	r.archiveCalls++
	if current := currentToken(r.lifecycle); current != input.ExpectedCurrentRevision {
		return authoring.DashboardLifecycle{}, errors.Join(authoring.ErrConflict, authoring.ErrStaleRevision)
	}
	r.lifecycle.Status = authoring.LifecycleStatusArchived
	r.commands[input.Evidence.ID] = struct {
		fingerprint string
		token       authoring.RevisionToken
	}{fingerprint: input.Evidence.Fingerprint, token: input.ExpectedCurrentRevision}
	return r.lifecycle, nil
}

func (r *fakeRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	return authoring.CompiledRevision{}, authoring.ErrNotFound
}

func currentToken(lifecycle authoring.DashboardLifecycle) authoring.RevisionToken {
	if lifecycle.Draft != nil {
		return lifecycle.Draft.Revision
	}
	if lifecycle.Published != nil {
		return lifecycle.Published.Revision
	}
	return authoring.RevisionToken{}
}

func newService(t *testing.T, repository authoring.Repository, auth *fakeAuthorizer, compiler *fakeCompiler) *service.Service {
	t.Helper()
	ids := []string{"dash-1", "draft-1", "rev-1", "rev-2", "rev-3"}
	next := func() (string, error) {
		value := ids[0]
		ids = ids[1:]
		return value, nil
	}
	createdAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	svc, err := service.NewService(service.Options{
		Repository: repository, Authorizer: auth, Compiler: compiler, Now: func() time.Time { return createdAt },
		NewDashboardID: func() (authoring.DashboardID, error) { value, err := next(); return authoring.DashboardID(value), err },
		NewDraftID:     func() (authoring.DraftID, error) { value, err := next(); return authoring.DraftID(value), err },
		NewRevisionID:  func() (authoring.RevisionID, error) { value, err := next(); return authoring.RevisionID(value), err },
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func create(t *testing.T, svc *service.Service) service.Result {
	t.Helper()
	result, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func editCommand(result service.Result, id authoring.CommandID, title string) authoring.Command {
	return authoring.Command{ID: id, DashboardID: result.Lifecycle.ID, DraftID: result.Lifecycle.Draft.ID, ExpectedRevision: result.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Metadata: &authoring.MetadataPatch{Title: &title}}
}

func TestCreateEditReplayPublishArchiveAndFailures(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	svc := newService(t, repository, auth, compiler)
	created := create(t, svc)
	if created.Lifecycle.Visibility != authoring.VisibilityPrivate || len(created.Lifecycle.Draft.Revision.RevisionID) == 0 || len(created.Lifecycle.Draft.Revision.ContentHash) == 0 {
		t.Fatalf("created = %#v", created)
	}
	page := repository.revisions[created.Revision.RevisionID].Document.Pages[0]
	if page.ID != "overview" || page.Title != "Overview" || page.Canvas.Width != 1366 || page.Grid.Columns != 12 {
		t.Fatalf("default page = %#v", page)
	}
	first := editCommand(created, "edit-1", "Orders v2")
	edited, err := svc.Execute(t.Context(), "project", first)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision.Number != 2 || edited.Lifecycle.Title != "Orders v2" {
		t.Fatalf("edited = %#v", edited)
	}
	replayed, err := svc.Execute(t.Context(), "project", first)
	if err != nil || replayed.Revision != edited.Revision {
		t.Fatalf("replayed = %#v, err = %v", replayed, err)
	}
	if compiler.calls != 0 {
		t.Fatalf("edit/replay invoked strict compiler %d times", compiler.calls)
	}
	second := editCommand(edited, "edit-2", "Orders v3")
	later, err := svc.Execute(t.Context(), "project", second)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err = svc.Execute(t.Context(), "project", first)
	if err != nil || replayed.Revision != edited.Revision || replayed.Lifecycle.Draft.Revision != later.Revision {
		t.Fatalf("replay after later edit = %#v, err = %v", replayed, err)
	}
	changed := first
	changed.Metadata.Title = stringPtr("different")
	if _, err := svc.Execute(t.Context(), "project", changed); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("command reuse error = %v", err)
	}

	compiler.err = errors.New("strict compile failed")
	publish := authoring.Command{ID: "publish-1", DashboardID: later.Lifecycle.ID, DraftID: later.Lifecycle.Draft.ID, ExpectedRevision: later.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}
	if _, err := svc.Execute(t.Context(), "project", publish); err == nil || repository.publishCalls != 0 {
		t.Fatalf("compile failure err=%v publishCalls=%d", err, repository.publishCalls)
	}
	compiler.err = nil
	published, err := svc.Execute(t.Context(), "project", publish)
	if err != nil || published.Lifecycle.Status != authoring.LifecycleStatusPublished || repository.publishCalls != 1 {
		t.Fatalf("published = %#v, err = %v", published, err)
	}
	if repository.lastPublish.Compilation.Definition.ID != string(later.Lifecycle.ID) || repository.lastPublish.Compilation.SemanticServingStateID != "state-1" || repository.lastPublish.Published.Compilation != repository.lastPublish.Compilation.Token() {
		t.Fatalf("compiled publication input = %#v", repository.lastPublish)
	}
	archive := authoring.Command{ID: "archive-1", DashboardID: later.Lifecycle.ID, ExpectedRevision: published.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Archive: &authoring.ArchivePayload{}}
	archived, err := svc.Execute(t.Context(), "project", archive)
	if err != nil || archived.Lifecycle.Status != authoring.LifecycleStatusArchived || repository.archiveCalls != 1 {
		t.Fatalf("archived = %#v, err = %v", archived, err)
	}
	if compiler.calls != 2 {
		t.Fatalf("archive/retries changed compiler calls = %d", compiler.calls)
	}
	if len(auth.requests) != 9 || auth.requests[0].Action != authoring.AuthorizationActionEdit || auth.requests[len(auth.requests)-1].Action != authoring.AuthorizationActionArchive {
		t.Fatalf("authorization requests = %#v", auth.requests)
	}
}

func TestCreateAuthorizationDenialAndStaleConflict(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{err: errors.New("denied")}, &fakeCompiler{}
	svc := newService(t, repository, auth, compiler)
	if _, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders", SemanticModel: "sales"}); err == nil || repository.lifecycle.ID != "" {
		t.Fatalf("denied create err=%v lifecycle=%#v", err, repository.lifecycle)
	}
	repository, auth, compiler = newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	svc = newService(t, repository, auth, compiler)
	created := create(t, svc)
	stale := editCommand(created, "edit-stale", "stale")
	stale.ExpectedRevision.Number++
	if _, err := svc.Execute(t.Context(), "project", stale); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale edit error = %v", err)
	}
}

func TestPublishRejectsInvalidCompilerResultBeforeRepository(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{semanticState: ""}
	svc := newService(t, repository, auth, compiler)
	created := create(t, svc)
	command := authoring.Command{ID: "publish-invalid", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID, ExpectedRevision: created.Revision, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Publish: &authoring.PublishPayload{}}
	compiler.invalidDefinition = true
	if _, err := svc.Execute(t.Context(), "project", command); err == nil || repository.publishCalls != 0 {
		t.Fatalf("invalid compiler result err=%v publishCalls=%d", err, repository.publishCalls)
	}
}

func TestDeterministicCreateClockAndIDs(t *testing.T) {
	repository, auth, compiler := newFakeRepository(), &fakeAuthorizer{}, &fakeCompiler{}
	svc := newService(t, repository, auth, compiler)
	created := create(t, svc)
	if created.Lifecycle.ID != "dash-1" || created.Lifecycle.Draft.ID != "draft-1" || created.Revision.RevisionID != "rev-1" || repository.revisions[created.Revision.RevisionID].CreatedAt != time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC) {
		t.Fatalf("deterministic create = %#v", created)
	}
}

func stringPtr(value string) *string { return &value }
