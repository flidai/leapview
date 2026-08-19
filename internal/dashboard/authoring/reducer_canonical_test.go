package authoring

import (
	"strconv"
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
}

func canonicalReducerCommandWithPayload(command Command, payload authoringPayload) Command {
	switch value := payload.(type) {
	case *MetadataPatch:
		command.Metadata = value
	case *AddPagePayload:
		command.AddPage = value
	case *AddVisualPayload:
		command.AddVisual = value
	case *AssignFieldPayload:
		command.AssignField = value
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
