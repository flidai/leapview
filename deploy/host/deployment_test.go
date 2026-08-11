package host_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapIsProviderNeutralAndDelegatesLifecycleToGo(t *testing.T) {
	bootstrap := read(t, "bootstrap-ubuntu.sh")
	if output, err := exec.Command("bash", "-n", "bootstrap-ubuntu.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n bootstrap-ubuntu.sh: %v\n%s", err, output)
	}
	for _, required := range []string{
		"Ubuntu 24.04 LTS", "docker-compose-v2", "docker pull", "docker create", "docker cp",
		`leapviewctl" host install`, "repository@sha256",
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
		"bootstrap_b64", "config_b64", "image_b64", "/usr/local/sbin/leapview-bootstrap",
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
	for _, required := range []string{"--init", "--maintain", "restic check"} {
		requireContains(t, hook, required)
	}
	if strings.Contains(hook, "snapshots >/dev/null 2>&1 || restic init") {
		t.Fatal("backup failures must not implicitly initialize a restic repository")
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
