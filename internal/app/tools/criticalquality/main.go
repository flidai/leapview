package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const policyVersion = 1

var coveragePattern = regexp.MustCompile(`coverage:\s+([0-9]+(?:\.[0-9]+)?)% of statements`)

type packageExpectation struct {
	Path            string   `json:"path"`
	MinimumCoverage float64  `json:"minimumCoverage"`
	BuildTags       []string `json:"buildTags,omitempty"`
}

type policy struct {
	Version  int                  `json:"version"`
	Packages []packageExpectation `json:"packages"`
}

type commandRunner func(root string, arguments ...string) (string, error)

func main() {
	if err := run(os.Args[1:], executeGo); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string, execute commandRunner) error {
	flags := flag.NewFlagSet("criticalquality", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	policyPath := flags.String("policy", ".quality/critical-packages.json", "critical-package policy")
	mode := flags.String("mode", "coverage", "qualification mode: coverage or race")
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	path := *policyPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(absoluteRoot, path)
	}
	expectations, err := readPolicy(path)
	if err != nil {
		return err
	}

	switch *mode {
	case "coverage":
		return checkCoverage(absoluteRoot, expectations, execute)
	case "race":
		return checkRace(absoluteRoot, expectations, execute)
	default:
		return fmt.Errorf("unsupported mode %q: want coverage or race", *mode)
	}
}

func readPolicy(path string) (policy, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return policy{}, fmt.Errorf("read critical-package policy: %w", err)
	}
	var expectations policy
	if err := json.Unmarshal(body, &expectations); err != nil {
		return policy{}, fmt.Errorf("decode critical-package policy: %w", err)
	}
	if err := validatePolicy(expectations); err != nil {
		return policy{}, err
	}
	return expectations, nil
}

func validatePolicy(expectations policy) error {
	if expectations.Version != policyVersion {
		return fmt.Errorf("critical-package policy version = %d, want %d", expectations.Version, policyVersion)
	}
	if len(expectations.Packages) == 0 {
		return errors.New("critical-package policy has no packages")
	}
	seen := make(map[string]bool, len(expectations.Packages))
	for _, expectation := range expectations.Packages {
		if expectation.Path == "" || !strings.HasPrefix(expectation.Path, "./") {
			return fmt.Errorf("critical package path %q must start with ./", expectation.Path)
		}
		if seen[expectation.Path] {
			return fmt.Errorf("critical package %s is declared more than once", expectation.Path)
		}
		seen[expectation.Path] = true
		if expectation.MinimumCoverage < 0 || expectation.MinimumCoverage > 100 {
			return fmt.Errorf("critical package %s has invalid coverage floor %.1f", expectation.Path, expectation.MinimumCoverage)
		}
		for _, tag := range expectation.BuildTags {
			if tag == "" || strings.ContainsAny(tag, ", \t\n") {
				return fmt.Errorf("critical package %s has invalid build tag %q", expectation.Path, tag)
			}
		}
	}
	return nil
}

func checkCoverage(root string, expectations policy, execute commandRunner) error {
	fmt.Println("Critical-package coverage")
	var problems []string
	for _, expectation := range expectations.Packages {
		output, err := execute(root, testArguments(expectation, "coverage")...)
		if err != nil {
			return fmt.Errorf("coverage qualification failed for %s: %w\n%s", expectation.Path, err, strings.TrimSpace(output))
		}
		observed, err := parseCoverage(output)
		if err != nil {
			return fmt.Errorf("parse coverage for %s: %w", expectation.Path, err)
		}
		fmt.Printf("- %s: %.1f%% (minimum %.1f%%)\n", expectation.Path, observed, expectation.MinimumCoverage)
		if observed+0.0001 < expectation.MinimumCoverage {
			problems = append(problems, fmt.Sprintf("%s coverage %.1f%% is below %.1f%%", expectation.Path, observed, expectation.MinimumCoverage))
		}
	}
	if len(problems) > 0 {
		return errors.New("critical-package coverage budget exceeded:\n- " + strings.Join(problems, "\n- "))
	}
	return nil
}

func checkRace(root string, expectations policy, execute commandRunner) error {
	fmt.Println("Critical-package race qualification")
	for _, expectation := range expectations.Packages {
		output, err := execute(root, testArguments(expectation, "race")...)
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			fmt.Println(trimmed)
		}
		if err != nil {
			return fmt.Errorf("race qualification failed for %s: %w", expectation.Path, err)
		}
	}
	return nil
}

func testArguments(expectation packageExpectation, mode string) []string {
	arguments := []string{"test", "-count=1"}
	if mode == "coverage" {
		arguments = append(arguments, "-cover")
	} else {
		arguments = append(arguments, "-race")
	}
	if len(expectation.BuildTags) > 0 {
		arguments = append(arguments, "-tags="+strings.Join(expectation.BuildTags, ","))
	}
	return append(arguments, expectation.Path)
}

func parseCoverage(output string) (float64, error) {
	matches := coveragePattern.FindStringSubmatch(output)
	if len(matches) != 2 {
		return 0, errors.New("go test output did not contain statement coverage")
	}
	value, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, fmt.Errorf("decode coverage %q: %w", matches[1], err)
	}
	return value, nil
}

func executeGo(root string, arguments ...string) (string, error) {
	command := exec.Command("go", arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	return string(output), err
}
