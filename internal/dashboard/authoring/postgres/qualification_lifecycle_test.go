package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/preview"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

const (
	qualificationProject = graph.ResourceID("project:sales")
	qualificationModel   = graph.ResourceID("model:sales")
	qualificationActor   = "actor"
	qualificationOwner   = "018f4f2e-0000-7000-0000-000000000601"
)

// TestDashboardAuthoringQualificationCreateCopyEditCompilePreviewPublishArchive
// is intentionally a real persistence qualification: the service uses the
// PostgreSQL repository, while preview uses one instrumented active-runtime fake.
func TestDashboardAuthoringQualificationCreateCopyEditCompilePreviewPublishArchive(t *testing.T) {
	db := authoringDB(t)
	repository, err := New(testDBTX{db}, &authoringAudit{}, authoringEvents{}, &authoringFence{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	compiler := &qualificationCompiler{}
	ids := qualificationIDs{
		dashboards: []authoring.DashboardID{"dashboard:source", "dashboard:copy"},
		drafts: []authoring.DraftID{
			"018f4f2e-0000-7000-8000-000000000701",
			"018f4f2e-0000-7000-8000-000000000702",
		},
		revisions: []authoring.RevisionID{
			"018f4f2e-0000-7000-8000-000000000711",
			"018f4f2e-0000-7000-8000-000000000712",
			"018f4f2e-0000-7000-8000-000000000713",
			"018f4f2e-0000-7000-8000-000000000714",
		},
	}
	service, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository,
		Authorizer: qualificationAuthorizer{},
		Compiler:   compiler,
		Now:        func() time.Time { return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) {
			return ids.nextDashboard()
		},
		NewDraftID: func() (authoring.DraftID, error) {
			return ids.nextDraft()
		},
		NewRevisionID: func() (authoring.RevisionID, error) {
			return ids.nextRevision()
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000721"), "dashboard_authoring.draft_created"), authoringservice.CreateRequest{
		ProjectID: qualificationProject, ActorID: qualificationActor, OwnerPrincipalID: qualificationOwner,
		Title: "Sales", Slug: "sales", SemanticModel: qualificationModel,
		Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision.Number != 1 || created.Lifecycle.Status != authoring.LifecycleStatusDraft || created.Lifecycle.Draft == nil {
		t.Fatalf("create result = %#v", created)
	}

	// Seed one governed visual through the trusted document replacement command;
	// subsequent browser-shaped edits remain narrow typed commands.
	sourceDocument := qualificationDocument(created.Lifecycle.ID.String(), "sales", "Sales")
	sourceEdit, err := service.Execute(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000722"), "dashboard_authoring.draft_edited"), qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000722", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID,
		ExpectedRevision: created.Revision, Provenance: qualificationProvenance("source-document"),
		ReplaceDocument: &authoring.ReplaceDocumentPayload{Document: sourceDocument},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sourceEdit.Revision.Number != 2 {
		t.Fatalf("source edit revision = %#v, want number 2", sourceEdit.Revision)
	}

	sourcePublished, err := service.Execute(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000723"), "dashboard_authoring.published"), qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000723", DashboardID: created.Lifecycle.ID, DraftID: created.Lifecycle.Draft.ID,
		ExpectedRevision: sourceEdit.Revision, Provenance: qualificationProvenance("source-publish"),
		Publish: &authoring.PublishPayload{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sourcePublished.Lifecycle.Status != authoring.LifecycleStatusPublished || sourcePublished.Lifecycle.Published == nil || compiler.calls != 1 {
		t.Fatalf("source publish = %#v, compiler calls=%d", sourcePublished, compiler.calls)
	}

	// Fork copies the retained published revision into a fresh private draft;
	// it must not alter the source lifecycle or its revision history.
	copied, err := service.Fork(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000724"), "dashboard_authoring.draft_created"), authoringservice.ForkRequest{
		ProjectID: qualificationProject, SourceDashboardID: created.Lifecycle.ID,
		ActorID: qualificationActor, OwnerPrincipalID: qualificationOwner, Title: "Sales Copy", Slug: "sales-copy",
		Origin: authoring.OriginUI, IdempotencyKey: "fork-copy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if copied.Revision.Number != 1 || copied.Lifecycle.Status != authoring.LifecycleStatusDraft || copied.Lifecycle.Draft == nil {
		t.Fatalf("copy result = %#v", copied)
	}
	if got, err := repository.Get(ctx, qualificationProject, created.Lifecycle.ID); err != nil || got.Status != authoring.LifecycleStatusPublished {
		t.Fatalf("source lifecycle after fork = %#v (%v)", got, err)
	}

	runtime := &qualificationRuntime{model: qualificationModelProjection()}
	identity, err := graph.NewServingIdentity(qualificationProject, "dev", "generation-qualification")
	if err != nil {
		t.Fatal(err)
	}
	provider := &qualificationProvider{lease: &qualificationLease{runtime: runtime, identity: identity}}
	previewService, err := preview.NewService(preview.Options{
		Repository: repository, Authorizer: qualificationAuthorizer{}, Provider: provider,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Two simulated tabs both read copy revision 1. The first commits a
	// placement-only edit; the second must fail CAS without adding a revision.
	tabRevision := copied.Revision
	moved, err := service.Execute(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000725"), "dashboard_authoring.draft_edited"), qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000725", DashboardID: copied.Lifecycle.ID, DraftID: copied.Lifecycle.Draft.ID,
		ExpectedRevision: tabRevision, Provenance: qualificationProvenance("copy-placement"),
		SetPlacements: &authoring.SetPlacementsPayload{PageID: "overview", Placements: []authoring.PlacementUpdate{{
			ComponentID: "orders-card", Placement: document.DashboardPlacement{Column: 2, Row: 1, ColumnSpan: 6, RowSpan: 4},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Revision.Number != 2 || moved.Revision == tabRevision {
		t.Fatalf("placement revision = %#v, previous=%#v", moved.Revision, tabRevision)
	}
	if _, err := service.Execute(ctx, qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000736", DashboardID: copied.Lifecycle.ID, DraftID: copied.Lifecycle.Draft.ID,
		ExpectedRevision: tabRevision, Provenance: qualificationProvenance("copy-stale-tab"),
		RenamePage: &authoring.RenamePagePayload{PageID: "overview", Title: "Stale tab"},
	}); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale tab error = %v", err)
	}
	current, err := repository.Get(ctx, qualificationProject, copied.Lifecycle.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Draft == nil || current.Draft.Revision != moved.Revision {
		t.Fatalf("stale tab changed current draft = %#v", current)
	}

	// Placement persistence and compile-only preview are both query-free. This
	// is the regression guard for geometry-only commands: no visual is executed
	// or requeried merely because a component moved.
	compiled, err := previewService.Compile(ctx, preview.CompileRequest{
		ProjectID: qualificationProject, ActorID: qualificationActor, DashboardID: copied.Lifecycle.ID,
		DraftID: copied.Lifecycle.Draft.ID, ExpectedRevision: moved.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Revision != moved.Revision || compiled.Definition.ID != copied.Lifecycle.ID.String() || runtime.queryCalls != 0 {
		t.Fatalf("compile result/query metrics = %#v/%d", compiled, runtime.queryCalls)
	}
	previewResult, err := previewService.Preview(ctx, preview.PreviewRequest{
		ProjectID: qualificationProject, ActorID: qualificationActor, DashboardID: copied.Lifecycle.ID,
		DraftID: copied.Lifecycle.Draft.ID, ExpectedRevision: moved.Revision, PageID: "overview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if previewResult.Revision != moved.Revision || previewResult.Definition.ID != copied.Lifecycle.ID.String() || runtime.queryCalls != 1 {
		t.Fatalf("preview result/query metrics = %#v/%d", previewResult, runtime.queryCalls)
	}

	published, err := service.Execute(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000726"), "dashboard_authoring.published"), qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000726", DashboardID: copied.Lifecycle.ID, DraftID: copied.Lifecycle.Draft.ID,
		ExpectedRevision: moved.Revision, Provenance: qualificationProvenance("copy-publish"),
		Publish: &authoring.PublishPayload{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Lifecycle.Status != authoring.LifecycleStatusPublished || published.Revision != moved.Revision || compiler.calls != 2 {
		t.Fatalf("copy publish = %#v, compiler calls=%d", published, compiler.calls)
	}
	storedCompilation, err := repository.GetPublishedCompilation(ctx, qualificationProject, copied.Lifecycle.ID)
	if err != nil || storedCompilation.AuthoredRevision != moved.Revision {
		t.Fatalf("stored copy compilation = %#v (%v)", storedCompilation, err)
	}

	archived, err := service.Execute(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000000727"), "dashboard_authoring.archived"), qualificationProject, authoring.Command{
		ID: "018f4f2e-0000-7000-8000-000000000727", DashboardID: copied.Lifecycle.ID,
		ExpectedRevision: published.Revision, Provenance: qualificationProvenance("copy-archive"),
		Archive: &authoring.ArchivePayload{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Lifecycle.Status != authoring.LifecycleStatusArchived || archived.Revision != moved.Revision {
		t.Fatalf("archive result = %#v", archived)
	}
}

type qualificationIDs struct {
	dashboards []authoring.DashboardID
	drafts     []authoring.DraftID
	revisions  []authoring.RevisionID
	dashboard  int
	draft      int
	revision   int
}

func (ids *qualificationIDs) nextDashboard() (authoring.DashboardID, error) {
	if ids.dashboard >= len(ids.dashboards) {
		return "", errors.New("qualification dashboard ids exhausted")
	}
	id := ids.dashboards[ids.dashboard]
	ids.dashboard++
	return id, nil
}

func (ids *qualificationIDs) nextDraft() (authoring.DraftID, error) {
	if ids.draft >= len(ids.drafts) {
		return "", errors.New("qualification draft ids exhausted")
	}
	id := ids.drafts[ids.draft]
	ids.draft++
	return id, nil
}

func (ids *qualificationIDs) nextRevision() (authoring.RevisionID, error) {
	if ids.revision >= len(ids.revisions) {
		return "", errors.New("qualification revision ids exhausted")
	}
	id := ids.revisions[ids.revision]
	ids.revision++
	return id, nil
}

func qualificationProvenance(commandID string) authoring.Provenance {
	return authoring.Provenance{Origin: authoring.OriginUI, ActorID: qualificationActor, ToolCallID: commandID}
}

func qualificationDocument(id, name, title string) document.DashboardDocument {
	dimension, metric := "status", "order_count"
	displayName := title
	return document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: id, Name: name, DisplayName: &displayName},
		Spec: document.DashboardSpec{
			SemanticModel: qualificationModel.String(), Filters: []document.DashboardFilter{},
			Visuals: map[string]document.DashboardVisual{
				"orders": {
					Type: document.DashboardVisualTypeBar,
					Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
						Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: &dimension}},
						Metrics: []document.DashboardMetricSelection{{String: &metric}},
					}},
					Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
				},
			},
			Pages: []document.DashboardPage{{
				ID: "overview", Title: "Overview",
				Components: []document.DashboardPageComponent{{Value: &document.VisualDashboardPageComponent{
					DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "orders-card", Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}},
					Type:                       "visual", Visual: "orders",
				}}},
			}},
		},
	}
}

type qualificationAuthorizer struct{}

func (qualificationAuthorizer) Authorize(context.Context, authoringservice.AuthorizationRequest) error {
	return nil
}

type qualificationCompiler struct{ calls int }

func (c *qualificationCompiler) Compile(_ context.Context, projectID, semanticModel graph.ResourceID, authored document.DashboardDocument) (authoringservice.Compilation, error) {
	c.calls++
	identity, err := graph.NewServingIdentity(projectID, "dev", "generation-qualification")
	if err != nil {
		return authoringservice.Compilation{}, err
	}
	title := authored.Metadata.Name
	if authored.Metadata.DisplayName != nil {
		title = *authored.Metadata.DisplayName
	}
	return authoringservice.Compilation{
		Definition:       dashboarddefinition.Definition{ID: authored.Metadata.ID, Title: title, SemanticModel: semanticModel.String(), Visualizations: map[string]visualizationdefinition.Definition{}},
		SemanticIdentity: identity,
	}, nil
}

type qualificationProvider struct{ lease *qualificationLease }

func (p *qualificationProvider) Acquire(context.Context) (projectruntime.Lease, error) {
	return p.lease, nil
}

type qualificationLease struct {
	runtime  *qualificationRuntime
	identity graph.ServingIdentity
}

func (l *qualificationLease) Runtime() projectruntime.Runtime { return l.runtime }
func (l *qualificationLease) Identity() graph.ServingIdentity { return l.identity }
func (l *qualificationLease) Release()                        {}

type qualificationRuntime struct {
	model      *semanticmodel.Model
	queryCalls int
}

func (r *qualificationRuntime) Close() error { return nil }

func (r *qualificationRuntime) SemanticModelProjection(graph.ResourceID) (*semanticmodel.Model, bool) {
	model := *r.model
	return &model, true
}

func (r *qualificationRuntime) QueryDashboardPageForDefinition(context.Context, dashboarddefinition.Definition, string, dashboard.Filters) (dashboard.Patch, error) {
	r.queryCalls++
	return dashboard.EmptyPatch(dashboard.Filters{}, nil), nil
}

func qualificationModelProjection() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name:     "sales",
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "status",
			Entities:   map[string]semanticmodel.EntityDefinition{"status": {Type: "primary", Fields: []string{"status"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"status": {Field: "orders.status", Type: "string", Datatype: semanticmodel.DataTypeString}},
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{"status": {
			Type: "string", Datatype: semanticmodel.DataTypeString,
			Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.status"}},
		}},
		Metrics: map[string]semanticmodel.Metric{"order_count": {
			Type: "aggregate", Dataset: "orders", Aggregation: "count",
			Input: &semanticmodel.MetricInput{Field: "orders.status"}, Empty: "zero",
		}},
	}
}
