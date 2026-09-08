package contractprojection

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestProjectionIdentifierValidationMatchesGeneratedPatterns(t *testing.T) {
	data, err := os.ReadFile("../contracts/gen/data-resources.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Definitions map[string]struct {
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		definition, property string
		validate             func(string) bool
	}{
		{"ResourceMetadata", "name", validProjectionName},
		{"ContractProjectionField", "classification", validProjectionIdentifier},
	} {
		pattern := schema.Definitions[test.definition].Properties[test.property].Pattern
		if pattern == "" {
			t.Fatalf("missing generated pattern for %s.%s", test.definition, test.property)
		}
		expression := regexp.MustCompile(pattern)
		for _, value := range []string{"", "orders", "_field2", "field-name.v2", "2field", "aŁ", "aı", "aé", "Ａ", "a\xff"} {
			if got, want := test.validate(value), expression.MatchString(value); got != want {
				t.Errorf("%s.%s(%q) = %v, generated pattern expects %v", test.definition, test.property, value, got, want)
			}
		}
	}
}

func TestProjectionIdentifierValidationIsASCIIByteBased(t *testing.T) {
	for _, value := range []string{"aŁ", "aı", "aé"} {
		if validProjectionIdentifier(value) {
			t.Errorf("validProjectionIdentifier(%q) accepted non-ASCII input", value)
		}
		if validProjectionName(value) {
			t.Errorf("validProjectionName(%q) accepted non-ASCII input", value)
		}
	}
	for _, value := range []string{"_field2", "field_name"} {
		if !validProjectionIdentifier(value) {
			t.Errorf("validProjectionIdentifier(%q) rejected ASCII input", value)
		}
	}
	for _, value := range []string{"_field2", "field_name", "field-name.v2"} {
		if !validProjectionName(value) {
			t.Errorf("validProjectionName(%q) rejected ASCII input", value)
		}
	}
}

func TestDecodePublicationRejectsNonASCIIProjectionNameAndField(t *testing.T) {
	wire, err := os.ReadFile("testdata/cross-language-projection.canonical.json")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		from string
		to   string
	}{
		{name: "metadata name", from: `"name":"orders"`, to: `"name":"aŁ"`},
		{name: "schema field name", from: `"a_field"`, to: `"aŁ"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Replace(wire, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(mutated, wire) {
				t.Fatalf("test mutation %q was not applied", test.from)
			}
			if _, err := DecodeSourcePublication(mutated); err == nil {
				t.Fatalf("DecodeSourcePublication accepted non-ASCII projection %s", test.to)
			}
		})
	}
}
