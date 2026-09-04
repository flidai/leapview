package architecture

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const modulePath = "github.com/flidai/leapview"

type goFile struct {
	path    string
	pkgDir  string
	imports []string
	body    string
}

var targetCapabilities = map[string]struct{}{
	"project": {}, "access": {}, "manageddata": {}, "analytics": {},
	"dashboard": {}, "agent": {}, "release": {}, "deployment": {}, "servingstate": {},
	"refresh": {}, "runtimehost": {}, "workload": {}, "lineage": {}, "semanticvalue": {}, "platform": {},
	"recoveryset": {},
}

var approvedInternalRoots = map[string]struct{}{
	"app": {}, "platform": {},
	"access": {}, "admin": {}, "agent": {}, "analytics": {}, "dashboard": {},
	"deployment": {}, "manageddata": {}, "project": {}, "refresh": {}, "release": {},
	"runtimehost": {}, "semanticvalue": {}, "servingstate": {}, "workload": {}, "lineage": {}, "extension": {},
	"recoveryset": {},
}

func TestRepositoryIdentityUsesOrganizationNamespace(t *testing.T) {
	const canonicalModule = "github.com/flidai/leapview"
	if modulePath != canonicalModule {
		t.Errorf("modulePath = %q, want %q", modulePath, canonicalModule)
	}

	goModule, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	require.NoError(t, err)
	if !strings.HasPrefix(string(goModule), "module "+canonicalModule+"\n") {
		t.Errorf("go.mod does not declare %s", canonicalModule)
	}

	legacyRepository := "github.com/" + "Yacobolo" + "/leapview"
	legacyImages := "ghcr.io/" + "yacobolo" + "/leapview"
	legacyImageAllowlist := map[string]struct{}{
		"docs/articles/start/installation.md": {},
		"docs/public-release.json":            {},
		"scripts/public_site_smoke.test.ts":   {},
	}
	root := repoRoot(t)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".data", ".git", ".leapview", ".terraform", ".tmp", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		// Packaged applications contain directory symlinks. Repository identity
		// is enforced against authored regular files, not generated filesystem
		// topology or other special entries.
		if !entry.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(body) {
			return nil
		}
		text := string(body)
		for _, forbidden := range []string{legacyRepository, legacyImages} {
			if strings.Contains(text, forbidden) {
				relative, relErr := filepath.Rel(root, path)
				if relErr != nil {
					return relErr
				}
				relativePath := filepath.ToSlash(relative)
				if forbidden == legacyImages {
					if _, allowed := legacyImageAllowlist[relativePath]; allowed {
						continue
					}
				}
				t.Errorf("%s retains legacy repository namespace %q", relativePath, forbidden)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestInternalRootTaxonomyIsClosed(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repoRoot(t), "internal"))
	require.NoError(t, err)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := approvedInternalRoots[entry.Name()]; !ok {
			t.Errorf("internal/%s is outside the approved root taxonomy", entry.Name())
		}
	}
}

func TestArchitectureOwnershipUsesRootTaxonomy(t *testing.T) {
	for _, rule := range PackageRules {
		if rule.Capability == "api" || rule.Capability == "ui" {
			t.Errorf("%s retains synthetic %q ownership instead of its physical app, platform, or capability owner", rule.Prefix, rule.Capability)
		}
	}
}

func TestSemanticValueIsAPublishedAnalyticsDependency(t *testing.T) {
	source, sourceOK := ClassifyPackage("internal/analytics/model")
	target, targetOK := ClassifyPackage("internal/semanticvalue")
	if !sourceOK || !targetOK {
		t.Fatalf("classify analytics/model=%v semanticvalue=%v", sourceOK, targetOK)
	}
	if target.Capability != "semanticvalue" || target.Layer != LayerContract {
		t.Fatalf("semanticvalue classification = %#v, want semanticvalue contract", target)
	}
	if violation := CapabilityImportViolation("internal/analytics/model", source, "internal/semanticvalue", target); violation != "" {
		t.Fatalf("analytics/model -> semanticvalue violation = %q, want published contract", violation)
	}
}

func TestAgentGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/agent/api/gen")
	if !ok {
		t.Fatal("Agent generated API package is not classified")
	}
	if rule.Capability != "agent" || rule.Layer != LayerAdapter {
		t.Fatalf("Agent generated API classification = %#v, want agent adapter", rule)
	}
	aggregate, ok := ClassifyPackage("internal/app/api/aggregate")
	if !ok || aggregate.Capability != "composition" || aggregate.Layer != LayerAdapter {
		t.Fatalf("application API aggregate classification = %#v, want composition adapter", aggregate)
	}
}

func TestCapabilityCLIIsAnAdapterOwnedByItsCapability(t *testing.T) {
	for _, capability := range []string{"access", "admin", "agent", "dashboard", "manageddata", "project"} {
		rule, ok := ClassifyPackage("internal/" + capability + "/cli")
		if !ok {
			t.Fatalf("%s CLI package is not classified", capability)
		}
		if rule.Capability != capability || rule.Layer != LayerAdapter {
			t.Fatalf("%s CLI classification = %#v, want %s adapter", capability, rule, capability)
		}
	}
}

func TestEnterpriseAuthoringPackagesRemainCapabilityOwned(t *testing.T) {
	tests := []struct {
		path       string
		capability string
		layer      Layer
	}{
		{path: "internal/platform/securestore", capability: "platform", layer: LayerPlatform},
		{path: "internal/access/cli", capability: "access", layer: LayerAdapter},
		{path: "internal/project/devloop", capability: "project", layer: LayerUseCase},
		{path: "internal/analytics/connectionbinding", capability: "analytics", layer: LayerUseCase},
		{path: "internal/analytics/modelsql", capability: "analytics", layer: LayerContract},
		{path: "internal/analytics/infisical", capability: "analytics", layer: LayerAdapter},
		{path: "internal/analytics/environment", capability: "analytics", layer: LayerAdapter},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			rule, ok := ClassifyPackage(test.path)
			if !ok {
				t.Fatalf("%s is not classified", test.path)
			}
			if rule.Capability != test.capability || rule.Layer != test.layer {
				t.Fatalf("%s classification = %#v, want %s %s", test.path, rule, test.capability, test.layer)
			}
		})
	}
}

func TestSQLiteFixtureBoundaryIsExplicitAndNonCompositional(t *testing.T) {
	for _, path := range SQLiteFixturePackagePrefixes {
		if !IsSQLitePackage(path) {
			t.Errorf("SQLite fixture prefix %q is not a SQLite package path", path)
		}
		if !IsSQLiteFixturePackage(path) {
			t.Errorf("SQLite fixture prefix %q is not recognized as a retained fixture", path)
		}
		if IsCompositionContractImport(path) {
			t.Errorf("SQLite fixture %q is exposed as a production composition contract", path)
		}
		rule, ok := ClassifyPackage(path)
		if !ok || rule.Layer != LayerAdapter {
			t.Errorf("SQLite fixture %q classification = %#v, %v; want adapter", path, rule, ok)
		}
	}
	for _, removed := range []string{
		"internal/analytics/sqlite",
		"internal/dashboard/appearance/sqlite",
		"internal/dashboard/authoring/sqlite",
		"internal/release/sqlite",
		"internal/manageddata/maintenance/sqlite",
	} {
		if IsSQLiteFixturePackage(removed) {
			t.Errorf("removed SQLite adapter %q remains in fixture allowlist", removed)
		}
	}
	for _, path := range SQLiteFixtureFilePaths {
		if !IsSQLiteFixtureFile(path) {
			t.Errorf("SQLite fixture file %q is not recognized as a retained fixture", path)
		}
	}
}

func TestPublicJobsPackageIsPlatformOwned(t *testing.T) {
	for _, path := range []string{"pkg/jobs", "pkg/jobs/queue"} {
		rule, ok := ClassifyPackage(path)
		if !ok {
			t.Fatalf("%s is not classified", path)
		}
		if rule.Capability != "platform" || rule.Layer != LayerPlatform {
			t.Fatalf("%s classification = %#v, want platform platform-layer", path, rule)
		}
	}
}

func TestPublicArrowResultPackageIsAnalyticsOwned(t *testing.T) {
	for _, path := range []string{"pkg/arrowresult", "pkg/arrowresult/internalcopy"} {
		rule, ok := ClassifyPackage(path)
		if !ok {
			t.Fatalf("%s is not classified", path)
		}
		if rule.Capability != "analytics" || rule.Layer != LayerContract {
			t.Fatalf("%s classification = %#v, want analytics contract-layer", path, rule)
		}
	}
}

func TestResultIdentityPackageIsAnAnalyticsContract(t *testing.T) {
	const path = "internal/analytics/resultidentity"
	rule, ok := ClassifyPackage(path)
	if !ok {
		t.Fatalf("%s is not classified", path)
	}
	if rule.Capability != "analytics" || rule.Layer != LayerContract {
		t.Fatalf("%s classification = %#v, want analytics contract-layer", path, rule)
	}
	if !IsPublicContractImport("analytics", path) {
		t.Fatalf("%s is not published as an analytics contract", path)
	}
}

func TestSourceDataIdentityPackageIsAnEngineIndependentAnalyticsContract(t *testing.T) {
	const path = "internal/analytics/sourcedataidentity"
	rule, ok := ClassifyPackage(path)
	if !ok {
		t.Fatalf("%s is not classified", path)
	}
	if rule.Capability != "analytics" || rule.Layer != LayerContract {
		t.Fatalf("%s classification = %#v, want analytics contract-layer", path, rule)
	}
	if !IsPublicContractImport("analytics", path) {
		t.Fatalf("%s is not published as an analytics contract", path)
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != path {
			continue
		}
		for _, imported := range file.imports {
			for _, forbidden := range []string{
				modulePath + "/internal/analytics/connectors",
				modulePath + "/internal/analytics/materialize",
				modulePath + "/internal/analytics/model",
				modulePath + "/internal/analytics/resultcache",
				modulePath + "/internal/deployment",
				modulePath + "/internal/release",
			} {
				if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
					t.Errorf("%s imports forbidden implementation dependency %s", file.path, imported)
				}
			}
		}
	}
}

func TestEnterpriseAuthoringForbiddenImportsAreRejected(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target string
	}{
		{
			name:   "project dev loop cannot import release adapters",
			source: "internal/project/devloop",
			target: "internal/release/filesystem",
		},
		{
			name:   "project dev loop cannot import deployment adapters",
			source: "internal/project/devloop",
			target: "internal/deployment/http",
		},
		{
			name:   "dashboard cannot import deployment",
			source: "internal/dashboard/runtime",
			target: "internal/deployment",
		},
		{
			name:   "runtime host cannot import access",
			source: "internal/runtimehost",
			target: "internal/access",
		},
		{
			name:   "runtime host cannot import deployment",
			source: "internal/runtimehost",
			target: "internal/deployment",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, sourceOK := ClassifyPackage(test.source)
			target, targetOK := ClassifyPackage(test.target)
			if !sourceOK || !targetOK {
				t.Fatalf("classify source=%s (%v) target=%s (%v)", test.source, sourceOK, test.target, targetOK)
			}
			if violation := CapabilityImportViolation(test.source, source, test.target, target); !strings.Contains(violation, "undeclared capability edge") {
				t.Fatalf("%s -> %s violation = %q, want undeclared capability edge", test.source, test.target, violation)
			}
		})
	}
}

func TestAccessMayImportOnlyThePublishedProjectGraphContract(t *testing.T) {
	if !IsSharedContractImport("access", "internal/project/graph") {
		t.Fatal("access graph contract is not published")
	}
	if IsSharedContractImport("access", "internal/project/graph/compiler") || IsSharedContractImport("project", "internal/project/graph") {
		t.Fatal("shared graph contract rule widened beyond the exact access contract")
	}
	source, ok := ClassifyPackage("internal/access")
	if !ok {
		t.Fatal("access package is not classified")
	}
	graphContract, ok := ClassifyPackage("internal/project/graph")
	if !ok || graphContract.Capability != "project" || graphContract.Layer != LayerContract {
		t.Fatal("project graph package is not classified")
	}
	if violation := CapabilityImportViolation("internal/access", source, "internal/project/graph", graphContract); violation != "" {
		t.Fatalf("access -> project/graph violation = %q, want allowed shared contract", violation)
	}
	for _, targetPath := range []string{"internal/project/schema", "internal/project/compiler", "internal/project/artifact"} {
		target, targetOK := ClassifyPackage(targetPath)
		if !targetOK {
			t.Fatalf("%s is not classified", targetPath)
		}
		if violation := CapabilityImportViolation("internal/access", source, targetPath, target); !strings.Contains(violation, "undeclared capability edge") {
			t.Fatalf("access -> %s violation = %q, want undeclared capability edge", targetPath, violation)
		}
	}
}

func TestAIContextIsNotConsumedByExecutablePackages(t *testing.T) {
	// AI context is authoring metadata. The project compiler may preserve it
	// while translating authored resources, but executable consumers must not
	// inspect it when authorizing, planning, materializing, or serving queries.
	for _, file := range productionGoFiles(t) {
		if !hasPackagePrefix(file.pkgDir, []string{
			"internal/access",
			"internal/analytics/query",
			"internal/analytics/materialize",
			"internal/analytics/duckdb",
			"internal/project/artifact",
			"internal/project/runtime",
		}) {
			continue
		}
		for _, forbidden := range []string{"AIContext", "aiContext"} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s consumes executable-forbidden AI context field %q", file.path, forbidden)
			}
		}
	}
}

func TestActivationOwnsCompiledPlannerConstruction(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		"internal/dashboard/consumer/optimizer.go",
		"internal/dashboard/runtime/service.go",
		"internal/dashboard/module/runtime_metrics.go",
		"internal/dashboard/queryauthz/data_authorization.go",
		"internal/dashboard/semanticapi/handler.go",
	}
	for _, relative := range forbidden {
		body, err := os.ReadFile(filepath.Join(root, relative))
		require.NoError(t, err)
		if strings.Contains(string(body), "NewCompiledPlanner") {
			t.Errorf("%s constructs a request-local semantic planner", relative)
		}
	}

	materializePath := filepath.Join(root, "internal/analytics/materialize/runtime.go")
	file, err := parser.ParseFile(token.NewFileSet(), materializePath, nil, 0)
	require.NoError(t, err)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "queryPlanner" || function.Body == nil {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "NewCompiledPlanner" {
					t.Errorf("materialize.Runtime.queryPlanner lazily compiles a semantic planner")
				}
			}
			return true
		})
	}
}

func TestEnterpriseAuthoringStateRemainsCapabilityOwned(t *testing.T) {
	tests := []struct {
		path       string
		capability string
		layer      Layer
	}{
		{path: "internal/deployment", capability: "deployment", layer: LayerContract},
		{path: "internal/release", capability: "release", layer: LayerContract},
		{path: "internal/release/filesystem", capability: "release", layer: LayerAdapter},
		{path: "internal/analytics/connectionbinding", capability: "analytics", layer: LayerUseCase},
		{path: "internal/analytics/infisical", capability: "analytics", layer: LayerAdapter},
		{path: "internal/analytics/environment", capability: "analytics", layer: LayerAdapter},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			rule, ok := ClassifyPackage(test.path)
			if !ok {
				t.Fatalf("%s is not classified", test.path)
			}
			if rule.Capability != test.capability || rule.Layer != test.layer {
				t.Fatalf("%s classification = %#v, want %s %s", test.path, rule, test.capability, test.layer)
			}
		})
	}

	root := repoRoot(t)
	for _, forbidden := range []string{
		"internal/project/candidate",
		"internal/release/candidate",
		"internal/manageddata/connectionbinding",
	} {
		if packageDirExists(root, forbidden) {
			t.Errorf("%s claims enterprise-authoring state owned by another capability", forbidden)
		}
	}
}

func TestEnterpriseAuthoringCapabilityDirectionIsExplicit(t *testing.T) {
	required := map[string][]string{
		"project":     {"access", "analytics", "dashboard", "refresh", "servingstate"},
		"release":     {"access", "project", "servingstate"},
		"deployment":  {"access", "manageddata", "project", "release", "runtimehost", "servingstate"},
		"runtimehost": {"manageddata", "servingstate"},
	}
	for source, targets := range required {
		for _, target := range targets {
			if !CapabilityDependencies[source][target] {
				t.Errorf("enterprise authoring capability edge %s -> %s is not declared", source, target)
			}
		}
	}
	for source, forbidden := range map[string][]string{
		"access":      {"project", "release", "deployment", "runtimehost"},
		"project":     {"release", "deployment", "runtimehost"},
		"release":     {"deployment", "runtimehost"},
		"runtimehost": {"access", "project", "release", "deployment"},
	} {
		for _, target := range forbidden {
			if CapabilityDependencies[source][target] {
				t.Errorf("enterprise authoring capability graph permits reverse edge %s -> %s", source, target)
			}
		}
	}
}

func TestEnterpriseAuthoringGuideDefinesOneTargetHostedLifecycle(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "guides", "cli", "validate-deploy.md")
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(body)
	for _, required := range []string{
		"already-running LeapView target",
		"leapview login",
		"leapview dev",
		"leapview publish",
		"localhost",
		"hosted",
		"self-hosted",
		"air-gapped",
		"synthetic data",
		"operator bootstrap",
		"canonical-origin, token-free HTTPS URL",
		"system browser by default",
		"does not require LeapView Desktop",
		"HttpOnly",
		"independently revocable",
		"row-level security",
		"read-only Infisical",
		"bounded stale",
		"already-authenticated source sessions",
		"built-in vault",
		"dynamic leases",
		"Kubernetes integration",
		"Capability",
		"Dependency direction",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("canonical enterprise authoring guide missing %q", required)
		}
	}
	login := strings.Index(text, "leapview login")
	dev := strings.Index(text, "leapview dev")
	publish := strings.Index(text, "leapview publish")
	if login < 0 || dev <= login || publish <= dev {
		t.Errorf("canonical commands are not taught in login -> dev -> publish order")
	}

	guideDirectory := filepath.Join(root, "docs", "guides", "cli")
	entries, err := os.ReadDir(guideDirectory)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(guideDirectory, entry.Name()))
		require.NoError(t, err)
		for _, forbidden := range []string{
			"leapview deploy",
			"leapview preview",
			"leapview staging",
			"--auto-approve",
		} {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("docs/guides/cli/%s presents alternate authoring command %q", entry.Name(), forbidden)
			}
		}
	}
}

func TestCapabilityCLIsUseGeneratedTypedClients(t *testing.T) {
	clientImports := map[string]string{
		"internal/agent/cli":     modulePath + "/internal/agent/api/gen",
		"internal/dashboard/cli": modulePath + "/internal/dashboard/api/gen",
	}
	seen := map[string]bool{}
	for _, file := range productionGoFiles(t) {
		requiredImport, capabilityCLI := clientImports[file.pkgDir]
		if !capabilityCLI {
			continue
		}
		seen[file.pkgDir] = seen[file.pkgDir] || importListContains(file.imports, requiredImport)
		for _, forbidden := range []string{"cliapi.Request", ".DoJSON(", `OperationID: "`} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s retains untyped CLI API surface %q", file.path, forbidden)
			}
		}
	}
	for pkgDir := range clientImports {
		if !seen[pkgDir] {
			t.Errorf("%s does not import its generated typed client package", pkgDir)
		}
	}

	cliAPI, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "platform", "cliapi", "client.go"))
	require.NoError(t, err)
	for _, forbidden := range []string{"type Request struct", "DoJSON("} {
		if strings.Contains(string(cliAPI), forbidden) {
			t.Errorf("platform CLI port retains transitional surface %q", forbidden)
		}
	}
}

func TestAccessCLIUsesStandardOAuthClient(t *testing.T) {
	requiredImports := map[string]bool{
		"golang.org/x/oauth2":                   false,
		"golang.org/x/oauth2/clientcredentials": false,
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/access/cli" {
			continue
		}
		for requiredImport := range requiredImports {
			requiredImports[requiredImport] = requiredImports[requiredImport] ||
				importListContains(file.imports, requiredImport)
		}
		if importListContains(file.imports, modulePath+"/internal/access/api/gen") {
			t.Errorf("%s routes OAuth lifecycle operations through the generated REST client", file.path)
		}
	}
	for requiredImport, found := range requiredImports {
		if !found {
			t.Errorf("internal/access/cli does not import standard OAuth package %q", requiredImport)
		}
	}
}

func TestProductionCodeDoesNotImportTestcontainers(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		// postgrestest is an explicitly test-only helper package.  It owns the
		// disposable PostgreSQL container lifecycle used by conformance lanes;
		// production packages remain prohibited from importing testcontainers.
		if strings.Contains(filepath.ToSlash(file.path), "/postgrestest/") {
			continue
		}
		for _, imported := range file.imports {
			if strings.HasPrefix(imported, "github.com/testcontainers/testcontainers-go") {
				t.Errorf("%s imports test-only container framework %q", file.path, imported)
			}
		}
	}
}

func TestMinIOIntegrationOwnsItsContainerLifecycle(t *testing.T) {
	root := repoRoot(t)
	testSource, err := os.ReadFile(filepath.Join(root, "internal", "app", "integration_minio_source_test.go"))
	require.NoError(t, err)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	require.NoError(t, err)
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	require.NoError(t, err)

	testText := string(testSource)
	for _, want := range []string{
		`github.com/testcontainers/testcontainers-go/modules/minio`,
		`testcontainers.CleanupContainer(t, minioContainer)`,
		`testcontainers.WithLogger(log.TestLogger(t))`,
		`minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e`,
	} {
		if !strings.Contains(testText, want) {
			t.Errorf("MinIO integration test must own container lifecycle: missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"LEAPVIEW_TEST_MINIO_ENDPOINT",
		"Start MinIO source integration service",
		"docker run --detach --name leapview-minio",
		"minio/minio@sha256:",
	} {
		if strings.Contains(string(workflow), forbidden) {
			t.Errorf("CI workflow must not own MinIO integration lifecycle: found %q", forbidden)
		}
	}
	taskText := string(taskfile)
	for _, want := range []string{
		"test:go:external:",
		`-run '^TestMinIOParquetSourceRefreshContract$'`,
	} {
		if !strings.Contains(taskText, want) {
			t.Errorf("local Go suite must run external-service tests serially: missing %q", want)
		}
	}
	const skipMinIO = `-skip '^TestMinIOParquetSourceRefreshContract$'`
	if count := strings.Count(taskText, skipMinIO); count != 4 {
		t.Errorf("each local application shard must defer MinIO to the serial external-service task: found %d %q flags, want 4", count, skipMinIO)
	}
}

func TestCapabilityAPIPackagesOptIntoTypedClientGeneration(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(repoRoot(t), "api", "apigen.yaml"))
	require.NoError(t, err)
	manifest := string(content)
	namespaces := []string{
		"LeapViewAPI.Access", "LeapViewAPI.Agent", "LeapViewAPI.Analytics",
		"LeapViewAPI.Dashboard", "LeapViewAPI.Deployment", "LeapViewAPI.ManagedData",
		"LeapViewAPI.Project", "LeapViewAPI.Refresh", "LeapViewAPI.Release",
	}
	for _, namespace := range namespaces {
		start := strings.Index(manifest, "        "+namespace+":")
		if start < 0 {
			t.Errorf("APIGen manifest is missing %s", namespace)
			continue
		}
		rest := manifest[start+1:]
		end := strings.Index(rest, "\n        LeapViewAPI.")
		if end >= 0 {
			rest = rest[:end]
		}
		if !strings.Contains(rest, "client_file: client.apigen.gen.go") {
			t.Errorf("%s does not own a generated typed client", namespace)
		}
	}
}

func TestApplicationCLIAdminOnlyComposesAdminOperations(t *testing.T) {
	forbiddenImports := map[string]bool{
		modulePath + "/internal/access/sqlite":       true,
		modulePath + "/internal/analytics/ducklake":  true,
		modulePath + "/internal/servingstate/sqlite": true,
	}
	var adminFile *goFile
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app/cli" {
			continue
		}
		for _, imported := range file.imports {
			if forbiddenImports[imported] {
				t.Errorf("%s imports offline capability adapter %s", file.path, imported)
			}
		}
		if file.path == "internal/app/cli/admin.go" {
			current := file
			adminFile = &current
		}
	}
	if adminFile == nil {
		t.Fatal("internal/app/cli/admin.go was not found")
	}
	for _, required := range []string{
		modulePath + "/internal/admin/cli",
		modulePath + "/internal/app/adminpostgres",
	} {
		if !importListContains(adminFile.imports, required) {
			t.Errorf("application CLI Admin composition is missing import %s", required)
		}
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), adminFile.path, adminFile.body, 0)
	require.NoError(t, err)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name != "adminCommand" {
			t.Errorf("application CLI Admin composition retains compatibility function %s", function.Name.Name)
		}
	}
}

func TestOfflineAdminUseCasesAreCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/admin/offline")
	if !ok {
		t.Fatal("Admin offline package is not classified")
	}
	if rule.Capability != "admin" || rule.Layer != LayerUseCase {
		t.Fatalf("Admin offline classification = %#v, want admin use-case", rule)
	}

	forbiddenImports := map[string]bool{
		modulePath + "/internal/access/sqlite":          true,
		modulePath + "/internal/analytics/ducklake":     true,
		modulePath + "/internal/app/config":             true,
		modulePath + "/internal/platform":               true,
		modulePath + "/internal/platform/locking":       true,
		modulePath + "/internal/servingstate/retention": true,
		modulePath + "/internal/servingstate/sqlite":    true,
	}
	found := false
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/admin/offline" {
			continue
		}
		found = true
		for _, imported := range file.imports {
			if forbiddenImports[imported] {
				t.Errorf("%s imports concrete application/infrastructure dependency %s", file.path, imported)
			}
		}
	}
	if !found {
		t.Fatal("internal/admin/offline production package was not found")
	}

}

func TestAccessGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/access/api/gen")
	if !ok {
		t.Fatal("Access generated API package is not classified")
	}
	if rule.Capability != "access" || rule.Layer != LayerAdapter {
		t.Fatalf("Access generated API classification = %#v, want access adapter", rule)
	}
}

func TestAnalyticsGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/analytics/api/gen")
	if !ok {
		t.Fatal("Analytics generated API package is not classified")
	}
	if rule.Capability != "analytics" || rule.Layer != LayerAdapter {
		t.Fatalf("Analytics generated API classification = %#v, want analytics adapter", rule)
	}
}

func TestProjectGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/project/api/gen")
	if !ok {
		t.Fatal("Project generated API package is not classified")
	}
	if rule.Capability != "project" || rule.Layer != LayerAdapter {
		t.Fatalf("Project generated API classification = %#v, want project adapter", rule)
	}
}

func TestProjectTransportContractsAreCapabilityOwned(t *testing.T) {
	root := repoRoot(t)
	projectContracts, err := os.ReadFile(filepath.Join(root, "internal", "project", "api", "contracts.go"))
	if err != nil {
		t.Fatalf("read Project API contracts: %v", err)
	}
	if !strings.Contains(string(projectContracts), "type ProjectResponse struct") {
		t.Fatal("Project capability does not own its handwritten response contract")
	}
	releaseContracts, err := os.ReadFile(filepath.Join(root, "internal", "release", "api", "contracts.go"))
	if err != nil {
		t.Fatalf("read Release API contracts: %v", err)
	}
	if strings.Contains(string(releaseContracts), "type ProjectResponse struct") {
		t.Fatal("Release capability still owns the Project response contract")
	}
}

func TestRefreshGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/refresh/api/gen")
	if !ok {
		t.Fatal("Refresh generated API package is not classified")
	}
	if rule.Capability != "refresh" || rule.Layer != LayerAdapter {
		t.Fatalf("Refresh generated API classification = %#v, want refresh adapter", rule)
	}
}

func TestDeploymentGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/deployment/api/gen")
	if !ok {
		t.Fatal("Deployment generated API package is not classified")
	}
	if rule.Capability != "deployment" || rule.Layer != LayerAdapter {
		t.Fatalf("Deployment generated API classification = %#v, want deployment adapter", rule)
	}
}

func TestReleaseGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/release/api/gen")
	if !ok {
		t.Fatal("Release generated API package is not classified")
	}
	if rule.Capability != "release" || rule.Layer != LayerAdapter {
		t.Fatalf("Release generated API classification = %#v, want release adapter", rule)
	}
}

func TestManagedDataGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/manageddata/api/gen")
	if !ok {
		t.Fatal("ManagedData generated API package is not classified")
	}
	if rule.Capability != "manageddata" || rule.Layer != LayerAdapter {
		t.Fatalf("ManagedData generated API classification = %#v, want manageddata adapter", rule)
	}
}

func TestDashboardGeneratedAPIIsCapabilityOwned(t *testing.T) {
	rule, ok := ClassifyPackage("internal/dashboard/api/gen")
	if !ok {
		t.Fatal("Dashboard generated API package is not classified")
	}
	if rule.Capability != "dashboard" || rule.Layer != LayerAdapter {
		t.Fatalf("Dashboard generated API classification = %#v, want dashboard adapter", rule)
	}
}

func TestApplicationOwnsProductConfigurationContract(t *testing.T) {
	root := repoRoot(t)
	if !packageDirExists(root, "internal/app/config/spec") {
		t.Fatal("application configuration contract is missing")
	}
	if packageDirExists(root, "internal/platform/config/spec") {
		t.Fatal("platform retains the product configuration contract")
	}

}

func TestPlatformProductionCodeDoesNotOwnApplicationEnvironment(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/platform" && !strings.HasPrefix(file.pkgDir, "internal/platform/") {
			continue
		}
		// postgrestest is an importable test harness rather than runtime code.
		// Its environment gate is deliberately owned by the conformance lane so
		// CI can fail closed while ordinary developer runs may skip without a
		// container provider.
		if file.pkgDir == "internal/platform/postgres/postgrestest" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file.path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, ok := selector.X.(*ast.Ident)
				if ok && packageName.Name == "os" && (selector.Sel.Name == "Getenv" || selector.Sel.Name == "LookupEnv") {
					t.Errorf("%s reads the process environment directly; application composition must inject configuration", file.path)
				}
			case *ast.BasicLit:
				if strings.Contains(value.Value, "LEAPVIEW_") {
					t.Errorf("%s contains application-specific configuration %s", file.path, value.Value)
				}
			}
			return true
		})
	}
}

func TestPlatformProductionCodeDoesNotImportProductCapabilities(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/platform" && !strings.HasPrefix(file.pkgDir, "internal/platform/") {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			targetPath := strings.TrimPrefix(imported, modulePath+"/")
			targetRoot := strings.Split(strings.TrimPrefix(targetPath, "internal/"), "/")[0]
			if targetRoot != "platform" {
				t.Errorf("%s imports product/app package %s", file.path, targetPath)
			}
		}
	}
}

func TestPlatformObservabilityContainsOnlyGenericMechanisms(t *testing.T) {
	root := repoRoot(t)
	if !packageDirExists(root, "internal/dashboard/observability") {
		t.Error("dashboard telemetry adapter is not owned by dashboard")
	}
	if !packageDirExists(root, "internal/workload/observability") {
		t.Error("workload telemetry adapter is not owned by workload")
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/platform/observability" {
			continue
		}
		for _, productTerm := range []string{"Dashboard", "Workload", "Analytics", "ServingState"} {
			if strings.Contains(file.body, productTerm) {
				t.Errorf("%s contains product-owned observability term %q", file.path, productTerm)
			}
		}
	}
}

func TestCapabilitiesDoNotImportApplicationComposition(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !strings.HasPrefix(file.pkgDir, "internal/") ||
			file.pkgDir == "internal/app" || strings.HasPrefix(file.pkgDir, "internal/app/") ||
			file.pkgDir == "internal/platform" || strings.HasPrefix(file.pkgDir, "internal/platform/") {
			continue
		}
		for _, imported := range file.imports {
			if imported == modulePath+"/internal/app" || strings.HasPrefix(imported, modulePath+"/internal/app/") {
				t.Errorf("%s imports application composition package %s", file.path, imported)
			}
		}
	}
}

func TestDeferredCapabilityEdgesRemainEmpty(t *testing.T) {
	if len(DeferredPackageEdges) != 0 {
		t.Fatalf("deferred capability edges = %v, want none", DeferredPackageEdges)
	}
}

func TestTargetCapabilityGraphDeclaresWorkload(t *testing.T) {
	if _, ok := targetCapabilities["workload"]; !ok {
		t.Fatal("workload is absent from the target capability graph")
	}
	if !packageDirExists(repoRoot(t), "internal/workload") {
		t.Fatal("declared workload capability package does not exist")
	}
}

func TestRefreshOwnsDurableRunState(t *testing.T) {
	if !packageDirExists(repoRoot(t), "internal/refresh/run") {
		t.Fatal("refresh run contract package does not exist")
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/analytics/materialize" {
			continue
		}
		for _, declaration := range []string{"type RunRecord struct", "type RunInput struct", "RunStatusQueued"} {
			if strings.Contains(file.body, declaration) {
				t.Errorf("%s retains refresh lifecycle declaration %q", file.path, declaration)
			}
		}
	}
}

func TestCapabilityModuleSurfacesExist(t *testing.T) {
	root := repoRoot(t)
	for _, capability := range []string{"access", "analytics", "manageddata", "release", "deployment", "refresh", "dashboard", "agent", "runtimehost", "servingstate", "workload", "admin"} {
		dir := "internal/" + capability + "/module"
		if !packageDirExists(root, dir) {
			t.Errorf("capability composition package %s does not exist", dir)
		}
	}
}

func TestCapabilityModulesUseBuildAsTheirConstructor(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		rule, ok := ClassifyPackage(file.pkgDir)
		if !ok || rule.Layer != LayerModule {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file.path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "New" {
				t.Errorf("%s exports New; capability modules expose Build(ctx, Config)", file.path)
			}
		}
	}
}

func TestCapabilityModulesDoNotExposeRepositories(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		rule, ok := ClassifyPackage(file.pkgDir)
		if !ok || rule.Layer != LayerModule {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file.path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil {
				continue
			}
			if function.Name.Name == "Repository" {
				t.Errorf("%s exposes a repository from a capability module; export a named read or write port", file.path)
			}
		}
	}
}

func TestApplicationAPIGenRoutesUseGeneratedAggregate(t *testing.T) {
	foundAggregateRegistration := false
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		for _, forbidden := range []string{
			"apigenOperationPrivilege",
			"apigenOperationObjectResolver",
			"apiGenObjectScopes",
			"isGlobalAgentOperation",
		} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s retains APIGen authorization behavior %q; access owns authorization", file.path, forbidden)
			}
		}
		if strings.Contains(file.body, "apiaggregate.RegisterAPIGenRoutes(r, platform.apiGenServers)") {
			foundAggregateRegistration = true
		}
		if strings.Contains(file.body, "type apiGenRouteHandler") {
			t.Errorf("%s retains the handwritten global APIGen route wrapper", file.path)
		}
	}
	if !foundAggregateRegistration {
		t.Fatal("internal/app does not register the generated APIGen aggregate")
	}
}

func TestApplicationRetainsOnlyProcessFacingSurfaces(t *testing.T) {
	root := repoRoot(t)
	found := false
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file.path)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file.path, err)
		}
		for _, declaration := range parsed.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok || generic.Tok != token.TYPE {
				continue
			}
			for _, spec := range generic.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if typeSpec.Name.Name != "Application" {
					continue
				}
				found = true
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("%s Application must be a struct", file.path)
				}
				fields := map[string]string{}
				for _, field := range structure.Fields.List {
					if len(field.Names) == 0 {
						fields["<embedded:"+expressionName(field.Type)+">"] = expressionName(field.Type)
					}
					for _, name := range field.Names {
						fields[name.Name] = expressionName(field.Type)
					}
				}
				want := map[string]string{"handler": "http.Handler", "lifecycle": "*applicationLifecycleOwner"}
				if !mapsEqual(fields, want) {
					t.Errorf("%s Application fields = %#v, want only handler and private lifecycle owner", file.path, fields)
				}
			}
		}
	}
	if !found {
		t.Fatal("internal/app does not declare Application")
	}
}

func TestApplicationPublicSurfaceIsClosed(t *testing.T) {
	want := map[string]bool{"Handler": true, "Start": true, "Shutdown": true, "Fatal": true}
	got := map[string]bool{}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !ast.IsExported(function.Name.Name) {
				continue
			}
			for _, receiver := range function.Recv.List {
				receiverName := strings.TrimPrefix(expressionName(receiver.Type), "*")
				if receiverName == "Application" {
					got[function.Name.Name] = true
				}
			}
		}
	}
	if !boolMapsEqual(got, want) {
		t.Fatalf("Application exported methods = %#v, want handler, start, shutdown, and fatal only", got)
	}
}

func TestCapabilityBrowserRoutesAreModuleOwned(t *testing.T) {
	root := repoRoot(t)
	routerPath := filepath.Join(root, "internal", "app", "router.go")
	body, err := os.ReadFile(routerPath)
	require.NoError(t, err)
	parsed, err := parser.ParseFile(token.NewFileSet(), routerPath, body, 0)
	require.NoError(t, err)
	routeMethods := map[string]bool{
		"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true,
		"Method": true, "Handle": true,
	}
	constants := stringConstants(parsed)
	capabilityPrefixes := []string{
		"/admin", "/auth/", "/chats", "/connections", "/sources", "/embed/dashboards",
		"/login", "/mcp", "/oauth/", "/public/dashboards", "/scim", "/upload-protocols/tus",
		"/.well-known/oauth",
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !routeMethods[selector.Sel.Name] {
			return true
		}
		path, ok := constantString(call.Args[0], constants)
		if !ok {
			return true
		}
		for _, prefix := range capabilityPrefixes {
			owned := path == prefix || strings.HasPrefix(path, prefix+"/")
			if strings.HasSuffix(prefix, "/") {
				owned = strings.HasPrefix(path, prefix)
			}
			if owned {
				t.Errorf("internal/app/router.go directly registers capability route %q; its module must own the route", path)
			}
		}
		return true
	})
	for module, mounts := range map[string][]string{
		"access":      {"MountLoginPage", "MountAuthenticatedBrowser", "MountLocalLogin", "MountOAuthEndpoints", "MountOAuthMetadata", "MountSCIM"},
		"admin":       {"MountAuthenticated"},
		"agent":       {"MountAuthenticated", "MountMCP"},
		"dashboard":   {"MountPublicDocuments", "MountPublicCommands", "MountPublicStream", "MountAuthenticated"},
		"manageddata": {"MountTus"},
	} {
		moduleBody, readErr := os.ReadFile(filepath.Join(root, "internal", module, "module", "routes.go"))
		require.NoError(t, readErr)
		for _, mount := range mounts {
			if !strings.Contains(string(moduleBody), "func (m *Module) "+mount+"(") {
				t.Errorf("internal/%s/module does not own expected route mount %s", module, mount)
			}
		}
	}
}

func TestCompositionDependencyBagsStayAtCompositionBoundaries(t *testing.T) {
	bagTypes := map[string]bool{
		"*capabilityRoutes": true, "*runtimeServices": true, "*platformServices": true, "*httpPolicy": true,
	}
	allowed := map[string]bool{
		"newCompositionSurfaces":   true,
		"buildApplicationSurfaces": true,
		"configureModules":         true,
		"configureAPIProtocol":     true,
		"configurePageStream":      true,
		"configureRefreshModule":   true,
		"Routes":                   true,
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Type.Params == nil || allowed[function.Name.Name] {
				continue
			}
			for _, parameter := range function.Type.Params.List {
				if bagTypes[expressionName(parameter.Type)] {
					t.Errorf("%s %s accepts composition bag %s; pass its narrow dependency instead", file.path, function.Name.Name, expressionName(parameter.Type))
				}
			}
		}
	}
}

func stringConstants(file *ast.File) map[string]string {
	values := map[string]string{}
	for _, declaration := range file.Decls {
		generic, ok := declaration.(*ast.GenDecl)
		if !ok || generic.Tok != token.CONST {
			continue
		}
		for _, spec := range generic.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if index >= len(value.Values) {
					continue
				}
				if resolved, ok := constantString(value.Values[index], values); ok {
					values[name.Name] = resolved
				}
			}
		}
	}
	return values
}

func constantString(expression ast.Expr, constants map[string]string) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		resolved, err := strconv.Unquote(value.Value)
		return resolved, err == nil
	case *ast.Ident:
		resolved, ok := constants[value.Name]
		return resolved, ok
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := constantString(value.X, constants)
		right, rightOK := constantString(value.Y, constants)
		return left + right, leftOK && rightOK
	default:
		return "", false
	}
}

func mapsEqual(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func boolMapsEqual(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}

func expressionName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return expressionName(value.X) + "." + value.Sel.Name
	case *ast.StarExpr:
		return "*" + expressionName(value.X)
	default:
		return ""
	}
}

func TestGeneratedQueryPackagesDoNotCombineCapabilitySQL(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "sqlc.yaml"))
	require.NoError(t, err)
	blocks := strings.Split(string(body), "\n  - engine:")
	for _, forbidden := range []struct {
		generatedPackage string
		queryPath        string
	}{
		{generatedPackage: `out: "internal/servingstate/internal/db"`, queryPath: `"internal/access/sqlite/queries`},
	} {
		for _, block := range blocks {
			if strings.Contains(block, forbidden.generatedPackage) && strings.Contains(block, forbidden.queryPath) {
				t.Errorf("sqlc package %s includes cross-capability query input %s", forbidden.generatedPackage, forbidden.queryPath)
			}
		}
	}
}

func TestCapabilitySQLCOutputsArePrivate(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "sqlc.yaml"))
	require.NoError(t, err)
	config := string(body)
	for _, output := range []string{
		"internal/access/internal/db",
		"internal/agent/internal/db",
		"internal/dashboard/internal/db",
		"internal/manageddata/internal/db",
		"internal/refresh/internal/db",
		"internal/servingstate/internal/db",
		"internal/project/internal/db",
	} {
		fragment := "package: \"db\"\n        out: \"" + output + "\""
		if !strings.Contains(config, fragment) {
			t.Errorf("sqlc output %s is not a capability-private db package", output)
		}
	}
	for _, legacy := range []string{
		"internal/access/sqlite/accessdb",
		"internal/agent/sqlite/agentdb",
		"internal/manageddata/sqlite/manageddb",
		"internal/refresh/sqlite/materializedb",
		"internal/refresh/sqlite/refreshdb",
		"internal/servingstate/sqlite/servingdb",
	} {
		if strings.Contains(config, legacy) {
			t.Errorf("sqlc retains public capability output %s", legacy)
		}
	}
}

func TestCapabilitiesOnlyImportOwnGeneratedQueries(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		source, sourceOK := ClassifyPackage(file.pkgDir)
		for _, imported := range file.imports {
			targetOwner, generated := capabilityGeneratedDBOwner(imported)
			if !generated {
				continue
			}
			if !sourceOK || source.Capability != targetOwner || source.Layer != LayerAdapter {
				t.Errorf("%s imports generated database package owned by %s outside its owning persistence adapters", file.path, targetOwner)
			}
		}
	}
}

func TestCrossCapabilityGeneratedQueryImportIsRejected(t *testing.T) {
	if owner, ok := capabilityGeneratedDBOwner(modulePath + "/internal/access/internal/db"); !ok || owner != "access" {
		t.Fatalf("generated Access database package owner = %q, %v", owner, ok)
	}
	source, ok := ClassifyPackage("internal/dashboard/publication/sqlite")
	if !ok {
		t.Fatal("Dashboard publication adapter is not classified")
	}
	targetOwner, generated := capabilityGeneratedDBOwner(modulePath + "/internal/access/internal/db")
	if !generated || source.Capability == targetOwner {
		t.Fatal("representative Dashboard-to-Access generated database import was not rejected")
	}
}

func capabilityGeneratedDBOwner(imported string) (string, bool) {
	relative := strings.TrimPrefix(imported, modulePath+"/")
	parts := strings.Split(relative, "/")
	if len(parts) != 4 || parts[0] != "internal" || parts[2] != "internal" || parts[3] != "db" {
		return "", false
	}
	if _, known := CapabilityDependencies[parts[1]]; !known || parts[1] == "platform" {
		return "", false
	}
	return parts[1], true
}

func TestCompositionDoesNotUseTestTransports(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		for _, imported := range file.imports {
			if imported == "net/http/httptest" {
				t.Errorf("%s uses httptest in process composition; response capture belongs to the consuming transport adapter", file.path)
			}
		}
	}
}

func TestRefreshPersistenceIsConstructedOnlyByItsModule(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if imported != modulePath+"/internal/refresh/sqlite" {
				continue
			}
			t.Errorf("%s imports the local refresh SQLite fixture; production must use the PostgreSQL persistence surface", file.path)
		}
	}
}

func TestPlatformJobModuleSurfaceExists(t *testing.T) {
	if !packageDirExists(repoRoot(t), "internal/platform/jobs/module") {
		t.Fatal("platform durable job module does not exist")
	}
}

func TestCapabilityModulesDoNotImportOtherModules(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		source, ok := ClassifyPackage(file.pkgDir)
		if !ok || source.Layer != LayerModule {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") || !strings.HasSuffix(imported, "/module") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			target, ok := ClassifyPackage(packagePath)
			if ok && target.Capability != source.Capability {
				t.Errorf("%s imports capability module %s; only internal/app may assemble modules", file.path, packagePath)
			}
		}
	}
}

func TestCapabilityModulesDoNotImportOtherCapabilityAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		source, ok := ClassifyPackage(file.pkgDir)
		if !ok || source.Layer != LayerModule {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			target, ok := ClassifyPackage(packagePath)
			if !ok || target.Layer != LayerAdapter || target.Capability == source.Capability {
				continue
			}
			if target.Capability == "platform" || target.Capability == "api" || target.Capability == "ui" {
				continue
			}
			t.Errorf("%s imports another capability's adapter %s; accept a consumer-owned port", file.path, packagePath)
		}
	}
}

func TestCapabilityModulesDoNotImportOtherCapabilityPersistenceAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		source, ok := ClassifyPackage(file.pkgDir)
		if !ok || source.Layer != LayerModule {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			target, ok := ClassifyPackage(packagePath)
			if !ok || target.Layer != LayerAdapter || target.Capability == source.Capability || !strings.Contains(packagePath, "/sqlite") {
				continue
			}
			if target.Capability == "platform" || target.Capability == "api" || target.Capability == "ui" {
				continue
			}
			t.Errorf("%s imports another capability's adapter %s; receive a contract through Config instead", file.path, packagePath)
		}
	}
}

func TestCapabilityModulesDoNotImportOtherCapabilityTransportAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		source, ok := ClassifyPackage(file.pkgDir)
		if !ok || source.Layer != LayerModule {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			target, ok := ClassifyPackage(packagePath)
			if !ok || target.Layer != LayerAdapter || target.Capability == source.Capability {
				continue
			}
			if target.Capability == "platform" || target.Capability == "api" || target.Capability == "ui" {
				continue
			}
			if strings.Contains(packagePath, "/http") || strings.Contains(packagePath, "/datastar") {
				t.Errorf("%s imports another capability's transport adapter %s; accept a consumer-owned port", file.path, packagePath)
			}
		}
	}
}

func TestCompositionOwnershipIsAnExplicitClosedSet(t *testing.T) {
	allowed := []string{
		"cmd",
		"internal/app",
		"internal/app/cli",
		"internal/app/tools",
	}
	for _, file := range productionGoFiles(t) {
		rule, ok := ClassifyPackage(file.pkgDir)
		if !ok || rule.Layer != LayerComposition {
			continue
		}
		permitted := false
		for _, prefix := range allowed {
			if file.pkgDir == prefix || strings.HasPrefix(file.pkgDir, prefix+"/") {
				permitted = true
				break
			}
		}
		if !permitted {
			t.Errorf("%s claims undeclared composition ownership", file.path)
		}
	}
}

func TestEveryProductionPackageHasAnArchitecturalOwner(t *testing.T) {
	seen := map[string]bool{}
	for _, file := range productionGoFiles(t) {
		if seen[file.pkgDir] {
			continue
		}
		seen[file.pkgDir] = true
		if _, ok := ClassifyPackage(file.pkgDir); !ok {
			t.Errorf("%s has no declared capability owner and layer", file.pkgDir)
		}
	}
}

func TestDeclaredCapabilityGraphIsAcyclic(t *testing.T) {
	const (
		unvisited = iota
		visiting
		visited
	)
	states := map[string]int{}
	stack := []string{}
	var visit func(string)
	visit = func(source string) {
		switch states[source] {
		case visited:
			return
		case visiting:
			cycle := append(append([]string(nil), stack...), source)
			t.Fatalf("capability dependency cycle: %s", strings.Join(cycle, " -> "))
		}
		states[source] = visiting
		stack = append(stack, source)
		for target := range CapabilityDependencies[source] {
			visit(target)
		}
		stack = stack[:len(stack)-1]
		states[source] = visited
	}
	for capability := range CapabilityDependencies {
		visit(capability)
	}
}

func TestProductionImportsFollowCapabilityGraph(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if strings.Contains(file.pkgDir, "/testing/") {
			continue
		}
		source, ok := ClassifyPackage(file.pkgDir)
		if !ok {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			if IsSQLitePackage(packagePath) {
				if sameSQLiteFixture(packagePath, file.pkgDir) {
					continue
				}
				t.Errorf("%s imports SQLite package %s; SQLite adapters are test fixtures only", file.path, packagePath)
				continue
			}
			target, ok := ClassifyPackage(packagePath)
			if !ok || source.Capability == target.Capability {
				continue
			}
			_, sourceIsProductCapability := targetCapabilities[source.Capability]
			if !sourceIsProductCapability || source.Layer == LayerComposition {
				continue
			}
			if violation := CapabilityImportViolation(file.pkgDir, source, packagePath, target); violation != "" {
				t.Errorf("%s imports %s: %s", file.path, packagePath, violation)
			}
		}
	}
}

// sameSQLiteFixture permits a retained fixture adapter to import its own
// generated support package while rejecting every cross-fixture or
// production-to-fixture dependency.
func sameSQLiteFixture(sourcePath, targetPath string) bool {
	if !IsSQLiteFixturePackage(sourcePath) || !IsSQLiteFixturePackage(targetPath) {
		return false
	}
	for _, prefix := range SQLiteFixturePackagePrefixes {
		if hasPackagePrefix(sourcePath, []string{prefix}) && hasPackagePrefix(targetPath, []string{prefix}) {
			return true
		}
	}
	return false
}

func TestProductionSourcesDoNotImportSQLiteAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if imported == "modernc.org/sqlite" {
				if !IsSQLiteFixtureFile(file.path) {
					t.Errorf("%s imports SQLite driver outside the platform Store fixture", file.path)
				}
				continue
			}
			if !strings.HasPrefix(imported, modulePath+"/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			if !IsSQLitePackage(packagePath) || sameSQLiteFixture(file.pkgDir, packagePath) {
				continue
			}
			t.Errorf("%s imports SQLite package %s; SQLite adapters are test fixtures only", file.path, packagePath)
		}
	}
}

func TestProductionDoesNotImportSupersededDuckDBQueryJSON(t *testing.T) {
	const superseded = modulePath + "/internal/analytics/duckdb/queryjson"
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if imported == superseded {
				t.Errorf("%s imports superseded DuckDB SQL analyzer %s", file.path, imported)
			}
		}
	}
}

func TestCapabilityModulesRequireDeclaredPublicContractEdges(t *testing.T) {
	runtimehostModule, ok := ClassifyPackage("internal/runtimehost/module")
	if !ok || runtimehostModule.Layer != LayerModule {
		t.Fatal("runtimehost module classification is unavailable")
	}
	projectBundle, ok := ClassifyPackage("internal/project/bundle")
	if !ok {
		t.Fatal("project bundle classification is unavailable")
	}
	if violation := CapabilityImportViolation("internal/runtimehost/module", runtimehostModule, "internal/project/bundle", projectBundle); !strings.Contains(violation, "undeclared capability edge") {
		t.Fatalf("runtimehost module -> project bundle violation = %q", violation)
	}

	agentModule, ok := ClassifyPackage("internal/agent/module")
	if !ok || agentModule.Layer != LayerModule {
		t.Fatal("agent module classification is unavailable")
	}
	dashboardRuntime, ok := ClassifyPackage("internal/dashboard/runtime")
	if !ok {
		t.Fatal("dashboard runtime classification is unavailable")
	}
	if violation := CapabilityImportViolation("internal/agent/module", agentModule, "internal/dashboard/runtime", dashboardRuntime); !strings.Contains(violation, "non-contract package") {
		t.Fatalf("agent module -> dashboard runtime violation = %q", violation)
	}

	dashboardReport, ok := ClassifyPackage("internal/dashboard/report")
	if !ok {
		t.Fatal("dashboard report classification is unavailable")
	}
	if violation := CapabilityImportViolation("internal/agent/module", agentModule, "internal/dashboard/report", dashboardReport); violation != "" {
		t.Fatalf("agent module -> dashboard report should be allowed, got %q", violation)
	}

	dashboardResolver, ok := ClassifyPackage("internal/dashboard/resolver")
	if !ok {
		t.Fatal("dashboard resolver classification is unavailable")
	}
	if violation := CapabilityImportViolation("internal/agent/module", agentModule, "internal/dashboard/resolver", dashboardResolver); violation != "" {
		t.Fatalf("agent module -> dashboard resolver should be allowed, got %q", violation)
	}
}

func TestApplicationImportsProductCapabilitiesOnlyThroughModules(t *testing.T) {
	// Project is intentionally compile-time-first and has no synthetic runtime
	// module. Its generated HTTP adapter and the closed set of process-level
	// contract ports are valid composition edges; arbitrary capability packages
	// remain rejected below.
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			target, ok := ClassifyPackage(packagePath)
			if !ok || target.Capability == "platform" || target.Capability == "composition" {
				continue
			}
			if target.Layer != LayerModule && packagePath != "internal/project/http" && !IsCompositionContractImport(packagePath) {
				t.Errorf("%s imports product package %s instead of its module surface", file.path, packagePath)
			}
		}
	}
}

func TestDashboardCatalogHasOnlyExplicitProjectionConsumers(t *testing.T) {
	allowed := map[string]bool{
		"internal/project/artifact/artifact.go": true,
	}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "internal/dashboard" || strings.HasPrefix(file.pkgDir, "internal/dashboard/") {
			continue
		}
		for _, imported := range file.imports {
			if imported != modulePath+"/internal/dashboard/catalog" {
				continue
			}
			path := strings.TrimPrefix(file.path, repoRoot(t)+"/")
			if !allowed[path] {
				t.Errorf("%s imports dashboard catalog instead of an owner-specific projection", file.path)
			}
		}
	}
}

func TestApplicationOwnsGlobalShellComposition(t *testing.T) {
	root := repoRoot(t)
	if !packageDirExists(root, "internal/app/shell") {
		t.Fatal("application shell composition package is missing")
	}
	for _, file := range productionGoFiles(t) {
		if !strings.HasSuffix(file.pkgDir, "/ui") {
			continue
		}
		for _, productNavigation := range []string{
			`ID: "dashboards", Label: "Dashboards"`,
			`ID: "chat", Label: "Chats"`,
			`ID: "admin", Label: "Admin"`,
		} {
			if strings.Contains(file.body, productNavigation) {
				t.Errorf("%s assembles global application navigation %q", file.path, productNavigation)
			}
		}
	}
}

func TestCapabilityRenderersUsePlatformPageMechanism(t *testing.T) {
	const platformPage = modulePath + "/internal/platform/web/page"
	for _, capability := range []string{"access", "admin", "agent", "dashboard"} {
		found := false
		for _, file := range productionGoFiles(t) {
			if file.pkgDir != "internal/"+capability+"/ui" {
				continue
			}
			for _, imported := range file.imports {
				if imported == platformPage {
					found = true
				}
			}
			for _, duplicatedHelper := range []string{"func inspectorScript(", "func inspectorElement(", "func pageHead("} {
				if strings.Contains(file.body, duplicatedHelper) {
					t.Errorf("%s retains duplicated document helper %q", file.path, duplicatedHelper)
				}
			}
		}
		if !found {
			t.Errorf("internal/%s/ui does not consume the platform page mechanism", capability)
		}
	}
}

func TestApplicationDoesNotReclaimAccessOrAnalyticsConstruction(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		for _, forbidden := range []string{"analyticsducklake.Open(", "accesssqlite.NewRepository("} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s constructs a migrated capability adapter via %s", file.path, forbidden)
			}
		}
		if strings.HasSuffix(file.path, "/auth.go") {
			t.Errorf("%s owns authentication behavior; move it to access/module", file.path)
		}
	}
}

func TestAppDoesNotRetainPlatformStore(t *testing.T) {
	root := repoRoot(t)
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file.path)), nil, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Store" {
				return true
			}
			if ident, ok := selector.X.(*ast.Ident); ok && ident.Name == "platform" {
				t.Errorf("%s retains platform.Store; keep the store local to application assembly", file.path)
			}
			return true
		})
	}
}

func TestOnlyCompositionImportsApplicationPackage(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if imported != modulePath+"/internal/app" {
				continue
			}
			rule, ok := ClassifyPackage(file.pkgDir)
			if !ok || rule.Layer != LayerComposition {
				t.Errorf("%s imports internal/app outside process composition", file.path)
			}
		}
	}
}

func TestLegacyApplicationContainerAPIIsAbsent(t *testing.T) {
	root := repoRoot(t)
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file.path)), nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			switch value := declaration.(type) {
			case *ast.GenDecl:
				for _, specification := range value.Specs {
					if named, ok := specification.(*ast.TypeSpec); ok {
						switch named.Name.Name {
						case "Server", "server", "Options", "serverOptions", "Host", "host":
							t.Errorf("%s declares legacy application container type %s", file.path, named.Name.Name)
						}
					}
				}
			case *ast.FuncDecl:
				if value.Recv == nil {
					switch value.Name.Name {
					case "New", "NewWithOptions", "newServer", "newServerWithOptions", "buildServer":
						t.Errorf("%s declares legacy application constructor %s", file.path, value.Name.Name)
					}
				}
			}
		}
	}
}

func TestRequestRuntimeDoesNotRetainConstructionDependencies(t *testing.T) {
	root := repoRoot(t)
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/app" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file.path)), nil, 0)
		require.NoError(t, err)
		for _, declaration := range parsed.Decls {
			generic, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, specification := range generic.Specs {
				named, ok := specification.(*ast.TypeSpec)
				if !ok || named.Name.Name != "runtimeRouter" {
					continue
				}
				structure, ok := named.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("%s runtimeRouter must be a struct", file.path)
				}
				for _, field := range structure.Fields.List {
					for _, name := range field.Names {
						switch name.Name {
						case "adminDatabase", "servingStateRepo", "managedDataResolver",
							"accessRepo", "reloader", "duckLakeCatalogPath", "duckLakeDataPath",
							"jobLeaseTimeout", "deploymentConfig":
							t.Errorf("%s runtimeRouter retains construction dependency %s", file.path, name.Name)
						}
					}
				}
			}
		}
	}
}

func TestAppDoesNotConstructRepositoriesFromSQLDB(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "internal/app" && file.path != "internal/app/composition.go" && strings.Contains(file.body, ".SQLDB()") {
			t.Errorf("%s constructs adapters from platform.Store; capability modules must receive construction ownership", file.path)
		}
	}
}

func TestWorkloadImportsNoProductCapabilities(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "internal/workload" {
			continue
		}
		for _, imported := range file.imports {
			if strings.HasPrefix(imported, modulePath+"/internal/") {
				t.Fatalf("%s imports product capability %s", file.path, imported)
			}
		}
	}
}

func TestReusableWorkloadPackageContainsOnlyGenericMechanisms(t *testing.T) {
	root := repoRoot(t)
	if !packageDirExists(root, "pkg/workload") {
		t.Fatal("reusable workload contract package does not exist")
	}
	packageRoot := filepath.Join(root, "pkg", "workload")
	err := filepath.WalkDir(packageRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && entry.Name() == "go.mod" {
			t.Errorf("%s creates a nested module; pkg/workload must remain in the root Go module", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect pkg/workload module boundary: %v", err)
	}
	rule, ok := ClassifyPackage("pkg/workload")
	if !ok || rule.Capability != "workload" || rule.Layer != LayerContract {
		t.Fatalf("pkg/workload classification = %#v, want workload contract", rule)
	}

	for _, file := range productionGoFiles(t) {
		if file.pkgDir != "pkg/workload" && !strings.HasPrefix(file.pkgDir, "pkg/workload/") {
			continue
		}
		for _, imported := range file.imports {
			if strings.HasPrefix(imported, modulePath+"/internal/") {
				t.Errorf("%s imports application-private package %s", file.path, imported)
			}
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(file.path)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file.path, err)
		}
		forbiddenDeclarations := map[string]struct{}{
			"Interactive":   {},
			"Background":    {},
			"Refresh":       {},
			"Control":       {},
			"Maintenance":   {},
			"DefaultConfig": {},
		}
		forbiddenClassValues := map[string]struct{}{
			"interactive": {},
			"background":  {},
			"refresh":     {},
			"control":     {},
			"maintenance": {},
		}
		for _, declaration := range parsed.Decls {
			switch value := declaration.(type) {
			case *ast.FuncDecl:
				if _, forbidden := forbiddenDeclarations[value.Name.Name]; forbidden {
					t.Errorf("%s declares LeapView workload policy identifier %s", file.path, value.Name.Name)
				}
			case *ast.GenDecl:
				for _, specification := range value.Specs {
					switch item := specification.(type) {
					case *ast.TypeSpec:
						if _, forbidden := forbiddenDeclarations[item.Name.Name]; forbidden {
							t.Errorf("%s declares LeapView workload policy identifier %s", file.path, item.Name.Name)
						}
					case *ast.ValueSpec:
						for _, name := range item.Names {
							if _, forbidden := forbiddenDeclarations[name.Name]; forbidden {
								t.Errorf("%s declares LeapView workload policy identifier %s", file.path, name.Name)
							}
						}
					}
				}
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil {
				if _, forbidden := forbiddenClassValues[value]; forbidden {
					t.Errorf("%s embeds LeapView workload class value %q", file.path, value)
				}
			}
			return true
		})
	}
}

func TestReusableWorkloadPackageHasSingleProductionAdapter(t *testing.T) {
	const reusableWorkload = modulePath + "/pkg/workload"
	adapterFound := false
	forbiddenSchedulerSymbols := []string{"type classQueue", "type waiter struct", "scheduleLocked(", "nextClassLocked(", "canGrantLocked("}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "pkg/workload" || strings.HasPrefix(file.pkgDir, "pkg/workload/") {
			continue
		}
		if file.pkgDir == "internal/workload" {
			for _, symbol := range forbiddenSchedulerSymbols {
				if strings.Contains(file.body, symbol) {
					t.Errorf("%s retains superseded scheduler mechanism %q", file.path, symbol)
				}
			}
			for _, imported := range file.imports {
				if imported == reusableWorkload || strings.HasPrefix(imported, reusableWorkload+"/") {
					adapterFound = true
				}
			}
			continue
		}
		for _, imported := range file.imports {
			if imported == reusableWorkload || strings.HasPrefix(imported, reusableWorkload+"/") {
				t.Errorf("%s consumes pkg/workload outside the sole internal/workload adapter", file.path)
			}
		}
	}
	if !adapterFound {
		t.Fatal("internal/workload does not adapt the generic pkg/workload scheduler")
	}
}

func TestOnlyWorkloadAdaptersAndCompositionDependOnWorkload(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if imported != modulePath+"/internal/workload" {
				continue
			}
			if !AllowsWorkloadImport(file.pkgDir) {
				t.Fatalf("%s depends on workload outside composition or an execution/worker adapter", file.path)
			}
		}
	}
}

func TestArrowImportsStayInsideAnalyticalDataPlaneAndExplicitEncoders(t *testing.T) {
	allowed := []string{
		"internal/analytics/arrowquery",
		"internal/analytics/arrowdecode",
		"internal/analytics/resultcache",
		"internal/analytics/materialize",
		"internal/analytics/ducklake",
		"internal/dashboard/semanticapi",
		"internal/dashboard/http",
		"pkg/arrowresult",
	}
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, "github.com/apache/arrow-go/") {
				continue
			}
			permitted := false
			for _, prefix := range allowed {
				if file.pkgDir == prefix || strings.HasPrefix(file.pkgDir, prefix+"/") {
					permitted = true
					break
				}
			}
			if !permitted {
				t.Fatalf("%s imports Arrow outside the analytical data plane or an explicit Arrow encoder", file.path)
			}
		}
	}
}

func TestUseCasesDoNotImportAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !isInternalPackage(file.pkgDir) || isAdapterOrCompositionPackage(file.pkgDir) {
			continue
		}
		for _, imported := range file.imports {
			if isForbiddenUseCaseImport(imported) {
				t.Fatalf("%s imports adapter or transport package %s", file.path, imported)
			}
		}
	}
}

func TestCapabilityAPIPackagesAreTransportContractOnly(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !strings.HasSuffix(file.pkgDir, "/api") || file.pkgDir == "internal/app/api" {
			continue
		}
		for _, imported := range file.imports {
			if imported == modulePath+"/internal/app" ||
				imported == "net/http" ||
				imported == "github.com/go-chi/chi/v5" ||
				strings.Contains(imported, "datastar") ||
				strings.Contains(imported, "gomponents") {
				t.Fatalf("%s imports forbidden API dependency %s", file.path, imported)
			}
		}
	}
}

func TestCapabilityUIPackagesAreRenderOnly(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !strings.HasSuffix(file.pkgDir, "/ui") {
			continue
		}
		for _, imported := range file.imports {
			if imported == modulePath+"/internal/app/api/gen" ||
				imported == modulePath+"/internal/platform/db" ||
				imported == "database/sql" ||
				imported == "net/http" ||
				imported == "github.com/go-chi/chi/v5" {
				t.Fatalf("%s imports forbidden UI dependency %s", file.path, imported)
			}
		}
		for _, forbidden := range []string{".QueryContext(", ".QueryRowContext(", ".ExecContext("} {
			if strings.Contains(file.body, forbidden) {
				t.Fatalf("%s performs storage access via %s", file.path, forbidden)
			}
		}
	}
}

func TestStaticSQLiteAdaptersUseGeneratedQueries(t *testing.T) {
	generatedOnly := map[string]bool{
		"internal/agent/sqlite":                 true,
		"internal/dashboard/publication/sqlite": true,
		"internal/manageddata/sqlite":           true,
		"internal/servingstate/sqlite":          true,
		"internal/project/sqlite":               true,
	}
	generatedOnlyFiles := map[string]bool{
		"internal/access/sqlite/api_symmetry.go":  true,
		"internal/access/sqlite/authorization.go": true,
		"internal/refresh/sqlite/runs.go":         true,
	}
	for _, file := range productionGoFiles(t) {
		if !generatedOnly[file.pkgDir] && !generatedOnlyFiles[file.path] {
			continue
		}
		for _, directCall := range []string{".QueryContext(", ".QueryRowContext(", ".ExecContext("} {
			if strings.Contains(file.body, directCall) {
				t.Fatalf("%s bypasses sqlc via %s", file.path, directCall)
			}
		}
	}
}

func TestCapabilitySQLiteAdaptersDoNotImportOtherSQLiteAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !strings.Contains(file.pkgDir, "/sqlite") {
			continue
		}
		for _, imported := range file.imports {
			if !strings.HasPrefix(imported, modulePath+"/internal/") || !strings.Contains(imported, "/sqlite") {
				continue
			}
			packagePath := strings.TrimPrefix(imported, modulePath+"/")
			source, sourceOK := ClassifyPackage(file.pkgDir)
			target, targetOK := ClassifyPackage(packagePath)
			if sourceOK && targetOK && source.Capability == target.Capability {
				continue
			}
			t.Errorf("%s imports persistence implementation %s; use a consumer-owned port or module bridge", file.path, imported)
		}
	}
}

func TestDashboardPersistenceDoesNotWriteAccessTables(t *testing.T) {
	root := repoRoot(t)
	dashboardRoot := filepath.Join(root, "internal", "dashboard")
	err := filepath.WalkDir(dashboardRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		extension := filepath.Ext(path)
		if entry.IsDir() || !strings.Contains(filepath.ToSlash(path), "/sqlite/") ||
			(extension != ".go" && extension != ".sql") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, statement := range []string{
			"INSERT INTO principals",
			"UPDATE principals",
			"DELETE FROM principals",
		} {
			if strings.Contains(string(body), statement) {
				t.Errorf("%s writes the Access-owned principals table via %q; use an Access-owned operation", filepath.ToSlash(relative), statement)
			}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestGeneratedPlatformQueriesStayInsidePlatform(t *testing.T) {
	const sharedQueries = modulePath + "/internal/platform/db"
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "internal/platform" || strings.HasPrefix(file.pkgDir, "internal/platform/db") {
			continue
		}
		for _, imported := range file.imports {
			if imported == sharedQueries {
				t.Errorf("%s imports the shared generated query package; generate capability-private queries instead", file.path)
			}
		}
	}
}

func TestPlatformSQLCOmitsUnusedCapabilityModels(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "sqlc.yaml"))
	require.NoError(t, err)
	config := string(body)
	start := strings.Index(config, `queries: "internal/platform/db/queries"`)
	end := strings.Index(config, `queries: "internal/access/sqlite/queries"`)
	if start < 0 || end < 0 || end <= start {
		t.Fatal("platform sqlc generation block is missing")
	}
	if !strings.Contains(config[start:end], "omit_unused_structs: true") {
		t.Fatal("platform sqlc generation exposes unused product-capability models")
	}
}

func TestSQLCQueriesAreSplitByDomain(t *testing.T) {
	root := repoRoot(t)
	for _, domain := range []string{
		"internal/access/sqlite/queries/access.sql",
		"internal/agent/sqlite/queries/agent.sql",
		"internal/platform/http/idempotency/sqlite/queries/idempotency.sql",
		"internal/platform/http/cursorsigning/sqlite/queries/cursor_signing.sql",
		"internal/dashboard/publication/sqlite/queries/publication.sql",
		"internal/manageddata/sqlite/queries/managed_data.sql",
		"internal/refresh/sqlite/runqueries/materialization.sql",
		"internal/platform/jobs/sqlite/queries/async_job.sql",
		"internal/platform/db/queries/platform.sql",
		"internal/refresh/sqlite/schedulequeries/refresh_pipeline.sql",
		"internal/servingstate/sqlite/queries/serving_state.sql",
		"internal/project/sqlite/queries/project.sql",
	} {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(domain)))
		if err != nil {
			t.Fatalf("read sqlc query domain %s: %v", domain, err)
		}
		if !strings.Contains(string(contents), "-- name:") {
			t.Fatalf("sqlc query domain %s contains no named queries", domain)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "platform", "db", "queries.sql")); !os.IsNotExist(err) {
		t.Fatal("legacy sqlc query monolith must not exist")
	}
}

func TestSQLCUsesRuntimeMigrationsAsItsSchemaSource(t *testing.T) {
	root := repoRoot(t)
	config, err := os.ReadFile(filepath.Join(root, "sqlc.yaml"))
	if err != nil {
		t.Fatalf("read sqlc config: %v", err)
	}
	if !strings.Contains(string(config), `schema: "internal/platform/migrations"`) {
		t.Fatal("sqlc must compile against the runtime Goose migrations")
	}
	if _, err := os.Stat(filepath.Join(root, "internal", "platform", "db", "schema.sql")); !os.IsNotExist(err) {
		t.Fatal("duplicate sqlc schema snapshot must not exist")
	}
}

func TestRequiredCapabilityAdaptersExist(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{
		"internal/access/http",
		"internal/admin/cli",
		"internal/admin/http",
		"internal/agent/cli",
		"internal/agent/http",
		"internal/analytics/connectors",
		"internal/refresh/http",
		"internal/dashboard/cli",
		"internal/dashboard/semanticapi",
		"internal/dashboard/http",
		"internal/manageddata/cli",
		"internal/project/cli",
	} {
		if !packageDirExists(root, dir) {
			t.Fatalf("required capability adapter package %s does not exist", dir)
		}
	}
}

func TestPlatformStoreSQLDBDoesNotLeakPastCompositionAndAdapters(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if !strings.Contains(file.body, ".SQLDB()") {
			continue
		}
		if isSQLDBAllowedFile(file) {
			continue
		}
		t.Fatalf("%s calls platform Store SQLDB outside composition or adapter construction", file.path)
	}
}

func TestRemovedLegacyAgentPackagesAreNotImported(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		for _, imported := range file.imports {
			switch imported {
			case modulePath + "/internal/agentapp",
				modulePath + "/internal/agentapp/sqlite",
				modulePath + "/internal/agenttools",
				modulePath + "/internal/agentconfig":
				t.Fatalf("%s imports legacy agent package %s", file.path, imported)
			}
		}
	}
}

func TestSecretComparisonsGoThroughSecretPackage(t *testing.T) {
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "internal/platform/security/secret" {
			continue
		}
		for _, imported := range file.imports {
			if imported == "crypto/subtle" {
				t.Fatalf("%s imports crypto/subtle directly; use internal/platform/security/secret for fixed-size secret comparisons", file.path)
			}
		}
	}
}

func TestProductionContainerContractExists(t *testing.T) {
	root := repoRoot(t)
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(dockerfile)
	for _, want := range []string{
		"FROM node:26-bookworm@sha256:",
		"FROM golang:1.27.0-bookworm@sha256:",
		"AS go-deps",
		"FROM go-deps AS sourcegen",
		"COPY --from=node /usr/local/bin/node /usr/local/bin/node",
		"COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules",
		"ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm",
		"./scripts/generate_build_sources.sh",
		"go run ./internal/app/tools/mapassets --out .data/map-assets",
		"go run ./internal/app/tools/clidocgen",
		"go run ./internal/app/tools/schemadocgen",
		"go run ./internal/app/tools/openapidocgen",
		"go run ./internal/app/tools/docsitegen",
		"FROM oven/bun:1.4.0@sha256:",
		"COPY --from=go-deps /usr/local/go/bin/gofmt /usr/local/bin/gofmt",
		"COPY --from=sourcegen /src/api/gen ./api/gen",
		"COPY --from=sourcegen /src/api/visualization ./api/visualization",
		"COPY --from=sourcegen /src/web/generated ./web/generated",
		"RUN bun install --frozen-lockfile --no-cache",
		"mkdir -p internal/dashboard/appearance",
		"bun scripts/generate_lucide_icon_catalog.ts",
		"bun scripts/generate_visualization_validator.ts",
		"bun run build",
		"FROM go-deps AS build",
		"COPY --from=sourcegen /src/internal/access/api/gen ./internal/access/api/gen",
		"COPY --from=sourcegen /src/internal/agent/api/gen ./internal/agent/api/gen",
		"COPY --from=sourcegen /src/internal/analytics/api/gen ./internal/analytics/api/gen",
		"COPY --from=sourcegen /src/internal/dashboard/api/gen ./internal/dashboard/api/gen",
		"COPY --from=sourcegen /src/internal/app/api/aggregate ./internal/app/api/aggregate",
		"COPY --from=sourcegen /src/internal/app/api/gen ./internal/app/api/gen",
		"COPY --from=sourcegen /src/internal/deployment/api/gen ./internal/deployment/api/gen",
		"COPY --from=sourcegen /src/internal/manageddata/api/gen ./internal/manageddata/api/gen",
		"COPY --from=sourcegen /src/internal/platform/http/api/gen ./internal/platform/http/api/gen",
		"COPY --from=sourcegen /src/internal/project/api/gen ./internal/project/api/gen",
		"COPY --from=sourcegen /src/internal/refresh/api/gen ./internal/refresh/api/gen",
		"COPY --from=sourcegen /src/internal/release/api/gen ./internal/release/api/gen",
		"COPY --from=sourcegen /src/internal/access/ui/signals/models.gen.go ./internal/access/ui/signals/models.gen.go",
		"COPY --from=sourcegen /src/internal/admin/ui/signals/models.gen.go ./internal/admin/ui/signals/models.gen.go",
		"COPY --from=sourcegen /src/internal/agent/ui/signals/models.gen.go ./internal/agent/ui/signals/models.gen.go",
		"COPY --from=sourcegen /src/internal/dashboard/ui/signals/models.gen.go ./internal/dashboard/ui/signals/models.gen.go",
		"COPY --from=web /src/internal/dashboard/appearance/icons_gen.go ./internal/dashboard/appearance/icons_gen.go",
		"COPY --from=sourcegen /src/docs ./docs",
		"CGO_ENABLED=1 go build",
		"CGO_ENABLED=1 go build -tags=duckdb_arrow -trimpath -ldflags=\"$BUILD_LDFLAGS\" -o /out/leapviewctl ./cmd/leapviewctl",
		"FROM debian:bookworm-slim@sha256:",
		"USER leapview",
		"WORKDIR /app",
		"COPY --from=web /src/static ./static",
		"COPY --from=sourcegen /src/.data/map-assets ./.data/map-assets",
		"LEAPVIEW_HOME=/var/lib/leapview/home",
		"LEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data",
		"LEAPVIEW_PRODUCTION=1",
		"HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD [\"leapview\", \"healthcheck\"]",
		"CMD [\"serve\", \"--production\"]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Dockerfile missing production container contract fragment %q", want)
		}
	}
	if count := strings.Count(text, "RUN go mod download"); count != 1 {
		t.Fatalf("Dockerfile downloads Go modules %d times, want one shared dependency stage", count)
	}
	const seededModuleCache = "type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked"
	if count := strings.Count(text, seededModuleCache); count != 4 {
		t.Fatalf("Dockerfile uses the seeded persistent Go module cache %d times, want source generation, map extraction, compilation, and extension-supply packaging", count)
	}

	ignored, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	ignoreText := string(ignored)
	for _, want := range []string{
		".data", ".leapview", "node_modules", "**/.tmp", "api/gen", "internal/access/api/gen", "internal/agent/api/gen", "internal/analytics/api/gen", "internal/dashboard/api/gen", "internal/deployment/api/gen", "internal/manageddata/api/gen",
		"internal/app/api/aggregate", "internal/app/api/gen", "internal/platform/http/api/gen", "internal/project/api/gen", "internal/refresh/api/gen", "internal/release/api/gen", "static/chunks",
	} {
		if !strings.Contains(ignoreText, want) {
			t.Fatalf(".dockerignore missing generated or runtime path %q", want)
		}
	}
}

func TestArchitectureDecisionLogIsWellFormed(t *testing.T) {
	root := repoRoot(t)
	adrRoot := filepath.Join(root, "adr")
	indexBody, err := os.ReadFile(filepath.Join(adrRoot, "README.md"))
	if err != nil {
		t.Fatalf("read ADR index: %v", err)
	}
	index := string(indexBody)
	if !strings.Contains(index, "# Architecture decision records") {
		t.Fatal("ADR index is missing its canonical heading")
	}
	if _, err := os.Stat(filepath.Join(adrRoot, "template.md")); err != nil {
		t.Fatalf("ADR template is unavailable: %v", err)
	}

	paths, err := filepath.Glob(filepath.Join(adrRoot, "[0-9][0-9][0-9][0-9]-*.md"))
	if err != nil {
		t.Fatalf("glob ADRs: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("ADR log contains no numbered decisions")
	}
	namePattern := regexp.MustCompile(`^([0-9]{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
	statusPattern := regexp.MustCompile(`(?m)^Status: (proposed|accepted|rejected|deprecated|superseded)$`)
	datePattern := regexp.MustCompile(`(?m)^Decision date: ([0-9]{4}-[0-9]{2}-[0-9]{2})$`)
	implementationPattern := regexp.MustCompile(`(?m)^Implementation: \S.*$`)
	seenIDs := make(map[string]string, len(paths))
	for _, path := range paths {
		name := filepath.Base(path)
		match := namePattern.FindStringSubmatch(name)
		if match == nil {
			t.Fatalf("ADR filename %q does not use NNNN-descriptive-name.md", name)
		}
		id := match[1]
		if prior, exists := seenIDs[id]; exists {
			t.Fatalf("ADR ID %s is reused by %s and %s", id, prior, name)
		}
		seenIDs[id] = name

		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read ADR %s: %v", name, readErr)
		}
		text := string(body)
		for _, want := range []string{
			"# ADR-" + id + ": ",
			"\n## Confirmation\n",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("ADR %s missing %q", name, want)
			}
		}
		if !statusPattern.MatchString(text) {
			t.Fatalf("ADR %s has a missing or unsupported decision status", name)
		}
		dateMatch := datePattern.FindStringSubmatch(text)
		if dateMatch == nil {
			t.Fatalf("ADR %s has no YYYY-MM-DD decision date", name)
		}
		if _, parseErr := time.Parse("2006-01-02", dateMatch[1]); parseErr != nil {
			t.Fatalf("ADR %s has invalid decision date %q: %v", name, dateMatch[1], parseErr)
		}
		if !implementationPattern.MatchString(text) {
			t.Fatalf("ADR %s has no implementation status", name)
		}
		if !strings.Contains(index, "]("+name+")") {
			t.Fatalf("ADR %s is missing from adr/README.md", name)
		}
	}

	indexedPattern := regexp.MustCompile(`\]\(([0-9]{4}-[a-z0-9]+(?:-[a-z0-9]+)*\.md)\)`)
	for _, match := range indexedPattern.FindAllStringSubmatch(index, -1) {
		if _, err := os.Stat(filepath.Join(adrRoot, match[1])); err != nil {
			t.Fatalf("ADR index links unavailable decision %s: %v", match[1], err)
		}
	}
}

func TestGeographicRendererDecisionAndDocumentationStayAligned(t *testing.T) {
	root := repoRoot(t)
	decision, err := os.ReadFile(filepath.Join(root, "adr", "0002-use-maplibre-for-geographic-rendering.md"))
	if err != nil {
		t.Fatalf("read geographic rendering decision: %v", err)
	}
	text := string(decision)
	for _, want := range []string{
		"# ADR-0002: Use MapLibre for geographic rendering",
		"Status: accepted",
		"MapLibre is the sole geographic renderer",
		"ECharts `geo`",
		"one geographic camera",
		"same-origin",
		"spatial-windowed",
		"| Capability | MapLibre | ECharts `geo` |",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("geographic rendering decision missing %q", want)
		}
	}
	article, err := os.ReadFile(filepath.Join(root, "docs", "articles", "architecture", "geographic-rendering.md"))
	if err != nil {
		t.Fatalf("read geographic rendering documentation: %v", err)
	}
	articleText := string(article)
	for _, want := range []string{
		"# Geographic rendering",
		"MapLibre is the sole geographic renderer",
		"ECharts `geo`",
		"same-origin",
		"spatial-windowed queries",
		"accessible tabular equivalent",
	} {
		if !strings.Contains(articleText, want) {
			t.Fatalf("geographic rendering documentation missing %q", want)
		}
	}
	navigation, err := os.ReadFile(filepath.Join(root, "docs", "navigation.yaml"))
	if err != nil {
		t.Fatalf("read docs navigation: %v", err)
	}
	if !strings.Contains(string(navigation), "source: articles/architecture/geographic-rendering.md") {
		t.Fatal("geographic rendering documentation is not registered in documentation navigation")
	}
}

func TestPublicSiteProductionContainerContractExists(t *testing.T) {
	root := repoRoot(t)
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.site"))
	if err != nil {
		t.Fatalf("read Dockerfile.site: %v", err)
	}
	text := string(dockerfile)
	for _, want := range []string{
		"FROM node:26-bookworm@sha256:",
		"FROM golang:1.27.0-bookworm@sha256:",
		"./scripts/generate_build_sources.sh",
		"go run -tags=duckdb_arrow ./internal/app/tools/ducklakeprepare",
		"go run -tags=duckdb_arrow ./internal/app/tools/visualdocgen",
		"FROM oven/bun:1.4.0@sha256:",
		"COPY --from=sourcegen /src/api/gen ./api/gen",
		"COPY --from=sourcegen /src/api/visualization ./api/visualization",
		"COPY --from=sourcegen /src/web/generated ./web/generated",
		"RUN bun install --frozen-lockfile --no-cache",
		"bun scripts/generate_visualization_validator.ts",
		"bun run build:site",
		"FROM golang:1.27.0-bookworm@sha256:",
		"CGO_ENABLED=0 go build -trimpath",
		"./cmd/leapview-site",
		"FROM gcr.io/distroless/static-debian12:nonroot@sha256:",
		"USER nonroot:nonroot",
		"ENV LEAPVIEW_SITE_BASE_URL=",
		"ENTRYPOINT [\"/leapview-site\"]",
		"CMD [\"-addr=:8081\"]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Dockerfile.site missing production container contract fragment %q", want)
		}
	}
	if strings.Contains(text, "apigen@v0.4.0") || strings.Contains(text, "apigenpostprocess") {
		t.Error("Dockerfile.site still uses the retired APIGen v0.4 generation pipeline")
	}
}

func TestBuildSourceGenerationContract(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "scripts", "generate_build_sources.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat shared build source generator: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("shared build source generator is not executable")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read shared build source generator: %v", err)
	}
	text := string(body)
	commands := []string{
		"GOTOOLCHAIN=go1.26.7 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote",
		"go run ./internal/app/tools/configgen",
		"go run ./internal/app/tools/layoutcontractgen",
		"go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target leapview-v1",
		"go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target ui-signals",
		"go -C pkg/apigen run ./cmd/apigen typespec-compile -manifest ../../api/apigen.yaml -target visualization-ir",
		"go -C pkg/apigen run ./cmd/apigen all -manifest ../../api/apigen.yaml -target visualization-ir",
		"schema export --format json-schema --out schemas/json",
	}
	previous := -1
	for _, command := range commands {
		current := strings.Index(text, command)
		if current < 0 {
			t.Fatalf("shared build source generator missing command %q", command)
		}
		if current <= previous {
			t.Fatalf("shared build source generator command %q is out of order", command)
		}
		previous = current
	}
}

func TestResponsiveLayoutContractGenerationIsAvailableToEveryBrowserBuild(t *testing.T) {
	root := repoRoot(t)
	files := map[string][]string{
		"Taskfile.yml": {
			"layout-contract:generate:",
			"internal/dashboard/layoutcontract/contracts.json",
			"web/generated/dashboard-layout/contracts.json",
			"go run ./internal/app/tools/layoutcontractgen",
			"build:\n    desc: Build browser assets\n    deps:\n      - node:deps\n      - layout-contract:generate",
			"site:build:\n    desc: Build the LeapView public site assets from generated contracts",
			"- task: layout-contract:generate",
		},
		filepath.Join("scripts", "generate_build_sources.sh"): {
			"go run ./internal/app/tools/layoutcontractgen",
		},
		filepath.Join("web", "components", "dashboard", "visualization", "layout.ts"): {
			"../../../generated/dashboard-layout/contracts.json",
		},
	}
	for name, fragments := range files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(body), fragment) {
				t.Errorf("%s missing responsive layout generation fragment %q", name, fragment)
			}
		}
	}
}

func TestCoreProceduralGuidesUseTheOperationalTemplate(t *testing.T) {
	root := repoRoot(t)
	guides := []string{
		"docs/articles/start/installation.md",
		"docs/articles/start/first-dashboard.md",
		"docs/articles/build/connect-data.md",
		"docs/articles/build/models.md",
		"docs/articles/build/semantic-model.md",
		"docs/articles/build/dashboard.md",
		"docs/guides/cli/validate-deploy.md",
		"docs/articles/operate/self-hosting.md",
		"docs/articles/security/oidc.md",
		"docs/articles/integrate/api-quickstart.md",
	}
	for _, guide := range guides {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(guide)))
		if err != nil {
			t.Errorf("read %s: %v", guide, err)
			continue
		}
		text := string(body)
		for _, section := range []string{"\n## Before you begin\n", "\n## Validate", "\n## Verify", "\n## Troubleshooting\n", "\n## Next steps\n"} {
			if !strings.Contains(text, section) {
				t.Errorf("%s missing procedural section %q", guide, strings.TrimSpace(section))
			}
		}
		if !strings.Contains(text, "\n1. ") {
			t.Errorf("%s does not contain a numbered procedure", guide)
		}
	}
}

func TestDevelopmentServerTracksCompiledFallbackProcess(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		`go build -tags=duckdb_arrow -o "$TMP_DIR/leapview-dev" ./cmd/leapview`,
		`"$TMP_DIR/leapview-dev" >> "$LOG_FILE" 2>&1 &`,
		`LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES="${LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES:-67108864}"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server script missing tracked binary fragment %q", want)
		}
	}
	if strings.Contains(serverText, `go run ./cmd/leapview >> "$LOG_FILE" 2>&1 &`) {
		t.Fatal("development server must not track the go run wrapper as the server process")
	}

	qa, err := os.ReadFile(filepath.Join(root, "scripts", "qa_ui_framework.ts"))
	if err != nil {
		t.Fatalf("read UI framework QA script: %v", err)
	}
	qaText := string(qa)
	if !strings.Contains(qaText, "const managedServerReadyAttempts = 1800") ||
		!strings.Contains(qaText, "attempt < managedServerReadyAttempts") {
		t.Fatal("UI framework QA must allow a cold Go build before checking server readiness")
	}
	for _, want := range []string{
		"LEAPVIEW_MANAGED_DATA_DIR: `${qaHome}/managed-data`",
		"['chmod', '-R', 'u+w', qaHome]",
	} {
		if !strings.Contains(qaText, want) {
			t.Fatalf("UI framework QA must isolate and clean managed-data state: missing %q", want)
		}
	}
}

func TestDevelopmentServerDefaultsAgentToAmbientDeepSeekCredential(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		`if [[ -z "${LEAPVIEW_AGENT_API_KEY:-}" && -n "${DEEPSEEK_API_KEY:-}" ]]; then`,
		`LEAPVIEW_AGENT_API_KEY="$DEEPSEEK_API_KEY"`,
		`LEAPVIEW_AGENT_BASE_URL="${LEAPVIEW_AGENT_BASE_URL:-https://api.deepseek.com}"`,
		`LEAPVIEW_AGENT_MODEL="${LEAPVIEW_AGENT_MODEL:-deepseek-v4-flash}"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must configure the default DeepSeek agent without overriding explicit agent credentials: missing %q", want)
		}
	}
}

func TestDevelopmentServerRunsAgentMCPSmokeCheckAfterPublishing(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		"mcp_smoke()",
		`"method":"tools/list"`,
		`"name":"catalog_list"`,
		`name:"query_semantic_model"`,
		`mcp_smoke "$port"`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must smoke-test the live MCP tool surface after publishing: missing %q", want)
		}
	}
	if strings.Index(serverText, `go run ./cmd/leapview publish`) > strings.Index(serverText, `mcp_smoke "$port"`) {
		t.Fatal("development MCP smoke check must run after candidate publication")
	}
}

func TestDevelopmentPublishingCanonicalizesSharedDatasetRoots(t *testing.T) {
	root := repoRoot(t)
	server, err := os.ReadFile(filepath.Join(root, "scripts", "dev-server.sh"))
	if err != nil {
		t.Fatalf("read development server script: %v", err)
	}
	serverText := string(server)
	for _, want := range []string{
		"canonical_source_root()",
		`local token="${LEAPVIEW_DEV_API_TOKEN:-dev}"`,
		`from="$(canonical_source_root "$from")"`,
		`candidate_id="$(awk '$1 == "candidate" { print $2; exit }' <<<"$dev_output")"`,
		`go run ./cmd/leapview publish "$candidate_id" --token "$token"`,
		`publish) publish_running "$@" ;;`,
	} {
		if !strings.Contains(serverText, want) {
			t.Fatalf("development server must safely resolve shared dataset roots: missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"go run ./cmd/leapview publish --project",
		"go run ./cmd/leapview publish --target",
	} {
		if strings.Contains(serverText, forbidden) {
			t.Fatalf("development server still uses retired publish selector %q", forbidden)
		}
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	if !strings.Contains(string(taskfile), "./scripts/dev-server.sh publish") {
		t.Fatal("dev:publish must delegate to the canonical development server publication path")
	}
}

func TestContinuousIntegrationWorkflowsAreTieredAndMergeQueueAware(t *testing.T) {
	root := repoRoot(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	mergeWorkflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "merge-validation.yml"))
	if err != nil {
		t.Fatalf("read merge validation workflow: %v", err)
	}
	artifactWorkflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "artifacts.yml"))
	if err != nil {
		t.Fatalf("read main artifact workflow: %v", err)
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	text := string(workflow)
	for _, want := range []string{
		"name: CI",
		"pull_request:",
		"types: [opened, synchronize, reopened, ready_for_review, stacked]",
		"workflow_dispatch:",
		"group: ci-${{ github.workflow }}-${{ github.event.pull_request.stack.id || github.ref }}",
		"apigen-validation:",
		"name: APIGen tests (PR)",
		"go-packages-validation:",
		"name: Go package tests (PR)",
		"go-application-validation:",
		"name: Go application tests (PR)",
		"frontend-validation:",
		"name: Frontend tests (PR)",
		"postgres-isolation-validation:",
		"name: PostgreSQL topology isolation (PR)",
		"spatial-tile-benchmarks:",
		"name: Spatial tile benchmarks (PR)",
		"runs-on: ubuntu-24.04",
		"uses: ./.github/actions/setup-ci",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare",
		"run: task ci:lane:go:apigen",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task generated:check",
		"ci-gate:",
		"name: CI gate",
		"needs: [apigen-validation, go-packages-validation, go-application-validation, frontend-validation, postgres-isolation-validation, spatial-tile-benchmarks]",
		"APIGEN_RESULT: ${{ needs.apigen-validation.result }}",
		"GO_PACKAGES_RESULT: ${{ needs.go-packages-validation.result }}",
		"GO_APPLICATION_RESULT: ${{ needs.go-application-validation.result }}",
		"FRONTEND_RESULT: ${{ needs.frontend-validation.result }}",
		"POSTGRES_ISOLATION_RESULT: ${{ needs.postgres-isolation-validation.result }}",
		"SPATIAL_BENCHMARK_RESULT: ${{ needs.spatial-tile-benchmarks.result }}",
		"Validation is deferred to the top of this stack.",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI workflow missing GitHub-hosted fragment %q", want)
		}
	}
	apigenCI := workflowJobBlock(t, text, "apigen-validation")
	goPackagesCI := workflowJobBlock(t, text, "go-packages-validation")
	goApplicationCI := workflowJobBlock(t, text, "go-application-validation")
	frontendCI := workflowJobBlock(t, text, "frontend-validation")
	postgresIsolationCI := workflowJobBlock(t, text, "postgres-isolation-validation")
	for name, block := range map[string]string{
		"apigen-validation":             apigenCI,
		"go-packages-validation":        goPackagesCI,
		"go-application-validation":     goApplicationCI,
		"frontend-validation":           frontendCI,
		"postgres-isolation-validation": postgresIsolationCI,
	} {
		for _, want := range []string{
			"github.event_name == 'workflow_dispatch'",
			"github.event.pull_request.stack == null",
			"github.event.pull_request.stack.position == github.event.pull_request.stack.size",
		} {
			if !strings.Contains(block, want) {
				t.Fatalf("%s job is not limited to standalone pull requests and stack tips: missing %q", name, want)
			}
		}
	}
	for _, want := range []string{
		"run: task postgres:test:up",
		"run: task postgres:test:check",
		"if: always()",
		"run: task postgres:test:down",
	} {
		if !strings.Contains(postgresIsolationCI, want) {
			t.Fatalf("PostgreSQL topology isolation lane missing %q", want)
		}
	}
	for _, want := range []string{
		"name: Validate Prometheus rules and fixtures",
		"run: task observability:check",
	} {
		if !strings.Contains(goPackagesCI, want) {
			t.Fatalf("PR Go package validation missing observability check %q", want)
		}
	}
	for _, forbidden := range []string{
		"push:",
		"Build and qualify the production image remotely",
		"Build and smoke-test the public site image remotely",
		"--file Dockerfile.site",
		"task image:qualify:production",
		"task image:qualify:site",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("PR CI retains post-merge artifact responsibility %q", forbidden)
		}
	}
	mergeText := string(mergeWorkflow)
	for _, want := range []string{
		"name: Merge validation",
		"merge_group:",
		"types: [checks_requested]",
		"group: merge-validation-${{ github.ref }}",
		"apigen-validation:",
		"name: APIGen tests (merge queue)",
		"go-packages-validation:",
		"name: Go package tests (merge queue)",
		"go-application-validation:",
		"name: Go application tests (merge queue)",
		"frontend-validation:",
		"name: Frontend tests (merge queue)",
		"full-validation:",
		"name: Full merge validation",
		"runs-on: ubuntu-24.04",
		"uses: ./.github/actions/setup-ci",
		"needs: [apigen-validation, go-packages-validation, go-application-validation, frontend-validation]",
		"run: task ci:full:extras",
		"name: CI gate",
		"needs: [apigen-validation, go-packages-validation, go-application-validation, frontend-validation, full-validation]",
	} {
		if !strings.Contains(mergeText, want) {
			t.Fatalf("merge validation workflow missing %q", want)
		}
	}
	if strings.Contains(mergeText, "group: merge-validation-${{ github.repository }}") {
		t.Fatal("merge validation concurrency must not cancel distinct merge-queue candidates")
	}
	goPackagesMerge := workflowJobBlock(t, mergeText, "go-packages-validation")
	for _, want := range []string{
		"name: Validate Prometheus rules and fixtures",
		"run: task observability:check",
	} {
		if !strings.Contains(goPackagesMerge, want) {
			t.Fatalf("merge-queue Go package validation missing observability check %q", want)
		}
	}
	for _, want := range []string{
		"observability:check:",
		"task: observability:alerts:check",
		"task: observability:sli:check",
	} {
		if !strings.Contains(string(taskfile), want) {
			t.Fatalf("Taskfile missing aggregate observability check %q", want)
		}
	}
	artifactText := string(artifactWorkflow)
	for _, want := range []string{
		"name: Main artifacts",
		"push:",
		"branches: [main]",
		"build-production-image:",
		"name: Build production image",
		"id: identity",
		"git show -s --format=%cI \"$revision\"",
		"uses: docker/build-push-action@",
		"BUILD_VERSION=${{ steps.identity.outputs.version }}",
		"BUILD_REVISION=${{ steps.identity.outputs.revision }}",
		"BUILD_TIME=${{ steps.identity.outputs.build_time }}",
		"BUILD_DIRTY=false",
		"BUILD_RELEASE=false",
		".dirty == false and .development == true",
		"cache-from: type=gha,scope=production-amd64",
		"cache-to: type=gha,mode=max,scope=production-amd64",
		"qualify-production-image:",
		"name: Qualify production image",
		"needs: build-production-image",
		"uses: ./.github/actions/setup-ci",
		"task image:qualify:production IMAGE=\"${immutable_image}\"",
	} {
		if !strings.Contains(artifactText, want) {
			t.Fatalf("main artifact workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"Build and smoke-test the public site image remotely",
		"--file Dockerfile.site",
		"task image:qualify:site",
	} {
		if strings.Contains(artifactText, forbidden) {
			t.Fatalf("main artifact workflow retains coupled site publication fragment %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"allow-source-fallback",
		"autback-poc",
		"depot/",
		"self-hosted",
		"group: leapview-ci",
		"clean: false",
		"run_ci_container.sh",
	} {
		if strings.Contains(text+mergeText+artifactText, forbidden) {
			t.Fatalf("CI workflows retain superseded runner fragment %q", forbidden)
		}
	}
	for _, contract := range []struct {
		block string
		want  string
	}{
		{block: goPackagesCI, want: "task ci:lane:go:packages"},
		{block: goApplicationCI, want: "task ci:lane:go:application"},
		{block: frontendCI, want: "task ci:lane:frontend"},
	} {
		for _, want := range []string{"uses: ./.github/actions/setup-ci", "task ci:prepare", contract.want} {
			if !strings.Contains(contract.block, want) {
				t.Fatalf("GitHub-hosted validation lane missing %q", want)
			}
		}
	}
	for _, want := range []string{"uses: ./.github/actions/setup-ci", "task ci:lane:go:apigen"} {
		if !strings.Contains(apigenCI, want) {
			t.Fatalf("GitHub-hosted APIGen lane missing %q", want)
		}
	}
	if strings.Contains(apigenCI, "task ci:prepare") {
		t.Fatal("GitHub-hosted APIGen lane must not pay for repository-wide preparation")
	}
	for name, block := range map[string]string{"go package": goPackagesCI, "frontend": frontendCI} {
		if !strings.Contains(block, "task generated:check") {
			t.Fatalf("%s validation lane must verify generated artifacts", name)
		}
	}
	for _, retired := range []string{"autback.json", "Dockerfile.autback", filepath.Join(".github", "workflows", "autback.yml")} {
		if _, err := os.Stat(filepath.Join(root, retired)); !os.IsNotExist(err) {
			t.Fatalf("retired Autback integration remains at %s: %v", retired, err)
		}
	}
	taskText := string(taskfile)
	ciDispatcher := taskfileTaskBlock(t, taskText, "ci")
	for _, want := range []string{"- task: ci:pr"} {
		if !strings.Contains(ciDispatcher, want) {
			t.Fatalf("ci must preserve the canonical local PR workload: missing %q", want)
		}
	}
	ciLocal := taskfileTaskBlock(t, taskText, "ci:local")
	if !strings.Contains(ciLocal, "- task: ci:full") {
		t.Fatal("ci:local must remain a compatibility alias for the full current-machine contract")
	}
	for _, retired := range []string{"test", "autback:test", "autback:ci"} {
		if strings.Contains(taskText, "  "+retired+":\n") {
			t.Fatalf("Taskfile retains redundant top-level target %q", retired)
		}
	}
	deployCheck := taskfileTaskBlock(t, taskText, "deploy:check")
	if !strings.Contains(deployCheck, "- api:generate") {
		t.Fatal("deploy:check must generate its build-only API inputs")
	}
	siteImageQualification := taskfileTaskBlock(t, taskText, "image:qualify:site")
	if !strings.Contains(siteImageQualification, "- task: api:generate") {
		t.Fatal("site image qualification must generate the leapviewctl API inputs in a clean checkout")
	}
	for _, want := range []string{
		"config:generate:",
		"go run ./internal/app/tools/configgen",
		"config:check:",
		"go run ./internal/app/tools/configgen --check",
		"node:audit:",
		"bun audit",
		"vuln:",
		"golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...",
		"security:policy:",
		"security:dependencies:",
		"security:source:",
		"security:check:",
		"ci:prepare:frontend:",
		"ci:test:docs-site:",
		"go test ./cmd/leapview-site ./docs ./site ./internal/app/site/...",
		"ci:test:frontend:core:",
		"ci:test:frontend:reports:",
		"ci:test:frontend:chat:",
		"ci:test:frontend:data:",
		"ci:test:frontend:site:",
		"test:go:",
		"task --parallel test:go:packages test:go:app:shards",
		"task --parallel --concurrency 3 test:go:app:0 test:go:app:1 test:go:app:2 test:go:app:3",
		"scripts/postgres-conformance-tests.sh list",
		"grep -Fvx -f",
		"--shard-count 4",
		"image:qualify:production:",
		"TMPDIR={{.ROOT_DIR}}/.tmp/qualification/tmp",
		"go run ./cmd/leapviewctl qualify image",
		"--require-immutable",
		"image:qualify:site:",
		"go run ./cmd/leapviewctl qualify site-image",
	} {
		if !strings.Contains(taskText, want) {
			t.Fatalf("Taskfile missing vulnerability gate fragment %q", want)
		}
	}
	var packageManifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	packageJSON, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	if err := json.Unmarshal(packageJSON, &packageManifest); err != nil {
		t.Fatalf("decode package.json: %v", err)
	}
	for script := range packageManifest.Scripts {
		if strings.HasPrefix(script, "test:") && !strings.Contains(taskText, "bun run "+script) {
			t.Errorf("frontend test script %q is not assigned to a Taskfile CI shard", script)
		}
	}
	for _, retired := range []string{
		"scripts/benchmark_autback_digest_push.sh",
		"scripts/qualify_production_image.sh",
		"scripts/smoke_production_image.sh",
		"scripts/smoke_site_image.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, retired)); !os.IsNotExist(err) {
			t.Fatalf("retired Autback shell implementation still exists at %s: %v", retired, err)
		}
	}
}

func TestPostgreSQLConformanceCIUsesSourceInventoryAndNoGlobalNameSkip(t *testing.T) {
	root := repoRoot(t)
	taskfileBytes, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	taskfile := string(taskfileBytes)
	packages := taskfileTaskBlock(t, taskfile, "test:go:packages")
	conformance := taskfileTaskBlock(t, taskfile, "test:go:postgres-conformance")
	if strings.Contains(packages, "-skip '^(TestPostgreSQL18") || strings.Contains(packages, "TestBaselinePostgreSQL18$") {
		t.Fatal("ordinary package lane must not skip PostgreSQL tests by test-name prefix")
	}
	for _, fragment := range []string{
		"export LEAPVIEW_POSTGRES_CONFORMANCE_SKIP=1",
		"scripts/postgres-conformance-tests.sh list",
		"grep -Fvx -f",
	} {
		if !strings.Contains(packages, fragment) {
			t.Fatalf("ordinary package lane missing PostgreSQL inventory guard %q", fragment)
		}
	}
	if !strings.Contains(conformance, "bash scripts/postgres-conformance-tests.sh run") {
		t.Fatal("PostgreSQL conformance lane must execute the same source-derived inventory")
	}
	if !strings.Contains(conformance, "LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED") {
		t.Fatal("PostgreSQL conformance lane must remain fail-closed")
	}
	scriptBytes, err := os.ReadFile(filepath.Join(root, "scripts", "postgres-conformance-tests.sh"))
	if err != nil {
		t.Fatalf("read PostgreSQL conformance inventory: %v", err)
	}
	script := string(scriptBytes)
	if !strings.Contains(script, "postgrestest\\.Start") || !strings.Contains(script, "tcpostgres\\.Run") {
		t.Fatal("PostgreSQL inventory must include shared and direct PostgreSQL container harnesses")
	}
	if strings.Contains(script, "testcontainers\\.Run") {
		t.Fatal("PostgreSQL inventory must not route generic containers such as MinIO")
	}
	listCmd := exec.Command("bash", filepath.Join(root, "scripts", "postgres-conformance-tests.sh"), "list")
	listCmd.Dir = root
	listedOutput, err := listCmd.Output()
	if err != nil {
		t.Fatalf("run PostgreSQL conformance inventory: %v", err)
	}
	allCmd := exec.Command("go", "list", "./...")
	allCmd.Dir = root
	allOutput, err := allCmd.Output()
	if err != nil {
		t.Fatalf("list Go packages for PostgreSQL inventory guard: %v", err)
	}
	allPackages := make(map[string]struct{})
	for _, packagePath := range strings.Fields(string(allOutput)) {
		allPackages[packagePath] = struct{}{}
	}
	for _, packagePath := range strings.Fields(string(listedOutput)) {
		if _, ok := allPackages[packagePath]; !ok {
			t.Fatalf("PostgreSQL inventory package %q is not a Go package", packagePath)
		}
	}
	filteredCmd := exec.Command("bash", "-c", `set -eu; inventory_file="$(mktemp)"; trap 'rm -f "$inventory_file"' EXIT; bash scripts/postgres-conformance-tests.sh list >"$inventory_file"; all_packages="$(go list ./...)"; printf '%s\n' "$all_packages" | grep -v '/internal/app$' | grep -Fvx -f "$inventory_file"`)
	filteredCmd.Dir = root
	filteredOutput, err := filteredCmd.Output()
	if err != nil {
		t.Fatalf("compute ordinary Go package set: %v", err)
	}
	filteredPackages := make(map[string]struct{})
	for _, packagePath := range strings.Fields(string(filteredOutput)) {
		filteredPackages[packagePath] = struct{}{}
	}
	for _, packagePath := range strings.Fields(string(listedOutput)) {
		if _, ok := filteredPackages[packagePath]; ok {
			t.Fatalf("PostgreSQL inventory package %q remains in ordinary package set", packagePath)
		}
	}
}

func TestContinuousIntegrationHasExplicitPRFullAndNightlyTiers(t *testing.T) {
	root := repoRoot(t)
	read := func(path ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(path...), err)
		}
		return string(data)
	}

	taskfile := read("Taskfile.yml")
	prWorkflow := read(".github", "workflows", "ci.yml")
	mergeWorkflow := read(".github", "workflows", "merge-validation.yml")
	nightlyWorkflow := read(".github", "workflows", "nightly.yml")
	pr := taskfileTaskBlock(t, taskfile, "ci:pr")
	for _, want := range []string{
		"- task: ci:prepare",
		"task --parallel --concurrency 2 ci:lane:go ci:lane:frontend",
		"- task: generated:check",
	} {
		if !strings.Contains(pr, want) {
			t.Fatalf("ci:pr missing %q", want)
		}
	}
	prepare := taskfileTaskBlock(t, taskfile, "ci:prepare")
	for _, want := range []string{"- task: ci:extensions:prepare", "- task: generate", "- task: build", "- task: site:build"} {
		if !strings.Contains(prepare, want) {
			t.Fatalf("ci:prepare missing %q", want)
		}
	}
	for _, lane := range []string{"ci:lane:go", "ci:lane:frontend"} {
		if !strings.Contains(taskfile, "  "+lane+":\n") {
			t.Fatalf("Taskfile missing bounded CI lane %q", lane)
		}
	}
	goLane := taskfileTaskBlock(t, taskfile, "test:go:prepared")
	if !strings.Contains(goLane, "task --parallel test:go:packages test:go:app:shards") {
		t.Fatal("prepared Go lane must allow the package sweep and two application shards to overlap")
	}
	appShards := taskfileTaskBlock(t, taskfile, "test:go:app:shards")
	if !strings.Contains(appShards, "task --parallel --concurrency 3 test:go:app:0 test:go:app:1 test:go:app:2 test:go:app:3") {
		t.Fatal("application test shards must retain a three-process bound")
	}
	frontendLane := taskfileTaskBlock(t, taskfile, "ci:lane:frontend")
	if strings.Contains(frontendLane, "- task: build") {
		t.Fatal("frontend lane must not replace production assets while Go tests are running")
	}
	frontendSite := taskfileTaskBlock(t, taskfile, "ci:test:frontend:site")
	if !strings.Contains(frontendSite, "bun run test:site:prepared") {
		t.Fatal("frontend site tests must use the site tree prepared before concurrent lanes")
	}
	full := taskfileTaskBlock(t, taskfile, "ci:full")
	for _, want := range []string{
		"- task: ci:pr",
		"- task: ci:full:extras",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("ci:full missing %q", want)
		}
	}
	fullExtras := taskfileTaskBlock(t, taskfile, "ci:full:extras")
	for _, want := range []string{
		"- task: desktop:test",
		"go vet ./...",
		"go test -race ./pkg/...",
		"- task: quality:critical:race",
		"- task: qa:ui-framework",
		"- task: deploy:check",
	} {
		if !strings.Contains(fullExtras, want) {
			t.Fatalf("ci:full:extras missing %q", want)
		}
	}
	nightly := taskfileTaskBlock(t, taskfile, "ci:nightly")
	for _, want := range []string{"- task: ci:full", "- task: ci:nightly:extras"} {
		if !strings.Contains(nightly, want) {
			t.Fatalf("ci:nightly missing %q", want)
		}
	}
	nightlyExtras := taskfileTaskBlock(t, taskfile, "ci:nightly:extras")
	for _, want := range []string{"- task: generate", "- task: security:check", "- task: dependency-security"} {
		if !strings.Contains(nightlyExtras, want) {
			t.Fatalf("ci:nightly:extras missing %q", want)
		}
	}
	ciLocal := taskfileTaskBlock(t, taskfile, "ci:local")
	if !strings.Contains(ciLocal, "- task: ci:full") {
		t.Fatal("ci:local must remain a compatibility alias for the full current-machine contract")
	}

	for _, want := range []string{
		"run: task ci:lane:go:apigen",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task generated:check",
	} {
		if !strings.Contains(prWorkflow, want) {
			t.Fatalf("pull-request workflow missing split fast-tier command %q", want)
		}
	}
	if strings.Contains(prWorkflow, "\n        run: task ci:pr\n") || strings.Contains(prWorkflow, "\n        run: task ci:full\n") {
		t.Fatal("pull-request workflow must distribute the fast tier across independent runners")
	}
	for _, want := range []string{
		"merge_group:",
		"run: task ci:lane:go:apigen",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task ci:full:extras",
	} {
		if !strings.Contains(mergeWorkflow, want) {
			t.Fatalf("merge queue must run the split full tier against the exact merge group: missing %q", want)
		}
	}
	for _, want := range []string{
		"name: Nightly CI",
		"schedule:",
		"cron: '17 2 * * *'",
		"workflow_dispatch:",
		"run: task ci:lane:go:apigen",
		"run: task ci:lane:go:packages",
		"run: task ci:lane:go:application",
		"run: node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		"run: task ci:full:extras",
		"run: task ci:nightly:extras",
	} {
		if !strings.Contains(nightlyWorkflow, want) {
			t.Fatalf("nightly workflow missing %q", want)
		}
	}
}

func TestDependencySecurityContractCoversEveryDependencyGraph(t *testing.T) {
	root := repoRoot(t)
	taskfileBytes, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	taskfile := string(taskfileBytes)

	nightlyExtras := taskfileTaskBlock(t, taskfile, "ci:nightly:extras")
	if !strings.Contains(nightlyExtras, "- task: dependency-security") {
		t.Fatal("ci:nightly:extras must delegate to the canonical dependency-security contract")
	}

	security := taskfileTaskBlock(t, taskfile, "dependency-security")
	for _, want := range []string{
		"- task: node:deps",
		"- task: desktop:deps",
		"npm --prefix pkg/apigen/typespec ci",
		"- task: generate",
		"- task: node:audit",
		"- task: desktop:audit",
		"- task: apigen:audit",
		"- task: vuln",
	} {
		if !strings.Contains(security, want) {
			t.Fatalf("dependency-security is missing %q", want)
		}
	}
	ordered := []string{
		"- task: node:deps",
		"- task: desktop:deps",
		"npm --prefix pkg/apigen/typespec ci",
		"- task: generate",
		"- task: vuln",
	}
	previous := -1
	for _, fragment := range ordered {
		at := strings.Index(security, fragment)
		if at < 0 {
			t.Fatalf("dependency-security is missing ordered fragment %q", fragment)
		}
		if at <= previous {
			t.Fatalf("dependency-security runs %q out of order", fragment)
		}
		previous = at
	}

	nodeDeps := taskfileTaskBlock(t, taskfile, "node:deps")
	if !strings.Contains(nodeDeps, "bun install --frozen-lockfile") {
		t.Fatal("root Bun security preparation must use the frozen lockfile")
	}
	desktopDeps := taskfileTaskBlock(t, taskfile, "desktop:deps")
	if !strings.Contains(desktopDeps, "bun install --frozen-lockfile") {
		t.Fatal("desktop Bun security preparation must use the frozen lockfile")
	}

	for task, fragments := range map[string][]string{
		"node:audit":            {"bun audit"},
		"desktop:audit":         {"dir: desktop", "bun audit"},
		"apigen:audit":          {"npm --prefix pkg/apigen/typespec ci", "npm --prefix pkg/apigen/typespec audit"},
		"vuln":                  {"GOMEMLIMIT: 4GiB", "golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./..."},
		"security:report":       {"dependency-security", "dependencyreport report", ".tmp/release-security/dependency-clearance.json"},
		"security:report:check": {"dependencyreport check", ".tmp/release-security/dependency-clearance.json"},
	} {
		block := taskfileTaskBlock(t, taskfile, task)
		for _, fragment := range fragments {
			if !strings.Contains(block, fragment) {
				t.Errorf("%s is missing dependency security command %q", task, fragment)
			}
		}
	}
}

func TestNightlyDependencySecurityReportArtifactsAreFailClosed(t *testing.T) {
	root := repoRoot(t)
	workflowBytes, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "nightly.yml"))
	if err != nil {
		t.Fatalf("read nightly workflow: %v", err)
	}
	security := workflowJobBlock(t, string(workflowBytes), "security-validation")
	for _, want := range []string{
		"id: dependency-scans",
		"id: dependency-report",
		"if: ${{ always() }}",
		"continue-on-error: true",
		"if: ${{ always() && steps.dependency-report.outcome == 'failure' }}",
		"name: dependency-security-report-failed",
		"id: dependency-check",
		"if: ${{ steps.dependency-report.outcome == 'success' }}",
		"name: dependency-security-report",
		"if: ${{ steps.dependency-scans.outcome == 'success' && steps.dependency-report.outcome == 'success' && steps.dependency-check.outcome == 'success' }}",
		"name: Require dependency security clearance",
		"SCANS_RESULT: ${{ steps.dependency-scans.outcome }}",
		"REPORT_RESULT: ${{ steps.dependency-report.outcome }}",
		"CHECK_RESULT: ${{ steps.dependency-check.outcome }}",
	} {
		if !strings.Contains(security, want) {
			t.Fatalf("nightly dependency security job missing fail-closed report fragment %q", want)
		}
	}
	validatedUpload := strings.Index(security, "name: Upload validated dependency security clearance report")
	if validatedUpload < 0 {
		t.Fatal("nightly dependency security job is missing the validated report upload")
	}
	validatedUploadBlock := security[validatedUpload:]
	if next := strings.Index(validatedUploadBlock, "\n      - name:"); next >= 0 {
		validatedUploadBlock = validatedUploadBlock[:next]
	}
	if strings.Contains(validatedUploadBlock, "always()") {
		t.Fatal("validated dependency security report upload must not use an unconditional always() guard")
	}
	if strings.Contains(validatedUploadBlock, "dependency-security-report-failed") {
		t.Fatal("validated dependency security report upload must not use the failed diagnostic artifact name")
	}
}

func TestGitHubHostedCISplitsGoWorkAndWarmsReusableBunCache(t *testing.T) {
	root := repoRoot(t)
	read := func(path ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(path...), err)
		}
		return string(data)
	}

	for _, workflow := range []string{"ci.yml", "merge-validation.yml", "nightly.yml"} {
		text := read(".github", "workflows", workflow)
		for _, want := range []string{
			"apigen-validation:",
			"run: task ci:lane:go:apigen",
			"go-packages-validation:",
			"run: task ci:lane:go:packages",
			"go-application-validation:",
			"run: task ci:lane:go:application",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s must split CPU-heavy Go work across runners: missing %q", workflow, want)
			}
		}
	}

	taskfile := read("Taskfile.yml")
	for _, want := range []string{
		"ci:lane:go:apigen:",
		"- task: apigen:test",
		"ci:lane:go:packages:",
		"- task: test:go:packages",
		"ci:lane:go:application:",
		"- task: test:go:app:shards",
		"- task: test:go:external",
	} {
		if !strings.Contains(taskfile, want) {
			t.Fatalf("Taskfile missing split Go lane fragment %q", want)
		}
	}

	setup := read(".github", "actions", "setup-ci", "action.yml")
	if !strings.Contains(setup, "key: bun-v2-") {
		t.Fatal("Bun cache key must be rolled after the incomplete default-branch cache")
	}
	artifacts := read(".github", "workflows", "artifacts.yml")
	setupAt := strings.Index(artifacts, "uses: ./.github/actions/setup-ci")
	populateAt := strings.Index(artifacts, "name: Populate main-branch Bun cache")
	if setupAt < 0 || populateAt < setupAt {
		t.Fatal("main artifact qualification must populate Bun downloads before the cache save hook")
	}
}

func TestGitHubHostedCIRecoversFromHungBunProcesses(t *testing.T) {
	root := repoRoot(t)
	const prepareWatchdog = "node scripts/ci_watchdog.mjs --timeout-seconds 420 --attempts 2 -- task ci:prepare"
	for workflow, contract := range map[string]struct {
		prepareCount    int
		frontendTimeout string
	}{
		"ci.yml":               {prepareCount: 3, frontendTimeout: "timeout-minutes: 30"},
		"merge-validation.yml": {prepareCount: 4, frontendTimeout: "timeout-minutes: 20"},
		"nightly.yml":          {prepareCount: 4, frontendTimeout: "timeout-minutes: 20"},
	} {
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", workflow))
		require.NoError(t, err)
		text := string(data)
		if strings.Contains(text, "run: task ci:prepare") {
			t.Fatalf("%s contains repository preparation without the Bun hang watchdog", workflow)
		}
		if got := strings.Count(text, prepareWatchdog); got != contract.prepareCount {
			t.Fatalf("%s wraps %d preparation steps, want %d", workflow, got, contract.prepareCount)
		}

		frontend := workflowJobBlock(t, text, "frontend-validation")
		for _, want := range []string{
			contract.frontendTimeout,
			prepareWatchdog,
			"node scripts/ci_watchdog.mjs --timeout-seconds 180 --attempts 2 -- task ci:lane:frontend",
		} {
			if !strings.Contains(frontend, want) {
				t.Fatalf("%s frontend lane does not bound and retry hung Bun work: missing %q", workflow, want)
			}
		}
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	require.NoError(t, err)
	frontendCore := taskfileTaskBlock(t, string(taskfile), "ci:test:frontend:core")
	if !strings.Contains(frontendCore, "node --test scripts/ci_watchdog.test.mjs") {
		t.Fatal("frontend core contract must exercise the Node watchdog independently of Bun")
	}

	nodeDeps := taskfileTaskBlock(t, string(taskfile), "node:deps")
	for _, want := range []string{"method: checksum", "package.json", "bun.lock", "node_modules/.bin/esbuild"} {
		if !strings.Contains(nodeDeps, want) {
			t.Errorf("node:deps must cache a verified install across nested preparation tasks: missing %q", want)
		}
	}

	setup, err := os.ReadFile(filepath.Join(root, ".github", "actions", "setup-ci", "action.yml"))
	require.NoError(t, err)
	if !strings.Contains(string(setup), "BUN_FEATURE_FLAG_NO_ORPHANS=1") {
		t.Fatal("hosted CI must enable Bun's inherited kernel-backed orphan cleanup")
	}
}

func TestGitHubHostedCIRunsAPIGenAsAnIndependentLeanLane(t *testing.T) {
	root := repoRoot(t)
	read := func(path ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{root}, path...)...))
		if err != nil {
			t.Fatalf("read %s: %v", filepath.Join(path...), err)
		}
		return string(data)
	}

	for _, workflow := range []string{"ci.yml", "merge-validation.yml", "nightly.yml"} {
		text := read(".github", "workflows", workflow)
		apigen := workflowJobBlock(t, text, "apigen-validation")
		for _, want := range []string{
			"uses: ./.github/actions/setup-ci",
			"run: task ci:lane:go:apigen",
		} {
			if !strings.Contains(apigen, want) {
				t.Fatalf("%s APIGen lane missing %q", workflow, want)
			}
		}
		for _, forbidden := range []string{"task ci:prepare", "task generated:check"} {
			if strings.Contains(apigen, forbidden) {
				t.Fatalf("%s APIGen lane must stay lean: found %q", workflow, forbidden)
			}
		}
	}

	taskfile := read("Taskfile.yml")
	apigenLane := taskfileTaskBlock(t, taskfile, "ci:lane:go:apigen")
	if !strings.Contains(apigenLane, "- task: apigen:test") {
		t.Fatal("APIGen CI lane must delegate to the complete vendored APIGen contract")
	}
	apigenTest := taskfileTaskBlock(t, taskfile, "apigen:test")
	for _, want := range []string{
		"npm --prefix pkg/apigen/typespec ci",
		"npm --prefix pkg/apigen/typespec test",
		"npm --prefix pkg/apigen/typespec run typecheck",
		"npm --prefix pkg/apigen/typespec run check:dist",
	} {
		if !strings.Contains(apigenTest, want) {
			t.Fatalf("APIGen test contract is missing %q", want)
		}
	}
	packagesLane := taskfileTaskBlock(t, taskfile, "ci:lane:go:packages")
	if strings.Contains(packagesLane, "apigen:test") {
		t.Fatal("Go package CI lane must not repeat the independent APIGen contract")
	}
}

func TestGitHubHostedWorkflowsUseEphemeralRunnersAndBoundedCaches(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"ci.yml", "merge-validation.yml", "nightly.yml", "artifacts.yml"} {
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(data)
		for _, want := range []string{"runs-on: ubuntu-24.04"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s must use GitHub-hosted ephemeral execution: missing %q", name, want)
			}
		}
		for _, forbidden := range []string{"self-hosted", "group: leapview-ci", "clean: false", "run_ci_container.sh"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s retains persistent-runner fragment %q", name, forbidden)
			}
		}
	}
	setup, err := os.ReadFile(filepath.Join(root, ".github", "actions", "setup-ci", "action.yml"))
	if err != nil {
		t.Fatalf("read GitHub-hosted CI setup action: %v", err)
	}
	setupText := string(setup)
	for _, want := range []string{
		"actions/setup-go@",
		"go-version-file: go.mod",
		"cache: true",
		"actions/setup-node@",
		`node-version: "24"`,
		"oven-sh/setup-bun@",
		"bun-version: 1.3.14",
		"hashicorp/setup-terraform@",
		"terraform_version: 1.13.5",
		"actions/cache@",
		"~/.bun/install/cache",
		"~/.cache/ms-playwright",
		"~/.cache/terraform",
		"install_go_tool()",
		"for attempt in 1 2 3",
		"GODEBUG=http2client=0 go install",
		"github.com/go-task/task/v3/cmd/task@v3.50.0",
		"github.com/bufbuild/buf/cmd/buf@v1.57.2",
		"playwright install --with-deps chromium",
	} {
		if !strings.Contains(setupText, want) {
			t.Fatalf("GitHub-hosted CI setup action missing %q", want)
		}
	}
	for _, retired := range []string{"Dockerfile.ci", filepath.Join("scripts", "run_ci_container.sh")} {
		if _, err := os.Stat(filepath.Join(root, retired)); !os.IsNotExist(err) {
			t.Fatalf("persistent-runner implementation remains at %s: %v", retired, err)
		}
	}
}

func TestContinuousIntegrationHealthWorkflowReportsAndAlerts(t *testing.T) {
	root := repoRoot(t)
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci-health.yml"))
	if err != nil {
		t.Fatalf("read CI health workflow: %v", err)
	}
	text := string(workflow)
	for _, want := range []string{
		"name: CI health",
		"schedule:",
		"workflow_dispatch:",
		"actions: read",
		"issues: write",
		"go run ./internal/app/tools/cireport",
		"--days 7",
		"name: ci-health",
		"retention-days: 30",
		"CI health thresholds exceeded",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI health workflow missing fragment %q", want)
		}
	}
	for _, forbidden := range []string{"id-token: write", "depot/", "--depot-builds", "depot-builds.json"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("CI health workflow retains retired Depot fragment %q", forbidden)
		}
	}
}

func TestLeapViewDeclaresGitHubHostedCIContract(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "depot.json")); !os.IsNotExist(err) {
		t.Fatalf("retired Depot project configuration still exists: %v", err)
	}
	for _, retired := range []string{
		"autback.json",
		"Dockerfile.autback",
		"Dockerfile.ci",
		filepath.Join("scripts", "run_ci_container.sh"),
		filepath.Join(".github", "workflows", "autback.yml"),
		filepath.Join("docs", "articles", "architecture", "autback.md"),
		filepath.Join("docs", "articles", "architecture", "self-hosted-ci.md"),
	} {
		if _, err := os.Stat(filepath.Join(root, retired)); !os.IsNotExist(err) {
			t.Fatalf("retired remote-execution integration remains at %s: %v", retired, err)
		}
	}
	packageJSON, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read package manifest: %v", err)
	}
	if !strings.Contains(string(packageJSON), `"typescript": "5.9.3"`) {
		t.Fatal("LeapView must pin the TypeScript compiler used by its remote test contract")
	}
	setup, err := os.ReadFile(filepath.Join(root, ".github", "actions", "setup-ci", "action.yml"))
	if err != nil {
		t.Fatalf("read GitHub-hosted CI setup action: %v", err)
	}
	setupText := string(setup)
	for _, want := range []string{
		"go-version-file: go.mod",
		`node-version: "24"`,
		"bun-version: 1.3.14",
		"terraform_version: 1.13.5",
		"github.com/go-task/task/v3/cmd/task@v3.50.0",
		"github.com/bufbuild/buf/cmd/buf@v1.57.2",
		"@playwright/test@1.61.1",
		"playwright install --with-deps chromium",
		"PLAYWRIGHT_BROWSERS_PATH=",
		"TF_PLUGIN_CACHE_DIR=",
	} {
		if !strings.Contains(setupText, want) {
			t.Fatalf("GitHub-hosted CI setup action missing %q", want)
		}
	}
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile: %v", err)
	}
	text := string(taskfile)
	for _, want := range []string{
		"ci:",
		"ci:pr:",
		"ci:full:",
		"ci:nightly:",
		"ci:local:",
		"- task: ci:pr",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Taskfile missing GitHub-hosted CI contract fragment %q", want)
		}
	}
	if strings.Contains(strings.ToLower(text), "autback") {
		t.Fatal("Taskfile retains Autback-specific execution syntax")
	}
	cutover, err := os.ReadFile(filepath.Join(root, "docs", "articles", "architecture", "github-hosted-ci.md"))
	if err != nil {
		t.Fatalf("read LeapView GitHub-hosted CI architecture: %v", err)
	}
	cutoverText := string(cutover)
	for _, want := range []string{
		"# GitHub-hosted CI",
		"ephemeral",
		"10 GB",
		"50 GB",
		"BuildKit",
		".github/actions/setup-ci",
		"task ci:pr",
		"task ci:full",
		"task ci:nightly",
	} {
		if !strings.Contains(cutoverText, want) {
			t.Fatalf("LeapView GitHub-hosted CI architecture missing %q", want)
		}
	}
	navigation, err := os.ReadFile(filepath.Join(root, "docs", "navigation.yaml"))
	if err != nil {
		t.Fatalf("read documentation navigation: %v", err)
	}
	for _, want := range []string{
		"slug: architecture/github-hosted-ci",
		"source: articles/architecture/github-hosted-ci.md",
	} {
		if !strings.Contains(string(navigation), want) {
			t.Fatalf("documentation navigation missing GitHub-hosted CI architecture fragment %q", want)
		}
	}
}

func TestFrontendScriptsDoNotRepeatedlyInstallPlaywright(t *testing.T) {
	root := repoRoot(t)
	packageJSON, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatalf("read package manifest: %v", err)
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(packageJSON, &manifest); err != nil {
		t.Fatalf("decode package manifest: %v", err)
	}
	if got := manifest.Scripts["browser:ensure"]; got != "node scripts/ensure_playwright.mjs" {
		t.Fatalf("browser:ensure must use the filesystem-first Playwright provisioner, got %q", got)
	}
	for name, command := range manifest.Scripts {
		if strings.Contains(command, "playwright install chromium") {
			t.Errorf("script %q repeatedly provisions Chromium instead of using browser:ensure", name)
		}
		if name != "browser:ensure" && strings.Contains(command, "bun run browser:ensure") {
			t.Errorf("script %q launches a redundant nested Bun process for Playwright readiness", name)
		}
	}
}

func TestPublicSiteBuildGeneratesIgnoredBrowserContracts(t *testing.T) {
	root := repoRoot(t)
	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}
	block := taskfileTaskBlock(t, string(taskfile), "site:build")
	for _, dependency := range []string{
		"- task: ui-signals:generate",
		"- task: visualization-ir:generate",
	} {
		if !strings.Contains(block, dependency) {
			t.Errorf("site:build must generate ignored browser contract %q in a clean checkout", dependency)
		}
	}
}

func workflowJobBlock(t *testing.T, workflow, job string) string {
	t.Helper()
	startMarker := "  " + job + ":"
	lines := strings.Split(workflow, "\n")
	start := -1
	for index, line := range lines {
		if line == startMarker {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("workflow job %q not found", job)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func taskfileTaskBlock(t *testing.T, taskfile, task string) string {
	t.Helper()
	startMarker := "  " + task + ":"
	lines := strings.Split(taskfile, "\n")
	start := -1
	for index, line := range lines {
		if line == startMarker {
			start = index
			break
		}
	}
	if start < 0 {
		t.Fatalf("Taskfile task %q not found", task)
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.HasSuffix(line, ":") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func TestSQLCOutputsAreGeneratedBuildInputs(t *testing.T) {
	root := repoRoot(t)
	sqlcConfig, err := os.ReadFile(filepath.Join(root, "sqlc.yaml"))
	if err != nil {
		t.Fatalf("read sqlc.yaml: %v", err)
	}
	var config struct {
		SQL []struct {
			Engine string `yaml:"engine"`
			Gen    struct {
				Go struct {
					Out        string `yaml:"out"`
					SQLPackage string `yaml:"sql_package"`
				} `yaml:"go"`
			} `yaml:"gen"`
		} `yaml:"sql"`
	}
	if err := yaml.Unmarshal(sqlcConfig, &config); err != nil {
		t.Fatalf("decode sqlc.yaml: %v", err)
	}
	var postgresOutputs []string
	for _, statement := range config.SQL {
		if statement.Engine != "postgresql" {
			continue
		}
		if statement.Gen.Go.SQLPackage != "pgx/v5" {
			t.Errorf("PostgreSQL sqlc output %q must use pgx/v5, got %q", statement.Gen.Go.Out, statement.Gen.Go.SQLPackage)
			continue
		}
		if statement.Gen.Go.Out == "" {
			t.Error("PostgreSQL sqlc statement has no generated output directory")
			continue
		}
		postgresOutputs = append(postgresOutputs, statement.Gen.Go.Out)
	}
	if len(postgresOutputs) == 0 {
		t.Fatal("sqlc.yaml has no PostgreSQL generated output directories")
	}

	files := map[string][]string{
		"Taskfile.yml": {
			"db:generate:",
			"GOTOOLCHAIN=go1.26.7 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote",
			"- task: db:generate",
		},
		".gitignore": {
			"internal/platform/db/db.go",
			"internal/platform/db/models.go",
			"internal/platform/db/*.sql.go",
			"internal/*/internal/db/",
			"internal/**/internal/db/",
			"internal/platform/**/sqlite/*db/",
		},
		".dockerignore": {
			"internal/platform/db/db.go",
			"internal/platform/db/models.go",
			"internal/platform/db/*.sql.go",
			"internal/*/internal/db/",
			"internal/**/internal/db/",
			"internal/platform/**/sqlite/*db/",
		},
		filepath.Join("scripts", "generate_build_sources.sh"): {
			"GOTOOLCHAIN=go1.26.7 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate --no-remote",
		},
		"Dockerfile": {
			"./scripts/generate_build_sources.sh",
			"FROM go-deps AS build",
		},
	}
	for name, fragments := range files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(body), fragment) {
				t.Errorf("%s missing sqlc generation contract fragment %q", name, fragment)
			}
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfileText := string(dockerfile)
	if strings.Contains(dockerfileText, "COPY --from=sourcegen /src/internal/platform/db") {
		t.Error("Dockerfile copies the SQLite platform fixture sqlc output into the production build")
	}
	for _, output := range postgresOutputs {
		copy := fmt.Sprintf("COPY --from=sourcegen /src/%s ./%s", output, output)
		if !strings.Contains(dockerfileText, copy) {
			t.Errorf("Dockerfile missing generated PostgreSQL sqlc output %s", output)
		}
	}
	for _, output := range []string{
		"internal/access/internal/db",
		"internal/agent/internal/db",
		"internal/dashboard/internal/db",
		"internal/manageddata/internal/db",
		"internal/refresh/internal/db",
		"internal/servingstate/internal/db",
		"internal/project/internal/db",
		"internal/platform/http/cursorsigning/sqlite/cursordb",
		"internal/platform/http/idempotency/sqlite/idempotencydb",
		"internal/platform/jobs/sqlite/jobdb",
	} {
		copy := fmt.Sprintf("COPY --from=sourcegen /src/%s ./%s", output, output)
		if strings.Contains(dockerfileText, copy) {
			t.Errorf("Dockerfile copies SQLite fixture sqlc output %s into the production build", output)
		}
	}
}

func TestPostgreSQLSQLCVerificationIsOfflineAndDatabaseBacked(t *testing.T) {
	root := repoRoot(t)
	files := map[string][]string{
		"Taskfile.yml": {
			"db:verify:",
			"vet --no-remote",
			"diff --no-remote",
			"db:prepare:",
			"LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true",
			"TestSQLCVetPreparesAgainstBaselinePostgreSQL18",
			"- task: db:prepare",
		},
		filepath.Join("internal", "app", "postgresbaseline", "sqlc_prepare_test.go"): {
			"postgresbaseline.Apply(ctx, tx)",
			"sqlc/db-prepare",
			"LEAPVIEW_SQLC_PREPARE_DATABASE_URL",
			"github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1",
			"PostgreSQL18",
		},
	}
	for name, fragments := range files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(body), fragment) {
				t.Errorf("%s missing sqlc verification contract fragment %q", name, fragment)
			}
		}
	}
}

func TestDerivedArtifactsAreGeneratedBuildInputs(t *testing.T) {
	root := repoRoot(t)
	files := map[string][]string{
		".gitignore": {
			"internal/app/config/config_gen.go",
			"internal/app/config/spec/names_gen.go",
			"web/generated/",
			"docs/catalog.json",
			"docs/search-index.json",
			"docs/configuration.md",
			"docs/api/*.md",
			"docs/api/operations.json",
			"docs/reference/cli/",
			"docs/reference/config/",
		},
		".dockerignore": {
			"internal/app/config/config_gen.go",
			"internal/app/config/spec/names_gen.go",
			"web/generated",
			"docs/catalog.json",
			"docs/search-index.json",
			"docs/configuration.md",
			"docs/api/*.md",
			"docs/api/operations.json",
			"docs/reference/cli",
			"docs/reference/config",
		},
		"Dockerfile.site": {
			"AS go-deps",
			"FROM go-deps AS sourcegen",
			"./scripts/generate_build_sources.sh",
			"go run -tags=duckdb_arrow ./internal/app/tools/ducklakeprepare",
			"go run ./internal/app/tools/clidocgen",
			"go run ./internal/app/tools/schemadocgen",
			"go run ./internal/app/tools/openapidocgen",
			"go run -tags=duckdb_arrow ./internal/app/tools/visualdocgen",
			"go run ./internal/app/tools/docsitegen",
			"FROM sourcegen AS build",
			"COPY --from=sourcegen /src/web/generated ./web/generated",
		},
		"Dockerfile": {
			"FROM go-deps AS build",
			"COPY --from=sourcegen /src/internal/app/config/config_gen.go ./internal/app/config/config_gen.go",
			"COPY --from=sourcegen /src/internal/app/config/spec/names_gen.go ./internal/app/config/spec/names_gen.go",
			"COPY --from=sourcegen /src/web/generated ./web/generated",
		},
		filepath.Join("scripts", "generate_build_sources.sh"): {
			"go run ./internal/app/tools/configgen",
		},
		"Taskfile.yml": {
			"ci:extensions:prepare:",
			"go run ./internal/app/tools/ducklakeprepare",
			"go run -tags=duckdb_arrow ./internal/app/tools/visualdocgen",
			"desc: Build the LeapView public site assets from generated contracts",
			"desc: Build the independently deployable public site from generated documentation",
			"desc: Start the public site from generated documentation on http://localhost:8081",
		},
	}
	for name, fragments := range files {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(string(body), fragment) {
				t.Errorf("%s missing generated-input contract fragment %q", name, fragment)
			}
		}
	}
	siteDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.site"))
	if err != nil {
		t.Fatalf("read Dockerfile.site: %v", err)
	}
	if count := strings.Count(string(siteDockerfile), "RUN go mod download"); count != 1 {
		t.Fatalf("Dockerfile.site downloads Go modules %d times, want one shared dependency stage", count)
	}
	const seededModuleCache = "type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked"
	if count := strings.Count(string(siteDockerfile), seededModuleCache); count != 3 {
		t.Fatalf("Dockerfile.site uses the seeded persistent Go module cache %d times, want source generation, visual documentation, and compilation", count)
	}

	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	require.NoError(t, err)
	if strings.Contains(string(gitignore), "!docs/reference/cli/manifest.json") {
		t.Error("generated CLI manifest must not be exempted from Git ignore rules")
	}

	taskfile, err := os.ReadFile(filepath.Join(root, "Taskfile.yml"))
	require.NoError(t, err)
	for _, generated := range []string{"docs/api/operations.json", "docs/reference/cli/manifest.json"} {
		if strings.Contains(generatedCheckCommand(string(taskfile)), generated) {
			t.Errorf("generated:check treats build-only artifact %q as a public snapshot", generated)
		}
	}
}

func TestArrowResponseContractDeclaresCursorTrailer(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "api", "typespec", "common.tsp"))
	require.NoError(t, err)
	contract := string(body)
	for _, fragment := range []string{
		`@extension("x-leapview-response-trailers", #["X-Next-Cursor"])`,
		`@header contentType: "application/vnd.apache.arrow.stream";`,
		`@header("X-Query-ID") queryId: string;`,
		`@header("X-Serving-Snapshot") servingSnapshot: string;`,
		`@header("X-LeapView-Arrow-Contract") arrowContract: "native-v1";`,
		`@header("Trailer") trailers: "X-Next-Cursor";`,
		`@header cacheControl: "no-store";`,
		`model GatewayTimeout`,
		`alias RowsetErrors = CommonErrors | GatewayTimeout;`,
	} {
		if !strings.Contains(contract, fragment) {
			t.Errorf("Arrow response contract missing native-v1 declaration %q", fragment)
		}
	}
	if strings.Contains(contract, `@header("X-Next-Cursor")`) {
		t.Error("Arrow response contract still advertises X-Next-Cursor as an initial header")
	}
	operations, err := os.ReadFile(filepath.Join(root, "api", "typespec", "bi.tsp"))
	require.NoError(t, err)
	if got := strings.Count(string(operations), `@extension("x-leapview-response-trailers", #["X-Next-Cursor"])`); got != 3 {
		t.Errorf("Arrow operation trailer declarations = %d, want 3", got)
	}
	if got := strings.Count(string(operations), `RowsetErrors;`); got != 3 {
		t.Errorf("Arrow operation timeout error declarations = %d, want 3", got)
	}
	openAPI, err := os.ReadFile(filepath.Join(root, "docs", "api", "openapi.yaml"))
	require.NoError(t, err)
	if got := strings.Count(string(openAPI), "x-leapview-response-trailers:"); got != 3 {
		t.Errorf("generated OpenAPI trailer declarations = %d, want 3", got)
	}
	for _, operationID := range []string{"queryDashboardVisualData", "previewSemanticDataset", "querySemanticModel"} {
		section := string(openAPI)
		start := strings.Index(section, "operationId: "+operationID)
		if start < 0 {
			t.Errorf("generated OpenAPI is missing operation %q", operationID)
			continue
		}
		section = section[start:]
		if end := strings.Index(section, "\n  /api/"); end >= 0 {
			section = section[:end]
		}
		if !strings.Contains(section, "        '504':") {
			t.Errorf("generated OpenAPI operation %q is missing the Arrow timeout response", operationID)
		}
	}
}

func workflowStep(workflow, startMarker, endMarker string) string {
	start := strings.Index(workflow, startMarker)
	if start < 0 {
		return ""
	}
	rest := workflow[start+len(startMarker):]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func generatedCheckCommand(taskfile string) string {
	start := strings.Index(taskfile, "  generated:check:")
	if start < 0 {
		return ""
	}
	rest := taskfile[start+len("  generated:check:"):]
	end := strings.Index(rest, "\n  api:generate:")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func TestFixedPlatformSQLiteQueriesUseSQLC(t *testing.T) {
	root := repoRoot(t)
	queryContracts := map[string][]string{
		filepath.Join("internal", "access", "sqlite", "queries", "authorization.sql"): {
			"-- name: InsertAuthorizationRoleBinding :exec",
			"-- name: InsertAuthorizationGrant :exec",
		},
		filepath.Join("internal", "platform", "db", "queries", "platform.sql"): {
			"-- name: InsertPlatformSettingIfMissing :exec",
		},
		filepath.Join("internal", "manageddata", "sqlite", "queries", "managed_data.sql"): {
			"-- name: ListManagedDataReachabilitySources :many",
		},
	}
	for name, markers := range queryContracts {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(body), marker) {
				t.Errorf("%s missing sqlc query %q", name, marker)
			}
		}
	}

	handwrittenSQL := map[string][]string{
		filepath.Join("internal", "platform", "store.go"): {
			"INSERT INTO platform_settings",
		},
	}
	for name, fragments := range handwrittenSQL {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(body), fragment) {
				t.Errorf("%s retains fixed-shape SQLite query %q instead of using sqlc", name, fragment)
			}
		}
	}
}

func TestAPIv1SQLiteAdaptersUseSQLC(t *testing.T) {
	packages := map[string]struct{}{
		"internal/platform/http/idempotency/sqlite":   {},
		"internal/jobs/sqlite":                        {},
		"internal/platform/http/cursorsigning/sqlite": {},
	}
	for _, file := range productionGoFiles(t) {
		if _, ok := packages[file.pkgDir]; !ok {
			continue
		}
		for _, forbidden := range []string{".ExecContext(", ".QueryContext(", ".QueryRowContext("} {
			if strings.Contains(file.body, forbidden) {
				t.Errorf("%s bypasses sqlc via %s", file.path, forbidden)
			}
		}
	}
}

func TestStorageArchitectureSpecDocumentsProcessOwnedDuckDB(t *testing.T) {
	root := repoRoot(t)
	spec, err := os.ReadFile(filepath.Join(root, "docs", "storage-architecture-spec.md"))
	if err != nil {
		t.Fatalf("read storage architecture spec: %v", err)
	}
	text := string(spec)
	for _, want := range []string{
		"Production deployments use one PostgreSQL control plane",
		"one process-owned DuckDB `DatabaseInstance`",
		"leapview.db               # local/evaluation SQLite control-plane fixture",
		"ducklake/catalog.duckdb   # local DuckDB-backed DuckLake metadata catalog",
		"Every physical relation in a serving plan",
		"AT (VERSION => 42)",
		"Runtime retirement closes generation-scoped cache state",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("storage architecture spec missing global catalog contract fragment %q", want)
		}
	}
	for _, forbidden := range []string{
		"ducklake/catalog.sqlite",
		"ducklake:sqlite:",
		"PostgreSQL as the server/multi-user DuckLake catalog backend",
		"one DuckDB file per semantic model",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("storage architecture spec still contains obsolete shared-catalog contract fragment %q", forbidden)
		}
	}
}

func TestAnalyticsModuleConstructsTheProcessDuckDBExactlyOnce(t *testing.T) {
	constructors := []string{}
	for _, file := range productionGoFiles(t) {
		if file.pkgDir == "internal/analytics/module" && strings.Contains(file.body, "analyticsducklake.Open(") {
			constructors = append(constructors, file.path)
		}
	}
	if len(constructors) != 1 {
		t.Fatalf("analytics module constructs DuckDB in %v, want exactly one constructor", constructors)
	}
	root := repoRoot(t)
	for _, path := range []string{
		"internal/app/runtimefactory/factory.go",
		"internal/analytics/duckdb/materialize.go",
		"internal/dashboard/analyticsruntime/factory.go",
		"internal/runtimehost/manager.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err)
		if strings.Contains(string(body), "analyticsducklake.Open(") || strings.Contains(string(body), "OpenSnapshot(") {
			t.Errorf("%s constructs a runtime-owned DuckDB instance", path)
		}
	}
}

func TestGovernedAnalyticalSessionBoundaryHasNoLegacyServingEscape(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"internal/analytics/ducklake/environment.go",
		"internal/dashboard/analyticsruntime/factory.go",
		"internal/dashboard/runtime/service.go",
		"internal/analytics/dataquery/query.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err)
		text := string(body)
		for _, forbidden := range []string{"func (e *Environment) SQLDB(", "OpenMaterializeRuntime", "OpenDashboardDataRuntime", "KindSourceRows"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s retains legacy analytical escape %q", path, forbidden)
			}
		}
	}
}

func TestCurrentConnectorRegistryIncludesQuackProduct(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"internal/analytics/connectors/registry.go",
		"internal/project/schema/contracts/contracts.cue",
		"schemas/json/connection.schema.json",
	} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err)
		if !strings.Contains(strings.ToLower(string(body)), "quack") {
			t.Errorf("%s does not expose Quack as a current connector", path)
		}
	}
}

func TestProductionUIDoesNotDependOnCDNScripts(t *testing.T) {
	root := repoRoot(t)
	forbiddenHosts := []string{"cdn.jsdelivr.net", "unpkg.com", "esm.sh", "skypack.dev"}

	for _, dir := range []string{"internal/dashboard/ui", "internal/app"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(body)
			for _, forbidden := range forbiddenHosts {
				if strings.Contains(text, forbidden) {
					rel, _ := filepath.Rel(root, path)
					t.Fatalf("%s references external script host %q; production UI assets must be served from /static", filepath.ToSlash(rel), forbidden)
				}
			}
			return nil
		})
		require.NoError(t, err)
	}

	staticFiles, err := filepath.Glob(filepath.Join(root, "static", "*.js"))
	require.NoError(t, err)
	for _, path := range staticFiles {
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		text := string(body)
		for _, forbidden := range forbiddenHosts {
			if strings.Contains(text, forbidden) {
				rel, _ := filepath.Rel(root, path)
				t.Fatalf("%s references external asset host %q; production bundles must be self-contained", filepath.ToSlash(rel), forbidden)
			}
		}
	}
}

func isSQLDBAllowedFile(file goFile) bool {
	if rule, ok := ClassifyPackage(file.pkgDir); ok && (rule.Layer == LayerComposition || rule.Layer == LayerModule) {
		return true
	}
	if file.pkgDir == "internal/app" {
		switch file.path {
		case "internal/app/build.go",
			"internal/app/server.go",
			"internal/app/publishes.go",
			"internal/app/refresh_runs.go",
			"internal/app/query_audit.go":
			return true
		default:
			return false
		}
	}
	if file.pkgDir == "internal/app/cli" ||
		file.pkgDir == "internal/integration" ||
		strings.HasPrefix(file.pkgDir, "internal/admin/storage") ||
		strings.HasPrefix(file.pkgDir, "internal/analytics/duckdb") ||
		strings.HasPrefix(file.pkgDir, "internal/analytics/ducklake") ||
		IsSQLiteFixtureFile(file.path) {
		return true
	}
	return false
}

func importListContains(imports []string, value string) bool {
	for _, imported := range imports {
		if imported == value || strings.Contains(imported, value) {
			return true
		}
	}
	return false
}

func hasPackagePrefix(packagePath string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if packagePath == prefix || strings.HasPrefix(packagePath, prefix+"/") {
			return true
		}
	}
	return false
}

func productionGoFiles(t *testing.T) []goFile {
	t.Helper()
	root := repoRoot(t)
	files := []goFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Self-contained tools may live in the monorepo while retaining their own
			// module and architecture. The LeapView package rules stop at that module
			// boundary just as the Go toolchain does.
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			switch entry.Name() {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			if filepath.Dir(path) == root {
				switch entry.Name() {
				case "static", "web", "dashboards":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		imports := make([]string, 0, len(parsed.Imports))
		for _, imported := range parsed.Imports {
			imports = append(imports, strings.Trim(imported.Path.Value, `"`))
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, goFile{
			path:    rel,
			pkgDir:  strings.TrimSuffix(rel, "/"+filepath.Base(rel)),
			imports: imports,
			body:    string(body),
		})
		return nil
	})
	require.NoError(t, err)
	return files
}

func packageDirExists(root, dir string) bool {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		return true
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			t.Fatal("go.mod not found")
		}
		dir = next
	}
}

func isInternalPackage(pkgDir string) bool {
	return pkgDir == "internal" || strings.HasPrefix(pkgDir, "internal/")
}

func isAdapterOrCompositionPackage(pkgDir string) bool {
	if rule, ok := ClassifyPackage(pkgDir); ok {
		switch rule.Layer {
		case LayerAdapter, LayerModule, LayerComposition, LayerPlatform:
			return true
		}
	}
	if pkgDir == "internal/app" ||
		pkgDir == "internal/app/cli" ||
		pkgDir == "internal/integration" ||
		pkgDir == "internal/platform" ||
		strings.HasPrefix(pkgDir, "internal/platform/") ||
		pkgDir == "internal/analytics/resource" ||
		pkgDir == "internal/access/oidc" ||
		pkgDir == "internal/access/httpauth" ||
		pkgDir == "internal/access/scimprov" ||
		pkgDir == "internal/admin/storage" ||
		pkgDir == "internal/agent/tools" ||
		strings.HasPrefix(pkgDir, "internal/app/tools/") ||
		strings.Contains(pkgDir, "/testing/") {
		return true
	}
	if strings.HasSuffix(pkgDir, "/module") {
		return true
	}
	for _, suffix := range []string{"/http", "/sqlite", "/filesystem", "/s3", "/tus", "/duckdb", "/ducklake", "/datastar", "/openai", "/ui"} {
		if strings.HasSuffix(pkgDir, suffix) || strings.Contains(pkgDir, suffix+"/") {
			return true
		}
	}
	return false
}

func isForbiddenUseCaseImport(imported string) bool {
	if imported == "net/http" ||
		imported == "database/sql" ||
		imported == "github.com/go-chi/chi/v5" ||
		strings.Contains(imported, "datastar") ||
		strings.Contains(imported, "gomponents") {
		return true
	}
	if imported == modulePath+"/internal/platform/db" {
		return true
	}
	if !strings.HasPrefix(imported, modulePath+"/internal/") {
		return false
	}
	packagePath := strings.TrimPrefix(imported, modulePath+"/")
	if rule, ok := ClassifyPackage(packagePath); ok && rule.Layer == LayerPlatform {
		return false
	}
	for _, segment := range []string{"/sqlite", "/filesystem", "/s3", "/tus", "/duckdb", "/ducklake", "/datastar", "/http", "/openai"} {
		if strings.Contains(packagePath, segment) {
			return true
		}
	}
	return false
}
