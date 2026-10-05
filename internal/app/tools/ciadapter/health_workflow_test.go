package ciadapter

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	platformci "github.com/flidai/leapview/internal/platform/ci"
)

func TestBuildHealthWorkflowUsesRevisionNamesAndRequiredNeeds(t *testing.T) {
	caller := []byte(`name: Older workflow title
jobs:
  recovery:
    name: Old caller label
    uses: ./.github/workflows/recovery.yml
  optional:
    name: Optional diagnostics
  ci-gate:
    name: Old gate label
    needs: [recovery]
`)
	reusable := []byte(`name: Old reusable title
jobs:
  recovery:
    name: Old recovery label
  historical-transition:
    name: Old transition label
`)
	contract, err := BuildHealthWorkflow("merge-validation.yml", caller, map[string][]byte{
		"./.github/workflows/recovery.yml": reusable,
	}, platformci.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(contract.RequiredJobs, []string{"ci-gate", "recovery", "recovery/historical-transition", "recovery/recovery"}) {
		t.Fatalf("required jobs = %v", contract.RequiredJobs)
	}
	if got := contract.JobNames["Old caller label / Old transition label"]; got != "recovery/historical-transition" {
		t.Fatalf("historical display name maps to %q", got)
	}
	if got := contract.JobNames["Old recovery label"]; got == "recovery/recovery" {
		t.Fatal("a callee label without its caller was accepted")
	}
	if slices.Contains(contract.RequiredJobs, "optional") {
		t.Fatalf("optional job entered required inventory: %v", contract.RequiredJobs)
	}
}

func TestBuildHealthWorkflowResolvesSupportedMatricesFromSourceAndPlan(t *testing.T) {
	workflow := []byte(`jobs:
  frontend-validation:
    name: Frontend tests (PR, ${{ matrix.shard }})
    strategy:
      matrix: ${{ fromJSON(needs.prepare.outputs.frontend_matrix) }}
  go-packages-validation:
    name: Go package tests (merge queue)
  ci-gate:
    name: CI gate
    needs: [frontend-validation, go-packages-validation]
`)
	plan := platformci.Plan{
		Version: platformci.PRPlanVersion,
		PR:      &platformci.PRPlan{Effective: platformci.PRJobs{Frontend: []string{"reports", "site"}}},
	}
	contract, err := BuildHealthWorkflow("ci.yml", workflow, nil, plan)
	if err != nil {
		t.Fatal(err)
	}
	if contract.JobNames["Frontend tests (PR, reports)"] != "frontend-validation/reports" || contract.JobNames["Frontend tests (PR, site)"] != "frontend-validation/site" {
		t.Fatalf("dynamic matrix name mappings = %v", contract.JobNames)
	}
	if !slices.Contains(contract.RequiredJobs, "frontend-validation/reports") || !slices.Contains(contract.RequiredJobs, "frontend-validation/site") {
		t.Fatalf("selected matrix jobs absent from contract: %v", contract.RequiredJobs)
	}
	if got := contract.JobNames["Go package tests (merge queue)"]; got != "go-packages-validation" {
		t.Fatalf("job ID/name mapping = %q", got)
	}
}

func TestBuildHealthWorkflowRejectsUnsupportedMatrixAndMissingReusableSource(t *testing.T) {
	unsupported := []byte(`jobs:
  frontend-validation:
    name: Frontend tests (${{ matrix.shard }})
    strategy:
      matrix: ${{ fromJSON(inputs.matrix) }}
  ci-gate:
    needs: [frontend-validation]
`)
	if _, err := BuildHealthWorkflow("ci.yml", unsupported, nil, platformci.Plan{}); err == nil || !strings.Contains(err.Error(), "unsupported dynamic matrix") {
		t.Fatalf("unsupported matrix was not explicit: %v", err)
	}
	unsupportedAxes := []byte(`jobs:
  matrix:
    name: Tests (${{ matrix.shard }})
    strategy:
      matrix:
        shard: [one, two]
        os: [linux, macos]
  ci-gate:
    needs: [matrix]
`)
	if _, err := BuildHealthWorkflow("ci.yml", unsupportedAxes, nil, platformci.Plan{}); err == nil || !strings.Contains(err.Error(), "unsupported static matrix axes") {
		t.Fatalf("unsupported matrix axes were not explicit: %v", err)
	}
	unnamedMatrix := []byte(`jobs:
  matrix:
    name: Tests
    strategy:
      matrix:
        shard: [one, two]
  ci-gate:
    needs: [matrix]
`)
	if _, err := BuildHealthWorkflow("ci.yml", unnamedMatrix, nil, platformci.Plan{}); err == nil || !strings.Contains(err.Error(), "without a shard name") {
		t.Fatalf("matrix without source-derived names was accepted: %v", err)
	}
	missing := []byte(`jobs:
  recovery:
    name: Recovery
    uses: ./.github/workflows/recovery.yml
  ci-gate:
    needs: [recovery]
`)
	if _, err := BuildHealthWorkflow("ci.yml", missing, nil, platformci.Plan{}); err == nil || !strings.Contains(err.Error(), "missing reusable workflow") {
		t.Fatalf("missing historical reusable contract was not explicit: %v", err)
	}
}

func TestMaintainedWorkflowContractsParseAtTheirCheckedOutRevision(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	qualification, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "demo-upgrade-qualification.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"ci.yml", "merge-validation.yml", "nightly.yml"} {
		workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", filename))
		if err != nil {
			t.Fatal(err)
		}
		contract, err := BuildHealthWorkflow(filename, workflow, map[string][]byte{
			"./.github/workflows/demo-upgrade-qualification.yml": qualification,
		}, platformci.Plan{
			Version: platformci.PRPlanVersion,
			PR:      &platformci.PRPlan{Effective: platformci.FullPRJobs()},
		})
		if err != nil {
			t.Errorf("%s: %v", filename, err)
			continue
		}
		for _, required := range []string{
			"host-recovery-validation",
			"host-recovery-validation/recovery",
			"host-recovery-validation/historical-transition",
		} {
			if !slices.Contains(contract.RequiredJobs, required) {
				t.Errorf("%s gate contract omits required workflow job %s", filename, required)
			}
			if filename == "ci.yml" && !slices.Contains(contract.PlanIndependentJobs, required) {
				t.Errorf("%s contract omits plan-independent required job %s", filename, required)
			}
		}
	}
}
