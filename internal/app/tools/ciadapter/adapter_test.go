package ciadapter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	platformci "github.com/flidai/leapview/internal/platform/ci"
	"gopkg.in/yaml.v3"
)

func TestPlanWireBindingRoundTripsAndPreservesLegacyField(t *testing.T) {
	plan := platformci.Plan{
		Version: platformci.PRPlanVersion,
		PR: &platformci.PRPlan{
			Nominal:   platformci.PRJobs{Warehouse: true, Docs: true},
			Effective: platformci.PRJobs{Warehouse: true, Docs: true},
		},
	}
	data, err := MarshalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, `"dbt": true`) || strings.Contains(text, `"warehouse"`) {
		t.Fatalf("wire plan used the wrong PR field: %s", text)
	}
	got, err := DecodePlan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("decoded plan = %#v, want %#v", got, plan)
	}
}

func TestDecodePlanRetainsHistoricalVersionOne(t *testing.T) {
	plan, err := DecodePlan(strings.NewReader(`{"version":1,"effective":{"docs":true},"nominal":{"docs":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != platformci.PlanVersion || !plan.Effective.Docs || !plan.Nominal.Docs || plan.PR != nil {
		t.Fatalf("historical plan decoded as %#v", plan)
	}
}

func TestDecodePlanRejectsUnknownTrailingDuplicateAndCollidingFields(t *testing.T) {
	tests := map[string]string{
		"unknown":         `{"version":1,"unexpected":true}`,
		"unknown nested":  `{"version":2,"pr":{"nominal":{"unexpected":true},"effective":{}}}`,
		"trailing":        `{"version":1} {}`,
		"duplicate":       `{"version":1,"version":1}`,
		"neutral-only":    `{"version":2,"pr":{"nominal":{"warehouse":true},"effective":{}}}`,
		"field-collision": `{"version":2,"pr":{"nominal":{"warehouse":false,"dbt":true},"effective":{}}}`,
		"case-collision":  `{"version":2,"pr":{"NOMINAL":{},"nominal":{}},"effective":{}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePlan(strings.NewReader(body)); err == nil {
				t.Fatalf("accepted malformed plan %s", body)
			}
		})
	}
	caseFolded, err := DecodePlan(strings.NewReader(`{"VERSION":2,"PR":{"NOMINAL":{"DBT":true},"EFFECTIVE":{}}}`))
	if err != nil || caseFolded.PR == nil || !caseFolded.PR.Nominal.Warehouse {
		t.Fatalf("case-insensitive workflow fields were not normalized: %#v, %v", caseFolded, err)
	}
	if _, err := DecodePlan(strings.NewReader(`{"version":2,"pr":{"nominal":{"WAREHOUSE":true},"effective":{}}}`)); err == nil {
		t.Fatal("accepted a case variant of the neutral warehouse field")
	}
}

func TestWorkflowAndInternalLaneMappings(t *testing.T) {
	for neutral, workflow := range map[string]string{
		"warehouse-validation":   "dbt-warehouse-boundary-validation",
		"go-packages-validation": "go-packages-validation",
		"unknown-lane":           "unknown-lane",
	} {
		if got := WorkflowJobID(neutral); got != workflow {
			t.Errorf("WorkflowJobID(%q) = %q, want %q", neutral, got, workflow)
		}
		if got := InternalJobID(workflow); got != neutral {
			t.Errorf("InternalJobID(%q) = %q, want %q", workflow, got, neutral)
		}
	}
	for text, want := range map[string]string{
		"warehouse-validation failed":         "dbt-warehouse-boundary-validation failed",
		"unknown/warehouse-validation-legacy": "unknown/warehouse-validation-legacy",
		"before warehouse-validation, after":  "before dbt-warehouse-boundary-validation, after",
		" warehouse-validation\tfailed\n":     " dbt-warehouse-boundary-validation\tfailed\n",
	} {
		if got := WorkflowText(text); got != want {
			t.Errorf("WorkflowText(%q) = %q, want %q", text, got, want)
		}
	}

	results, err := InternalResults(map[string]string{
		"dbt-warehouse-boundary-validation": "success",
		"docs-validation":                   "skipped",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"warehouse-validation": "success", "docs-validation": "skipped"}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("internal results = %#v, want %#v", results, want)
	}
	if _, err := InternalResults(map[string]string{
		"dbt-warehouse-boundary-validation": "success",
		"warehouse-validation":              "success",
	}); err == nil {
		t.Fatal("accepted colliding workflow and neutral result keys")
	}
	if _, err := InternalResults(map[string]string{"warehouse-validation": "success"}); err == nil {
		t.Fatal("accepted a neutral result key at the workflow boundary")
	}
}

func TestPRWorkflowConsumesPlannerOutputsAndAlwaysGates(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		On   map[string]map[string]any `yaml:"on"`
		Jobs map[string]struct {
			If      string            `yaml:"if"`
			Needs   []string          `yaml:"needs"`
			Outputs map[string]string `yaml:"outputs"`
			Steps   []struct {
				ID   string            `yaml:"id"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config.On["pull_request"]["paths"] != nil || config.On["pull_request"]["paths-ignore"] != nil {
		t.Fatal("workflow path filtering bypasses required gate")
	}
	gate := config.Jobs["ci-gate"]
	if config.Jobs["prepare"].If != "${{ github.event_name != 'pull_request' || !github.event.pull_request.draft }}" {
		t.Error("PR planning must skip drafts while allowing manual dispatch")
	}
	if _, ok := config.On["workflow_dispatch"]; !ok {
		t.Error("CI must support manual dispatch for draft branches")
	}
	prEvents, _ := config.On["pull_request"]["types"].([]any)
	for _, event := range []string{"opened", "synchronize", "reopened", "ready_for_review"} {
		if !slices.Contains(prEvents, any(event)) {
			t.Errorf("CI must handle PR event %s", event)
		}
	}
	if gate.If != "${{ always() && (github.event_name != 'pull_request' || !github.event.pull_request.draft) }}" || !slices.Contains(gate.Needs, "prepare") {
		t.Fatal("gate must skip drafts and require planning on every eligible outcome")
	}
	prepare := config.Jobs["prepare"]
	if prepare.Outputs["plan_attempt"] != "${{ steps.plan.outputs.plan_attempt }}" || prepare.Outputs["plan_artifact_id"] != "${{ steps.plan-artifact.outputs.artifact-id }}" {
		t.Error("gate evidence must identify the planning job's attempt and immutable artifact")
	}
	var uploadBound, downloadBound, attemptBound bool
	for _, step := range prepare.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.ID == "plan-artifact" && step.With["path"] == "ci-plan.json" {
			uploadBound = true
		}
	}
	for _, step := range gate.Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") {
			downloadBound = step.With["artifact-ids"] == "${{ needs.prepare.outputs.plan_artifact_id }}" && step.With["name"] == "" && step.With["merge-multiple"] == "true"
		}
		if strings.Contains(step.Run, "--expected-attempt \"$PLAN_ATTEMPT\"") {
			attemptBound = step.Env["PLAN_ATTEMPT"] == "${{ needs.prepare.outputs.plan_attempt }}"
		}
	}
	if !uploadBound || !downloadBound || !attemptBound {
		t.Errorf("gate must consume producer evidence across retries: upload=%t download=%t attempt=%t", uploadBound, downloadBound, attemptBound)
	}
	for neutral := range platformci.FullPRJobs().Selected() {
		workflow := WorkflowJobID(neutral)
		job, ok := config.Jobs[workflow]
		if !ok {
			t.Fatalf("missing lane %s", workflow)
		}
		key := strings.ReplaceAll(workflow, "-", "_")
		if !slices.Equal(job.Needs, []string{"prepare"}) || job.If != "needs.prepare.outputs."+key+" == 'true'" {
			t.Errorf("%s bypasses selection", workflow)
		}
		if config.Jobs["prepare"].Outputs[key] != "${{ steps.plan.outputs."+key+" }}" {
			t.Errorf("%s output disconnected", workflow)
		}
		if !slices.Contains(gate.Needs, workflow) {
			t.Errorf("gate omits %s", workflow)
		}
	}
	for _, fragment := range []string{"fetch-depth: 0", "--stack-base \"$STACK_BASE\"", "--head \"$GITHUB_SHA\"", "--expected-attempt \"$PLAN_ATTEMPT\"", "--expected-deferred=\"$DEFERRED\"", "--frontend-matrix \"$FRONTEND_MATRIX\""} {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("missing candidate/gate contract %s", fragment)
		}
	}
}

func TestPlanWireBindingHasNoUnintendedCoreJSONShape(t *testing.T) {
	plan := platformci.Plan{Version: platformci.PRPlanVersion, PR: &platformci.PRPlan{Nominal: platformci.PRJobs{Warehouse: true}, Effective: platformci.PRJobs{Warehouse: true}}}
	data, err := MarshalPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["pr"].(map[string]any)["nominal"].(map[string]any)["warehouse"]; ok {
		t.Fatal("neutral warehouse field leaked into the artifact")
	}
}

func TestPRWorkflowConcurrencyPreservesOtherStackLayers(t *testing.T) {
	for _, workflow := range []string{"ci", "security"} {
		t.Run(workflow, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", ".github", "workflows", workflow+".yml"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Concurrency struct {
					Group  string `yaml:"group"`
					Cancel bool   `yaml:"cancel-in-progress"`
				} `yaml:"concurrency"`
			}
			if err := yaml.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			// Every PR owns required checks, even when its validation is
			// deferred. A different layer must not cancel that feedback.
			if strings.Contains(config.Concurrency.Group, "stack.id") || !strings.Contains(config.Concurrency.Group, "github.ref") {
				t.Fatalf("concurrency can cancel another stack layer: %s", config.Concurrency.Group)
			}
			if !config.Concurrency.Cancel {
				t.Fatal("new revisions must still cancel obsolete runs for the same PR")
			}
		})
	}
}
