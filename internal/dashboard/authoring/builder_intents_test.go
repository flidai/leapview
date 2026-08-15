package authoring

import (
	"errors"
	"reflect"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
)

func intentCommand(current Revision, payload authoringPayload) Command {
	command := Command{ID: "intent-1", DashboardID: current.DashboardID, DraftID: "draft-1", ExpectedRevision: current.Token(), Provenance: contractProvenance()}
	switch value := payload.(type) {
	case *SetVisibilityPayload:
		command.SetVisibility = value
	case *AddPagePayload:
		command.AddPage = value
	case *AddVisualPayload:
		command.AddVisual = value
	case *AssignFieldPayload:
		command.AssignField = value
	}
	return command
}

func TestBuilderIntentUnionIsClosedAndVisibilityIsTransactional(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	command := intentCommand(current, &SetVisibilityPayload{Visibility: VisibilityOrganization})
	if !command.IsBuilderIntent() {
		t.Fatal("visibility command is not a builder intent")
	}
	next, revision, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if next.Visibility != VisibilityOrganization || revision.Document.Title != current.Document.Title {
		t.Fatalf("visibility result = lifecycle=%#v revision=%#v", next, revision)
	}
	metadata := intentCommand(current, &MetadataPatch{Title: stringPtr("not an intent")})
	if metadata.IsBuilderIntent() {
		t.Fatal("document metadata was accepted as builder intent")
	}
}

func TestBuilderIntentAddPageAndVisualAreDeterministicAndAtomic(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	pageCommand := intentCommand(current, &AddPagePayload{})
	next, revision, err := ApplyEdit(lifecycle, current, pageCommand, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := revision.Document.Pages[len(revision.Document.Pages)-1]; got.ID != "page-2" || got.Title != "Page 2" || got.Canvas.Width != 1366 || got.Grid.Columns != 12 {
		t.Fatalf("default page = %#v", got)
	}
	visualCommand := intentCommand(revision, &AddVisualPayload{PageID: "overview", Type: "bar"})
	_, revision, err = ApplyEdit(next, revision, visualCommand, "rev-3", 3, time.Date(2026, 8, 15, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	visual, ok := revision.Document.Visuals["visual_3"]
	if !ok || visual.Chart == nil || visual.Chart.Type != "bar" {
		t.Fatalf("default visual = %#v", visual)
	}
	if got := revision.Document.Pages[0].Visuals[len(revision.Document.Pages[0].Visuals)-1].ID; got != "visual_3_tile" {
		t.Fatalf("default visual component ID = %q", got)
	}
	if got := revision.Document.Pages[0].Visuals[len(revision.Document.Pages[0].Visuals)-1].Placement; got.Col != 1 || got.Row != 5 {
		t.Fatalf("visual placement = %#v, expected next non-overlapping cell", got)
	}
	before := revision
	duplicate := intentCommand(revision, &AddVisualPayload{PageID: "overview", VisualID: "revenue", Type: "line"})
	if _, _, err := ApplyEdit(revisionLifecycle(revision), revision, duplicate, "rev-4", 4, time.Date(2026, 8, 15, 15, 0, 0, 0, time.UTC)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate visual error = %v", err)
	}
	if !reflect.DeepEqual(before, revision) {
		t.Fatal("failed duplicate visual mutated current revision")
	}
}

func TestBuilderIntentRejectsNonCanonicalProvidedVisualIdentifiers(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	for _, test := range []struct {
		name      string
		visual    string
		component string
	}{
		{name: "visual", visual: "foo-bar", component: "foo_bar"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := intentCommand(current, &AddVisualPayload{PageID: "overview", VisualID: test.visual, ComponentID: test.component, Type: "bar"})
			if _, _, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC)); !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("non-canonical identifier error = %v", err)
			}
		})
	}
}

func TestBuilderIntentPreservesCanonicalProvidedVisualIdentifiers(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	command := intentCommand(current, &AddVisualPayload{PageID: "overview", VisualID: "custom_visual", ComponentID: "custom_component", Type: "bar"})
	_, revision, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := revision.Document.Visuals["custom_visual"]; !ok {
		t.Fatal("canonical caller visual ID was rewritten or dropped")
	}
	components := revision.Document.Pages[0].Visuals
	if got := components[len(components)-1].ID; got != "custom_component" {
		t.Fatalf("canonical caller component ID = %q", got)
	}
}

func revisionLifecycle(revision Revision) DashboardLifecycle {
	return DashboardLifecycle{ProjectID: "project-1", ID: revision.DashboardID, OwnerPrincipalID: "principal-1", Slug: "sales", Title: revision.Document.Title, SemanticModel: revision.Document.SemanticModel, Visibility: VisibilityPrivate, Status: LifecycleStatusDraft, Draft: &Draft{ID: "draft-1", DashboardID: revision.DashboardID, Revision: revision.Token(), Provenance: revision.Provenance}}
}

func TestAssignFieldTargetsExactComponentAndTypedSlots(t *testing.T) {
	lifecycle, current := reducerFixture(t)
	// Place the existing definition a second time on the same page. The
	// component identity, not the shared definition ID, selects the target.
	document := current.Document
	document.Pages[0].Visuals = append(document.Pages[0].Visuals, dashboardmodel.PageVisual{ID: "revenue-copy", Kind: "visual", Visual: "revenue"})
	current, err := NewRevision("rev-copy", current.DashboardID, 2, time.Date(2026, 8, 15, 12, 30, 0, 0, time.UTC), document, current.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = current.Token()
	command := intentCommand(current, &AssignFieldPayload{PageID: "overview", VisualID: "revenue-copy", FieldID: "orders.region", Role: FieldRoleDimension})
	_, revision, err := ApplyEdit(lifecycle, current, command, "rev-next", 3, time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got := revision.Document.Visuals["revenue"].Chart.Query.Dimensions; len(got) != 2 || got[1].Field != "orders.region" {
		t.Fatalf("assigned field = %#v", got)
	}
}

func TestValidGovernedFieldIDIsCanonical(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: "order_count", want: true},
		{value: "orders.status", want: true},
		{value: "1orders.status", want: false},
		{value: ".status", want: false},
		{value: "orders.", want: false},
		{value: "orders..status", want: false},
		{value: "a.b.c", want: false},
		{value: "orders-status", want: false},
		{value: "orders:status", want: false},
		{value: "SUM(order_count)", want: false},
	} {
		t.Run(test.value, func(t *testing.T) {
			if got := ValidGovernedFieldID(test.value); got != test.want {
				t.Fatalf("ValidGovernedFieldID(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
