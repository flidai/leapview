package app

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
	apigenapi "github.com/flidai/leapview/internal/app/api/gen"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	manageddatagen "github.com/flidai/leapview/internal/manageddata/api/gen"
	projectgen "github.com/flidai/leapview/internal/project/api/gen"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

const expectedAPIGenAggregateOperationCount = 197

func TestAPIGenUsesTypedClientGenerator(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	for _, source := range []string{
		"typespec_entrypoint: typespec/main.tsp",
		"typespec_entrypoint: signals/main.tsp",
		"typespec_entrypoint: visualization/main.tsp",
		"typespec_entrypoint: desktop-discovery/main.tsp",
		"typespec_dir: ../internal/agent/contracts",
	} {
		if !strings.Contains(manifestText, source) {
			t.Fatalf("manifest should select TypeSpec source %q, got:\n%s", source, manifestText)
		}
	}
	if strings.Contains(manifestText, "cue_dir:") {
		t.Fatalf("manifest should not use cue_dir after APIGen v0.3.0 migration")
	}
	for _, want := range []string{
		"unmatched: error",
		"LeapViewAPI:",
		"LeapViewAPI.Access:",
		"LeapViewAPI.Agent:",
		"LeapViewAPI.Analytics:",
		"LeapViewAPI.Dashboard:",
		"LeapViewAPI.Deployment:",
		"LeapViewAPI.ManagedData:",
		"LeapViewAPI.Project:",
		"LeapViewAPI.Protocol:",
		"LeapViewAPI.Refresh:",
		"LeapViewAPI.Release:",
		"import_path: github.com/flidai/leapview/internal/app/api/gen",
	} {
		if !strings.Contains(manifestText, want) {
			t.Fatalf("manifest should define the coalesced capability package plan setting %q", want)
		}
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	taskText := string(taskfile)
	for _, want := range []string{
		"- task: api:generate\n      - task: agent-contracts:generate\n      - task: ui-signals:generate\n      - task: schema:generate",
		"- task: desktop-discovery:generate",
		"schema:generate:\n    desc: Generate JSON Schema artifacts for LeapView YAML contracts\n    deps:\n      - db:generate\n      - config:generate\n      - api:generate\n      - ui-signals:generate",
		"ui-signals:generate:\n    desc: Generate UI signal Go and TypeScript contracts from TypeSpec\n    deps:\n      - api:generate",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("Taskfile.yml does not enforce generated-model ordering %q", want)
		}
	}
	for _, want := range []string{
		"github.com/Yacobolo/toolbelt/apigen/cmd/apigen typespec-compile",
		"github.com/Yacobolo/toolbelt/apigen/cmd/apigen all",
		"go -C pkg/apigen test ./...",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("Taskfile.yml missing generation command %q", want)
		}
	}
	for _, forbidden := range []string{"cue-compile", "apigen@v0.2.0", "apigen@v0.3.0", "apigen@v0.3.2", "apigen@v0.3.3", "apigen@v0.4.0", "apigen@v0.5.0", "apigen@v0.5.1", "apigen@v0.5.2", "apigen@v0.5.3", "apigen@v0.6.0", "apigen@v0.6.1", "apigen@v0.6.2", "apigen@v0.6.3", "apigen@v0.6.4", "apigen@v0.6.5", "apigen@v0.7.0", "apigen@v0.7.1", "apigen@v0.7.2", "apigen@v0.7.3", "apigenpostprocess"} {
		if strings.Contains(taskText, forbidden) {
			t.Fatalf("Taskfile.yml should not contain superseded generator %q", forbidden)
		}
	}
	buildSources, err := os.ReadFile(filepath.Join(root, "scripts", "generate_build_sources.sh"))
	if err != nil {
		t.Fatalf("read container source-generation script: %v", err)
	}
	if want := "APIGEN=github.com/Yacobolo/toolbelt/apigen/cmd/apigen"; !strings.Contains(string(buildSources), want) {
		t.Fatalf("container source-generation script missing APIGen pin %q", want)
	}
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.Contains(string(goMod), "replace github.com/Yacobolo/toolbelt/apigen => ./pkg/apigen") {
		t.Fatal("go.mod does not select the vendored APIGen module")
	}
	if _, err := os.Stat(filepath.Join(root, "pkg", "apigen", "UPSTREAM.md")); err != nil {
		t.Fatalf("vendored APIGen provenance is missing: %v", err)
	}
	for _, want := range []string{
		"typespec-compile -manifest api/apigen.yaml -target desktop-discovery-contracts",
		"all -manifest api/apigen.yaml -target desktop-discovery-contracts",
	} {
		if !strings.Contains(string(buildSources), want) {
			t.Fatalf("container source-generation script missing desktop discovery generation command %q", want)
		}
	}
	if want := "go run ./internal/app/tools/layoutcontractgen"; !strings.Contains(string(buildSources), want) {
		t.Fatalf("container source-generation script missing layout contract generation %q", want)
	}

	ir, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}
	var irDoc map[string]any
	if err := json.Unmarshal(ir, &irDoc); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}
	if got := irDoc["schema_version"]; got != "v4" {
		t.Fatalf("APIGen IR schema_version = %#v, want v4", got)
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "tools", "apigenpostprocess")); !os.IsNotExist(err) {
		t.Fatalf("APIGen should not require a postprocessor, stat error = %v", err)
	}
	for path, forbidden := range map[string]string{
		filepath.Join(root, "api", "typespec", "bi.tsp"):                        "toolbelt#34",
		filepath.Join(root, "internal", "agent", "tools", "apigen_provider.go"): "projectUnionToolResult",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("APIGen superseded workaround %q in %s", forbidden, path)
		}
	}
}

func TestAPIGenAgentCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	for _, want := range []string{
		"aggregate:\n        dir: ../internal/app/api/aggregate\n        package: aggregate",
		"LeapViewAPI.Agent:\n          dir: ../internal/agent/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/agent/api/gen",
	} {
		if !strings.Contains(manifestText, want) {
			t.Fatalf("manifest missing Agent capability package plan %q", want)
		}
	}
	if strings.Contains(manifestText, "LeapViewAPI.Agent: *leapview_api_go_package") {
		t.Fatal("Agent namespace is still coalesced into the application generated package")
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, want := range []string{
		"internal/agent/api/gen/request_models.gen.go",
		"internal/agent/api/gen/server.apigen.gen.go",
		"internal/app/api/aggregate/server.apigen.gen.go",
		"internal/platform/http/api/gen/request_models.gen.go",
	} {
		if !strings.Contains(string(taskfile), want) {
			t.Fatalf("Taskfile.yml does not track generated Agent composition artifact %q", want)
		}
	}
}

func TestAPIGenAgentCapabilityOwnsItsOperationSurface(t *testing.T) {
	agentContracts := agentgen.GetAPIGenOperationContracts()
	if got, want := len(agentContracts), 13; got != want {
		t.Fatalf("Agent generated operations = %d, want %d", got, want)
	}
	for operationID, contract := range agentContracts {
		if len(contract.Tags) != 1 || contract.Tags[0] != "Agent" {
			t.Errorf("Agent operation %q tags = %v", operationID, contract.Tags)
		}
		if _, exists := apigenapi.GetAPIGenOperationContracts()[operationID]; exists {
			t.Errorf("Agent operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenAccessCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	for _, want := range []string{
		"LeapViewAPI.Access:\n          dir: ../internal/access/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/access/api/gen",
		"LeapViewAPI.Analytics:\n          dir: ../internal/analytics/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/analytics/api/gen",
	} {
		if !strings.Contains(manifestText, want) {
			t.Fatalf("manifest missing Access capability package plan %q", want)
		}
	}
	if strings.Contains(manifestText, "LeapViewAPI.Access: *leapview_api_go_package") {
		t.Fatal("Access namespace is still coalesced into the application generated package")
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, want := range []string{
		"internal/access/api/gen/request_models.gen.go",
		"internal/access/api/gen/server.apigen.gen.go",
		"internal/analytics/api/gen/request_models.gen.go",
		"internal/analytics/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), want) {
			t.Fatalf("Taskfile.yml does not track generated Access artifact %q", want)
		}
	}
}

func TestAPIGenAccessCapabilityOwnsItsOperationSurface(t *testing.T) {
	accessContracts := accessgen.GetAPIGenOperationContracts()
	if got, want := len(accessContracts), 58; got != want {
		t.Fatalf("Access generated operations = %d, want %d", got, want)
	}
	allowedTags := map[string]bool{"Access": true, "Audit": true, "Current User": true}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range accessContracts {
		if len(contract.Tags) != 1 || !allowedTags[contract.Tags[0]] {
			t.Errorf("Access operation %q tags = %v", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Access operation %q is still emitted by the application package", operationID)
		}
	}
	if _, exists := accessContracts["listQueryEvents"]; exists {
		t.Fatal("Analytics-owned listQueryEvents is emitted by the Access package")
	}
	if _, exists := appContracts["listQueryEvents"]; exists {
		t.Fatal("Analytics-owned listQueryEvents is still emitted by the application package")
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenAnalyticsCapabilityOwnsItsOperationSurface(t *testing.T) {
	analyticsContracts := analyticsgen.GetAPIGenOperationContracts()
	if got, want := len(analyticsContracts), 11; got != want {
		t.Fatalf("Analytics generated operations = %d, want %d", got, want)
	}
	for operationID, contract := range analyticsContracts {
		wantTag := "Connections"
		if operationID == "listQueryEvents" {
			wantTag = "Audit"
		}
		if len(contract.Tags) != 1 || contract.Tags[0] != wantTag {
			t.Fatalf("%s tags = %v, want [%s]", operationID, contract.Tags, wantTag)
		}
		if _, exists := apigenapi.GetAPIGenOperationContracts()[operationID]; exists {
			t.Fatalf("Analytics-owned %s is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenProjectCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.Project:\n          dir: ../internal/project/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/project/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing Project capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.Project: *leapview_api_go_package") {
		t.Fatal("Project namespace is still coalesced into the application generated package")
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/project/api/gen/request_models.gen.go",
		"internal/project/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated Project artifact %q", path)
		}
	}
}

func TestAPIGenProjectCapabilityOwnsItsOperationSurface(t *testing.T) {
	projectContracts := projectgen.GetAPIGenOperationContracts()
	if got, want := len(projectContracts), 2; got != want {
		t.Fatalf("Project generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	allowedTags := map[string]bool{"Projects": true, "Search": true}
	for operationID, contract := range projectContracts {
		if len(contract.Tags) != 1 || !allowedTags[contract.Tags[0]] {
			t.Errorf("Project operation %q tags = %v, want [Projects] or [Search]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Project operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenRefreshCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.Refresh:\n          dir: ../internal/refresh/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/refresh/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing Refresh capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.Refresh: *leapview_api_go_package") {
		t.Fatal("Refresh namespace is still coalesced into the application generated package")
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/refresh/api/gen/request_models.gen.go",
		"internal/refresh/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated Refresh artifact %q", path)
		}
	}
}

func TestAPIGenRefreshCapabilityOwnsItsOperationSurface(t *testing.T) {
	refreshContracts := refreshgen.GetAPIGenOperationContracts()
	if got, want := len(refreshContracts), 5; got != want {
		t.Fatalf("Refresh generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range refreshContracts {
		if len(contract.Tags) != 1 || contract.Tags[0] != "Refresh Runs" {
			t.Errorf("Refresh operation %q tags = %v, want [Refresh Runs]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Refresh operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenDeploymentCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.Deployment:\n          dir: ../internal/deployment/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/deployment/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing Deployment capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.Deployment: *leapview_api_go_package") {
		t.Fatal("Deployment namespace is still coalesced into the application generated package")
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/deployment/api/gen/request_models.gen.go",
		"internal/deployment/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated Deployment artifact %q", path)
		}
	}
}

func TestAPIGenDeploymentCapabilityOwnsItsOperationSurface(t *testing.T) {
	contracts := deploymentgen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 40; got != want {
		t.Fatalf("Deployment generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range contracts {
		if len(contract.Tags) != 1 || (contract.Tags[0] != "Deployments" && contract.Tags[0] != "Delivery") {
			t.Errorf("Deployment operation %q tags = %v, want [Deployments] or [Delivery]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Deployment operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenReleaseCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.Release:\n          dir: ../internal/release/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/release/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing Release capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.Release: *leapview_api_go_package") {
		t.Fatal("Release namespace is still coalesced into the application generated package")
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/release/api/gen/request_models.gen.go",
		"internal/release/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated Release artifact %q", path)
		}
	}
}

func TestAPIGenReleaseCapabilityOwnsItsOperationSurface(t *testing.T) {
	contracts := releasegen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 6; got != want {
		t.Fatalf("Release generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range contracts {
		if len(contract.Tags) != 1 || contract.Tags[0] != "Releases" {
			t.Errorf("Release operation %q tags = %v, want [Releases]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Release operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenManagedDataCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.ManagedData:\n          dir: ../internal/manageddata/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/manageddata/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing ManagedData capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.ManagedData: *leapview_api_go_package") {
		t.Fatal("ManagedData namespace is still coalesced into the application generated package")
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/manageddata/api/gen/request_models.gen.go",
		"internal/manageddata/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated ManagedData artifact %q", path)
		}
	}
}

func TestAPIGenManagedDataCapabilityOwnsItsOperationSurface(t *testing.T) {
	contracts := manageddatagen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 15; got != want {
		t.Fatalf("ManagedData generated operations = %d, want %d", got, want)
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range contracts {
		if len(contract.Tags) != 1 || contract.Tags[0] != "Managed Data" {
			t.Errorf("ManagedData operation %q tags = %v, want [Managed Data]", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("ManagedData operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenDashboardCapabilityOwnsItsGeneratedPackage(t *testing.T) {
	root := projectRoot(t)
	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	want := "LeapViewAPI.Dashboard:\n          dir: ../internal/dashboard/api/gen\n          package: gen\n          import_path: github.com/flidai/leapview/internal/dashboard/api/gen"
	if !strings.Contains(manifestText, want) {
		t.Fatalf("manifest missing Dashboard capability package plan %q", want)
	}
	if strings.Contains(manifestText, "LeapViewAPI.Dashboard: *leapview_api_go_package") {
		t.Fatal("Dashboard namespace is still coalesced into the application generated package")
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	for _, path := range []string{
		"internal/dashboard/api/gen/request_models.gen.go",
		"internal/dashboard/api/gen/server.apigen.gen.go",
	} {
		if !strings.Contains(string(taskfile), path) {
			t.Fatalf("Taskfile.yml does not track generated Dashboard artifact %q", path)
		}
	}
}

func TestAPIGenDashboardCapabilityOwnsItsOperationSurface(t *testing.T) {
	contracts := dashboardgen.GetAPIGenOperationContracts()
	if got, want := len(contracts), 36; got != want {
		t.Fatalf("Dashboard generated operations = %d, want %d", got, want)
	}
	allowedTags := map[string]bool{"BI": true, "Publications": true, "Dashboard Authoring": true}
	for operationID := range map[string]struct{}{
		"listDashboardAuthoringCatalog":          {},
		"getDashboardAuthoringDashboard":         {},
		"getDashboardAuthoringDraft":             {},
		"getDashboardAuthoringDraftRevision":     {},
		"getDashboardAuthoringPublishedRevision": {},
		"createDashboardAuthoringDraft":          {},
		"executeDashboardAuthoringCommand":       {},
		"forkDashboardAuthoringDraft":            {},
		"previewDashboardAuthoringDraft":         {},
		"exportDashboardAuthoringSource":         {},
	} {
		if _, ok := contracts[operationID]; !ok {
			t.Errorf("Dashboard authoring operation %q is missing from generated package", operationID)
		}
	}
	appContracts := apigenapi.GetAPIGenOperationContracts()
	for operationID, contract := range contracts {
		if len(contract.Tags) != 1 || !allowedTags[contract.Tags[0]] {
			t.Errorf("Dashboard operation %q tags = %v", operationID, contract.Tags)
		}
		if _, exists := appContracts[operationID]; exists {
			t.Errorf("Dashboard operation %q is still emitted by the application package", operationID)
		}
	}
	if got, want := len(apiaggregate.GetAPIGenOperationContracts()), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("aggregate generated operations = %d, want %d", got, want)
	}
}

func TestAPIGenIRAssignsCapabilityNamespaces(t *testing.T) {
	root := projectRoot(t)
	content, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}

	var document struct {
		Endpoints []struct {
			OperationID string   `json:"operation_id"`
			Namespace   string   `json:"namespace"`
			Tags        []string `json:"tags"`
		} `json:"endpoints"`
		Schemas map[string]struct {
			Namespace string `json:"namespace"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}
	if got, want := len(document.Endpoints), expectedAPIGenAggregateOperationCount; got != want {
		t.Fatalf("APIGen IR endpoints = %d, want %d", got, want)
	}

	namespaceByTag := map[string]string{
		"System":              "LeapViewAPI",
		"Instance":            "LeapViewAPI",
		"Current User":        "LeapViewAPI.Access",
		"Access":              "LeapViewAPI.Access",
		"Audit":               "LeapViewAPI.Access",
		"Agent":               "LeapViewAPI.Agent",
		"BI":                  "LeapViewAPI.Dashboard",
		"Dashboard Authoring": "LeapViewAPI.Dashboard",
		"Connections":         "LeapViewAPI.Analytics",
		"Publications":        "LeapViewAPI.Dashboard",
		"Deployments":         "LeapViewAPI.Deployment",
		"Delivery":            "LeapViewAPI.Deployment",
		"Managed Data":        "LeapViewAPI.ManagedData",
		"Projects":            "LeapViewAPI.Project",
		"Search":              "LeapViewAPI.Project",
		"Refresh Runs":        "LeapViewAPI.Refresh",
		"Releases":            "LeapViewAPI.Release",
	}
	for _, endpoint := range document.Endpoints {
		if len(endpoint.Tags) != 1 {
			t.Errorf("endpoint %q tags = %v, want exactly one ownership tag", endpoint.OperationID, endpoint.Tags)
			continue
		}
		want, ok := namespaceByTag[endpoint.Tags[0]]
		if !ok {
			t.Errorf("endpoint %q has unmapped ownership tag %q", endpoint.OperationID, endpoint.Tags[0])
			continue
		}
		if endpoint.OperationID == "listQueryEvents" {
			want = "LeapViewAPI.Analytics"
		}
		if endpoint.Namespace != want {
			t.Errorf("endpoint %q namespace = %q, want %q for tag %q", endpoint.OperationID, endpoint.Namespace, want, endpoint.Tags[0])
		}
	}

	allowedSchemaNamespaces := map[string]struct{}{
		"LeapViewAPI":             {},
		"LeapViewAPI.Access":      {},
		"LeapViewAPI.Agent":       {},
		"LeapViewAPI.Analytics":   {},
		"LeapViewAPI.Dashboard":   {},
		"LeapViewAPI.Deployment":  {},
		"LeapViewAPI.ManagedData": {},
		"LeapViewAPI.Project":     {},
		"LeapViewAPI.Protocol":    {},
		"LeapViewAPI.Refresh":     {},
		"LeapViewAPI.Release":     {},
		"LeapViewVisualization":   {},
	}
	for name, schema := range document.Schemas {
		if _, ok := allowedSchemaNamespaces[schema.Namespace]; !ok {
			t.Errorf("schema %q namespace = %q, want an explicit capability, root, or external namespace", name, schema.Namespace)
		}
	}
}

func TestAPIGenOwnsUISignalContracts(t *testing.T) {
	root := projectRoot(t)

	manifest, err := os.ReadFile(filepath.Join(root, "api", "apigen.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifestText := string(manifest)
	for _, want := range []string{
		"name: ui-signals",
		"kind: contracts",
		"typespec_dir: .",
		"typespec_entrypoint: signals/main.tsp",
		"ts_out: ../web/generated/signals/index.ts",
	} {
		if !strings.Contains(manifestText, want) {
			t.Fatalf("APIGen manifest missing UI signal contract setting %q", want)
		}
	}
	if strings.Contains(manifestText, "json_schema_out: ../schemas/signals/ui-signals.schema.json") {
		t.Fatal("APIGen manifest should not generate an unused UI signal JSON Schema")
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	taskText := string(taskfile)
	for _, want := range []string{
		"typespec-compile -manifest api/apigen.yaml -target ui-signals",
		"all -manifest api/apigen.yaml -target ui-signals",
		"go run ./internal/app/tools/signalcontracts",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("Taskfile.yml missing UI signal generation command %q", want)
		}
	}
	if strings.Contains(taskText, "go run ./internal/app/tools/uisignalsgen") {
		t.Fatal("Taskfile.yml still uses the Go reflection UI signal generator")
	}
	if strings.Contains(taskText, "schemas/signals/ui-signals.schema.json") {
		t.Fatal("Taskfile.yml should not track an unused UI signal JSON Schema")
	}

	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, path := range []string{
		"internal/access/ui/signals/models.gen.go",
		"internal/admin/ui/signals/models.gen.go",
		"internal/agent/ui/signals/models.gen.go",
		"internal/dashboard/ui/signals/models.gen.go",
	} {
		if !strings.Contains(string(gitignore), path) {
			t.Fatalf("generated Go UI signal models should ignore %s", path)
		}
	}

	for _, path := range []string{
		"internal/access/ui/signals/models.gen.go",
		"internal/admin/ui/signals/models.gen.go",
		"internal/agent/ui/signals/models.gen.go",
		"internal/dashboard/ui/signals/models.gen.go",
	} {
		if !strings.Contains(taskText, path) {
			t.Fatalf("repository generation contract does not include %s", path)
		}
	}

	typespec, err := os.ReadFile(filepath.Join(root, "api", "signals", "main.tsp"))
	if err != nil {
		t.Fatalf("read UI signal TypeSpec source: %v", err)
	}
	typespecText := string(typespec)
	for _, want := range []string{"@apigen.`package`", "@apigen.contract", "@apigen.`metadata`"} {
		if !strings.Contains(typespecText, want) {
			t.Fatalf("UI signal TypeSpec source missing %q", want)
		}
	}

	for _, path := range [][]string{
		{"internal", "access", "ui", "signals", "models.gen.go"},
		{"internal", "admin", "ui", "signals", "models.gen.go"},
		{"internal", "agent", "ui", "signals", "models.gen.go"},
		{"internal", "dashboard", "ui", "signals", "models.gen.go"},
	} {
		generatedGo, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
		if err != nil {
			t.Fatalf("read generated Go UI signal models %s: %v", filepath.Join(path...), err)
		}
		if !strings.Contains(string(generatedGo), "Code generated by apigen data-contract Go emitter") {
			t.Fatalf("%s was not generated by APIGen data contracts", filepath.Join(path...))
		}
	}

	if _, err := os.Stat(filepath.Join(root, "internal", "tools", "uisignalsgen")); !os.IsNotExist(err) {
		t.Fatalf("legacy UI signal reflection generator still exists: %v", err)
	}

	ir, err := os.ReadFile(filepath.Join(root, "api", "gen", "ui-signals-ir.json"))
	if err != nil {
		t.Fatalf("read UI signal contract IR: %v", err)
	}
	var irDoc struct {
		SchemaVersion string `json:"schema_version"`
		Schemas       map[string]struct {
			Namespace string `json:"namespace"`
		} `json:"schemas"`
		Contracts []struct {
			Name       string         `json:"name"`
			Kind       string         `json:"kind"`
			Extensions map[string]any `json:"extensions"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(ir, &irDoc); err != nil {
		t.Fatalf("decode UI signal contract IR: %v", err)
	}
	if irDoc.SchemaVersion != "v4" {
		t.Fatalf("UI signal IR schema_version = %q, want v4", irDoc.SchemaVersion)
	}
	if len(irDoc.Contracts) != 121 {
		t.Fatalf("UI signal IR contracts = %d, want 121", len(irDoc.Contracts))
	}
	foundEnvelopeMetadata := false
	foundImportedVisualizationRoot := false
	foundDashboardVisualizationSignal := false
	for _, contract := range irDoc.Contracts {
		if contract.Name == "DashboardEnvelope" && contract.Kind == "ui-envelope" && contract.Extensions["x-leapview-contract-role"] == "envelope" {
			foundEnvelopeMetadata = true
		}
		if contract.Name == "VisualizationEnvelope" {
			foundImportedVisualizationRoot = true
		}
		if contract.Name == "DashboardVisualizationSignal" && contract.Kind == "ui-signal" {
			foundDashboardVisualizationSignal = true
		}
	}
	if !foundEnvelopeMetadata {
		t.Fatal("DashboardEnvelope contract metadata was not preserved in IR")
	}
	if foundImportedVisualizationRoot {
		t.Fatal("UI signal contract roots must not duplicate imported visualization contracts")
	}
	if schema, ok := irDoc.Schemas["VisualizationEnvelope"]; !ok || schema.Namespace != "LeapViewVisualization" {
		t.Fatalf("UI signals do not retain the canonical visualization schema ownership: %#v", schema)
	}
	if !foundDashboardVisualizationSignal {
		t.Fatal("UI signals do not emit the dashboard visualization transport")
	}
}

func TestAPIGenRoutesCoverHeadlessAPINotUITransports(t *testing.T) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatalf("embedded openapi: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi paths missing: %#v", spec["paths"])
	}

	for _, path := range []string{
		"/api/v1/me",
		"/api/v1/me/effective-capabilities",
		"/api/v1/me/api-tokens",
		"/api/v1/me/api-tokens/{token}",
		"/api/v1/me/sessions",
		"/api/v1/me/sessions/{session}",
		"/api/v1/principals",
		"/api/v1/principals/{principal}",
		"/api/v1/search",
		"/api/v1/dashboards",
		"/api/v1/dashboards/{dashboard}",
		"/api/v1/dashboards/{dashboard}/pages/{page}",
		"/api/v1/dashboards/{dashboard}/pages/{page}/visuals/{visual}",
		"/api/v1/dashboards/{dashboard}/pages/{page}/visuals/{visual}/query",
		"/api/v1/dashboards/{dashboard}/pages/{page}/filters/{filter}",
		"/api/v1/dashboards/{dashboard}/pages/{page}/filters/{filter}/values",
		"/api/v1/dashboards/{dashboard}/pages/{page}/query",
		"/api/v1/semantic-models",
		"/api/v1/semantic-models/{model}",
		"/api/v1/semantic-models/{model}/datasets",
		"/api/v1/semantic-models/{model}/datasets/{dataset}",
		"/api/v1/semantic-models/{model}/datasets/{dataset}/fields",
		"/api/v1/semantic-models/{model}/datasets/{dataset}/preview",
		"/api/v1/semantic-models/{model}/datasets/{dataset}/preview/explain",
		"/api/v1/semantic-models/{model}/relationships",
		"/api/v1/semantic-models/{model}/query",
		"/api/v1/semantic-models/{model}/query/explain",
		"/api/v1/projects/{project}/releases",
		"/api/v1/projects/{project}/releases/{release}/artifact",
		"/api/v1/projects/{project}/releases/{release}/finalize",
		"/api/v1/projects/{project}/deployments",
		"/api/v1/projects/{project}/refresh-runs",
		"/api/v1/projects/{project}/refresh-runs/{run}",
		"/api/v1/agent/config",
		"/api/v1/agent/conversations",
		"/api/v1/agent/conversations/{conversation}",
		"/api/v1/agent/conversations/{conversation}/messages",
		"/api/v1/agent/conversations/{conversation}/runs",
		"/api/v1/agent/conversations/{conversation}/runs/{run}",
		"/api/v1/agent/conversations/{conversation}/runs/{run}/events",
		"/api/v1/principals",
		"/api/v1/principals/{principal}",
		"/api/v1/principals/{principal}/password-reset",
		"/api/v1/projects/{project}/roles",
		"/api/v1/groups",
		"/api/v1/groups/{group}",
		"/api/v1/groups/{group}/members",
		"/api/v1/groups/{group}/members/{principal}",
		"/api/v1/projects/{project}/audit-events",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("generated OpenAPI missing path %s", path)
		}
	}

	for _, path := range []string{"/api/publishes", "/api/v1/admin/agent/config", "/updates", "/commands/select", "/chat/updates", "/dashboards/{dashboard}"} {
		if _, ok := paths[path]; ok {
			t.Fatalf("generated OpenAPI should not include UI transport path %s", path)
		}
	}
	if _, ok := paths["/api/v1/me/permissions"]; ok {
		t.Fatal("generated OpenAPI still includes removed /api/v1/me/permissions path")
	}
}

func TestAPIGenOperationExtensions(t *testing.T) {
	contracts := apiaggregate.GetAPIGenOperationContracts()
	toolContracts := apiaggregate.GetAPIGenToolContracts()
	toolsByOperation := make(map[string]string, len(toolContracts))
	for name, tool := range toolContracts {
		toolsByOperation[tool.OperationID] = name
	}
	agentTools := map[string]string{
		"querySemanticModel":       "query_semantic_model",
		"queryDashboardVisualData": "query_dashboard_visual",
	}
	publicOperations := map[string]bool{
		"getInstance": true,
	}
	authenticatedOperations := map[string]bool{
		"addGroupMember":                   true,
		"archiveAgentConversation":         true,
		"cancelAgentRun":                   true,
		"cancelRefreshRun":                 true,
		"changeCurrentPassword":            true,
		"checkAuthorizationBatch":          true,
		"createAgentConversation":          true,
		"createAgentRun":                   true,
		"createCurrentAPIToken":            true,
		"createDashboardAuthoringDraft":    true,
		"createGroup":                      true,
		"createPrincipal":                  true,
		"createRefreshRun":                 true,
		"createServicePrincipal":           true,
		"createServicePrincipalSecret":     true,
		"decideDeviceAuthorization":        true,
		"deleteCurrentAvatar":              true,
		"deleteGroup":                      true,
		"deletePrincipal":                  true,
		"deleteProductLogo":                true,
		"deleteServicePrincipal":           true,
		"disablePrincipal":                 true,
		"enablePrincipal":                  true,
		"executeDashboardAuthoringCommand": true,
		"forkDashboardAuthoringDraft":      true,
		"getAgentConfig":                   true,
		"getAgentConversation":             true,
		"getAgentRun":                      true,
		"getCapabilities":                  true,
		"getCurrentPrincipal":              true,
		"getDashboardPublication":          true,
		"getGroup":                         true,
		"getPrincipal":                     true,
		"getPrincipalAvatar":               true,
		"getProductAPIStatus":              true,
		"getProductAuthenticationStatus":   true,
		"getProductLogo":                   true,
		"getProductSettings":               true,
		"getProductSystemStatus":           true,
		"getRefreshRun":                    true,
		"getServicePrincipal":              true,
		"getServicePrincipalSecret":        true,
		"listAgentConversations":           true,
		"listAgentEvents":                  true,
		"listAgentMessages":                true,
		"listAgentRuns":                    true,
		"listCurrentAPITokens":             true,
		"listCurrentAuthoringSessions":     true,
		"listCurrentEffectiveCapabilities": true,
		"listCurrentSessions":              true,
		"listDashboardAuthoringCatalog":    true,
		"listDashboardPublications":        true,
		"listDashboards":                   true,
		"listGroupMembers":                 true,
		"listGroups":                       true,
		"listManagedConnections":           true,
		"listPlatformAuditEvents":          true,
		"listPrincipalSessions":            true,
		"listPrincipals":                   true,
		"listRefreshRunEvents":             true,
		"listRefreshRuns":                  true,
		"listSemanticModels":               true,
		"listServicePrincipalSecrets":      true,
		"listServicePrincipals":            true,
		"removeGroupMember":                true,
		"resetPrincipalPassword":           true,
		"resetProductSettings":             true,
		"resumeDashboardPublication":       true,
		"revokeCurrentAPIToken":            true,
		"revokeCurrentAuthoringSession":    true,
		"revokeCurrentSession":             true,
		"revokePrincipalSession":           true,
		"revokeServicePrincipalSecret":     true,
		"rotateDashboardPublication":       true,
		"search":                           true,
		"suspendDashboardPublication":      true,
		"updateAgentConfig":                true,
		"updateAgentConversation":          true,
		"updateCurrentPrincipal":           true,
		"updateCurrentTheme":               true,
		"updateGroup":                      true,
		"updatePrincipal":                  true,
		"updateProductSettings":            true,
		"updateServicePrincipal":           true,
		"uploadCurrentAvatar":              true,
		"uploadProductLogo":                true,
	}
	for operationID, contract := range contracts {
		authz, ok := contract.Extensions["x-authz"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing generated x-authz extension: %#v", operationID, contract.Extensions["x-authz"])
		}
		if publicOperations[operationID] {
			if got := authz["mode"]; got != "none" {
				t.Fatalf("%s x-authz mode = %#v, want none", operationID, got)
			}
			continue
		}
		if authenticatedOperations[operationID] {
			if got := authz["mode"]; got != "authenticated" {
				t.Fatalf("%s x-authz mode = %#v, want authenticated", operationID, got)
			}
			continue
		}
		if got := authz["mode"]; got != "privilege" {
			t.Fatalf("%s x-authz mode = %#v, want privilege", operationID, got)
		}
		value, valueOK := authz["privilege"].(string)
		capability, err := access.ParseCapability(value)
		if !valueOK || err != nil {
			t.Fatalf("%s missing generated privilege metadata", operationID)
		}
		if got := authz["privilege"]; got != string(capability) {
			t.Fatalf("%s x-authz capability = %#v, want %q", operationID, got, capability)
		}
		if wantName, ok := agentTools[operationID]; ok {
			if got := toolsByOperation[operationID]; got != wantName {
				t.Fatalf("%s generated tool name = %q, want %q", operationID, got, wantName)
			}
		} else if name := toolsByOperation[operationID]; name != "" {
			t.Fatalf("%s should not have generated tool %q", operationID, name)
		}
		if _, hasLegacy := contract.Extensions["x-agent"]; hasLegacy {
			t.Fatalf("%s retained legacy x-agent metadata", operationID)
		}
		if _, ok := contract.Extensions["x-leapview-dispatch"]; ok {
			t.Fatalf("%s should not have raw-body dispatch extension", operationID)
		}
	}
}

func TestAPIGenOperationKindsAndRoleMappingAreExhaustive(t *testing.T) {
	rolesByCapability := make(map[access.Capability][]string)
	for _, role := range access.CanonicalProjectRoles() {
		for _, capability := range access.ProjectRoleCapabilities(role) {
			rolesByCapability[capability] = append(rolesByCapability[capability], string(role))
		}
	}
	runtimeContracts := apiaggregate.GetAPIGenCommandRuntimeContracts()
	commandCount := 0
	for operationID, contract := range apiaggregate.GetAPIGenOperationContracts() {
		switch contract.Kind {
		case apiaggregate.GenOperationKindQuery:
			if contract.Command != nil {
				t.Errorf("query %s has command metadata: %#v", operationID, contract.Command)
			}
		case apiaggregate.GenOperationKindCommand:
			commandCount++
			command := contract.Command
			if command == nil {
				t.Errorf("command %s has no command metadata", operationID)
				continue
			}
			if !command.Audit.Required || command.Audit.SuccessAction == "" {
				t.Errorf("command %s does not require a stable success audit: %#v", operationID, command.Audit)
			}
			if command.Audit.Payload == nil {
				t.Errorf("command %s has no typed audit payload contract", operationID)
			}
			if command.Failures == nil {
				t.Errorf("command %s did not explicitly declare its failure vocabulary", operationID)
			}
			runtimeFailures, ok := apiaggregate.GetAPIGenCommandFailureContracts(operationID)
			if !ok {
				t.Errorf("command %s has no generated runtime failure contract", operationID)
			} else if len(runtimeFailures) != len(command.Failures) {
				t.Errorf("command %s runtime failure count = %d, generated count = %d", operationID, len(runtimeFailures), len(command.Failures))
			} else {
				for index, failure := range command.Failures {
					runtimeFailure := runtimeFailures[index]
					if runtimeFailure.Kind != failure.Kind || runtimeFailure.StatusCode != failure.StatusCode || runtimeFailure.Code != failure.Code || runtimeFailure.PublicDetail != failure.PublicDetail {
						t.Errorf("command %s runtime failure %#v differs from generated failure %#v", operationID, runtimeFailure, failure)
					}
					if !slices.Contains(contract.DocumentedStatusCodes, failure.StatusCode) {
						t.Errorf("command %s failure %q status %d is not documented", operationID, failure.Kind, failure.StatusCode)
					}
				}
			}
			if command.Audit.Guarantee != "transactional" && command.Audit.Guarantee != "best-effort" {
				t.Errorf("command %s has no supported audit guarantee: %#v", operationID, command.Audit)
			}
			runtimeContract, ok := apiaggregate.GetAPIGenCommandRuntimeContract(operationID)
			if !ok {
				t.Errorf("command %s has no generated runtime contract", operationID)
			} else if err := runtimeContract.Validate(); err != nil {
				t.Errorf("command %s runtime contract is invalid: %v", operationID, err)
			} else if runtimeContract.OperationID != operationID || runtimeContract.Owner != command.Owner ||
				runtimeContract.Method != contract.Method || runtimeContract.Path != contract.Path ||
				string(runtimeContract.Idempotency) != command.Idempotency || string(runtimeContract.Concurrency) != command.Concurrency ||
				runtimeContract.AuthzMode != command.AuthzMode || runtimeContract.Privilege != command.Privilege ||
				runtimeContract.AuditAction != command.Audit.SuccessAction || string(runtimeContract.Guarantee) != command.Audit.Guarantee {
				t.Errorf("command %s runtime contract %#v differs from generated metadata %#v", operationID, runtimeContract, command)
			} else if command.Audit.Payload == nil || runtimeContract.AuditPayload == nil {
				t.Errorf("command %s runtime audit payload is missing: generated=%#v runtime=%#v", operationID, command.Audit.Payload, runtimeContract.AuditPayload)
			} else if runtimeContract.AuditPayload.Schema != command.Audit.Payload.Schema ||
				runtimeContract.AuditPayload.SchemaVersion != command.Audit.Payload.SchemaVersion ||
				string(runtimeContract.AuditPayload.Retention) != command.Audit.Payload.Retention ||
				len(runtimeContract.AuditPayload.Fields) != len(command.Audit.Payload.Fields) {
				t.Errorf("command %s runtime audit payload %#v differs from generated metadata %#v", operationID, runtimeContract.AuditPayload, command.Audit.Payload)
			} else {
				for index, field := range command.Audit.Payload.Fields {
					runtimeField := runtimeContract.AuditPayload.Fields[index]
					if runtimeField.Name != field.Name || string(runtimeField.Sensitivity) != field.Sensitivity {
						t.Errorf("command %s runtime audit field %#v differs from generated field %#v", operationID, runtimeField, field)
					}
				}
			}
			if ok {
				if (command.Target == nil) != (runtimeContract.Target == nil) {
					t.Errorf("command %s runtime target %#v differs from generated target %#v", operationID, runtimeContract.Target, command.Target)
				} else if command.Target != nil && (runtimeContract.Target.Parameter != command.Target.Parameter || runtimeContract.Target.Type != command.Target.Type) {
					t.Errorf("command %s runtime target %#v differs from generated target %#v", operationID, runtimeContract.Target, command.Target)
				}
				if len(runtimeContract.AdditionalExposures) != len(command.AdditionalExposures) {
					t.Errorf("command %s runtime exposures %#v differ from generated exposures %#v", operationID, runtimeContract.AdditionalExposures, command.AdditionalExposures)
				} else {
					for index, exposure := range command.AdditionalExposures {
						if string(runtimeContract.AdditionalExposures[index]) != string(exposure) {
							t.Errorf("command %s runtime exposure %q differs from generated exposure %q", operationID, runtimeContract.AdditionalExposures[index], exposure)
						}
					}
				}
				dependencies := runtimeContract.Dependencies()
				for dependency, required := range map[apigencommand.Dependency]bool{
					apigencommand.DependencyAuthorization: command.AuthzMode != "none",
					apigencommand.DependencyIdempotency:   command.Idempotency == "required",
					apigencommand.DependencyConcurrency:   command.Concurrency == "if-match",
					apigencommand.DependencyAudit:         true,
					apigencommand.DependencyJobQueue:      command.Execution != nil,
				} {
					if slices.Contains(dependencies, dependency) != required {
						t.Errorf("command %s dependency %q required=%v dependencies=%#v", operationID, dependency, required, dependencies)
					}
				}
				if runtimeContract.SpanName() != "command."+operationID {
					t.Errorf("command %s span name = %q", operationID, runtimeContract.SpanName())
				}
			}
			if command.AuthzMode != contract.AuthzMode {
				t.Errorf("command %s authz mode %q differs from operation mode %q", operationID, command.AuthzMode, contract.AuthzMode)
			}
			if contract.Method == http.MethodPost && command.Idempotency != "required" {
				t.Errorf("POST command %s idempotency = %q", operationID, command.Idempotency)
			}
			if contract.Method == http.MethodPatch && command.Concurrency != "if-match" {
				t.Errorf("PATCH command %s concurrency = %q", operationID, command.Concurrency)
			}
			if command.Target != nil && !strings.Contains(contract.Path, "{"+command.Target.Parameter+"}") {
				t.Errorf("command %s target %#v is absent from %s", operationID, command.Target, contract.Path)
			}
			if command.AuthzMode == "privilege" {
				capability, err := access.ParseCapability(command.Privilege)
				if err != nil {
					t.Errorf("command %s has unknown capability %q", operationID, command.Privilege)
					continue
				}
				if len(rolesByCapability[capability]) == 0 {
					t.Errorf("command %s capability %q is not granted by any project role", operationID, capability)
				}
			}
		default:
			t.Errorf("operation %s has no normalized command/query kind: %q", operationID, contract.Kind)
		}
	}
	if len(runtimeContracts) != commandCount {
		t.Errorf("runtime command registry has %d entries, want %d generated commands", len(runtimeContracts), commandCount)
	}
}

func TestAPIGenAsyncExecutionContractsAreGeneratedEndToEnd(t *testing.T) {
	contracts := apiaggregate.GetAPIGenOperationContracts()
	starters := make([]string, 0)
	controls := make([]string, 0)
	for operationID, operation := range contracts {
		accepted := slices.Contains(operation.DocumentedStatusCodes, http.StatusAccepted)
		if operation.Command == nil {
			if accepted {
				t.Errorf("non-command operation %q documents 202 Accepted", operationID)
			}
			continue
		}
		if operation.Command.Execution == nil {
			if accepted {
				controls = append(controls, operationID)
			}
			continue
		}
		starters = append(starters, operationID)
		if !accepted {
			t.Errorf("async starter %q does not document 202 Accepted", operationID)
		}
		runtimeContract, ok := apiaggregate.GetAPIGenCommandRuntimeContract(operationID)
		if !ok || runtimeContract.Execution == nil {
			t.Fatalf("async operation %q has no runtime execution contract", operationID)
		}
		generated, runtime := operation.Command.Execution, runtimeContract.Execution
		if generated.Mode != runtime.Mode || generated.Guarantee != runtime.Guarantee || generated.JobKind != runtime.JobKind || generated.ResourceKind != runtime.ResourceKind ||
			generated.InitialEvent != runtime.InitialEvent || generated.InitialState != runtime.InitialState ||
			generated.StatusOperation != runtime.StatusOperation || generated.EventsOperation != runtime.EventsOperation ||
			generated.Cancellation != runtime.Cancellation {
			t.Errorf("async operation %q runtime contract %#v differs from generated metadata %#v", operationID, runtime, generated)
		}
		if generated.Guarantee != "transactional" {
			t.Errorf("async operation %q execution guarantee = %q, want transactional", operationID, generated.Guarantee)
		}
		for role, referencedOperationID := range map[string]string{
			"status": generated.StatusOperation,
			"events": generated.EventsOperation,
		} {
			referenced, ok := contracts[referencedOperationID]
			if !ok {
				t.Errorf("async operation %q %s operation %q does not exist", operationID, role, referencedOperationID)
				continue
			}
			if referenced.Kind != apiaggregate.GenOperationKindQuery || referenced.Method != http.MethodGet {
				t.Errorf("async operation %q %s operation %q is %s %s, want GET query", operationID, role, referencedOperationID, referenced.Method, referenced.Kind)
			}
		}
	}
	sort.Strings(starters)
	sort.Strings(controls)
	wantStarters := []string{
		"activateDeployment",
		"createAgentRun",
		"createDeployment",
		"createRefreshRun",
		"finalizeManagedDataUploadSession",
		"finalizeRelease",
		"publishProjectCandidate",
		"retryDeployment",
		"rollbackDeployment",
	}
	wantControls := []string{"cancelAgentRun", "cancelDeployment", "cancelRefreshRun", "publishDeliveryCandidate", "rollbackDeliveryGeneration"}
	if !slices.Equal(starters, wantStarters) {
		t.Errorf("async starters = %v, want %v", starters, wantStarters)
	}
	if !slices.Equal(controls, wantControls) {
		t.Errorf("synchronous controls returning 202 = %v, want %v", controls, wantControls)
	}
}

func TestAPIGenUploadArtifactUsesNativeOctetStreamBody(t *testing.T) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatalf("embedded openapi: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi paths missing: %#v", spec["paths"])
	}
	operation := mustOpenAPIOperation(t, paths, "/api/v1/projects/{project}/releases/{release}/artifact", "put")
	if _, ok := operation["x-leapview-dispatch"]; ok {
		t.Fatalf("upload operation should not use x-leapview-dispatch: %#v", operation["x-leapview-dispatch"])
	}
	requestBody, _ := operation["requestBody"].(map[string]any)
	content, _ := requestBody["content"].(map[string]any)
	octetStream, ok := content["application/octet-stream"].(map[string]any)
	if !ok {
		t.Fatalf("upload operation missing application/octet-stream request body: %#v", requestBody)
	}
	schema, _ := octetStream["schema"].(map[string]any)
	if schema == nil {
		t.Fatalf("upload operation missing application/octet-stream schema: %#v", octetStream)
	}
	if got := schema["type"]; got != "string" {
		t.Fatalf("upload operation schema type = %#v, want string", got)
	}
	if got := schema["format"]; got != "binary" {
		t.Fatalf("upload operation schema format = %#v, want binary", got)
	}

	root := projectRoot(t)
	ir, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}
	var irDoc struct {
		Endpoints []struct {
			OperationID string `json:"operation_id"`
			RequestBody *struct {
				Contents []struct {
					ContentType string `json:"content_type"`
					BodyKind    string `json:"body_kind"`
				} `json:"contents"`
			} `json:"request_body"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(ir, &irDoc); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}
	for _, endpoint := range irDoc.Endpoints {
		if endpoint.OperationID != "uploadReleaseArtifact" {
			continue
		}
		if endpoint.RequestBody == nil || len(endpoint.RequestBody.Contents) != 1 {
			t.Fatalf("upload IR request body = %#v", endpoint.RequestBody)
		}
		content := endpoint.RequestBody.Contents[0]
		if content.ContentType != "application/octet-stream" || content.BodyKind != "binary" {
			t.Fatalf("upload IR content = %#v, want application/octet-stream binary", content)
		}
		var generatedBody releasegen.GenUploadReleaseArtifactBody
		_ = []byte(generatedBody)
		return
	}
	t.Fatal("uploadReleaseArtifact missing from APIGen IR")
}

func TestAPIGenListOperationsUseStandardEnvelope(t *testing.T) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatalf("embedded openapi: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok {
		t.Fatalf("openapi paths missing: %#v", spec["paths"])
	}
	components, _ := spec["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for _, tc := range []struct {
		path   string
		method string
	}{
		{"/api/v1/dashboards", "get"},
		{"/api/v1/projects/{project}/connections", "get"},
		{"/api/v1/projects/{project}/audit-events", "get"},
		{"/api/v1/projects/{project}/refresh-runs", "get"},
		{"/api/v1/projects/{project}/releases", "get"},
		{"/api/v1/projects/{project}/deployments", "get"},
		{"/api/v1/semantic-models", "get"},
		{"/api/v1/agent/conversations", "get"},
	} {
		operation := mustOpenAPIOperation(t, paths, tc.path, tc.method)
		for _, want := range []string{"limit", "pageToken"} {
			if !openAPIOperationHasQueryParam(operation, want) {
				t.Fatalf("%s %s missing query param %s", tc.method, tc.path, want)
			}
		}
		schemaName := responseSchemaName(operation, "200")
		if schemaName == "" {
			t.Fatalf("%s %s missing 200 response schema", tc.method, tc.path)
		}
		schema, _ := schemas[schemaName].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if _, ok := properties["items"]; !ok {
			t.Fatalf("%s %s schema %s missing items property: %#v", tc.method, tc.path, schemaName, properties)
		}
		if _, ok := properties["page"]; !ok {
			t.Fatalf("%s %s schema %s missing page property: %#v", tc.method, tc.path, schemaName, properties)
		}
		if _, ok := properties["dashboards"]; ok {
			t.Fatalf("%s %s schema %s has legacy dashboards property", tc.method, tc.path, schemaName)
		}
		if _, ok := properties["models"]; ok {
			t.Fatalf("%s %s schema %s has legacy models property", tc.method, tc.path, schemaName)
		}
	}
}

func mustOpenAPIOperation(t *testing.T, paths map[string]any, path, method string) map[string]any {
	t.Helper()
	pathItem, ok := paths[path].(map[string]any)
	if !ok {
		t.Fatalf("path %s missing", path)
	}
	operation, ok := pathItem[method].(map[string]any)
	if !ok {
		t.Fatalf("%s operation missing for %s", method, path)
	}
	return operation
}

func openAPIOperationHasQueryParam(operation map[string]any, name string) bool {
	parameters, _ := operation["parameters"].([]any)
	for _, raw := range parameters {
		parameter, _ := raw.(map[string]any)
		if parameter["name"] == name && parameter["in"] == "query" {
			return true
		}
	}
	return false
}

func responseSchemaName(operation map[string]any, status string) string {
	responses, _ := operation["responses"].(map[string]any)
	response, _ := responses[status].(map[string]any)
	content, _ := response["content"].(map[string]any)
	jsonContent, _ := content["application/json"].(map[string]any)
	schema, _ := jsonContent["schema"].(map[string]any)
	ref, _ := schema["$ref"].(string)
	return strings.TrimPrefix(ref, "#/components/schemas/")
}

func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatalf("could not find project root from %s", dir)
		}
		dir = next
	}
}
