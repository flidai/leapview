package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestProductionRuntimeOSPackagesUseFrozenSignedSnapshot(t *testing.T) {
	root := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	sourcesPath := filepath.Join("deploy", "container", "debian-bookworm.sources")
	sourcesBytes, err := os.ReadFile(filepath.Join(root, sourcesPath))
	if err != nil {
		t.Fatalf("read runtime package sources: %v", err)
	}
	dockerfile := string(dockerfileBytes)
	sources := string(sourcesBytes)

	copySources := "COPY " + filepath.ToSlash(sourcesPath) + " /etc/apt/sources.list.d/debian.sources"
	for _, required := range []string{
		"FROM debian:bookworm-slim@sha256:",
		"COPY --from=go-deps /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt",
		copySources,
		"apt-get install -y --no-install-recommends ca-certificates libstdc++6 tzdata",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing reproducible runtime package fragment %q", required)
		}
	}
	if copyAt, updateAt := strings.Index(dockerfile, copySources), strings.Index(dockerfile, "RUN apt-get update"); copyAt < 0 || updateAt < 0 || copyAt > updateAt {
		t.Fatal("frozen Debian sources must replace the image defaults before apt-get update")
	}

	snapshotPattern := regexp.MustCompile(`/([0-9]{8}T[0-9]{6}Z)/`)
	matches := snapshotPattern.FindAllStringSubmatch(sources, -1)
	if len(matches) != 2 || matches[0][1] != matches[1][1] {
		t.Fatalf("runtime sources must pin Debian and Debian security to one timestamp: %q", sources)
	}
	for _, required := range []string{
		"URIs: https://snapshot.debian.org/archive/debian/" + matches[0][1] + "/",
		"URIs: https://snapshot.debian.org/archive/debian-security/" + matches[0][1] + "/",
		"Suites: bookworm bookworm-updates",
		"Suites: bookworm-security",
		"Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg",
		"Check-Valid-Until: no",
	} {
		if !strings.Contains(sources, required) {
			t.Fatalf("runtime package sources missing %q", required)
		}
	}
	for _, movingSource := range []string{"deb.debian.org", "security.debian.org", "archive.ubuntu.com"} {
		if strings.Contains(sources, movingSource) {
			t.Fatalf("runtime package sources contain moving repository %q", movingSource)
		}
	}
	if count := strings.Count(sources, "Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg"); count != 2 {
		t.Fatalf("signed repository binding count = %d, want 2", count)
	}
	if count := strings.Count(sources, "Check-Valid-Until: no"); count != 2 {
		t.Fatalf("snapshot validity override count = %d, want 2", count)
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
