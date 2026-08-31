package authoring

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalPageCommandsRenameDuplicateMoveAndLayout(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	commandNumber := 0
	apply := func(payload authoringPayload) error {
		commandNumber++
		command := Command{
			ID:               CommandID("page-command-" + strconv.Itoa(commandNumber)),
			DashboardID:      current.DashboardID,
			DraftID:          lifecycle.Draft.ID,
			ExpectedRevision: current.Token(),
			Provenance:       canonicalReducerProvenance(),
		}
		command = canonicalReducerCommandWithPayload(command, payload)
		nextLifecycle, nextRevision, err := ApplyEdit(lifecycle, current, command, RevisionID("page-revision-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 19, 12, int(current.Number), 0, 0, time.UTC))
		if err == nil {
			lifecycle, current = nextLifecycle, nextRevision
		}
		return err
	}

	if err := apply(&RenamePagePayload{PageID: "overview", Title: "Revenue overview"}); err != nil {
		t.Fatal(err)
	}
	if current.Document.Spec.Pages[0].Title != "Revenue overview" {
		t.Fatalf("renamed page title = %q", current.Document.Spec.Pages[0].Title)
	}
	if err := apply(&AddPagePayload{PageID: "details", Title: "Details"}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&DuplicatePagePayload{PageID: "overview", NewPageID: "overview-copy", Title: "Overview copy"}); err != nil {
		t.Fatal(err)
	}
	pages := current.Document.Spec.Pages
	if got := []string{pages[0].ID, pages[1].ID, pages[2].ID}; got[1] != "overview-copy" {
		t.Fatalf("duplicate page order = %#v", got)
	}
	if len(current.Document.Spec.Visuals) != 2 {
		t.Fatalf("visual definitions after duplicate = %d", len(current.Document.Spec.Visuals))
	}
	cloneComponent, ok := pages[1].Components[0].Value.(*document.VisualDashboardPageComponent)
	if !ok || cloneComponent.Visual == "base" {
		t.Fatalf("duplicate visual component = %#v", pages[1].Components[0])
	}
	clone := current.Document.Spec.Visuals[cloneComponent.Visual]
	if clone.Title == nil || *clone.Title != "Base" {
		t.Fatalf("cloned visual title = %#v", clone.Title)
	}
	if source := current.Document.Spec.Visuals["base"]; source.Title == nil || *source.Title != "Base" || source.Title == clone.Title {
		t.Fatalf("source visual was not deeply cloned = %#v", source.Title)
	}

	if err := apply(&MovePagePayload{PageID: "details", Index: 0}); err != nil {
		t.Fatal(err)
	}
	if got := []string{current.Document.Spec.Pages[0].ID, current.Document.Spec.Pages[1].ID, current.Document.Spec.Pages[2].ID}; strings.Join(got, ",") != "details,overview,overview-copy" {
		t.Fatalf("moved page order = %#v", got)
	}
	if err := apply(&UpdatePageLayoutPayload{PageID: "overview", Columns: 12, RowHeight: 36, Gap: 8, Padding: 4}); err != nil {
		t.Fatal(err)
	}
	layout := current.Document.Spec.Pages[1].Layout
	if layout == nil || layout.Columns == nil || *layout.Columns != 12 || layout.RowHeight == nil || *layout.RowHeight != 36 || layout.Gap == nil || *layout.Gap != 8 || layout.Padding == nil || *layout.Padding != 4 {
		t.Fatalf("page layout = %#v", layout)
	}
	if err := apply(&UpdatePageLayoutPayload{PageID: "overview", Columns: 6, RowHeight: 36, Gap: 8, Padding: 4}); err == nil || !strings.Contains(err.Error(), "exceed grid") {
		t.Fatalf("narrow layout error = %v", err)
	}
	if *current.Document.Spec.Pages[1].Layout.Columns != 12 {
		t.Fatalf("invalid layout mutated page = %#v", current.Document.Spec.Pages[1].Layout)
	}
}

func TestCanonicalPageCommandsValidateMoveAndLayoutInputs(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	for name, payload := range map[string]authoringPayload{
		"negative index":   &MovePagePayload{PageID: "overview", Index: -1},
		"zero columns":     &UpdatePageLayoutPayload{PageID: "overview", Columns: 0, RowHeight: 1},
		"zero row height":  &UpdatePageLayoutPayload{PageID: "overview", Columns: 1, RowHeight: 0},
		"negative gap":     &UpdatePageLayoutPayload{PageID: "overview", Columns: 1, RowHeight: 1, Gap: -1},
		"negative padding": &UpdatePageLayoutPayload{PageID: "overview", Columns: 1, RowHeight: 1, Padding: -1},
	} {
		t.Run(name, func(t *testing.T) {
			command := canonicalReducerCommandWithPayload(Command{ID: "invalid-page-command", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}, payload)
			if err := command.Validate(); err == nil {
				t.Fatal("invalid page payload was accepted")
			}
		})
	}
}

func TestCanonicalRemovePageRetainsLastPageGuard(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	command := canonicalReducerCommandWithPayload(Command{
		ID: "remove-last-page", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
	}, &RemovePagePayload{PageID: "overview"})
	if _, _, err := ApplyEdit(lifecycle, current, command, "page-revision-2", 2, time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "last page") {
		t.Fatalf("last page removal error = %v", err)
	}
}

func TestCanonicalPageCommandsRejectProjectionBoundsWithoutMutation(t *testing.T) {
	t.Run("add page", func(t *testing.T) {
		_, revision := canonicalReducerFixture(t)
		doc := revision.Document
		doc.Spec.Pages = make([]document.DashboardPage, maxAuthoringPages)
		for index := range doc.Spec.Pages {
			doc.Spec.Pages[index] = document.DashboardPage{ID: "page_" + strconv.Itoa(index+1), Title: "Page", Components: []document.DashboardPageComponent{}}
		}
		before := len(doc.Spec.Pages)
		err := applyCanonicalPayload(&doc, &AddPagePayload{PageID: "new-page"})
		if err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") || len(doc.Spec.Pages) != before {
			t.Fatalf("bounded add page error=%v pages=%d", err, len(doc.Spec.Pages))
		}
	})

	t.Run("duplicate page", func(t *testing.T) {
		_, revision := canonicalReducerFixture(t)
		doc := revision.Document
		doc.Spec.Pages = make([]document.DashboardPage, maxAuthoringPages)
		for index := range doc.Spec.Pages {
			doc.Spec.Pages[index] = document.DashboardPage{ID: "page_" + strconv.Itoa(index+1), Title: "Page", Components: []document.DashboardPageComponent{}}
		}
		before := len(doc.Spec.Pages)
		err := applyCanonicalPayload(&doc, &DuplicatePagePayload{PageID: "page_1"})
		if err == nil || !strings.Contains(err.Error(), "pages exceed bounded limit") || len(doc.Spec.Pages) != before {
			t.Fatalf("bounded duplicate page error=%v pages=%d", err, len(doc.Spec.Pages))
		}
	})

	t.Run("duplicate visual components", func(t *testing.T) {
		_, revision := canonicalReducerFixture(t)
		doc := revision.Document
		page := &doc.Spec.Pages[0]
		for index := 1; index < maxAuthoringVisualComponents; index++ {
			page.Components = append(page.Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
				DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "component_" + strconv.Itoa(index+1), Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: int32(index + 1), ColumnSpan: 1, RowSpan: 1}},
				Type:                       "visual", Visual: "base",
			}})
		}
		beforePages, beforeVisuals := len(doc.Spec.Pages), len(doc.Spec.Visuals)
		err := applyCanonicalPayload(&doc, &DuplicatePagePayload{PageID: "overview"})
		if err == nil || !strings.Contains(err.Error(), "visuals exceed bounded limit") || len(doc.Spec.Pages) != beforePages || len(doc.Spec.Visuals) != beforeVisuals {
			t.Fatalf("bounded duplicate visuals error=%v pages=%d visuals=%d", err, len(doc.Spec.Pages), len(doc.Spec.Visuals))
		}
	})

	t.Run("duplicate filter components", func(t *testing.T) {
		_, revision := canonicalReducerFixture(t)
		doc := revision.Document
		if err := addCanonicalFilter(&doc, AddFilterPayload{FilterID: "status", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
			t.Fatal(err)
		}
		page := &doc.Spec.Pages[0]
		for index := 0; index < maxAuthoringFilterComponents; index++ {
			page.Components = append(page.Components, document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
				DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "filter_component_" + strconv.Itoa(index+1), Type: "filter", Placement: document.DashboardPlacement{Column: 1, Row: int32(index + 1), ColumnSpan: 1, RowSpan: 1}},
				Type:                       "filter", Filter: "status",
			}})
		}
		beforePages, beforeVisuals := len(doc.Spec.Pages), len(doc.Spec.Visuals)
		beforeComponents := len(doc.Spec.Pages[0].Components)
		err := applyCanonicalPayload(&doc, &DuplicatePagePayload{PageID: "overview"})
		if err == nil || !strings.Contains(err.Error(), "filter components exceed bounded limit") || len(doc.Spec.Pages) != beforePages || len(doc.Spec.Visuals) != beforeVisuals || len(doc.Spec.Pages[0].Components) != beforeComponents {
			t.Fatalf("bounded duplicate filters error=%v pages=%d visuals=%d components=%d", err, len(doc.Spec.Pages), len(doc.Spec.Visuals), len(doc.Spec.Pages[0].Components))
		}
	})
}

func TestCanonicalDuplicatePageRetainsPageLocalFilterBindingsAndComponents(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	doc := revision.Document
	if err := addCanonicalFilter(&doc, AddFilterPayload{FilterID: "status", Label: "Status", Dimension: "status", Dataset: "orders", ControlType: "multiSelect"}); err != nil {
		t.Fatal(err)
	}
	targets := []string{"base-component"}
	doc.Spec.Pages[0].FilterBindings = &[]document.DashboardPageFilterBinding{{ID: "status", Filter: "status", Targets: &targets}}
	doc.Spec.Pages[0].Components = append(doc.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.FilterDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "status-slicer", Type: "filter", Placement: document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 3, RowSpan: 2}},
		Type:                       "filter", Filter: "status",
	}})
	if err := applyCanonicalPayload(&doc, &DuplicatePagePayload{PageID: "overview", NewPageID: "overview-copy"}); err != nil {
		t.Fatal(err)
	}
	clone := doc.Spec.Pages[1]
	bindingsValid := false
	if clone.FilterBindings != nil && len(*clone.FilterBindings) == 1 {
		binding := (*clone.FilterBindings)[0]
		bindingsValid = binding.Filter == "status" && binding.Targets != nil && len(*binding.Targets) == 1 && (*binding.Targets)[0] == "base-component"
	}
	if !bindingsValid {
		t.Fatalf("cloned page bindings = %#v", clone.FilterBindings)
	}
	filterComponentFound := false
	for _, component := range clone.Components {
		if filter, ok := component.Value.(*document.FilterDashboardPageComponent); ok && filter.Filter == "status" && filter.ID == "status-slicer" {
			filterComponentFound = true
		}
	}
	if !filterComponentFound {
		t.Fatalf("cloned page filter components = %#v", clone.Components)
	}
}
