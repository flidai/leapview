package application_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

const chatVisualCommandID = authoring.CommandID("018f5c8a-9d40-7c50-9e31-1c8a7b1a0e0b")

func TestAddChatVisualToDraftPreservesDocumentAndReplaysOriginalResult(t *testing.T) {
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "owner", ConversationID: "conversation", ToolCallID: "call-chat-visual"}
	initialDocument := chatVisualIntegrationDocument()
	initialRevision, err := authoring.NewRevision("revision-initial", "dashboard", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), initialDocument, authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "sales", ID: "dashboard", OwnerPrincipalID: "owner", Slug: "dashboard", Title: "Dashboard",
		SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft", DashboardID: "dashboard", Revision: initialRevision.Token(), Provenance: initialRevision.Provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &applicationRepository{
		lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{initialRevision.ID: initialRevision},
		commands: map[authoring.CommandID]authoring.CommandResult{}, fingerprints: map[authoring.CommandID]string{},
	}
	auth := &applicationAuthorizer{}
	app, err := application.New(application.Options{
		Authoring: newApplicationService(t, repo, auth), Repository: repo, Authorizer: auth,
		AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	source := chatVisualIntegrationSource()
	request := application.AddChatVisualRequest{
		ProjectID: "sales", ActorID: "owner", DashboardID: "dashboard", PageID: "overview",
		Source: source, CommandID: chatVisualCommandID, Provenance: provenance,
	}
	added, err := app.AddChatVisualToDraft(t.Context(), request)
	if err != nil {
		t.Fatalf("AddChatVisualToDraft() error = %v", err)
	}
	if added.Revision.Number != 2 || repo.appendCalls != 1 {
		t.Fatalf("add result revision/appends = %d/%d, want 2/1", added.Revision.Number, repo.appendCalls)
	}
	addedRevision, err := repo.GetRevision(t.Context(), "sales", "dashboard", added.Revision.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	visualID, imported := findImportedChatVisual(t, addedRevision.Document)
	if !reflect.DeepEqual(imported, source.Visual) {
		t.Fatalf("imported visual = %#v, want exact source %#v", imported, source.Visual)
	}
	if len(addedRevision.Document.Spec.Visuals) != len(initialDocument.Spec.Visuals)+1 {
		t.Fatalf("visual count = %d, want %d", len(addedRevision.Document.Spec.Visuals), len(initialDocument.Spec.Visuals)+1)
	}
	for id, visual := range initialDocument.Spec.Visuals {
		if got := addedRevision.Document.Spec.Visuals[id]; !reflect.DeepEqual(got, visual) {
			t.Errorf("existing visual %q changed: %#v != %#v", id, got, visual)
		}
	}
	if got, want := len(addedRevision.Document.Spec.Pages[0].Components), len(initialDocument.Spec.Pages[0].Components)+1; got != want {
		t.Fatalf("overview component count = %d, want %d", got, want)
	}
	component := addedRevision.Document.Spec.Pages[0].Components[len(addedRevision.Document.Spec.Pages[0].Components)-1]
	base, err := component.Base()
	if err != nil {
		t.Fatal(err)
	}
	visualComponent, ok := component.Value.(*document.VisualDashboardPageComponent)
	if !ok || visualComponent.Visual != visualID || base.Placement.Column != 1 || base.Placement.Row != 7 || base.Placement.ColumnSpan != 6 || base.Placement.RowSpan != 4 {
		t.Fatalf("appended component placement/visual = %#v/%#v, want visual %q at column 1 row 7 span 6x4", base, visualComponent, visualID)
	}
	if len(addedRevision.Document.Spec.Filters) != len(initialDocument.Spec.Filters)+len(source.Filters) {
		t.Fatalf("filter count = %d, want %d", len(addedRevision.Document.Spec.Filters), len(initialDocument.Spec.Filters)+len(source.Filters))
	}
	if !reflect.DeepEqual(addedRevision.Document.Spec.Filters[0], initialDocument.Spec.Filters[0]) {
		t.Fatalf("existing filter changed: %#v != %#v", addedRevision.Document.Spec.Filters[0], initialDocument.Spec.Filters[0])
	}
	for _, filter := range addedRevision.Document.Spec.Filters[1:] {
		if filter.Targets == nil || len(*filter.Targets) != 1 || (*filter.Targets)[0] != visualID {
			t.Errorf("imported filter %q targets = %#v, want only %q", filter.ID, filter.Targets, visualID)
		}
	}

	replayed, err := app.AddChatVisualToDraft(t.Context(), request)
	if err != nil || replayed.Revision != added.Revision || repo.appendCalls != 1 {
		t.Fatalf("same-key replay = %#v, err=%v, appends=%d", replayed, err, repo.appendCalls)
	}

	// Simulate a later edit that removes the imported visual. A retry must
	// return its immutable original result instead of resurrecting that visual.
	laterDocument, err := addedRevision.Document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	delete(laterDocument.Spec.Visuals, visualID)
	page := &laterDocument.Spec.Pages[0]
	components := page.Components[:0]
	for _, candidate := range page.Components {
		candidateBase, err := candidate.Base()
		if err != nil {
			t.Fatal(err)
		}
		if candidateBase.ID != visualID+"_component" {
			components = append(components, candidate)
		}
	}
	page.Components = components
	filters := laterDocument.Spec.Filters[:0]
	for _, filter := range laterDocument.Spec.Filters {
		if filter.ID != "chat-region" && filter.ID != "chat-status" {
			filters = append(filters, filter)
		}
	}
	laterDocument.Spec.Filters = filters
	laterProvenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"}
	laterRevision, err := authoring.NewRevision("revision-later", "dashboard", 3, time.Date(2026, 8, 18, 0, 2, 0, 0, time.UTC), laterDocument, laterProvenance)
	if err != nil {
		t.Fatal(err)
	}
	laterLifecycle := repo.lifecycle
	laterLifecycle.Draft.Revision = laterRevision.Token()
	laterLifecycle.Draft.Provenance = laterRevision.Provenance
	repo.lifecycle = laterLifecycle
	repo.revisions[laterRevision.ID] = laterRevision

	replayedAfterDeletion, err := app.AddChatVisualToDraft(t.Context(), request)
	if err != nil || replayedAfterDeletion.Revision != added.Revision || repo.appendCalls != 1 {
		t.Fatalf("replay after later deletion = %#v, err=%v, appends=%d", replayedAfterDeletion, err, repo.appendCalls)
	}
	currentDraft, err := app.Draft(t.Context(), application.DraftRequest{ProjectID: "sales", ActorID: "owner", DashboardID: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := currentDraft.Revision.Document.Spec.Visuals[visualID]; exists {
		t.Fatal("retry resurrected a visual deleted by a later draft edit")
	}

	changedPage := request
	changedPage.PageID = "summary"
	if _, err := app.AddChatVisualToDraft(t.Context(), changedPage); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("same-key request with a changed page error = %v, want ErrCommandReuse", err)
	}
	changedSource := request
	changedTitle := "A different visual"
	changedSource.Source.Visual.Title = &changedTitle
	if _, err := app.AddChatVisualToDraft(t.Context(), changedSource); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("same-key request with changed source error = %v, want ErrCommandReuse", err)
	}
	if repo.appendCalls != 1 {
		t.Fatalf("conflicting retries appended %d revisions", repo.appendCalls)
	}
}

func chatVisualIntegrationDocument() document.DashboardDocument {
	return document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard", Name: "dashboard"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales", Filters: []document.DashboardFilter{{
				ID: "existing-filter", Label: "Existing", Dimension: "region",
				Targets: stringSlice("existing-sales"),
				Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}},
			}},
			Visuals: map[string]document.DashboardVisual{
				"existing-sales": integrationVisual("Existing sales", "month", "net_sales", 73),
				"existing-cost":  integrationVisual("Existing cost", "month", "cost", 91),
			},
			Pages: []document.DashboardPage{
				{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{
					integrationVisualComponent("sales-component", "existing-sales", 1, 3),
					integrationVisualComponent("cost-component", "existing-cost", 4, 3),
				}},
				{ID: "summary", Title: "Summary", Components: []document.DashboardPageComponent{}},
			},
		},
	}
}

func chatVisualIntegrationSource() application.ChatVisualImport {
	queryLimit := int32(321)
	dataBudget := document.DashboardDataBudget{MaxRows: 137}
	visual := document.DashboardVisual{
		Type: document.DashboardVisualTypeColumn, Title: stringPtr("Chat net sales by country"),
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr("country")}},
			Metrics: []document.DashboardMetricSelection{{String: stringPtr("net_sales")}}, Limit: &queryLimit,
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}}, DataBudget: &dataBudget,
	}
	target := []string{"chat-artifact"}
	return application.ChatVisualImport{
		ArtifactID: "chat-artifact", ToolCallID: "call-chat-visual", SemanticModelID: graph.ResourceID("sales"), Visual: visual,
		Filters: []document.DashboardFilter{
			{ID: "chat-region", Label: "Region", Dimension: "region", Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}},
			{ID: "chat-status", Label: "Order status", Dimension: "status", Targets: &target, Control: document.DashboardFilterControl{Value: &document.MultiSelectDashboardFilterControl{Type: "multiSelect"}}},
		},
	}
}

func integrationVisual(title, dimension, metric string, limit int32) document.DashboardVisual {
	return document.DashboardVisual{
		Type: document.DashboardVisualTypeBar, Title: stringPtr(title),
		Query: document.DashboardQuery{Value: &document.AggregateDashboardQuery{
			Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{{String: stringPtr(dimension)}},
			Metrics: []document.DashboardMetricSelection{{String: stringPtr(metric)}}, Limit: &limit,
		}},
		Presentation: document.DashboardPresentation{Value: &document.CartesianDashboardPresentation{Type: "cartesian"}},
	}
}

func integrationVisualComponent(id, visual string, row, rowSpan int32) document.DashboardPageComponent {
	return document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: id, Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: row, ColumnSpan: 6, RowSpan: rowSpan}},
		Type:                       "visual", Visual: visual,
	}}
}

func findImportedChatVisual(t *testing.T, value document.DashboardDocument) (string, document.DashboardVisual) {
	t.Helper()
	for _, component := range value.Spec.Pages[0].Components {
		visual, ok := component.Value.(*document.VisualDashboardPageComponent)
		if !ok || !strings.HasPrefix(visual.Visual, "chat_visual_") {
			continue
		}
		definition, exists := value.Spec.Visuals[visual.Visual]
		if !exists {
			t.Fatalf("page component references missing imported visual %q", visual.Visual)
		}
		return visual.Visual, definition
	}
	t.Fatal("imported visual page component was not found")
	return "", document.DashboardVisual{}
}

func stringPtr(value string) *string { return &value }

func stringSlice(values ...string) *[]string { return &values }
