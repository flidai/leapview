package main

import (
	"reflect"
	"testing"
)

func TestPortableSchemaDereferencesAndKeepsFormatProperty(t *testing.T) {
	definitions := map[string]any{
		"Result": map[string]any{
			"type":                  "object",
			"unevaluatedProperties": false,
			"properties": map[string]any{
				"createdAt": map[string]any{"type": "string", "format": "date-time"},
				"format":    map[string]any{"type": "object", "unevaluatedProperties": false},
			},
		},
	}
	got, err := portableSchema(map[string]any{"$ref": "#/$defs/Result"}, definitions, map[string]bool{})
	if err != nil {
		t.Fatalf("portableSchema(): %v", err)
	}
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"createdAt": map[string]any{"type": "string"},
			"format":    map[string]any{"type": "object", "additionalProperties": false},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portable schema = %#v, want %#v", got, want)
	}
}

func TestPortableSchemaRejectsRecursiveReferences(t *testing.T) {
	definitions := map[string]any{
		"Node": map[string]any{
			"type":       "object",
			"properties": map[string]any{"child": map[string]any{"$ref": "#/$defs/Node"}},
		},
	}
	if _, err := portableSchema(map[string]any{"$ref": "#/$defs/Node"}, definitions, map[string]bool{}); err == nil {
		t.Fatal("portableSchema() accepted a recursive contract")
	}
}

func TestPortableSchemaDropsUnsupportedArrayConstraints(t *testing.T) {
	input := map[string]any{
		"type": "array", "items": map[string]any{"type": "string"},
		"minItems": 1, "maxItems": 4, "uniqueItems": true,
	}
	got, err := portableSchema(input, nil, map[string]bool{})
	if err != nil {
		t.Fatalf("portableSchema(): %v", err)
	}
	want := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("portable schema = %#v, want %#v", got, want)
	}
}
