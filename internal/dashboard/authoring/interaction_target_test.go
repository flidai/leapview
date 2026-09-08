package authoring

import (
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func interactionTargetDocument(t *testing.T) document.DashboardDocument {
	t.Helper()
	_, revision := canonicalReducerFixture(t)
	doc := revision.Document
	doc.Spec.Visuals["target"] = defaultCanonicalVisual("bar", "Target")
	doc.Spec.Pages[0].Components = append(doc.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{
		DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "target-component", Type: "visual", Placement: document.DashboardPlacement{Column: 1, Row: 5, ColumnSpan: 12, RowSpan: 4}},
		Type:                       "visual", Visual: "target",
	}})
	return doc
}

func TestSetCanonicalInteractionTargetPreservesSelectionAndMovesOneTarget(t *testing.T) {
	doc := interactionTargetDocument(t)
	dimension := "status"
	query := doc.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
	query.Dimensions = []document.DashboardDimensionSelection{{String: &dimension}}
	filterTargets := []string{"target", "other"}
	highlightTargets := []string{"highlighted", "target"}
	noneTargets := []string{"none"}
	label := "Status label"
	selection := document.SelectionDashboardInteraction{
		DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection", Targets: &filterTargets},
		Type:                     "selection", Mode: document.DashboardSelectionModeMultiple, Toggle: false,
		Mappings:         []document.DashboardInteractionMapping{{Field: "status", Value: "status", Label: &label}},
		HighlightTargets: &highlightTargets, NoneTargets: &noneTargets,
	}
	doc.Spec.Visuals["base"] = func() document.DashboardVisual {
		visual := doc.Spec.Visuals["base"]
		visual.Interactions = &[]document.DashboardInteraction{{Value: &selection}}
		return visual
	}()
	if err := setCanonicalInteractionTarget(&doc, SetInteractionTargetPayload{PageID: "overview", VisualID: "base-component", TargetVisualID: "target", Effect: "none"}); err != nil {
		t.Fatal(err)
	}
	updated := doc.Spec.Visuals["base"]
	got := (*updated.Interactions)[0].Value.(*document.SelectionDashboardInteraction)
	if got.Mode != selection.Mode || got.Toggle != selection.Toggle || !reflect.DeepEqual(got.Mappings, selection.Mappings) {
		t.Fatalf("selection metadata changed: %#v", got)
	}
	if got.Targets == nil || !reflect.DeepEqual(*got.Targets, []string{"other"}) {
		t.Fatalf("filter targets = %#v", got.Targets)
	}
	if got.HighlightTargets == nil || !reflect.DeepEqual(*got.HighlightTargets, []string{"highlighted"}) {
		t.Fatalf("highlight targets = %#v", got.HighlightTargets)
	}
	if got.NoneTargets == nil || !reflect.DeepEqual(*got.NoneTargets, []string{"none", "target"}) {
		t.Fatalf("none targets = %#v", got.NoneTargets)
	}
}

func TestSetCanonicalInteractionTargetInfersDefaultAndRejectsUnsupportedWithoutClobber(t *testing.T) {
	t.Run("infers aggregate dimension", func(t *testing.T) {
		doc := interactionTargetDocument(t)
		field := "status"
		query := doc.Spec.Visuals["base"].Query.Value.(*document.AggregateDashboardQuery)
		query.Dimensions = []document.DashboardDimensionSelection{{String: &field}}
		if err := setCanonicalInteractionTarget(&doc, SetInteractionTargetPayload{PageID: "overview", VisualID: "base", TargetVisualID: "target-component", Effect: "filter"}); err != nil {
			t.Fatal(err)
		}
		selection := (*doc.Spec.Visuals["base"].Interactions)[0].Value.(*document.SelectionDashboardInteraction)
		if selection.Mode != document.DashboardSelectionModeSingle || !selection.Toggle || len(selection.Mappings) != 1 || selection.Mappings[0].Field != field || selection.Targets == nil || !reflect.DeepEqual(*selection.Targets, []string{"target"}) {
			t.Fatalf("inferred selection = %#v", selection)
		}
	})
	for name, interaction := range map[string]document.DashboardInteraction{
		"multiple": {Value: &document.SelectionDashboardInteraction{DashboardInteractionBase: document.DashboardInteractionBase{Type: "selection"}, Type: "selection", Mode: document.DashboardSelectionModeSingle, Toggle: true, Mappings: []document.DashboardInteractionMapping{}}},
		"spatial":  {Value: &document.SpatialSelectionDashboardInteraction{DashboardInteractionBase: document.DashboardInteractionBase{Type: "spatialSelection"}, Type: "spatialSelection"}},
	} {
		t.Run(name, func(t *testing.T) {
			doc := interactionTargetDocument(t)
			original := doc.Spec.Visuals["base"]
			items := []document.DashboardInteraction{interaction}
			if name == "multiple" {
				items = append(items, interaction)
			}
			original.Interactions = &items
			doc.Spec.Visuals["base"] = original
			err := setCanonicalInteractionTarget(&doc, SetInteractionTargetPayload{PageID: "overview", VisualID: "base", TargetVisualID: "target", Effect: "filter"})
			if err == nil || !strings.Contains(err.Error(), "source visual") {
				t.Fatalf("unsupported interaction error = %v", err)
			}
			if !reflect.DeepEqual(doc.Spec.Visuals["base"], original) {
				t.Fatal("unsupported interaction was clobbered")
			}
		})
	}
}

func TestSetCanonicalInteractionTargetRejectsSelf(t *testing.T) {
	doc := interactionTargetDocument(t)
	err := setCanonicalInteractionTarget(&doc, SetInteractionTargetPayload{PageID: "overview", VisualID: "base", TargetVisualID: "base-component", Effect: "filter"})
	if err == nil || !strings.Contains(err.Error(), "cannot be the same visual") {
		t.Fatalf("self target error = %v", err)
	}
}

func TestSetInteractionTargetCommandValidationIsBoundedBuilderIntent(t *testing.T) {
	_, revision := canonicalReducerFixture(t)
	command := Command{
		ID: "interaction-command", DashboardID: revision.DashboardID, DraftID: "draft-1", ExpectedRevision: revision.Token(),
		Provenance: canonicalReducerProvenance(), SetInteractionTarget: &SetInteractionTargetPayload{PageID: "overview", VisualID: "source", TargetVisualID: "target", Effect: "highlight"},
	}
	if !command.IsBuilderIntent() {
		t.Fatal("set interaction target is not recognized as a builder intent")
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("valid command rejected: %v", err)
	}
	command.SetInteractionTarget.Effect = "explode"
	if err := command.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported interaction target effect") {
		t.Fatalf("invalid effect error = %v", err)
	}
}
