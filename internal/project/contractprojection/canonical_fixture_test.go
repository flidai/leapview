package contractprojection

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestCurrentCrossLanguageFixtureMatchesSourceProjection(t *testing.T) {
	wire, err := os.ReadFile("testdata/cross-language-projection.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	wire = bytes.TrimSpace(wire)
	input, err := os.ReadFile("testdata/cross-language-projection.input.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourcePublication(input); err == nil {
		t.Fatal("non-canonical publication bytes were accepted")
	}
	if _, err := DecodeSourcePublication(wire); err != nil {
		t.Fatalf("canonical fixture was rejected: %v", err)
	}

	var authored projectcontracts.Source
	if err := json.Unmarshal([]byte(`{
  "apiVersion":"leapview.dev/v1",
  "kind":"Source",
  "metadata":{"id":"source:orders","name":"orders"},
  "spec":{
    "connection":"connection:warehouse",
    "location":{"type":"relation","name":"orders"},
    "schema":{"mode":"strict","fields":{
      "z_field":{"nullable":true,"datatype":"String"},
      "a_field":{"datatype":"Integer"}
    }}
  }
}`), &authored); err != nil {
		t.Fatalf("decode authored source: %v", err)
	}
	projected, err := ProjectSource(authored, Contract{Version: "1.2.3", Compatibility: "backward"})
	if err != nil {
		t.Fatalf("project authored source: %v", err)
	}
	canonical, err := CanonicalBytes(projected)
	if err != nil {
		t.Fatalf("canonicalize projected source: %v", err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(wire)) {
		t.Fatalf("projected bytes = %s, want %s", canonical, bytes.TrimSpace(wire))
	}
}

func TestRFC8785NumberCorpus(t *testing.T) {
	input, err := os.ReadFile("testdata/rfc8785-numbers.input.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/rfc8785-numbers.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := canonicalizeRFC8785(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, bytes.TrimSpace(want)) {
		t.Fatalf("RFC corpus mismatch: %s", got)
	}
}
