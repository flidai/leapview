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

func TestFloatFilterLiteralAcceptsPreservedJSONNumber(t *testing.T) {
	value, err := CoerceSemanticLiteral(json.Number("2.5"), MetricDimension{Datatype: DataTypeFloat})
	if err != nil {
		t.Fatal(err)
	}
	if value != 2.5 {
		t.Fatalf("Float filter value = %#v", value)
	}
}

func TestDecimalFilterLiteralUsesCanonicalFixedPointLexicalForm(t *testing.T) {
	for _, token := range []string{"", "not-a-number", "NaN", "Infinity"} {
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
		{token: "1e3", want: "1000"},
		{token: "01", want: "1"},
		{token: "+1", want: "1"},
		{token: "-0", want: "0"},
		{token: "-0.0", want: "0"},
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

func TestSemanticFilterLiteralUsesSharedAccessCanonicalization(t *testing.T) {
	stringValue, err := CoerceSemanticLiteral("Cafe\u0301", MetricDimension{Datatype: DataTypeString})
	if err != nil {
		t.Fatal(err)
	}
	if stringValue != "Café" {
		t.Fatalf("String filter value = %q, want NFC", stringValue)
	}
	if _, err := CoerceSemanticLiteral("north\namerica", MetricDimension{Datatype: DataTypeString}); err == nil {
		t.Fatal("String filter accepted a control character")
	}
	if _, err := CoerceSemanticLiteral(float64(1), MetricDimension{Datatype: DataTypeInteger}); err == nil {
		t.Fatal("Integer filter accepted a cross-type Float value")
	}
	timestamp, err := CoerceSemanticLiteral("2024-01-02T03:04:05.1200+02:30", MetricDimension{Datatype: DataTypeDateTimeTZ})
	if err != nil {
		t.Fatal(err)
	}
	if timestamp != "2024-01-02T00:34:05.12Z" {
		t.Fatalf("Timestamp filter value = %q", timestamp)
	}
}
