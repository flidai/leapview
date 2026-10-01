package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestProductionRuntimeUsesPinnedMinimalDebianBase(t *testing.T) {
	root := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)

	for _, required := range []string{
		"FROM gcr.io/distroless/cc-debian13:debug-nonroot@sha256:",
		"SHELL [\"/busybox/sh\", \"-c\"]",
		"test \"$(id -u leapview)\" = 999",
		"mkdir -p /var/lib/leapview/home && \\\n    chown -R leapview:leapview /var/lib/leapview /app",
		"USER leapview:leapview",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing minimal runtime fragment %q", required)
		}
	}
	for _, forbidden := range []string{"apt-get", "debian-bookworm.sources"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Fatalf("minimal runtime unexpectedly contains %q", forbidden)
		}
	}

	siteDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.site"))
	if err != nil {
		t.Fatalf("read Dockerfile.site: %v", err)
	}
	if strings.Contains(string(siteDockerfile), "apt-get install") {
		t.Fatal("distroless site runtime unexpectedly installs OS packages")
	}
	qualificationDockerfile, err := os.ReadFile(filepath.Join(root, "deploy", "compose", "qualification", "Dockerfile.authoring-client"))
	if err != nil {
		t.Fatalf("read authoring qualification Dockerfile: %v", err)
	}
	if strings.Contains(string(qualificationDockerfile), "deb.debian.org") || strings.Contains(string(qualificationDockerfile), "snapshot.debian.org") {
		t.Fatal("qualification image must not override its pinned base's package sources")
	}
}

func TestRuntimeOpenSSLRepairUsesChecksummedDebianPackages(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot(t), "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	_, stage, ok := strings.Cut(string(data), "FROM node AS runtime-security-update")
	if !ok {
		t.Fatal("runtime security package stage is missing")
	}
	stage, _, _ = strings.Cut(stage, "FROM gcr.io/distroless/")
	for _, invariant := range []string{
		"openssl_deb_version=3.5.7-1~deb13u3",
		"for package in libssl3t64 openssl-provider-legacy; do",
		"https://security.debian.org/debian-security/pool/updates/main/o/openssl/",
		"amd64)", "arm64)", "unsupported runtime package architecture",
		"dpkg-deb --control", "$control_dir/control", "$control_dir/md5sums",
		"var/lib/dpkg/status.d/$package", "var/lib/dpkg/status.d/$package.md5sums",
	} {
		if !strings.Contains(stage, invariant) {
			t.Errorf("runtime package stage lacks %q", invariant)
		}
	}
	hashes := regexp.MustCompile(`(?:libssl|provider)_sha256=([0-9a-f]{64})`).FindAllStringSubmatch(stage, -1)
	distinct := make(map[string]bool)
	for _, match := range hashes {
		distinct[match[1]] = true
	}
	if len(hashes) != 4 || len(distinct) != 4 {
		t.Fatal("both architectures need separate SHA256 pins for both vendor packages")
	}
	verify, extract := strings.Index(stage, "sha256sum --check --status ||"), strings.Index(stage, "dpkg-deb --extract")
	if verify < 0 || extract < 0 || verify >= extract || !strings.Contains(stage[verify:extract], "exit 1") {
		t.Fatal("package extraction must follow fail-closed checksum verification")
	}
}
