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

func ptr[T any](value T) *T { return &value }
