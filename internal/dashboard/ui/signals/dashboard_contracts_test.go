package signals

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
)

func TestDashboardContractConversionsPreserveJSON(t *testing.T) {
	t.Parallel()

	selections := []dashboard.InteractionSelection{{ID: "visual:orders:point", SourceKind: "visual", SourceID: "orders", InteractionKind: "point", Label: "42", Order: 1, Entries: []dashboard.InteractionSelectionEntry{{Label: "42", Mappings: []dashboard.InteractionSelectionMapping{{Field: "ratings.rating_bucket", Dataset: "ratings", Value: float64(42), Label: "Rating"}}}}}}
	assertSameJSON(t, selections, DashboardInteractionSelectionsFromDashboard(selections))
	spatial := []dashboard.SpatialInteractionSelection{}
	assertSameJSON(t, spatial, DashboardSpatialSelectionsFromDashboard(spatial))
}

func TestDashboardFilterStateUsesEmptyJSONCollections(t *testing.T) {
	t.Parallel()

	contract := DashboardFilterStateFromDomain(dashboardfilter.State{})
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal filter state: %v", err)
	}
	if contract.DirtyBindings == nil {
		t.Fatalf("dirty bindings must be an empty collection: %s", encoded)
	}
	if got := string(encoded); !strings.Contains(got, `"dirtyBindings":[]`) {
		t.Fatalf("filter state = %s, want empty dirtyBindings array", got)
	}
}

func TestDashboardRelativePeriodExpressionPreservesFalseIncludeCurrent(t *testing.T) {
	t.Parallel()

	contract := DashboardFilterExpressionFromDomain(dashboardfilter.Expression{
		Kind:           dashboardfilter.ExpressionRelativePeriod,
		Direction:      dashboardfilter.DirectionPrevious,
		Count:          10,
		Unit:           dashboardfilter.UnitYear,
		IncludeCurrent: false,
		Anchor:         dashboardfilter.AnchorCurrentTime,
	})
	encoded, err := json.Marshal(contract)
	if err != nil {
		t.Fatalf("marshal relative-period expression: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"includeCurrent":false`) {
		t.Fatalf("relative-period expression = %s, want explicit false includeCurrent", got)
	}
}

func assertSameJSON(t *testing.T, left, right any) {
	t.Helper()
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatalf("marshal source: %v", err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(leftJSON, &leftValue); err != nil {
		t.Fatalf("decode source: %v", err)
	}
	if err := json.Unmarshal(rightJSON, &rightValue); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if !reflect.DeepEqual(leftValue, rightValue) {
		t.Fatalf("JSON differs:\nsource:   %s\ncontract: %s", leftJSON, rightJSON)
	}
}
