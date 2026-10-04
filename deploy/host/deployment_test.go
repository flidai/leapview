package host_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapIsProviderNeutralAndDelegatesLifecycleToGo(t *testing.T) {
	bootstrap := read(t, "bootstrap-linux.sh")
	info, err := os.Stat("bootstrap-linux.sh")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("bootstrap-linux.sh must be executable")
	}
	if output, err := exec.Command("bash", "-n", "bootstrap-linux.sh").CombinedOutput(); err != nil {
		t.Fatalf("bash -n bootstrap-linux.sh: %v\n%s", err, output)
	}
	for _, required := range []string{
		"ubuntu:24.04) compose_package=docker-compose-v2",
		"debian:13) compose_package=docker-compose",
		"requires Ubuntu 24.04 LTS or Debian 13", "\"$compose_package\"", "docker compose version",
		"docker pull", "docker create", "docker cp",
		`leapviewctl" host install`, "repository@sha256",
	} {
		requireContains(t, bootstrap, required)
	}
	for _, forbidden := range []string{
		"docker compose up", "docker compose down", "leapviewctl init", "leapviewctl start", "terraform", "hcloud", "hetzner", "netcup",
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
		"python3 scripts/package_compose_bundle.py assemble",
		`--source-root . --controller "$controller"`,
	} {
		requireContains(t, release, required)
	}
}

func TestHostOperationalScriptsAreSyntacticallyValid(t *testing.T) {
	for _, path := range []string{filepath.Join("files", "leapviewctl-wrapper")} {
		if output, err := exec.Command("bash", "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("bash -n %s: %v\n%s", path, err, output)
		}
	}
	wrapper := read(t, filepath.Join("files", "leapviewctl-wrapper"))
	if strings.Contains(wrapper, "LEAPVIEWCTL_BACKUP_HOOK") {
		t.Fatal("host wrapper must not configure an application backup hook")
	}
}

func TestNixOSPrerequisitesModuleDoesNotOwnApplicationLifecycle(t *testing.T) {
	module := read(t, "nixos.nix")
	for _, required := range []string{
		"virtualisation.docker.enable = true;",
		"virtualisation.docker.enableOnBoot = true;",
		"python3",
		"openssl",
		"coreutils",
		"findutils",
		"util-linux",
		"gnutar",
		"gzip",
		"xz",
		"programs.nix-ld",
		"pkgs.stdenv.cc.cc.lib",
		"networking.firewall.allowedTCPPorts = [ 80 443 ];",
		"networking.firewall.allowedUDPPorts = [ 443 ];",
		"export LEAPVIEWCTL_ROOT=/opt/leapview",
		"exec /opt/leapview/leapviewctl \"$@\"",
	} {
		requireContains(t, module, required)
	}
	for _, forbidden := range []string{
		"systemd.services",
		"virtualisation.oci-containers",
		"docker compose up",
		"leapviewctl host install",
		"5432",
	} {
		if strings.Contains(module, forbidden) {
			t.Errorf("NixOS prerequisites module contains lifecycle or public database fragment %q", forbidden)
		}
	}

	readme := read(t, "README.md")
	for _, required := range []string{
		"NixOS host prerequisites",
		"inputs.leapview.outPath + \"/deploy/host/nixos.nix\"",
		"nixos-rebuild switch",
		"Ubuntu 24.04 and Debian 13",
		"sudo -n leapviewctl version",
	} {
		requireContains(t, readme, required)
	}
}

func TestHostPayloadOmitsApplicationBackupAssets(t *testing.T) {
	for _, path := range []string{
		filepath.Join("files", "leapview-backup-hook"),
		filepath.Join("files", "leapview-backup.service"),
		filepath.Join("files", "leapview-backup.timer"),
		filepath.Join("files", "leapview-backup-maintenance.service"),
		filepath.Join("files", "leapview-backup-maintenance.timer"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("obsolete host backup asset %s is still present", path)
		}
	}
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
