package strictjson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type document struct {
	Name   string `json:"name"`
	Nested struct {
		Value int `json:"value"`
	} `json:"nested"`
}

func TestDecodeValidDocument(t *testing.T) {
	var got document
	if err := Decode([]byte(`{"name":"demo","nested":{"value":7}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || got.Nested.Value != 7 {
		t.Fatalf("decoded document = %#v", got)
	}
}

func TestDecodeRejectsNestedDuplicateKeys(t *testing.T) {
	for _, input := range []string{
		`{"name":"first","name":"second"}`,
		`{"nested":{"value":1,"value":2}}`,
		`{"nested":{"value":1,"VALUE":2}}`,
	} {
		var got document
		err := Decode([]byte(input), &got)
		if !errors.Is(err, ErrDuplicateKey) {
			t.Fatalf("Decode(%s) error = %v, want duplicate key", input, err)
		}
		var duplicate *DuplicateKeyError
		if !errors.As(err, &duplicate) || duplicate.Key == "" {
			t.Fatalf("Decode(%s) error = %v, want typed duplicate", input, err)
		}
	}
}

func TestCaseSensitiveDuplicateOption(t *testing.T) {
	var got map[string]int
	err := DecodeWithOptions([]byte(`{"value":1,"VALUE":2}`), &got, Options{
		DuplicateKeys:      CaseSensitiveKeys,
		AllowUnknownFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("decoded map = %#v", got)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var got document
	if err := Decode([]byte(`{"name":"demo","unknown":true}`), &got); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Decode unknown field error = %v", err)
	}
	if err := DecodeWithOptions([]byte(`{"name":"demo","unknown":true}`), &got, Options{AllowUnknownFields: true}); err != nil {
		t.Fatalf("DecodeWithOptions allowing unknown fields: %v", err)
	}
}

func TestDecodeRejectsTrailingData(t *testing.T) {
	for _, input := range []string{
		`{"name":"demo"}{}`,
		`{"name":"demo"} trailing`,
	} {
		var got document
		if err := Decode([]byte(input), &got); !errors.Is(err, ErrTrailingData) {
			t.Fatalf("Decode(%q) error = %v, want trailing data", input, err)
		}
	}
}

func TestDecodeEnforcesSizeLimit(t *testing.T) {
	var got any
	err := DecodeWithOptions([]byte(`{"name":"demo"}`), &got, Options{MaxBytes: 4})
	if !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("DecodeWithOptions error = %v, want size limit", err)
	}
	err = DecodeReader(strings.NewReader(`{"name":"demo"}`), &got, Options{MaxBytes: 4})
	if !errors.Is(err, ErrSizeLimit) {
		t.Fatalf("DecodeReader error = %v, want size limit", err)
	}
}

func TestDecodeEnforcesDepthLimit(t *testing.T) {
	var got any
	err := DecodeWithOptions([]byte(`{"one":{"two":true}}`), &got, Options{
		MaxDepth:           1,
		AllowUnknownFields: true,
	})
	if !errors.Is(err, ErrDepthLimit) {
		t.Fatalf("DecodeWithOptions error = %v, want depth limit", err)
	}
}

func TestDecodeRejectsInvalidSyntax(t *testing.T) {
	var got any
	if err := Decode([]byte(`{"name":`), &got); err == nil {
		t.Fatal("Decode accepted invalid syntax")
	}
}

func TestDecodeReader(t *testing.T) {
	var got document
	if err := DecodeReader(strings.NewReader(`{"name":"reader"}`), &got, Options{}); err != nil {
		t.Fatal(err)
	}
	if got.Name != "reader" {
		t.Fatalf("decoded name = %q", got.Name)
	}
}

func TestDecodeDoesNotNarrowJSONNumbersDuringValidation(t *testing.T) {
	var got struct {
		Value json.Number `json:"value"`
	}
	if err := Decode([]byte(`{"value":1e1000}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Value.String() != "1e1000" {
		t.Fatalf("decoded number = %q", got.Value)
	}
}
