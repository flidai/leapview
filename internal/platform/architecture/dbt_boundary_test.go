package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDBTWarehouseBoundaryDoesNotEnterLeapViewRuntime(t *testing.T) {
	root := repoRoot(t)
	for _, relative := range []string{"go.mod", "go.sum"} {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.Contains(strings.ToLower(fields[0]), "dbt") {
				t.Errorf("%s contains a dbt module dependency: %s", relative, line)
			}
		}
	}

	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := string(dockerfile)
	marker := " AS runtime\n"
	if index := strings.Index(runtime, marker); index >= 0 {
		runtime = runtime[index+len(marker):]
	} else {
		t.Fatal("Dockerfile has no runtime stage")
	}
	for _, forbidden := range []string{"dbt-core", "dbt-duckdb", "python", "manifest.json", "run_results.json"} {
		if strings.Contains(strings.ToLower(runtime), forbidden) {
			t.Errorf("runtime image contains dbt-side dependency %q", forbidden)
		}
	}

	leapviewBundle := filepath.Join(root, "examples/dbt-warehouse-boundary/leapview")
	err = filepath.Walk(leapviewBundle, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil || info.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, forbidden := range []string{"manifest.json", "run_results.json", "profiles.yml", "dbt_project.yml"} {
			if strings.Contains(strings.ToLower(string(body)), forbidden) {
				t.Errorf("LeapView serving bundle %s depends on %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDBTWarehouseBoundaryWorkflowPublishesBeforeLeapView(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, ".github/workflows/dbt-warehouse-boundary-reference.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(body)

	ordered := []string{
		"Set up pinned Python",
		"Install pinned dbt Core",
		"Install pinned dbt-duckdb",
		"Read the bounded producer inputs",
		"Run dbt build and verify physical Parquet",
		"Select a new immutable publication prefix",
		"Upload the complete selected mart set",
		"Verify the exact published mart set",
		"Qualify the physical publication through LeapView",
	}
	previous := -1
	for _, step := range ordered {
		index := strings.Index(workflow, step)
		if index < 0 {
			t.Fatalf("reference workflow is missing %q", step)
		}
		if index <= previous {
			t.Fatalf("reference workflow step %q is out of order", step)
		}
		previous = index
	}

	for _, required := range []string{
		"--overwrite false", "length(@)", "${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${GITHUB_SHA}",
		"AZURE_CORE_OUTPUT: none", "LEAPVIEW_WORKLOAD_CLIENT_ID", "type: azure_blob",
		"dev --once --no-browser", ".candidateId", "publish \"$candidate_id\" --format json",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("reference workflow is missing fail-closed boundary %q", required)
		}
	}
	for _, forbidden := range []string{"manifest.json", "run_results.json", "dbt parse", "dbt ls"} {
		if strings.Contains(strings.ToLower(workflow), forbidden) {
			t.Errorf("reference workflow treats dbt metadata as authority through %q", forbidden)
		}
	}
}

func TestDBTExampleUsesExternalNonIncrementalMarts(t *testing.T) {
	root := repoRoot(t)
	project, err := os.ReadFile(filepath.Join(root, "examples/dbt-warehouse-boundary/dbt/dbt_project.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(project)
	for _, required := range []string{"+materialized: external", "+format: parquet", "clean-targets:"} {
		if !strings.Contains(text, required) {
			t.Errorf("dbt project is missing external publication contract %q", required)
		}
	}
	if strings.Contains(strings.ToLower(text), "incremental") {
		t.Error("dbt reference marts must remain non-incremental")
	}
}
