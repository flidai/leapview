package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

func TestPackageTestArgumentsEnforceSerialExecution(t *testing.T) {
	input := []string{"-test.v", "-test.parallel=8"}
	want := []string{"-test.v", "-test.parallel=8", "-test.parallel=1"}
	if got := packageTestArguments(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("package test arguments = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(input, []string{"-test.v", "-test.parallel=8"}) {
		t.Fatalf("input arguments were modified: %#v", input)
	}
}

func TestPackageTestEnvironmentEnforcesDisposableServer(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED", "0")
	got := packageTestEnvironment("postgres://package-server", "package-token")
	want := []string{
		"LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1",
		postgrestest.PackageServerURLEnv + "=postgres://package-server",
		postgrestest.PackageServerTokenEnv + "=package-token",
	}
	if !reflect.DeepEqual(got[len(got)-len(want):], want) {
		t.Fatalf("package environment suffix = %#v, want %#v", got[len(got)-len(want):], want)
	}
	if os.Getenv("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED") != "0" {
		t.Fatal("package environment changed the parent process")
	}
}
