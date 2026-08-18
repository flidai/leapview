package preview

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type previewFixture struct {
	repository *previewRepository
	authorizer *previewAuthorizer
	provider   *previewProvider
	runtime    *previewRuntime
	service    *Service
	request    PreviewRequest
	revision   authoring.Revision
}

func newPreviewFixture(t *testing.T) previewFixture {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("revision-1", "sales", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), previewDocument(), provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "draft-1", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &previewRuntime{model: previewModel()}
	identity, _ := graph.NewServingIdentity("project", "production", "serving-1")
	provider := &previewProvider{lease: &previewLease{runtime: runtime, identity: identity}}
	repo := &previewRepository{lifecycle: lifecycle, revision: revision}
	auth := &previewAuthorizer{}
	svc, err := NewService(Options{Repository: repo, Authorizer: auth, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return previewFixture{repository: repo, authorizer: auth, provider: provider, runtime: runtime, service: svc, request: PreviewRequest{ProjectID: "project", ActorID: "actor", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: revision.Token(), PageID: "overview"}, revision: revision}
}

func previewDocument() document.DashboardDocument {
	status, metric := "status", "order_count"
	return document.DashboardDocument{APIVersion: document.DashboardApiVersionLeapviewDevV1, Kind: document.DashboardResourceKindDashboard, Metadata: document.DashboardMetadata{ID: "sales", Name: "sales", DisplayName: previewStringPtr("Sales")}, Spec: document.DashboardSpec{SemanticModel: "sales_model", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{"orders": {Type: document.DashboardVisualTypeBar, Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &status}}, Metrics: []document.DashboardMetricSelection{{String: &metric}}}}, Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}}}}, Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}}}}
}
func previewStringPtr(value string) *string { return &value }
func previewModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "sales_model", Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}, Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "status", Entities: map[string]semanticmodel.ModelEntitySpec{"status": {Type: "primary", Fields: []string{"status"}}}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString}}}}, Dimensions: map[string]semanticmodel.SemanticDimension{"status": {Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}}}}, Metrics: map[string]semanticmodel.Metric{"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero"}}}
}

func TestPreviewAuthorizesBeforeRevisionAndRuntimeReads(t *testing.T) {
	f := newPreviewFixture(t)
	f.authorizer.err = errors.New("denied")
	if _, err := f.service.Preview(t.Context(), f.request); !errors.Is(err, f.authorizer.err) {
		t.Fatalf("error = %v", err)
	}
	if f.repository.getRevisionCalls != 0 || f.provider.acquireCalls != 0 {
		t.Fatalf("reads after denial = revision %d acquire %d", f.repository.getRevisionCalls, f.provider.acquireCalls)
	}
	if len(f.authorizer.requests) != 1 || f.authorizer.requests[0].Action != authoring.AuthorizationActionEdit {
		t.Fatalf("authorization = %#v", f.authorizer.requests)
	}
}

func TestPreviewRejectsStaleBeforeLeaseAndCompilesThroughOneLease(t *testing.T) {
	f := newPreviewFixture(t)
	f.request.ExpectedRevision.Number++
	if _, err := f.service.Preview(t.Context(), f.request); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale error = %v", err)
	}
	if f.provider.acquireCalls != 0 || f.repository.getRevisionCalls != 0 {
		t.Fatalf("stale touched boundaries: acquire=%d revision=%d", f.provider.acquireCalls, f.repository.getRevisionCalls)
	}
	f = newPreviewFixture(t)
	result, err := f.service.Preview(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	if f.provider.acquireCalls != 1 || f.provider.lease.releases != 1 || f.runtime.projectionCalls != 1 || f.runtime.queryCalls != 1 {
		t.Fatalf("lease/runtime calls = acquire %d release %d projection %d query %d", f.provider.acquireCalls, f.provider.lease.releases, f.runtime.projectionCalls, f.runtime.queryCalls)
	}
	if result.Revision != f.revision.Token() || result.Definition.ID != "sales" || result.SemanticEvidence.Identity.GenerationID != "serving-1" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPreviewSemanticMismatchAndStrictErrorReleaseWithoutPersistence(t *testing.T) {
	f := newPreviewFixture(t)
	f.runtime.model = &semanticmodel.Model{Name: "other_model"}
	if _, err := f.service.Preview(t.Context(), f.request); !errors.Is(err, ErrSemanticMismatch) || f.provider.lease.releases != 1 || f.runtime.queryCalls != 0 {
		t.Fatalf("mismatch err=%v release=%d query=%d", err, f.provider.lease.releases, f.runtime.queryCalls)
	}
	f = newPreviewFixture(t)
	invalidDoc, err := f.revision.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	value := invalidDoc.Spec.Visuals["orders"]
	metric, dimension := "missing_metric", "status"
	value.Query.Value = &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &dimension}}, Metrics: []document.DashboardMetricSelection{{String: &metric}}}
	invalidDoc.Spec.Visuals["orders"] = value
	invalidRevision, err := authoring.NewRevision("revision-invalid", "sales", 2, time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC), invalidDoc, f.revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	f.repository.revision = invalidRevision
	f.repository.lifecycle.Draft.Revision = invalidRevision.Token()
	f.request.ExpectedRevision = invalidRevision.Token()
	if _, err := f.service.Preview(t.Context(), f.request); err == nil {
		t.Fatal("invalid draft previewed")
	}
	if f.provider.lease.releases != 1 || f.repository.writeCalls != 0 {
		t.Fatalf("strict preview side effects release=%d writes=%d", f.provider.lease.releases, f.repository.writeCalls)
	}
}

func TestPreviewResultMutationDoesNotMutateRuntimeModel(t *testing.T) {
	f := newPreviewFixture(t)
	result, err := f.service.Preview(t.Context(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	before := *f.runtime.model
	result.Definition.Pages[0].Title = "changed"
	if !reflect.DeepEqual(*f.runtime.model, before) {
		t.Fatal("runtime model mutated")
	}
}

type previewRepository struct {
	lifecycle                    authoring.DashboardLifecycle
	revision                     authoring.Revision
	getRevisionCalls, writeCalls int
}

func (r *previewRepository) Get(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *previewRepository) GetRevision(context.Context, graph.ResourceID, authoring.DashboardID, authoring.RevisionID) (authoring.Revision, error) {
	r.getRevisionCalls++
	return r.revision, nil
}
func (r *previewRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	r.writeCalls++
	panic("preview write")
}
func (r *previewRepository) List(context.Context, graph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	panic("unused")
}
func (r *previewRepository) CountBySemanticModel(context.Context, graph.ResourceID) ([]authoring.SemanticModelUsage, error) {
	panic("unused")
}
func (r *previewRepository) LookupCommandResult(context.Context, graph.ResourceID, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	panic("unused")
}
func (r *previewRepository) LookupCreateOperation(context.Context, authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	panic("unused")
}
func (r *previewRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	r.writeCalls++
	panic("preview write")
}
func (r *previewRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	r.writeCalls++
	panic("preview write")
}
func (r *previewRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	r.writeCalls++
	panic("preview write")
}
func (r *previewRepository) GetPublishedCompilation(context.Context, graph.ResourceID, authoring.DashboardID) (authoring.CompiledRevision, error) {
	panic("unused")
}

type previewAuthorizer struct {
	requests []authoringservice.AuthorizationRequest
	err      error
}

func (a *previewAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	return a.err
}

type previewProvider struct {
	lease        *previewLease
	acquireCalls int
}

func (p *previewProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	p.acquireCalls++
	return p.lease, nil
}

type previewLease struct {
	runtime  *previewRuntime
	identity graph.ServingIdentity
	releases int
}

func (l *previewLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *previewLease) Identity() graph.ServingIdentity { return l.identity }
func (l *previewLease) Release()                        { l.releases++ }

type previewRuntime struct {
	model                       *semanticmodel.Model
	projectionCalls, queryCalls int
}

func (r *previewRuntime) Close() error              { return nil }
func (r *previewRuntime) DuckLakeSnapshotID() int64 { return 42 }
func (r *previewRuntime) SemanticModelProjection(id graph.ResourceID) (*semanticmodel.Model, bool) {
	r.projectionCalls++
	if r.model == nil || r.model.Name != id.String() {
		return nil, false
	}
	copy := *r.model
	return &copy, true
}
func (r *previewRuntime) QueryDashboardPageForDefinition(context.Context, definition.Definition, string, dashboard.Filters) (dashboard.Patch, error) {
	r.queryCalls++
	return dashboard.EmptyPatch(dashboard.Filters{}, nil), nil
}
