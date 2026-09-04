package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"
)

func TestDBTWarehouseBoundaryDoesNotEnterLeapViewRuntime(t *testing.T) {
	root := repoRoot(t)
	goModPath := filepath.Join(root, "go.mod")
	goModBody, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatal(err)
	}
	goMod, err := modfile.Parse(goModPath, goModBody, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}
	for _, required := range goMod.Require {
		if isDBTModulePath(required.Mod.Path) {
			t.Errorf("go.mod requires dbt module %q", required.Mod.Path)
		}
	}
	for _, replaced := range goMod.Replace {
		for _, modulePath := range []string{replaced.Old.Path, replaced.New.Path} {
			if isDBTModulePath(modulePath) {
				t.Errorf("go.mod replaces dbt module with %q", modulePath)
			}
		}
	}
	for _, excluded := range goMod.Exclude {
		if isDBTModulePath(excluded.Mod.Path) {
			t.Errorf("go.mod excludes dbt module %q", excluded.Mod.Path)
		}
	}

	goSumPath := filepath.Join(root, "go.sum")
	goSumBody, err := os.ReadFile(goSumPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(goSumBody), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && isDBTModulePath(fields[0]) {
			t.Errorf("go.sum contains a dbt module dependency: %s", line)
		}
	}

	for _, file := range productionGoFiles(t) {
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), file.path, file.body, 0)
		if parseErr != nil {
			t.Fatalf("parse production Go file %s: %v", file.path, parseErr)
		}
		for _, imported := range parsed.Imports {
			importPath, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("parse import path in %s: %v", file.path, unquoteErr)
			}
			if isDBTModulePath(importPath) {
				t.Errorf("production Go file %s imports dbt-specific package %q", file.path, importPath)
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.Ident:
				if isDBTIdentifier(node.Name) {
					t.Errorf("production Go file %s uses dbt-specific identifier %q", file.path, node.Name)
				}
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				literal := node.Value
				if value, unquoteErr := strconv.Unquote(literal); unquoteErr == nil {
					literal = value
				}
				if isDBTText(literal) {
					t.Errorf("production Go file %s contains dbt-specific string literal %q", file.path, literal)
				}
			}
			return true
		})
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

func isDBTModulePath(path string) bool {
	return isDBTText(path)
}

func isDBTText(value string) bool {
	lower := strings.ToLower(value)
	for {
		index := strings.Index(lower, "dbt")
		if index < 0 {
			return false
		}
		end := index + len("dbt")
		beforeIsBoundary := index == 0 || !isASCIIAlphaNumeric(lower[index-1])
		afterIsBoundary := end == len(lower) || !isASCIIAlphaNumeric(lower[end])
		if beforeIsBoundary && afterIsBoundary {
			return true
		}
		lower = lower[end:]
	}
}

func isDBTIdentifier(identifier string) bool {
	if identifier == "DBTX" {
		return false
	}
	if isDBTText(identifier) {
		return true
	}
	lower := strings.ToLower(identifier)
	if !strings.HasPrefix(lower, "dbt") {
		return false
	}
	suffix := identifier[len("dbt"):]
	if suffix == "" {
		return true
	}
	first := suffix[0]
	return first == '_' || first == '-' || (first >= 'A' && first <= 'Z')
}

func isASCIIAlphaNumeric(value byte) bool {
	return (value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

type dbtBoundaryWorkflow struct {
	Permissions map[string]string         `yaml:"permissions"`
	Env         map[string]string         `yaml:"env"`
	Jobs        map[string]dbtBoundaryJob `yaml:"jobs"`
}

type dbtBoundaryJob struct {
	If          string                    `yaml:"if"`
	Environment string                    `yaml:"environment"`
	Permissions map[string]string         `yaml:"permissions"`
	Needs       []string                  `yaml:"needs"`
	Outputs     map[string]string         `yaml:"outputs"`
	Env         map[string]string         `yaml:"env"`
	Steps       []dbtBoundaryWorkflowStep `yaml:"steps"`
}

type dbtBoundaryWorkflowStep struct {
	ID   string                 `yaml:"id"`
	Name string                 `yaml:"name"`
	Uses string                 `yaml:"uses"`
	Run  string                 `yaml:"run"`
	Env  map[string]string      `yaml:"env"`
	With map[string]interface{} `yaml:"with"`
}

func TestDBTWarehouseBoundaryWorkflowPublishesBeforeLeapView(t *testing.T) {
	root := repoRoot(t)
	workflow := readDBTBoundaryWorkflow(t, filepath.Join(root, ".github/workflows/dbt-warehouse-boundary-reference.yml"))
	producer, ok := workflow.Jobs["publish-dbt"]
	if !ok {
		t.Fatal("reference workflow is missing publish-dbt job")
	}
	activation, ok := workflow.Jobs["activate-leapview"]
	if !ok {
		t.Fatal("reference workflow is missing activate-leapview job")
	}

	if workflow.Permissions["contents"] != "read" {
		t.Fatalf("reference workflow top-level contents permission = %q, want read", workflow.Permissions["contents"])
	}
	if _, granted := workflow.Permissions["id-token"]; granted {
		t.Fatal("reference workflow must not grant top-level id-token permission")
	}
	assertDBTBoundaryTrustedJob(t, "publish-dbt", producer)
	assertDBTBoundaryTrustedJob(t, "activate-leapview", activation)
	if producer.Permissions["contents"] != "read" || producer.Permissions["id-token"] != "write" {
		t.Fatalf("producer job permissions = %#v, want contents: read and id-token: write", producer.Permissions)
	}
	if activation.Permissions["contents"] != "read" {
		t.Fatalf("activation job contents permission = %q, want read", activation.Permissions["contents"])
	}
	if _, granted := activation.Permissions["id-token"]; granted {
		t.Fatal("activation job must not receive the producer OIDC permission")
	}
	if !containsString(activation.Needs, "publish-dbt") {
		t.Fatalf("activation job needs = %v, want publish-dbt", activation.Needs)
	}
	if producer.Outputs["publication_prefix"] != "${{ steps.select-prefix.outputs.publication_prefix }}" {
		t.Fatalf("producer publication output = %q", producer.Outputs["publication_prefix"])
	}
	if activation.Env["PUBLICATION_PREFIX"] != "${{ needs.publish-dbt.outputs.publication_prefix }}" {
		t.Fatalf("activation publication input = %q", activation.Env["PUBLICATION_PREFIX"])
	}
	if producer.Env["AZURE_CORE_OUTPUT"] != "none" || activation.Env["AZURE_CORE_OUTPUT"] != "none" {
		t.Fatal("both workflow jobs must suppress Azure response output")
	}

	producerOrder := []string{
		"Set up pinned Python",
		"Install pinned dbt Core",
		"Install pinned dbt-duckdb",
		"Read the bounded producer inputs",
		"Run dbt build and verify physical Parquet",
		"Select a new immutable publication prefix",
		"Upload the complete selected mart set",
		"Verify the exact published mart set",
		"End the producer credential session",
	}
	previous := -1
	for _, name := range producerOrder {
		index := dbtBoundaryStepIndex(producer.Steps, name)
		if index < 0 {
			t.Fatalf("producer job is missing %q", name)
		}
		if index <= previous {
			t.Fatalf("producer step %q is out of order", name)
		}
		previous = index
	}
	activationOrder := []string{
		"Render the ordinary Azure-backed LeapView bundle",
		"Assert authored current-profile Project identity",
		"Set up the cached LeapView CI toolchain",
		"Build the ordinary LeapView CLI",
		"Qualify the physical publication through LeapView",
	}
	previous = -1
	for _, name := range activationOrder {
		index := dbtBoundaryStepIndex(activation.Steps, name)
		if index < 0 {
			t.Fatalf("activation job is missing %q", name)
		}
		if index <= previous {
			t.Fatalf("activation step %q is out of order", name)
		}
		previous = index
	}

	producerLogin := dbtBoundaryStepByName(t, producer.Steps, "Authenticate the producer with Azure OIDC")
	if !strings.HasPrefix(producerLogin.Uses, "azure/login@") {
		t.Errorf("producer authentication uses %q, want azure/login action", producerLogin.Uses)
	}
	for key, value := range map[string]string{
		"client-id":       "${{ vars.DBT_PRODUCER_CLIENT_ID }}",
		"tenant-id":       "${{ vars.AZURE_TENANT_ID }}",
		"subscription-id": "${{ vars.AZURE_SUBSCRIPTION_ID }}",
	} {
		if producerLogin.With[key] != value {
			t.Errorf("producer authentication %s = %#v, want %q", key, producerLogin.With[key], value)
		}
	}

	prefix := dbtBoundaryStepByName(t, producer.Steps, "Select a new immutable publication prefix")
	if prefix.ID != "select-prefix" {
		t.Fatalf("publication-prefix step id = %q, want select-prefix", prefix.ID)
	}
	for _, required := range []string{
		`prefix="dbt-warehouse-boundary/${GITHUB_REPOSITORY}/${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}-${GITHUB_SHA}"`,
		`--prefix "$prefix/"`,
		"--query 'length(@)'",
		`test "$existing" = 0`,
		`printf 'publication_prefix=%s\n' "$prefix" >> "$GITHUB_OUTPUT"`,
	} {
		if !strings.Contains(prefix.Run, required) {
			t.Errorf("publication-prefix step is missing %q", required)
		}
	}
	upload := dbtBoundaryStepByName(t, producer.Steps, "Upload the complete selected mart set")
	for _, required := range []string{`--name "$PUBLICATION_PREFIX/$mart"`, "--overwrite false"} {
		if !strings.Contains(upload.Run, required) {
			t.Errorf("publication upload step is missing %q", required)
		}
	}
	verify := dbtBoundaryStepByName(t, producer.Steps, "Verify the exact published mart set")
	for _, required := range []string{
		`--prefix "$PUBLICATION_PREFIX/"`,
		"--query '[].name'",
		`test "$actual" = "$expected"`,
		`"$PUBLICATION_PREFIX/dim_customers.parquet"`,
		`"$PUBLICATION_PREFIX/fct_orders.parquet"`,
	} {
		if !strings.Contains(verify.Run, required) {
			t.Errorf("exact-publication verification step is missing %q", required)
		}
	}

	logout := dbtBoundaryStepByName(t, producer.Steps, "End the producer credential session")
	if strings.TrimSpace(logout.Run) != "az logout" {
		t.Errorf("producer logout command = %q, want az logout", logout.Run)
	}
	for _, step := range producer.Steps {
		if dbtBoundaryStepContains(step, "${{ secrets.") {
			t.Errorf("producer step %q receives a LeapView production secret", step.Name)
		}
	}

	for _, step := range activation.Steps {
		if strings.HasPrefix(step.Uses, "azure/login@") || strings.Contains(step.Run, "az login") {
			t.Errorf("activation step %q can authenticate as the producer", step.Name)
		}
	}
	assertProject := dbtBoundaryStepByName(t, activation.Steps, "Assert authored current-profile Project identity")
	for _, required := range []string{
		`"$LEAPVIEW_PROJECT_FILE"`,
		"kind: Project",
		"metadata:",
		"LEAPVIEW_WORKLOAD_PROJECT",
		"test \"$project_id\"",
	} {
		if !strings.Contains(assertProject.Run, required) {
			t.Errorf("Project identity assertion step is missing %q", required)
		}
	}
	render := dbtBoundaryStepByName(t, activation.Steps, "Render the ordinary Azure-backed LeapView bundle")
	if !strings.Contains(render.Run, "type: azure_blob") {
		t.Fatal("rendered LeapView bundle does not select the ordinary azure_blob connection")
	}

	qualify := dbtBoundaryStepByName(t, activation.Steps, "Qualify the physical publication through LeapView")
	if len(qualify.Env) != 2 {
		t.Fatalf("LeapView credential step env = %#v, want exactly two workload credentials", qualify.Env)
	}
	for key, value := range map[string]string{
		"LEAPVIEW_WORKLOAD_CLIENT_ID":     "${{ secrets.LEAPVIEW_WORKLOAD_CLIENT_ID }}",
		"LEAPVIEW_WORKLOAD_CLIENT_SECRET": "${{ secrets.LEAPVIEW_WORKLOAD_CLIENT_SECRET }}",
	} {
		if qualify.Env[key] != value {
			t.Errorf("LeapView credential step env %s = %q, want secret-scoped value", key, qualify.Env[key])
		}
	}
	for _, step := range activation.Steps {
		if step.Name == qualify.Name {
			continue
		}
		if dbtBoundaryStepContains(step, "${{ secrets.") {
			t.Errorf("step %q receives a production secret outside the LeapView credential step", step.Name)
		}
	}

	for _, required := range []string{
		`"$RUNNER_TEMP/leapview" dev --once --no-browser --format json`,
		`--candidate-key "$candidate_key"`,
		"--source-repository",
		"--source-ref",
		"--source-revision",
		".candidateId",
		`"$RUNNER_TEMP/leapview" publish "$candidate_id" --format json`,
	} {
		if !strings.Contains(qualify.Run, required) {
			t.Errorf("LeapView qualification step is missing %q", required)
		}
	}
	for jobName, job := range map[string]dbtBoundaryJob{"publish-dbt": producer, "activate-leapview": activation} {
		for _, step := range job.Steps {
			if dbtBoundaryStepContains(step, "manifest.json") || dbtBoundaryStepContains(step, "run_results.json") || dbtBoundaryStepContains(step, "dbt parse") || dbtBoundaryStepContains(step, "dbt ls") {
				t.Errorf("reference workflow treats dbt metadata as authority through %s step %q", jobName, step.Name)
			}
		}
	}
}

func assertDBTBoundaryTrustedJob(t *testing.T, name string, job dbtBoundaryJob) {
	t.Helper()
	guard := strings.Join(strings.Fields(job.If), " ")
	for _, required := range []string{
		"github.ref == format('refs/heads/{0}', github.event.repository.default_branch)",
		"github.ref_protected == true",
	} {
		if !strings.Contains(guard, required) {
			t.Errorf("%s trusted-ref guard is missing %q: %q", name, required, job.If)
		}
	}
	if strings.Contains(guard, "||") {
		t.Errorf("%s trusted-ref guard is not fail-closed: %q", name, job.If)
	}
	if job.Environment != "dbt-warehouse-boundary-production" {
		t.Errorf("%s environment = %q, want dbt-warehouse-boundary-production", name, job.Environment)
	}
}

func TestDBTWarehouseBoundaryWorkflowIsRequiredByCIGate(t *testing.T) {
	root := repoRoot(t)
	workflow := readDBTBoundaryWorkflow(t, filepath.Join(root, ".github/workflows/ci.yml"))
	dbtJob, ok := workflow.Jobs["dbt-warehouse-boundary-validation"]
	if !ok {
		t.Fatal("CI workflow is missing dbt-warehouse-boundary-validation job")
	}
	qualifyFound := false
	for _, step := range dbtJob.Steps {
		if strings.Contains(step.Run, "task dbt:warehouse:qualify") {
			qualifyFound = true
			break
		}
	}
	if !qualifyFound {
		t.Fatal("CI dbt validation job does not run task dbt:warehouse:qualify")
	}

	gate, ok := workflow.Jobs["ci-gate"]
	if !ok {
		t.Fatal("CI workflow is missing ci-gate job")
	}
	if !containsString(gate.Needs, "dbt-warehouse-boundary-validation") {
		t.Fatalf("CI gate needs = %v, missing dbt validation job", gate.Needs)
	}
	for _, step := range gate.Steps {
		if step.Env["DBT_WAREHOUSE_RESULT"] == "${{ needs.dbt-warehouse-boundary-validation.result }}" {
			if !strings.Contains(step.Run, "DBT_WAREHOUSE_RESULT") {
				t.Fatal("CI gate does not fail closed on DBT_WAREHOUSE_RESULT")
			}
			return
		}
	}
	t.Fatal("CI gate does not wire DBT_WAREHOUSE_RESULT from the dbt validation job")
}

func TestDBTWarehouseBoundaryCIJobUsesTheTieredWorkflow(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"dbt-warehouse-boundary-validation:",
		"name: dbt physical contract (PR)",
		"needs: [apigen-validation, go-packages-validation, go-application-validation, frontend-validation, spatial-tile-benchmarks, dbt-warehouse-boundary-validation]",
		"DBT_WAREHOUSE_RESULT: ${{ needs.dbt-warehouse-boundary-validation.result }}",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("CI workflow missing dbt validation fragment %q", want)
		}
	}
	dbtWarehouseCI := workflowJobBlock(t, text, "dbt-warehouse-boundary-validation")
	for _, want := range []string{
		"github.event_name == 'workflow_dispatch'",
		"github.event.pull_request.stack == null",
		"github.event.pull_request.stack.position == github.event.pull_request.stack.size",
	} {
		if !strings.Contains(dbtWarehouseCI, want) {
			t.Fatalf("dbt validation job is not limited to standalone pull requests and stack tips: missing %q", want)
		}
	}
}

func readDBTBoundaryWorkflow(t *testing.T, path string) dbtBoundaryWorkflow {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var workflow dbtBoundaryWorkflow
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatalf("parse workflow %s: %v", path, err)
	}
	return workflow
}

func dbtBoundaryStepByName(t *testing.T, steps []dbtBoundaryWorkflowStep, name string) dbtBoundaryWorkflowStep {
	t.Helper()
	for _, step := range steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("workflow is missing step %q", name)
	return dbtBoundaryWorkflowStep{}
}

func dbtBoundaryStepIndex(steps []dbtBoundaryWorkflowStep, name string) int {
	for index, step := range steps {
		if step.Name == name {
			return index
		}
	}
	return -1
}

func dbtBoundaryStepContains(step dbtBoundaryWorkflowStep, needle string) bool {
	if strings.Contains(step.Run, needle) || strings.Contains(step.Uses, needle) {
		return true
	}
	for _, value := range step.Env {
		if strings.Contains(value, needle) {
			return true
		}
	}
	for _, value := range step.With {
		if strings.Contains(toString(value), needle) {
			return true
		}
	}
	return false
}

func toString(value interface{}) string {
	if stringValue, ok := value.(string); ok {
		return stringValue
	}
	return ""
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
