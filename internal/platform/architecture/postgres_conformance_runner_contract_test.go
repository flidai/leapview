package architecture

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const (
	postgresLegacyRunCall = "tcpostgres" + ".Run(t)"
	postgresSharedStart   = "postgrestest" + ".Start(t)"
	postgresTLSStart      = "postgrestest" + ".StartTLS(t)"
)

func TestPostgreSQLConformanceRunnerUsesCompleteBoundedInventory(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/pg/legacy/legacy_test.go":    "package legacy\n\nfunc TestLegacy(t *testing.T) { " + postgresLegacyRunCall + " }\n",
		"internal/pg/shared/duplicate_test.go": "package shared\n\nfunc TestDuplicate(t *testing.T) { " + postgresSharedStart + " }\n",
		"internal/pg/shared/shared_test.go":    "package shared\n\nfunc TestShared(t *testing.T) { " + postgresSharedStart + " }\n",
		"internal/pg/tls/tls_test.go":          "package tls\n\nfunc TestTLS(t *testing.T) { " + postgresTLSStart + " }\n",
		"internal/minio/minio_test.go":         "package minio\n\nfunc TestMinIO(t *testing.T) { testcontainers.Run(t) }\n",
	})

	result := fixture.run(t, 0)
	if result.err != nil {
		t.Fatalf("run PostgreSQL conformance fixture: %v\n%s", result.err, result.output)
	}
	wantArgs := []string{
		"test",
		"-tags", "integration duckdb_arrow",
		"-p", "4",
		"-count=1",
		"-v",
		"-skip", "^TestMinIOParquetSourceRefreshContract$",
		"github.com/flidai/leapview/internal/pg/legacy",
		"github.com/flidai/leapview/internal/pg/shared",
		"github.com/flidai/leapview/internal/pg/tls",
	}
	if !reflect.DeepEqual(result.args, wantArgs) {
		t.Fatalf("go test args = %#v, want %#v", result.args, wantArgs)
	}
	if result.required != "1" {
		t.Fatalf("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED = %q, want 1", result.required)
	}
}

func TestPostgreSQLConformanceRunnerFailsClosedOnEmptyInventory(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, nil)
	result := fixture.run(t, 0)
	if result.err == nil {
		t.Fatal("empty PostgreSQL conformance inventory unexpectedly succeeded")
	}
	if !strings.Contains(result.output, "PostgreSQL conformance inventory is empty") {
		t.Fatalf("empty inventory error = %q", result.output)
	}
	if _, err := os.Stat(fixture.stubArgs); err == nil {
		t.Fatal("empty inventory invoked the Go runner")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat empty-inventory Go stub log: %v", err)
	}
}

func TestPostgreSQLConformanceRunnerPropagatesGoErrors(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/pg/shared/shared_test.go": "package shared\n\nfunc TestShared(t *testing.T) { " + postgresSharedStart + " }\n",
	})
	result := fixture.run(t, 37)
	if result.err == nil {
		t.Fatal("Go test failure was not propagated")
	}
	exitErr, ok := result.err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 37 {
		t.Fatalf("Go test error = %v, want exit code 37", result.err)
	}
}

type postgresConformanceFixture struct {
	root        string
	script      string
	stubArgs    string
	stubRequire string
}

type postgresConformanceRun struct {
	args     []string
	required string
	output   string
	err      error
}

func newPostgreSQLConformanceFixture(t *testing.T, testFiles map[string]string) postgresConformanceFixture {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatalf("create fixture scripts directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module github.com/flidai/leapview\n\ngo 1.26.8\n"), 0o644); err != nil {
		t.Fatalf("write fixture go.mod: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts", "postgres-conformance-tests.sh"))
	if err != nil {
		t.Fatalf("read PostgreSQL conformance runner: %v", err)
	}
	script := filepath.Join(root, "scripts", "postgres-conformance-tests.sh")
	if err := os.WriteFile(script, source, 0o755); err != nil {
		t.Fatalf("write fixture PostgreSQL conformance runner: %v", err)
	}
	for relative, body := range testFiles {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create fixture directory for %s: %v", relative, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture test %s: %v", relative, err)
		}
	}
	for _, args := range [][]string{
		{"-C", root, "init", "--quiet"},
		{"-C", root, "add", "--all"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("prepare fixture git repository: %v\n%s", err, output)
		}
	}
	stubDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatalf("create fixture bin directory: %v", err)
	}
	stub := filepath.Join(stubDir, "go")
	stubSource := "#!/usr/bin/env bash\nset -eu\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS\"\nprintf '%s' \"${LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED-}\" > \"$STUB_REQUIRED\"\nexit \"${STUB_EXIT:-0}\"\n"
	if err := os.WriteFile(stub, []byte(stubSource), 0o755); err != nil {
		t.Fatalf("write fixture Go stub: %v", err)
	}
	return postgresConformanceFixture{
		root:        root,
		script:      script,
		stubArgs:    filepath.Join(root, "stub-args"),
		stubRequire: filepath.Join(root, "stub-required"),
	}
}

func (f postgresConformanceFixture) run(t *testing.T, exitCode int) postgresConformanceRun {
	t.Helper()
	stubDir := filepath.Join(f.root, "bin")
	cmd := exec.Command("bash", f.script, "run")
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_ARGS="+f.stubArgs,
		"STUB_REQUIRED="+f.stubRequire,
		"STUB_EXIT="+strconv.Itoa(exitCode),
	)
	output, err := cmd.CombinedOutput()
	result := postgresConformanceRun{output: string(output), err: err}
	if args, readErr := os.ReadFile(f.stubArgs); readErr == nil {
		result.args = strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	}
	if required, readErr := os.ReadFile(f.stubRequire); readErr == nil {
		result.required = string(required)
	}
	return result
}
