package apigen

import (
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCheckedInExampleBuilds(t *testing.T) {
	t.Helper()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	exampleRoot := filepath.Join(moduleRootFromWorkingDir(t, cwd), "example")
	runCommand(t, exampleRoot, "go", "test", "./...")
}

func TestExample_TypeSpecToGeneratedBuildAndRun(t *testing.T) {
	t.Helper()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	moduleRoot := moduleRootFromWorkingDir(t, cwd)
	apigenRoot := moduleRoot
	exampleRoot := prepareExampleWorkspace(t, moduleRoot)
	addr := allocateLoopbackAddr(t)
	baseURL := "http://" + addr
	manifestPath := filepath.Join(exampleRoot, "apigen.targets.yaml")

	cleanupPaths := []string{
		filepath.Join(exampleRoot, "api", "gen"),
		filepath.Join(exampleRoot, "contracts", "gen"),
		filepath.Join(exampleRoot, "internal", "api", "gen", "server.apigen.gen.go"),
		filepath.Join(exampleRoot, "internal", "api", "gen", "request_models.gen.go"),
		filepath.Join(exampleRoot, "internal", "contracts", "models.gen.go"),
		filepath.Join(exampleRoot, "cmd", "cli", "gen", "apigen_registry.gen.go"),
		filepath.Join(exampleRoot, "server"),
		filepath.Join(exampleRoot, "cli"),
	}
	t.Cleanup(func() {
		for _, path := range cleanupPaths {
			_ = os.RemoveAll(path)
		}
	})
	for _, path := range cleanupPaths {
		_ = os.RemoveAll(path)
	}

	runCommand(
		t,
		apigenRoot,
		"go",
		"run",
		"./cmd/apigen",
		"typespec-compile",
		"-manifest", manifestPath,
		"-target", "example",
	)

	runCommand(
		t,
		apigenRoot,
		"go",
		"run",
		"./cmd/apigen",
		"typespec-compile",
		"-manifest", manifestPath,
		"-target", "signal-contracts",
	)

	runCommand(
		t,
		apigenRoot,
		"go",
		"run",
		"./cmd/apigen",
		"all",
		"-manifest", manifestPath,
		"-target", "example",
	)

	runCommand(
		t,
		apigenRoot,
		"go",
		"run",
		"./cmd/apigen",
		"all",
		"-manifest", manifestPath,
		"-target", "signal-contracts",
	)

	serverBinary := filepath.Join(exampleRoot, "server")
	cliBinary := filepath.Join(exampleRoot, "cli")
	runCommand(t, exampleRoot, "go", "build", "-o", serverBinary, "./cmd/server")
	runCommand(t, exampleRoot, "go", "build", "-o", cliBinary, "./cmd/cli")

	serverGenerated := mustReadFile(t, filepath.Join(exampleRoot, "internal", "api", "gen", "server.apigen.gen.go"))
	require.Contains(t, serverGenerated, "APIGen Todo Example")
	require.Contains(t, serverGenerated, `func RegisterAPIGenStrictRoutes`)
	require.Contains(t, serverGenerated, `type GenStrictServerInterface interface`)
	require.Contains(t, serverGenerated, `"/todos"`)
	require.Contains(t, serverGenerated, "package gen")
	require.Contains(t, serverGenerated, "GetAPIGenToolContracts")
	require.Contains(t, serverGenerated, `"list_todos"`)
	require.Contains(t, serverGenerated, `"delete_todo"`)

	cliGenerated := mustReadFile(t, filepath.Join(exampleRoot, "cmd", "cli", "gen", "apigen_registry.gen.go"))
	require.Contains(t, cliGenerated, `Command: []string{"todos", "list"}`)
	require.Contains(t, cliGenerated, `Command: []string{"todos", "create"}`)
	require.Contains(t, cliGenerated, `Command: []string{"todos", "complete"}`)
	openAPI := mustReadFile(t, filepath.Join(exampleRoot, "api", "gen", "openapi.yaml"))
	require.Contains(t, openAPI, "x-apigen-tool:")
	require.NotContains(t, openAPI, "x-agent:")
	require.Contains(t, cliGenerated, `Command: []string{"todos", "delete"}`)
	require.Contains(t, cliGenerated, `Confirm: "always"`)
	require.NotContains(t, cliGenerated, "widgets")

	assertGeneratedImportsUsePublicSurfaces(t, filepath.Join(exampleRoot, "internal", "api", "gen", "server.apigen.gen.go"))
	assertGeneratedImportsUsePublicSurfaces(t, filepath.Join(exampleRoot, "internal", "api", "gen", "request_models.gen.go"))
	assertGeneratedImportsUsePublicSurfaces(t, filepath.Join(exampleRoot, "internal", "contracts", "models.gen.go"))
	assertGeneratedImportsUsePublicSurfaces(t, filepath.Join(exampleRoot, "cmd", "cli", "gen", "apigen_registry.gen.go"))

	contractIR := mustReadFile(t, filepath.Join(exampleRoot, "contracts", "gen", "json-ir.json"))
	require.Contains(t, contractIR, `"schema_version": "v4"`)
	require.Contains(t, contractIR, `"contracts":`)
	require.Contains(t, contractIR, `"DashboardEnvelope"`)
	require.NotContains(t, contractIR, `"openapi"`)

	contractGo := mustReadFile(t, filepath.Join(exampleRoot, "internal", "contracts", "models.gen.go"))
	require.Contains(t, contractGo, "type DashboardEnvelope struct")
	require.Contains(t, contractGo, "Visuals map[string]DashboardVisual")

	contractTS := mustReadFile(t, filepath.Join(exampleRoot, "contracts", "gen", "contracts.ts"))
	require.Contains(t, contractTS, "export interface DashboardEnvelope")
	require.Contains(t, contractTS, "visuals: Record<string, DashboardVisual>")

	contractSchema := mustReadFile(t, filepath.Join(exampleRoot, "contracts", "gen", "contracts.schema.json"))
	require.Contains(t, contractSchema, `"x-apigen-contracts"`)
	require.Contains(t, contractSchema, `"x-libredash-signal-key"`)

	routerSource := mustReadFile(t, filepath.Join(exampleRoot, "internal", "api", "router.go"))
	require.Contains(t, routerSource, "gen.RegisterAPIGenStrictRoutes")

	serverSource := mustReadFile(t, filepath.Join(exampleRoot, "internal", "api", "server.go"))
	require.Contains(t, serverSource, "var _ gen.GenStrictServerInterface = (*Server)(nil)")

	serverCmd := exec.Command(serverBinary)
	serverCmd.Dir = exampleRoot
	serverCmd.Env = append(os.Environ(), "TODO_EXAMPLE_ADDR="+addr)
	serverOutput := &strings.Builder{}
	serverCmd.Stdout = serverOutput
	serverCmd.Stderr = serverOutput
	require.NoError(t, serverCmd.Start())
	t.Cleanup(func() {
		_ = serverCmd.Process.Kill()
		_ = serverCmd.Wait()
	})

	waitForHTTP(t, baseURL+"/todos")

	spec := mustFetchJSON(t, baseURL+"/openapi.json")
	require.Contains(t, spec, "APIGen Todo Example")
	require.Contains(t, spec, "/todos")

	cliEnv := []string{"TODO_EXAMPLE_BASE_URL=" + baseURL}

	listOutput := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "list")
	require.Contains(t, listOutput, "todo-1")
	require.Contains(t, listOutput, "write docs")
	require.Contains(t, listOutput, "todo-2")

	createOutput := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "create", "buy milk")
	require.Contains(t, createOutput, "todo-3")
	require.Contains(t, createOutput, "buy milk")
	require.Contains(t, createOutput, "open")

	getOutput := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "get", "todo-3")
	require.Contains(t, getOutput, "todo-3")
	require.Contains(t, getOutput, "buy milk")
	require.Contains(t, getOutput, "open")

	completeOutput := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "complete", "todo-3")
	require.Contains(t, completeOutput, "todo-3")
	require.Contains(t, completeOutput, "completed")

	filteredList := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "list", "--status", "completed")
	require.Contains(t, filteredList, "todo-2")
	require.Contains(t, filteredList, "todo-3")
	require.NotContains(t, filteredList, "todo-1")

	runCommandEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "delete", "todo-3", "--yes")

	finalList := runCommandOutputEnv(t, exampleRoot, cliEnv, cliBinary, "todos", "list")
	require.NotContains(t, finalList, "todo-3")
}

func runCommand(t *testing.T, dir string, name string, args ...string) {
	t.Helper()

	runCommandEnv(t, dir, nil, name, args...)
}

func runCommandEnv(t *testing.T, dir string, extraEnv []string, name string, args ...string) {
	t.Helper()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed:\n%s", name, strings.Join(args, " "), string(output))
	}
}

func runCommandOutput(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()

	return runCommandOutputEnv(t, dir, nil, name, args...)
}

func runCommandOutputEnv(t *testing.T, dir string, extraEnv []string, name string, args ...string) string {
	t.Helper()

	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), extraEnv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed:\n%s", name, strings.Join(args, " "), string(output))
	}
	return string(output)
}

func allocateLoopbackAddr(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		_ = listener.Close()
	}()
	return listener.Addr().String()
}

func waitForHTTP(t *testing.T, endpoint string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(endpoint) //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", endpoint)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

func mustFetchJSON(t *testing.T, endpoint string) string {
	t.Helper()

	resp, err := http.Get(endpoint) //nolint:noctx
	require.NoError(t, err)
	defer func() {
		_ = resp.Body.Close()
	}()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return string(encoded)
}

func assertGeneratedImportsUsePublicSurfaces(t *testing.T, path string) {
	t.Helper()

	content := mustReadFile(t, path)
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, `"github.com/Yacobolo/toolbelt/`) {
			continue
		}
		if strings.Contains(line, `"github.com/Yacobolo/toolbelt/apigen/`) {
			continue
		}
		t.Fatalf("%s imports non-public repo package: %s", path, strings.TrimSpace(line))
	}
}

func moduleRootFromWorkingDir(t *testing.T, cwd string) string {
	t.Helper()

	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate apigen module root from %s", cwd)
		}
		dir = parent
	}
}

func prepareExampleWorkspace(t *testing.T, moduleRoot string) string {
	t.Helper()

	sourceRoot := filepath.Join(moduleRoot, "example")
	workspaceRoot := filepath.Join(t.TempDir(), "example-consumer")
	require.NoError(t, copyDir(sourceRoot, workspaceRoot))

	goModPath := filepath.Join(workspaceRoot, "go.mod")
	goModContent, err := os.ReadFile(goModPath)
	require.NoError(t, err)

	updatedGoMod := strings.ReplaceAll(
		string(goModContent),
		"replace github.com/Yacobolo/toolbelt/apigen => ..",
		"replace github.com/Yacobolo/toolbelt/apigen => "+moduleRoot,
	)
	require.NoError(t, os.WriteFile(goModPath, []byte(updatedGoMod), 0o644))

	return workspaceRoot
}

func copyDir(src string, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, relPath)
		if d.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		return copyFile(path, targetPath)
	})
}

func copyFile(src string, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		_ = srcFile.Close()
	}()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		_ = dstFile.Close()
	}()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return nil
}
