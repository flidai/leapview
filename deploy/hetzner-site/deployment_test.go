package hetznersite_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeIsAStatelessTwoServiceSite(t *testing.T) {
	compose := readFile(t, filepath.Join("files", "compose.yaml"))
	var document struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Environment map[string]string `yaml:"environment"`
			Command     []string          `yaml:"command"`
			Volumes     []string          `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		t.Fatalf("parse Compose file: %v", err)
	}
	if len(document.Services) != 2 {
		t.Fatalf("services = %v, want exactly caddy and leapview-site", document.Services)
	}
	site, siteOK := document.Services["leapview-site"]
	caddy, caddyOK := document.Services["caddy"]
	if !siteOK || !caddyOK {
		t.Fatalf("services = %v, want exactly caddy and leapview-site", document.Services)
	}
	if len(site.Volumes) != 0 {
		t.Fatalf("leapview-site must not have application-state volumes: %v", site.Volumes)
	}
	if site.Environment["LEAPVIEW_SITE_BASE_URL"] != "https://leapview.dev" {
		t.Fatalf("LEAPVIEW_SITE_BASE_URL = %q", site.Environment["LEAPVIEW_SITE_BASE_URL"])
	}
	if !slices.Contains(site.Command, "-image-reference=${LEAPVIEW_SITE_IMAGE:?must be an immutable digest}") {
		t.Fatalf("site command does not receive the immutable deployment identity: %v", site.Command)
	}
	if _, configured := site.Environment["LEAPVIEW_SITE_SHOWCASE_EMBED_URL"]; configured {
		t.Fatal("the optional live-dashboard embed must remain unset for rc.1")
	}
	for name, contract := range map[string]struct {
		image string
		want  string
	}{
		"leapview-site": {image: site.Image, want: "${LEAPVIEW_SITE_IMAGE:?must be an immutable digest}"},
		"caddy":         {image: caddy.Image, want: "${CADDY_IMAGE:?must be an immutable digest}"},
	} {
		if contract.image != contract.want {
			t.Errorf("%s image = %q, want guarded immutable input %q", name, contract.image, contract.want)
		}
	}
	for _, forbidden := range []string{
		"duckdb", "leapview-state", "admin_email", "bootstrap", "backup",
		"ghcr.io/flidai/leapview@",
	} {
		if strings.Contains(strings.ToLower(compose), forbidden) {
			t.Errorf("site Compose contains forbidden product fragment %q", forbidden)
		}
	}
}

func TestSiteImageAndRuntimeExposeThePromotedBuildIdentity(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "..", "Dockerfile.site"))
	compose := readFile(t, filepath.Join("files", "compose.yaml"))
	for _, fragment := range []string{
		"ARG BUILD_REVISION=unknown",
		"-X main.buildRevision=${BUILD_REVISION}",
	} {
		requireContains(t, dockerfile, fragment)
	}
	for _, fragment := range []string{
		"-image-reference=${LEAPVIEW_SITE_IMAGE:?must be an immutable digest}",
	} {
		requireContains(t, compose, fragment)
	}
}

func TestTerraformProtectsThePermanentOrigin(t *testing.T) {
	main := readFile(t, "main.tf")
	outputs := readFile(t, "outputs.tf")
	for _, fragment := range []string{
		`resource "hcloud_primary_ip" "site"`,
		"auto_delete       = false",
		"delete_protection = true",
		`resource "hcloud_server" "site"`,
		"delete_protection        = true",
		"rebuild_protection       = true",
		"shutdown_before_deletion = true",
		"prevent_destroy = true",
		"ignore_changes = [user_data]",
		"backups                  = false",
		"ipv6_enabled = false",
	} {
		requireContains(t, main, fragment)
	}
	for _, fragment := range []string{
		`output "reserved_ipv4"`,
		`output "canonical_hostname"`,
		`output "deployment_target"`,
		`output "dns_records"`,
	} {
		requireContains(t, outputs, fragment)
	}
}

func TestTerraformRejectsMutableImagesAndWorldOpenSSH(t *testing.T) {
	variables := readFile(t, "variables.tf")
	for _, fragment := range []string{
		`variable "bootstrap_site_image"`,
		`variable "caddy_image"`,
		`@sha256:`,
		`variable "ssh_allowed_cidrs"`,
		`cidr != "0.0.0.0/0"`,
		`cidr != "::/0"`,
		`variable "operator_ssh_public_key"`,
	} {
		requireContains(t, variables, fragment)
	}
	if strings.Contains(variables, `default     = ["0.0.0.0/0", "::/0"]`) {
		t.Fatal("SSH must not be world-open by default")
	}
}

func TestBootstrapConsumesArtifactsWithoutBuildingOrInitializingProduct(t *testing.T) {
	main := readFile(t, "main.tf")
	cloudInit := readFile(t, "cloud-init.yaml.tftpl")
	provision := readFile(t, filepath.Join("files", "provision.sh"))
	for _, fragment := range []string{
		"compose_b64", "caddyfile_b64", "deployment_env_b64", "provision_b64", "deploy_b64",
		"reconcile_b64", "reconcile_service_b64", "reconcile_timer_b64",
	} {
		requireContains(t, main, fragment)
		requireContains(t, cloudInit, fragment)
	}
	for _, fragment := range []string{
		`docker pull "$LEAPVIEW_SITE_IMAGE"`,
		`docker pull "$CADDY_IMAGE"`,
		"config --quiet",
		"up --detach",
	} {
		requireContains(t, provision, fragment)
	}
	for _, forbidden := range []string{
		"git clone", "docker build", "leapviewctl init", "admin", "duckdb", "backup",
	} {
		if strings.Contains(strings.ToLower(cloudInit+"\n"+provision), forbidden) {
			t.Errorf("stateless bootstrap contains forbidden product operation %q", forbidden)
		}
	}
}

func TestRoutineDeploymentRestoresThePreviousReleaseWhenQualificationFails(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "docker"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "flock"), "#!/usr/bin/env bash\nexit 0\n")

	previous := siteImage("1")
	candidate := siteImage("2")
	writeFile(t, filepath.Join(root, "deployment.env"), "LEAPVIEW_SITE_IMAGE="+previous+"\nCADDY_IMAGE=caddy:2.10.2-alpine@sha256:"+strings.Repeat("3", 64)+"\n", 0o600)
	writeExecutable(t, filepath.Join(root, "provision.sh"), `#!/usr/bin/env bash
set -euo pipefail
site_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${site_root}/deployment.env"
if [[ -f "${site_root}/fail-candidate" && "${LEAPVIEW_SITE_IMAGE}" != "`+previous+`" ]]; then
  exit 23
fi
printf '%s\n' "${LEAPVIEW_SITE_IMAGE}" > "${site_root}/deployed-image"
`)
	writeFile(t, filepath.Join(root, "fail-candidate"), "", 0o600)

	command := exec.Command("bash", materializeDeployScript(t, root), candidate)
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("deployment succeeded, want candidate qualification failure\n%s", output)
	}

	deploymentEnv := readPath(t, filepath.Join(root, "deployment.env"))
	requireContains(t, deploymentEnv, previous)
	if strings.Contains(deploymentEnv, candidate) {
		t.Fatalf("failed candidate remains active:\n%s", deploymentEnv)
	}
	if got := strings.TrimSpace(readPath(t, filepath.Join(root, "deployed-image"))); got != previous {
		t.Fatalf("deployed image after rollback = %q, want %q", got, previous)
	}
	rollbacks, err := filepath.Glob(filepath.Join(root, "deployment.env.rollback.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rollbacks) != 1 {
		t.Fatalf("rollback snapshots = %v, want one retained snapshot", rollbacks)
	}
}

func TestRoutineDeploymentRecordsSuccessfulReleaseEvidence(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(bin, "docker"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "flock"), "#!/usr/bin/env bash\nexit 0\n")

	previous := siteImage("4")
	candidate := siteImage("5")
	writeFile(t, filepath.Join(root, "deployment.env"), "LEAPVIEW_SITE_IMAGE="+previous+"\nCADDY_IMAGE=caddy:2.10.2-alpine@sha256:"+strings.Repeat("6", 64)+"\n", 0o600)
	writeExecutable(t, filepath.Join(root, "provision.sh"), `#!/usr/bin/env bash
set -euo pipefail
site_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${site_root}/deployment.env"
printf '%s\n' "${LEAPVIEW_SITE_IMAGE}" > "${site_root}/deployed-image"
`)

	command := exec.Command("bash", materializeDeployScript(t, root), candidate)
	command.Env = append(os.Environ(),
		"PATH="+bin+":"+os.Getenv("PATH"),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("deploy candidate: %v\n%s", err, output)
	}

	requireContains(t, readPath(t, filepath.Join(root, "deployment.env")), candidate)
	if got := strings.TrimSpace(readPath(t, filepath.Join(root, "previous-image"))); got != previous {
		t.Fatalf("previous image = %q, want %q", got, previous)
	}
	history := readPath(t, filepath.Join(root, "deployment-history.tsv"))
	requireContains(t, history, previous)
	requireContains(t, history, candidate)
	requireContains(t, history, "activated")
}

func TestOperatorDeploymentPinsTheServerIdentityAndQualifiesThePublicRoute(t *testing.T) {
	operator := readFile(t, filepath.Join("..", "..", "scripts", "deploy_site.sh"))
	for _, fragment := range []string{
		"ssh-keyscan",
		"ssh-keygen -lf",
		`"$scanned_key" == \#*`,
		`"$scanned_key_file"`,
		"StrictHostKeyChecking=yes",
		"UserKnownHostsFile=",
		"deploy/hetzner-site/ssh-host-key.sha256",
		"SITE_SSH_PRIVATE_KEY",
		"chmod 0600",
		"deploy/hetzner-site/files/compose.yaml",
		"/opt/leapview-site/compose.yaml",
		"/opt/leapview-site/deploy.sh",
		"leapview-site-reconcile.timer",
		"https://leapview.dev/healthz",
		"https://leapview.dev/readyz",
		"https://www.leapview.dev/",
	} {
		requireContains(t, operator, fragment)
	}
	for _, forbidden := range []string{
		"StrictHostKeyChecking=no",
		"~/.ssh/known_hosts",
		"${HOME}/.ssh/known_hosts",
	} {
		if strings.Contains(operator, forbidden) {
			t.Errorf("operator deployment contains forbidden fragment %q", forbidden)
		}
	}
}

func TestGitHubActionsPromotesVerifiedSiteImageWithoutProductionCredentials(t *testing.T) {
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "site-deploy.yml"))
	for _, fragment := range []string{
		"name: Deploy public site",
		"push:",
		"branches: [main]",
		"workflow_dispatch:",
		"uses: ./.github/workflows/site-image.yml",
		"environment: leapview-site-production",
		"packages: write",
		"ghcr.io/flidai/leapview-site:production",
		"needs.publish.outputs.image_reference",
		"needs.publish.outputs.revision",
		"https://leapview.dev/build.json",
		"https://leapview.dev/healthz",
		"https://leapview.dev/readyz",
	} {
		requireContains(t, workflow, fragment)
	}
	for _, forbidden := range []string{
		"autback", "SITE_SSH_PRIVATE_KEY", "HCLOUD_TOKEN", "ssh ", "scp ",
		"docker build ", "Dockerfile.site",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("public-site deployment workflow contains forbidden fragment %q", forbidden)
		}
	}
}

func TestPullReconcilerResolvesDesiredTagToImmutableDigest(t *testing.T) {
	reconciler := readFile(t, filepath.Join("files", "reconcile.sh"))
	for _, fragment := range []string{
		"ghcr.io/flidai/leapview-site:production",
		"docker pull",
		"RepoDigests",
		"immutable_site_reference",
		`"$site_root/deploy.sh"`,
		"deployed-image",
		"failed-desired-image",
		"flock -n",
		`[[ "$status" -eq 75 ]]`,
	} {
		requireContains(t, reconciler, fragment)
	}
	if strings.Contains(reconciler, "deployment.env.next") {
		t.Fatal("the reconciler must delegate activation and rollback to deploy.sh")
	}

	service := readFile(t, filepath.Join("files", "leapview-site-reconcile.service"))
	timer := readFile(t, filepath.Join("files", "leapview-site-reconcile.timer"))
	for _, fragment := range []string{
		"Type=oneshot",
		"ExecStart=/opt/leapview-site/reconcile.sh",
		"ProtectSystem=strict",
	} {
		requireContains(t, service, fragment)
	}
	for _, fragment := range []string{"OnBootSec=1min", "OnUnitActiveSec=1min", "WantedBy=timers.target"} {
		requireContains(t, timer, fragment)
	}
}

func TestPullReconcilerActivatesEachDesiredDigestOnce(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	desired := siteImage("8")
	writeExecutable(t, filepath.Join(bin, "flock"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "docker"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1 $2" == "pull ghcr.io/flidai/leapview-site:production" ]]; then
  exit 0
fi
if [[ "$1 $2" == "image inspect" ]]; then
  printf '%s\n' "`+desired+`"
  exit 0
fi
exit 64
`)
	writeExecutable(t, filepath.Join(root, "deploy.sh"), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" > "$(dirname "$0")/deployed-image"
printf 'called\n' >> "$(dirname "$0")/deploy-calls"
`)
	writeFile(t, filepath.Join(root, "deployed-image"), siteImage("7")+"\n", 0o644)

	reconciler := materializeReconcileScript(t, root)
	for range 2 {
		command := exec.Command("bash", reconciler)
		command.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("reconcile desired image: %v\n%s", err, output)
		}
	}
	if got := strings.TrimSpace(readPath(t, filepath.Join(root, "deployed-image"))); got != desired {
		t.Fatalf("deployed image = %q, want %q", got, desired)
	}
	if got := strings.Count(readPath(t, filepath.Join(root, "deploy-calls")), "called"); got != 1 {
		t.Fatalf("deploy calls = %d, want one", got)
	}
}

func TestPullReconcilerSuppressesRepeatedFailedDigest(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	desired := siteImage("a")
	writeExecutable(t, filepath.Join(bin, "flock"), "#!/usr/bin/env bash\nexit 0\n")
	writeExecutable(t, filepath.Join(bin, "docker"), `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "pull" ]]; then exit 0; fi
if [[ "$1 $2" == "image inspect" ]]; then printf '%s\n' "`+desired+`"; exit 0; fi
exit 64
`)
	writeExecutable(t, filepath.Join(root, "deploy.sh"), `#!/usr/bin/env bash
printf 'called\n' >> "$(dirname "$0")/deploy-calls"
exit 23
`)
	writeFile(t, filepath.Join(root, "deployed-image"), siteImage("9")+"\n", 0o644)

	reconciler := materializeReconcileScript(t, root)
	first := exec.Command("bash", reconciler)
	first.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := first.CombinedOutput(); err == nil {
		t.Fatalf("first reconciliation succeeded, want candidate failure\n%s", output)
	}
	second := exec.Command("bash", reconciler)
	second.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if output, err := second.CombinedOutput(); err != nil {
		t.Fatalf("repeated failed digest was not suppressed: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(readPath(t, filepath.Join(root, "failed-desired-image"))); got != desired {
		t.Fatalf("failed desired image = %q, want %q", got, desired)
	}
	if got := strings.Count(readPath(t, filepath.Join(root, "deploy-calls")), "called"); got != 1 {
		t.Fatalf("deploy calls = %d, want one", got)
	}
}

func TestTaskExposesTheBoundedSiteDeployment(t *testing.T) {
	taskfile := readFile(t, filepath.Join("..", "..", "Taskfile.yml"))
	for _, fragment := range []string{
		"site:deploy:",
		"vars: [LEAPVIEW_SITE_IMAGE]",
		"infisical run",
		"--path /hetzner-site/operator",
		"--projectId c52a0183-fc82-4614-b437-c237a889d79c",
		"./scripts/deploy_site.sh",
	} {
		requireContains(t, taskfile, fragment)
	}
}

func TestRemoteStateAndReviewedApplyWorkflow(t *testing.T) {
	backend := readFile(t, "backend.tf")
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "site-infrastructure.yml"))
	operatorPublicKey := readFile(t, "operator-ssh-key.pub")
	for _, fragment := range []string{
		`cloud {`,
		`organization = "Flid"`,
		`name = "leapview-site-production"`,
	} {
		requireContains(t, backend, fragment)
	}
	for _, fragment := range []string{
		"name: Plan or apply permanent public-site infrastructure",
		"workflow_dispatch:",
		"group: leapview-site-infrastructure",
		"environment: leapview-site-production",
		"terraform plan",
		"terraform show -no-color",
		"actions/upload-artifact@",
		"retention-days: 90",
		"needs: plan",
		"terraform apply",
		"terraform plan -detailed-exitcode",
		"TF_TOKEN_app_terraform_io",
		"TF_VAR_hcloud_token",
		"id-token: write",
		"Infisical/secrets-action@6cd3f7c0e4cc0d2395ee4ef414eb6eeb5d3e73db # v1.0.17",
		`method: "oidc"`,
		`identity-id: "7e92da75-ac4f-49f2-8924-4561c3547902"`,
		`oidc-audience: "https://github.com/flidai"`,
		`domain: "https://us.infisical.com"`,
		`project-slug: "leapview"`,
		`env-slug: "prod"`,
		`secret-path: "/hetzner-site/infrastructure"`,
		"deploy/hetzner-site/operator-ssh-key.pub",
	} {
		requireContains(t, workflow, fragment)
	}
	if got := strings.Count(workflow, "Infisical/secrets-action@"); got != 2 {
		t.Errorf("expected plan and apply to each fetch Infisical secrets, got %d uses", got)
	}
	if got := strings.Count(workflow, `domain: "https://us.infisical.com"`); got != 2 {
		t.Errorf("expected plan and apply to each use the Infisical US endpoint, got %d uses", got)
	}
	if !strings.HasPrefix(operatorPublicKey, "ssh-ed25519 ") {
		t.Errorf("operator public key must be a checked-in Ed25519 public key")
	}
	for _, forbidden := range []string{
		"terraform destroy", "pull_request:", "-auto-approve",
		"aws s3api", "TF_STATE_", "AWS_ENDPOINT_URL_S3",
		"secrets.HCP_API_TOKEN", "secrets.HCLOUD_TOKEN", "secrets.SITE_SSH_PUBLIC_KEY",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("permanent infrastructure workflow contains forbidden fragment %q", forbidden)
		}
	}
}

func TestMergeValidationValidatesPermanentSiteInfrastructure(t *testing.T) {
	taskfile := readFile(t, filepath.Join("..", "..", "Taskfile.yml"))
	mergeValidation := readFile(t, filepath.Join("..", "..", ".github", "workflows", "merge-validation.yml"))
	for _, fragment := range []string{
		"go test ./deploy/hetzner-site",
		"terraform fmt -check -recursive deploy/hetzner-site",
		"terraform -chdir=deploy/hetzner-site init -backend=false -input=false",
		"terraform -chdir=deploy/hetzner-site validate",
		"terraform -chdir=deploy/hetzner-site test",
	} {
		requireContains(t, taskfile, fragment)
	}
	for _, fragment := range []string{
		"runs-on: ubuntu-24.04",
		"uses: ./.github/actions/setup-ci",
		"run: task ci:full:extras",
	} {
		requireContains(t, mergeValidation, fragment)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	return readPath(t, path)
}

func readPath(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func writeFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	writeFile(t, path, contents, 0o700)
}

func siteImage(digit string) string {
	return "ghcr.io/flidai/leapview-site@sha256:" + strings.Repeat(digit, 64)
}

func materializeDeployScript(t *testing.T, root string) string {
	t.Helper()
	contents := readPath(t, filepath.Join("files", "deploy.sh"))
	const productionRoot = `site_root="/opt/leapview-site"`
	if strings.Count(contents, productionRoot) != 1 {
		t.Fatalf("deploy script must declare the production root exactly once")
	}
	contents = strings.Replace(contents, productionRoot, "site_root="+strconv.Quote(root), 1)
	path := filepath.Join(root, "deploy-under-test.sh")
	writeExecutable(t, path, contents)
	return path
}

func materializeReconcileScript(t *testing.T, root string) string {
	t.Helper()
	contents := readPath(t, filepath.Join("files", "reconcile.sh"))
	const productionRoot = `site_root="/opt/leapview-site"`
	if strings.Count(contents, productionRoot) != 1 {
		t.Fatalf("reconcile script must declare the production root exactly once")
	}
	contents = strings.Replace(contents, productionRoot, "site_root="+strconv.Quote(root), 1)
	path := filepath.Join(root, "reconcile-under-test.sh")
	writeExecutable(t, path, contents)
	return path
}

func requireContains(t *testing.T, contents, fragment string) {
	t.Helper()
	if !strings.Contains(contents, fragment) {
		t.Fatalf("missing %q", fragment)
	}
}
