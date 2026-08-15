package layoutcontract

import (
	"reflect"
	"testing"
)

func TestRequirementsPreserveExplicitFeaturesAcrossLayouts(t *testing.T) {
	got, err := Requirements(ContractKPI, []Feature{FeatureComparison, FeatureTrend})
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{
		{Layout: "wide", Minimum: Size{Width: 320, Height: 148}},
		{Layout: "stacked", Minimum: Size{Width: 192, Height: 124}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("requirements = %#v, want %#v", got, want)
	}

	resolution, err := Resolve(ContractKPI, Size{Width: 250, Height: 130}, []Feature{FeatureComparison, FeatureTrend})
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Fits || resolution.Layout != "stacked" || resolution.Minimum != (Size{Width: 192, Height: 124}) {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestOuterRequirementsIncludeComponentChrome(t *testing.T) {
	got, err := OuterRequirements(ContractSlicerDateRange, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{
		{Layout: "inline", Minimum: Size{Width: 288, Height: 94}},
		{Layout: "stacked", Minimum: Size{Width: 192, Height: 154}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outer requirements = %#v, want %#v", got, want)
	}
}

func TestExplicitSlicerSummaryAddsOneLine(t *testing.T) {
	got, err := OuterRequirements(ContractSlicerDateRange, []Feature{FeatureSummary})
	if err != nil {
		t.Fatal(err)
	}
	want := []Requirement{
		{Layout: "inline", Minimum: Size{Width: 288, Height: 112}},
		{Layout: "stacked", Minimum: Size{Width: 192, Height: 172}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("outer requirements = %#v, want %#v", got, want)
	}
}

func TestRequirementsFailClosedForUnsupportedExplicitFeature(t *testing.T) {
	if _, err := Requirements(ContractSlicerDateRange, []Feature{FeatureTrend}); err == nil {
		t.Fatal("expected unsupported feature error")
	}
}
