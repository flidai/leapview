package contractprojection

import (
	"encoding/json"
	"testing"
)

func TestCanonicalSemanticLiteralTypes(t *testing.T) {
	for _, test := range []struct {
		datatype string
		value    any
		want     string
	}{
		{"String", "e\u0301", "String\x00é"},
		{"Integer", json.Number("9007199254740993"), "Integer\x009007199254740993"},
		{"Decimal", json.Number("1.2500"), "Decimal\x001.25"},
		{"Boolean", true, "Boolean\x00true"},
		{"Date", "2026-09-07", "Date\x002026-09-07"},
		{"Time", "12:34:56", "Time\x0012:34:56"},
		{"DateTime", "2026-09-07T12:34:56", "DateTime\x002026-09-07T12:34:56"},
		{"DateTimeTz", "2026-09-07T14:34:56+02:00", "Timestamp\x002026-09-07T12:34:56Z"},
	} {
		t.Run(test.datatype, func(t *testing.T) {
			value, err := canonicalLiteral(test.datatype, test.value)
			if err != nil || canonicalValueKey(value) != test.want {
				t.Fatalf("canonical value = %s, %v; want %s", canonicalValueKey(value), err, test.want)
			}
			if err := validateCanonicalValue(value); err != nil {
				t.Fatalf("canonical value cannot be replayed: %v", err)
			}
		})
	}
	for _, datatype := range []string{"Float", "Opaque"} {
		if _, err := canonicalLiteral(datatype, json.Number("1.2")); err == nil {
			t.Fatalf("accepted unsupported type %s", datatype)
		}
	}
}
