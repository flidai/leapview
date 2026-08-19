package main

import (
	"strings"
	"testing"
)

func TestLoadInputPinnedSourceCompleteness(t *testing.T) {
	input, err := loadInput(defaultSource, defaultLock, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Files) != 20 {
		t.Fatalf("source files = %d, want 20", len(input.Files))
	}
	if len(input.Docs) != 294 {
		t.Fatalf("documentation rows = %d, want 294", len(input.Docs))
	}
	var foundArgMax, foundRepeat bool
	for _, doc := range input.Docs {
		if doc.Name == "arg_max" {
			foundArgMax = true
			if doc.FunctionType != "aggregate_function_set" || doc.Kind != "aggregate" || doc.Category != "distributive" {
				t.Fatalf("arg_max provenance = %#v", doc)
			}
			if len(doc.Aliases) != 2 || doc.Aliases[0] != "argmax" || doc.Aliases[1] != "max_by" {
				t.Fatalf("arg_max aliases = %#v", doc.Aliases)
			}
		}
		if doc.Name == "repeat" {
			foundRepeat = true
			if len(doc.Variants) != 3 || doc.Variants[1].Parameters[0].Type != "ANY[]" {
				t.Fatalf("repeat variants = %#v", doc.Variants)
			}
		}
	}
	if !foundArgMax || !foundRepeat {
		t.Fatalf("expected representative docs: arg_max=%v repeat=%v", foundArgMax, foundRepeat)
	}
}

func TestValidateSourceUsesEveryLockedHash(t *testing.T) {
	input, err := loadInput(defaultSource, defaultLock, false)
	if err != nil {
		t.Fatal(err)
	}
	for path := range input.Lock.Files {
		input.Lock.Files[path] = strings.Repeat("0", 64)
		if err := validateSource(defaultSource, input.Lock, false); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("validateSource(%s) error = %v", path, err)
		}
		break
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	first, err := loadInput(defaultSource, defaultLock, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadInput(defaultSource, defaultLock, false)
	if err != nil {
		t.Fatal(err)
	}
	a, err := render(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := render(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("render output changed between identical source reads")
	}
	if strings.Contains(a, "extra_functions") || strings.Contains(a, "struct:") {
		t.Fatal("C++ implementation-only fields leaked into generated output")
	}
}

func TestDecodeSourceFunctionsRejectsUnknownFields(t *testing.T) {
	_, err := decodeSourceFunctions([]byte(`[{"name":"future","type":"scalar_function","new_sql_metadata":true}]`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decodeSourceFunctions error = %v", err)
	}
}
