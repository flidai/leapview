package poolcompatibility

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

type extensionAdmissionFake struct {
	name     string
	admitted extension.AdmittedExtension
	err      error
}

func (a *extensionAdmissionFake) AdmitExtension(_ context.Context, name string) (extension.AdmittedExtension, error) {
	a.name = name
	return a.admitted, a.err
}

func TestLocalPoolUsesAdmittedRuntimeIdentity(t *testing.T) {
	admission := &extensionAdmissionFake{admitted: extension.AdmittedExtension{
		Name: "ducklake", DuckDBVersion: "v1.5.4", ExtensionVersion: "d318a545",
	}}
	tuple, err := LocalPool(t.Context(), admission)
	if err != nil {
		t.Fatal(err)
	}
	if admission.name != "ducklake" {
		t.Fatalf("admitted extension = %q, want ducklake", admission.name)
	}
	if tuple.DuckDBRuntime != "duckdb:1.5.4" || tuple.DuckLakeExtension != "ducklake:d318a545" || tuple.CatalogFormat != "ducklake-catalog:v1" {
		t.Fatalf("local evaluation compatibility = %#v", tuple)
	}
}

func TestLocalPoolRejectsNonDuckLakeAdmission(t *testing.T) {
	admission := &extensionAdmissionFake{admitted: extension.AdmittedExtension{
		Name: "httpfs", DuckDBVersion: "v1.5.4", ExtensionVersion: "c3f215a",
	}}
	if _, err := LocalPool(t.Context(), admission); err == nil {
		t.Fatal("non-DuckLake admission accepted as local pool compatibility")
	}
}
