// Command governanceaudit checks a read-only snapshot of the GitHub repository
// governance settings. It deliberately has no write path: the default mode
// reads a JSON file and --live shells out to GET-only gh api calls.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	repository  = "flidai/leapview"
	mainRuleset = "main"
	mainRef     = "refs/heads/main"
)

// RequiredChecks are the stable status contexts required by the main ruleset.
// They intentionally do not include the workflow prefix: GitHub rulesets store
// the status context, not the display name of the workflow that emitted it.
var requiredChecks = []string{"CI gate", "Security gate"}

// TrustedBuilders is the allow-list documented in SECURITY.md. Workflow paths
// are compared exactly, while the display names make reports useful to people.
var trustedBuilders = []Builder{
	{Workflow: ".github/workflows/artifacts.yml", Name: "Main artifacts"},
	{Workflow: ".github/workflows/release.yml", Name: "Release image"},
	{Workflow: ".github/workflows/site-image.yml", Name: "Publish public site image"},
	{Workflow: ".github/workflows/electron-security-proof.yml", Name: "Electron security proof"},
	{Workflow: ".github/workflows/desktop-preview-release.yml", Name: "Desktop unsigned preview release"},
}

// GovernedEnvironments are the deployment environments whose settings are
// part of the repository security boundary. desktop-preview is a separate,
// unsigned evaluation channel and is intentionally not a production
// deployment environment in this audit.
var governedEnvironments = []EnvironmentContract{
	{Name: "leapview-demo", MainOnly: true, ReviewRequired: true},
	{Name: "leapview-ephemeral-qualification", MainOnly: false, ReviewRequired: true},
	{Name: "leapview-site-production", MainOnly: true, ReviewRequired: true},
}

type Builder struct {
	Workflow string `json:"workflow"`
	Name     string `json:"name"`
}

type EnvironmentContract struct {
	Name           string
	MainOnly       bool
	ReviewRequired bool
}

// Snapshot is deliberately small and matches the JSON returned by the two
// GitHub endpoints. Either endpoint response may be supplied as an array or as
// the normal wrapper object ({"rulesets": [...]} / {"environments": [...]}).
// Keeping this shape injectable makes an audit reproducible and network-free.
type Snapshot struct {
	Rulesets     json.RawMessage `json:"rulesets"`
	Environments json.RawMessage `json:"environments"`
}

type ruleset struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Target      string     `json:"target"`
	Enforcement string     `json:"enforcement"`
	Conditions  conditions `json:"conditions"`
	Rules       []rule     `json:"rules"`
}

type conditions struct {
	RefName refNameCondition `json:"ref_name"`
}

type refNameCondition struct {
	Include []string `json:"include"`
}

type rule struct {
	Type       string     `json:"type"`
	Parameters parameters `json:"parameters"`
}

type parameters struct {
	RequiredStatusChecks []statusCheck `json:"required_status_checks"`
}

type statusCheck struct {
	Context string `json:"context"`
}

type environment struct {
	Name                   string                  `json:"name"`
	ProtectionRules        []protectionRule        `json:"protection_rules"`
	DeploymentBranchPolicy *deploymentBranchPolicy `json:"deployment_branch_policy"`
	// GitHub's environments endpoint normally exposes whether custom policies
	// are enabled inside deployment_branch_policy. Tests and offline exports
	// may additionally include the named policies returned by the subordinate
	// endpoint; accept both shapes without weakening the main-branch check.
	CustomBranchPolicies customBranchPolicies `json:"custom_branch_policies"`
}

type protectionRule struct {
	Type              string `json:"type"`
	WaitTimer         int    `json:"wait_timer"`
	PreventSelfReview bool   `json:"prevent_self_review"`
}

type deploymentBranchPolicy struct {
	ProtectedBranches    bool `json:"protected_branches"`
	CustomBranchPolicies bool `json:"custom_branch_policies"`
}

type customBranchPolicy struct {
	Name string `json:"name"`
}

type customBranchPolicies []customBranchPolicy

func (p *customBranchPolicies) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*p = nil
		return nil
	}
	if bytes.Equal(data, []byte("false")) || bytes.Equal(data, []byte("true")) {
		// The environments endpoint uses this boolean in its summary shape.
		*p = nil
		return nil
	}
	var values []customBranchPolicy
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*p = values
	return nil
}

type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Report struct {
	OK       bool      `json:"ok"`
	Findings []Finding `json:"findings,omitempty"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr *os.File) error {
	flags := flag.NewFlagSet("governanceaudit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	snapshotPath := flags.String("snapshot", "", "JSON snapshot containing rulesets and environments")
	live := flags.Bool("live", false, "read rulesets and environments with GET-only gh api calls")
	repo := flags.String("repo", repository, "GitHub owner/repository used by --live")
	jsonOutput := flags.Bool("json", false, "emit a machine-readable report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *live && *snapshotPath != "" {
		return errors.New("--snapshot and --live are mutually exclusive")
	}
	var snapshot Snapshot
	var err error
	switch {
	case *live:
		snapshot, err = readLiveSnapshot(context.Background(), *repo)
	case *snapshotPath != "":
		snapshot, err = readSnapshotFile(*snapshotPath)
	default:
		err = errors.New("one of --snapshot or --live is required; refusing an implicit network request")
	}
	if err != nil {
		return err
	}
	report, err := Audit(snapshot)
	if err != nil {
		return err
	}
	if *jsonOutput {
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		_, err = fmt.Fprintln(stdout, string(data))
		if err != nil {
			return err
		}
	} else {
		if report.OK {
			_, err = fmt.Fprintln(stdout, "governance audit: PASS")
		} else {
			_, err = fmt.Fprintln(stdout, "governance audit: FAIL")
			for _, finding := range report.Findings {
				_, _ = fmt.Fprintf(stdout, "- %s: %s\n", finding.Code, finding.Message)
			}
		}
		if err != nil {
			return err
		}
	}
	if !report.OK {
		return errors.New("governance audit failed")
	}
	return nil
}

func readSnapshotFile(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode snapshot %q: %w", path, err)
	}
	return snapshot, nil
}

func readLiveSnapshot(ctx context.Context, repo string) (Snapshot, error) {
	if strings.TrimSpace(repo) == "" {
		return Snapshot{}, errors.New("--repo cannot be empty")
	}
	rulesetSummaries, err := ghGet(ctx, repo, "rulesets")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read rulesets with gh: %w", err)
	}
	summaries, err := decodeRulesets(rulesetSummaries)
	if err != nil {
		return Snapshot{}, fmt.Errorf("decode live ruleset summaries: %w", err)
	}
	rulesets := make([]json.RawMessage, 0, 1)
	for _, summary := range summaries {
		if summary.Name != mainRuleset {
			continue
		}
		if summary.ID == 0 {
			return Snapshot{}, errors.New("live main ruleset summary has no id")
		}
		detail, getErr := ghGet(ctx, repo, fmt.Sprintf("rulesets/%d", summary.ID))
		if getErr != nil {
			return Snapshot{}, fmt.Errorf("read main ruleset with gh: %w", getErr)
		}
		rulesets = append(rulesets, json.RawMessage(detail))
	}
	rulesetsJSON, err := json.Marshal(rulesets)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode live rulesets: %w", err)
	}

	environments := make([]json.RawMessage, 0, len(governedEnvironments))
	for _, contract := range governedEnvironments {
		detail, getErr := ghGet(ctx, repo, "environments/"+contract.Name)
		if getErr != nil {
			return Snapshot{}, fmt.Errorf("read environment %q with gh: %w", contract.Name, getErr)
		}
		environments = append(environments, json.RawMessage(detail))
	}
	environmentsJSON, err := json.Marshal(environments)
	if err != nil {
		return Snapshot{}, fmt.Errorf("encode live environments: %w", err)
	}
	return Snapshot{Rulesets: rulesetsJSON, Environments: environmentsJSON}, nil
}

func ghGet(ctx context.Context, repo, resource string) ([]byte, error) {
	// Keep the command explicit and GET-only. No token or response body is
	// written by this tool; gh obtains credentials from its normal environment.
	cmd := exec.CommandContext(ctx, "gh", "api", "--method", "GET", "repos/"+repo+"/"+resource)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	return stdout.Bytes(), nil
}

// Audit validates the injected endpoint responses against the documented
// contract. It returns findings rather than stopping at the first mismatch so
// a reviewer can correct all governance drift in one read-only run.
func Audit(snapshot Snapshot) (Report, error) {
	rulesets, err := decodeRulesets(snapshot.Rulesets)
	if err != nil {
		return Report{}, fmt.Errorf("decode rulesets: %w", err)
	}
	environments, err := decodeEnvironments(snapshot.Environments)
	if err != nil {
		return Report{}, fmt.Errorf("decode environments: %w", err)
	}
	findings := make([]Finding, 0)
	main := findMainRuleset(rulesets)
	if main == nil {
		findings = append(findings, Finding{"ruleset.missing", "active ruleset named main does not protect refs/heads/main"})
	} else {
		if main.Enforcement != "active" {
			findings = append(findings, Finding{"ruleset.inactive", "ruleset main must have enforcement active"})
		}
		if main.Target != "branch" {
			findings = append(findings, Finding{"ruleset.target", "ruleset main must target branches"})
		}
		checks := requiredStatusChecks(*main)
		for _, required := range requiredChecks {
			if !checks[required] {
				findings = append(findings, Finding{"ruleset.required_check", fmt.Sprintf("ruleset main is missing required status context %q", required)})
			}
		}
		for check := range checks {
			if !contains(requiredChecks, check) {
				findings = append(findings, Finding{"ruleset.unexpected_check", fmt.Sprintf("ruleset main contains undocumented required status context %q", check)})
			}
		}
	}

	environmentsByName := make(map[string]environment, len(environments))
	for _, env := range environments {
		environmentsByName[env.Name] = env
	}
	for _, contract := range governedEnvironments {
		env, ok := environmentsByName[contract.Name]
		if !ok {
			findings = append(findings, Finding{"environment.missing", fmt.Sprintf("governed environment %q is missing", contract.Name)})
			continue
		}
		if contract.ReviewRequired && !hasRequiredReviewer(env.ProtectionRules) {
			findings = append(findings, Finding{"environment.review", fmt.Sprintf("environment %q must require a reviewer", env.Name)})
		}
		if contract.MainOnly && !mainBranchPolicy(env) {
			findings = append(findings, Finding{"environment.branch_policy", fmt.Sprintf("environment %q must be restricted to the protected main branch", env.Name)})
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Code == findings[j].Code {
			return findings[i].Message < findings[j].Message
		}
		return findings[i].Code < findings[j].Code
	})
	return Report{OK: len(findings) == 0, Findings: findings}, nil
}

func findMainRuleset(rulesets []ruleset) *ruleset {
	for i := range rulesets {
		candidate := &rulesets[i]
		if candidate.Name != mainRuleset {
			continue
		}
		for _, include := range candidate.Conditions.RefName.Include {
			if include == mainRef || include == "main" {
				return candidate
			}
		}
	}
	return nil
}

func requiredStatusChecks(value ruleset) map[string]bool {
	checks := make(map[string]bool)
	for _, candidate := range value.Rules {
		if candidate.Type != "required_status_checks" {
			continue
		}
		for _, check := range candidate.Parameters.RequiredStatusChecks {
			if strings.TrimSpace(check.Context) != "" {
				checks[check.Context] = true
			}
		}
	}
	return checks
}

func hasRequiredReviewer(rules []protectionRule) bool {
	for _, rule := range rules {
		if rule.Type == "required_reviewers" {
			return true
		}
	}
	return false
}

func mainBranchPolicy(env environment) bool {
	if env.DeploymentBranchPolicy != nil && env.DeploymentBranchPolicy.ProtectedBranches {
		return true
	}
	for _, policy := range env.CustomBranchPolicies {
		if policy.Name == "main" || policy.Name == mainRef {
			return true
		}
	}
	return false
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func decodeRulesets(raw json.RawMessage) ([]ruleset, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("rulesets is required")
	}
	var list []ruleset
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var wrapper struct {
		Rulesets []ruleset `json:"rulesets"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Rulesets == nil {
		return nil, errors.New("rulesets array is missing")
	}
	return wrapper.Rulesets, nil
}

func decodeEnvironments(raw json.RawMessage) ([]environment, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("environments is required")
	}
	var list []environment
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var wrapper struct {
		Environments []environment `json:"environments"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Environments == nil {
		return nil, errors.New("environments array is missing")
	}
	return wrapper.Environments, nil
}
