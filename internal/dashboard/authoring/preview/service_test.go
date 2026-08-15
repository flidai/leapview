package preview

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type previewFixture struct {
	repository *fakeRepository
	authorizer *fakeAuthorizer
	provider   *fakeProvider
	runtime    *fakeRuntime
	service    *Service
	request    PreviewRequest
	revision   authoring.Revision
}

func newPreviewFixture(t *testing.T) previewFixture {
	t.Helper()
	document := previewDocument()
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("revision-1", "sales", 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		WorkspaceID: "workspace", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales",
		SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-1", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	model := previewModel()
	runtime := &fakeRuntime{model: model}
	provider := &fakeProvider{lease: &fakeLease{runtime: runtime, servingState: "serving-1", snapshot: 42}}
	repository := &fakeRepository{lifecycle: lifecycle, revision: revision}
	authorizer := &fakeAuthorizer{}
	service, err := NewService(Options{Repository: repository, Authorizer: authorizer, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return previewFixture{
		repository: repository, authorizer: authorizer, provider: provider, runtime: runtime, service: service,
		request:  PreviewRequest{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: revision.Token(), PageID: "overview"},
		revision: revision,
	}
}

func previewDocument() authoring.Dashboard {
	return authoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales_model",
		Visuals: authoring.TabularVisualizations("table", map[string]authoring.TableVisual{
			"orders": {Title: "Orders", Query: authoring.TableQuery{Table: "orders", Fields: []string{"orders.status", "order_count"}}},
		}),
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders", Kind: "visual", Visual: "orders", Placement: dashboard.PagePlacement{Col: 1, Row: 1, ColSpan: 4, RowSpan: 4}}}}},
	}
}

func previewModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales_model",
		Tables: map[string]semanticmodel.Table{
			"orders": {Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string"}}},
		},
		Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Aggregation: "count", Input: semanticmodel.MeasureInput{Field: "orders.status"}, Empty: "zero"}},
	}
}

func TestPreviewAuthorizesBeforeLoadingRevision(t *testing.T) {
	fixture := newPreviewFixture(t)
	fixture.authorizer.err = errors.New("denied")
	_, err := fixture.service.Preview(context.Background(), fixture.request)
	if !errors.Is(err, fixture.authorizer.err) {
		t.Fatalf("Preview() error = %v, want denial", err)
	}
	if fixture.repository.getRevisionCalls != 0 {
		t.Fatal("revision loaded before edit authorization")
	}
	if fixture.provider.acquireCalls != 0 {
		t.Fatal("runtime acquired before edit authorization")
	}
	if got := fixture.authorizer.requests[0].Action; got != authoring.AuthorizationActionEdit {
		t.Fatalf("authorization action = %q, want edit", got)
	}
}

func TestPreviewRejectsStaleRevisionBeforeLoadingOrAcquiring(t *testing.T) {
	fixture := newPreviewFixture(t)
	fixture.request.ExpectedRevision.Number++
	_, err := fixture.service.Preview(context.Background(), fixture.request)
	if !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("Preview() error = %v, want stale revision", err)
	}
	if fixture.repository.getRevisionCalls != 0 || fixture.provider.acquireCalls != 0 {
		t.Fatalf("stale preview touched later boundaries: revision=%d acquire=%d", fixture.repository.getRevisionCalls, fixture.provider.acquireCalls)
	}
}

func TestPreviewStrictlyCompilesInvalidDraftWithoutPersistence(t *testing.T) {
	fixture := newPreviewFixture(t)
	invalid := fixture.revision
	invalid.Document.Visuals["orders"] = authoring.TabularVisualizations("table", map[string]authoring.TableVisual{
		"orders": {Title: "Orders", Query: authoring.TableQuery{Table: "missing_table", Fields: []string{"orders.status", "order_count"}}},
	})["orders"]
	invalid.ContentHash, _ = authoring.DashboardContentHash(invalid.Document)
	fixture.repository.revision = invalid
	fixture.repository.lifecycle.Draft.Revision = invalid.Token()
	fixture.request.ExpectedRevision = invalid.Token()
	_, err := fixture.service.Preview(context.Background(), fixture.request)
	if err == nil {
		t.Fatal("invalid strict draft unexpectedly previewed")
	}
	if fixture.provider.acquireCalls != 1 || fixture.provider.lease.releaseCalls != 1 {
		t.Fatalf("lease lifecycle = acquire %d release %d", fixture.provider.acquireCalls, fixture.provider.lease.releaseCalls)
	}
	if fixture.repository.writeCalls != 0 {
		t.Fatal("preview performed a repository write")
	}
}

func TestPreviewUsesOneLeaseForModelAndQueryAndReturnsEvidence(t *testing.T) {
	fixture := newPreviewFixture(t)
	result, err := fixture.service.Preview(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.provider.acquireCalls != 1 || fixture.provider.lease.releaseCalls != 1 {
		t.Fatalf("lease lifecycle = acquire %d release %d", fixture.provider.acquireCalls, fixture.provider.lease.releaseCalls)
	}
	if fixture.runtime.projectionCalls != 1 || fixture.runtime.queryCalls != 1 {
		t.Fatalf("runtime calls = projection %d query %d", fixture.runtime.projectionCalls, fixture.runtime.queryCalls)
	}
	if fixture.runtime.queryRuntime != fixture.runtime {
		t.Fatal("query did not execute on leased runtime")
	}
	if result.Revision != fixture.revision.Token() || result.Definition.ID != "sales" || result.PagePatch.Status.Error != "" {
		t.Fatalf("unexpected preview result: %#v", result)
	}
	if result.SemanticEvidence.ServingStateID != "serving-1" || result.SemanticEvidence.DuckLakeSnapshotID != 42 {
		t.Fatalf("semantic evidence = %#v", result.SemanticEvidence)
	}
}

func TestPreviewRejectsSemanticModelMismatchAndReleasesLease(t *testing.T) {
	fixture := newPreviewFixture(t)
	fixture.runtime.model = &semanticmodel.Model{Name: "other_model"}
	_, err := fixture.service.Preview(context.Background(), fixture.request)
	if !errors.Is(err, ErrSemanticMismatch) {
		t.Fatalf("Preview() error = %v, want semantic mismatch", err)
	}
	if fixture.provider.lease.releaseCalls != 1 {
		t.Fatalf("release calls = %d, want one", fixture.provider.lease.releaseCalls)
	}
	if fixture.runtime.queryCalls != 0 {
		t.Fatal("query ran after semantic mismatch")
	}
}

func TestPreviewDoesNotMutateBaseRuntimeModel(t *testing.T) {
	fixture := newPreviewFixture(t)
	result, err := fixture.service.Preview(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	result.Definition.Pages[0].Visuals[0].ID = "mutated"
	delete(result.Definition.Visualizations, "orders")
	projected, ok := fixture.runtime.SemanticModelProjection("sales_model")
	if !ok || projected.Name != "sales_model" || projected.Tables["orders"].Dimensions["status"].Field != "orders.status" {
		t.Fatalf("base runtime model changed: %#v", projected)
	}
}

type fakeRepository struct {
	lifecycle        authoring.DashboardLifecycle
	revision         authoring.Revision
	getRevisionCalls int
	writeCalls       int
	mu               sync.Mutex
}

func (r *fakeRepository) Get(context.Context, string, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *fakeRepository) GetRevision(context.Context, string, authoring.DashboardID, authoring.RevisionID) (authoring.Revision, error) {
	r.mu.Lock()
	r.getRevisionCalls++
	r.mu.Unlock()
	return r.revision, nil
}

type fakeAuthorizer struct {
	requests []authoringservice.AuthorizationRequest
	err      error
}

func (a *fakeAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.requests = append(a.requests, request)
	return a.err
}

type fakeProvider struct {
	lease        *fakeLease
	acquireCalls int
}

func (p *fakeProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquireCalls++
	return p.lease, nil
}

type fakeLease struct {
	runtime      *fakeRuntime
	servingState servingstate.ID
	snapshot     int64
	releaseCalls int
}

func (l *fakeLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *fakeLease) ServingStateID() servingstate.ID { return l.servingState }
func (l *fakeLease) DuckLakeSnapshotID() int64       { return l.snapshot }
func (l *fakeLease) Release()                        { l.releaseCalls++ }

type fakeRuntime struct {
	model           *semanticmodel.Model
	projectionCalls int
	queryCalls      int
	queryRuntime    *fakeRuntime
}

func (r *fakeRuntime) Close() error { return nil }
func (r *fakeRuntime) SemanticModelProjection(id string) (*semanticmodel.Model, bool) {
	r.projectionCalls++
	if r.model == nil || r.model.Name != id {
		return nil, false
	}
	encoded := *r.model
	encoded.Tables = map[string]semanticmodel.Table{}
	for name, table := range r.model.Tables {
		copyTable := table
		copyTable.Dimensions = map[string]semanticmodel.MetricDimension{}
		for field, dimension := range table.Dimensions {
			copyTable.Dimensions[field] = dimension
		}
		encoded.Tables[name] = copyTable
	}
	return &encoded, true
}
func (r *fakeRuntime) QueryDashboardPageForDefinition(_ context.Context, _ definition.Definition, _ string, _ dashboard.Filters) (dashboard.Patch, error) {
	r.queryCalls++
	r.queryRuntime = r
	return dashboard.EmptyPatch(dashboard.Filters{}, nil), nil
}
