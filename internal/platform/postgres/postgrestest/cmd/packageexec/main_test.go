package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

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

func TestFinishPackageServerPreservesRunAndTerminationErrors(t *testing.T) {
	runErr := errors.New("package tests failed")
	terminationErr := errors.New("package server termination failed")
	called := false
	err := finishPackageServer(runErr, func(ctx context.Context) error {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 {
			t.Fatal("termination did not receive a live cleanup deadline")
		}
		return terminationErr
	})
	if !called {
		t.Fatal("package server termination was not attempted")
	}
	if !errors.Is(err, runErr) {
		t.Fatalf("test run error was not preserved: %v", err)
	}
	if !errors.Is(err, terminationErr) {
		t.Fatalf("termination error was not surfaced: %v", err)
	}
}

func TestFinishPackageServerReturnsSuccessfulRun(t *testing.T) {
	called := false
	if err := finishPackageServer(nil, func(context.Context) error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("successful package server lifecycle: %v", err)
	}
	if !called {
		t.Fatal("successful package server lifecycle did not terminate the server")
	}
}
