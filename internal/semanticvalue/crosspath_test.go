package semanticvalue_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestProfileV1GoldenMatchesSemanticFilterPath(t *testing.T) {
	content, err := os.ReadFile("testdata/profile-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Profile string `json:"profile"`
		Cases   []struct {
			Name      string             `json:"name"`
			Type      semanticvalue.Type `json:"type"`
			Input     any                `json:"input"`
			Canonical string             `json:"canonical"`
		} `json:"cases"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Profile != semanticvalue.Profile {
		t.Fatalf("fixture profile = %q, want %q", fixture.Profile, semanticvalue.Profile)
	}

	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			canonical, err := semanticvalue.Canonicalize(test.Type, test.Input)
			if err != nil {
				t.Fatal(err)
			}
			if canonical.Canonical() != test.Canonical {
				t.Fatalf("shared canonical value = %q, want %q", canonical.Canonical(), test.Canonical)
			}

			dimension := semanticmodel.MetricDimension{Datatype: semanticDatatype(test.Type)}
			filterValue, err := semanticmodel.CoerceSemanticLiteral(test.Input, dimension)
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprint(filterValue); got != test.Canonical {
				t.Fatalf("semantic filter value = %q, want %q", got, test.Canonical)
			}
		})
	}
}

func semanticDatatype(typeName semanticvalue.Type) semanticmodel.LogicalDataType {
	switch typeName {
	case semanticvalue.TypeString:
		return semanticmodel.DataTypeString
	case semanticvalue.TypeBoolean:
		return semanticmodel.DataTypeBoolean
	case semanticvalue.TypeInteger:
		return semanticmodel.DataTypeInteger
	case semanticvalue.TypeDecimal:
		return semanticmodel.DataTypeDecimal
	case semanticvalue.TypeDate:
		return semanticmodel.DataTypeDate
	case semanticvalue.TypeTimestamp:
		return semanticmodel.DataTypeDateTimeTZ
	default:
		return ""
	}
}
