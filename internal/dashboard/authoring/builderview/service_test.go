package builderview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type builderFixture struct {
	service    *Service
	repository *builderRepository
	authorizer *builderAuthorizer
	provider   *builderProvider
	lease      *builderLease
	runtime    *builderRuntime
	revision   authoring.Revision
}

func newBuilderFixture(t *testing.T) *builderFixture {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("revision-1", "sales", 7, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), builderDocument(), provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "draft-1", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &builderRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision}}
	auth := &builderAuthorizer{errors: map[authoring.AuthorizationAction]error{}}
	runtime := &builderRuntime{modelID: "sales_model", model: builderModel()}
	identity, _ := graph.NewServingIdentity("project", "production", "state-1")
	lease := &builderLease{runtime: runtime, identity: identity}
	provider := &builderProvider{lease: lease}
	svc, err := NewService(Options{Provider: provider, Repository: repo, Authorizer: auth})
	if err != nil {
		t.Fatal(err)
	}
	return &builderFixture{service: svc, repository: repo, authorizer: auth, provider: provider, lease: lease, runtime: runtime, revision: revision}
}

func builderDocument() document.DashboardDocument {
	status, metric := "status", "order_count"
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeBar,
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &status}}, Metrics: []document.DashboardMetricSelection{{String: &metric}},
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
	}
	base := document.DashboardPageComponentBase{ID: "orders-placement", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 4, RowSpan: 4}}
	return document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "sales", Name: "sales", DisplayName: builderStringPtr("Sales")},
		Spec: document.DashboardSpec{
			SemanticModel: "sales_model", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{"orders": visual},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: base, Type: "visual", Visual: "orders"}}}}},
		},
	}
}
func builderStringPtr(value string) *string { return &value }
func builderModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "status", Entities: map[string]semanticmodel.EntityDefinition{"status": {Type: "primary", Fields: []string{"status"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Label: "Status", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Dimensions: map[string]semanticmodel.SemanticDimension{"status": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Label: "Order count", Input: &semanticmodel.MetricInput{Field: "orders.status"}}}}
}

func TestBuildAuthorizesBeforeRevisionAndRuntimeAndPreservesExactToken(t *testing.T) {
	f := newBuilderFixture(t)
	f.authorizer.errors[authoring.AuthorizationActionEdit] = errors.Join(errors.New("policy"), access.ErrForbidden)
	if _, err := f.service.Build(t.Context(), Request{ProjectID: "project", ActorID: "actor", DashboardID: "sales"}); !errors.Is(err, access.ErrForbidden) || f.repository.revisionCalls != 0 || f.provider.acquireCalls != 0 {
		t.Fatalf("denied build err=%v revision=%d acquire=%d", err, f.repository.revisionCalls, f.provider.acquireCalls)
	}
	f = newBuilderFixture(t)
	signal, err := f.service.Build(t.Context(), Request{ProjectID: "project", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if signal.Revision.ID != f.revision.ID.String() || signal.Revision.Number != int64(f.revision.Number) || signal.Revision.ContentHash != f.revision.ContentHash || f.provider.acquireCalls != 1 || f.lease.releases != 1 {
		t.Fatalf("signal/token/lease = %#v/%#v acquire=%d release=%d", signal.Revision, f.revision.Token(), f.provider.acquireCalls, f.lease.releases)
	}
	if signal.SemanticModel.ID != "sales_model" {
		t.Fatalf("semantic model signal id = %q, want canonical resource id", signal.SemanticModel.ID)
	}
}

func TestBuildReleasesLeaseOnRuntimeFailuresAndProjectsDetachedModel(t *testing.T) {
	for name, mutate := range map[string]func(*builderFixture){"missing model": func(f *builderFixture) { f.runtime.model = nil }, "wrong model": func(f *builderFixture) { f.runtime.modelID = "other" }, "missing capability": func(f *builderFixture) { f.lease.runtime = plainBuilderRuntime{} }} {
		t.Run(name, func(t *testing.T) {
			f := newBuilderFixture(t)
			before := *f.runtime.model
			mutate(f)
			if _, err := f.service.Build(t.Context(), Request{ProjectID: "project", ActorID: "actor", DashboardID: "sales"}); err == nil {
				t.Fatal("build succeeded")
			}
			if f.lease.releases != 1 {
				t.Fatalf("release=%d", f.lease.releases)
			}
			if f.runtime.model != nil && !reflect.DeepEqual(*f.runtime.model, before) && name != "wrong model" {
				t.Fatal("model mutated")
			}
		})
	}
}

func TestProjectPagesCanonicalOrderingAndGlobalBounds(t *testing.T) {
	doc := builderDocument()
	doc.Spec.Pages = []document.DashboardPage{{ID: "z-page", Title: "Z"}, {ID: "a-page", Title: "A"}}
	pages, _, selected, _, err := projectPages(doc, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{pages[0].ID, pages[1].ID}; !reflect.DeepEqual(got, []string{"a-page", "z-page"}) || selected != "a-page" {
		t.Fatalf("pages/selection = %#v/%q", got, selected)
	}
	doc.Spec.Pages = make([]document.DashboardPage, maxPages+1)
	if _, _, _, _, err := projectPages(doc, "", ""); err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") {
		t.Fatalf("bound error = %v", err)
	}
}

type builderRepository struct {
	lifecycle     authoring.DashboardLifecycle
	revisions     map[authoring.RevisionID]authoring.Revision
	revisionCalls int
}

func (r *builderRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *builderRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *builderRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *builderRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	panic("unused")
}
func (r *builderRepository) GetRevision(_ context.Context, _ graph.ResourceID, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.revisionCalls++
	value, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return value, nil
}
func (r *builderRepository) LookupCommandResult(context.Context, graph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	panic("unused")
}
func (r *builderRepository) LookupCreateOperation(context.Context, authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	panic("unused")
}
func (r *builderRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	panic("unused")
}
func (r *builderRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *builderRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *builderRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	panic("unused")
}

type builderAuthorizer struct {
	errors map[authoring.AuthorizationAction]error
}

func (a *builderAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	return a.errors[request.Action]
}

type builderProvider struct {
	lease        *builderLease
	acquireCalls int
}

func (p *builderProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquireCalls++
	return p.lease, nil
}

type builderLease struct {
	runtime  projectruntime.Runtime
	identity graph.ServingIdentity
	releases int
}

func (l *builderLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *builderLease) Identity() graph.ServingIdentity { return l.identity }
func (l *builderLease) Release()                        { l.releases++ }

type builderRuntime struct {
	modelID graph.ResourceID
	model   *semanticmodel.Model
}

func (r *builderRuntime) Close() error { return nil }
func (r *builderRuntime) SemanticModelProjection(id graph.ResourceID) (*semanticmodel.Model, bool) {
	if r.model == nil || r.modelID != id {
		return nil, false
	}
	copy := *r.model
	return &copy, true
}

type plainBuilderRuntime struct{}

func (plainBuilderRuntime) Close() error { return nil }
