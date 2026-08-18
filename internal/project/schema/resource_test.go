package configschema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const testConnectionYAML = `apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:files
  name: files
spec:
  kind: managed
`

const testConnectionJSON = `{"apiVersion":"leapview.dev/v1","kind":"Connection","metadata":{"id":"connection:files","name":"files"},"spec":{"kind":"managed"}}`

func TestNormalizeResourceCanonicalYAMLandJSON(t *testing.T) {
	yamlBytes, err := NormalizeResource(KindConnection, "connection.yaml", []byte(testConnectionYAML))
	if err != nil {
		t.Fatalf("NormalizeResource(YAML): %v", err)
	}
	jsonBytes, err := NormalizeResource(KindConnection, "connection.json", []byte(testConnectionJSON))
	if err != nil {
		t.Fatalf("NormalizeResource(JSON): %v", err)
	}
	if !reflect.DeepEqual(yamlBytes, jsonBytes) {
		t.Fatalf("YAML and JSON normalization differ:\nYAML: %s\nJSON: %s", yamlBytes, jsonBytes)
	}
	if got, want := string(yamlBytes), `{"apiVersion":"leapview.dev/v1","kind":"Connection","metadata":{"id":"connection:files","name":"files"},"spec":{"kind":"managed"}}`; got != want {
		t.Fatalf("normalized bytes = %s, want %s", got, want)
	}
}

type testUnionDTO struct {
	Kind string `json:"kind"`
}

var testJSONDecodeCalls int

func (d *testUnionDTO) UnmarshalJSON(data []byte) error {
	testJSONDecodeCalls++
	type plain testUnionDTO
	return json.Unmarshal(data, (*plain)(d))
}

func TestDecodeResourceUsesGeneratedJSONDecoder(t *testing.T) {
	testJSONDecodeCalls = 0
	var fromYAML, fromJSON testUnionDTO
	if err := DecodeResource(KindConnection, "connection.yaml", []byte(testConnectionYAML), &fromYAML); err != nil {
		t.Fatalf("DecodeResource(YAML): %v", err)
	}
	if err := DecodeResource(KindConnection, "connection.json", []byte(testConnectionJSON), &fromJSON); err != nil {
		t.Fatalf("DecodeResource(JSON): %v", err)
	}
	if testJSONDecodeCalls != 2 {
		t.Fatalf("destination UnmarshalJSON calls = %d, want 2", testJSONDecodeCalls)
	}
	if !reflect.DeepEqual(fromYAML, fromJSON) || fromYAML.Kind != "Connection" {
		t.Fatalf("decoded resources differ: YAML=%#v JSON=%#v", fromYAML, fromJSON)
	}
}

func TestNormalizeResourceRejectsAmbiguousYAML(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		code    string
		message string
	}{
		{
			name:    "duplicate key",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  kind: managed", 1),
			code:    "schema.duplicate_key",
			message: "duplicate mapping key",
		},
		{
			name:    "anchor",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: &managed managed", 1),
			code:    "schema.alias",
			message: "anchors",
		},
		{
			name:    "alias",
			doc:     strings.Replace(testConnectionYAML, "spec:\n  kind: managed", "spec: &spec\n  kind: managed\nother: *spec", 1),
			code:    "schema.alias",
			message: "anchors",
		},
		{
			name:    "explicit tag",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: !!str managed", 1),
			code:    "schema.tag",
			message: "explicit YAML tag",
		},
		{
			name:    "non-string key",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {true: value}", 1),
			code:    "schema.key",
			message: "keys must be strings",
		},
		{
			name:    "non-finite float",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {ratio: .nan}", 1),
			code:    "schema.number",
			message: "non-finite",
		},
		{
			name:    "underflowing float",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {ratio: 1e-400}", 1),
			code:    "schema.number",
			message: "underflows",
		},
		{
			name:    "overflowing float",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {ratio: 1e400}", 1),
			code:    "schema.number",
			message: "cannot be represented",
		},
		{
			name:    "timestamp",
			doc:     strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {when: 2024-01-01}", 1),
			code:    "schema.tag",
			message: "tag",
		},
		{
			name:    "multiple documents",
			doc:     testConnectionYAML + "---\n" + testConnectionYAML,
			code:    "schema.document",
			message: "multiple YAML documents",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NormalizeResource(KindConnection, "connection.yaml", []byte(tt.doc))
			assertDiagnostic(t, err, tt.code, tt.message)
		})
	}
}

func TestNormalizeResourcePreservesSourcePosition(t *testing.T) {
	doc := strings.Replace(testConnectionYAML, "  kind: managed", "  kind: managed\n  options: {true: value}", 1)
	_, err := NormalizeResource(KindConnection, "connection.yaml", []byte(doc))
	diagnostic := assertDiagnosticMessage(t, err, "schema.key", "keys must be strings")
	if diagnostic.File != "connection.yaml" || diagnostic.Line != 8 || diagnostic.Column == 0 {
		t.Fatalf("diagnostic position = %#v, want connection.yaml:8:<column>", diagnostic)
	}
}

func TestNormalizeResourceRejectsUnderflowingJSONNumber(t *testing.T) {
	doc := strings.Replace(testConnectionJSON, `"kind":"managed"`, `"kind":"managed","options":{"ratio":1e-400}`, 1)
	_, err := NormalizeResource(KindConnection, "connection.json", []byte(doc))
	assertDiagnostic(t, err, "schema.number", "underflows")
}
