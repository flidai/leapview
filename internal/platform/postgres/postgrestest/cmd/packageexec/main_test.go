package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/testcontainers/testcontainers-go"
)

// Probe the real SDK session identity without starting Docker. The worker mode
// is handled before Go's test flag parser, only in this fixture test executable.
func init() {
	mode := os.Getenv("LEAPVIEW_POSTGRES_PACKAGE_SESSION_PROBE")
	if mode == "" {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == packageWorkerFlag {
		switch mode {
		case "session":
			fmt.Println(testcontainers.SessionID())
		case "failure":
			os.Exit(17)
		case "signal":
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
			defer stop()
			marker := os.Getenv("LEAPVIEW_POSTGRES_PACKAGE_PROBE_MARKER")
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				os.Exit(1)
			}
			<-ctx.Done()
			if err := os.WriteFile(marker+".cleanup", nil, 0o600); err != nil {
				os.Exit(1)
			}
		}
		os.Exit(0)
	}
	if err := runIsolated(context.Background(), []string{"unused-test-binary"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestPackageWorkersOwnDistinctSDKCleanupSessions(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := func() string {
		t.Helper()
		command := exec.CommandContext(t.Context(), executable, "-test.run=^$")
		command.Env = append(os.Environ(), "LEAPVIEW_POSTGRES_PACKAGE_SESSION_PROBE=session")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("probe package worker session: %v: %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	first, second := probe(), probe()
	if len(first) != 64 || len(second) != 64 || first == second {
		t.Fatalf("independent package workers share a cleanup session: %q, %q", first, second)
	}
}

func TestIsolatedWorkerPreservesFailure(t *testing.T) {
	t.Setenv("LEAPVIEW_POSTGRES_PACKAGE_SESSION_PROBE", "failure")
	err := runIsolated(t.Context(), []string{"unused-test-binary"})
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("worker failure was not preserved: %v", err)
	}
}

func TestIsolatedWorkerReceivesCancellationBeforeTermination(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "worker-ready")
	t.Setenv("LEAPVIEW_POSTGRES_PACKAGE_SESSION_PROBE", "signal")
	t.Setenv("LEAPVIEW_POSTGRES_PACKAGE_PROBE_MARKER", marker)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runIsolated(ctx, []string{"unused-test-binary"}) }()
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("worker exited before cancellation: %v", err)
		case <-deadline:
			t.Fatal("worker did not start")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation was not preserved: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not handle cancellation")
	}
	if _, err := os.Stat(marker + ".cleanup"); err != nil {
		t.Fatalf("worker was terminated before graceful cleanup: %v", err)
	}
}

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
