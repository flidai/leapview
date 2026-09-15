package architecture

import (
	"os"
	"path/filepath"
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
		"FROM gcr.io/distroless/cc-debian12:debug-nonroot@sha256:",
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
