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
