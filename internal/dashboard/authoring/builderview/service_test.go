package builderview

import (
	"context"
	"errors"
	"reflect"
	"strconv"
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
			SemanticModel: "sales_model", Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}}}, Visuals: map[string]document.DashboardVisual{"orders": visual},
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
	if len(signal.VisualCatalog) != 26 || signal.VisualCatalog[0].Type != "line" || signal.VisualCatalog[0].ReferenceHref != "/docs/visuals/line" {
		t.Fatalf("visual catalog = %#v", signal.VisualCatalog)
	}
	if len(signal.Pages) != 1 || len(signal.Pages[0].Visuals) != 1 || len(signal.Pages[0].Visuals[0].FormatOptions) == 0 {
		t.Fatalf("projected format options = %#v", signal.Pages)
	}
	if len(signal.Filters) != 1 || signal.Filters[0].ID != "status" || signal.Filters[0].ControlType != "multiSelect" || !signal.Filters[0].ReaderEditable || len(signal.Filters[0].Bindings) != 1 || signal.Filters[0].Bindings[0].Scope != "report" {
		t.Fatalf("projected filters = %#v", signal.Filters)
	}
}

func TestProjectFiltersExposesAuthoredPageBindings(t *testing.T) {
	bindings := []document.DashboardPageFilterBinding{{ID: "page_status", Filter: "status"}}
	authored := document.DashboardDocument{Spec: document.DashboardSpec{
		Filters: []document.DashboardFilter{{ID: "status", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}}},
		Pages:   []document.DashboardPage{{ID: "overview", FilterBindings: &bindings}},
	}}
	projected := projectFilters(authored)
	if len(projected) != 1 || len(projected[0].Bindings) != 1 {
		t.Fatalf("projected filters = %#v", projected)
	}
	binding := projected[0].Bindings[0]
	if binding.ID != "page_status" || binding.Scope != "page" || binding.PageID == nil || *binding.PageID != "overview" {
		t.Fatalf("projected page binding = %#v", binding)
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

func TestProjectedSemanticDimensionIDAppliesToAggregateReducer(t *testing.T) {
	semantic, err := projectSemanticModel("sales_model", builderModel())
	if err != nil {
		t.Fatal(err)
	}
	var fieldID string
	var physicalField bool
	for _, dataset := range semantic.Datasets {
		if dataset.ID != "orders" {
			continue
		}
		for _, field := range dataset.Fields {
			if field.ID == "status" && field.Kind == "dimension" {
				fieldID = field.ID
			}
			if field.ID == "orders.status" && field.Kind == "dimension" {
				physicalField = true
			}
		}
	}
	if fieldID != "status" {
		t.Fatalf("projected semantic dimension id = %q, want unqualified member id", fieldID)
	}
	if !physicalField {
		t.Fatal("projected dataset lost its physical table field for detail/table use")
	}

	// Start with an aggregate visual that has no dimensions, then feed the
	// projected field through the existing canonical assign_field reducer path.
	doc := builderDocument()
	aggregate, ok := doc.Spec.Visuals["orders"].Query.Value.(*document.AggregateDashboardQuery)
	if !ok {
		t.Fatal("builder fixture visual is not aggregate")
	}
	aggregate.Dimensions = []document.DashboardDimensionSelection{}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	current, err := authoring.NewRevision("revision-assign", "sales", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales",
		SemanticModel: "sales_model", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-assign", DashboardID: "sales", Revision: current.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := authoring.Command{
		ID: "assign-status", DashboardID: "sales", DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: provenance,
		AssignField: &authoring.AssignFieldPayload{PageID: "overview", VisualID: "orders-placement", FieldID: fieldID, Role: authoring.FieldRoleDimension},
	}
	_, next, err := authoring.ApplyEdit(lifecycle, current, command, "revision-assign-2", 2, time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("assign projected semantic dimension: %v", err)
	}
	updated, ok := next.Document.Spec.Visuals["orders"].Query.Value.(*document.AggregateDashboardQuery)
	if !ok || len(updated.Dimensions) != 1 || updated.Dimensions[0].String == nil || *updated.Dimensions[0].String != "status" {
		t.Fatalf("assigned aggregate dimensions = %#v", updated)
	}
}

func TestProjectPagesPreservesAuthoredOrderingAndGlobalBounds(t *testing.T) {
	doc := builderDocument()
	doc.Spec.Pages = []document.DashboardPage{{ID: "z-page", Title: "Z"}, {ID: "a-page", Title: "A"}}
	pages, _, selected, _, err := projectPages(doc, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{pages[0].ID, pages[1].ID}; !reflect.DeepEqual(got, []string{"z-page", "a-page"}) || selected != "z-page" {
		t.Fatalf("pages/selection = %#v/%q", got, selected)
	}
	doc.Spec.Pages = make([]document.DashboardPage, maxPages+1)
	if _, _, _, _, err := projectPages(doc, "", ""); err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") {
		t.Fatalf("bound error = %v", err)
	}
}

func TestProjectPagesProjectsCanonicalFilterComponentsAndMissingReferences(t *testing.T) {
	doc := builderDocument()
	doc.Spec.Pages[0].Components = append(doc.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "status-slicer", Type: "filter", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 3, RowSpan: 2}},
		Type:                       "filter",
		Filter:                     "status",
	}})
	pages, diagnostics, _, _, err := projectPages(doc, "overview", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 || len(pages) != 1 || len(pages[0].FilterComponents) != 1 {
		t.Fatalf("projection = pages %#v diagnostics %#v", pages, diagnostics)
	}
	if pages[0].Grid.Columns != 12 || pages[0].Grid.RowHeight != 48 || pages[0].Grid.Gap != 16 || pages[0].Grid.Padding != 16 {
		t.Fatalf("default grid = %#v", pages[0].Grid)
	}
	component := pages[0].FilterComponents[0]
	if component.ID != "status-slicer" || component.FilterID != "status" || component.Label != "Status" || component.ControlType != "multiSelect" {
		t.Fatalf("filter component = %#v", component)
	}
	if component.Placement.Col != 7 || component.Placement.Row != 1 || component.Placement.ColSpan != 3 || component.Placement.RowSpan != 2 {
		t.Fatalf("filter component placement = %#v", component.Placement)
	}

	doc.Spec.Pages[0].Components[1].Value.(*document.FilterDashboardPageComponent).Filter = "missing"
	pages, diagnostics, _, _, err = projectPages(doc, "overview", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages[0].FilterComponents) != 0 || len(diagnostics) != 1 || diagnostics[0].Code != "FILTER_MISSING" {
		t.Fatalf("missing filter projection = pages %#v diagnostics %#v", pages, diagnostics)
	}

	doc = builderDocument()
	doc.Spec.Pages[0].Components = make([]document.DashboardPageComponent, 0, maxFilterComponents+1)
	for index := 0; index <= maxFilterComponents; index++ {
		doc.Spec.Pages[0].Components = append(doc.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
			DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "slicer-" + strconv.Itoa(index), Type: "filter", Placement: document.DashboardPlacement{Column: 1, Row: int32(index + 1), ColumnSpan: 3, RowSpan: 2}},
			Type:                       "filter",
			Filter:                     "status",
		}})
	}
	if _, _, _, _, err := projectPages(doc, "overview", ""); err == nil || !strings.Contains(err.Error(), "filter components exceed bounded limit") {
		t.Fatalf("filter component bound error = %v", err)
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
