package authoring

import (
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

func TestCanonicalReducerKeepsComboSeriesInSyncWithMetricEdits(t *testing.T) {
	lifecycle, current := canonicalReducerFixture(t)
	visual := current.Document.Spec.Visuals["base"]
	visual.Type = document.DashboardVisualTypeCombo
	netRevenue := "net_revenue"
	revenue, orders, margin := "revenue", "orders", "margin"
	visual.Query.Value = &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"},
		Type:               "aggregate",
		Dimensions:         []document.DashboardDimensionSelection{},
		Metrics: []document.DashboardMetricSelection{
			{Reference: &document.DashboardMetricReference{Metric: revenue, Alias: &netRevenue}},
			{String: &orders},
		},
	}
	visual.Presentation.Value = &document.CartesianDashboardPresentation{
		DashboardPresentationBase: document.DashboardPresentationBase{Type: "cartesian"},
		Type:                      "cartesian",
		Series: &[]document.DashboardComboSeries{
			{Field: netRevenue, Mark: document.DashboardComboSeriesMarkColumn, Axis: document.DashboardComboSeriesAxisPrimary},
			{Field: orders, Mark: document.DashboardComboSeriesMarkLine, Axis: document.DashboardComboSeriesAxisSecondary},
		},
	}
	current.Document.Spec.Visuals["base"] = visual
	var err error
	current, err = NewRevision("combo-fields-1", current.DashboardID, current.Number, current.CreatedAt, current.Document, canonicalReducerProvenance())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = current.Token()
	sequence := 0
	apply := func(payload authoringPayload) {
		t.Helper()
		sequence++
		command := Command{
			ID:          CommandID("combo-fields-" + string(rune('a'+sequence))),
			DashboardID: current.DashboardID, DraftID: lifecycle.Draft.ID,
			ExpectedRevision: current.Token(), Provenance: canonicalReducerProvenance(),
		}
		if options, ok := payload.(*SetVisualQueryOptionsPayload); ok {
			command.SetVisualQueryOptions = options
		} else {
			command = canonicalReducerCommandWithPayload(command, payload)
		}
		var next Revision
		var applyErr error
		lifecycle, next, applyErr = ApplyEdit(lifecycle, current, command, RevisionID("combo-fields-rev-"+string(rune('a'+sequence))), current.Number+1, time.Date(2026, 9, 25, 12, 0, sequence, 0, time.UTC))
		if applyErr != nil {
			t.Fatal(applyErr)
		}
		current = next
	}
	series := func() []document.DashboardComboSeries {
		t.Helper()
		value := current.Document.Spec.Visuals["base"].Presentation.Value.(*document.CartesianDashboardPresentation)
		if value.Series == nil {
			t.Fatal("combo presentation series is nil")
		}
		return *value.Series
	}
	fields := func(values []document.DashboardComboSeries) []string {
		t.Helper()
		result := make([]string, len(values))
		for index, item := range values {
			result[index] = item.Field
		}
		return result
	}

	apply(&AssignFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: margin, Role: FieldRoleMetric})
	added := series()
	if got, want := fields(added), []string{netRevenue, orders, margin}; !equalStrings(got, want) {
		t.Fatalf("series fields after metric add = %v, want %v", got, want)
	}
	if added[0].Mark != document.DashboardComboSeriesMarkColumn || added[0].Axis != document.DashboardComboSeriesAxisPrimary || added[1].Mark != document.DashboardComboSeriesMarkLine || added[1].Axis != document.DashboardComboSeriesAxisSecondary {
		t.Fatalf("metric add changed existing combo settings: %#v", added)
	}
	if added[2].Mark != document.DashboardComboSeriesMarkLine || added[2].Axis != document.DashboardComboSeriesAxisPrimary {
		t.Fatalf("new series did not receive safe defaults: %#v", added[2])
	}

	apply(&MoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: margin, Role: FieldRoleMetric, Direction: "up"})
	moved := series()
	if got, want := fields(moved), []string{netRevenue, margin, orders}; !equalStrings(got, want) {
		t.Fatalf("series fields after metric reorder = %v, want %v", got, want)
	}
	if moved[0] != added[0] || moved[1] != added[2] || moved[2] != added[1] {
		t.Fatalf("reorder did not preserve per-series mark/axis settings: before=%#v after=%#v", added, moved)
	}

	apply(&RemoveFieldPayload{PageID: "overview", VisualID: "base-component", FieldID: revenue, Role: FieldRoleMetric})
	removed := series()
	if got, want := fields(removed), []string{margin, orders}; !equalStrings(got, want) {
		t.Fatalf("series fields after metric removal = %v, want %v", got, want)
	}
	if removed[0] != moved[1] || removed[1] != moved[2] {
		t.Fatalf("remove did not preserve remaining per-series settings: before=%#v after=%#v", moved, removed)
	}

	alias := "operating_margin"
	apply(&SetVisualQueryOptionsPayload{PageID: "overview", VisualID: "base-component", FieldID: margin, Role: FieldRoleMetric, Alias: &alias})
	updated := series()
	if got, want := fields(updated), []string{alias, orders}; !equalStrings(got, want) {
		t.Fatalf("series fields after metric alias rename = %v, want %v", got, want)
	}
	if updated[0].Mark != removed[0].Mark || updated[0].Axis != removed[0].Axis || updated[1] != removed[1] {
		t.Fatalf("alias rename did not preserve per-series settings: before=%#v after=%#v", removed, updated)
	}

	query := compiler.LoweredDashboardQuery{
		Type:        "aggregate",
		Binding:     definition.QueryBinding{Aggregate: &definition.AggregateQueryBinding{Metrics: []definition.FieldBinding{{Alias: alias}, {Alias: orders}}}},
		ResultFrame: []compiler.DashboardQueryResultField{{Name: alias}, {Name: orders}},
	}
	if _, err := compiler.LowerCanonicalDashboardPresentationForQuery(current.Document.Spec.Visuals["base"].Presentation, document.DashboardVisualTypeCombo, query); err != nil {
		t.Fatalf("synchronized combo presentation did not compile against its metrics: %v", err)
	}
}

func TestCanonicalComboSyncPreservesImplicitSeriesDefaults(t *testing.T) {
	visual := defaultCanonicalVisual(string(document.DashboardVisualTypeCombo), "Combo")
	metric := "revenue"
	visual.Query.Value = &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"},
		Type:               "aggregate",
		Dimensions:         []document.DashboardDimensionSelection{},
		Metrics:            []document.DashboardMetricSelection{{String: &metric}},
	}
	presentation := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
	presentation.Series = nil
	syncCanonicalComboSeries(&visual)
	if presentation.Series != nil {
		t.Fatalf("automatic combo defaults became explicit: %#v", presentation.Series)
	}
	lowered, err := compiler.LowerCanonicalDashboardPresentation(visual.Presentation, document.DashboardVisualTypeCombo)
	if err != nil {
		t.Fatal(err)
	}
	canonical, ok := lowered.(visualizationir.CartesianVisualizationPresentation)
	if !ok || canonical.ComboSeries != nil {
		t.Fatalf("implicit combo defaults were changed by lowering: %#v", lowered)
	}
}

func TestCanonicalComboSyncContinuesExistingHorizontalBars(t *testing.T) {
	visual := defaultCanonicalVisual(string(document.DashboardVisualTypeCombo), "Combo")
	first, second := "first_metric", "second_metric"
	visual.Query.Value = &document.AggregateDashboardQuery{
		DashboardQueryBase: document.DashboardQueryBase{Type: "aggregate"},
		Type:               "aggregate",
		Dimensions:         []document.DashboardDimensionSelection{},
		Metrics: []document.DashboardMetricSelection{
			{String: &first}, {String: &second},
		},
	}
	presentation := visual.Presentation.Value.(*document.CartesianDashboardPresentation)
	presentation.Series = &[]document.DashboardComboSeries{{
		Field: first, Mark: document.DashboardComboSeriesMarkBar, Axis: document.DashboardComboSeriesAxisSecondary,
	}}
	syncCanonicalComboSeries(&visual)
	if got := *presentation.Series; len(got) != 2 || got[1].Field != second || got[1].Mark != document.DashboardComboSeriesMarkBar || got[1].Axis != document.DashboardComboSeriesAxisPrimary {
		t.Fatalf("new series does not continue horizontal-bar mark with primary-axis default: %#v", got)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
