// Command securitypolicy validates repository dependency-security coverage.
//
// It is intentionally a small repository-owned tool rather than a CI-provider
// script: local checks and CI therefore exercise exactly the same contract.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	root := flag.String("root", ".", "repository root")
	asOf := flag.String("as-of", "", "UTC date (YYYY-MM-DD) used for exception expiry checks")
	flag.Parse()

	now := time.Now().UTC()
	if *asOf != "" {
		parsed, err := time.Parse("2006-01-02", *asOf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "security policy: invalid -as-of date: %v\n", err)
			os.Exit(2)
		}
		now = parsed
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "security policy: resolve root: %v\n", err)
		os.Exit(2)
	}
	if err := ValidateRepository(absoluteRoot, now); err != nil {
		fmt.Fprintf(os.Stderr, "security policy: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("security policy: coverage, exceptions, and Dependabot updater coverage are valid")
}
