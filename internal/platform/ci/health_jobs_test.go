package ci

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Reading the real workflows makes a renamed lane or added required matrix
// member fail locally before weekly reports silently lose its evidence.
func TestHealthRegistryMatchesCurrentWorkflows(t *testing.T) {
	for _, workflow := range []string{"ci.yml", "merge-validation.yml", "nightly.yml"} {
		t.Run(workflow, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", workflow))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Jobs map[string]struct {
					Name     string `yaml:"name"`
					Strategy struct {
						Matrix yaml.Node `yaml:"matrix"`
					} `yaml:"strategy"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			var actual []string
			for id, job := range config.Jobs {
				names := []string{job.Name}
				var matrix struct {
					Shards []string `yaml:"shard"`
				}
				if job.Strategy.Matrix.Kind == yaml.ScalarNode {
					if workflow != "ci.yml" || job.Strategy.Matrix.Value != "${{ fromJSON(needs.prepare.outputs.frontend_matrix) }}" {
						t.Fatal("unexpected dynamic matrix")
					}
					matrix.Shards = FullPRJobs().Frontend
				} else if job.Strategy.Matrix.Kind != 0 {
					if err := job.Strategy.Matrix.Decode(&matrix); err != nil {
						t.Fatal(err)
					}
				}
				if len(matrix.Shards) > 0 {
					names = nil
					for _, shard := range matrix.Shards {
						names = append(names, strings.ReplaceAll(job.Name, "${{ matrix.shard }}", shard))
					}
				}
				for _, name := range names {
					normalized := HealthJobName(name)
					if strings.HasPrefix(normalized, "unknown/") {
						t.Errorf("unmapped lane %s", name)
					}
					if id != "agent-tool-evaluation" {
						actual = append(actual, normalized)
					}
				}
			}
			expected := ExpectedHealthJobs(workflow)
			slices.Sort(actual)
			slices.Sort(expected)
			if !slices.Equal(actual, expected) {
				t.Fatalf("expected inventory %v != workflow %v", expected, actual)
			}
		})
	}
}

func TestExpectedMergeJobsCannotBeOverriddenByPartialPlan(t *testing.T) {
	jobs := Jobs{Docs: true}
	report := AnalyzeHealth([]HealthRun{{Workflow: "merge-validation.yml", Event: "merge_group", Conclusion: "success", Plan: Plan{Version: PlanVersion, Nominal: jobs, Effective: jobs}, Results: map[string]string{"docs": "success"}}})
	run := report.Runs[0]
	if run.ExpectedSource != "workflow_registry" || !slices.Contains(run.UnknownJobs, "full-validation") || run.SelectionConfidence == "verified" {
		t.Fatalf("partial plan overrode merge contract: %+v", run)
	}
}
