package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func corpusTestPaths(t *testing.T) (string, string) {
	t.Helper()
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return repo, filepath.Join(repo, "playground", "dashboard-design-evaluation", "manifest.json")
}

// Qualify real positive inputs and compiler-valid negative examples. In particular,
// schema/compiler success must never qualify a wrong neighbor, binding or placement.
func TestQualifyCanonicalDesignCorpus(t *testing.T) {
	repo, path := corpusTestPaths(t)
	report, err := qualifyCorpus(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Qualified || report.AgentTrials != 0 || len(report.Tasks) != 10 {
		t.Fatalf("incomplete or mislabeled deterministic qualification: %#v", report)
	}
	if report.SupportSHA256 == "" || report.ManifestSHA256 == "" || report.InputSHA256 == "" || report.Compiler.ExecutableSHA256 == "" || report.Compiler.Commit == "" {
		t.Fatal("qualification omitted input/compiler identity")
	}
	for _, task := range report.Tasks {
		if !task.Qualified || !task.SeedSchema || !task.OracleSchema || !task.OracleCompiler || !task.SeedDiffers || len(task.RejectedNegatives) == 0 {
			t.Fatalf("task not fully qualified: %#v", task)
		}
		if task.ID == "repair-preserving-neighbors" && (task.SeedCompiler || !strings.Contains(task.SeedDiagnostic, "nonexistent_period")) {
			t.Fatalf("repair seed did not preserve its intended failure: %#v", task)
		}
	}
}

func copyCorpusForTest(t *testing.T) (string, string, corpusManifest) {
	t.Helper()
	repo, path := corpusTestPaths(t)
	base := t.TempDir()
	if err := os.CopyFS(base, os.DirFS(filepath.Dir(path))); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := loadCorpusManifest(filepath.Join(base, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return repo, base, manifest
}

func writeCorpusTestManifest(t *testing.T, base string, manifest corpusManifest) string {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCorpusRejectsMissingTasksAndFixtures(t *testing.T) {
	t.Run("missing task even with other fixtures intact", func(t *testing.T) {
		_, base, manifest := copyCorpusForTest(t)
		manifest.Tasks = manifest.Tasks[:len(manifest.Tasks)-1]
		_, _, err := loadCorpusManifest(writeCorpusTestManifest(t, base, manifest))
		if err == nil || !strings.Contains(err.Error(), "missing required task repair-preserving-neighbors") {
			t.Fatalf("missing task accepted: %v", err)
		}
	})
	t.Run("missing recoverable seed", func(t *testing.T) {
		_, base, manifest := copyCorpusForTest(t)
		if err := os.Remove(filepath.Join(base, "tasks", "ranked-chart", "seed", "dashboards", "evaluation.yaml")); err != nil {
			t.Fatal(err)
		}
		if _, err := verifyCorpusInputs(base, manifest); err == nil || !strings.Contains(err.Error(), "ranked-chart/seed") {
			t.Fatalf("missing seed accepted: %v", err)
		}
	})
	t.Run("extra unrecorded source", func(t *testing.T) {
		_, base, manifest := copyCorpusForTest(t)
		path := filepath.Join(base, "tasks", "ranked-chart", "oracle", "dashboards", "extra.yaml")
		if err := os.WriteFile(path, []byte("unexpected source"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := verifyCorpusInputs(base, manifest); err == nil || !strings.Contains(err.Error(), "unfrozen source file") {
			t.Fatalf("extra source accepted: %v", err)
		}
	})
}

func TestCorpusRejectsInvalidOracleEvenAfterInputHashesAreUpdated(t *testing.T) {
	_, base, manifest := copyCorpusForTest(t)
	task := manifest.Tasks[0]
	path := filepath.Join(base, filepath.FromSlash(task.OracleRoot), "dashboards", "evaluation.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "dimension: purchase_date", "dimension: nonexistent_period", 1))
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	manifest.FrozenFiles[task.OracleRoot+"/dashboards/evaluation.yaml"] = sha256Text(data)
	if _, err := verifyCorpusInputs(base, manifest); err != nil {
		t.Fatal(err)
	}
	_, err = qualifyCorpusTask(base, filepath.Join(base, manifest.SupportRoot), task)
	if err == nil || !strings.Contains(err.Error(), "oracle compiler") || !strings.Contains(err.Error(), "nonexistent_period") {
		t.Fatalf("compiler-invalid oracle qualified: %v", err)
	}
}

func TestCorpusRejectsOracleEquivalentNegative(t *testing.T) {
	_, base, manifest := copyCorpusForTest(t)
	task := manifest.Tasks[0]
	task.Negatives[0].SourceRoot = task.OracleRoot
	_, err := qualifyCorpusTask(base, filepath.Join(base, manifest.SupportRoot), task)
	if err == nil || !strings.Contains(err.Error(), "accepted valid-but-wrong negative") {
		t.Fatalf("vacuous negative qualified: %v", err)
	}
}
