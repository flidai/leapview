package runtime

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/dashboard"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
)

func TestQueryRuntimeUsesOneVisualizationDataService(t *testing.T) {
	visualizations := &VisualizationDataService{}
	snapshots := &SnapshotService{visualizations: visualizations}
	queries := &QueryService{snapshots: snapshots, visualizations: visualizations}

	if queries.visualizations != snapshots.visualizations {
		t.Fatal("query and snapshot paths must share one visualization data service")
	}
}

func TestFlattenHierarchyRowsBuildsDeterministicNodeParentFrames(t *testing.T) {
	t.Parallel()

	rows := reportdef.QueryRows{
		{"region": "Americas", "city": "Springfield", "value": 3.0},
		{"region": "Europe", "city": "Springfield", "value": 5.0},
		{"region": "Americas", "city": "Austin", "value": 7.0},
	}
	want := []dashboard.Datum{
		{"node": "Americas", "parent": nil, "value": 10.0, "region": "Americas", "city": nil},
		{"node": "Austin", "parent": "Americas", "value": 7.0, "region": "Americas", "city": "Austin"},
		{"node": "Springfield", "parent": "Americas", "value": 3.0, "region": "Americas", "city": "Springfield"},
		{"node": "Europe", "parent": nil, "value": 5.0, "region": "Europe", "city": nil},
		{"node": "Springfield", "parent": "Europe", "value": 5.0, "region": "Europe", "city": "Springfield"},
	}

	got, err := flattenHierarchyRowsTyped(rows, []string{"region", "city"}, "value", false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("flattenHierarchyRows() = %#v, want %#v", got, want)
	}
}

func TestFlattenHierarchyRowsRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		rows reportdef.QueryRows
	}{
		{name: "missing level", rows: reportdef.QueryRows{{"level_0": "Americas", "level_1": nil, "value": 1.0}}},
		{name: "nonnumeric value", rows: reportdef.QueryRows{{"level_0": "Americas", "level_1": "Austin", "value": "many"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := flattenHierarchyRowsTyped(test.rows, []string{"level_0", "level_1"}, "value", false); err == nil {
				t.Fatal("expected invalid hierarchy data to fail")
			}
		})
	}
}

func TestFlattenHierarchyRowsPreservesExactDecimalSums(t *testing.T) {
	rows := reportdef.QueryRows{
		{"region": "Americas", "city": "Austin", "value": "9007199254740993.125"},
		{"region": "Americas", "city": "Austin", "value": "0.875"},
	}
	got, err := flattenHierarchyRowsTyped(rows, []string{"region", "city"}, "value", true)
	if err != nil {
		t.Fatal(err)
	}
	if got[0]["value"] != "9007199254740994.000" {
		t.Fatalf("exact hierarchy sum = %#v, want canonical decimal", got[0]["value"])
	}
}
