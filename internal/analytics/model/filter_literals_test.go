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
