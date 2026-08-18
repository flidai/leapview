package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDerivePathFormatOptionsRequiresOneReaderModelPerFormat(t *testing.T) {
	doc := document{Schemas: map[string]schema{
		"PathSourceLocation":     {OneOf: []schemaRef{{Ref: "CSVPathSourceLocation"}, {Ref: "JSONPathSourceLocation"}}, Discriminator: &discriminator{PropertyName: "format", Mapping: map[string]string{"csv": "CSVPathSourceLocation", "json": "JSONPathSourceLocation"}}},
		"CSVPathSourceLocation":  {Properties: map[string]property{"format": {Schema: schemaRef{Enum: []string{"csv"}}}}},
		"JSONPathSourceLocation": {Properties: map[string]property{"format": {Schema: schemaRef{Enum: []string{"json"}}}}},
		"ReaderDefaults": {Properties: map[string]property{
			"csv": {Schema: schemaRef{Ref: "CSVReaderOptions"}},
		}},
	}}
	if _, err := derivePathFormatOptions(doc); err == nil {
		t.Fatal("missing ReaderDefaults json model was accepted")
	}
}

func TestDerivePathFormatOptionsRejectsUnpairedReaderModel(t *testing.T) {
	doc := document{Schemas: map[string]schema{
		"PathSourceLocation":    {OneOf: []schemaRef{{Ref: "CSVPathSourceLocation"}}, Discriminator: &discriminator{PropertyName: "format", Mapping: map[string]string{"csv": "CSVPathSourceLocation"}}},
		"CSVPathSourceLocation": {Properties: map[string]property{"format": {Schema: schemaRef{Enum: []string{"csv"}}}}},
		"ReaderDefaults": {Properties: map[string]property{
			"csv":  {Schema: schemaRef{Ref: "CSVReaderOptions"}},
			"json": {Schema: schemaRef{Ref: "JSONReaderOptions"}},
		}},
		"CSVReaderOptions":  {Extensions: map[string]json.RawMessage{"x-leapview-format": json.RawMessage(`{"name":"csv","scanKind":"table_function","defaults":{}}`)}},
		"JSONReaderOptions": {Extensions: map[string]json.RawMessage{"x-leapview-format": json.RawMessage(`{"name":"json","scanKind":"table_function","defaults":{}}`)}},
	}}
	if _, err := derivePathFormatOptions(doc); err == nil {
		t.Fatal("unpaired ReaderDefaults json model was accepted")
	}
}

func TestDerivePathFormatOptionsPreservesIROrderAndReferences(t *testing.T) {
	doc := document{Schemas: map[string]schema{
		"PathSourceLocation":     {OneOf: []schemaRef{{Ref: "JSONPathSourceLocation"}, {Ref: "CSVPathSourceLocation"}}, Discriminator: &discriminator{PropertyName: "format", Mapping: map[string]string{"json": "JSONPathSourceLocation", "csv": "CSVPathSourceLocation"}}},
		"JSONPathSourceLocation": {Properties: map[string]property{"format": {Schema: schemaRef{Enum: []string{"json"}}}}},
		"CSVPathSourceLocation":  {Properties: map[string]property{"format": {Schema: schemaRef{Enum: []string{"csv"}}}}},
		"ReaderDefaults": {Properties: map[string]property{
			"csv":  {Schema: schemaRef{Ref: "CSVReaderOptions"}},
			"json": {Schema: schemaRef{Ref: "JSONReaderOptions"}},
		}},
		"CSVReaderOptions":  {Extensions: map[string]json.RawMessage{"x-leapview-format": json.RawMessage(`{"name":"csv","scanKind":"table_function","defaults":{}}`)}},
		"JSONReaderOptions": {Extensions: map[string]json.RawMessage{"x-leapview-format": json.RawMessage(`{"name":"json","scanKind":"table_function","defaults":{}}`)}},
	}}
	pairs, err := derivePathFormatOptions(doc)
	if err != nil {
		t.Fatalf("derive path format options: %v", err)
	}
	if len(pairs) != 2 || pairs[0].Format != "json" || pairs[0].Model != "JSONReaderOptions" || pairs[1].Format != "csv" || pairs[1].Model != "CSVReaderOptions" || !reflect.DeepEqual(pairs[0].Defaults, map[string]any{}) || !reflect.DeepEqual(pairs[1].Defaults, map[string]any{}) {
		t.Fatalf("pairs = %#v, want IR order and refs", pairs)
	}
}
