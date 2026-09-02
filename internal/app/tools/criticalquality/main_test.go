package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseCoverage(t *testing.T) {
	observed, err := parseCoverage("ok example 0.1s coverage: 75.8% of statements\n")
	if err != nil {
		t.Fatal(err)
	}
	if observed != 75.8 {
		t.Fatalf("coverage = %.1f, want 75.8", observed)
	}
	if _, err := parseCoverage("ok example 0.1s\n"); err == nil {
		t.Fatal("missing coverage output was accepted")
	}
}

func TestCoverageBudgetRejectsARegression(t *testing.T) {
	expectations := policy{Version: policyVersion, Packages: []packageExpectation{
		{Path: "./internal/access", MinimumCoverage: 75.8},
		{Path: "./internal/runtimehost", MinimumCoverage: 64.8},
	}}
	execute := func(_ string, arguments ...string) (string, error) {
		path := arguments[len(arguments)-1]
		if path == "./internal/access" {
			return "coverage: 75.8% of statements", nil
		}
		return "coverage: 64.7% of statements", nil
	}
	err := checkCoverage(".", expectations, execute)
	if err == nil || !strings.Contains(err.Error(), "./internal/runtimehost coverage 64.7% is below 64.8%") {
		t.Fatalf("coverage regression error = %v", err)
	}
}

func TestRaceQualificationStopsAtTheFailingPackage(t *testing.T) {
	expectations := policy{Version: policyVersion, Packages: []packageExpectation{
		{Path: "./internal/access"},
		{Path: "./internal/runtimehost"},
	}}
	calls := 0
	execute := func(_ string, _ ...string) (string, error) {
		calls++
		if calls == 2 {
			return "race detected", errors.New("exit status 1")
		}
		return "ok", nil
	}
	if err := checkRace(".", expectations, execute); err == nil || !strings.Contains(err.Error(), "./internal/runtimehost") {
		t.Fatalf("race qualification error = %v", err)
	}
}

func TestArgumentsIncludeCoverageRaceAndBuildTags(t *testing.T) {
	expectation := packageExpectation{Path: "./internal/project/compiler", BuildTags: []string{"duckdb_arrow"}}
	if got, want := testArguments(expectation, "coverage"), []string{"test", "-count=1", "-cover", "-tags=duckdb_arrow", "./internal/project/compiler"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("coverage arguments = %#v, want %#v", got, want)
	}
	if got, want := testArguments(expectation, "race"), []string{"test", "-count=1", "-race", "-tags=duckdb_arrow", "./internal/project/compiler"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("race arguments = %#v, want %#v", got, want)
	}
}

func TestPolicyRequiresUniqueRelativePackagesAndValidFloors(t *testing.T) {
	tests := []policy{
		{Version: 2, Packages: []packageExpectation{{Path: "./internal/access"}}},
		{Version: policyVersion},
		{Version: policyVersion, Packages: []packageExpectation{{Path: "internal/access"}}},
		{Version: policyVersion, Packages: []packageExpectation{{Path: "./internal/access", MinimumCoverage: 101}}},
		{Version: policyVersion, Packages: []packageExpectation{{Path: "./internal/access"}, {Path: "./internal/access"}}},
		{Version: policyVersion, Packages: []packageExpectation{{Path: "./internal/project/compiler", BuildTags: []string{"duckdb arrow"}}}},
	}
	for index, expectations := range tests {
		if err := validatePolicy(expectations); err == nil {
			t.Fatalf("invalid policy %d was accepted: %#v", index, expectations)
		}
	}
}
