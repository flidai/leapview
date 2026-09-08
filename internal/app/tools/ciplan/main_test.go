package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	platformci "github.com/flidai/leapview/internal/platform/ci"
)

func TestWriteGitHubOutputsUsesNonEmptySentinelMatrices(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeGitHubOutputs(output, platformci.Plan{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		`go_matrix={"include":[{"name":"not-selected","app_shard":""}]}`,
		`frontend_matrix={"include":[{"name":"not-selected"}]}`,
		"go_tests=false",
		"frontend_tests=false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("GitHub output missing %q:\n%s", want, text)
		}
	}
}

func TestCumulativeStackPlanAndGate(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "CI test")
	git("config", "user.email", "ci@example.test")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "base")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-b", "stack")
	write("internal/access/sqlite/session.go", "backend lower layer")
	git("add", ".")
	git("commit", "-m", "lower")
	lower := git("rev-parse", "HEAD")
	write("README.md", "upper docs")
	git("add", ".")
	git("commit", "-m", "upper")
	head := git("rev-parse", "HEAD")
	args := []string{"--event", "pull_request", "--pr-number", "1", "--base", lower, "--head", head, "--stack-base", "main", "--run-id", "123", "--attempt", "2"}
	if err := runPlan(args); err != nil {
		t.Fatal(err)
	}
	var plan platformci.Plan
	if err := readJSON("ci-plan.json", &plan); err != nil {
		t.Fatal(err)
	}
	if plan.PR.Base != base || plan.PR.Head != head || !plan.PR.Effective.GoApplication || !plan.PR.Effective.Docs {
		t.Fatalf("lost cumulative backend change: %+v", plan.PR)
	}
	results := map[string]string{"prepare": "success"}
	for name, on := range plan.PR.Effective.Selected() {
		results[name] = "skipped"
		if on {
			results[name] = "success"
		}
	}
	saveResults := func() { data, _ := json.Marshal(results); write("results.json", string(data)) }
	saveResults()
	gateArgs := []string{"--plan", "ci-plan.json", "--results", "results.json", "--expected-head", head, "--expected-run-id", "123", "--expected-attempt", "2", "--frontend-matrix", prFrontendMatrix(plan)}
	if err := runGate(gateArgs); err != nil {
		t.Fatal(err)
	}
	results["prepare"] = "failure"
	saveResults()
	if runGate(gateArgs) == nil {
		t.Fatal("planner failure passed")
	}
	results["prepare"] = "success"
	saveResults()
	badMatrix := append([]string(nil), gateArgs...)
	badMatrix[len(badMatrix)-1] = `{"shard":["core"]}`
	if runGate(badMatrix) == nil {
		t.Fatal("missing selected matrix member passed")
	}
	stale := append([]string(nil), gateArgs...)
	stale[9] = "1"
	if runGate(stale) == nil {
		t.Fatal("stale attempt passed")
	}
	if err := runPlan(append(args, "--deferred=true")); err != nil {
		t.Fatal(err)
	}
	if runGate(gateArgs) == nil {
		t.Fatal("unexpected deferral passed")
	}

	if err := readJSON("ci-plan.json", &plan); err != nil {
		t.Fatal(err)
	}
	for name := range results {
		results[name] = "skipped"
	}
	results["prepare"] = "success"
	saveResults()
	gateArgs[len(gateArgs)-1] = prFrontendMatrix(plan)
	if err := runGate(append(gateArgs, "--expected-deferred=true")); err != nil {
		t.Fatalf("legitimate stack deferral rejected: %v", err)
	}
}

func TestPlanningFailureWritesNoArtifact(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	err := runPlan([]string{"--event", "pull_request", "--base", "missing-base", "--head", "missing-head"})
	if err == nil {
		t.Fatal("missing history accepted")
	}
	if _, err := os.Stat("ci-plan.json"); !os.IsNotExist(err) {
		t.Fatal("failed planner published an artifact")
	}
}

func TestCurrentOutputsMatchSelectedFrontendMatrix(t *testing.T) {
	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"web/components/chat/chat-page.dom.test.ts"}}})
	file := filepath.Join(t.TempDir(), "outputs")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeGitHubOutputs(file, plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"frontend_validation=true", `frontend_matrix={"shard":["core","chat"]}`, "go_application_validation=false", "docs_validation=false"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing output %s", want)
		}
	}
}
