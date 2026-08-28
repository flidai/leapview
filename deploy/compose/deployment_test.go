package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"
)

var (
	generateConfigOnce   sync.Once
	generateConfigOutput []byte
	generateConfigError  error
)

const configValidatorTestProgram = `package configvalidator

import (
	"testing"

	"github.com/flidai/leapview/internal/app/config"
)

func TestProductionConfigValidator(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Production = true
	if err := cfg.Validate(config.ProfileServe); err != nil {
		t.Fatal(err)
	}
}
`

func TestComposeSingleInstanceContract(t *testing.T) {
	compose := read(t, "compose.yaml")
	for _, required := range []string{
		"leapview-state:/var/lib/leapview",
		"${COMPOSE_APP_BIND:-127.0.0.1:8080}:8080",
		"read_only: true",
		"cap_drop: [ALL]",
		"stop_grace_period: 2m",
		"type: tmpfs",
		"target: /tmp",
		"size: 536870912",
		"mode: 01777",
		"LEAPVIEW_IMAGE: ${LEAPVIEW_IMAGE:?set LEAPVIEW_IMAGE in deployment.env}",
		"./release-transition-policy.json:/run/leapview/release-transition-policy.json:ro",
	} {
		if !strings.Contains(compose, required) {
			t.Fatalf("compose.yaml missing %q", required)
		}
	}
	if strings.Contains(compose, "container_name:") {
		t.Fatal("generic Compose must allow independent project names on one host")
	}
	if strings.Contains(compose, "./backups:/backups") {
		t.Fatal("backup archives must cross the container boundary as streams, not through a host bind with incompatible ownership")
	}
	if strings.Contains(compose, "/tmp:rw,noexec") {
		t.Fatal("tmpfs short syntax is rejected by Docker Desktop when its options are interpreted as mount paths")
	}
	appEnvironment := read(t, "leapview.env.example")
	for _, required := range []string{
		"LEAPVIEW_HOME=/var/lib/leapview/home",
		"LEAPVIEW_PUBLIC_URL=https://dash.example.com",
		"LEAPVIEW_ALLOWED_HOSTS=dash.example.com",
		"LEAPVIEW_TRUST_PROXY_HEADERS=true",
	} {
		if !strings.Contains(appEnvironment, required) {
			t.Fatalf("leapview.env.example missing %q", required)
		}
	}
	https := read(t, "compose.https.yaml")
	if !strings.Contains(https, "CADDY_IMAGE") ||
		!strings.Contains(https, "${CADDY_HTTP_BIND:-80}:80") ||
		!strings.Contains(https, "${CADDY_HTTPS_BIND:-443}:443") ||
		!strings.Contains(https, "${CADDY_HTTPS_UDP_BIND:-443}:443/udp") {
		t.Fatal("HTTPS overlay is incomplete")
	}
	deploymentEnvironment := read(t, "deployment.env.example")
	for _, required := range []string{"CADDY_HTTP_BIND=80", "CADDY_HTTPS_BIND=443", "CADDY_HTTPS_UDP_BIND=443"} {
		if !strings.Contains(deploymentEnvironment, required) {
			t.Errorf("deployment.env.example missing %q", required)
		}
	}
	for _, required := range []string{"type: tmpfs", "target: /tmp", "size: 67108864", "mode: 01777"} {
		if !strings.Contains(https, required) {
			t.Errorf("compose.https.yaml missing %q", required)
		}
	}
	if strings.Contains(https, "/tmp:rw,noexec") {
		t.Fatal("Caddy tmpfs short syntax is rejected by Docker Desktop when its options are interpreted as mount paths")
	}
}

func TestPublicImageIsPrimaryOnboardingContract(t *testing.T) {
	publicReleaseImage := readPublicReleaseImage(t)
	release := read(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	for _, required := range []string{
		"IMAGE_NAME: ghcr.io/flidai/leapview",
		"runner: ubuntu-24.04",
		"runner: ubuntu-24.04-arm",
		"platforms: linux/${{ matrix.arch }}",
		`--tag "${IMAGE_NAME}:latest"`,
		"docker buildx imagetools create",
		"Verify anonymous image pull",
		"docker logout ghcr.io",
		"docker buildx imagetools inspect",
	} {
		if !strings.Contains(release, required) {
			t.Fatalf("release workflow missing public image contract %q", required)
		}
	}
	if strings.Contains(release, "docker/setup-qemu-action@") {
		t.Fatal("release workflow must build each public architecture on its native runner")
	}

	documents := []struct {
		name     string
		image    string
		required []string
	}{
		{
			name:  filepath.Join("..", "..", "README.md"),
			image: "ghcr.io/flidai/leapview:latest",
			required: []string{
				"ghcr.io/flidai/leapview:latest",
				"docker pull",
			},
		},
		{
			name:  filepath.Join("..", "..", "docs", "articles", "start", "installation.md"),
			image: publicReleaseImage,
			required: []string{
				publicReleaseImage,
				"docker pull",
				"admin initialize --format json",
			},
		},
	}
	for _, contract := range documents {
		name := contract.name
		document := read(t, name)
		for _, required := range contract.required {
			if !strings.Contains(document, required) {
				t.Errorf("%s does not document public image onboarding contract %q", name, required)
			}
		}
		image := strings.Index(document, contract.image)
		controller := strings.Index(document, "./leapviewctl init")
		if controller >= 0 && image > controller {
			t.Errorf("%s presents the operations controller before the public image", name)
		}
	}
}

func TestProductionImageCarriesPinnedOfflineExtensionSupply(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile := read(t, filepath.Join(root, "Dockerfile"))
	for _, required := range []string{
		"FROM build AS extension-supply",
		"./internal/app/tools/extensionsupply",
		"COPY --from=extension-supply /out/extension-supply /usr/local/share/leapview/extensions",
		"LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH=/usr/local/share/leapview/extensions/extension-supply.json",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing offline extension supply contract %q", required)
		}
	}
	tool := read(t, filepath.Join(root, "internal", "app", "tools", "extensionsupply", "main.go"))
	for _, required := range []string{"SET autoinstall_known_extensions = false", "SET autoload_known_extensions = false", `"LOAD '"+escaped+"'"`} {
		if !strings.Contains(tool, required) {
			t.Fatalf("extension supply builder missing exact-path verification contract %q", required)
		}
	}
	compose := read(t, "compose.yaml")
	if strings.Contains(compose, "DUCKDB_EXTENSION_SUPPLY") {
		t.Fatal("standard Compose must not expose extension supply selection as authored env")
	}
	release := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for _, required := range []string{"Verify target-native offline extension supply", "extension-supply.json.sha256", "duckdb_extension"} {
		if !strings.Contains(release, required) {
			t.Fatalf("release qualification missing extension supply check %q", required)
		}
	}
}

func TestFiveMinuteEvaluationContract(t *testing.T) {
	root := filepath.Join("..", "..")
	publicReleaseImage := readPublicReleaseImage(t)
	dockerfile := read(t, filepath.Join(root, "Dockerfile"))
	if !strings.Contains(dockerfile, "COPY evaluation ./evaluation") {
		t.Fatal("runtime image does not include the self-contained evaluation project and data")
	}
	dashboard := read(t, filepath.Join(root, "evaluation", "project", "dashboards", "sales-overview.yaml"))
	for _, required := range []string{
		"type: static",
		"value: {type: string, value: SP}",
		"value: {type: string, value: RJ}",
		"value: {type: string, value: MG}",
		"value: {type: string, value: PR}",
	} {
		if !strings.Contains(dashboard, required) {
			t.Errorf("five-minute evaluation dashboard missing deterministic state option %q", required)
		}
	}
	ordersModel := read(t, filepath.Join(root, "evaluation", "project", "models", "orders.yaml"))
	if !strings.Contains(ordersModel, "try_cast(revenue AS DECIMAL(18, 2)) AS revenue") {
		t.Fatal("five-minute evaluation revenue transform must produce the authored Decimal physical type")
	}
	for _, contract := range []struct {
		name       string
		imageRun   string
		imageSetup string
	}{
		{
			name:     filepath.Join(root, "README.md"),
			imageRun: "ghcr.io/flidai/leapview:latest evaluate",
		},
		{
			name:       filepath.Join(root, "docs", "articles", "start", "installation.md"),
			imageSetup: "IMAGE='" + publicReleaseImage + "'",
			imageRun:   `"$IMAGE" evaluate`,
		},
	} {
		document := read(t, contract.name)
		for _, required := range []string{
			contract.imageSetup,
			"--name leapview-evaluate",
			"--publish 127.0.0.1:8080:8080",
			"--volume leapview-evaluate:/var/lib/leapview",
			contract.imageRun,
			"docker exec leapview-evaluate leapview evaluate first-login",
			"docker rm --force leapview-evaluate",
			"docker volume rm leapview-evaluate",
			"Five-minute Sales Evaluation",
			"no source checkout",
		} {
			if required == "" {
				continue
			}
			if !strings.Contains(document, required) {
				t.Errorf("%s missing five-minute evaluation contract %q", contract.name, required)
			}
		}
	}
	installation := read(
		t,
		filepath.Join(
			root,
			"docs",
			"articles",
			"start",
			"installation.md",
		),
	)
	for _, required := range []string{
		"--name leapview-evaluate-2",
		"--publish 127.0.0.1:8081:8081",
		"--volume leapview-evaluate-2:/var/lib/leapview",
		"evaluate --port 8081",
		"leapview login http://localhost:8080",
		"leapview dev --once",
		"leapview publish",
	} {
		if !strings.Contains(installation, required) {
			t.Errorf(
				"installation guide missing isolated evaluation target contract %q",
				required,
			)
		}
	}
}

func readPublicReleaseImage(t *testing.T) string {
	t.Helper()

	manifest := read(t, filepath.Join("..", "..", "docs", "public-release.json"))
	var release struct {
		Image string `json:"image"`
	}
	if err := json.Unmarshal([]byte(manifest), &release); err != nil {
		t.Fatalf("parse public release manifest: %v", err)
	}
	if release.Image == "" {
		t.Fatal("public release manifest has no image")
	}
	return release.Image
}

func TestQualificationRunbookMatchesExecutablePerformancePolicy(t *testing.T) {
	root := filepath.Join("..", "..")
	policyJSON := read(t, filepath.Join(root, "deploy", "compose", "qualification", "performance-policy.json"))
	runbook := read(t, filepath.Join(root, "deploy", "compose", "QUALIFICATION.md"))

	var policy struct {
		Budgets map[string]float64 `json:"budgets"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		t.Fatalf("parse performance policy: %v", err)
	}

	type documentedBudget struct {
		key         string
		measurement string
		format      func(float64) string
	}
	seconds := func(milliseconds float64) string {
		return fmt.Sprintf("%g s", milliseconds/1000)
	}
	number := func(value float64) string {
		return fmt.Sprintf("%g", value)
	}
	bytes := func(unit float64, suffix string) func(float64) string {
		return func(value float64) string {
			return fmt.Sprintf("%g %s", value/unit, suffix)
		}
	}

	documented := []documentedBudget{
		{key: "coldDashboardReadyP95Ms", measurement: "Restart-cold dashboard readiness p95", format: seconds},
		{key: "warmDashboardReadyP95Ms", measurement: "Warm dashboard readiness p95", format: seconds},
		{key: "filterToSettleP95Ms", measurement: "Filter-to-settle p95", format: seconds},
		{key: "tableInteractionP95Ms", measurement: "Governed table-sort interaction p95", format: seconds},
		{key: "governedQueryP95Ms", measurement: "Governed query p95", format: seconds},
		{key: "refreshP95Ms", measurement: "Refresh/materialization p95", format: seconds},
		{key: "concurrentQueryP95Ms", measurement: "Eight-reader governed-query p95", format: seconds},
		{key: "errorRateMax", measurement: "Controlled-request error rate", format: number},
		{key: "peakResidentMemoryBytes", measurement: "Peak resident memory", format: bytes(1<<30, "GiB")},
		{key: "cpuSecondsMax", measurement: "Measured workload CPU", format: func(value float64) string {
			return fmt.Sprintf("%g CPU-seconds", value)
		}},
		{key: "temporaryDiskGrowthBytesMax", measurement: "Temporary state growth", format: bytes(1<<20, "MiB")},
		{key: "goroutineGrowthMax", measurement: "Steady-state goroutine growth", format: number},
		{key: "openConnectionsMax", measurement: "Peak open DuckDB connections", format: number},
	}

	if len(policy.Budgets) != len(documented) {
		t.Errorf("performance policy has %d budgets, but the runbook contract documents %d", len(policy.Budgets), len(documented))
	}
	documentedKeys := make(map[string]struct{}, len(documented))
	for _, budget := range documented {
		documentedKeys[budget.key] = struct{}{}
		value, ok := policy.Budgets[budget.key]
		if !ok {
			t.Errorf("performance policy missing budget %q", budget.key)
			continue
		}
		row := fmt.Sprintf("| %s | %s |", budget.measurement, budget.format(value))
		if count := strings.Count(runbook, row); count != 1 {
			t.Errorf("QUALIFICATION.md must contain exactly one policy-derived row %q; found %d", row, count)
		}
	}
	for key := range policy.Budgets {
		if _, ok := documentedKeys[key]; !ok {
			t.Errorf("performance policy budget %q has no runbook formatter", key)
		}
	}
}

func TestInstalledCandidateQualificationContract(t *testing.T) {
	root := filepath.Join("..", "..")
	release := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	workflow := read(t, filepath.Join(root, ".github", "workflows", "installed-candidate.yml"))
	installed := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_installed.go"))
	imageQualification := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_image.go"))
	recovery := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_recovery.go"))
	browser := read(t, filepath.Join(root, "deploy", "compose", "qualification", "browser.mjs"))
	authoringWorker := read(t, filepath.Join(root, "deploy", "compose", "qualification", "authoring-worker.mjs"))
	performance := read(t, filepath.Join(root, "deploy", "compose", "qualification", "performance.mjs"))
	performancePolicy := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_performance.go"))
	runtimeQualification := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_image_runtime.go"))
	runbook := read(t, filepath.Join(root, "deploy", "compose", "QUALIFICATION.md"))

	for _, required := range []string{"cp -R deploy/compose/qualification", "args=(qualify installed-candidate", "--require-release-transition", "test -n \"$previous_image\"", "transition-qualification.json", "gh release create", "needs: [image, qualify, minio-conformance, plan-gc-conformance]"} {
		if !strings.Contains(release, required) {
			t.Errorf("release workflow missing %q", required)
		}
	}
	gate := strings.Index(release, "args=(qualify installed-candidate")
	if gate < 0 || gate > strings.Index(release, "gh release create") || gate > strings.Index(release, "Publish qualified image tags") {
		t.Fatal("installed-candidate qualification must precede all publication")
	}
	for _, required := range []string{"workflow_dispatch:", "schedule:", "ubuntu-24.04-arm", "sha256sum --check", "args=(qualify installed-candidate", "--require-release-transition", "test -n \"$previous_image\"", "--previous-image", "transition-qualification.json", "Create qualification incident"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("installed-candidate workflow missing %q", required)
		}
	}
	for _, required := range []string{"func (c *Controller) QualifyInstalledCandidate", "runQualificationAuthoring", "runQualificationRecovery", "qualification-report.json", "runtime-identity.json", "performance-report.json", "recovery-report.json", "verifyQualificationLegacyPolicy", "restoreQualificationBackup"} {
		if !strings.Contains(installed, required) {
			t.Errorf("typed installed-candidate controller missing %q", required)
		}
	}
	for _, required := range []string{"bootstrapQualificationLocalPhysicalPool", `"pool", "qualify"`, "startQualificationBootstrap", "waitQualificationBootstrapLiveness", `"up", "-d", "--no-deps", "caddy"`, "waitQualificationReadiness"} {
		if !strings.Contains(installed, required) {
			t.Errorf("installed qualification missing sealed-delivery bootstrap contract %q", required)
		}
	}
	for _, required := range []string{"bootstrapQualificationLocalPhysicalPool", "startQualificationBootstrap", "waitQualificationReadiness"} {
		if !strings.Contains(imageQualification, required) {
			t.Errorf("production-image qualification missing sealed-delivery bootstrap contract %q", required)
		}
	}
	restoreStart := strings.LastIndex(installed, "restoreController.Start(ctx)")
	restoreApply := strings.LastIndex(installed, "restoreController.Restore(")
	if restoreStart < 0 || restoreApply < 0 || restoreStart < restoreApply {
		t.Error("isolated restore must apply admitted pool and target state before readiness-gated start")
	}
	for _, required := range []string{"missing_physical_pool_admission", "target_revision_missing", `"unhealthy"`} {
		if !strings.Contains(runtimeQualification, required) {
			t.Errorf("bare production-image smoke missing fail-closed startup assertion %q", required)
		}
	}
	for _, required := range []string{"ManagedUpload", "ReleaseFinalization", "DeploymentActivation", "RefreshRecovery", "QueryStreamReconnect", "BackupInterruption", "RestorePreflight", "BoundedDisk", "waitForQualificationEvents", "/events?limit=100"} {
		if !strings.Contains(recovery, required) {
			t.Errorf("typed recovery controller missing %q", required)
		}
	}
	for _, removed := range []string{
		filepath.Join(root, "deploy", "compose", "qualification", "qualify.sh"),
		filepath.Join(root, "deploy", "compose", "qualification", "authoring.sh"),
		filepath.Join(root, "deploy", "compose", "qualification", "recover.sh"),
		filepath.Join(root, "scripts", "qualify_authoring_image.sh"),
	} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Errorf("legacy shell orchestrator still exists: %s", removed)
		}
	}
	if strings.Contains(performance, "setInterval(") || strings.Count(performance, "metricSamples.push(await metricSnapshot())") < 7 || !strings.Contains(performance, "{ mode: 0o644 }") {
		t.Error("performance evidence must be bounded and artifact-readable")
	}
	for name, script := range map[string]string{
		"authoring":   authoringWorker,
		"browser":     browser,
		"performance": performance,
	} {
		if !strings.Contains(script, "new URL('/login', baseURL).href") {
			t.Errorf("%s qualification worker must navigate to the explicit public login route", name)
		}
	}
	for _, required := range []string{
		`table.evaluate((element) => element.scrollIntoView({ block: 'center' }))`,
		`rows.first().waitFor({ state: 'visible', timeout: 30_000 })`,
		`stateCells.first().waitFor({ state: 'visible', timeout: 30_000 })`,
	} {
		if !strings.Contains(browser, required) {
			t.Errorf("browser qualification must wait for asynchronous governed table rendering %q", required)
		}
	}
	if !strings.Contains(browser, `name: 'State: SP'`) || !strings.Contains(performance, "name: `State: ${value}`") {
		t.Error("browser qualification must assert the table cell accessibility label")
	}
	if !strings.Contains(performance, `name: /^Order(?: [↑↓])?$/`) {
		t.Error("performance qualification must select only the sortable Order header")
	}
	if strings.Contains(browser, "encodeURIComponent(process.env.QUALIFICATION_PROJECT_ID") ||
		!strings.Contains(browser, "Authorization: `Bearer ${credentials.auditToken}`") {
		t.Error("browser qualification must preserve canonical project IDs and use the dedicated audit credential")
	}
	if strings.Contains(performance, "encodeURIComponent(projectID)") || strings.Contains(performance, "encodeURIComponent(semanticModelID)") {
		t.Error("performance qualification must preserve canonical resource IDs in route paths")
	}
	for _, required := range []string{
		"validateQualificationPerformancePolicy",
		"evaluateQualificationPerformance",
		"compareQualificationPerformance",
		"finalizeQualificationPerformanceReport",
	} {
		if !strings.Contains(performancePolicy, required) {
			t.Errorf("Go performance policy controller missing %q", required)
		}
	}
	if strings.Contains(performance, "evaluatePerformance") ||
		strings.Contains(performance, "comparePerformance") {
		t.Error("browser worker must not own performance policy decisions")
	}
	if strings.Contains(performance, "measures:") || !strings.Contains(performance, "metrics: [{ field: 'order_count' }, { field: 'revenue' }]") {
		t.Error("performance governed query must use semantic metrics")
	}
	for _, required := range []string{"Automated step", "Human check", "Interruption recovery", "fresh-install-only", "./leapviewctl qualify installed-candidate"} {
		if !strings.Contains(runbook, required) {
			t.Errorf("qualification runbook missing %q", required)
		}
	}
}

func TestEnterpriseAuthoringGoldenJourneyContract(t *testing.T) {
	root := filepath.Join("..", "..")
	ci := read(t, filepath.Join(root, ".github", "workflows", "ci.yml"))
	artifacts := read(t, filepath.Join(root, ".github", "workflows", "artifacts.yml"))
	installed := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_installed.go"))
	authoring := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_authoring.go"))
	client := read(t, filepath.Join(root, "internal", "app", "cli", "composectl", "qualification_client.go"))
	deploymentClient := read(t, filepath.Join(root, "internal", "deployment", "api", "gen", "client.apigen.gen.go"))
	worker := read(t, filepath.Join(root, "deploy", "compose", "qualification", "authoring-worker.mjs"))
	clientImage := read(t, filepath.Join(root, "deploy", "compose", "qualification", "Dockerfile.authoring-client"))

	if strings.Contains(ci, "image:qualify:production") {
		t.Error("external pull requests must not qualify or publish production images")
	}
	for _, required := range []string{"task image:qualify:production IMAGE=\"${immutable_image}\"", "qualify image", "--image {{.IMAGE | quote}}"} {
		if !strings.Contains(artifacts+read(t, filepath.Join(root, "Taskfile.yml")), required) {
			t.Errorf("main artifact job must qualify its immutable digest remotely: missing %q", required)
		}
	}
	if !strings.Contains(installed, "runQualificationAuthoring") {
		t.Error("installed qualification must reuse authoring")
	}
	for _, required := range []string{"runQualificationCLI", "\"login\",", "\"dev\",", "\"--once\"", "\"--no-browser\"", "\"publish\",", "gnome-keyring-daemon"} {
		if !strings.Contains(client, required) {
			t.Errorf("typed CLI worker missing %q", required)
		}
	}
	if strings.Contains(client, "LEAPVIEW_API_TOKEN") {
		t.Error("authoring must use browser-approved login")
	}
	for _, required := range []string{"verifyExactAuthoringCandidate", "authoring-report.json", "BrowserApprovedLogin", "NativeKeyring", "PrivatePreview", "ExactCandidateActivated", "RequestDeliveryPublicationApproval", "ApproveDeliveryPublicationApproval", "dbus-run-session", "PROJECT_ADMIN", "capabilities"} {
		if !strings.Contains(authoring, required) {
			t.Errorf("typed authoring controller missing %q", required)
		}
	}
	for _, required := range []string{"approval-requests", "/activate"} {
		if !strings.Contains(deploymentClient, required) {
			t.Errorf("generated deployment client missing %q", required)
		}
	}
	if strings.Contains(authoring, "MANAGE_PLATFORM") {
		t.Error("qualification reviewer must not receive a platform grant")
	}
	if strings.Contains(authoring, "PLATFORM_ADMIN") {
		t.Error("authoring credentials must not claim the durable platform-admin role")
	}
	for _, required := range []string{"Authorize LeapView CLI", "CLI authorized", "/candidates/", "Governed order rows", "check({ force: true })"} {
		if !strings.Contains(worker, required) {
			t.Errorf("browser worker missing %q", required)
		}
	}
	if !strings.Contains(worker, "new URL(administratorPage.url())") || strings.Contains(worker, "/dashboards/sales-overview") {
		t.Error("browser worker must follow the candidate preview's canonical dashboard redirect")
	}
	for _, required := range []string{"ARG LEAPVIEW_IMAGE", "FROM ${LEAPVIEW_IMAGE}", "dbus-daemon", "gnome-keyring", "USER author", "CMD [\"/usr/local/libexec/leapviewctl\", \"qualify\", \"client-worker\"]"} {
		if !strings.Contains(clientImage, required) {
			t.Errorf("authoring client image missing %q", required)
		}
	}
}

func TestControllerBuildAndLifecycleCommands(t *testing.T) {
	root := t.TempDir()
	controller := buildController(t, root)
	output, err := exec.Command(controller, "help").CombinedOutput()
	if err != nil {
		t.Fatalf("leapviewctl help: %v\n%s", err, output)
	}
	for _, command := range []string{"version", "init", "start", "status", "logs", "first-login", "backup", "restore", "upgrade", "rollback"} {
		if !strings.Contains(string(output), command) {
			t.Fatalf("controller help missing %s:\n%s", command, output)
		}
	}
}

func TestReleaseIdentityContract(t *testing.T) {
	root := filepath.Join("..", "..")
	version := strings.TrimSpace(read(t, filepath.Join(root, "VERSION")))
	if strings.HasPrefix(version, "v") || !semver.IsValid("v"+version) {
		t.Fatalf("VERSION = %q, want unprefixed semantic version", version)
	}
	packageManifest := read(t, filepath.Join(root, "package.json"))
	if !strings.Contains(packageManifest, `"version": "`+version+`"`) {
		t.Fatalf("package.json does not match VERSION %q", version)
	}

	dockerfile := read(t, filepath.Join(root, "Dockerfile"))
	for _, required := range []string{
		"BUILD_VERSION=development",
		"BUILD_REVISION=unknown",
		"BUILD_TIME=unknown",
		"BUILD_DIRTY=true",
		"BUILD_RELEASE=false",
		"internal/platform/buildinfo.version",
		"internal/platform/buildinfo.revision",
		"internal/platform/buildinfo.buildTime",
		"internal/platform/buildinfo.dirty",
		"internal/platform/buildinfo.release",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile missing build identity contract %q", required)
		}
	}

	release := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for _, required := range []string{
		"Resolve authoritative build identity",
		"VERSION",
		"BUILD_TIME=",
		"BUILD_DIRTY=false",
		"BUILD_RELEASE=",
		"release-identity.json",
		"./leapviewctl version --json",
		"Verify published runtime identity",
		`docker run --rm "$IMAGE_REFERENCE" version --json`,
	} {
		if !strings.Contains(release, required) {
			t.Errorf("release workflow missing build identity contract %q", required)
		}
	}

	for _, name := range []string{
		filepath.Join(root, "deploy", "compose", "README.md"),
		filepath.Join(root, "docs", "articles", "start", "installation.md"),
	} {
		document := read(t, name)
		for _, required := range []string{
			"release-identity.json",
			"leapviewctl version --json",
			"org.opencontainers.image.version",
			"/api/v1/capabilities",
		} {
			if !strings.Contains(document, required) {
				t.Errorf("%s missing identity verification step %q", name, required)
			}
		}
	}
}

func TestRepositoryReleaseTransitionValuesBindRealCandidate(t *testing.T) {
	root := filepath.Join("..", "..")
	version := strings.TrimSpace(read(t, filepath.Join(root, "VERSION")))
	base, err := compatibility.EmbeddedPolicy()
	require.NoError(t, err)
	template, err := compatibility.ParseCandidateTransitionTemplate([]byte(read(t, filepath.Join(
		root,
		"internal", "platform", "compatibility", "release-transition-template.json",
	))))
	require.NoError(t, err)
	if base.CandidateRelease != template.PredecessorRelease {
		t.Fatalf("embedded candidate %q does not match reviewed predecessor %q", base.CandidateRelease, template.PredecessorRelease)
	}

	releaseWorkflow := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for _, required := range []string{
		`canonical_version="$(tr -d '[:space:]' < VERSION)"`,
		`version="$canonical_version"`,
		`BUILD_VERSION: ${{ needs.identity.outputs.version }}`,
		`"version": os.environ["BUILD_VERSION"]`,
		`--candidate-admission assembled-image-admission.json`,
	} {
		if !strings.Contains(releaseWorkflow, required) {
			t.Fatalf("release workflow does not bind repository VERSION through admission: missing %q", required)
		}
	}

	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("e", 64)
	bound, err := base.BindCandidateWithTemplate(compatibility.ReleaseIdentity{
		Version: version, SourceRevision: strings.Repeat("d", 40),
		Image: image, Distribution: "public",
	}, template.Platforms, template)
	require.NoError(t, err)

	predecessor, ok := bound.ReleaseByID(template.PredecessorRelease)
	if !ok {
		t.Fatalf("bound policy omits reviewed predecessor %q", template.PredecessorRelease)
	}
	candidateID := "v" + version
	candidate, ok := bound.ReleaseByID(candidateID)
	if !ok || bound.CandidateRelease != candidateID {
		t.Fatalf("bound candidate = %#v, present=%v, policy candidate=%q", candidate, ok, bound.CandidateRelease)
	}
	for _, platform := range template.Platforms {
		previousIdentity := predecessor.IdentityForPlatform(platform)
		candidateIdentity := candidate.IdentityForPlatform(platform)
		if previousIdentity.ReleaseID != template.PredecessorRelease || previousIdentity.Image == "" {
			t.Fatalf("predecessor identity for %s = %#v", platform, previousIdentity)
		}
		if candidateIdentity.ReleaseID != candidateID || candidateIdentity.Version != version || candidateIdentity.Image != image {
			t.Fatalf("candidate identity for %s = %#v", platform, candidateIdentity)
		}
		for _, transition := range []struct {
			operation compatibility.Operation
			current   compatibility.ReleaseIdentity
			next      compatibility.ReleaseIdentity
		}{
			{operation: compatibility.OperationUpgrade, current: previousIdentity, next: candidateIdentity},
			{operation: compatibility.OperationRollback, current: candidateIdentity, next: previousIdentity},
		} {
			decision := bound.Evaluate(compatibility.Request{
				Operation: transition.operation,
				Current:   transition.current,
				Next:      transition.next,
			})
			if err := decision.Err(); err != nil {
				t.Fatalf("%s %s decision = %#v: %v", platform, transition.operation, decision, err)
			}
		}
	}
}

func TestControllerInitializationGeneratesValidPublicOrigin(t *testing.T) {
	binaryDir := t.TempDir()
	validator := buildConfigValidator(t, binaryDir)
	image := "example.com/leapview@sha256:" + strings.Repeat("a", 64)

	for _, test := range []struct {
		name       string
		args       []string
		composeTLS string
	}{
		{name: "built-in Caddy", composeTLS: "1"},
		{name: "trusted external HTTPS proxy", args: []string{"--no-https"}, composeTLS: "0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			buildController(t, root)
			copyDeploymentFile(t, root, "deployment.env.example", 0o600)
			fakeDocker := filepath.Join(root, "fake-docker")
			script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
root="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [[ " $* " == *" config validate --production "* ]]; then
  set -a
  source "$root/leapview.env"
  set +a
  exec %q config validate --production
fi
if [[ " $* " == *" admin initialize --format json "* ]]; then
  printf '{"email":"admin@example.com","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-19T00:00:00Z"}\n'
fi
`, validator)
			if err := os.WriteFile(fakeDocker, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			args := []string{"init", "--admin-email", "admin@example.com", "--domain", "dash.example.com", "--image", image}
			args = append(args, test.args...)
			runController(t, root, fakeDocker, "", args...)

			appEnvironment := readFile(t, filepath.Join(root, "leapview.env"))
			for _, required := range []string{
				"LEAPVIEW_PUBLIC_URL=https://dash.example.com\n",
				"LEAPVIEW_ALLOWED_HOSTS=dash.example.com\n",
				"LEAPVIEW_TRUST_PROXY_HEADERS=true\n",
			} {
				if !strings.Contains(appEnvironment, required) {
					t.Errorf("leapview.env missing %q:\n%s", required, appEnvironment)
				}
			}
			deploymentEnvironment := readFile(t, filepath.Join(root, "deployment.env"))
			for _, required := range []string{
				"CADDY_DOMAIN=dash.example.com\n",
				"COMPOSE_HTTPS=" + test.composeTLS + "\n",
			} {
				if !strings.Contains(deploymentEnvironment, required) {
					t.Errorf("deployment.env missing %q:\n%s", required, deploymentEnvironment)
				}
			}
		})
	}
}

func TestControllerReleasePackagingContract(t *testing.T) {
	release := read(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	for _, required := range []string{
		"./cmd/leapviewctl",
		"CGO_ENABLED=0",
		"linux amd64",
		"linux arm64",
		"darwin amd64",
		"darwin arm64",
	} {
		if !strings.Contains(release, required) {
			t.Fatalf("release workflow missing Go controller packaging contract %q", required)
		}
	}
	generation := strings.Index(release, "- name: Generate release build inputs\n        run: task generate")
	packaging := strings.Index(release, "- name: Build candidate Compose archives")
	if generation < 0 || packaging < 0 || generation > packaging {
		t.Fatal("release workflow must generate every ignored build input before compiling Compose archives")
	}
	admission := strings.Index(release, "- name: Admit exact assembled release image")
	binding := strings.Index(release, "--bind-release release-identity.json")
	if admission < 0 || binding < 0 || admission > binding {
		t.Fatal("release workflow must admit the candidate image identity before binding its transition policy")
	}
	upload := strings.Index(release[packaging:], "- name: Upload unpublished candidate")
	if upload < 0 {
		t.Fatal("release workflow is missing candidate upload")
	}
	packagingBlock := release[packaging : packaging+upload]
	for _, required := range []string{
		"IMAGE_REFERENCE: ${{ steps.assembled_admission.outputs.image }}",
		"--candidate-admission assembled-image-admission.json",
		"--predecessor-evidence-output predecessor-verification.json",
	} {
		if !strings.Contains(packagingBlock, required) {
			t.Fatalf("release policy generation does not consume admission contract %q", required)
		}
	}
	if strings.Contains(packagingBlock, "steps.assemble.outputs.digest") {
		t.Fatal("release policy generation must not consume the unadmitted assembly digest")
	}
	for _, required := range []string{"release-transition-policy.json predecessor-verification.json \"dist/$package/\"", "candidate/release-transition-policy.json"} {
		if !strings.Contains(release, required) {
			t.Fatalf("release workflow does not distribute candidate-bound policy %q", required)
		}
	}
	dockerfile := read(t, filepath.Join("..", "..", "Dockerfile"))
	if !strings.Contains(dockerfile, "/usr/local/libexec/leapviewctl") {
		t.Fatal("application image must carry the matching Linux controller for provider extraction")
	}
}

func TestV010ReleaseWorkflowInvokesPolicyBoundPreservationQualification(t *testing.T) {
	workflow := read(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	require.NoError(t, validateV010ReleaseWorkflow(workflow))
}

func TestV010ReleaseWorkflowRejectsIncompleteOrUnboundWiring(t *testing.T) {
	workflow := read(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	for _, test := range []struct {
		name        string
		old         string
		replacement string
	}{
		{
			name: "missing admission artifact",
			old:  `candidate_admission="$GITHUB_WORKSPACE/candidate/assembled-image-admission.json"`,
		},
		{
			name: "missing predecessor evidence",
			old:  `--predecessor-evidence "$predecessor_evidence"`,
		},
		{
			name:        "wrong policy digest",
			old:         `policy_sha256="$(sha256sum "$policy" | awk '{print $1}')"`,
			replacement: `policy_sha256="0000000000000000000000000000000000000000000000000000000000000000"`,
		},
		{
			name: "missing evidence artifact",
			old:  `${{ runner.temp }}/installed-candidate-evidence/v0.1-preservation-qualification.json`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := strings.Replace(workflow, test.old, test.replacement, 1)
			require.NotEqual(t, workflow, mutated, "contract mutation did not match workflow")
			require.Error(t, validateV010ReleaseWorkflow(mutated))
		})
	}
}

func validateV010ReleaseWorkflow(workflow string) error {
	required := []string{
		"- name: Run exact v0.1 preservation qualification",
		"if: matrix.arch == 'amd64'",
		"- name: Set up exact OCI resolver",
		`candidate_admission="$GITHUB_WORKSPACE/candidate/assembled-image-admission.json"`,
		`policy="$PACKAGE_ROOT/release-transition-policy.json"`,
		`policy_sha256="$(sha256sum "$policy" | awk '{print $1}')"`,
		`./leapviewctl qualify v0.1-artifact-review`,
		`--policy-sha256 "$policy_sha256"`,
		`--evidence "$predecessor_evidence"`,
		`test -s "$predecessor_evidence"`,
		`./leapviewctl qualify v0.1-preservation`,
		`--candidate-admission "$candidate_admission"`,
		`--predecessor-evidence "$predecessor_evidence"`,
		`test -s "$qualification_evidence"`,
		`${{ runner.temp }}/installed-candidate-evidence/v0.1-reviewed-identity.json`,
		`${{ runner.temp }}/installed-candidate-evidence/v0.1-preservation-qualification.json`,
		"needs: [image, qualify, minio-conformance, plan-gc-conformance]",
	}
	for _, contract := range required {
		if !strings.Contains(workflow, contract) {
			return fmt.Errorf("release workflow missing v0.1 qualification contract %q", contract)
		}
	}
	review := strings.Index(workflow, "./leapviewctl qualify v0.1-artifact-review")
	qualification := strings.Index(workflow, "./leapviewctl qualify v0.1-preservation")
	upload := strings.Index(workflow, "- name: Upload bounded qualification evidence")
	if review < 0 || qualification < 0 || upload < 0 || review >= qualification || qualification >= upload {
		return fmt.Errorf("release workflow must review v0.1 identity, qualify preservation, then upload evidence")
	}
	return nil
}

func TestControllerLifecycleWithStateAwareUpgradeRollback(t *testing.T) {
	root := t.TempDir()
	buildController(t, root)
	buildConfigValidator(t, root)
	copyDeploymentFile(t, root, "deployment.env.example", 0o600)
	fakeDocker := filepath.Join(root, "fake-docker")
	if err := os.WriteFile(fakeDocker, []byte(`#!/usr/bin/env bash
set -euo pipefail
root="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
printf '%s\n' "$*" >>"$root/docker.log"
if [[ -n "${FAKE_DOCKER_FAIL_COMMAND:-}" && " $* " == *" ${FAKE_DOCKER_FAIL_COMMAND} "* ]]; then exit 42; fi
if [[ "${FAKE_DOCKER_FAIL_RESTORE_ONCE:-}" == 1 && " $* " == *' admin restore '* && ! -e "$root/restore-failed-once" ]]; then
  touch "$root/restore-failed-once"
  exit 42
fi
if [[ "${1:-}" == inspect ]]; then
  template="${3:-}"
  if [[ "$template" == *Running* ]]; then printf 'true\n'; exit 0; fi
  image="$(awk -F= '$1=="LEAPVIEW_IMAGE" {sub(/^[^=]*=/, ""); print; exit}' "$root/deployment.env")"
  if [[ -n "${FAKE_DOCKER_FAIL_IMAGE:-}" && "$image" == "$FAKE_DOCKER_FAIL_IMAGE" ]]; then printf 'unhealthy\n'; else printf 'healthy\n'; fi
  exit 0
fi
[[ "${1:-}" == compose ]] || exit 0
shift
while [[ $# -gt 0 ]]; do
  case "$1" in
    --project-directory|--env-file|-f) shift 2 ;;
    *) command="$1"; shift; break ;;
  esac
done
case "${command:-}" in
  ps) [[ " $* " == *' -q '* ]] && printf 'fake-container\n' ;;
  run)
    if [[ " $* " == *' config validate --production '* ]]; then
      set -a
      source "$root/leapview.env"
      set +a
      exec "$root/config-validator"
    elif [[ " $* " == *' admin initialize --format json '* ]]; then
      printf '{"email":"admin@example.com","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-19T00:00:00Z"}\n'
    elif [[ " $* " == *' admin backup '* ]]; then
      output=""
      while [[ $# -gt 0 ]]; do
        if [[ "$1" == --out ]]; then output="$2"; break; fi
        shift
      done
      if [[ "$output" == - ]]; then
        printf 'validated archive\n'
      else
        output="$root/${output#/}"
        mkdir -p "$(dirname -- "$output")"
        printf 'validated archive\n' >"$output"
      fi
    fi
    ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}

	oldImage := "example.com/leapview@sha256:" + strings.Repeat("a", 64)
	newImage := "example.com/leapview@sha256:" + strings.Repeat("b", 64)
	transitionPolicy := filepath.Join(root, "release-transition-policy.json")
	writeReleaseTransitionPolicy(t, transitionPolicy, oldImage, newImage)
	runController(t, root, fakeDocker, "", "init", "--admin-email", "admin@example.com", "--domain", "dash.example.com", "--image", oldImage)
	for _, name := range []string{"deployment.env", "leapview.env", "initial-credentials.json"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions = %v, %v", name, info.Mode().Perm(), err)
		}
	}
	if output := runController(t, root, fakeDocker, "", "first-login"); !strings.Contains(output, `"temporaryPassword":"temporary"`) {
		t.Fatalf("first-login output = %s", output)
	}
	if _, err := os.Stat(filepath.Join(root, "initial-credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("one-time credentials were not deleted: %v", err)
	}
	runController(t, root, fakeDocker, "", "start")
	t.Setenv("FAKE_DOCKER_FAIL_COMMAND", "admin backup")
	if output, err := runControllerResult(root, fakeDocker, "", "backup"); err == nil || !strings.Contains(output, "previous service state was restored") {
		t.Fatalf("failed backup result = %v, %s", err, output)
	}
	t.Setenv("FAKE_DOCKER_FAIL_COMMAND", "")
	backupOutput := runController(t, root, fakeDocker, "", "backup")
	backupPath := strings.TrimSpace(backupOutput)
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup missing: %v (%s)", err, backupOutput)
	}
	runController(t, root, fakeDocker, "", "restore", backupPath)
	t.Setenv("FAKE_DOCKER_FAIL_COMMAND", "pull leapview")
	if output, err := runControllerResult(root, fakeDocker, "", "upgrade", "--transition-policy", transitionPolicy, newImage); err == nil || !strings.Contains(output, "previous image and service state were restored") {
		t.Fatalf("failed pull result = %v, %s", err, output)
	}
	requireDeploymentImage(t, root, oldImage)
	t.Setenv("FAKE_DOCKER_FAIL_COMMAND", "")

	output, err := runControllerResult(root, fakeDocker, newImage, "upgrade", "--transition-policy", transitionPolicy, newImage)
	if err == nil || !strings.Contains(output, "previous image and state were restored") {
		t.Fatalf("failed upgrade result = %v, %s", err, output)
	}
	requireDeploymentImage(t, root, oldImage)
	runController(t, root, fakeDocker, "", "upgrade", "--transition-policy", transitionPolicy, newImage)
	requireDeploymentImage(t, root, newImage)
	t.Setenv("FAKE_DOCKER_FAIL_RESTORE_ONCE", "1")
	if output, err := runControllerResult(root, fakeDocker, "", "rollback", "--transition-policy", transitionPolicy, "--confirm"); err == nil || !strings.Contains(output, "pre-rollback image and state were reinstated") {
		t.Fatalf("failed rollback result = %v, %s", err, output)
	}
	requireDeploymentImage(t, root, newImage)
	t.Setenv("FAKE_DOCKER_FAIL_RESTORE_ONCE", "")
	runController(t, root, fakeDocker, "", "rollback", "--transition-policy", transitionPolicy, "--confirm")
	requireDeploymentImage(t, root, oldImage)
	log, err := os.ReadFile(filepath.Join(root, "docker.log"))
	if err != nil || !strings.Contains(string(log), "admin restore") {
		t.Fatalf("controller did not restore paired state: %v\n%s", err, log)
	}
}

func writeReleaseTransitionPolicy(t *testing.T, path, previousImage, candidateImage string) {
	t.Helper()
	denied := compatibility.Rule{
		ReasonCode: compatibility.ReasonDeniedNoExplicitRule, Remediation: "use an explicit transition", Requirements: []string{},
	}
	requirements := []string{compatibility.RequirementBackupBeforeMutation, compatibility.RequirementStoppedInstance}
	policy := &compatibility.Policy{
		SchemaVersion: compatibility.CurrentSchemaVersion, PolicyVersion: "test/release-v2", CandidateRelease: "v2.0.0",
		Releases: []compatibility.Release{
			{ID: "v1.0.0", Version: "1.0.0", SourceRevision: strings.Repeat("a", 40), Distribution: "public", LegacyMarkers: []string{}, LegacyBackupVersions: []int{}, Artifacts: []compatibility.Artifact{{Platform: "linux/amd64", Image: previousImage}}, Defaults: compatibility.ReleaseDefaults{FreshInstall: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedFreshInstall, Requirements: []string{}}, Upgrade: denied, Rollback: denied}},
			{ID: "v2.0.0", Version: "2.0.0", SourceRevision: strings.Repeat("b", 40), Distribution: "public", LegacyMarkers: []string{}, LegacyBackupVersions: []int{}, Artifacts: []compatibility.Artifact{{Platform: "linux/amd64", Image: candidateImage}}, Defaults: compatibility.ReleaseDefaults{FreshInstall: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedFreshInstall, Requirements: []string{}}, Upgrade: denied, Rollback: denied}},
		},
		Transitions: []compatibility.Transition{
			{Operation: compatibility.OperationUpgrade, From: "v1.0.0", To: "v2.0.0", Platforms: []string{"linux/amd64"}, Decision: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedExplicitTransition, Requirements: requirements}},
			{Operation: compatibility.OperationRollback, From: "v2.0.0", To: "v1.0.0", Platforms: []string{"linux/amd64"}, Decision: compatibility.Rule{Allowed: true, ReasonCode: compatibility.ReasonAllowedExplicitTransition, Requirements: requirements}},
		},
	}
	contents, err := compatibility.MarshalPolicy(policy)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, contents, 0o600))
}

func TestControllerInitializationIsRetryableAndRequiresPinnedProxy(t *testing.T) {
	image := "example.com/leapview@sha256:" + strings.Repeat("a", 64)
	setup := func(t *testing.T) (string, string) {
		t.Helper()
		root := t.TempDir()
		buildController(t, root)
		buildConfigValidator(t, root)
		copyDeploymentFile(t, root, "deployment.env.example", 0o600)
		fakeDocker := filepath.Join(root, "fake-docker")
		if err := os.WriteFile(fakeDocker, []byte(`#!/usr/bin/env bash
set -euo pipefail
root="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if [[ -f "$root/fail-validation" && " $* " == *" config validate "* ]]; then exit 42; fi
if [[ " $* " == *" config validate --production "* ]]; then
  set -a
  source "$root/leapview.env"
  set +a
  exec "$root/config-validator"
elif [[ " $* " == *" admin initialize --format json "* ]]; then
  printf '{"email":"admin@example.com","temporaryPassword":"temporary","publisherToken":"publisher","publisherTokenExpiresAt":"2026-07-19T00:00:00Z"}\n'
fi
`), 0o700); err != nil {
			t.Fatal(err)
		}
		return root, fakeDocker
	}

	t.Run("retry after validation failure", func(t *testing.T) {
		root, fakeDocker := setup(t)
		if err := os.WriteFile(filepath.Join(root, "fail-validation"), []byte("fail\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := runControllerResult(root, fakeDocker, "", "init", "--admin-email", "admin@example.com", "--domain", "dash.example.com", "--image", image); err == nil || !strings.Contains(output, "initialization can be retried") {
			t.Fatalf("failed initialization = %v, %s", err, output)
		}
		for _, name := range []string{"leapview.env", "initial-credentials.json"} {
			if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
				t.Fatalf("partial initialization retained %s: %v", name, err)
			}
		}
		if err := os.Remove(filepath.Join(root, "fail-validation")); err != nil {
			t.Fatal(err)
		}
		runController(t, root, fakeDocker, "", "init", "--admin-email", "admin@example.com", "--domain", "dash.example.com", "--image", image)
	})

	t.Run("mutable proxy image", func(t *testing.T) {
		root, fakeDocker := setup(t)
		examplePath := filepath.Join(root, "deployment.env.example")
		contents, err := os.ReadFile(examplePath)
		require.NoError(t, err)
		lines := strings.Split(string(contents), "\n")
		for i := range lines {
			if strings.HasPrefix(lines[i], "CADDY_IMAGE=") {
				lines[i] = "CADDY_IMAGE=caddy:latest"
			}
		}
		if err := os.WriteFile(examplePath, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := runControllerResult(root, fakeDocker, "", "init", "--admin-email", "admin@example.com", "--domain", "dash.example.com", "--image", image); err == nil || !strings.Contains(output, "image must be pinned by digest") {
			t.Fatalf("mutable proxy result = %v, %s", err, output)
		}
	})
}

func copyDeploymentFile(t *testing.T, targetDir, name string, mode os.FileMode) {
	t.Helper()
	contents, err := os.ReadFile(name)
	require.NoError(t, err)
	if err := os.WriteFile(filepath.Join(targetDir, name), contents, mode); err != nil {
		t.Fatal(err)
	}
}

func buildController(t *testing.T, targetDir string) string {
	t.Helper()
	target := filepath.Join(targetDir, "leapviewctl")
	command := exec.Command("go", "build", "-o", target, "./cmd/leapviewctl")
	command.Dir = filepath.Join("..", "..")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build leapviewctl: %v\n%s", err, output)
	}
	return target
}

func buildConfigValidator(t *testing.T, targetDir string) string {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	generateConfigOnce.Do(func() {
		command := exec.Command("go", "run", "./internal/app/tools/configgen")
		command.Dir = repositoryRoot
		generateConfigOutput, generateConfigError = command.CombinedOutput()
	})
	if generateConfigError != nil {
		t.Fatalf("generate configuration contract: %v\n%s", generateConfigError, generateConfigOutput)
	}

	temporaryRoot := filepath.Join(repositoryRoot, ".tmp")
	if err := os.MkdirAll(temporaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sourceDir, err := os.MkdirTemp(temporaryRoot, "compose-config-validator-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })
	if err := os.WriteFile(filepath.Join(sourceDir, "validator_test.go"), []byte(configValidatorTestProgram), 0o600); err != nil {
		t.Fatal(err)
	}
	relativeSourceDir, err := filepath.Rel(repositoryRoot, sourceDir)
	require.NoError(t, err)

	target := filepath.Join(targetDir, "config-validator")
	command := exec.Command("go", "test", "-c", "-o", target, "./"+filepath.ToSlash(relativeSourceDir))
	command.Dir = repositoryRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build production config validator: %v\n%s", err, output)
	}
	return target
}

func runController(t *testing.T, root, docker, failImage string, args ...string) string {
	t.Helper()
	output, err := runControllerResult(root, docker, failImage, args...)
	if err != nil {
		t.Fatalf("leapviewctl %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func runControllerResult(root, docker, failImage string, args ...string) (string, error) {
	command := exec.Command(filepath.Join(root, "leapviewctl"), args...)
	command.Dir = root
	command.Env = append(os.Environ(), "LEAPVIEWCTL_ROOT="+root, "LEAPVIEWCTL_DOCKER_BIN="+docker, "DOCKER_DEFAULT_PLATFORM=linux/amd64", "FAKE_DOCKER_FAIL_IMAGE="+failImage)
	output, err := command.CombinedOutput()
	return string(output), err
}

func requireDeploymentImage(t *testing.T, root, image string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "deployment.env"))
	if err != nil || !strings.Contains(string(contents), "LEAPVIEW_IMAGE="+image+"\n") {
		t.Fatalf("deployment image is not %s: %v\n%s", image, err, contents)
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	return readFile(t, name)
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	value, err := os.ReadFile(name)
	require.NoError(t, err)
	return string(value)
}
