package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ciadapter "github.com/flidai/leapview/internal/app/tools/ciadapter"
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
	plan, err := readPlan("ci-plan.json")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PR.Base != base || plan.PR.Head != head || !plan.PR.Effective.GoApplication || !plan.PR.Effective.Docs {
		t.Fatalf("lost cumulative backend change: %+v", plan.PR)
	}
	results := map[string]string{"prepare": "success"}
	for name, on := range plan.PR.Effective.Selected() {
		results[ciadapter.WorkflowJobID(name)] = "skipped"
		if on {
			results[ciadapter.WorkflowJobID(name)] = "success"
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

	plan, err = readPlan("ci-plan.json")
	if err != nil {
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

func TestWarehouseBoundaryUsesHistoricalWorkflowOutputID(t *testing.T) {
	t.Parallel()

	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"internal/analytics/query/planner.go"}}})
	if plan.PR == nil || !plan.PR.Effective.Warehouse {
		t.Fatalf("warehouse boundary was not selected: %+v", plan.PR)
	}
	output := filepath.Join(t.TempDir(), "github-output")
	if err := os.WriteFile(output, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeGitHubOutputs(output, plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "dbt_warehouse_boundary_validation=true") {
		t.Fatalf("legacy workflow output missing:\n%s", data)
	}

	plan.PR.Head, plan.PR.RunID, plan.PR.Attempt = "candidate", "42", "1"
	artifact, err := ciadapter.MarshalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(artifact), `"dbt": true`) {
		t.Fatalf("artifact did not preserve the v2 wire field:\n%s", artifact)
	}
	planPath := filepath.Join(t.TempDir(), "ci-plan.json")
	if err := os.WriteFile(planPath, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{"prepare": "success"}
	for name, selected := range plan.PR.Effective.Selected() {
		wireName := ciadapter.WorkflowJobID(name)
		if selected {
			results[wireName] = "success"
		} else {
			results[wireName] = "skipped"
		}
	}
	resultsPath := filepath.Join(t.TempDir(), "results.json")
	resultData, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, resultData, 0600); err != nil {
		t.Fatal(err)
	}
	matrix := prFrontendMatrix(plan)
	args := []string{"--plan", planPath, "--results", resultsPath, "--expected-head", "candidate", "--expected-run-id", "42", "--expected-attempt", "1", "--frontend-matrix", matrix}
	if err := runGate(args); err != nil {
		t.Fatalf("historical wire artifact rejected: %v", err)
	}
	neutralArtifact := bytes.Replace(artifact, []byte(`"dbt": true`), []byte(`"warehouse": true`), 1)
	if err := os.WriteFile(planPath, neutralArtifact, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runGate(args); err == nil {
		t.Fatal("neutral plan field was accepted at the wire boundary")
	}
	if err := os.WriteFile(planPath, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	results[ciadapter.WorkflowJobID("warehouse-validation")] = "skipped"
	resultData, err = json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, resultData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runGate(args); err == nil {
		t.Fatal("selected warehouse lane accepted a skipped result")
	} else if !strings.Contains(err.Error(), "dbt-warehouse-boundary-validation") {
		t.Fatalf("gate diagnostic lost the workflow lane name: %v", err)
	}
}

func TestGateRejectsNeutralAndWireResultCollision(t *testing.T) {
	t.Parallel()

	plan := platformci.PlanChanges(platformci.Input{Event: "pull_request", PullRequestNumber: 1}, []platformci.Change{{Status: "M", Paths: []string{"internal/analytics/query/planner.go"}}})
	plan.PR.Head, plan.PR.RunID, plan.PR.Attempt = "candidate", "42", "1"
	artifact, err := ciadapter.MarshalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	planPath, resultsPath := filepath.Join(dir, "ci-plan.json"), filepath.Join(dir, "results.json")
	if err := os.WriteFile(planPath, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{"prepare": "success"}
	for name, selected := range plan.PR.Effective.Selected() {
		wireName := ciadapter.WorkflowJobID(name)
		if selected {
			results[wireName] = "success"
		} else {
			results[wireName] = "skipped"
		}
	}
	resultData, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, resultData, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--plan", planPath, "--results", resultsPath, "--expected-head", "candidate", "--expected-run-id", "42", "--expected-attempt", "1", "--frontend-matrix", prFrontendMatrix(plan)}
	if err := runGate(args); err != nil {
		t.Fatalf("valid workflow results rejected: %v", err)
	}
	wireName := ciadapter.WorkflowJobID("warehouse-validation")
	delete(results, wireName)
	results["warehouse-validation"] = "success"
	resultData, err = json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, resultData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runGate(args); err == nil {
		t.Fatal("neutral workflow result alias was accepted")
	}
	results[wireName] = "success"
	resultData, err = json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsPath, resultData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runGate(args); err == nil {
		t.Fatal("neutral/wire alias collision was accepted")
	}
}
