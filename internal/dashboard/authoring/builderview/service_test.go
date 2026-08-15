package builderview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/runtimehost"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

var builderTestTime = time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

func TestBuildAuthorizesEditBeforeDraftRevisionDisclosure(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.authorizer.errByAction[authoring.AuthorizationActionEdit] = errors.Join(errors.New("policy"), access.ErrForbidden)

	_, err := fixture.service.Build(context.Background(), Request{WorkspaceID: " workspace ", ActorID: "actor", DashboardID: "sales"})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("Build error = %v, want forbidden", err)
	}
	if fixture.repository.revisionCalls != 0 {
		t.Fatalf("GetRevision calls = %d, want zero before edit authorization", fixture.repository.revisionCalls)
	}
	if fixture.provider.acquireCalls != 0 {
		t.Fatalf("runtime acquisitions = %d, want zero before edit authorization", fixture.provider.acquireCalls)
	}
}

func TestBuildUsesExactDraftRevisionAndReleasesOneLease(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.repository.lifecycle.Draft.Revision.ContentHash = "wrong" // the repository pointer is malformed

	_, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err == nil || !strings.Contains(err.Error(), "validate current draft pointer") {
		t.Fatalf("Build error = %v, want invalid exact draft pointer", err)
	}
	if fixture.repository.revisionCalls != 0 || fixture.provider.acquireCalls != 0 {
		t.Fatalf("malformed pointer disclosed/acquired revisionCalls=%d acquireCalls=%d", fixture.repository.revisionCalls, fixture.provider.acquireCalls)
	}

	fixture = newBuilderFixture(t)
	signal, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if signal.Revision.ID != fixture.revision.ID.String() || signal.Revision.Number != int64(fixture.revision.Number) || signal.Revision.ContentHash != fixture.revision.ContentHash {
		t.Fatalf("revision signal = %#v, want exact token %#v", signal.Revision, fixture.revision.Token())
	}
	if fixture.repository.revisionID != fixture.revision.ID {
		t.Fatalf("GetRevision id = %q, want %q", fixture.repository.revisionID, fixture.revision.ID)
	}
	if fixture.provider.acquireCalls != 1 || fixture.lease.releaseCalls != 1 {
		t.Fatalf("lease lifecycle acquire=%d release=%d, want 1/1", fixture.provider.acquireCalls, fixture.lease.releaseCalls)
	}
}

func TestBuildReleasesLeaseOnEveryPostAcquireFailure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*builderFixture)
	}{
		{name: "empty serving state", mutate: func(f *builderFixture) { f.lease.servingState = "" }},
		{name: "runtime missing capability", mutate: func(f *builderFixture) { f.lease.runtime = &plainRuntime{} }},
		{name: "semantic model missing", mutate: func(f *builderFixture) { f.runtime.model = nil }},
		{name: "semantic identity mismatch", mutate: func(f *builderFixture) { f.runtime.model.Name = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBuilderFixture(t)
			test.mutate(fixture)
			if _, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"}); err == nil {
				t.Fatal("Build succeeded with invalid active runtime")
			}
			if fixture.provider.acquireCalls != 1 || fixture.lease.releaseCalls != 1 {
				t.Fatalf("lease lifecycle acquire=%d release=%d, want 1/1", fixture.provider.acquireCalls, fixture.lease.releaseCalls)
			}
		})
	}
}

func TestBuildProjectsDetachedSemanticModelWithoutMutation(t *testing.T) {
	fixture := newBuilderFixture(t)
	beforeModel := cloneBuilderModel(fixture.runtime.model)
	beforeDocument, err := fixture.revision.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fixture.runtime.model, beforeModel) {
		t.Fatalf("runtime semantic model was mutated: got %#v want %#v", fixture.runtime.model, beforeModel)
	}
	if !reflect.DeepEqual(fixture.revision.Document, beforeDocument) {
		t.Fatalf("authored dashboard document was mutated: got %#v want %#v", fixture.revision.Document, beforeDocument)
	}
}

func TestBuildSeparatesOriginAndForkEvidence(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.revision.Provenance = authoring.Provenance{
		Origin: authoring.OriginFile, ActorID: "actor",
		Source: &authoring.SourceMetadata{Path: "dashboards/sales.yaml"},
		ForkedFrom: &authoring.ForkEvidence{Kind: authoring.ForkSourceWorkspace, Workspace: &authoring.WorkspaceForkEvidence{
			SourceWorkspaceID: "source", SourceDashboardID: "upstream", SourceRevision: fixture.revision.Token(),
		}},
	}
	// The revision hash/provenance are immutable fields, so rebuild it with the
	// desired provenance and update both exact pointers in the fixture.
	revision, err := authoring.NewRevision(fixture.revision.ID, "sales", fixture.revision.Number, fixture.revision.CreatedAt, fixture.revision.Document, fixture.revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revision = revision
	fixture.repository.revisions[revision.ID] = revision
	fixture.repository.lifecycle.Draft.Revision = revision.Token()

	signal, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if signal.Origin.Kind != "file" || signal.Origin.Label != "Project file" || signal.Origin.SourcePath == nil || *signal.Origin.SourcePath != "dashboards/sales.yaml" {
		t.Fatalf("origin = %#v", signal.Origin)
	}
	if signal.SourceEvidence == nil || signal.SourceEvidence.Value == nil {
		t.Fatalf("source evidence = %#v, want workspace fork evidence", signal.SourceEvidence)
	}
	kind, err := signal.SourceEvidence.Kind()
	if err != nil || kind != "workspace" {
		t.Fatalf("source evidence kind = %q, err=%v", kind, err)
	}
	if signal.Origin.Kind == kind {
		t.Fatalf("origin and fork evidence collapsed into one kind: %#v", signal.Origin)
	}
}

func TestSourceEvidenceProjectsBothForkVariants(t *testing.T) {
	workspace := sourceEvidenceSignal(authoring.Provenance{ForkedFrom: &authoring.ForkEvidence{
		Kind:      authoring.ForkSourceWorkspace,
		Workspace: &authoring.WorkspaceForkEvidence{SourceWorkspaceID: "source", SourceDashboardID: "upstream", SourceRevision: authoring.RevisionToken{RevisionID: "revision-1", Number: 2, ContentHash: "sha256:" + strings.Repeat("a", 64)}},
	}})
	if workspace == nil || workspace.Value == nil {
		t.Fatalf("workspace evidence = %#v", workspace)
	}
	workspaceKind, err := workspace.Kind()
	if err != nil || workspaceKind != "workspace" {
		t.Fatalf("workspace evidence kind = %q, err=%v", workspaceKind, err)
	}

	project := sourceEvidenceSignal(authoring.Provenance{ForkedFrom: &authoring.ForkEvidence{
		Kind:    authoring.ForkSourceProject,
		Project: &authoring.ProjectForkEvidence{SourceWorkspaceID: "source", SourceDashboardID: "upstream", ServingStateID: "serving-1", Path: "dashboards/upstream.yaml"},
	}})
	if project == nil || project.Value == nil {
		t.Fatalf("project evidence = %#v", project)
	}
	projectKind, err := project.Kind()
	if err != nil || projectKind != "project" {
		t.Fatalf("project evidence kind = %q, err=%v", projectKind, err)
	}
	if workspaceKind == projectKind {
		t.Fatal("workspace and project fork evidence kinds collapsed")
	}
}

func TestBuildDeterministicProjectionAndComponentIdentity(t *testing.T) {
	fixture := newBuilderFixture(t)
	document := fixture.revision.Document
	document.Pages = []dashboard.Page{
		{ID: "z-page", Title: "Z", Visuals: []dashboard.PageVisual{{ID: "z-placement", Kind: "visual", Visual: "orders"}}},
		{ID: "a-page", Title: "A", Visuals: []dashboard.PageVisual{
			{ID: "second-placement", Kind: "visual", Visual: "orders"},
			{ID: "first-placement", Kind: "visual", Visual: "orders"},
		}},
	}
	revision, err := authoring.NewRevision(fixture.revision.ID, "sales", fixture.revision.Number, fixture.revision.CreatedAt, document, fixture.revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revision = revision
	fixture.repository.revisions[revision.ID] = revision
	fixture.repository.lifecycle.Draft.Revision = revision.Token()

	one, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	two, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.Pages, two.Pages) || !reflect.DeepEqual(one.SemanticModel, two.SemanticModel) {
		t.Fatalf("projection is not deterministic:\none=%#v\ntwo=%#v", one, two)
	}
	if got := []string{one.Pages[0].ID, one.Pages[1].ID}; !reflect.DeepEqual(got, []string{"a-page", "z-page"}) {
		t.Fatalf("page order = %#v", got)
	}
	if got := []string{one.Pages[0].Visuals[0].ID, one.Pages[0].Visuals[1].ID}; !reflect.DeepEqual(got, []string{"first-placement", "second-placement"}) {
		t.Fatalf("component identity/order = %#v", got)
	}
	if got := []string{one.Pages[0].Visuals[0].VisualID, one.Pages[0].Visuals[1].VisualID}; !reflect.DeepEqual(got, []string{"orders", "orders"}) {
		t.Fatalf("authored visual identity = %#v", got)
	}
}

func TestBuildBoundsGlobalCountsAndDoesNotLeakAuthoredUnion(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.revision.Document.Visuals["orders"] = authoring.ChartVisualization(authoring.Visual{
		Type: "bar", Title: "Revenue", Query: authoring.VisualQuery{
			Dimensions: []authoring.FieldRef{{Field: "SUM(secret_password)", Alias: "SUM(secret_password)"}},
			Measures:   []authoring.FieldRef{{Field: "orders.amount", Alias: "amount"}},
		},
	})
	// Rebuild the immutable revision after changing its document.
	revision, err := authoring.NewRevision(fixture.revision.ID, "sales", fixture.revision.Number, fixture.revision.CreatedAt, fixture.revision.Document, fixture.revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	fixture.revision = revision
	fixture.repository.revisions[revision.ID] = revision
	fixture.repository.lifecycle.Draft.Revision = revision.Token()
	signal, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret_password") || strings.Contains(string(encoded), "AuthoringVisualization") {
		t.Fatalf("unsafe/authored union content leaked in signal: %s", encoded)
	}

	tooMany := fixture.revision.Document
	tooMany.Pages = make([]dashboard.Page, maxPages+1)
	for i := range tooMany.Pages {
		tooMany.Pages[i].ID = "page-" + string(rune('a'+i%26)) + string(rune('A'+i/26))
	}
	tooManyRevision, err := authoring.NewRevision(fixture.revision.ID, "sales", fixture.revision.Number, fixture.revision.CreatedAt, tooMany, fixture.revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	fixture.repository.revisions[tooManyRevision.ID] = tooManyRevision
	fixture.repository.lifecycle.Draft.Revision = tooManyRevision.Token()
	if _, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"}); err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") {
		t.Fatalf("global page bound error = %v", err)
	}
}

func TestBuildCapabilityMappingTreatsForbiddenAsFalseAndBackendErrorsAsFatal(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.authorizer.errByAction[authoring.AuthorizationActionPublish] = access.ErrForbidden
	fixture.authorizer.errByAction[authoring.AuthorizationActionView] = access.ErrForbidden
	signal, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
	if err != nil {
		t.Fatal(err)
	}
	if !signal.Capabilities.CanEdit || !signal.Capabilities.CanShare || !signal.Capabilities.CanPreview || signal.Capabilities.CanPublish || signal.Capabilities.CanExport {
		t.Fatalf("capabilities = %#v", signal.Capabilities)
	}

	fixture = newBuilderFixture(t)
	fixture.authorizer.errByAction[authoring.AuthorizationActionPublish] = errors.New("policy backend unavailable")
	if _, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"}); err == nil || !strings.Contains(err.Error(), "policy backend unavailable") {
		t.Fatalf("backend authorization error = %v", err)
	}
	if fixture.provider.acquireCalls != 0 {
		t.Fatalf("runtime acquired after capability backend error: %d", fixture.provider.acquireCalls)
	}
}

func TestBuildRejectsInvalidCurrentLifecycleBeforeRevisionOrRuntime(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*builderFixture)
		want   string
	}{
		{name: "archived", mutate: func(f *builderFixture) {
			f.repository.lifecycle.Status = authoring.LifecycleStatusArchived
		}, want: "dashboard is archived"},
		{name: "unsupported status", mutate: func(f *builderFixture) {
			f.repository.lifecycle.Status = authoring.LifecycleStatus("retired")
		}, want: "unsupported lifecycle status"},
		{name: "missing draft", mutate: func(f *builderFixture) {
			f.repository.lifecycle.Draft = nil
		}, want: "dashboard has no draft"},
		{name: "draft belongs to another dashboard", mutate: func(f *builderFixture) {
			f.repository.lifecycle.Draft.DashboardID = "other"
		}, want: "draft identity does not match lifecycle"},
		{name: "incomplete draft pointer", mutate: func(f *builderFixture) {
			f.repository.lifecycle.Draft.Revision = authoring.RevisionToken{}
		}, want: "validate current draft pointer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBuilderFixture(t)
			test.mutate(fixture)
			_, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want %q", err, test.want)
			}
			if fixture.repository.revisionCalls != 0 || fixture.provider.acquireCalls != 0 {
				t.Fatalf("invalid lifecycle disclosed/acquired revisionCalls=%d acquireCalls=%d", fixture.repository.revisionCalls, fixture.provider.acquireCalls)
			}
		})
	}
}

func TestRevisionNumberOverflowFailsBeforeSignalConversion(t *testing.T) {
	fixture := newBuilderFixture(t)
	fixture.revision.Number = uint64(1 << 63) // keep the document/hash valid, but exceed signal int64
	fixture.revision.ContentHash = fixture.revision.Token().ContentHash
	fixture.repository.revisions[fixture.revision.ID] = fixture.revision
	fixture.repository.lifecycle.Draft.Revision = fixture.revision.Token()
	if _, err := fixture.service.Build(context.Background(), Request{WorkspaceID: "workspace", ActorID: "actor", DashboardID: "sales"}); err == nil || !strings.Contains(err.Error(), "exceeds signal range") {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestProjectSemanticModelUsesDeterministicGlobalBounds(t *testing.T) {
	t.Run("tables", func(t *testing.T) {
		model := &semanticmodel.Model{Tables: make(map[string]semanticmodel.Table, maxTables+1)}
		for i := 0; i <= maxTables; i++ {
			model.Tables[fmt.Sprintf("table_%04d", i)] = semanticmodel.Table{}
		}
		if _, err := projectSemanticModel(model); err == nil || !strings.Contains(err.Error(), "tables exceed bounded limit") {
			t.Fatalf("table bound error = %v", err)
		}
	})
	t.Run("fields per table", func(t *testing.T) {
		dimensions := make(map[string]semanticmodel.SemanticDimension, maxFields+1)
		for i := 0; i <= maxFields; i++ {
			id := fmt.Sprintf("field_%04d", i)
			dimensions[id] = semanticmodel.SemanticDimension{Bindings: map[string]semanticmodel.DimensionBinding{
				"orders": {Field: "orders." + id},
			}}
		}
		model := &semanticmodel.Model{
			Tables:     map[string]semanticmodel.Table{"orders": {}},
			Dimensions: dimensions,
		}
		if _, err := projectSemanticModel(model); err == nil || !strings.Contains(err.Error(), "fields exceed bounded limit") {
			t.Fatalf("field bound error = %v", err)
		}
	})
	t.Run("fields globally", func(t *testing.T) {
		model := &semanticmodel.Model{Tables: make(map[string]semanticmodel.Table, 2)}
		for tableIndex := 0; tableIndex < 2; tableIndex++ {
			dimensions := make(map[string]semanticmodel.MetricDimension, maxFields/2+1)
			for fieldIndex := 0; fieldIndex <= maxFields/2; fieldIndex++ {
				id := fmt.Sprintf("field_%04d", fieldIndex)
				dimensions[id] = semanticmodel.MetricDimension{Field: fmt.Sprintf("table_%d.%s", tableIndex, id)}
			}
			model.Tables[fmt.Sprintf("table_%d", tableIndex)] = semanticmodel.Table{Dimensions: dimensions}
		}
		if _, err := projectSemanticModel(model); err == nil || !strings.Contains(err.Error(), "fields exceed bounded limit") {
			t.Fatalf("global field bound error = %v", err)
		}
	})
}

func TestProjectPagesUsesGlobalBoundsAndPlacementIdentity(t *testing.T) {
	document := authoring.Dashboard{
		Visuals: map[string]authoring.AuthoringVisualization{
			"orders": authoring.TabularVisualization("table", authoring.TableVisual{}),
		},
	}
	t.Run("pages", func(t *testing.T) {
		document.Pages = make([]dashboard.Page, maxPages+1)
		for i := range document.Pages {
			document.Pages[i].ID = fmt.Sprintf("page_%04d", i)
		}
		if _, _, _, _, err := projectPages(document, "", ""); err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") {
			t.Fatalf("page bound error = %v", err)
		}
	})
	t.Run("visuals", func(t *testing.T) {
		document.Pages = []dashboard.Page{{ID: "overview", Visuals: make([]dashboard.PageVisual, maxVisuals+1)}}
		for i := range document.Pages[0].Visuals {
			document.Pages[0].Visuals[i] = dashboard.PageVisual{ID: fmt.Sprintf("placement_%04d", i), Kind: "visual", Visual: "orders"}
		}
		if _, _, _, _, err := projectPages(document, "", ""); err == nil || !strings.Contains(err.Error(), "visuals exceed bounded limit") {
			t.Fatalf("visual bound error = %v", err)
		}
	})
}

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
	document := authoring.Dashboard{
		ID: "sales", Title: "Sales", SemanticModel: "sales",
		Visuals: map[string]authoring.AuthoringVisualization{
			"orders": authoring.TabularVisualization("table", authoring.TableVisual{Title: "Orders", Query: authoring.TableQuery{Columns: []authoring.FieldRef{{Field: "orders.status", Alias: "Status"}}}}),
		},
		Pages: []dashboard.Page{{ID: "overview", Title: "Overview", Visuals: []dashboard.PageVisual{{ID: "orders-placement", Kind: "visual", Visual: "orders"}}}},
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}
	revision, err := authoring.NewRevision("revision-1", "sales", 7, builderTestTime, document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		WorkspaceID: "workspace", ID: "sales", OwnerPrincipalID: "owner", Slug: "sales", Title: "Sales", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft-1", DashboardID: "sales", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &builderRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision}}
	authorizer := &builderAuthorizer{errByAction: map[authoring.AuthorizationAction]error{}}
	runtime := &builderRuntime{model: &semanticmodel.Model{
		Name: "sales", Title: "Sales model", Tables: map[string]semanticmodel.Table{
			"orders": {Description: "Orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Label: "Status", Type: "string"}}},
		}, Measures: map[string]semanticmodel.MetricMeasure{"order_count": {Fact: "orders", Label: "Order count", Input: semanticmodel.MeasureInput{Field: "orders.id"}}},
	}}
	lease := &builderLease{runtime: runtime, servingState: "serving-1"}
	provider := &builderProvider{lease: lease}
	service, err := NewService(Options{Provider: provider, Repository: repository, Authorizer: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	return &builderFixture{service: service, repository: repository, authorizer: authorizer, provider: provider, lease: lease, runtime: runtime, revision: revision}
}

type builderRepository struct {
	lifecycle     authoring.DashboardLifecycle
	revisions     map[authoring.RevisionID]authoring.Revision
	revisionCalls int
	revisionID    authoring.RevisionID
}

func (r *builderRepository) Create(context.Context, authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	return authoring.DashboardLifecycle{}, errors.New("unexpected create")
}
func (r *builderRepository) Get(context.Context, string, authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	return r.lifecycle, nil
}
func (r *builderRepository) List(context.Context, string) ([]authoring.DashboardLifecycle, error) {
	return nil, errors.New("unexpected list")
}
func (r *builderRepository) CountBySemanticModel(context.Context, string) ([]authoring.SemanticModelUsage, error) {
	return nil, errors.New("unexpected count")
}
func (r *builderRepository) GetRevision(_ context.Context, _ string, _ authoring.DashboardID, id authoring.RevisionID) (authoring.Revision, error) {
	r.revisionCalls++
	r.revisionID = id
	revision, ok := r.revisions[id]
	if !ok {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return revision, nil
}
func (r *builderRepository) LookupCommandResult(context.Context, string, authoring.DashboardID, authoring.CommandEvidence) (authoring.CommandResult, bool, error) {
	return authoring.CommandResult{}, false, errors.New("unexpected command lookup")
}
func (r *builderRepository) AppendDraft(context.Context, authoring.AppendDraftInput) (authoring.Revision, error) {
	return authoring.Revision{}, errors.New("unexpected append")
}
func (r *builderRepository) Publish(context.Context, authoring.PublishInput) (authoring.DashboardLifecycle, error) {
	return authoring.DashboardLifecycle{}, errors.New("unexpected publish")
}
func (r *builderRepository) Archive(context.Context, authoring.ArchiveInput) (authoring.DashboardLifecycle, error) {
	return authoring.DashboardLifecycle{}, errors.New("unexpected archive")
}
func (r *builderRepository) GetPublishedCompilation(context.Context, string, authoring.DashboardID) (authoring.CompiledRevision, error) {
	return authoring.CompiledRevision{}, errors.New("unexpected compilation")
}

type builderAuthorizer struct {
	errByAction map[authoring.AuthorizationAction]error
}

func (a *builderAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	return a.errByAction[request.Action]
}

type builderProvider struct {
	lease        *builderLease
	acquireCalls int
}

func (p *builderProvider) Acquire(context.Context) (runtimehost.Lease, error) {
	p.acquireCalls++
	return p.lease, nil
}

type builderLease struct {
	runtime      runtimehost.Runtime
	servingState servingstate.ID
	releaseCalls int
}

func (l *builderLease) Runtime() runtimehost.Runtime    { return l.runtime }
func (l *builderLease) ServingStateID() servingstate.ID { return l.servingState }
func (l *builderLease) DuckLakeSnapshotID() int64       { return 0 }
func (l *builderLease) Release()                        { l.releaseCalls++ }

type builderRuntime struct {
	model *semanticmodel.Model
}

func (r *builderRuntime) Close() error { return nil }
func (r *builderRuntime) SemanticModelProjection(id string) (*semanticmodel.Model, bool) {
	if r.model == nil || r.model.Name != id {
		return nil, false
	}
	return r.model, true
}

type plainRuntime struct{}

func (*plainRuntime) Close() error { return nil }

func cloneBuilderModel(value *semanticmodel.Model) *semanticmodel.Model {
	if value == nil {
		return nil
	}
	copied := *value
	if value.Tables != nil {
		copied.Tables = make(map[string]semanticmodel.Table, len(value.Tables))
		for id, table := range value.Tables {
			tableCopy := table
			if table.Dimensions != nil {
				tableCopy.Dimensions = make(map[string]semanticmodel.MetricDimension, len(table.Dimensions))
				for field, dimension := range table.Dimensions {
					tableCopy.Dimensions[field] = dimension
				}
			}
			copied.Tables[id] = tableCopy
		}
	}
	if value.Measures != nil {
		copied.Measures = make(map[string]semanticmodel.MetricMeasure, len(value.Measures))
		for id, measure := range value.Measures {
			measureCopy := measure
			if measure.Filters != nil {
				measureCopy.Filters = append([]semanticmodel.MeasureFilter(nil), measure.Filters...)
			}
			copied.Measures[id] = measureCopy
		}
	}
	if value.Dimensions != nil {
		copied.Dimensions = make(map[string]semanticmodel.SemanticDimension, len(value.Dimensions))
		for id, dimension := range value.Dimensions {
			dimensionCopy := dimension
			if dimension.Bindings != nil {
				dimensionCopy.Bindings = make(map[string]semanticmodel.DimensionBinding, len(dimension.Bindings))
				for table, binding := range dimension.Bindings {
					bindingCopy := binding
					bindingCopy.Path = append([]string(nil), binding.Path...)
					dimensionCopy.Bindings[table] = bindingCopy
				}
			}
			dimensionCopy.Grains = append([]string(nil), dimension.Grains...)
			copied.Dimensions[id] = dimensionCopy
		}
	}
	return &copied
}

var _ authoring.Repository = (*builderRepository)(nil)
var _ authoringservice.Authorizer = (*builderAuthorizer)(nil)
var _ runtimehost.Provider = (*builderProvider)(nil)
var _ Runtime = (*builderRuntime)(nil)
