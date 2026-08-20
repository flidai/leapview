package duckdbsql

import (
	"strings"
	"testing"
)

func TestGeneratedFunctionDocumentationProvenance(t *testing.T) {
	version, commit, files := GeneratedFunctionDocumentationSource()
	if version != "v1.5.4" || commit != DuckDBSourceCommit {
		t.Fatalf("source identity = %q %q", version, commit)
	}
	if len(files) != 20 {
		t.Fatalf("source files = %d, want 20", len(files))
	}
	docs := GeneratedFunctionDocumentationSnapshot()
	if len(docs) != 294 {
		t.Fatalf("documentation rows = %d, want 294", len(docs))
	}
	for _, doc := range docs {
		if doc.SourcePath == "" || doc.Category == "" || doc.FunctionType == "" || doc.Kind == "" {
			t.Fatalf("incomplete provenance: %#v", doc)
		}
		if strings.Contains(doc.Description, "extra_functions") {
			t.Fatal("C++ implementation metadata leaked into description")
		}
	}
}

func TestGeneratedFunctionDocumentationDeepCopy(t *testing.T) {
	first := GeneratedFunctionDocumentationSnapshot()
	if len(first) == 0 {
		t.Fatal("empty documentation inventory")
	}
	first[0].Aliases = append(first[0].Aliases, "mutated")
	if len(GeneratedFunctionDocumentationSnapshot()[0].Aliases) == len(first[0].Aliases) {
		t.Fatal("aliases were not copied")
	}
	for i := range first {
		if len(first[i].Variants) > 0 && len(first[i].Variants[0].Parameters) > 0 {
			first[i].Variants[0].Parameters[0].Name = "mutated"
			second := GeneratedFunctionDocumentationSnapshot()
			if second[i].Variants[0].Parameters[0].Name == "mutated" {
				t.Fatal("variant parameters were not copied")
			}
			return
		}
	}
	t.Fatal("no structured variant found")
}

func TestGeneratedFunctionDocumentationResolvesToRuntimeInventory(t *testing.T) {
	runtimeNames := make(map[string]struct{})
	for _, function := range GeneratedInventorySnapshot().Functions {
		runtimeNames[strings.ToLower(function.FunctionName)] = struct{}{}
	}
	for _, doc := range GeneratedFunctionDocumentationSnapshot() {
		for _, name := range append([]string{doc.Name}, doc.Aliases...) {
			if _, ok := runtimeNames[strings.ToLower(name)]; !ok {
				t.Errorf("documented function %q from %s is absent from the pinned runtime inventory", name, doc.SourcePath)
			}
		}
	}
}
