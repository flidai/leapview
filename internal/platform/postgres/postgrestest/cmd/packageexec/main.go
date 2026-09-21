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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "PostgreSQL package runner requires a test binary")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) (runErr error) {
	startupCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	container, adminURL, token, err := postgrestest.RunPackageServer(startupCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("start disposable PostgreSQL package server: %w", err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(cleanupCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("terminate PostgreSQL package server: %w", err))
		}
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
