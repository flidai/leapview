package testminio

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildContextArchiveIncludesDockerfileAndModuleHelper(t *testing.T) {
	contents, err := buildContextArchive()
	if err != nil {
		t.Fatal(err)
	}

	want := map[string][]byte{
		"Dockerfile":          dockerfile,
		"download-modules.sh": downloadModules,
	}
	reader := tar.NewReader(bytes.NewReader(contents))
	for len(want) != 0 {
		header, err := reader.Next()
		if err != nil {
			t.Fatalf("read build context entry: %v", err)
		}
		var entry bytes.Buffer
		if _, err := entry.ReadFrom(reader); err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		expected, ok := want[header.Name]
		if !ok {
			t.Fatalf("unexpected build context entry %q", header.Name)
		}
		if !bytes.Equal(entry.Bytes(), expected) {
			t.Errorf("build context entry %q does not match embedded input", header.Name)
		}
		delete(want, header.Name)
	}
}

func TestDownloadModulesRetriesThenPreservesArguments(t *testing.T) {
	result := runDownloadModules(t, 2, "-x", "example.com/fixture@v1.2.3")
	if result.err != nil {
		t.Fatalf("module helper failed after transient errors: %v\n%s", result.err, result.output)
	}
	goInvocation := "GODEBUG=http2client=0\nargc=4\narg=mod\narg=download\narg=-x\narg=example.com/fixture@v1.2.3\n"
	if got, want := result.goLog, strings.Repeat(goInvocation, 3); got != want {
		t.Errorf("go argv and environment mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
	if got, want := result.sleepLog, "5\n10\n"; got != want {
		t.Errorf("retry backoff = %q, want %q", got, want)
	}
}

func TestDownloadModulesExhaustionStopsAfterThreeAttempts(t *testing.T) {
	result := runDownloadModules(t, 3)
	var exitErr *exec.ExitError
	if !errors.As(result.err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("helper error = %v, want go's exit status 17\n%s", result.err, result.output)
	}
	goInvocation := "GODEBUG=http2client=0\nargc=2\narg=mod\narg=download\n"
	if got, want := result.goLog, strings.Repeat(goInvocation, 3); got != want {
		t.Errorf("go was invoked unexpectedly\ngot:\n%s\nwant:\n%s", got, want)
	}
	if got, want := result.sleepLog, "5\n10\n"; got != want {
		t.Errorf("retry backoff = %q, want %q", got, want)
	}
	if !strings.Contains(result.output, "failed after 3 attempts") {
		t.Errorf("missing exhaustion message: %s", result.output)
	}
}

type downloadResult struct {
	goLog    string
	sleepLog string
	output   string
	err      error
}

func runDownloadModules(t *testing.T, failures int, args ...string) downloadResult {
	t.Helper()
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(binDir, "go"), `#!/bin/sh
{
	printf 'GODEBUG=%s\n' "${GODEBUG-}"
	printf 'argc=%s\n' "$#"
	for arg do printf 'arg=%s\n' "$arg"; done
} >> "$GO_LOG"
count=0
if [ -f "$GO_COUNT" ]; then count=$(cat "$GO_COUNT"); fi
count=$((count + 1))
printf '%s\n' "$count" > "$GO_COUNT"
if [ "$count" -le "$GO_FAILS" ]; then exit 17; fi
`)
	writeExecutable(t, filepath.Join(binDir, "sleep"), `#!/bin/sh
printf '%s\n' "$1" >> "$SLEEP_LOG"
[ "$#" -eq 1 ]
`)
	helperPath := filepath.Join(dir, "download-modules.sh")
	if err := os.WriteFile(helperPath, downloadModules, 0600); err != nil {
		t.Fatal(err)
	}
	goLog := filepath.Join(dir, "go.log")
	sleepLog := filepath.Join(dir, "sleep.log")
	countPath := filepath.Join(dir, "go-count")
	cmd := exec.Command("/bin/sh", append([]string{helperPath}, args...)...)
	cmd.Env = []string{
		"PATH=" + binDir + ":/usr/bin:/bin",
		"GO_LOG=" + goLog,
		"GO_COUNT=" + countPath,
		"GO_FAILS=" + fmt.Sprint(failures),
		"SLEEP_LOG=" + sleepLog,
	}
	output, err := cmd.CombinedOutput()
	return downloadResult{
		goLog:    readTestLog(t, goLog),
		sleepLog: readTestLog(t, sleepLog),
		output:   string(output),
		err:      err,
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0755); err != nil {
		t.Fatal(err)
	}
}

func readTestLog(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
