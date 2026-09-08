package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	platformci "github.com/flidai/leapview/internal/platform/ci"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "gate" {
		if err := runGate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := runPlan(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runPlan(args []string) error {
	flags := flag.NewFlagSet("ciplan", flag.ContinueOnError)
	event := flags.String("event", "", "GitHub event name")
	base := flags.String("base", "", "base commit")
	head := flags.String("head", "", "tested candidate commit")
	stackBase := flags.String("stack-base", "", "cumulative stack target ref")
	deferred := flags.Bool("deferred", false, "defer lower stack layer")
	runID := flags.String("run-id", "", "GitHub run identity")
	attempt := flags.String("attempt", "", "GitHub attempt identity")
	prNumber := flags.Int("pr-number", 0, "pull request number")
	labels := flags.String("labels", "", "comma-separated pull request labels")
	output := flags.String("output", "ci-plan.json", "plan JSON output")
	githubOutput := flags.String("github-output", "", "GitHub Actions output file")
	githubSummary := flags.String("github-summary", "", "GitHub Actions summary file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *event == "" {
		return errors.New("--event is required")
	}

	if *head != "" {
		resolved, err := resolveCommit(*head)
		if err != nil {
			return err
		}
		*head = resolved
	}
	if *stackBase != "" {
		resolved, err := resolveCommit(*stackBase)
		if err != nil {
			return err
		}
		data, err := exec.Command("git", "merge-base", resolved, *head).Output()
		if err != nil {
			return fmt.Errorf("resolve cumulative stack base: %w", err)
		}
		*base = strings.TrimSpace(string(data))
	}
	if *base != "" {
		resolved, err := resolveCommit(*base)
		if err != nil {
			return err
		}
		*base = resolved
	}
	var changes []platformci.Change
	if *event == "pull_request" {
		if *base == "" || *head == "" {
			return errors.New("--base and --head are required for pull_request")
		}
		diff, err := gitDiff(*base, *head)
		if err != nil {
			return err
		}
		changes, err = platformci.ParseNameStatusZ(diff)
		if err != nil {
			return err
		}
	}
	plan := platformci.PlanChanges(platformci.Input{
		Event:             *event,
		PullRequestNumber: *prNumber,
		Labels:            splitLabels(*labels),
	}, changes)
	plan.PR.Base = *base
	plan.PR.Head = *head
	plan.PR.RunID = *runID
	plan.PR.Attempt = *attempt
	if *deferred {
		if *event != "pull_request" {
			return errors.New("only PR stack layers can defer")
		}
		plan.PR.Deferred = true
		plan.PR.Effective = platformci.PRJobs{}
		plan.Reason = "Validation is deferred to the top of this stack."
	}
	if err := platformci.ValidatePRPlan(plan); err != nil {
		return err
	}
	planJSON, err := plan.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, append(planJSON, '\n'), 0o644); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	if *githubOutput != "" {
		if err := writeGitHubOutputs(*githubOutput, plan); err != nil {
			return err
		}
	}
	if *githubSummary != "" {
		if err := appendSummary(*githubSummary, plan); err != nil {
			return err
		}
	}
	fmt.Println(string(planJSON))
	return nil
}

func runGate(args []string) error {
	flags := flag.NewFlagSet("ciplan gate", flag.ContinueOnError)
	planPath := flags.String("plan", "ci-plan.json", "plan JSON")
	resultsPath := flags.String("results", "", "job results JSON")
	expectedHead := flags.String("expected-head", "", "tested candidate")
	expectedRun := flags.String("expected-run-id", "", "run identity")
	expectedAttempt := flags.String("expected-attempt", "", "attempt identity")
	expectedDeferred := flags.Bool("expected-deferred", false, "event stack deferral")
	frontendMatrix := flags.String("frontend-matrix", "", "matrix consumed by the workflow")
	githubSummary := flags.String("github-summary", "", "GitHub Actions summary file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *resultsPath == "" {
		return errors.New("--results is required")
	}
	var plan platformci.Plan
	if err := readJSON(*planPath, &plan); err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	results := map[string]string{}
	if err := readJSON(*resultsPath, &results); err != nil {
		return fmt.Errorf("read results: %w", err)
	}
	if err := platformci.ValidatePRPlan(plan); err != nil {
		return err
	}
	if *expectedHead == "" || *expectedRun == "" || *expectedAttempt == "" {
		return errors.New("gate requires candidate/run/attempt identity")
	}
	if plan.PR.Head != *expectedHead || plan.PR.RunID != *expectedRun || plan.PR.Attempt != *expectedAttempt || plan.PR.Deferred != *expectedDeferred {
		return errors.New("plan identity or stack deferral does not match this event")
	}
	if *frontendMatrix != prFrontendMatrix(plan) {
		return errors.New("workflow frontend matrix differs from plan")
	}
	report := platformci.EvaluatePlanGate(plan, results)
	if *githubSummary != "" {
		if err := appendGateSummary(*githubSummary, report); err != nil {
			return err
		}
	}
	if report.OK {
		fmt.Println("CI gate passed")
		return nil
	}
	message := "CI gate rejected workflow:\n- " + strings.Join(report.Problems, "\n- ")
	if len(report.AuditMisses) > 0 {
		message += "\nselection audit misses: " + strings.Join(report.AuditMisses, ", ")
	}
	return errors.New(message)
}

func resolveCommit(ref string) (string, error) {
	data, err := exec.Command("git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("resolve commit %q: %w", ref, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func gitDiff(base, head string) ([]byte, error) {
	command := exec.Command("git", "diff", "--name-status", "-z", "--find-renames", base, head)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s..%s: %w: %s", base, head, err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

func writeGitHubOutputs(filename string, plan platformci.Plan) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output: %w", err)
	}
	defer file.Close()

	selected := plan.Effective.Selected()
	if plan.PR != nil {
		selected = plan.PR.Effective.Selected()
	}
	if plan.PR != nil {
		for _, name := range sortedJobNames(selected) {
			if _, err := fmt.Fprintf(file, "%s=%t\n", strings.ReplaceAll(name, "-", "_"), selected[name]); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(file, "frontend_matrix=%s\n", prFrontendMatrix(plan))
		return err
	}
	for _, name := range []string{
		"prepare", "frontend-prepare", "docs", "go-tests", "frontend-tests",
		"go-analysis", "ui-route-qa", "node-audit", "go-vuln", "site-image",
		"production-image", "deployment-contracts",
	} {
		key := strings.ReplaceAll(name, "-", "_")
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, strconv.FormatBool(selected[name])); err != nil {
			return err
		}
	}
	goInclude := plan.Effective.GoMatrix
	if len(goInclude) == 0 {
		goInclude = []platformci.GoShard{{Name: "not-selected"}}
	}
	goMatrix, err := json.Marshal(map[string]any{"include": goInclude})
	if err != nil {
		return err
	}
	frontendInclude := make([]map[string]string, 0, len(plan.Effective.Frontend))
	for _, name := range plan.Effective.Frontend {
		frontendInclude = append(frontendInclude, map[string]string{"name": name})
	}
	if len(frontendInclude) == 0 {
		frontendInclude = append(frontendInclude, map[string]string{"name": "not-selected"})
	}
	frontendMatrix, err := json.Marshal(map[string]any{"include": frontendInclude})
	if err != nil {
		return err
	}
	effective, err := json.Marshal(plan.Effective)
	if err != nil {
		return err
	}
	nominal, err := json.Marshal(plan.Nominal)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		file,
		"go_matrix=%s\nfrontend_matrix=%s\neffective_json=%s\nnominal_json=%s\naudit=%s\n",
		goMatrix,
		frontendMatrix,
		effective,
		nominal,
		strconv.FormatBool(plan.Audit),
	)
	return err
}

func appendSummary(filename string, plan platformci.Plan) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub summary: %w", err)
	}
	defer file.Close()

	selected := plan.Effective.Selected()
	if plan.PR != nil {
		selected = plan.PR.Effective.Selected()
	}
	var run, skip []string
	for _, name := range sortedJobNames(selected) {
		if selected[name] {
			run = append(run, name)
		} else {
			skip = append(skip, name)
		}
	}
	_, err = fmt.Fprintf(file,
		"## CI plan\n\n**Reason:** %s\n\n**Audit:** %t\n\n**Run:** %s\n\n**Skip:** %s\n\n",
		plan.Reason,
		plan.Audit,
		strings.Join(run, ", "),
		strings.Join(skip, ", "),
	)
	return err
}

func appendGateSummary(filename string, report platformci.GateReport) error {
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if report.OK {
		_, err = fmt.Fprintln(file, "## CI gate\n\nAll selected jobs passed.")
		return err
	}
	_, err = fmt.Fprintf(file, "## CI gate\n\n- %s\n", strings.Join(report.Problems, "\n- "))
	if err == nil && len(report.AuditMisses) > 0 {
		_, err = fmt.Fprintf(file, "\n**Selection audit misses:** %s\n", strings.Join(report.AuditMisses, ", "))
	}
	return err
}

func readJSON(filename string, destination any) error {
	data, err := os.ReadFile(filename)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func splitLabels(value string) []string {
	var labels []string
	for _, label := range strings.Split(value, ",") {
		if label = strings.TrimSpace(label); label != "" {
			labels = append(labels, label)
		}
	}
	return labels
}

func sortedJobNames(selected map[string]bool) []string {
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func prFrontendMatrix(plan platformci.Plan) string {
	shards := plan.PR.Effective.Frontend
	if len(shards) == 0 {
		shards = []string{"not-selected"}
	}
	data, _ := json.Marshal(map[string][]string{"shard": shards})
	return string(data)
}
