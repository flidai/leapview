package authoring

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func reducerFixture(t *testing.T) (DashboardLifecycle, Revision) {
	t.Helper()
	document := Dashboard{
		ID:            "sales",
		Title:         "Sales",
		Description:   "A sales dashboard",
		SemanticModel: "sales_model",
		Visuals: map[string]AuthoringVisualization{
			"revenue": ChartVisualization(Visual{Title: "Revenue", Type: "line", Query: VisualQuery{Dimensions: []FieldRef{{Field: "month", Alias: "month"}}, Measures: []FieldRef{{Field: "revenue", Alias: "revenue"}}}}),
			"orders":  TabularVisualization("table", TableVisual{Title: "Orders", Query: TableQuery{Table: "orders", Fields: []string{"order_id"}}}),
		},
		Pages: []dashboardmodel.Page{{
			ID: "overview", Title: "Overview", Canvas: dashboardmodel.PageCanvas{Width: 1200, Height: 800}, Grid: dashboardmodel.PageGrid{Columns: 12, RowHeight: 40, Gap: 8, Padding: 8},
			Visuals: []dashboardmodel.PageVisual{
				{ID: "revenue-tile", Kind: "visual", Visual: "revenue", Placement: dashboardmodel.PagePlacement{Col: 1, Row: 1, ColSpan: 6, RowSpan: 4}, X: 1, Y: 2, Width: 3, Height: 4},
				{ID: "orders-tile", Kind: "visual", Visual: "orders", Placement: dashboardmodel.PagePlacement{Col: 7, Row: 1, ColSpan: 6, RowSpan: 4}, X: 5, Y: 6, Width: 7, Height: 8},
			},
		}},
	}
	created := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	provenance := contractProvenance()
	current, err := NewRevision("rev-1", "sales", 1, created, document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := DashboardLifecycle{ProjectID: "project-1", ID: "sales", OwnerPrincipalID: "principal-1", Slug: "sales", Title: "Sales", SemanticModel: document.SemanticModel, Visibility: VisibilityPrivate, Status: LifecycleStatusDraft, Draft: &Draft{ID: "draft-1", DashboardID: "sales", Revision: current.Token(), Provenance: provenance}}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	return lifecycle, current
}

func blankDraftFixture(t *testing.T) (DashboardLifecycle, Revision) {
	t.Helper()
	document := Dashboard{
		ID:            "blank",
		Title:         "Blank dashboard",
		SemanticModel: "sales_model",
		Visuals:       map[string]AuthoringVisualization{},
		Pages: []dashboardmodel.Page{{
			ID: "overview",
		}},
	}
	provenance := contractProvenance()
	current, err := NewRevision("rev-1", "blank", 1, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC), document, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := DashboardLifecycle{ProjectID: "project-1", ID: "blank", OwnerPrincipalID: "principal-1", Slug: "blank", Title: document.Title, SemanticModel: document.SemanticModel, Visibility: VisibilityPrivate, Status: LifecycleStatusDraft, Draft: &Draft{ID: "draft-1", DashboardID: "blank", Revision: current.Token(), Provenance: provenance}}
	if err := lifecycle.Validate(); err != nil {
		t.Fatal(err)
	}
	return lifecycle, current
}

func reducerCommand(current Revision, payload authoringPayload) Command {
	command := Command{ID: "command-1", DashboardID: "sales", DraftID: "draft-1", ExpectedRevision: current.Token(), Provenance: contractProvenance()}
	return reducerCommandFor(command, payload)
}

func reducerCommandFor(command Command, payload authoringPayload) Command {
	switch value := payload.(type) {
	case *MetadataPatch:
		command.Metadata = value
	case *UpsertPagePayload:
		command.UpsertPage = value
	case *RemovePagePayload:
		command.RemovePage = value
	case *UpsertVisualPayload:
		command.UpsertVisual = value
	case *RemoveVisualPayload:
		command.RemoveVisual = value
	case *SetLayoutPayload:
		command.SetLayout = value
	case *SetFiltersPayload:
		command.SetFilters = value
	case *SetInteractionPayload:
		command.SetInteraction = value
	}
	return command
}

func applyReducer(t *testing.T, lifecycle DashboardLifecycle, current Revision, command Command) (DashboardLifecycle, Revision) {
	t.Helper()
	next, revision, err := ApplyEdit(lifecycle, current, command, RevisionID(fmt.Sprintf("rev-%d", current.Number+1)), current.Number+1, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return next, revision
}

func TestApplyEditPayloads(t *testing.T) {
	title := "Updated sales"
	tests := []struct {
		name  string
		edit  func(Revision) authoringPayload
		check func(*testing.T, DashboardLifecycle, Revision)
	}{
		{name: "metadata", edit: func(Revision) authoringPayload {
			return &MetadataPatch{Title: &title, Description: stringPtr(""), Slug: stringPtr("updated-sales"), Visibility: visibilityPtr(VisibilityOrganization)}
		}, check: func(t *testing.T, lifecycle DashboardLifecycle, revision Revision) {
			if lifecycle.Title != title || lifecycle.Slug != "updated-sales" || lifecycle.Visibility != VisibilityOrganization || revision.Document.Description != "" {
				t.Fatalf("metadata not applied: lifecycle=%#v document=%#v", lifecycle, revision.Document)
			}
		}},
		{name: "page upsert", edit: func(Revision) authoringPayload {
			return &UpsertPagePayload{Page: dashboardmodel.Page{ID: "new-page", Title: "New page", Canvas: dashboardmodel.PageCanvas{Width: 1200, Height: 800}, Grid: dashboardmodel.PageGrid{Columns: 12, RowHeight: 40, Gap: 8, Padding: 8}}}
		}, check: func(t *testing.T, _ DashboardLifecycle, revision Revision) {
			if len(revision.Document.Pages) != 2 || revision.Document.Pages[1].ID != "new-page" {
				t.Fatalf("page not appended: %#v", revision.Document.Pages)
			}
		}},
		{name: "visual upsert", edit: func(Revision) authoringPayload {
			return &UpsertVisualPayload{VisualID: "new-visual", Visual: ChartVisualization(Visual{Title: "New", Type: "line", Query: VisualQuery{Dimensions: []FieldRef{{Field: "month", Alias: "month"}}, Measures: []FieldRef{{Field: "revenue", Alias: "revenue"}}}})}
		}, check: func(t *testing.T, _ DashboardLifecycle, revision Revision) {
			if _, ok := revision.Document.Visuals["new-visual"]; !ok {
				t.Fatal("visual not inserted")
			}
		}},
		{name: "layout update", edit: func(Revision) authoringPayload {
			return &SetLayoutPayload{PageID: "overview", Canvas: &dashboardmodel.PageCanvas{Width: 1400, Height: 900}, Placements: map[string]dashboardmodel.PagePlacement{"revenue-tile": {Col: 2, Row: 2, ColSpan: 4, RowSpan: 3}, "orders-tile": {Col: 6, Row: 2, ColSpan: 6, RowSpan: 3}}}
		}, check: func(t *testing.T, _ DashboardLifecycle, revision Revision) {
			if revision.Document.Pages[0].Canvas.Width != 1400 || revision.Document.Pages[0].Visuals[0].Placement.Col != 2 || revision.Document.Pages[0].Visuals[0].X != 1 || revision.Document.Pages[0].Visuals[0].Width != 3 {
				t.Fatalf("layout update changed authored/compiled geometry unexpectedly: %#v", revision.Document.Pages[0])
			}
		}},
		{name: "chart interaction", edit: func(Revision) authoringPayload {
			return &SetInteractionPayload{PageID: "overview", VisualID: "revenue", Interaction: &Interaction{PointSelection: SelectionInteraction{Toggle: true, Mappings: []SelectionMapping{{Field: "orders.id", Value: "id"}}}}}
		}, check: func(t *testing.T, _ DashboardLifecycle, revision Revision) {
			if !revision.Document.Visuals["revenue"].Chart.Interaction.PointSelection.Toggle {
				t.Fatal("chart interaction not applied")
			}
		}},
		{name: "table interaction clear", edit: func(Revision) authoringPayload { return &SetInteractionPayload{VisualID: "orders", Clear: true} }, check: func(t *testing.T, _ DashboardLifecycle, revision Revision) {
			if !revision.Document.Visuals["orders"].Tabular.Interaction.PointSelection.IsZero() {
				t.Fatal("table interaction not cleared")
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lifecycle, current := reducerFixture(t)
			next, revision := applyReducer(t, lifecycle, current, reducerCommand(current, test.edit(current)))
			test.check(t, next, revision)
		})
	}
}

func TestApplyEditAllowsPlacementClearInDraft(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	before := current
	command := reducerCommand(current, &SetLayoutPayload{PageID: "overview", Placements: map[string]dashboardmodel.PagePlacement{}})
	_, revision, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("empty placement clear error = %v", err)
	}
	for _, visual := range revision.Document.Pages[0].Visuals {
		if !visual.Placement.IsZero() {
			t.Fatalf("placement was not cleared in draft: %#v", visual.Placement)
		}
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatal("successful layout clear mutated source revision")
	}
}

func TestApplyEditBlankPageAddIncompleteVisualAndRemoveLastVisual(t *testing.T) {
	lifecycle, current := blankDraftFixture(t)
	if err := current.Document.ValidateDraftStructure(); err != nil {
		t.Fatalf("blank draft structure validation error = %v", err)
	}
	visual := ChartVisualization(Visual{Type: "line"})
	command := reducerCommandFor(Command{ID: "command-1", DashboardID: "blank", DraftID: "draft-1", ExpectedRevision: current.Token(), Provenance: contractProvenance()}, &UpsertVisualPayload{VisualID: "first", Visual: visual})
	next, revision := applyReducer(t, lifecycle, current, command)
	if len(revision.Document.Pages) != 1 || len(revision.Document.Pages[0].Visuals) != 0 {
		t.Fatalf("adding first visual unexpectedly changed blank page: %#v", revision.Document.Pages)
	}
	if _, err := revision.Document.Clone(); err != nil {
		t.Fatalf("incomplete draft could not be cloned: %v", err)
	}
	if err := revision.Document.ValidateContract(); err == nil {
		t.Fatal("semantically incomplete visual unexpectedly passed strict validation")
	}
	command = reducerCommandFor(Command{ID: "command-2", DashboardID: "blank", DraftID: "draft-1", ExpectedRevision: revision.Token(), Provenance: contractProvenance()}, &RemoveVisualPayload{VisualID: "first"})
	next, revision = applyReducer(t, next, revision, command)
	if len(revision.Document.Visuals) != 0 {
		t.Fatalf("last visual was not removed: %#v", revision.Document.Visuals)
	}
	if len(revision.Document.Pages) != 1 || revision.Document.Pages[0].ID != "overview" {
		t.Fatalf("removing last visual changed blank page: %#v", revision.Document.Pages)
	}
}

func TestApplyEditFiltersAndRemovals(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	page := &RemovePagePayload{PageID: "overview"}
	command := reducerCommand(current, page)
	if _, _, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("remove last page error = %v", err)
	}
	filters := reducerCommand(current, &SetFiltersPayload{Clear: true})
	_, revision := applyReducer(t, lifecycle, current, filters)
	if len(revision.Document.FilterDefinitions) != 0 || revision.Document.FilterApplication.Mode != "immediate" {
		t.Fatalf("filters not cleared/defaulted: %#v", revision.Document)
	}
	replacement := reducerCommand(current, &SetFiltersPayload{
		Definitions: map[string]dashboardfilter.Definition{"region": {Label: "Region", Field: "region", Predicates: []dashboardfilter.PredicatePolicy{{Kind: dashboardfilter.ExpressionSet, Operators: []dashboardfilter.Operator{dashboardfilter.OperatorIn}}}}},
		Application: &dashboardfilter.ApplicationPolicy{Mode: dashboardfilter.ApplicationDeferred},
	})
	_, revision = applyReducer(t, lifecycle, current, replacement)
	if revision.Document.FilterDefinitions["region"].Label != "Region" || revision.Document.FilterApplication.Mode != dashboardfilter.ApplicationDeferred {
		t.Fatalf("filter replacement not applied: %#v", revision.Document)
	}
	remove := reducerCommand(current, &RemoveVisualPayload{VisualID: "revenue"})
	if _, _, err := ApplyEdit(lifecycle, current, remove, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)); !errors.Is(err, ErrConflict) {
		t.Fatalf("dangling visual remove error = %v", err)
	}
}

func TestApplyEditSuccessfulRemovals(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	page := dashboardmodel.Page{ID: "temporary", Title: "Temporary", Canvas: dashboardmodel.PageCanvas{Width: 1200, Height: 800}, Grid: dashboardmodel.PageGrid{Columns: 12, RowHeight: 40, Gap: 8, Padding: 8}}
	next, revision := applyReducer(t, lifecycle, current, reducerCommand(current, &UpsertPagePayload{Page: page}))
	next, revision = applyReducer(t, next, revision, reducerCommand(revision, &RemovePagePayload{PageID: "temporary"}))
	if len(revision.Document.Pages) != 1 || revision.Document.Pages[0].ID != "overview" {
		t.Fatalf("page removal not applied: %#v", revision.Document.Pages)
	}
	visual := ChartVisualization(Visual{Title: "Temporary", Type: "line", Query: VisualQuery{Dimensions: []FieldRef{{Field: "month", Alias: "month"}}, Measures: []FieldRef{{Field: "revenue", Alias: "revenue"}}}})
	next, revision = applyReducer(t, next, revision, reducerCommand(revision, &UpsertVisualPayload{VisualID: "temporary", Visual: visual}))
	_, revision = applyReducer(t, next, revision, reducerCommand(revision, &RemoveVisualPayload{VisualID: "temporary"}))
	if _, exists := revision.Document.Visuals["temporary"]; exists {
		t.Fatal("visual removal not applied")
	}
}

func TestApplyEditStaleHashAndPublishedLifecycle(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	stale := reducerCommand(current, &MetadataPatch{Description: stringPtr("stale")})
	stale.ExpectedRevision.Number++
	if _, _, err := ApplyEdit(lifecycle, current, stale, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale command error = %v", err)
	}
	command := reducerCommand(current, &MetadataPatch{Description: stringPtr("next")})
	command.ContentHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if _, _, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)); !errors.Is(err, ErrConflict) {
		t.Fatalf("result hash assertion error = %v", err)
	}
	next, revision := applyReducer(t, lifecycle, current, reducerCommand(current, &MetadataPatch{Description: stringPtr("next")}))
	next.Status = LifecycleStatusPublished
	next.Published = &Published{Revision: current.Token(), Compilation: CompiledRevisionToken{AuthoredRevision: current.Token(), DefinitionHash: "sha256:1111111111111111111111111111111111111111111111111111111111111111", SemanticServingStateID: "state-1"}, PublishedAt: time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC), Provenance: contractProvenance()}
	next, revision = applyReducer(t, next, revision, reducerCommand(revision, &MetadataPatch{Description: stringPtr("published draft")}))
	if next.Status != LifecycleStatusPublished || next.Published == nil || next.Draft.Revision != revision.Token() {
		t.Fatalf("published lifecycle was not retained with new draft: %#v", next)
	}
}

func TestApplyEditDoesNotAliasInputsAndIsDeterministic(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	command := reducerCommand(current, &SetInteractionPayload{VisualID: "revenue", Interaction: &Interaction{PointSelection: SelectionInteraction{Mappings: []SelectionMapping{{Field: "orders.id", Value: "id"}}, Targets: []string{"orders"}}}})
	lifecycleBefore := lifecycle
	currentBefore := current
	commandBefore := command
	before := current.Document.Visuals["revenue"].Chart.Interaction
	nextOne, revisionOne := applyReducer(t, lifecycle, current, command)
	nextTwo, revisionTwo := applyReducer(t, lifecycle, current, command)
	if !reflect.DeepEqual(nextOne, nextTwo) || !reflect.DeepEqual(revisionOne, revisionTwo) {
		t.Fatal("same explicit inputs produced different results")
	}
	if !reflect.DeepEqual(current.Document.Visuals["revenue"].Chart.Interaction, before) {
		t.Fatal("current revision was mutated")
	}
	if revisionOne.Document.Visuals["revenue"].Chart.Interaction.PointSelection.Targets[0] != "orders" {
		t.Fatal("result aliases command payload")
	}
	// Mutating every returned object must leave lifecycle, revision, and the
	// nested command payload untouched.
	nextOne.Draft.Provenance.Source.Metadata["channel"] = "result"
	revisionOne.Document.Pages[0].Visuals[0].Badges = append(revisionOne.Document.Pages[0].Visuals[0].Badges, "result")
	if !reflect.DeepEqual(lifecycle, lifecycleBefore) || !reflect.DeepEqual(current, currentBefore) || !reflect.DeepEqual(command, commandBefore) {
		t.Fatal("result aliases lifecycle, revision, or command inputs")
	}
	command.SetInteraction.Interaction.PointSelection.Targets[0] = "mutated-command"
	if revisionOne.Document.Visuals["revenue"].Chart.Interaction.PointSelection.Targets[0] != "orders" {
		t.Fatal("result aliases command payload after command mutation")
	}
}

func visibilityPtr(value Visibility) *Visibility { return &value }
