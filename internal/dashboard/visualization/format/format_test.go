package format

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
)

type fixture struct {
	Locale   string                 `json:"locale"`
	Format   ir.VisualizationFormat `json:"format"`
	Value    any                    `json:"value"`
	Expected string                 `json:"expected"`
}

func TestSharedFormattingFixtures(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../../api/visualization/conformance/formatting.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Expected, func(t *testing.T) {
			got, err := Value(fixture.Locale, fixture.Format, fixture.Value)
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if got != fixture.Expected {
				t.Fatalf("Value = %q, want %q", got, fixture.Expected)
			}
		})
	}
}

func TestFormattingFailsClosed(t *testing.T) {
	t.Parallel()
	if _, err := Value("de-DE", ir.VisualizationFormat{Value: &ir.NumberVisualizationFormat{Kind: "number"}}, 1); err == nil {
		t.Fatal("expected unsupported locale to fail")
	}
	if _, err := Value("en-US", ir.VisualizationFormat{Value: &ir.CurrencyVisualizationFormat{Kind: "currency", Currency: "XYZ"}}, 1); err == nil {
		t.Fatal("expected unsupported currency to fail")
	}
}

func TestDateOnlyTemporalValueCannotSatisfyTimeOnlyFormat(t *testing.T) {
	t.Parallel()
	_, err := Value("en-US", ir.VisualizationFormat{Value: &ir.TemporalVisualizationFormat{Kind: "temporal", TimeStyle: ptr("medium")}}, "2026-07-19")
	if err == nil {
		t.Fatal("expected date-only value with time-only format to fail")
	}
}

func TestPartialFractionDigitsPreserveFormatDefaults(t *testing.T) {
	t.Parallel()
	minimum, maximum := int32(1), int32(6)
	tests := []struct {
		name     string
		format   ir.VisualizationFormat
		value    any
		expected string
	}{
		{
			name:   "number maximum preserves default minimum",
			format: ir.VisualizationFormat{Value: &ir.NumberVisualizationFormat{Kind: "number", MaximumFractionDigits: ptr(maximum)}},
			value:  1.2345678, expected: "1.234568",
		},
		{
			name:   "number minimum preserves default maximum",
			format: ir.VisualizationFormat{Value: &ir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: ptr(minimum)}},
			value:  1.23456, expected: "1.235",
		},
		{
			name:   "currency zero maximum adapts minimum",
			format: ir.VisualizationFormat{Value: &ir.CurrencyVisualizationFormat{Kind: "currency", Currency: "USD", MaximumFractionDigits: ptr(int32(0))}},
			value:  252.24, expected: "$252",
		},
		{
			name:   "currency minimum above default adapts maximum",
			format: ir.VisualizationFormat{Value: &ir.CurrencyVisualizationFormat{Kind: "currency", Currency: "USD", MinimumFractionDigits: ptr(int32(3))}},
			value:  2, expected: "$2.000",
		},
		{
			name:   "percent zero maximum preserves default minimum",
			format: ir.VisualizationFormat{Value: &ir.PercentVisualizationFormat{Kind: "percent", MaximumFractionDigits: ptr(int32(0))}},
			value:  0.125, expected: "13%",
		},
		{
			name:   "percent minimum above default adapts maximum",
			format: ir.VisualizationFormat{Value: &ir.PercentVisualizationFormat{Kind: "percent", MinimumFractionDigits: ptr(int32(2))}},
			value:  0.125, expected: "12.50%",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Value("en-US", test.format, test.value)
			if err != nil {
				t.Fatalf("Value: %v", err)
			}
			if got != test.expected {
				t.Fatalf("Value = %q, want %q", got, test.expected)
			}
		})
	}
}

func TestPartialFractionDigitsDoNotChangeCompactDefaults(t *testing.T) {
	t.Parallel()
	format := ir.VisualizationFormat{Value: &ir.CompactVisualizationFormat{Kind: "compact", MaximumFractionDigits: ptr(int32(2))}}
	got, err := Value("en-US", format, 1234)
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got != "1.23K" {
		t.Fatalf("Value = %q, want %q", got, "1.23K")
	}
}

func TestExplicitInvalidFractionDigitsRemainRejected(t *testing.T) {
	t.Parallel()
	minimum, maximum := int32(2), int32(1)
	formats := []ir.VisualizationFormat{
		{Value: &ir.NumberVisualizationFormat{Kind: "number", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}},
		{Value: &ir.CurrencyVisualizationFormat{Kind: "currency", Currency: "USD", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}},
		{Value: &ir.PercentVisualizationFormat{Kind: "percent", MinimumFractionDigits: &minimum, MaximumFractionDigits: &maximum}},
	}
	for _, format := range formats {
		if _, err := Value("en-US", format, 1); err == nil {
			t.Fatal("expected invalid explicit fraction digit pair to fail")
		}
	}
}

func ptr[T any](value T) *T { return &value }
