package authoring

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalAddVisualSupportsEveryVisualTypeWithNonOverlappingPlacement(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	types := []document.DashboardVisualType{
		document.DashboardVisualTypeLine, document.DashboardVisualTypeArea, document.DashboardVisualTypeBar, document.DashboardVisualTypeColumn,
		document.DashboardVisualTypePie, document.DashboardVisualTypeDonut, document.DashboardVisualTypeScatter, document.DashboardVisualTypeFunnel,
		document.DashboardVisualTypeTreemap, document.DashboardVisualTypeGauge, document.DashboardVisualTypeHeatmap, document.DashboardVisualTypeSankey,
		document.DashboardVisualTypeGraph, document.DashboardVisualTypeMap, document.DashboardVisualTypeCandlestick, document.DashboardVisualTypeBoxplot,
		document.DashboardVisualTypeCombo, document.DashboardVisualTypeWaterfall, document.DashboardVisualTypeHistogram, document.DashboardVisualTypeRadar,
		document.DashboardVisualTypeTree, document.DashboardVisualTypeSunburst, document.DashboardVisualTypeKpi, document.DashboardVisualTypeTable,
		document.DashboardVisualTypeMatrix, document.DashboardVisualTypePivot,
	}
	expected := map[document.DashboardVisualType]struct{ query, presentation string }{
		document.DashboardVisualTypeHistogram: {"histogram", "cartesian"},
		document.DashboardVisualTypeBoxplot:   {"distribution", "cartesian"},
		document.DashboardVisualTypeTable:     {"records", "table"},
		document.DashboardVisualTypeMatrix:    {"pivot", "table"},
		document.DashboardVisualTypePivot:     {"pivot", "table"},
		document.DashboardVisualTypePie:       {"aggregate", "proportional"},
		document.DashboardVisualTypeDonut:     {"aggregate", "proportional"},
		document.DashboardVisualTypeFunnel:    {"aggregate", "proportional"},
		document.DashboardVisualTypeTreemap:   {"aggregate", "hierarchy"},
		document.DashboardVisualTypeSankey:    {"aggregate", "hierarchy"},
		document.DashboardVisualTypeGraph:     {"aggregate", "hierarchy"},
		document.DashboardVisualTypeTree:      {"aggregate", "hierarchy"},
		document.DashboardVisualTypeSunburst:  {"aggregate", "hierarchy"},
		document.DashboardVisualTypeGauge:     {"aggregate", "polar"},
		document.DashboardVisualTypeRadar:     {"aggregate", "polar"},
		document.DashboardVisualTypeMap:       {"records", "geographic"},
		document.DashboardVisualTypeKpi:       {"aggregate", "kpi"},
	}
	for _, typ := range types {
		if _, ok := expected[typ]; !ok {
			expected[typ] = struct{ query, presentation string }{"aggregate", "cartesian"}
		}
	}
	for index, typ := range types {
		command := Command{ID: CommandID("add-" + string(typ)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(), AddVisual: &AddVisualPayload{PageID: "overview", Type: string(typ)}}
		var next Revision
		var err error
		lifecycle, next, err = ApplyEdit(lifecycle, current, command, RevisionID("rev-"+string(rune('a'+index))), current.Number+1, time.Date(2026, 8, 18, 12, index, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("add %s: %v", typ, err)
		}
		visualID := "visual_" + strconv.Itoa(index+2)
		visual, ok := next.Document.Spec.Visuals[visualID]
		if !ok || visual.Type != typ || visual.Query.Value == nil || visual.Presentation.Value == nil {
			t.Fatalf("add %s produced %#v", typ, visual)
		}
		queryType, err := visual.Query.Type()
		if err != nil {
			t.Fatal(err)
		}
		presentationType, err := visual.Presentation.Type()
		if err != nil {
			t.Fatal(err)
		}
		want := expected[typ]
		if queryType != want.query || presentationType != want.presentation {
			t.Fatalf("add %s query/presentation = %s/%s, want %s/%s", typ, queryType, presentationType, want.query, want.presentation)
		}
		page := next.Document.Spec.Pages[0]
		added := page.Components[len(page.Components)-1]
		placement, err := added.Base()
		if err != nil {
			t.Fatalf("add %s placement: %v", typ, err)
		}
		for _, component := range page.Components[:len(page.Components)-1] {
			other, err := component.Base()
			if err != nil {
				t.Fatal(err)
			}
			if placementsOverlap(placement.Placement, other.Placement) {
				t.Fatalf("add %s overlaps %q", typ, other.ID)
			}
		}
		current = next
	}
}

func TestCanonicalAddVisualBindsGovernedInitialFieldAtomically(t *testing.T) {
	t.Run("metric KPI", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-metric-kpi", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "kpi", Title: "Revenue", FieldID: "revenue", Role: FieldRoleMetric, FieldValidated: true},
		}
		_, next, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		visual := next.Document.Spec.Visuals["visual_2"]
		query, ok := visual.Query.Value.(*document.AggregateDashboardQuery)
		if !ok || visual.Type != document.DashboardVisualTypeKpi || len(query.Metrics) != 1 || query.Metrics[0].String == nil || *query.Metrics[0].String != "revenue" {
			t.Fatalf("initial KPI = %#v query=%#v", visual, query)
		}
		component, err := next.Document.Spec.Pages[0].Components[1].Base()
		if err != nil || component.Placement.ColumnSpan != 4 || component.Placement.RowSpan != 3 {
			t.Fatalf("initial KPI placement = %#v err=%v", component, err)
		}
	})

	t.Run("dimension table", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-dimension-table", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "table", Title: "Status", FieldID: "status", Role: FieldRoleDetail, ResolvedTable: "orders", FieldValidated: true},
		}
		_, next, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		query, ok := next.Document.Spec.Visuals["visual_2"].Query.Value.(*document.RecordsDashboardQuery)
		if !ok || query.Dataset != "orders" || len(query.Fields) != 1 || query.Fields[0].String == nil || *query.Fields[0].String != "status" {
			t.Fatalf("initial table query = %#v", query)
		}
		component, err := next.Document.Spec.Pages[0].Components[1].Base()
		if err != nil || component.Placement.ColumnSpan != 6 || component.Placement.RowSpan != 5 {
			t.Fatalf("initial table placement = %#v err=%v", component, err)
		}
	})

	t.Run("rejects unvalidated wire field", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		command := Command{
			ID: "add-unvalidated", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
			AddVisual: &AddVisualPayload{PageID: "overview", Type: "kpi", FieldID: "revenue", Role: FieldRoleMetric},
		}
		if _, _, err := ApplyEdit(lifecycle, current, command, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "governed validation") {
			t.Fatalf("unvalidated initial field error = %v", err)
		}
	})
}

func canonicalReducerFixture(t *testing.T) (DashboardLifecycle, Revision) {
	t.Helper()
	baseComponent := document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{
			ID: "base-component", Type: "visual",
			Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 4},
		},
		Type: "visual", Visual: "base",
	}}
	documentValue := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard:test", Name: "test"},
		Spec: document.DashboardSpec{
			SemanticModel: "model",
			Filters:       []document.DashboardFilter{},
			Visuals:       map[string]document.DashboardVisual{"base": defaultCanonicalVisual("bar", "Base")},
			Pages:         []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{baseComponent}}},
		},
	}
	provenance := canonicalReducerProvenance()
	revision, err := NewRevision("rev-1", "dashboard:test", 1, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), documentValue, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewDashboardLifecycle(NewDashboardLifecycleInput{ProjectID: "project:test", ID: "dashboard:test", OwnerPrincipalID: "owner", Slug: "test", Title: "Test", SemanticModel: "model", Visibility: VisibilityPrivate, Draft: &Draft{ID: "draft-1", DashboardID: "dashboard:test", Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle, revision
}

func TestApplyRevisionRestoreAppendsMonotonicRevisionFromExactTarget(t *testing.T) {
	lifecycle, target := canonicalReducerFixture(t)
	edit := Command{
		ID: "add-page", DashboardID: target.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: target.Token(), Provenance: canonicalReducerProvenance(),
		AddPage: &AddPagePayload{PageID: "details", Title: "Details"},
	}
	lifecycle, current, err := ApplyEdit(lifecycle, target, edit, "rev-2", 2, time.Date(2026, 8, 18, 12, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	restore := Command{
		ID: "restore-page", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
		ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		RestoreRevision: &RestoreRevisionPayload{TargetRevision: target.Token()},
	}
	nextLifecycle, restored, err := ApplyRevisionRestore(lifecycle, current, target, restore, "rev-3", 3, time.Date(2026, 8, 18, 12, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Number != 3 || len(restored.Document.Spec.Pages) != 1 || restored.Document.Spec.Pages[0].ID != "overview" {
		t.Fatalf("restored revision = number %d pages %#v", restored.Number, restored.Document.Spec.Pages)
	}
	if !sameRevisionToken(nextLifecycle.Draft.Revision, restored.Token()) {
		t.Fatalf("draft token = %#v, want %#v", nextLifecycle.Draft.Revision, restored.Token())
	}
	if restored.ContentHash != target.ContentHash {
		t.Fatalf("restored content hash = %q, want %q", restored.ContentHash, target.ContentHash)
	}
}

func canonicalReducerProvenance() Provenance {
	return Provenance{Origin: OriginUI, ActorID: "actor", ConversationID: "conversation", ToolCallID: "tool"}
}

func TestCanonicalReducerAppliesMetadataPageVisualLayoutFiltersInteractionAndFieldAssignment(t *testing.T) {
	apply := func(t *testing.T, lifecycle DashboardLifecycle, current Revision, payload authoringPayload) (DashboardLifecycle, Revision, error) {
		t.Helper()
		command := Command{ID: CommandID("command-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		return ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 13, int(current.Number), 0, 0, time.UTC))
	}
	t.Run("metadata and page", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		title, description, slug := "Updated", "Description", "updated"
		lifecycle, current, err := apply(t, lifecycle, current, &MetadataPatch{Title: &title, Description: &description, Slug: &slug})
		if err != nil || lifecycle.Title != title || current.Document.Metadata.DisplayName == nil || *current.Document.Metadata.DisplayName != title || current.Document.Metadata.Description == nil || *current.Document.Metadata.Description != description {
			t.Fatalf("metadata edit = %#v %#v (%v)", lifecycle, current.Document.Metadata, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AddPagePayload{PageID: "details", Title: "Details"})
		if err != nil || len(current.Document.Spec.Pages) != 2 || current.Document.Spec.Pages[1].ID != "details" {
			t.Fatalf("page edit = %#v (%v)", current.Document.Spec.Pages, err)
		}
	})
	t.Run("visual layout filters interaction and assign field", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		var err error
		lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "revenue", Type: "bar", Title: "Revenue"})
		if err != nil || current.Document.Spec.Visuals["revenue"].Type != document.DashboardVisualTypeBar {
			t.Fatalf("visual edit = %#v (%v)", current.Document.Spec.Visuals, err)
		}
		columns := int32(16)
		lifecycle, current, err = apply(t, lifecycle, current, &SetLayoutPayload{PageID: "overview", Layout: &document.DashboardLayoutOverride{Columns: &columns}})
		if err != nil || current.Document.Spec.Pages[0].Layout == nil || *current.Document.Spec.Pages[0].Layout.Columns != columns {
			t.Fatalf("layout edit = %#v (%v)", current.Document.Spec.Pages[0].Layout, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &SetFiltersPayload{Clear: true})
		if err != nil || current.Document.Spec.Filters == nil || len(current.Document.Spec.Filters) != 0 {
			t.Fatalf("filter clear = %#v (%v)", current.Document.Spec.Filters, err)
		}
		targets := []string{"base"}
		interaction := &document.DashboardInteraction{Value: &document.SelectionDashboardInteraction{Type: "selection", Mode: document.DashboardSelectionModeSingle, Toggle: true, Mappings: []document.DashboardInteractionMapping{{Field: "status", Value: "label"}}, DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection", Targets: &targets}}}
		lifecycle, current, err = apply(t, lifecycle, current, &SetInteractionPayload{PageID: "overview", VisualID: "base", Interaction: interaction})
		if err != nil || current.Document.Spec.Visuals["base"].Interactions == nil || len(*current.Document.Spec.Visuals["base"].Interactions) != 1 {
			t.Fatalf("interaction edit = %#v (%v)", current.Document.Spec.Visuals["base"].Interactions, err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric})
		if err != nil {
			t.Fatalf("assign field error = %v", err)
		}
		query, ok := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
		if !ok || len(query.Metrics) != 1 || query.Metrics[0].String == nil || *query.Metrics[0].String != "revenue" {
			t.Fatalf("assigned field query = %#v", current.Document.Spec.Visuals["base"].Query)
		}
		if lifecycle.Draft.Revision != current.Token() {
			t.Fatalf("lifecycle draft pointer = %#v, revision = %#v", lifecycle.Draft.Revision, current.Token())
		}
	})
	t.Run("qualified detail field resolves dataset and stores canonical member", func(t *testing.T) {
		lifecycle, current := canonicalReducerFixture(t)
		var err error
		lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "records", ComponentID: "records-component", Type: "table", Title: "Records"})
		if err != nil {
			t.Fatalf("add records visual: %v", err)
		}
		lifecycle, current, err = apply(t, lifecycle, current, &AssignFieldPayload{PageID: "overview", VisualID: "records-component", FieldID: "sales_orders.order_id", Role: FieldRoleDetail, ResolvedTable: "sales_orders"})
		if err != nil {
			t.Fatalf("assign qualified detail field: %v", err)
		}
		query, ok := current.Document.Spec.Visuals["records"].Query.Value.(*document.RecordsDashboardQuery)
		if !ok || query.Dataset != "sales_orders" || len(query.Fields) != 1 || query.Fields[0].String == nil || *query.Fields[0].String != "order_id" {
			t.Fatalf("assigned records query = %#v", current.Document.Spec.Visuals["records"].Query)
		}
	})
}

func TestCanonicalReducerAtomicallyUpdatesComponentPlacements(t *testing.T) {
	apply := func(t *testing.T, lifecycle DashboardLifecycle, current Revision, payload authoringPayload) (DashboardLifecycle, Revision, error) {
		t.Helper()
		command := Command{ID: CommandID("placement-command-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		return ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("placement-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 14, int(current.Number), 0, 0, time.UTC))
	}
	lifecycle, current := canonicalReducerFixture(t)
	var err error
	lifecycle, current, err = apply(t, lifecycle, current, &AddVisualPayload{PageID: "overview", VisualID: "secondary", ComponentID: "secondary-component", Type: "bar", Title: "Secondary"})
	if err != nil {
		t.Fatal(err)
	}
	placements := &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{
		{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 6, RowSpan: 4}},
		{ComponentID: "secondary-component", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}},
	}}
	lifecycle, current, err = apply(t, lifecycle, current, placements)
	if err != nil {
		t.Fatalf("atomic placement update: %v", err)
	}
	for _, component := range current.Document.Spec.Pages[0].Components {
		base, baseErr := component.Base()
		if baseErr != nil {
			t.Fatal(baseErr)
		}
		switch base.ID {
		case "base-component":
			if base.Placement.Column != 1 || base.Placement.ColumnSpan != 6 {
				t.Fatalf("base placement = %#v", base.Placement)
			}
		case "secondary-component":
			if base.Placement.Column != 7 || base.Placement.ColumnSpan != 6 {
				t.Fatalf("secondary placement = %#v", base.Placement)
			}
		}
	}
	before := current
	_, _, err = apply(t, lifecycle, current, &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{
		{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 7, RowSpan: 4}},
		{ComponentID: "secondary-component", Placement: document.DashboardPlacement{Column: 7, Row: 1, ColumnSpan: 6, RowSpan: 4}},
	}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("overlapping placement error = %v, want conflict", err)
	}
	if got, _ := before.Document.Spec.Pages[0].Components[0].Base(); got.Placement.ColumnSpan != 6 {
		t.Fatalf("failed atomic update mutated base placement = %#v", got.Placement)
	}
	_, _, err = apply(t, lifecycle, current, &SetPlacementsPayload{PageID: "overview", Placements: []PlacementUpdate{{ComponentID: "base-component", Placement: document.DashboardPlacement{Column: 13, Row: 1, ColumnSpan: 1, RowSpan: 1}}}})
	if err == nil || !strings.Contains(err.Error(), "exceed grid") {
		t.Fatalf("out-of-grid placement error = %v", err)
	}
}

func TestCanonicalReducerSelectedVisualEditingCommands(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("selected-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("selected-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 15, int(current.Number), 0, 0, time.UTC))
		return err
	}
	if err := apply(&AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeColumn}); err != nil {
		t.Fatal(err)
	}
	if cartesian, ok := current.Document.Spec.Visuals["base"].Presentation.Value.(*document.CartesianDashboardPresentation); !ok || cartesian.Orientation == nil || *cartesian.Orientation != document.DashboardOrientationVertical {
		t.Fatalf("column orientation = %#v", current.Document.Spec.Visuals["base"].Presentation)
	}
	if err := apply(&SetVisualTypePayload{PageID: "overview", VisualID: "base-component", Type: document.DashboardVisualTypeLine}); err != nil {
		t.Fatal(err)
	}
	if got := current.Document.Spec.Visuals["base"].Type; got != document.DashboardVisualTypeLine {
		t.Fatalf("visual type = %q", got)
	}
	if query, ok := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery); !ok || len(query.Metrics) != 1 {
		t.Fatalf("compatible metric was not preserved: %#v", current.Document.Spec.Visuals["base"].Query)
	}
	if err := apply(&RenameVisualPayload{PageID: "overview", VisualID: "base-component", Title: "Revenue trend"}); err != nil {
		t.Fatal(err)
	}
	showTitle, showLegend, showAxes, showLabels := false, false, false, false
	if err := apply(&UpdateVisualFormatPayload{PageID: "overview", VisualID: "base-component", TitleVisible: &showTitle, LegendVisible: &showLegend, AxisVisible: &showAxes, DataLabelsVisible: &showLabels}); err != nil {
		t.Fatal(err)
	}
	base := current.Document.Spec.Visuals["base"]
	if base.Title == nil || *base.Title != "Revenue trend" || base.TitleVisible == nil || *base.TitleVisible {
		t.Fatalf("title format = %#v", base)
	}
	presentation, ok := base.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Legend == nil || *presentation.Legend != document.DashboardLegendPositionNone || presentation.Labels == nil || presentation.Labels.Density != document.DashboardLabelDensityHidden {
		t.Fatalf("visual format = %#v", base.Presentation)
	}
	if basePresentation, _ := base.Presentation.Base(); basePresentation == nil || basePresentation.AxisVisible == nil || *basePresentation.AxisVisible {
		t.Fatalf("axis format = %#v", base.Presentation)
	}
	stacking := "percent"
	if err := apply(&UpdateVisualFormatPayload{PageID: "overview", VisualID: "base-component", FormatKey: "stacking", FormatValue: &stacking}); err != nil {
		t.Fatal(err)
	}
	base = current.Document.Spec.Visuals["base"]
	presentation, ok = base.Presentation.Value.(*document.CartesianDashboardPresentation)
	if !ok || presentation.Stacking == nil || *presentation.Stacking != document.DashboardStackingModePercent {
		t.Fatalf("contract format = %#v", base.Presentation)
	}
	if err := apply(&DuplicateVisualPayload{PageID: "overview", VisualID: "base-component"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Visuals) != 2 || len(current.Document.Spec.Pages[0].Components) != 2 {
		t.Fatalf("duplicate counts = %d/%d", len(current.Document.Spec.Visuals), len(current.Document.Spec.Pages[0].Components))
	}
	if err := apply(&RemoveVisualPayload{PageID: "overview", VisualID: "base-component"}); err != nil {
		t.Fatal(err)
	}
	if len(current.Document.Spec.Pages[0].Components) != 1 || len(current.Document.Spec.Visuals) != 1 {
		t.Fatalf("remove counts = %d/%d", len(current.Document.Spec.Visuals), len(current.Document.Spec.Pages[0].Components))
	}
}

func TestCanonicalReducerRemovesAndMovesSelectedFields(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	apply := func(payload authoringPayload) error {
		command := Command{ID: CommandID("field-edit-" + strconv.FormatUint(current.Number, 10)), DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance()}
		var err error
		lifecycle, current, err = ApplyEdit(lifecycle, current, canonicalReducerCommandWithPayload(command, payload), RevisionID("field-rev-"+strconv.FormatUint(current.Number+1, 10)), current.Number+1, time.Date(2026, 8, 18, 16, int(current.Number), 0, 0, time.UTC))
		return err
	}
	for _, field := range []string{"revenue", "orders"} {
		if err := apply(&AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: field, Role: FieldRoleMetric}); err != nil {
			t.Fatal(err)
		}
	}
	if err := apply(&MoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "revenue", Role: FieldRoleMetric, Direction: "down"}); err != nil {
		t.Fatal(err)
	}
	query := current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	first, _ := canonicalMetricSelection(query.Metrics[0])
	if first != "orders" {
		t.Fatalf("moved metric order = %#v", query.Metrics)
	}
	if err := apply(&RemoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: "orders", Role: FieldRoleMetric}); err != nil {
		t.Fatal(err)
	}
	query = current.Document.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	if len(query.Metrics) != 1 {
		t.Fatalf("removed metric count = %d", len(query.Metrics))
	}
}

func canonicalReducerCommandWithPayload(command Command, payload authoringPayload) Command {
	switch value := payload.(type) {
	case *MetadataPatch:
		command.Metadata = value
	case *AddPagePayload:
		command.AddPage = value
	case *AddVisualPayload:
		command.AddVisual = value
	case *SetPlacementsPayload:
		command.SetPlacements = value
	case *AssignFieldPayload:
		command.AssignField = value
	case *SetVisualTypePayload:
		command.SetVisualType = value
	case *RenameVisualPayload:
		command.RenameVisual = value
	case *DuplicateVisualPayload:
		command.DuplicateVisual = value
	case *UpdateVisualFormatPayload:
		command.UpdateVisualFormat = value
	case *RemoveFieldPayload:
		command.RemoveField = value
	case *MoveFieldPayload:
		command.MoveField = value
	case *RemoveVisualPayload:
		command.RemoveVisual = value
	case *SetLayoutPayload:
		command.SetLayout = value
	case *SetFiltersPayload:
		command.SetFilters = value
	case *SetInteractionPayload:
		command.SetInteraction = value
	default:
		panic("unsupported canonical reducer test payload")
	}
	return command
}
