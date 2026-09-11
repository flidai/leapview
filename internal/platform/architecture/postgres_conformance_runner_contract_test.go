package architecture

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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

func TestPostgreSQLConformanceRunnerShardsAppPackageWithMatchingTags(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/app/app_test.go":          "package app\n\nfunc TestApp(t *testing.T) { " + postgresSharedStart + " }\n",
		"internal/pg/shared/shared_test.go": "package shared\n\nfunc TestShared(t *testing.T) { " + postgresSharedStart + " }\n",
	})
	result := fixture.runWithEnv(t, 0, "STUB_APP_SHARDS=1")
	if result.err != nil {
		t.Fatalf("run sharded PostgreSQL app conformance fixture: %v\n%s", result.err, result.output)
	}

	invocations := fixture.stubInvocations(t)
	if len(invocations) != 9 {
		t.Fatalf("stub invocations = %d, want one non-app run, four discoveries, and four app workers: %#v", len(invocations), invocations)
	}
	wantNonApp := []string{"test", "-tags", "integration duckdb_arrow", "-p", "4", "-count=1", "-v", "-skip", "^TestMinIOParquetSourceRefreshContract$", "github.com/flidai/leapview/internal/pg/shared"}
	nonAppCount := 0
	for _, invocation := range invocations {
		if len(invocation) > 1 && invocation[0] == "test" && !slices.Contains(invocation, "-run") {
			nonAppCount++
			if !reflect.DeepEqual(invocation, wantNonApp) {
				t.Fatalf("non-app invocation = %#v, want %#v", invocation, wantNonApp)
			}
		}
	}
	if nonAppCount != 1 {
		t.Fatalf("non-app invocations = %d, want one: %#v", nonAppCount, invocations)
	}
	seenDiscovery := make(map[string]int)
	seenWorker := make(map[string]int)
	for _, invocation := range invocations {
		if len(invocation) > 1 && invocation[0] == "run" {
			if got, want := invocation[len(invocation)-2:], []string{"--tags", "integration duckdb_arrow"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("discovery invocation = %#v, want trailing tags %#v", invocation, want)
			}
			shard := invocation[5]
			seenDiscovery[shard]++
			continue
		}
		if len(invocation) > 1 && invocation[0] == "test" {
			runIndex := slices.Index(invocation, "-run")
			if runIndex < 0 {
				continue
			}
			if runIndex+1 >= len(invocation) {
				t.Fatalf("app worker invocation has no -run pattern: %#v", invocation)
			}
			seenWorker[invocation[runIndex+1]]++
			if !slices.Contains(invocation, "-tags") || !slices.Contains(invocation, "integration duckdb_arrow") {
				t.Fatalf("app worker invocation omitted integration tags: %#v", invocation)
			}
		}
	}
	if len(seenDiscovery) != 4 {
		t.Fatalf("discovery shards = %#v, want four distinct shards", seenDiscovery)
	}
	if len(seenWorker) != 4 {
		t.Fatalf("worker patterns = %#v, want four distinct patterns exactly once", seenWorker)
	}
	workerEnvs := fixture.stubWorkerEnvs(t)
	if len(workerEnvs) != 4 {
		t.Fatalf("app worker environments = %#v, want four worker environments", workerEnvs)
	}
	for _, env := range workerEnvs {
		if env != "1" {
			t.Fatalf("app worker REQUIRED value = %q, want 1", env)
		}
	}
	for shard, count := range seenDiscovery {
		if count != 1 {
			t.Fatalf("discovery shard %s invoked %d times, want once", shard, count)
		}
	}
	for pattern, count := range seenWorker {
		if count != 1 {
			t.Fatalf("worker pattern %q invoked %d times, want once", pattern, count)
		}
	}
}

func TestPostgreSQLConformanceRunnerFailsClosedBeforeAppWorkersOnShardDiscoveryError(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/app/app_test.go": "package app\n\nfunc TestApp(t *testing.T) { " + postgresSharedStart + " }\n",
	})
	result := fixture.runWithEnv(t, 0, "STUB_APP_SHARDS=1", "STUB_DISCOVERY_FAIL=2")
	if result.err == nil {
		t.Fatal("shard discovery failure unexpectedly succeeded")
	}
	for _, invocation := range fixture.stubInvocations(t) {
		if len(invocation) > 1 && invocation[0] == "test" && slices.Contains(invocation, "-run") {
			t.Fatalf("app worker started after discovery failure: %#v", invocation)
		}
	}
}

func TestPostgreSQLConformanceRunnerWaitsForAllAppWorkersAndPropagatesFailure(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/app/app_test.go": "package app\n\nfunc TestApp(t *testing.T) { " + postgresSharedStart + " }\n",
	})
	result := fixture.runWithEnv(t, 0, "STUB_APP_SHARDS=1", "STUB_APP_FAILURE=1")
	if result.err == nil {
		t.Fatal("app shard failure unexpectedly succeeded")
	}
	exitErr, ok := result.err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 19 {
		t.Fatalf("app shard error = %v, want exit code 19", result.err)
	}
	invocations := fixture.stubInvocations(t)
	workerCount := 0
	for _, invocation := range invocations {
		if len(invocation) > 1 && invocation[0] == "test" && slices.Contains(invocation, "-run") {
			workerCount++
		}
	}
	if workerCount != 4 {
		t.Fatalf("started app workers = %d, want all four workers reaped after one failed: %#v", workerCount, invocations)
	}
	if completed := fixture.stubCompletionCount(t); completed != 4 {
		t.Fatalf("completed app workers = %d, want all four workers reaped after one failed", completed)
	}
}

type postgresConformanceFixture struct {
	root            string
	script          string
	stubArgs        string
	stubRequire     string
	stubLog         string
	stubRequiredLog string
	stubCompleteLog string
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
	stubSource := `#!/usr/bin/env bash
set -eu
printf '%s\n' "$@" > "$STUB_ARGS"
printf '%s' "${LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED-}" > "$STUB_REQUIRED"
if [[ -n "${STUB_LOG-}" ]]; then
  mkdir -p "$STUB_LOG"
  printf '%s\n' '---' "$@" > "$STUB_LOG/$BASHPID"
fi
if [[ -n "${STUB_REQUIRED_LOG-}" ]]; then
  mkdir -p "$STUB_REQUIRED_LOG"
  printf '%s' "${LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED-}" > "$STUB_REQUIRED_LOG/$BASHPID"
fi
if [[ "${1-}" == run && "${STUB_APP_SHARDS-}" == 1 ]]; then
  shard=""
  for ((index = 1; index <= $#; index++)); do
    if [[ "${!index}" == --shard-index ]]; then
      next=$((index + 1))
      shard="${!next}"
      break
    fi
  done
  if [[ "$shard" == "${STUB_DISCOVERY_FAIL-}" ]]; then
    exit 23
  fi
  printf '^(?:TestAppShard%s)$\n' "$shard"
  exit 0
fi
if [[ "${1-}" == test && "${STUB_APP_SHARDS-}" == 1 ]] && [[ "${STUB_APP_FAILURE-}" != "" ]]; then
  for argument in "$@"; do
    if [[ "$argument" == *"TestAppShard${STUB_APP_FAILURE}"* ]]; then
      if [[ -n "${STUB_COMPLETE_LOG-}" ]]; then
        mkdir -p "$STUB_COMPLETE_LOG"
        printf '%s' failed > "$STUB_COMPLETE_LOG/$BASHPID"
      fi
      exit 19
    fi
  done
fi
if [[ "${1-}" == test && "${STUB_APP_SHARDS-}" == 1 ]] && [[ "$*" == *" -run "* ]]; then
  sleep 0.2
  if [[ -n "${STUB_COMPLETE_LOG-}" ]]; then
    mkdir -p "$STUB_COMPLETE_LOG"
    printf '%s' passed > "$STUB_COMPLETE_LOG/$BASHPID"
  fi
fi
exit "${STUB_EXIT:-0}"
`
	if err := os.WriteFile(stub, []byte(stubSource), 0o755); err != nil {
		t.Fatalf("write fixture Go stub: %v", err)
	}
	return postgresConformanceFixture{
		root:            root,
		script:          script,
		stubArgs:        filepath.Join(root, "stub-args"),
		stubRequire:     filepath.Join(root, "stub-required"),
		stubLog:         filepath.Join(root, "stub-log"),
		stubRequiredLog: filepath.Join(root, "stub-required-log"),
		stubCompleteLog: filepath.Join(root, "stub-complete-log"),
	}
}

func (f postgresConformanceFixture) run(t *testing.T, exitCode int) postgresConformanceRun {
	return f.runWithEnv(t, exitCode)
}

func (f postgresConformanceFixture) runWithEnv(t *testing.T, exitCode int, extra ...string) postgresConformanceRun {
	t.Helper()
	stubDir := filepath.Join(f.root, "bin")
	cmd := exec.Command("bash", f.script, "run")
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_ARGS="+f.stubArgs,
		"STUB_REQUIRED="+f.stubRequire,
		"STUB_LOG="+f.stubLog,
		"STUB_REQUIRED_LOG="+f.stubRequiredLog,
		"STUB_COMPLETE_LOG="+f.stubCompleteLog,
		"STUB_EXIT="+strconv.Itoa(exitCode),
	)
	cmd.Env = append(cmd.Env, extra...)
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

func (f postgresConformanceFixture) stubInvocations(t *testing.T) [][]string {
	t.Helper()
	entries, err := os.ReadDir(f.stubLog)
	if err != nil {
		t.Fatalf("read PostgreSQL runner stub log directory: %v", err)
	}
	var invocations [][]string
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(f.stubLog, entry.Name()))
		if err != nil {
			t.Fatalf("read PostgreSQL runner stub log %s: %v", entry.Name(), err)
		}
		invocations = append(invocations, parseStubInvocations(string(contents))...)
	}
	return invocations
}

func (f postgresConformanceFixture) stubWorkerEnvs(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.stubLog)
	if err != nil {
		t.Fatalf("read PostgreSQL runner stub log directory: %v", err)
	}
	var values []string
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(f.stubLog, entry.Name()))
		if err != nil {
			t.Fatalf("read PostgreSQL runner stub log %s: %v", entry.Name(), err)
		}
		invocations := parseStubInvocations(string(contents))
		if len(invocations) != 1 || len(invocations[0]) < 2 || invocations[0][0] != "test" || !slices.Contains(invocations[0], "-run") {
			continue
		}
		env, err := os.ReadFile(filepath.Join(f.stubRequiredLog, entry.Name()))
		if err != nil {
			t.Fatalf("read PostgreSQL runner worker environment %s: %v", entry.Name(), err)
		}
		values = append(values, string(env))
	}
	return values
}

func (f postgresConformanceFixture) stubCompletionCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(f.stubCompleteLog)
	if err != nil {
		t.Fatalf("read PostgreSQL runner completion log directory: %v", err)
	}
	return len(entries)
}

func parseStubInvocations(output string) [][]string {
	var invocations [][]string
	var invocation []string
	for _, line := range strings.Split(output, "\n") {
		if line == "---" {
			if len(invocation) > 0 {
				invocations = append(invocations, invocation)
			}
			invocation = nil
			continue
		}
		if line != "" {
			invocation = append(invocation, line)
		}
	}
	if len(invocation) > 0 {
		invocations = append(invocations, invocation)
	}
	return invocations
}
