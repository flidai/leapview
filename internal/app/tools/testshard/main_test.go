package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPostgresApplicationShardsCoverCompiledInventoryAndPropagateFailures(t *testing.T) {
	tool := filepath.Join(t.TempDir(), "testshard")
	if out, err := exec.Command("go", "build", "-o", tool, ".").CombinedOutput(); err != nil {
		t.Fatalf("build selector: %v\n%s", err, out)
	}
	script, err := os.ReadFile("../../../../scripts/postgres-app-shards.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"", "compile", "list", "shard", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"scripts", "internal/app", "bin", "exec"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			write("scripts/postgres-app-shards.sh", string(script))
			write("bin/go", `#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$ROOT/commands"
if [[ "$1" == test ]]; then
  [[ "$FAILURE" != compile ]] || exit 31
  [[ "$*" == "test -c -tags integration duckdb_arrow -o $ROOT/exec/app.test ./internal/app" ]]
  cp "$ROOT/list-binary" "$ROOT/exec/app.test"
else
  cp "$TOOL" "$ROOT/exec/testshard"
fi
`)
			write("list-binary", `#!/usr/bin/env bash
set -eu
[[ "$PWD" == "$ROOT/internal/app" ]]
[[ "$*" == '-test.list .' ]]
[[ "$FAILURE" != list ]] || exit 32
printf '%s\n' TestA TestB TestC TestIntegrationOnly ExampleDemo FuzzDecode TestG TestH
`)
			write("exec/postgres-package-exec", `#!/usr/bin/env bash
set -eu
[[ "$PWD" == "$ROOT/internal/app" ]]
[[ "$LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED" == 1 ]]
printf '%s\n' "$@" > "$ROOT/args-$$"
printf '%s\n' "$PPID" > "$ROOT/parent-$$"
[[ "$FAILURE" != shard || "$*" != *TestIntegrationOnly* ]] || exit 33
if [[ "$FAILURE" == cancel ]]; then
  trap 'touch "$ROOT/stopped-$$"; exit 0' TERM
  touch "$ROOT/ready-$$"
  # Bound even a broken cancellation implementation so the fixture cannot leak.
  sleep 2
fi
`)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts/postgres-app-shards.sh"), filepath.Join(root, "exec"))
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"), "ROOT="+root, "TOOL="+tool, "FAILURE="+failure)
			if failure == "cancel" {
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				waited := false
				defer func() {
					if !waited {
						_ = cmd.Process.Signal(syscall.SIGTERM)
						_ = cmd.Wait()
					}
				}()
				deadline := time.Now().Add(5 * time.Second)
				for {
					ready, _ := filepath.Glob(filepath.Join(root, "ready-*"))
					if len(ready) == 4 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("workers did not become ready")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				err := cmd.Wait()
				waited = true
				if err == nil {
					t.Fatal("cancellation was accepted as success")
				}
				stopped, _ := filepath.Glob(filepath.Join(root, "stopped-*"))
				if len(stopped) != 4 {
					t.Fatalf("only %d of 4 wrappers cleaned up on cancellation", len(stopped))
				}
				return
			}
			output, err := cmd.CombinedOutput()
			if failure != "" {
				if err == nil {
					t.Fatalf("%s failure accepted\n%s", failure, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("run shards: %v\n%s", err, output)
			}
			files, err := filepath.Glob(filepath.Join(root, "args-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 4 {
				t.Fatalf("got %d workers, want 4", len(files))
			}

			parents, err := filepath.Glob(filepath.Join(root, "parent-*"))
			if err != nil {
				t.Fatal(err)
			}
			owners := map[string]bool{}
			for _, path := range parents {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				owners[string(data)] = true
			}
			if len(owners) != 4 {
				t.Fatalf("shards share Testcontainers session parents: %v", owners)
			}
			seen := map[string]int{}
			for _, file := range files {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				args := strings.Split(string(data), "\n")
				for _, flag := range []string{"-test.paniconexit0", "-test.parallel=1", "-test.count=1", "-test.timeout=30m", "-test.v", "-test.skip=^TestMinIOParquetSourceRefreshContract$"} {
					if !strings.Contains(string(data), flag+"\n") {
						t.Fatalf("missing %s in %s", flag, data)
					}
				}
				pattern := regexp.MustCompile(strings.TrimPrefix(args[2], "-test.run="))
				for _, name := range []string{"TestA", "TestB", "TestC", "TestIntegrationOnly", "ExampleDemo", "FuzzDecode", "TestG", "TestH"} {
					if pattern.MatchString(name) {
						seen[name]++
					}
				}
			}
			if len(seen) != 8 {
				t.Fatalf("incomplete inventory: %v", seen)
			}
			for name, count := range seen {
				if count != 1 {
					t.Fatalf("%s ran %d times", name, count)
				}
			}
		})
	}
}
