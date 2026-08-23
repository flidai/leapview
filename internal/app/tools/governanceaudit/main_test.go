package main

import (
	"encoding/json"
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

	if !mainBranchPolicy(environment{DeploymentBranchPolicy: &deploymentBranchPolicy{ProtectedBranches: true}}) {
		t.Fatal("protected branch policy was rejected")
	}
	if !mainBranchPolicy(environment{CustomBranchPolicies: []customBranchPolicy{{Name: "main"}}}) {
		t.Fatal("named custom branch policy was rejected")
	}
	if mainBranchPolicy(environment{}) {
		t.Fatal("empty branch policy was accepted")
	}
}

func testSnapshot(t *testing.T, includeEnvironments, protectedBranches bool) Snapshot {
	t.Helper()
	branchPolicy := `"deployment_branch_policy":{"protected_branches":false}`
	if protectedBranches {
		branchPolicy = `"deployment_branch_policy":{"protected_branches":true}`
	}
	envJSON := `[]`
	if includeEnvironments {
		envJSON = `[
			{"name":"leapview-demo","protection_rules":[{"type":"required_reviewers"}],` + branchPolicy + `},
			{"name":"leapview-ephemeral-qualification","protection_rules":[{"type":"required_reviewers"}]},
			{"name":"leapview-site-production","protection_rules":[{"type":"required_reviewers"}],` + branchPolicy + `}
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
