// Command securitydependencies runs the repository-owned dependency security
// scanners with a bounded lifetime and fail-closed result contract.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	rootFlag := flag.String("root", "", "repository root (defaults to the git top-level)")
	timeoutFlag := flag.Duration("timeout", defaultTimeout, "maximum duration for each scanner command")
	flag.Parse()
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fail("dependency security: resolve repository root", err)
	}
	if *timeoutFlag <= 0 {
		fail("dependency security: invalid timeout", errors.New("timeout must be positive"))
	}
	runner := &runner{root: root, timeout: *timeoutFlag, scanBudget: defaultScanBudget, stdout: os.Stdout, stderr: os.Stderr}
	if err := runner.run(); err != nil {
		fail("dependency security", err)
	}
}

func fail(prefix string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", prefix, err)
	os.Exit(1)
}

func resolveRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		root, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return root, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), rootLookupTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", errors.New("git returned an empty repository root")
	}
	return filepath.Abs(root)
}
