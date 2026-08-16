package main

import (
	"os"
	"strings"
	"testing"
)

func TestSearchClientTransportPolicy(t *testing.T) {
	data, err := os.ReadFile("../../../project/api/gen/client.apigen.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, want := range []string{
		"AddQuery(query, \"kind\", request.Params.Kind, true)",
		"limit must be between 1 and 200",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("generated Search client missing policy %q", want)
		}
	}
}

func TestSearchClientTransportPolicyPatchIsIdempotentAndFailsClosed(t *testing.T) {
	data, err := os.ReadFile("../../../project/api/gen/client.apigen.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	first, err := applySearchPolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := applySearchPolicy(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("search transport policy patch is not idempotent")
	}
	if _, err := applySearchPolicy([]byte("package gen\n")); err == nil {
		t.Fatal("generator shape change was silently accepted")
	}
}

func TestAccessServerResourceKindPolicy(t *testing.T) {
	data, err := os.ReadFile("../../../access/api/gen/server.apigen.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if got := strings.Count(source, "*ResourceKind"); got != 0 {
		t.Fatalf("generated access server still contains %d unqualified ResourceKind parameters", got)
	}
	if got := strings.Count(source, "*GenSchemaResourceKind"); got != 3 {
		t.Fatalf("generated access server has %d qualified ResourceKind parameters, want 3", got)
	}
}

func TestAccessServerResourceKindPolicyPatchIsIdempotentAndFailsClosed(t *testing.T) {
	data, err := os.ReadFile("../../../access/api/gen/server.apigen.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	first, err := applyAccessResourceKindPolicy(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := applyAccessResourceKindPolicy(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("access ResourceKind policy patch is not idempotent")
	}
	if _, err := applyAccessResourceKindPolicy([]byte("package gen\n")); err == nil {
		t.Fatal("generator shape change was silently accepted")
	}
}
