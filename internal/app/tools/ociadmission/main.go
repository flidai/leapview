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
	if err := runAdmission(os.Args[1:], os.Environ(), os.Stdout, os.Stderr); err != nil {
		var usage usageError
		if errors.As(err, &usage) {
			fmt.Fprintln(os.Stderr, usage.Error())
			os.Exit(64)
		}
		fmt.Fprintf(os.Stderr, "OCI admission rejected: %s\n", redactError(err))
		os.Exit(1)
	}
}
