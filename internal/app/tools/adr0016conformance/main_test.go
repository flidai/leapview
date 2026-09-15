package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedMatrixIsCurrent(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	generated, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, generated) {
		t.Fatal("ADR-0016 standards conformance matrix is stale")
	}
}

func TestDeferredProfileCannotClaimImplementation(t *testing.T) {
	item := profile{
		ID: "deferred/example", Publisher: "Example", Standard: "Example", StandardVersion: "1.0.0", UpstreamRevision: "not pinned",
		Directions: []string{"export"}, Extension: "none", Status: "deferred", AdapterPath: "internal/project/contractodcs",
		SpecificationPath: "adr/0016-adopt-standards-aligned-data-contracts-and-interchange.md", Implementation: "deferred", Boundary: "none",
	}
	if err := validateProfile(filepath.Clean(filepath.Join("..", "..", "..", "..")), item, map[string]struct{}{}); err == nil {
		t.Fatal("deferred profile claimed an adapter")
	}
}
