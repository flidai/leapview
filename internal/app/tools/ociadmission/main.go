// Command ociadmission admits a repository-owned OCI artifact only when its
// immutable digest, provenance, SBOM, and vulnerability evidence satisfy the
// repository contract.
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	run := func() error {
		if len(os.Args) > 1 && os.Args[1] == "verify-nix-pair" {
			return runNixPair(os.Args[2:], os.Stdout, os.Stderr)
		}
		if len(os.Args) > 1 && os.Args[1] == "admit-nix" {
			return runNixAdmission(os.Args[2:], os.Environ(), os.Stdout, os.Stderr)
		}
		if len(os.Args) > 1 && os.Args[1] == "verify-receipt" {
			return runVerifyReceipt(os.Args[2:], os.Stdout, os.Stderr)
		}
		return runAdmission(os.Args[1:], os.Environ(), os.Stdout, os.Stderr)
	}
	if err := run(); err != nil {
		var usage usageError
		if errors.As(err, &usage) {
			fmt.Fprintln(os.Stderr, redactError(usage, os.Environ()))
			os.Exit(64)
		}
		fmt.Fprintf(os.Stderr, "OCI admission rejected: %s\n", redactError(err, os.Environ()))
		os.Exit(1)
	}
}
