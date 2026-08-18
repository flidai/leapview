package authoring

import (
	"strings"
	"testing"
)

func TestValidateConditionalFormattingAcceptsClosedGovernedPolicies(t *testing.T) {
	t.Parallel()

	visual := Visual{
		Type:  "column",
		Query: VisualQuery{Metrics: []FieldRef{{Field: "orders.revenue", Alias: "revenue"}}},
		Presentation: VisualPresentation{ConditionalFormatting: []VisualConditionalFormat{
			{
				ID: "revenue-gradient", Target: "mark_fill", Field: "value", Kind: "gradient",
				Minimum: numberPointer(0), Maximum: numberPointer(100),
				Low: VisualConditionalStyle{Color: "danger"}, High: VisualConditionalStyle{Color: "success"},
				Null: VisualConditionalStyle{Color: "neutral"},
			},
			{
				ID: "revenue-rules", Target: "icon", Field: "value", Kind: "rules",
				Rules: []VisualConditionalRule{
					{Operator: "less_than", Value: 50, Style: VisualConditionalStyle{Color: "danger", Icon: "arrow_down"}},
					{Operator: "greater_or_equal", Value: 50, Style: VisualConditionalStyle{Color: "success", Icon: "arrow_up"}},
				},
				Null:    VisualConditionalStyle{Icon: "warning"},
				Default: VisualConditionalStyle{Color: "neutral", Icon: "circle"},
			},
			{
				ID: "status-values", Target: "label_foreground", Field: "label", Kind: "field", SourceField: "series",
				Values: map[string]VisualConditionalStyle{
					"late": {Color: "danger", Icon: "warning"},
				},
				Null:    VisualConditionalStyle{Icon: "warning"},
				Default: VisualConditionalStyle{Color: "ink", Icon: "circle"},
			},
		}},
	}

	if err := validateVisualPresentation("revenue", visual); err != nil {
		t.Fatalf("validateVisualPresentation() error = %v", err)
	}
}

func TestValidateConditionalFormattingRejectsAmbiguousOrUnsafePolicies(t *testing.T) {
	t.Parallel()

	validGradient := VisualConditionalFormat{
		ID: "revenue", Target: "mark_fill", Field: "value", Kind: "gradient",
		Minimum: numberPointer(0), Maximum: numberPointer(100),
		Low: VisualConditionalStyle{Color: "danger"}, High: VisualConditionalStyle{Color: "success"},
		Null: VisualConditionalStyle{Color: "neutral"},
	}
	tests := []struct {
		name   string
		format VisualConditionalFormat
		want   string
	}{
		{name: "missing identity", format: func() VisualConditionalFormat { value := validGradient; value.ID = ""; return value }(), want: "requires id"},
		{name: "raw color", format: func() VisualConditionalFormat { value := validGradient; value.High.Color = "#00ff00"; return value }(), want: `unsupported color intent "#00ff00"`},
		{name: "inverted domain", format: func() VisualConditionalFormat {
			value := validGradient
			value.Minimum, value.Maximum = numberPointer(100), numberPointer(0)
			return value
		}(), want: "minimum must be less than maximum"},
		{name: "rules without default", format: VisualConditionalFormat{
			ID: "rules", Target: "mark_fill", Field: "value", Kind: "rules",
			Rules: []VisualConditionalRule{{Operator: "less_than", Value: 0, Style: VisualConditionalStyle{Color: "danger", Icon: "arrow_down"}}},
			Null:  VisualConditionalStyle{Icon: "warning"},
		}, want: "requires default style"},
		{name: "color rule without cue", format: VisualConditionalFormat{
			ID: "rules", Target: "mark_fill", Field: "value", Kind: "rules",
			Rules: []VisualConditionalRule{{Operator: "less_than", Value: 0, Style: VisualConditionalStyle{Color: "danger"}}},
			Null:  VisualConditionalStyle{Icon: "warning"}, Default: VisualConditionalStyle{Icon: "circle"},
		}, want: "requires a redundant icon cue"},
		{name: "field without source", format: VisualConditionalFormat{
			ID: "field", Target: "label_foreground", Field: "value", Kind: "field",
			Values: map[string]VisualConditionalStyle{"late": {Color: "danger", Icon: "warning"}},
			Null:   VisualConditionalStyle{Icon: "warning"}, Default: VisualConditionalStyle{Icon: "circle"},
		}, want: "requires source_field"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visual := Visual{Type: "column", Presentation: VisualPresentation{ConditionalFormatting: []VisualConditionalFormat{test.format}}}
			err := validateVisualPresentation("revenue", visual)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateVisualPresentation() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
