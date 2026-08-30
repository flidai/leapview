package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionRuntimeUsesPinnedDistrolessCompatibilityImage(t *testing.T) {
	root := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	coverageBytes, err := os.ReadFile(filepath.Join(root, ".security", "coverage.yaml"))
	if err != nil {
		t.Fatalf("read security coverage: %v", err)
	}
	dockerfile := string(dockerfileBytes)
	coverage := string(coverageBytes)
	const runtimeImage = "gcr.io/distroless/cc-debian12:debug-nonroot@sha256:923320b891f20d5f4bd43ed3a72eeee2f3323d481d6f4bd8d0b2c96d1c0758bc"
	for _, required := range []string{
		"FROM " + runtimeImage + " AS runtime",
		`SHELL ["/busybox/sh", "-c"]`,
		`printf '%s\n' 'leapview:x:999:' >> /etc/group`,
		`printf '%s\n' 'leapview:x:999:999::/var/lib/leapview:/sbin/nologin' >> /etc/passwd`,
		`test "$(id -u leapview)" = 999`,
		`test "$(id -g leapview)" = 999`,
		"for utility in sh env cat cp rm mkdir find du wc test stat readlink sha256sum tar gzip gunzip sync; do",
		`command -v "$utility" >/dev/null || exit 1`,
		"mkdir -p /var/lib/leapview",
		"chown -R leapview:leapview /var/lib/leapview /app",
		"USER leapview:leapview",
		`VOLUME ["/var/lib/leapview"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing distroless runtime compatibility fragment %q", required)
		}
	}
	if !strings.Contains(coverage, "      - "+runtimeImage) {
		t.Fatal("security coverage does not bind the exact distroless runtime image")
	}
	if strings.Contains(dockerfile, "FROM debian:bookworm-slim") || strings.Contains(dockerfile, "apt-get install") {
		t.Fatal("production runtime still carries the vulnerable package-manager image path")
	}
	rootAt := strings.Index(dockerfile, "USER root")
	runtimeAt := strings.Index(dockerfile, "USER leapview:leapview")
	identityAt := strings.Index(dockerfile, "leapview:x:999:999")
	ownershipAt := strings.Index(dockerfile, "chown -R leapview:leapview /var/lib/leapview /app")
	if rootAt < 0 || identityAt < rootAt || ownershipAt < identityAt || runtimeAt < ownershipAt {
		t.Fatal("runtime identity and persistent paths must be prepared as root before switching permanently to leapview")
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
		t.Fatal("derived qualification image must inherit the release image's frozen sources without overriding them")
	}
}
