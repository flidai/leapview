package application

import (
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/compiler"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestFilterScopeIntentRetainsOnlyCompatibleConsumers(t *testing.T) {
	lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, false, false)
	headroom := "base_headroom"
	cash := revision.Document.Spec.Visuals["monthly-performance"]
	cash.Query = document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{{String: &headroom}}}}
	revision.Document.Spec.Visuals["cash"] = cash
	revision.Document.Spec.Pages[0].Components = append(revision.Document.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "cash-card", Placement: document.DashboardPlacement{Column: 1, Row: 8, ColumnSpan: 4, RowSpan: 3}}, Type: "visual", Visual: "cash"}})
	var err error
	revision, err = authoring.NewRevision(revision.ID, revision.DashboardID, revision.Number, revision.CreatedAt, revision.Document, revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = revision.Token()
	command.AssignField = nil
	for _, scope := range []string{"page", "report", "page"} {
		patch := &authoring.SetFilterScopePayload{FilterID: "finance_date", Scope: scope}
		if scope == "page" {
			patch.PageID = "overview"
		}
		if err := setCompatibleFilterScopeTargets(revision.Document, model, patch); err != nil {
			t.Fatal(err)
		}
		want := []string{"monthly-performance"}
		if scope == "page" {
			want = []string{"monthly-component"}
		}
		if !reflect.DeepEqual(patch.Targets, want) {
			t.Fatalf("%s targets = %v, want %v", scope, patch.Targets, want)
		}
		command.SetFilterScope = patch
		command.ID = authoring.CommandID("filter-scope-" + strconv.FormatUint(revision.Number, 10))
		command.ExpectedRevision = revision.Token()
		lifecycle, revision, err = authoring.ApplyEdit(lifecycle, revision, command, authoring.RevisionID(command.ID), revision.Number+1, revision.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err != nil {
			t.Fatalf("%s scope broke preview: %v", scope, err)
		}
	}
	for _, test := range []struct {
		name    string
		targets []string
		page    string
	}{
		{"incompatible visual", []string{"cash-card"}, "overview"},
		{"unknown visual", []string{"missing"}, "overview"},
		{"unknown page", nil, "missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			patch := &authoring.SetFilterScopePayload{FilterID: "finance_date", Scope: "page", PageID: test.page, Targets: test.targets}
			if err := setCompatibleFilterScopeTargets(revision.Document, model, patch); err == nil {
				t.Fatal("invalid filter scope accepted")
			}
		})
	}
	patch := &authoring.SetFilterScopePayload{FilterID: "missing", Scope: "report"}
	if err := setCompatibleFilterScopeTargets(revision.Document, model, patch); !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("missing filter error = %v", err)
	}
}

func TestFilterScopeValidationAllowsRepairingInvalidFiltersIndividually(t *testing.T) {
	lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, false, true)
	headroom := "base_headroom"
	cash := revision.Document.Spec.Visuals["monthly-performance"]
	cash.Query = document.DashboardQuery{Value: &document.AggregateDashboardQuery{Type: "aggregate", Dimensions: []document.DashboardDimensionSelection{}, Metrics: []document.DashboardMetricSelection{{String: &headroom}}}}
	revision.Document.Spec.Visuals["cash"] = cash
	// Restore the finance visual to a valid single-dataset query. Both filters
	// incorrectly include the separate cash visual, as older scope edits did.
	sales := "net_sales"
	finance := revision.Document.Spec.Visuals["monthly-performance"]
	finance.Query.Value.(*document.AggregateDashboardQuery).Metrics = []document.DashboardMetricSelection{{String: &sales}}
	revision.Document.Spec.Visuals["monthly-performance"] = finance
	revision.Document.Spec.Pages[0].Components = append(revision.Document.Spec.Pages[0].Components, document.DashboardPageComponent{Value: &document.VisualDashboardPageComponent{DashboardPageComponentBase: document.DashboardPageComponentBase{ID: "cash-card", Placement: document.DashboardPlacement{Column: 1, Row: 8, ColumnSpan: 4, RowSpan: 3}}, Type: "visual", Visual: "cash"}})
	revision.Document.Spec.Filters[0].Targets = nil
	second := revision.Document.Spec.Filters[0]
	second.ID = "second_date"
	revision.Document.Spec.Filters = append(revision.Document.Spec.Filters, second)
	var err error
	revision, err = authoring.NewRevision(revision.ID, revision.DashboardID, revision.Number, revision.CreatedAt, revision.Document, revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Draft.Revision = revision.Token()
	command.AssignField = nil
	for index, id := range []string{"finance_date", "second_date"} {
		if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err == nil {
			t.Fatal("expected another invalid filter before repair")
		}
		command.ID = authoring.CommandID("repair-" + id)
		command.ExpectedRevision = revision.Token()
		command.SetFilterScope = &authoring.SetFilterScopePayload{FilterID: id, Scope: "report"}
		if err := setCompatibleFilterScopeTargets(revision.Document, model, command.SetFilterScope); err != nil {
			t.Fatal(err)
		}
		if err := validateFilterScopeCompatibility(lifecycle, revision, command, model); err != nil {
			t.Fatalf("repair %d blocked by another invalid filter: %v", index+1, err)
		}
		lifecycle, revision, err = authoring.ApplyEdit(lifecycle, revision, command, authoring.RevisionID(command.ID), revision.Number+1, revision.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := compiler.CompileCanonicalDashboardBuilderFilters(revision.Document, model); err != nil {
		t.Fatalf("repaired draft remains invalid: %v", err)
	}
}

func TestFilterScopeIntentPreservesWholeScopeAndExplicitVisualTargets(t *testing.T) {
	for _, scope := range []string{"report", "page"} {
		t.Run(scope, func(t *testing.T) {
			lifecycle, revision, command, model := assignmentFilterCompatibilityFixture(t, false, false)
			command.AssignField = nil
			patch := &authoring.SetFilterScopePayload{FilterID: "finance_date", Scope: scope}
			if scope == "page" {
				patch.PageID = "overview"
			}
			if err := setCompatibleFilterScopeTargets(revision.Document, model, patch); err != nil {
				t.Fatal(err)
			}
			if patch.Targets != nil {
				t.Fatalf("whole %s scope became an explicit subset: %v", scope, patch.Targets)
			}
			command.SetFilterScope = patch
			if err := validateFilterScopeCompatibility(lifecycle, revision, command, model); err != nil {
				t.Fatal(err)
			}
			// Choosing one visual remains explicit, even on a page with only one chart.
			if scope == "page" {
				patch.Targets = []string{"monthly-component"}
				if err := setCompatibleFilterScopeTargets(revision.Document, model, patch); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(patch.Targets, []string{"monthly-component"}) {
					t.Fatalf("explicit visual scope widened: %v", patch.Targets)
				}
			}
		})
	}
}
