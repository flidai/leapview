package querymap

import (
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestFieldsPreserveAliasesAndLegacyNilEmptySemantics(t *testing.T) {
	if got := Fields(nil); got == nil || len(got) != 0 {
		t.Fatalf("Fields(nil) = %#v, want non-nil empty", got)
	}
	empty := []QueryField{}
	if got := Fields(empty); got == nil || len(got) != 0 {
		t.Fatalf("Fields(empty) = %#v, want non-nil empty", got)
	}
	got := Fields([]QueryField{{Field: "orders.created_at", Alias: "created"}, {Field: "orders.total", Alias: "revenue"}})
	want := []dataquery.Field{{Field: "orders.created_at", Alias: "created"}, {Field: "orders.total", Alias: "revenue"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields() = %#v, want %#v", got, want)
	}
}

func TestFiltersMapRecursiveGroupsAndLegacyNilEmptySemantics(t *testing.T) {
	input := []QueryFilter{{
		Field: "orders.state", Dataset: "orders", Operator: "in", Values: []any{"CA", "NY"},
		Groups: []QueryFilterGroup{{Filters: []QueryFilter{{Field: "orders.total", Operator: "gt", Values: []any{100}}}}},
	}}
	got := Filters(input)
	want := []dataquery.Filter{{
		Field: "orders.state", Dataset: "orders", Operator: "in", Values: []any{"CA", "NY"},
		Groups: []dataquery.FilterGroup{{Filters: []dataquery.Filter{{Field: "orders.total", Operator: "gt", Values: []any{100}, Groups: []dataquery.FilterGroup{}}}}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filters() = %#v, want %#v", got, want)
	}
	input[0].Values[0] = "mutated"
	input[0].Groups[0].Filters[0].Values[0] = 999
	if got[0].Values[0] != "CA" || got[0].Groups[0].Filters[0].Values[0] != 100 {
		t.Fatal("Filters did not copy mutable value slices")
	}
	if got := Filters(nil); got == nil || len(got) != 0 {
		t.Fatalf("Filters(nil) = %#v, want non-nil empty", got)
	}
	empty := []QueryFilter{}
	if got := Filters(empty); got == nil || len(got) != 0 {
		t.Fatalf("Filters(empty) = %#v, want non-nil empty", got)
	}
}

func TestFiltersWithSpatialPreservesSpatialRuntimeContract(t *testing.T) {
	input := []QueryFilter{{Spatial: &SpatialFilter{
		Kind: "bbox", LatitudeField: "lat", LongitudeField: "lon", Dataset: "orders",
		West: -124, South: 32, East: -114, North: 42,
		Points: []SpatialPoint{{Longitude: -122, Latitude: 37}},
	}}}
	got := FiltersWithSpatial(input)
	if got[0].Spatial == nil || got[0].Spatial.Points[0].Longitude != -122 {
		t.Fatalf("FiltersWithSpatial() dropped spatial predicate: %#v", got)
	}
}

func TestSortsPreserveOrderAndDirection(t *testing.T) {
	if got := Sorts(nil); got == nil || len(got) != 0 {
		t.Fatalf("Sorts(nil) = %#v, want non-nil empty", got)
	}
	got := Sorts([]QuerySort{{Field: "created", Direction: "desc"}, {Field: "revenue", Direction: "asc"}})
	want := []dataquery.Sort{{Field: "created", Direction: "desc"}, {Field: "revenue", Direction: "asc"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sorts() = %#v, want %#v", got, want)
	}
}
