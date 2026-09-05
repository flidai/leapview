package access

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestCanonicalSemanticAttributeValuesUsesSemanticValueBounds(t *testing.T) {
	definition := SemanticAttributeDefinition{Enabled: true, Name: "labels", Type: semanticvalue.TypeString, Shape: SemanticAttributeList}
	values, _, err := CanonicalSemanticAttributeValues(definition, []string{"b", "a", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(values, ",") != "a,b" {
		t.Fatalf("canonical list = %#v", values)
	}
	if values, _, err := CanonicalSemanticAttributeValues(definition, []string{"{literal}", "[literal]", "left=>right"}); err != nil || len(values) != 3 {
		t.Fatalf("rejected valid punctuation-bearing strings: values=%#v err=%v", values, err)
	}
	for _, input := range []any{map[string]string{"blocked": "value"}, []any{[]string{"nested"}}, func() {}} {
		if _, _, err := CanonicalSemanticAttributeValues(definition, input); err == nil {
			t.Errorf("accepted unsupported list value %#v", input)
		}
	}
	if _, _, err := CanonicalSemanticAttributeValues(SemanticAttributeDefinition{Enabled: true, Type: semanticvalue.TypeString, Shape: SemanticAttributeScalar}, []string{"not-scalar"}); err == nil {
		t.Fatal("accepted a collection for a scalar attribute")
	}
	tooMany := make([]any, semanticvalue.MaxSetValues+1)
	for index := range tooMany {
		tooMany[index] = strings.Repeat("x", index+1)
	}
	if _, _, err := CanonicalSemanticAttributeValues(definition, tooMany); err == nil {
		t.Fatal("accepted more than the semanticvalue cardinality bound")
	}
}
