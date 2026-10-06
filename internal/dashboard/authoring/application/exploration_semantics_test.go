package application_test

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/exploration"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
)

func TestAppendExplorationPreservesFilterPredicatesThroughCanonicalCompilation(t *testing.T) {
	for _, operator := range []string{"greater_than", "greater_than_or_equal", "less_than", "less_than_or_equal", "equals", "not_equals", "is_null", "is_not_null"} {
		t.Run(operator, func(t *testing.T) {
			model := explorationAppendModel()
			model.Dimensions["amount"] = semanticmodel.SemanticDimension{Type: "number", Datatype: semanticmodel.DataTypeDecimal, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.amount"}}}
			app, repo, _, _, initial := newExplorationAppendApplicationForModel(t, nil, model)
			target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID})
			if err != nil {
				t.Fatal(err)
			}
			expression := exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: operator, Value: exploration.ExplorationFilterValue{Value: &exploration.DecimalExplorationFilterValue{Kind: "decimal", Value: "10.25"}}}}
			if strings.HasPrefix(operator, "is_") {
				expression.Value = &exploration.NullCheckExplorationFilterExpression{Kind: "null_check", Operator: operator}
			}
			spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{{Field: "amount", Expression: expression}}, Sort: []exploration.ExplorationSort{}, Limit: 100}
			result, err := app.AppendExploration(t.Context(), explorationAppendRequest(target, initial.ID, spec))
			if err != nil {
				t.Fatalf("append must pass canonical dashboard compilation: %v", err)
			}
			stored := repo.revisions[result.Revision.RevisionID].Document
			if len(stored.Spec.Filters) != 1 || repo.appendCalls != 1 {
				t.Fatalf("filters/appends = %d/%d", len(stored.Spec.Filters), repo.appendCalls)
			}
			filter := stored.Spec.Filters[0]
			wantKind := "range"
			if operator == "equals" || operator == "not_equals" {
				wantKind = "comparison"
			} else if strings.HasPrefix(operator, "is_") {
				wantKind = "nullCheck"
			}
			if kind, err := filter.Default.Type(); err != nil || kind != wantKind {
				t.Fatalf("predicate kind = %q (%v), want %q", kind, err, wantKind)
			}
			switch value := filter.Default.Value.(type) {
			case *document.RangeDashboardFilterExpression:
				bound := value.Lower
				if strings.HasPrefix(operator, "less_") {
					bound = value.Upper
					if value.Lower != nil {
						t.Fatal("less-than comparison gained a lower bound")
					}
				} else if value.Upper != nil {
					t.Fatal("greater-than comparison gained an upper bound")
				}
				if bound == nil || bound.Inclusive != strings.HasSuffix(operator, "or_equal") || bound.Value.Value.(*document.DecimalDashboardFilterValue).Value != "10.25" {
					t.Fatalf("comparison bound = %#v", bound)
				}
				if _, ok := filter.Control.Value.(*document.NumericRangeDashboardFilterControl); !ok || filter.Operators != nil {
					t.Fatal("range control has incompatible operators")
				}
			case *document.ComparisonDashboardFilterExpression:
				want := document.DashboardFilterOperatorEquals
				if operator == "not_equals" {
					want = document.DashboardFilterOperatorNotEquals
				}
				if value.Operator != want || value.Value.Value.(*document.DecimalDashboardFilterValue).Value != "10.25" {
					t.Fatalf("typed comparison = %#v", value)
				}
				if _, ok := filter.Control.Value.(*document.TextDashboardFilterControl); !ok {
					t.Fatal("typed equality requires an editable value control")
				}
			case *document.NullCheckDashboardFilterExpression:
				want := document.DashboardFilterOperatorIsNull
				if operator == "is_not_null" {
					want = document.DashboardFilterOperatorIsNotNull
				}
				if value.Operator != want {
					t.Fatalf("null predicate = %#v", value)
				}
			default:
				t.Fatalf("unexpected converted predicate %T", value)
			}
		})
	}
}

func TestAppendExplorationRelativeTimeRangeCompiles(t *testing.T) {
	app, repo, _, _, initial := newExplorationAppendApplication(t, nil)
	target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID})
	if err != nil {
		t.Fatal(err)
	}
	spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Dimensions: []exploration.ExplorationDimensionRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Limit: 100,
		Time: &exploration.ExplorationTimeSelection{Field: "purchase_date", Grain: exploration.ExplorationTimeGrainMonth, Range: &exploration.ExplorationTimeRange{Value: &exploration.RelativeExplorationTimeRange{Kind: "relative", Direction: exploration.ExplorationRelativeDirectionPrevious, Count: 3, Unit: exploration.ExplorationRelativeUnitMonth, IncludeCurrent: true, Anchor: exploration.ExplorationRelativeAnchorCurrentTime}}},
	}
	result, err := app.AppendExploration(t.Context(), explorationAppendRequest(target, initial.ID, spec))
	if err != nil {
		t.Fatalf("relative time range failed canonical compilation: %v", err)
	}
	filter := repo.revisions[result.Revision.RevisionID].Document.Spec.Filters[0]
	if _, ok := filter.Control.Value.(*document.RelativePeriodDashboardFilterControl); !ok {
		t.Fatalf("relative range control = %T", filter.Control.Value)
	}
	value, ok := filter.Default.Value.(*document.RelativePeriodDashboardFilterExpression)
	if !ok || value.Count != 3 || value.Direction != document.DashboardRelativeDirectionPrevious || value.Unit != document.DashboardRelativeUnitMonth || !value.IncludeCurrent || value.Anchor != document.DashboardRelativeAnchorCurrentTime {
		t.Fatalf("relative range = %#v", filter.Default.Value)
	}
}

func TestAppendExplorationRejectsChangedPhysicalTimeSemanticsBeforeWrite(t *testing.T) {
	for _, semantics := range []struct{ name, timezone, calendar, weekStart string }{
		{"timezone", "America/Los_Angeles", "gregorian", "sunday"},
		{"calendar", "UTC", "iso8601", "monday"},
		{"week start", "UTC", "gregorian", "monday"},
	} {
		for _, source := range []string{"dimension", "time", "filter"} {
			t.Run(semantics.name+"/"+source, func(t *testing.T) {
				model := explorationAppendModel()
				date := model.Dimensions["purchase_date"]
				date.Timezone, date.Calendar, date.WeekStart = semantics.timezone, semantics.calendar, semantics.weekStart
				model.Dimensions["purchase_date"] = date
				app, repo, _, _, initial := newExplorationAppendApplicationForModel(t, nil, model)
				target, err := app.ExplorationTarget(t.Context(), application.ExplorationTargetRequest{ProjectID: "sales", ActorID: "actor", SourceModelID: "semantic-model:sales", DashboardID: initial.ID})
				if err != nil {
					t.Fatal(err)
				}
				spec := exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic-model:sales", Dimensions: []exploration.ExplorationDimensionRef{{Field: "status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100}
				switch source {
				case "dimension":
					grain := exploration.ExplorationTimeGrainWeek
					spec.Dimensions = []exploration.ExplorationDimensionRef{{Field: "orders.purchase_date", Grain: &grain}}
				case "time":
					spec.Time = &exploration.ExplorationTimeSelection{Field: "orders.purchase_date", Grain: exploration.ExplorationTimeGrainWeek}
				case "filter":
					spec.Filters = []exploration.ExplorationFilter{{Field: "orders.purchase_date", Expression: exploration.ExplorationFilterExpression{Value: &exploration.RelativePeriodExplorationFilterExpression{Kind: "relative_period", Direction: exploration.ExplorationRelativeDirectionPrevious, Count: 1, Unit: exploration.ExplorationRelativeUnitWeek, Anchor: exploration.ExplorationRelativeAnchorCurrentTime}}}}
				}
				_, err = app.AppendExploration(t.Context(), explorationAppendRequest(target, initial.ID, spec))
				if err == nil || !strings.Contains(err.Error(), "cannot preserve UTC/gregorian/sunday") {
					t.Fatalf("temporal mismatch error = %v", err)
				}
				if repo.appendCalls != 0 {
					t.Fatal("changed time semantics reached repository write")
				}
			})
		}
	}
}
