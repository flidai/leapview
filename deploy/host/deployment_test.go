package host_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/compatibility"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
)

func TestBootstrapIsProviderNeutralAndDelegatesLifecycleToGo(t *testing.T) {
	bootstrap := read(t, "bootstrap-ubuntu.sh")
	if output, err := exec.Command("bash", "-n", "bootstrap-ubuntu.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n bootstrap-ubuntu.sh: %v\n%s", err, output)
	}
	for _, required := range []string{
		"Ubuntu 24.04 LTS", "docker-compose-v2", "docker pull", "docker create", "docker cp",
		`leapviewctl" host install`, "repository@sha256", "release-transition-policy.json",
	} {
		requireContains(t, bootstrap, required)
	}
	for _, forbidden := range []string{
		"docker compose", "leapviewctl init", "leapviewctl start", "terraform", "hcloud", "hetzner", "netcup",
	} {
		if strings.Contains(strings.ToLower(bootstrap), forbidden) {
			t.Errorf("bootstrap contains lifecycle/provider fragment %q", forbidden)
		}
	}
}

func TestCloudInitOnlyDeliversBootstrapInputs(t *testing.T) {
	cloudInit := read(t, "cloud-init.yaml.tftpl")
	for _, required := range []string{
		"bootstrap_b64", "config_b64", "image_b64", "policy_b64", "/usr/local/sbin/leapview-bootstrap",
	} {
		requireContains(t, cloudInit, required)
	}
	for _, forbidden := range []string{"compose.yaml", "Caddyfile", "leapviewctl init", "docker compose"} {
		if strings.Contains(cloudInit, forbidden) {
			t.Errorf("cloud-init contains application lifecycle fragment %q", forbidden)
		}
	}
}

func TestProductionImageCarriesCanonicalDeploymentPayload(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfile := read(t, filepath.Join(root, "Dockerfile"))
	for _, required := range []string{
		"/usr/local/share/leapview/deployment/",
		"deploy/compose/compose.yaml",
		"deploy/host/files/",
		"deployment/leapviewctl",
		"release-transition-policy.json",
	} {
		requireContains(t, dockerfile, required)
	}
	release := read(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	for _, required := range []string{
		"deploy/host/files/leapviewctl-wrapper",
		"deploy/host/files/leapview-backup-hook",
		"deploy/host/files/leapview-backup.service",
		"deploy/host/files/leapview-backup.timer",
		"deploy/host/files/leapview-backup-maintenance.service",
		"deploy/host/files/leapview-backup-maintenance.timer",
		"deploy/host/bootstrap-ubuntu.sh",
	} {
		requireContains(t, release, required)
	}
}

func TestHostOperationalScriptsAreSyntacticallyValid(t *testing.T) {
	for _, path := range []string{
		filepath.Join("files", "leapviewctl-wrapper"),
		filepath.Join("files", "leapview-backup-hook"),
	} {
		if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
	hook := read(t, filepath.Join("files", "leapview-backup-hook"))
	for _, required := range []string{
		"--init", "--maintain", "restic check",
		"/usr/local/sbin/leapviewctl", "host reconcile-recovery-qualification",
	} {
		requireContains(t, hook, required)
	}
	if strings.Contains(hook, "snapshots >/dev/null 2>&1 || restic init") {
		t.Fatal("backup failures must not implicitly initialize a restic repository")
	}
}

func TestBackupHookMigratesPR368InstallationThroughRealController(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "opt", "leapview")
	configDir := filepath.Join(base, "etc", "leapview")
	systemBin := filepath.Join(base, "usr", "local", "sbin")
	systemd := filepath.Join(base, "etc", "systemd", "system")
	bin := filepath.Join(base, "usr", "bin")
	fullPayload := filepath.Join(base, "fai-516-payload")
	stateRoot := filepath.Join(base, "var", "lib", "leapview")
	for _, directory := range []string{root, configDir, systemBin, systemd, bin, fullPayload, stateRoot} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	policy, identity := hostMigrationPolicy(t)
	policyDocument, err := compatibility.MarshalPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	controller := filepath.Join(fullPayload, "leapviewctl")
	buildHermeticHostController(t, controller, base, identity)
	if err := os.Chmod(controller, 0o700); err != nil {
		t.Fatal(err)
	}
	writeHostMigrationPayload(t, fullPayload, base, policyDocument)

	generationName := "sha256-" + strings.TrimPrefix(identity.Image[strings.LastIndex(identity.Image, "@")+1:], "sha256:")
	generation := filepath.Join(root, "releases", generationName)
	if err := os.MkdirAll(generation, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, relative := range legacyHostPayloadFiles {
		copyFile(t, filepath.Join(fullPayload, relative), filepath.Join(generation, relative))
	}
	if err := os.Symlink(filepath.Join("releases", generationName), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	installLegacyHostLinks(t, root, systemBin, systemd)
	resolvedController, err := filepath.EvalSymlinks(filepath.Join(root, "leapviewctl"))
	if err != nil {
		t.Fatal(err)
	}
	if resolvedController != filepath.Join(generation, "leapviewctl") {
		t.Fatalf("installed controller resolves to %s, want release generation %s", resolvedController, generation)
	}

	marker := map[string]any{
		"schemaVersion": 1, "domain": "dash.example.com", "adminEmail": "admin@example.com",
		"environment": "prod", "image": identity.Image, "https": true,
	}
	markerDocument, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".host-install.json"), markerDocument, 0o600)
	writeFile(t, filepath.Join(root, "deployment.env"), []byte(strings.Join([]string{
		"LEAPVIEW_IMAGE=" + identity.Image,
		"CADDY_IMAGE=caddy@sha256:" + strings.Repeat("d", 64),
		"COMPOSE_HTTPS=0",
	}, "\n")+"\n"), 0o600)
	writeFile(t, filepath.Join(root, "leapview.env"), []byte(strings.Join([]string{
		"LEAPVIEW_PRODUCTION=1",
		"LEAPVIEW_ENVIRONMENT=prod",
		"LEAPVIEW_HOME=/var/lib/leapview/home",
		"LEAPVIEW_MANAGED_DATA_BACKEND=local",
		"LEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data",
	}, "\n")+"\n"), 0o600)

	home := filepath.Join(stateRoot, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(t.Context(), "prod"); err != nil {
		t.Fatal(err)
	}
	instanceID, err := store.InstanceID(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	docker := filepath.Join(bin, "docker")
	writeFakeHostRuntime(t, docker, fullPayload, stateRoot)
	writeFakeSystemctl(t, filepath.Join(bin, "systemctl"), filepath.Join(systemBin, "leapviewctl"))

	command := exec.Command(filepath.Join(systemBin, "leapview-backup-hook"), "--maintain")
	command.Env = append(environmentWithout(os.Environ(), "LEAPVIEWCTL_ROOT", "LEAPVIEWCTL_HOST_CONTROLLER"),
		"LEAPVIEWCTL_DOCKER_BIN="+docker,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute installed backup hook: %v\n%s", err, output)
	}

	qualifiedGeneration, err := os.Readlink(filepath.Join(root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if qualifiedGeneration != filepath.Join("releases", generationName+"-qualification-v1") {
		t.Fatalf("active generation = %q, want complete qualification generation", qualifiedGeneration)
	}
	for _, relative := range qualificationHostPayloadFiles {
		if _, err := os.Stat(filepath.Join(root, "current", relative)); err != nil {
			t.Fatalf("qualification asset %s was not installed: %v", relative, err)
		}
	}
	applicationEnvironment, err := os.ReadFile(filepath.Join(root, "leapview.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"LEAPVIEW_RECOVERY_QUALIFICATION_ENABLED=true",
		"LEAPVIEW_RECOVERY_QUALIFICATION_EXECUTION_ENVIRONMENT=host",
	} {
		if !strings.Contains(string(applicationEnvironment), required+"\n") {
			t.Fatalf("managed qualification configuration is missing %s", required)
		}
	}
	for _, unit := range []string{"leapview-recovery-qualification.service", "leapview-recovery-qualification.timer"} {
		if _, err := os.Stat(filepath.Join(systemd, unit)); err != nil {
			t.Fatalf("qualification unit %s was not installed: %v", unit, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "timer-enabled")); err != nil {
		t.Fatalf("qualification timer was not enabled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "qualification-started")); err != nil {
		t.Fatalf("post-migration qualification validation did not run: %v", err)
	}

	store, err = platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
		UPDATE recovery_qualification_schedules
		SET next_run_at = ?
		WHERE closed_at IS NULL AND operation = 'backup'
	`, time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05.000000000Z")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	qualification := exec.Command(filepath.Join(systemBin, "leapviewctl"), "qualify", "scheduled-recovery")
	qualification.Env = append(environmentWithout(os.Environ(), "LEAPVIEWCTL_ROOT", "LEAPVIEWCTL_HOST_CONTROLLER"),
		"LEAPVIEWCTL_DOCKER_BIN="+docker,
	)
	if output, err := qualification.CombinedOutput(); err != nil {
		t.Fatalf("execute installed qualification owner: %v\n%s", err, output)
	}

	store, err = platform.Open(t.Context(), filepath.Join(home, "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	occurrences, err := refreshmodule.NewRecoveryRepository(store.SQLDB()).Occurrences(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policySum := sha256.Sum256(policyDocument)
	policyDigest := hex.EncodeToString(policySum[:])
	var backupOccurrence *refreshmodule.Occurrence
	for index := range occurrences {
		if occurrences[index].Operation == refreshmodule.OperationBackup {
			backupOccurrence = &occurrences[index]
			break
		}
	}
	if backupOccurrence == nil || backupOccurrence.Status != refreshmodule.StatusSucceeded || backupOccurrence.EvidenceStatus != "published" {
		t.Fatalf("owner-validated backup occurrence was not published: %#v", backupOccurrence)
	}
	if backupOccurrence.ArtifactIdentity != identity.Image || backupOccurrence.PolicySHA256 != policyDigest || len(backupOccurrence.Evidence) != 1 {
		t.Fatalf("backup occurrence lost scheduled identity: %#v", backupOccurrence)
	}
	evidence := backupOccurrence.Evidence[0]
	document, err := os.ReadFile(filepath.Join(home, "artifacts", "recovery-qualification", evidence.SHA256+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := platform.ValidateInstanceBackupManifestDocument(document, platform.InstanceBackupEvidenceExpectation{
		ArtifactIdentity: identity.Image,
		PolicyVersion:    policy.PolicyVersion,
		PolicySHA256:     policyDigest,
		TargetScope:      "instance:" + instanceID,
	}); err != nil {
		t.Fatalf("retained FAI-515 owner evidence is invalid: %v", err)
	}
}

var legacyHostPayloadFiles = []string{
	"leapviewctl", "release-transition-policy.json", "compose.yaml", "compose.https.yaml", "Caddyfile",
	"deployment.env.example", "leapviewctl-wrapper", "leapview-backup-hook", "leapview-backup.service",
	"leapview-backup.timer", "leapview-backup-maintenance.service", "leapview-backup-maintenance.timer",
}

var qualificationHostPayloadFiles = []string{
	"leapview.env.example", "README.md", "QUALIFICATION.md",
	filepath.Join("qualification", "Dockerfile.authoring-client"), filepath.Join("qualification", "authoring-worker.mjs"),
	filepath.Join("qualification", "browser.mjs"), filepath.Join("qualification", "bun.lock"),
	filepath.Join("qualification", "package.json"), filepath.Join("qualification", "performance-policy.json"),
	filepath.Join("qualification", "performance.mjs"), "leapview-recovery-qualification.service",
	"leapview-recovery-qualification.timer",
}

func hostMigrationPolicy(t *testing.T) (*compatibility.Policy, compatibility.ReleaseIdentity) {
	t.Helper()
	base, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	template, err := compatibility.EmbeddedCandidateTransitionTemplate()
	if err != nil {
		t.Fatal(err)
	}
	identity := compatibility.ReleaseIdentity{
		ReleaseID: "v0.2.0-rc.2", Version: "0.2.0-rc.2", SourceRevision: strings.Repeat("2", 40),
		Image: "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64), Distribution: "public",
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	policy, err := base.BindCandidateWithTemplate(identity, template.Platforms, template)
	if err != nil {
		t.Fatal(err)
	}
	return policy, identity
}

func buildHermeticHostController(t *testing.T, target, rootfs string, identity compatibility.ReleaseIdentity) {
	t.Helper()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	overlayDir := filepath.Join(filepath.Dir(target), ".overlay")
	if err := os.MkdirAll(overlayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	overlay := make(map[string]string)
	for _, relative := range []string{
		filepath.Join("internal", "app", "cli", "hostinstall", "install.go"),
		filepath.Join("internal", "app", "cli", "hostinstall", "command.go"),
	} {
		source := filepath.Join(repositoryRoot, relative)
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(relative, "install.go") {
			replacements := map[string]string{
				`ConfigDir: "/etc/leapview"`:       `ConfigDir: "` + filepath.Join(rootfs, "etc", "leapview") + `"`,
				`SystemBin: "/usr/local/sbin"`:     `SystemBin: "` + filepath.Join(rootfs, "usr", "local", "sbin") + `"`,
				`Systemd:   "/etc/systemd/system"`: `Systemd:   "` + filepath.Join(rootfs, "etc", "systemd", "system") + `"`,
				`Systemctl: "systemctl"`:           `Systemctl: "` + filepath.Join(rootfs, "usr", "bin", "systemctl") + `"`,
			}
			for before, after := range replacements {
				if !strings.Contains(string(contents), before) {
					t.Fatalf("host path overlay contract no longer contains %q", before)
				}
				contents = []byte(strings.Replace(string(contents), before, after, 1))
			}
		} else {
			const rootGuard = "os.Geteuid() != 0"
			if strings.Count(string(contents), rootGuard) != 2 {
				t.Fatalf("host command root guard contract changed")
			}
			contents = []byte(strings.ReplaceAll(string(contents), rootGuard, "false"))
		}
		destination := filepath.Join(overlayDir, filepath.Base(relative))
		writeFile(t, destination, contents, 0o600)
		overlay[source] = destination
	}
	overlayDocument, err := json.Marshal(map[string]any{"Replace": overlay})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(overlayDir, "overlay.json")
	writeFile(t, overlayPath, overlayDocument, 0o600)
	ldflags := strings.Join([]string{
		"-X github.com/flidai/leapview/internal/platform/buildinfo.version=" + identity.Version,
		"-X github.com/flidai/leapview/internal/platform/buildinfo.revision=" + identity.SourceRevision,
		"-X github.com/flidai/leapview/internal/platform/buildinfo.buildTime=2026-08-26T00:00:00Z",
		"-X github.com/flidai/leapview/internal/platform/buildinfo.dirty=false",
		"-X github.com/flidai/leapview/internal/platform/buildinfo.release=true",
	}, " ")
	command := exec.Command("go", "build", "-overlay", overlayPath, "-ldflags", ldflags, "-o", target, "./cmd/leapviewctl")
	command.Dir = repositoryRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build real hermetic leapviewctl: %v\n%s", err, output)
	}
}

func writeHostMigrationPayload(t *testing.T, root, rootfs string, policy []byte) {
	t.Helper()
	allFiles := append(append([]string{}, legacyHostPayloadFiles...), qualificationHostPayloadFiles...)
	for _, relative := range allFiles {
		if relative == "leapviewctl" {
			continue
		}
		var contents []byte
		source := filepath.Join("files", relative)
		if value, err := os.ReadFile(source); err == nil {
			contents = value
		} else {
			contents = []byte(relative + "\n")
		}
		if relative == "release-transition-policy.json" {
			contents = policy
		}
		if relative == "deployment.env.example" {
			contents = []byte("LEAPVIEW_IMAGE=unused\nCADDY_IMAGE=caddy@sha256:" + strings.Repeat("d", 64) + "\nCOMPOSE_HTTPS=0\n")
		}
		if relative == "leapviewctl-wrapper" || relative == "leapview-backup-hook" {
			contents = []byte(strings.NewReplacer(
				"/opt/leapview", filepath.Join(rootfs, "opt", "leapview"),
				"/usr/local/sbin", filepath.Join(rootfs, "usr", "local", "sbin"),
				"/etc/leapview", filepath.Join(rootfs, "etc", "leapview"),
			).Replace(string(contents)))
		}
		mode := os.FileMode(0o600)
		if relative == "leapviewctl-wrapper" || relative == "leapview-backup-hook" {
			mode = 0o700
		} else if strings.HasSuffix(relative, ".service") || strings.HasSuffix(relative, ".timer") {
			mode = 0o644
		}
		writeFile(t, filepath.Join(root, relative), contents, mode)
	}
}

func installLegacyHostLinks(t *testing.T, root, systemBin, systemd string) {
	t.Helper()
	for _, relative := range legacyHostPayloadFiles {
		target := filepath.Join(root, relative)
		switch {
		case relative == "leapviewctl-wrapper":
			target = filepath.Join(systemBin, "leapviewctl")
		case relative == "leapview-backup-hook":
			target = filepath.Join(systemBin, relative)
		case strings.HasSuffix(relative, ".service") || strings.HasSuffix(relative, ".timer"):
			target = filepath.Join(systemd, relative)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(root, "current", relative)
		relativeSource, err := filepath.Rel(filepath.Dir(target), source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(relativeSource, target); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFakeHostRuntime(t *testing.T, path, payload, stateRoot string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
case " $* " in
  *" image inspect "*) exit 0 ;;
  *" create "*) printf 'payload-container\n'; exit 0 ;;
  *" cp payload-container:"*) cp -R "` + payload + `/". "$3"; exit 0 ;;
  *" rm --force payload-container "*) exit 0 ;;
  *" version --format "*"Server.Os"*) printf '` + runtime.GOOS + `/` + runtime.GOARCH + `\n'; exit 0 ;;
  *" version --format "*) printf '27.0.0\n'; exit 0 ;;
  *" compose "*" ps -q leapview "*) printf 'leapview-container\n'; exit 0 ;;
  *" inspect --format "*"leapview-container "*) printf '` + stateRoot + `\n'; exit 0 ;;
esac
printf 'unexpected fake runtime command: %s\n' "$*" >&2
exit 1
`
	writeFile(t, path, []byte(script), 0o700)
}

func writeFakeSystemctl(t *testing.T, path, controller string) {
	t.Helper()
	base := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	script := `#!/bin/sh
set -eu
case "${1:-}" in
  is-enabled) test -f "` + filepath.Join(base, "timer-enabled") + `" ;;
  daemon-reload|link) exit 0 ;;
  enable)
    : >"` + filepath.Join(base, "timer-enabled") + `"
    exit 0
    ;;
  disable)
    rm -f "` + filepath.Join(base, "timer-enabled") + `"
    exit 0
    ;;
  start)
    if [ "${2:-}" = "leapview-recovery-qualification.service" ]; then
      "` + controller + `" qualify scheduled-recovery
      : >"` + filepath.Join(base, "qualification-started") + `"
    fi
    exit 0
    ;;
esac
printf 'unexpected fake systemctl command: %s\n' "$*" >&2
exit 1
`
	writeFile(t, path, []byte(script), 0o700)
}

func environmentWithout(values []string, names ...string) []string {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		name, _, _ := strings.Cut(value, "=")
		if !blocked[name] {
			result = append(result, value)
		}
	}
	return result
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, destination, contents, info.Mode().Perm())
}

func writeFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func TestBackupSchedulingSeparatesCreationFromMaintenance(t *testing.T) {
	backupService := read(t, filepath.Join("files", "leapview-backup.service"))
	maintenanceService := read(t, filepath.Join("files", "leapview-backup-maintenance.service"))
	maintenanceTimer := read(t, filepath.Join("files", "leapview-backup-maintenance.timer"))
	for _, service := range []string{backupService, maintenanceService} {
		for _, required := range []string{
			"UMask=0077", "NoNewPrivileges=yes", "PrivateTmp=yes", "ProtectSystem=strict",
			"ProtectHome=yes", "ReadWritePaths=/opt/leapview", "TimeoutStartSec=",
		} {
			requireContains(t, service, required)
		}
	}
	requireContains(t, maintenanceService, "leapview-backup-hook --maintain")
	requireContains(t, maintenanceTimer, "OnCalendar=Sun")
}

func read(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func requireContains(t *testing.T, contents, fragment string) {
	t.Helper()
	if !strings.Contains(contents, fragment) {
		t.Fatalf("missing %q", fragment)
	}
}
