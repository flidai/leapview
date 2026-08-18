package main

import "testing"

func TestDerivePathFormatOptionsRequiresOneReaderModelPerFormat(t *testing.T) {
	doc := document{Schemas: map[string]schema{
		"PathSourceLocation": {Properties: map[string]property{
			"format": {Schema: schemaRef{Enum: []string{"csv", "json"}}},
		}},
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
		"PathSourceLocation": {Properties: map[string]property{
			"format": {Schema: schemaRef{Enum: []string{"csv"}}},
		}},
		"ReaderDefaults": {Properties: map[string]property{
			"csv":  {Schema: schemaRef{Ref: "CSVReaderOptions"}},
			"json": {Schema: schemaRef{Ref: "JSONReaderOptions"}},
		}},
	}}
	if _, err := derivePathFormatOptions(doc); err == nil {
		t.Fatal("unpaired ReaderDefaults json model was accepted")
	}
}

func TestDerivePathFormatOptionsPreservesIROrderAndReferences(t *testing.T) {
	doc := document{Schemas: map[string]schema{
		"PathSourceLocation": {Properties: map[string]property{
			"format": {Schema: schemaRef{Enum: []string{"json", "csv"}}},
		}},
		"ReaderDefaults": {Properties: map[string]property{
			"csv":  {Schema: schemaRef{Ref: "CSVReaderOptions"}},
			"json": {Schema: schemaRef{Ref: "JSONReaderOptions"}},
		}},
	}}
	pairs, err := derivePathFormatOptions(doc)
	if err != nil {
		t.Fatalf("derive path format options: %v", err)
	}
	if len(pairs) != 2 || pairs[0] != (pathFormatOption{Format: "json", Model: "JSONReaderOptions"}) || pairs[1] != (pathFormatOption{Format: "csv", Model: "CSVReaderOptions"}) {
		t.Fatalf("pairs = %#v, want IR order and refs", pairs)
	}
}
