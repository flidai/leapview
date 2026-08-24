// Command securitysource runs the repository's source and secret security
// gates.  It intentionally keeps scanner invocation in Go so local checks and
// CI share one bounded, fail-closed implementation.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	trivyImage       = "aquasec/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969"
	gitleaksVersion  = "v8.30.1"
	defaultBaseRef   = "origin/main"
	defaultTimeout   = 15 * time.Minute
	securityPolicyGo = "internal/app/tools/securitypolicy/main.go"
)

// Config controls one source-security run.  Timeout applies independently to
// each external command; a parent context may impose a shorter deadline.
type Config struct {
	Root    string
	BaseRef string
	Timeout time.Duration
	Stdout  io.Writer
	Stderr  io.Writer
	Now     time.Time
}

func main() {
	root := flag.String("root", ".", "repository root")
	baseRef := flag.String("base-ref", "", "Git baseline for candidate-history scanning")
	timeout := flag.Duration("timeout", defaultTimeout, "maximum duration for each external command")
	flag.Parse()

	if *baseRef == "" {
		*baseRef = os.Getenv("SECURITY_GITLEAKS_BASE_REF")
		if *baseRef == "" {
			*baseRef = defaultBaseRef
		}
	}
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "security source: timeout must be positive")
		os.Exit(2)
	}

	cfg := Config{
		Root:    *root,
		BaseRef: *baseRef,
		Timeout: *timeout,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Now:     time.Now().UTC(),
	}
	if err := Run(context.Background(), cfg); err != nil {
		fmt.Fprintf(os.Stderr, "security source: %v\n", err)
		os.Exit(1)
	}
}
