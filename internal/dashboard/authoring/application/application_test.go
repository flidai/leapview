package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	dashboardcatalog "github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestNewRequiresAllCompositionPorts(t *testing.T) {
	repo := &applicationRepository{}
	auth := &applicationAuthorizer{}
	svc := newApplicationService(t, repo, auth)
	valid := application.Options{Authoring: svc, Repository: repo, Authorizer: auth, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return nil, nil }}
	for name, mutate := range map[string]func(*application.Options){"authoring": func(o *application.Options) { o.Authoring = nil }, "repository": func(o *application.Options) { o.Repository = nil }, "authorizer": func(o *application.Options) { o.Authorizer = nil }, "runtime": func(o *application.Options) { o.AcquireRuntime = nil }} {
		t.Run(name, func(t *testing.T) {
			options := valid
			mutate(&options)
			if _, err := application.New(options); err == nil {
				t.Fatal("New succeeded with missing composition port")
			}
		})
	}
}

func TestCatalogOperationsNormalizeProjectAndReleaseOneLeaseEach(t *testing.T) {
	repo, auth := &applicationRepository{}, &applicationAuthorizer{}
	svc := newApplicationService(t, repo, auth)
	projects := []string{"sales", "finance"}
	leases := []*applicationLease{}
	app, err := application.New(application.Options{Authoring: svc, Repository: repo, Authorizer: auth, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) {
		if len(projects) == 0 {
			return nil, errors.New("unexpected acquire")
		}
		project := projects[0]
		projects = projects[1:]
		identity, _ := graph.NewServingIdentity(graph.ResourceID(project), "production", "state-"+project)
		lease := &applicationLease{runtime: applicationRuntime{catalog: dashboardcatalog.Catalog{Dashboards: []dashboardcatalog.Dashboard{{ID: graph.ResourceID(project + "-sales"), Title: project + " sales", SemanticModel: "sales"}}}}, identity: identity}
		leases = append(leases, lease)
		return lease, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := app.List(t.Context(), catalog.ListRequest{ProjectID: "sales", ActorID: "actor"})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Items[0].ProjectID != "sales" {
		t.Fatalf("list = %#v", listed)
	}
	got, err := app.Get(t.Context(), catalog.GetRequest{ProjectID: "finance", ActorID: "actor", DashboardID: "finance-sales"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "finance" || got.ID != "finance-sales" || len(leases) != 2 || leases[0].releases != 1 || leases[1].releases != 1 {
		t.Fatalf("get/leases = %#v/%#v", got, leases)
	}
}

func newApplicationService(t *testing.T, repo *applicationRepository, auth *applicationAuthorizer) *service.Service {
	t.Helper()
	svc, err := service.NewService(service.Options{Repository: repo, Authorizer: auth, Compiler: applicationCompiler{}, Now: func() time.Time { return time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC) }, NewDashboardID: func() (authoring.DashboardID, error) { return "dashboard", nil }, NewDraftID: func() (authoring.DraftID, error) { return "draft", nil }, NewRevisionID: func() (authoring.RevisionID, error) { return "revision", nil }})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type applicationAuthorizer struct{}

func (*applicationAuthorizer) Authorize(context.Context, service.AuthorizationRequest) error {
	return nil
}

type applicationCompiler struct{}

func (applicationCompiler) Compile(context.Context, graph.ResourceID, graph.ResourceID, document.DashboardDocument) (service.Compilation, error) {
	return service.Compilation{Definition: definition.Definition{ID: "dashboard", SemanticModel: "sales"}}, nil
}

type applicationRepository struct {
	lifecycle    authoring.DashboardLifecycle
	revisions    map[authoring.RevisionID]authoring.Revision
	commands     map[authoring.CommandID]authoring.CommandResult
	fingerprints map[authoring.CommandID]string
	appendCalls  int
}

func (*applicationRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *applicationRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if r.lifecycle.ID == "" {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return r.lifecycle, nil
}
func (*applicationRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	return nil, nil
}
func (*applicationRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	return nil, nil
}
func (r *applicationRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *applicationRepository) LookupCommandResult(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, evidence authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	result, ok := r.commands[evidence.ID]
	if !ok {
		return authoring.CommandResult{}, false, nil
	}
	if r.fingerprints[evidence.ID] != evidence.Fingerprint {
		return authoring.CommandResult{}, false, authoring.ErrCommandReuse
	}
	return result, true, nil
}
func (*applicationRepository) LookupCreateOperation(context.Context, authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	return authoring.CreateOperationResult{}, false, nil
}
func (r *applicationRepository) AppendDraft(_ context.Context, input authoring.AppendDraftInput) (authoring.Revision, error) {
	if r.lifecycle.Draft == nil || r.lifecycle.Draft.Revision != input.ExpectedDraftRevision {
		return authoring.Revision{}, authoring.ErrStaleRevision
	}
	r.appendCalls++
	r.lifecycle = input.Next
	if r.revisions == nil {
		r.revisions = map[authoring.RevisionID]authoring.Revision{}
	}
	r.revisions[input.Revision.ID] = input.Revision
	if r.commands == nil {
		r.commands = map[authoring.CommandID]authoring.CommandResult{}
	}
	if r.fingerprints == nil {
		r.fingerprints = map[authoring.CommandID]string{}
	}
	r.commands[input.Evidence.ID] = authoring.CommandResult{Revision: input.Revision.Token()}
	r.fingerprints[input.Evidence.ID] = input.Evidence.Fingerprint
	return input.Revision, nil
}
func (*applicationRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (*applicationRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (*applicationRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	return authoring.CompiledRevision{}, authoring.ErrNotFound
}

type applicationRuntime struct{ catalog dashboardcatalog.Catalog }

func (applicationRuntime) Close() error                        { return nil }
func (r applicationRuntime) Catalog() dashboardcatalog.Catalog { return r.catalog }

type applicationLease struct {
	runtime  projectruntime.Runtime
	identity graph.ServingIdentity
	releases int
}

func (l *applicationLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *applicationLease) Identity() graph.ServingIdentity { return l.identity }
func (l *applicationLease) Release()                        { l.releases++ }

var _ sourceadapter.AcquireRuntime = (func(context.Context) (projectruntime.Lease, error))(nil)
