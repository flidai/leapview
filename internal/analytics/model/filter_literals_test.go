package model

import (
	"encoding/json"
	"testing"
)

func TestDecimalFilterLiteralUsesPrecisionSafeRepresentation(t *testing.T) {
	value, err := CoerceSemanticLiteral("9007199254740993.125", MetricDimension{Datatype: DataTypeDecimal})
	if err != nil {
		t.Fatal(err)
	}
	if value != json.Number("9007199254740993.125") {
		t.Fatalf("Decimal filter value = %#v", value)
	}
	if _, err := CoerceSemanticLiteral(float64(9007199254740993.125), MetricDimension{Datatype: DataTypeDecimal}); err == nil {
		t.Fatal("binary float was accepted as an exact Decimal literal")
	}
}

func TestDecimalFilterLiteralUsesCanonicalFixedPointLexicalForm(t *testing.T) {
	for _, token := range []string{"1e3", "01", "+1", "-0", "-0.0"} {
		t.Run("reject/"+token, func(t *testing.T) {
			if _, err := CoerceSemanticLiteral(token, MetricDimension{Datatype: DataTypeDecimal}); err == nil {
				t.Fatalf("Decimal filter literal %q was accepted", token)
			}
		})
	}

	for _, test := range []struct {
		token string
		want  json.Number
	}{
		{token: "0", want: "0"},
		{token: "0.0", want: "0"},
		{token: "1", want: "1"},
		{token: "1.0", want: "1"},
		{token: "-1", want: "-1"},
		{token: "-1.0", want: "-1"},
		{token: "9007199254740993.125", want: "9007199254740993.125"},
	} {
		t.Run("accept/"+test.token, func(t *testing.T) {
			got, err := CoerceSemanticLiteral(test.token, MetricDimension{Datatype: DataTypeDecimal})
			if err != nil {
				t.Fatalf("Decimal filter literal %q failed: %v", test.token, err)
			}
			if got != test.want {
				t.Fatalf("Decimal filter literal %q = %q, want %q", test.token, got, test.want)
			}
		})
	}
}
