package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
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
	postgresSharedOpen    = "postgrestest" + ".Open(t, applySchema)"
)

func TestPostgreSQLConformanceRunnerUsesCompleteBoundedInventory(t *testing.T) {
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/pg/legacy/legacy_test.go":    "package legacy\n\nfunc TestLegacy(t *testing.T) { " + postgresLegacyRunCall + " }\n",
		"internal/pg/open/helper_test.go":      "package open\n\nfunc testStore(t *testing.T) { " + postgresSharedOpen + " }\n",
		"internal/pg/open/open_test.go":        "package open\n\nfunc TestOpen(t *testing.T) { testStore(t) }\n",
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
		"-exec", "postgres-package-exec",
		"-tags", "integration duckdb_arrow",
		"-p", "4",
		"-parallel", "1",
		"-count=1",
		"-timeout=30m",
		"-v",
		"-skip", "^TestMinIOParquetSourceRefreshContract$",
		"github.com/flidai/leapview/internal/pg/legacy",
		"github.com/flidai/leapview/internal/pg/open",
		"github.com/flidai/leapview/internal/pg/shared",
		"github.com/flidai/leapview/internal/pg/tls",
	}
	gotArgs := append([]string(nil), result.args...)
	if len(gotArgs) > 2 && gotArgs[1] == "-exec" {
		gotArgs[2] = filepath.Base(gotArgs[2])
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("go test args = %#v, want %#v", result.args, wantArgs)
	}
	if !reflect.DeepEqual(result.buildArgs, []string{"build", "-o", "postgres-package-exec", "./internal/platform/postgres/postgrestest/cmd/packageexec"}) {
		t.Fatalf("Go package runner build args = %#v", result.buildArgs)
	}
	if result.required != "1" {
		t.Fatalf("LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED = %q, want 1", result.required)
	}
}

func TestPostgreSQLConformanceInventoryIncludesEverySharedHarnessCaller(t *testing.T) {
	root := repoRoot(t)
	command := exec.Command("bash", filepath.Join(root, "scripts", "postgres-conformance-tests.sh"), "list")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list real PostgreSQL conformance packages: %v\n%s", err, output)
	}
	listed := make(map[string]bool)
	for _, name := range strings.Fields(string(output)) {
		listed[name] = true
	}
	moduleBytes, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module := strings.Fields(string(moduleBytes))[1]
	files, err := exec.Command("git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*_test.go").Output()
	if err != nil {
		t.Fatal(err)
	}
	callers := 0
	for _, relative := range strings.Split(string(files), "\x00") {
		if relative == "" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, relative), nil, 0)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		aliases := make(map[string]bool)
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path != module+"/internal/platform/postgres/postgrestest" {
				continue
			}
			name := "postgrestest"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			aliases[name] = true
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name, ok := selector.X.(*ast.Ident)
			if !ok || !aliases[name.Name] || (selector.Sel.Name != "Start" && selector.Sel.Name != "StartTLS" && selector.Sel.Name != "Open") {
				return true
			}
			callers++
			packageName := module + "/" + filepath.ToSlash(filepath.Dir(relative))
			if !listed[packageName] {
				t.Errorf("%s calls shared PostgreSQL harness %s but is absent from the required conformance inventory", relative, selector.Sel.Name)
			}
			return true
		})
	}
	if callers == 0 {
		t.Fatal("shared PostgreSQL harness source inventory is empty")
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
	if _, err := os.Stat(filepath.Join(fixture.root, "image-ready")); !os.IsNotExist(err) {
		t.Fatalf("empty inventory invoked image preparation: %v", err)
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

func TestPostgreSQLConformanceRunnerPropagatesImageErrors(t *testing.T) {
	t.Setenv("STUB_IMAGE_EXIT", "41")
	fixture := newPostgreSQLConformanceFixture(t, map[string]string{
		"internal/pg/shared/shared_test.go": "package shared\n\nfunc TestShared(t *testing.T) { " + postgresSharedStart + " }\n",
	})
	result := fixture.run(t, 0)
	exitErr, ok := result.err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 41 {
		t.Fatalf("image preparation error = %v, want exit code 41", result.err)
	}
	if _, err := os.Stat(fixture.stubBuild); !os.IsNotExist(err) {
		t.Fatalf("Go build ran after image preparation failed: %v", err)
	}
}

func TestPostgreSQLConformanceRunsApplicationWaveExactlyOnce(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			fixture := newPostgreSQLConformanceFixture(t, map[string]string{
				"internal/app/app_test.go":          "package app\nfunc TestApp(t *testing.T) { " + postgresSharedStart + " }\n",
				"internal/pg/shared/shared_test.go": "package shared\nfunc TestShared(t *testing.T) { " + postgresSharedStart + " }\n",
			})
			marker := filepath.Join(fixture.root, "app-wave")
			body := "#!/usr/bin/env bash\nset -eu\ntest -d \"$1\"\nprintf 'wave\\n' >> '" + marker + "'\n"
			if fail {
				body += "exit 39\n"
			}
			if err := os.WriteFile(filepath.Join(fixture.root, "scripts/postgres-app-shards.sh"), []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			result := fixture.run(t, 0)
			if fail {
				if result.err == nil {
					t.Fatal("application wave failure accepted")
				}
				if len(result.args) != 0 {
					t.Fatal("package wave ran after application failure")
				}
			} else {
				if result.err != nil {
					t.Fatalf("runner: %v\n%s", result.err, result.output)
				}
				args := strings.Join(result.args, " ")
				if strings.Contains(args, "github.com/flidai/leapview/internal/app") || !strings.Contains(args, "github.com/flidai/leapview/internal/pg/shared") {
					t.Fatalf("wrong remaining inventory: %s", args)
				}
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "wave\n" {
				t.Fatalf("application wave did not run exactly once: %q, %v", data, err)
			}
		})
	}
}

type postgresConformanceFixture struct {
	root        string
	script      string
	stubArgs    string
	stubBuild   string
	stubRequire string
}

type postgresConformanceRun struct {
	args      []string
	buildArgs []string
	required  string
	output    string
	err       error
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
	script := filepath.Join(root, "scripts", "postgres-conformance-tests.sh")
	for _, relative := range []string{
		"scripts/postgres-conformance-tests.sh",
		"scripts/prepare_ci_fixture_images.sh",
		"internal/platform/postgres/postgrestest/harness.go",
	} {
		source, err := os.ReadFile(filepath.Join(repoRoot(t), relative))
		if err != nil {
			t.Fatalf("read conformance fixture input %s: %v", relative, err)
		}
		target := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, source, 0o644); err != nil {
			t.Fatalf("write conformance fixture input %s: %v", relative, err)
		}
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
	stubSource := "#!/usr/bin/env bash\nset -eu\ntest -f \"$STUB_IMAGE_READY\"\nif [[ \"$1\" == build ]]; then printf '%s\\n' \"$@\" > \"$STUB_BUILD\"; exit 0; fi\nprintf '%s\\n' \"$@\" > \"$STUB_ARGS\"\nprintf '%s' \"${LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED-}\" > \"$STUB_REQUIRED\"\nexit \"${STUB_EXIT:-0}\"\n"
	if err := os.WriteFile(stub, []byte(stubSource), 0o755); err != nil {
		t.Fatalf("write fixture Go stub: %v", err)
	}
	dockerStub := `#!/usr/bin/env bash
set -eu
if [[ "${STUB_IMAGE_EXIT:-0}" != 0 ]]; then exit "$STUB_IMAGE_EXIT"; fi
printf 'sha256:%064d\n' 0
touch "$STUB_IMAGE_READY"
`
	if err := os.WriteFile(filepath.Join(stubDir, "docker"), []byte(dockerStub), 0o755); err != nil {
		t.Fatalf("write fixture Docker stub: %v", err)
	}
	return postgresConformanceFixture{
		root:        root,
		script:      script,
		stubArgs:    filepath.Join(root, "stub-args"),
		stubBuild:   filepath.Join(root, "stub-build"),
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
		"STUB_BUILD="+f.stubBuild,
		"STUB_REQUIRED="+f.stubRequire,
		"STUB_IMAGE_READY="+filepath.Join(f.root, "image-ready"),
		"STUB_EXIT="+strconv.Itoa(exitCode),
	)
	output, err := cmd.CombinedOutput()
	result := postgresConformanceRun{output: string(output), err: err}
	if args, readErr := os.ReadFile(f.stubArgs); readErr == nil {
		result.args = strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	}
	if args, readErr := os.ReadFile(f.stubBuild); readErr == nil {
		result.buildArgs = strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
		if len(result.buildArgs) > 2 {
			result.buildArgs[2] = filepath.Base(result.buildArgs[2])
		}
	}
	if required, readErr := os.ReadFile(f.stubRequire); readErr == nil {
		result.required = string(required)
	}
	return result
}
