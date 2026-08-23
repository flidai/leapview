package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditPassesDocumentedSnapshot(t *testing.T) {
	t.Parallel()

	report, err := Audit(testSnapshot(t, true, true))
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("report = %#v", report)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("findings = %#v", report.Findings)
	}
}

func TestAuditReportsEveryMissingGovernanceBoundary(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot(t, true, false)
	snapshot.Environments = json.RawMessage(`[
		{"name":"leapview-demo","protection_rules":[],"deployment_branch_policy":{"protected_branches":false}},
		{"name":"leapview-site-production","protection_rules":[],"deployment_branch_policy":{"protected_branches":false}}
	]`)
	// Keep one active main ruleset but remove both required contexts.
	snapshot.Rulesets = json.RawMessage(`[{
		"name":"main","target":"branch","enforcement":"active",
		"conditions":{"ref_name":{"include":["refs/heads/main"]}},
		"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[]}}]
	}]`)
	report, err := Audit(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("invalid snapshot passed")
	}
	wantCodes := []string{
		"environment.branch_policy",
		"environment.missing",
		"environment.review",
		"ruleset.required_check",
	}
	gotCodes := make([]string, 0, len(report.Findings))
	for _, finding := range report.Findings {
		gotCodes = append(gotCodes, finding.Code)
	}
	joined := strings.Join(gotCodes, "\n")
	for _, want := range wantCodes {
		if !strings.Contains(joined, want) {
			t.Errorf("findings %v do not contain %q", gotCodes, want)
		}
	}
	// Both contexts are independently required.
	if countCode(report.Findings, "ruleset.required_check") != len(requiredChecks) {
		t.Fatalf("required-check findings = %#v", report.Findings)
	}
}

func TestAuditRejectsMissingSnapshotSections(t *testing.T) {
	t.Parallel()

	_, err := Audit(Snapshot{Rulesets: json.RawMessage(`[]`)})
	if err == nil || !strings.Contains(err.Error(), "decode environments") {
		t.Fatalf("err = %v", err)
	}
}

func TestDecodeEndpointWrappersAndArrays(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot(t, true, true)
	var rulesets []json.RawMessage
	if err := json.Unmarshal(snapshot.Rulesets, &rulesets); err != nil {
		t.Fatal(err)
	}
	snapshot.Rulesets = json.RawMessage(`{"rulesets":` + string(snapshot.Rulesets) + `}`)
	snapshot.Environments = json.RawMessage(`{"environments":` + string(snapshot.Environments) + `}`)
	report, err := Audit(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("wrapped report = %#v", report)
	}
}

func TestFindMainRulesetFindsMisconfiguredMainForSpecificFindings(t *testing.T) {
	t.Parallel()

	rulesets, err := decodeRulesets(json.RawMessage(`[
		{"name":"main","target":"branch","enforcement":"disabled","conditions":{"ref_name":{"include":["refs/heads/main"]}}},
		{"name":"main","target":"tag","enforcement":"active","conditions":{"ref_name":{"include":["refs/heads/main"]}}}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if findMainRuleset(rulesets) == nil {
		t.Fatal("misconfigured main ruleset was not found")
	}
}

func TestAuditRejectsUndocumentedRequiredCheck(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot(t, true, true)
	snapshot.Rulesets = json.RawMessage(`[{"name":"main","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["refs/heads/main"]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI gate"},{"context":"Security gate"},{"context":"unreviewed check"}]}}]}]`)
	report, err := Audit(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || countCode(report.Findings, "ruleset.unexpected_check") != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestMainBranchPolicyAcceptsProtectedOrNamedCustomPolicy(t *testing.T) {
	t.Parallel()

	if mainBranchPolicy(environment{DeploymentBranchPolicy: &deploymentBranchPolicy{ProtectedBranches: true}}) {
		t.Fatal("protected branch policy was accepted as exact main-only policy")
	}
	if !mainBranchPolicy(environment{
		DeploymentBranchPolicy: &deploymentBranchPolicy{CustomBranchPolicies: true},
		CustomBranchPolicies:   []customBranchPolicy{{Name: "main", Type: "branch"}},
	}) {
		t.Fatal("named custom branch policy was rejected")
	}
	if mainBranchPolicy(environment{}) {
		t.Fatal("empty branch policy was accepted")
	}
	if mainBranchPolicy(environment{
		DeploymentBranchPolicy: &deploymentBranchPolicy{CustomBranchPolicies: true},
		CustomBranchPolicies: []customBranchPolicy{
			{Name: "main", Type: "branch"},
			{Name: "release", Type: "branch"},
		},
	}) {
		t.Fatal("multiple custom branch policies were accepted as exact main-only policy")
	}
	if mainBranchPolicy(environment{
		DeploymentBranchPolicy: &deploymentBranchPolicy{CustomBranchPolicies: true},
		CustomBranchPolicies:   []customBranchPolicy{{Name: "main", Type: "tag"}},
	}) {
		t.Fatal("tag policy was accepted as exact main-only policy")
	}
}

func TestAuditRequiresReviewerSelfReviewProtectionAndReviewer(t *testing.T) {
	t.Parallel()

	snapshot := testSnapshot(t, true, true)
	snapshot.Environments = json.RawMessage(`[
		{"name":"leapview-demo","protection_rules":[{"type":"required_reviewers","prevent_self_review":false,"reviewers":[]}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true},"custom_branch_policies":[{"name":"main","type":"branch"}]},
		{"name":"leapview-ephemeral-qualification","protection_rules":[{"type":"required_reviewers","prevent_self_review":false,"reviewers":[]}]},
		{"name":"leapview-site-production","protection_rules":[{"type":"required_reviewers","prevent_self_review":false,"reviewers":[]}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true},"custom_branch_policies":[{"name":"main","type":"branch"}]}
	]`)
	report, err := Audit(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("reviewer drift passed")
	}
	if countCode(report.Findings, "environment.review.prevent_self_review") != 3 {
		t.Fatalf("findings = %#v", report.Findings)
	}
	if countCode(report.Findings, "environment.review.reviewer") != 3 {
		t.Fatalf("findings = %#v", report.Findings)
	}
}

func TestReadLiveSnapshotFetchesEnabledCustomBranchPolicies(t *testing.T) {
	binDir := t.TempDir()
	ghPath := filepath.Join(binDir, "gh")
	ghScript := `#!/bin/sh
case "$4" in
  repos/flidai/leapview/rulesets) printf '[{"name":"main","id":1}]' ;;
  repos/flidai/leapview/rulesets/1) printf '{"name":"main","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["refs/heads/main"]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI gate"},{"context":"Security gate"}]}}]}' ;;
  repos/flidai/leapview/environments/leapview-demo|repos/flidai/leapview/environments/leapview-site-production) printf '{"name":"%s","protection_rules":[{"type":"required_reviewers","prevent_self_review":true,"reviewers":[{"type":"User","id":1}]}],"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}' "$(basename "$4")" ;;
  repos/flidai/leapview/environments/leapview-ephemeral-qualification) printf '{"name":"leapview-ephemeral-qualification","protection_rules":[{"type":"required_reviewers","prevent_self_review":true,"reviewers":[{"type":"User","id":1}]}]}' ;;
  repos/flidai/leapview/environments/*/deployment-branch-policies) printf '{"total_count":1,"branch_policies":[{"name":"main","type":"branch"}]}' ;;
  *) echo "unexpected gh resource: $4" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(ghPath, []byte(ghScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	snapshot, err := readLiveSnapshot(t.Context(), "flidai/leapview")
	if err != nil {
		t.Fatal(err)
	}
	report, err := Audit(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("live report = %#v", report)
	}
	var environments []environment
	if err := json.Unmarshal(snapshot.Environments, &environments); err != nil {
		t.Fatal(err)
	}
	for _, env := range environments {
		if env.Name == "leapview-demo" || env.Name == "leapview-site-production" {
			if len(env.CustomBranchPolicies) != 1 || env.CustomBranchPolicies[0].Name != "main" || env.CustomBranchPolicies[0].Type != "branch" {
				t.Fatalf("environment %q custom policies = %#v", env.Name, env.CustomBranchPolicies)
			}
		}
	}
}

func testSnapshot(t *testing.T, includeEnvironments, protectedBranches bool) Snapshot {
	t.Helper()
	branchPolicy := `"deployment_branch_policy":{"protected_branches":false}`
	if protectedBranches {
		branchPolicy = `"deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true},"custom_branch_policies":[{"name":"main","type":"branch"}]`
	}
	envJSON := `[]`
	if includeEnvironments {
		envJSON = `[
			{"name":"leapview-demo","protection_rules":[{"type":"required_reviewers","prevent_self_review":true,"reviewers":[{"type":"User","id":1}]}],` + branchPolicy + `},
			{"name":"leapview-ephemeral-qualification","protection_rules":[{"type":"required_reviewers","prevent_self_review":true,"reviewers":[{"type":"User","id":1}]}]},
			{"name":"leapview-site-production","protection_rules":[{"type":"required_reviewers","prevent_self_review":true,"reviewers":[{"type":"User","id":1}]}],` + branchPolicy + `}
		]`
	}
	return Snapshot{
		Rulesets:     json.RawMessage(`[{"name":"main","target":"branch","enforcement":"active","conditions":{"ref_name":{"include":["refs/heads/main"]}},"rules":[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"CI gate"},{"context":"Security gate"}]}}]}]`),
		Environments: json.RawMessage(envJSON),
	}
}

func countCode(findings []Finding, code string) int {
	count := 0
	for _, finding := range findings {
		if finding.Code == code {
			count++
		}
	}
	return count
}
