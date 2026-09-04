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

func TestPortableSchemaProjectsCanonicalConstraintsForProvider(t *testing.T) {
	definitions := map[string]any{
		"Result": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":  "string",
					"const": "fixed",
				},
				"values": map[string]any{
					"type":     "array",
					"minItems": float64(1),
					"maxItems": float64(3),
					"items": map[string]any{
						"type":  "integer",
						"const": float64(2),
					},
				},
				"settings": map[string]any{
					"type":          "object",
					"minProperties": float64(1),
				},
			},
		},
	}

	got, err := portableSchema(map[string]any{"$ref": "#/$defs/Result"}, definitions, map[string]bool{})
	if err != nil {
		t.Fatalf("portableSchema(): %v", err)
	}
	result := got.(map[string]any)
	properties := result["properties"].(map[string]any)
	kind := properties["kind"].(map[string]any)
	if want := []any{"fixed"}; !reflect.DeepEqual(kind["enum"], want) {
		t.Fatalf("string const enum = %#v, want %#v", kind["enum"], want)
	}
	values := properties["values"].(map[string]any)
	if _, ok := values["minItems"]; ok {
		t.Fatal("portable schema retained minItems")
	}
	if _, ok := values["maxItems"]; ok {
		t.Fatal("portable schema retained maxItems")
	}
	items := values["items"].(map[string]any)
	if _, ok := items["const"]; ok {
		t.Fatal("portable schema retained numeric const")
	}
	if want := []any{float64(2)}; !reflect.DeepEqual(items["enum"], want) {
		t.Fatalf("numeric const enum = %#v, want %#v", items["enum"], want)
	}
	settings := properties["settings"].(map[string]any)
	if _, ok := settings["minProperties"]; ok {
		t.Fatal("portable schema retained minProperties")
	}
}

func TestPortableSchemaPreservesPropertiesNamedProviderKeywords(t *testing.T) {
	definitions := map[string]any{
		"Result": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"const":         map[string]any{"type": "string"},
				"minItems":      map[string]any{"type": "string"},
				"maxItems":      map[string]any{"type": "string"},
				"minProperties": map[string]any{"type": "string"},
			},
		},
	}

	got, err := portableSchema(map[string]any{"$ref": "#/$defs/Result"}, definitions, map[string]bool{})
	if err != nil {
		t.Fatalf("portableSchema(): %v", err)
	}
	properties := got.(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"const", "minItems", "maxItems", "minProperties"} {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("portable schema property %q missing", name)
		}
		if gotType := property["type"]; gotType != "string" {
			t.Errorf("property %q type = %#v, want string", name, gotType)
		}
	}
}
