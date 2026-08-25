package hetzner_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTerraformProductionContracts(t *testing.T) {
	variables := readFile(t, "variables.tf")
	main := readFile(t, "main.tf")
	cloudInit := readFile(t, filepath.Join("..", "host", "cloud-init.yaml.tftpl"))
	requireContains(t, variables, `variable "leapview_image"`)
	requireContains(t, variables, `variable "release_transition_policy_path"`)
	requireContains(t, variables, `@sha256:`)
	requireContains(t, variables, `variable "ssh_allowed_cidrs"`)
	if strings.Contains(variables, `default     = ["0.0.0.0/0", "::/0"]`) {
		t.Fatal("SSH must not be open to the world by default")
	}
	if strings.Contains(variables, `variable "repo_ref"`) || strings.Contains(variables, `variable "repo_url"`) {
		t.Fatal("production deployment must consume an image, not mutable source refs")
	}
	requireMatch(t, main, `(?m)^\s*backups\s*=\s*true\s*$`)
	requireMatch(t, main, `(?m)^\s*shutdown_before_deletion\s*=\s*true\s*$`)
	if strings.Contains(cloudInit, "leapviewctl_b64") {
		t.Fatal("cloud-init must not embed a source-tree controller script")
	}
	if strings.Contains(cloudInit, "git clone") || strings.Contains(cloudInit, "docker build") {
		t.Fatal("cloud-init must not clone or build application source")
	}
}

func TestHetznerConsumesGenericComposeLifecycle(t *testing.T) {
	main := readFile(t, "main.tf")
	for _, fragment := range []string{
		`${path.module}/../host/cloud-init.yaml.tftpl`,
		`${path.module}/../host/bootstrap-ubuntu.sh`,
		`jsonencode({`,
		`schemaVersion = 1`,
	} {
		requireContains(t, main, fragment)
	}
	for _, forbidden := range []string{
		"compose_b64", "compose_https_b64", "caddyfile_b64", "leapviewctl_wrapper_b64",
		"backup_hook_b64", "provision_b64", "docker compose", "leapviewctl init", "leapviewctl start",
	} {
		if strings.Contains(main, forbidden) {
			t.Fatalf("provider provisioning maintains lifecycle fragment %q", forbidden)
		}
	}
}

func TestProviderScriptsAreSmallValidLayers(t *testing.T) {
	entries, err := os.ReadDir("files")
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("provider directory contains host implementation files: %v", entries)
	}
}

func TestReleaseWorkflowPublishesComposeArchiveAndAttestedImage(t *testing.T) {
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	for _, fragment := range []string{
		"tags:", "needs: [image, qualify, minio-conformance, plan-gc-conformance]", "gh release create",
		"packages: write", "attestations: write", "id-token: write",
		"docker/build-push-action@", "actions/attest@", "push-to-registry: true",
		"leapview-compose-", "deployment.env.example", ".tar.gz.sha256", "./cmd/leapviewctl",
	} {
		requireContains(t, workflow, fragment)
	}
}

func TestPublicSiteImagePublicationContract(t *testing.T) {
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "site-image.yml"))
	for _, fragment := range []string{
		"name: Publish public site image",
		"workflow_dispatch:",
		"workflow_call:",
		"IMAGE_NAME: ghcr.io/flidai/leapview-site",
		"packages: write",
		"attestations: write",
		"id-token: write",
		"runner: ubuntu-24.04",
		"runner: ubuntu-24.04-arm",
		"platforms: linux/${{ matrix.arch }}",
		"file: Dockerfile.site",
		"push: true",
		"sbom: true",
		"provenance: mode=max",
		"org.opencontainers.image.version=",
		"org.opencontainers.image.revision=",
		"actions/attest@",
		"docker buildx imagetools create",
		"site-image-reference.txt",
		"GITHUB_STEP_SUMMARY",
		"docker logout ghcr.io",
		`docker buildx imagetools inspect "$IMAGE_REFERENCE"`,
		`docker image inspect "$IMAGE_REFERENCE"`,
		"docker run --detach",
		"/healthz",
		"/readyz",
		"/build.json",
		"/release.json",
		"/docs/installation",
		"refs/heads/main",
	} {
		requireContains(t, workflow, fragment)
	}
	for _, forbidden := range []string{
		"pull_request:",
		"docker/setup-qemu-action@",
		"ghcr.io/flidai/leapview-site:latest",
		"git clone",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("public site image publication contains forbidden fragment %q", forbidden)
		}
	}
}

func TestPublicSiteImageGeneratesEmbeddedRuntimeAssets(t *testing.T) {
	dockerfile := readFile(t, filepath.Join("..", "..", "Dockerfile.site"))
	generateVisualDocs := strings.Index(dockerfile, "go run -tags=duckdb_arrow ./internal/app/tools/visualdocgen")
	buildSite := strings.Index(dockerfile, "go build -trimpath")
	if generateVisualDocs < 0 {
		t.Fatal("public site image must generate visual documentation with the analytical runtime enabled")
	}
	if buildSite < 0 || generateVisualDocs > buildSite {
		t.Fatal("public site image must generate visual documentation before compiling the server")
	}
	requireContains(t, dockerfile, "test -f docs/visuals/examples.gen.json")
}

func TestSupplyChainInputsArePinned(t *testing.T) {
	for _, name := range []string{"Dockerfile", "Dockerfile.site"} {
		dockerfile := readFile(t, filepath.Join("..", "..", name))
		if !strings.HasPrefix(dockerfile, "# syntax=docker/dockerfile:1.7@sha256:") {
			t.Errorf("%s frontend is not pinned by digest", name)
		}
		assertDockerfileImagesPinned(t, name, dockerfile)
	}
	workflows, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	mutableAction := regexp.MustCompile(`(?m)^\s*uses:\s+[^#\s]+@v[0-9]+(?:\s|$)`)
	for _, workflow := range workflows {
		if match := mutableAction.FindString(readFile(t, workflow)); match != "" {
			t.Errorf("GitHub Action is not pinned by commit in %s: %s", workflow, strings.TrimSpace(match))
		}
	}
}

func assertDockerfileImagesPinned(t *testing.T, name, dockerfile string) {
	t.Helper()
	stages := make(map[string]struct{})
	hexDigest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, line := range strings.Split(dockerfile, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "FROM" {
			continue
		}
		image := fields[1]
		if _, internal := stages[image]; !internal && image != "scratch" {
			_, digest, pinned := strings.Cut(image, "@sha256:")
			if !pinned || !hexDigest.MatchString(digest) {
				t.Errorf("%s base image is not pinned by a valid SHA-256 digest: %s", name, line)
			}
		}
		if len(fields) >= 4 && fields[2] == "AS" {
			stages[fields[3]] = struct{}{}
		}
	}
}

func TestEphemeralDeploymentExercisesPublicAndBackupContracts(t *testing.T) {
	workflow := readFile(t, filepath.Join("..", "..", ".github", "workflows", "hetzner-deploy.yml"))
	for _, fragment := range []string{
		"workflow_dispatch:", "environment: leapview-ephemeral-qualification", "terraform apply",
		"public_ready=false", "--connect-timeout 5", "leapviewctl backup", "leapviewctl restore",
		"leapview-backup-hook --init", "restic restore latest", "leapview-backup-hook --maintain",
		`.publisherToken`, "if: always()", "terraform destroy",
		"id-token: write",
		"attestations: read",
		"source_revision:",
		"uses: ./.github/actions/oci-admission",
		"expected-workflow: flidai/leapview/.github/workflows/artifacts.yml",
		"source-revision: ${{ inputs.source_revision }}",
			"TF_VAR_leapview_image=${{ steps.admission.outputs.image }}",
			"TF_VAR_release_transition_policy_path",
		"Infisical/secrets-action@77ab1f4ccd183a543cb5b42435fbd181189f4995 # v1.0.16",
		`method: "oidc"`,
		`identity-id: "6aac9c3e-4f33-45b5-aa4e-884839b950a7"`,
		`oidc-audience: "https://github.com/flidai"`,
		`domain: "https://us.infisical.com"`,
		`project-slug: "leapview"`,
		`env-slug: "prod"`,
		`secret-path: "/hetzner-qualification/infrastructure"`,
	} {
		requireContains(t, workflow, fragment)
	}
	if strings.Contains(workflow, "pull_request:") {
		t.Fatal("cloud deployment must require an explicit, environment-protected dispatch")
	}
	for _, forbidden := range []string{
		"environment: hetzner-deployment",
		"secrets.HCLOUD_TOKEN",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("ephemeral deployment workflow contains forbidden fragment %q", forbidden)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func requireContains(t *testing.T, contents, fragment string) {
	t.Helper()
	if !strings.Contains(contents, fragment) {
		t.Fatalf("missing %q", fragment)
	}
}

func requireMatch(t *testing.T, contents, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(contents) {
		t.Fatalf("missing pattern %q", pattern)
	}
}
