package filter

import (
	"strings"
	"testing"
)

func TestCanonicalizeSetTypesDeduplicatesAndSortsValues(t *testing.T) {
	expression := Expression{
		Kind:     ExpressionSet,
		Operator: OperatorIn,
		Values: []Value{
			{Kind: ValueInteger, Value: "10"},
			{Kind: ValueInteger, Value: "2"},
			{Kind: ValueInteger, Value: "10"},
		},
	}

	got, err := Canonicalize(expression, ValueInteger)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ExpressionSet || len(got.Values) != 2 {
		t.Fatalf("canonical expression = %#v", got)
	}
	if got.Values[0].Value != "2" || got.Values[1].Value != "10" {
		t.Fatalf("canonical values = %#v, want numeric order 2, 10", got.Values)
	}
}

func TestCanonicalizeEmptyExpressionsAsUnfiltered(t *testing.T) {
	for _, expression := range []Expression{
		{Kind: ExpressionSet, Operator: OperatorIn},
		{Kind: ExpressionRange},
		{},
	} {
		got, err := Canonicalize(expression, ValueString)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != ExpressionUnfiltered {
			t.Fatalf("canonical expression = %#v, want unfiltered", got)
		}
	}
}

func TestCanonicalizeRejectsValueTypeMismatch(t *testing.T) {
	_, err := Canonicalize(Expression{
		Kind:     ExpressionComparison,
		Operator: OperatorEquals,
		Value:    &Value{Kind: ValueBoolean, Value: true},
	}, ValueString)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("Canonicalize() error = %v", err)
	}
}

func TestBindingKeyIsStableAndScopeSensitive(t *testing.T) {
	first := BindingKey("sales", ScopePage, "overview", "state")
	second := BindingKey("sales", ScopePage, "overview", "state")
	report := BindingKey("sales", ScopeReport, "", "state")
	otherPage := BindingKey("sales", ScopePage, "operations", "state")

	if first != second || !strings.HasPrefix(first, "fb_") {
		t.Fatalf("stable binding key = %q, %q", first, second)
	}
	if first == report || first == otherPage || report == otherPage {
		t.Fatalf("binding keys are not scope-sensitive: %q %q %q", first, report, otherPage)
	}
}

func TestTypedV1URLRoundTripUsesCanonicalExpression(t *testing.T) {
	expression := Expression{
		Kind:     ExpressionSet,
		Operator: OperatorIn,
		Values: []Value{
			{Kind: ValueString, Value: "WA"},
			{Kind: ValueString, Value: "CA"},
			{Kind: ValueString, Value: "WA"},
		},
	}
	encoded, err := EncodeTypedV1(expression, ValueString)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "=") {
		t.Fatalf("typed_v1 encoding contains base64 padding: %q", encoded)
	}
	got, err := DecodeTypedV1(encoded, ValueString)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Values) != 2 || got.Values[0].Value != "CA" || got.Values[1].Value != "WA" {
		t.Fatalf("decoded canonical expression = %#v", got)
	}
}

func TestSharedLinkCodecLifecycleSupportsDecodeAfterDeprecationButNotEncode(t *testing.T) {
	registry := NewSharedLinkRegistry()
	if err := registry.SetStatus(URLEncodingTypedV1, SharedLinkCodecDeprecated); err != nil {
		t.Fatal(err)
	}
	expression := Expression{Kind: ExpressionSet, Operator: OperatorIn, Values: []Value{{Kind: ValueString, Value: "CA"}}}
	encoded, err := EncodeTypedV1(expression, ValueString)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Encode(URLEncodingTypedV1, expression, ValueString); err == nil {
		t.Fatal("deprecated codec remained enabled for new links")
	}
	if _, err := registry.Decode(URLEncodingTypedV1, encoded, ValueString); err != nil {
		t.Fatalf("deprecated codec no longer decodes existing links: %v", err)
	}
	if err := registry.SetStatus(URLEncodingTypedV1, SharedLinkCodecRetired); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Decode(URLEncodingTypedV1, encoded, ValueString); err == nil {
		t.Fatal("retired codec decoded a shared link")
	}
}

func TestCanonicalizeRelativePeriodRequiresCompatibleAnchor(t *testing.T) {
	_, err := Canonicalize(Expression{
		Kind:        ExpressionRelativePeriod,
		Direction:   DirectionPrevious,
		Count:       1,
		Unit:        UnitMonth,
		Anchor:      AnchorFixed,
		AnchorValue: &Value{Kind: ValueString, Value: "not-a-date"},
	}, ValueDate)
	if err == nil {
		t.Fatal("Canonicalize() accepted an incompatible fixed anchor")
	}
}
