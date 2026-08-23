// Command securitypolicy validates repository dependency-security coverage.
//
// It is intentionally a small repository-owned tool rather than a CI-provider
// script: local checks and CI therefore exercise exactly the same contract.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/app/securitypolicy"
)

func main() {
	root := flag.String("root", ".", "repository root")
	asOf := flag.String("as-of", "", "UTC date (YYYY-MM-DD) used for exception expiry checks")
	emitExceptions := flag.Bool("exceptions-json", false, "emit validated exceptions as canonical JSON")
	matchScanner := flag.String("match-scanner", "", "scanner identity for an exact exception match")
	matchRule := flag.String("match-rule", "", "rule identity for an exact exception match")
	matchResource := flag.String("match-resource", "", "resource identity for an exact exception match")
	matchSeverity := flag.String("match-severity", "", "finding severity (HIGH and CRITICAL are never waivable)")
	matchClass := flag.String("match-class", "", "finding class (provenance and release-signing are never waivable)")
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
	matchRequested := anyMatchFlag(*matchScanner, *matchRule, *matchResource, *matchSeverity, *matchClass)
	var contract securitypolicy.Exceptions
	if *emitExceptions || matchRequested {
		contract, err = securitypolicy.LoadValidatedExceptions(absoluteRoot, now)
	} else {
		err = securitypolicy.ValidateRepository(absoluteRoot, now)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "security policy: %v\n", err)
		os.Exit(1)
	}

	if *emitExceptions {
		if matchRequested {
			fmt.Fprintln(os.Stderr, "security policy: exceptions-json cannot be combined with match flags")
			os.Exit(2)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(contract); err != nil {
			fmt.Fprintf(os.Stderr, "security policy: encode exceptions: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if matchRequested {
		if strings.TrimSpace(*matchScanner) == "" || strings.TrimSpace(*matchRule) == "" || strings.TrimSpace(*matchResource) == "" {
			fmt.Fprintln(os.Stderr, "security policy: match requires scanner, rule, and resource")
			os.Exit(2)
		}
		if exception, ok := contract.Match(securitypolicy.Finding{Scanner: *matchScanner, Rule: *matchRule, Resource: *matchResource, Severity: *matchSeverity, Class: *matchClass}); ok {
			fmt.Println(exception.ID)
			return
		}
		os.Exit(1)
	}
	fmt.Println("security policy: coverage, exceptions, and Dependabot updater coverage are valid")
}

func anyMatchFlag(values ...string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}
