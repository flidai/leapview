// Package main runs one Go test package against one disposable PostgreSQL server.
// The test binary still creates and drops its own databases and roles per test.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
)

const packageWorkerFlag = "--postgres-package-worker"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "PostgreSQL package runner requires a test binary")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	var err error
	if args[0] == packageWorkerFlag {
		if len(args) < 2 {
			err = errors.New("PostgreSQL package worker requires a test binary")
		} else {
			err = run(ctx, args[1:])
		}
	} else {
		err = runIsolated(ctx, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// The SDK derives its cleanup session from the parent PID. Go's -exec workers
// otherwise share the go test PID, so a reaper shutting down between packages
// can be reused by the next worker. Keep a distinct launcher alive for each
// server worker; its reaper then owns only that package's lifetime.
func runIsolated(ctx context.Context, args []string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, append([]string{packageWorkerFlag}, args...)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	// Give the worker its existing 30-second explicit cleanup deadline before
	// terminating an unresponsive process after cancellation.
	command.WaitDelay = 40 * time.Second
	if err := command.Run(); err != nil {
		return fmt.Errorf("run isolated PostgreSQL package worker: %w", err)
	}
	return nil
}

func run(ctx context.Context, args []string) (runErr error) {
	startupCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	container, adminURL, token, err := postgrestest.RunPackageServer(startupCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("start disposable PostgreSQL package server: %w", err)
	}
	defer func() {
		runErr = finishPackageServer(runErr, func(cleanupCtx context.Context) error {
			return container.Terminate(cleanupCtx)
		})
	}()

	command := exec.CommandContext(ctx, args[0], packageTestArguments(args[1:])...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Env = packageTestEnvironment(adminURL, token)
	if err := command.Run(); err != nil {
		return fmt.Errorf("run PostgreSQL package tests: %w", err)
	}
	return nil
}

func finishPackageServer(runErr error, terminate func(context.Context) error) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := terminate(cleanupCtx); err != nil {
		return errors.Join(runErr, fmt.Errorf("terminate PostgreSQL package server: %w", err))
	}
	return runErr
}

// PostgreSQL roles are cluster-wide and several conformance tests assert
// their exact production names. Keep tests serial inside a package while the
// conformance runner continues to parallelize independent package servers.
// Append this after Go's flags so an inherited -test.parallel cannot override
// the shared-server isolation rule.
func packageTestArguments(args []string) []string {
	return append(append([]string(nil), args...), "-test.parallel=1")
}

func packageTestEnvironment(adminURL, token string) []string {
	return append(os.Environ(),
		"LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1",
		postgrestest.PackageServerURLEnv+"="+adminURL,
		postgrestest.PackageServerTokenEnv+"="+token,
	)
}
