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
	refreshFlag := flag.Bool("refresh-javascript-evidence", false, "run live JavaScript audits and atomically refresh vulnerability evidence")
	binaryFlag := flag.String("binary", "", "inspect an exact Linux Go ELF binary without executing it")
	programFlag := flag.String("binary-package", "", "expected main package of the exact binary")
	platformFlag := flag.String("binary-platform", "", "expected binary platform: linux/amd64 or linux/arm64")
	evidenceFlag := flag.String("binary-evidence", "", "binary report directory (must be new for a scan)")
	verifyFlag := flag.Bool("verify-binary-evidence", false, "verify existing exact-binary evidence offline")
	flag.Parse()
	if flag.NArg() != 0 {
		fail("dependency security", errors.New("unexpected positional arguments"))
	}
	binaryOperation := *binaryFlag != "" || *programFlag != "" || *platformFlag != "" || *evidenceFlag != "" || *verifyFlag
	if binaryOperation && (*binaryFlag == "" || *programFlag == "" || *platformFlag == "" || *evidenceFlag == "" || *refreshFlag) {
		fail("dependency security", errors.New("binary mode requires binary, package, platform and evidence directory; JavaScript refresh is a separate operation"))
	}
	root, err := resolveRoot(*rootFlag)
	if err != nil {
		fail("dependency security: resolve repository root", err)
	}
	if *timeoutFlag <= 0 {
		fail("dependency security: invalid timeout", errors.New("timeout must be positive"))
	}
	runner := &runner{root: root, timeout: *timeoutFlag, stdout: os.Stdout, stderr: os.Stderr}
	if binaryOperation {
		binary, err := filepath.Abs(*binaryFlag)
		if err != nil {
			fail("dependency security", err)
		}
		run := runner.scanBinaryEvidence
		if *verifyFlag {
			run = runner.verifyBinaryEvidence
		}
		if err := run(binary, *programFlag, *platformFlag, *evidenceFlag); err != nil {
			fail("dependency security: exact binary", err)
		}
		return
	}
	run := runner.run
	if *refreshFlag {
		run = runner.runRefresh
	}
	if err := run(); err != nil {
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
