package authoring

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestCanonicalAppendExplorationVisualIsAtomicAndPreservesExactPlacement(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	placement := document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 8, RowSpan: 5}
	targets := []string{"exploration_visual"}
	payload := AppendExplorationVisualPayload{
		PageID: "overview", VisualID: "exploration_visual", ComponentID: "exploration_component", Placement: placement,
		SemanticModel: "model", Visual: defaultCanonicalVisual("line", "Explore"),
		Filters: []document.DashboardFilter{{
			ID: "exploration_filter_1", Label: "Status", Dimension: "status",
			Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}},
			Default: &document.DashboardFilterExpression{Value: &document.UnfilteredDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "unfiltered"}, Type: "unfiltered"}},
			Targets: &targets,
		}},
	}
	command := Command{ID: "append-exploration", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(), AppendExplorationVisual: &payload}
	nextLifecycle, next, err := ApplyEdit(lifecycle, current, command, "rev-append", current.Number+1, time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("append exploration visual: %v", err)
	}
	if _, ok := next.Document.Spec.Visuals[payload.VisualID]; !ok {
		t.Fatalf("appended visual missing: %#v", next.Document.Spec.Visuals)
	}
	if len(next.Document.Spec.Filters) != 1 || next.Document.Spec.Filters[0].ID != payload.Filters[0].ID || next.Document.Spec.Filters[0].Targets == nil || len(*next.Document.Spec.Filters[0].Targets) != 1 || (*next.Document.Spec.Filters[0].Targets)[0] != payload.VisualID {
		t.Fatalf("appended filters = %#v", next.Document.Spec.Filters)
	}
	page := next.Document.Spec.Pages[0]
	component := page.Components[len(page.Components)-1]
	base, err := component.Base()
	if err != nil {
		t.Fatal(err)
	}
	visual, ok := component.Value.(*document.VisualDashboardPageComponent)
	if !ok || visual.Type != "visual" || visual.Visual != payload.VisualID || base.ID != payload.ComponentID || base.Placement != placement {
		t.Fatalf("appended component = %#v base=%#v visual=%#v, want exact target/placement", component, base, visual)
	}
	if len(current.Document.Spec.Visuals) != 1 || len(current.Document.Spec.Pages[0].Components) != 1 || len(current.Document.Spec.Filters) != 0 {
		t.Fatal("append mutated the current revision")
	}
	if nextLifecycle.Draft == nil || nextLifecycle.Draft.Revision != next.Token() {
		t.Fatalf("next lifecycle draft = %#v, revision = %#v", nextLifecycle.Draft, next.Token())
	}
}

func TestCanonicalAppendExplorationVisualRejectsConflictsAndUnsupportedScopes(t *testing.T) {
	tests := map[string]func(*AppendExplorationVisualPayload){
		"visual overwrite":        func(value *AppendExplorationVisualPayload) { value.VisualID = "base" },
		"component overwrite":     func(value *AppendExplorationVisualPayload) { value.ComponentID = "base-component" },
		"semantic model mismatch": func(value *AppendExplorationVisualPayload) { value.SemanticModel = "other-model" },
		"overlapping placement":   func(value *AppendExplorationVisualPayload) { value.Placement.Row = 1 },
		"filter targets another visual": func(value *AppendExplorationVisualPayload) {
			targets := []string{"other_visual"}
			value.Filters[0].Targets = &targets
		},
		"duplicate filter IDs": func(value *AppendExplorationVisualPayload) {
			value.Filters = append(value.Filters, value.Filters[0])
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			lifecycle, current := canonicalReducerFixture(t)
			targets := []string{"exploration_visual"}
			payload := AppendExplorationVisualPayload{
				PageID: "overview", VisualID: "exploration_visual", ComponentID: "exploration_component",
				Placement: document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 8, RowSpan: 5}, SemanticModel: "model",
				Visual:  defaultCanonicalVisual("line", "Explore"),
				Filters: []document.DashboardFilter{{ID: "exploration_filter_1", Label: "Status", Dimension: "status", Control: document.DashboardFilterControl{Value: &document.TextDashboardFilterControl{DashboardFilterControlBase: document.DashboardFilterControlBase{Type: "text"}, Type: "text"}}, Default: &document.DashboardFilterExpression{Value: &document.UnfilteredDashboardFilterExpression{DashboardFilterExpressionBase: document.DashboardFilterExpressionBase{Type: "unfiltered"}, Type: "unfiltered"}}, Targets: &targets}},
			}
			mutate(&payload)
			command := Command{ID: "append-exploration", DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID, ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(), AppendExplorationVisual: &payload}
			if _, _, err := ApplyEdit(lifecycle, current, command, "rev-append", current.Number+1, time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)); err == nil {
				t.Fatal("append unexpectedly succeeded")
			}
		})
	}
}

func TestCandidateAppendExplorationDocumentValidatesBeforePersistence(t *testing.T) {
	_, current := canonicalReducerFixture(t)
	payload := AppendExplorationVisualPayload{
		PageID: "overview", VisualID: "candidate_visual", ComponentID: "candidate_component",
		Placement: document.DashboardPlacement{Column: 1, Row: 6, ColumnSpan: 8, RowSpan: 5}, SemanticModel: "model",
		Visual: defaultCanonicalVisual("line", "Explore"),
	}
	candidate, err := CandidateAppendExplorationDocument(current.Document, payload)
	if err != nil {
		t.Fatalf("candidate document: %v", err)
	}
	if _, ok := candidate.Spec.Visuals[payload.VisualID]; !ok || len(candidate.Spec.Pages[0].Components) != 2 {
		t.Fatalf("candidate append = %#v", candidate)
	}
	if _, err := CandidateAppendExplorationDocument(current.Document, AppendExplorationVisualPayload{PageID: "overview", VisualID: "bad", ComponentID: "bad_component", Placement: payload.Placement, SemanticModel: "model", Visual: payload.Visual, Filters: []document.DashboardFilter{{ID: "filter", Targets: &[]string{"other"}}}}); err == nil || !strings.Contains(err.Error(), "target only") {
		t.Fatalf("invalid candidate error = %v", err)
	}
	if _, err := CandidateAppendExplorationDocument(current.Document, AppendExplorationVisualPayload{PageID: "overview", VisualID: "bad", ComponentID: "bad_component", Placement: payload.Placement, SemanticModel: "model"}); err == nil || !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("missing visual error = %v", err)
	}
}

func TestCanonicalAppendExplorationVisualRejectsPlacementIntegerOverflow(t *testing.T) {
	_, current := canonicalReducerFixture(t)
	payload := AppendExplorationVisualPayload{
		PageID: "overview", VisualID: "overflow_visual", ComponentID: "overflow_component",
		Placement: document.DashboardPlacement{Column: math.MaxInt32, Row: 1, ColumnSpan: 2, RowSpan: 1}, SemanticModel: "model",
		Visual: defaultCanonicalVisual("line", "Explore"),
	}
	if _, err := CandidateAppendExplorationDocument(current.Document, payload); err == nil || !strings.Contains(err.Error(), "coordinate bounds") {
		t.Fatalf("overflow placement error = %v", err)
	}
}
