package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const productionRuntimeDigest = "sha256:205572d5e48117e14b44b42627890fa8d3e8e65bb37a80abb3317e5151e7f35b"

func TestProductionRuntimeUsesPinnedShelllessChainguardImage(t *testing.T) {
	root := repoRoot(t)
	dockerfileBytes, err := os.ReadFile(filepath.Join(root, "Dockerfile"))
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	dockerfile := string(dockerfileBytes)
	runtimeBase := "FROM cgr.dev/chainguard/glibc-dynamic@" + productionRuntimeDigest + " AS runtime"
	runtimeAt := strings.Index(dockerfile, runtimeBase)
	if runtimeAt < 0 {
		t.Fatalf("Dockerfile missing reviewed production runtime %q", runtimeBase)
	}
	runtimeStage := dockerfile[runtimeAt:]

	for _, required := range []string{
		"FROM build AS runtime-layout",
		"install -d -m 0700 -o 65532 -g 65532 /runtime-root/var/lib/leapview",
		"COPY --from=go-deps /etc/ssl/certs/ca-certificates.crt /runtime-root/etc/ssl/certs/ca-certificates.crt",
		"COPY --from=go-deps /usr/local/go/lib/time/zoneinfo.zip /runtime-root/usr/local/share/leapview/zoneinfo.zip",
		"COPY --from=runtime-layout /runtime-root/ /",
		"USER 65532:65532",
		"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
		"HOME=/var/lib/leapview/home",
		"ZONEINFO=/usr/local/share/leapview/zoneinfo.zip",
		`HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/leapview", "healthcheck"]`,
		`ENTRYPOINT ["/usr/local/bin/leapview"]`,
		`CMD ["serve", "--production"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile missing shellless runtime contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"\nRUN ",
		"/bin/sh",
		"/bin/bash",
		"busybox",
		"apt-get",
		"apk add",
		"USER root",
	} {
		if strings.Contains(runtimeStage, forbidden) {
			t.Fatalf("production runtime stage contains forbidden utility contract %q", forbidden)
		}
	}
	if strings.Contains(runtimeStage, ":latest") {
		t.Fatal("production runtime must never use a mutable latest tag")
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(productionRuntimeDigest) {
		t.Fatalf("production runtime digest is invalid: %q", productionRuntimeDigest)
	}

	for _, requiredMode := range []string{
		"chmod 0555",
		"chmod 0500",
		"chmod 0400",
		"chmod 0444",
		"find /runtime-root/app -type d -exec chmod 0555",
		"find /runtime-root/app -type f -exec chmod 0444",
	} {
		if !strings.Contains(dockerfile[:runtimeAt], requiredMode) {
			t.Fatalf("runtime layout does not enforce %q", requiredMode)
		}
	}
}

func TestQualificationToolingKeepsFrozenDebianBoundary(t *testing.T) {
	root := repoRoot(t)
	qualificationDockerfileBytes, err := os.ReadFile(filepath.Join(
		root,
		"deploy",
		"compose",
		"qualification",
		"Dockerfile.authoring-client",
	))
	if err != nil {
		t.Fatalf("read authoring qualification Dockerfile: %v", err)
	}
	qualificationDockerfile := string(qualificationDockerfileBytes)
	sourcesPath := filepath.Join(
		"deploy",
		"compose",
		"qualification",
		"debian-bookworm.sources",
	)
	sourcesBytes, err := os.ReadFile(filepath.Join(root, sourcesPath))
	if err != nil {
		t.Fatalf("read qualification package sources: %v", err)
	}
	sources := string(sourcesBytes)

	for _, required := range []string{
		"FROM ${LEAPVIEW_IMAGE} AS leapview-payload",
		"FROM debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df",
		"COPY debian-bookworm.sources /etc/apt/sources.list.d/debian.sources",
		"apt-get install -y --no-install-recommends dbus-daemon gnome-keyring",
		"COPY --from=leapview-payload /usr/local/bin/leapview /usr/local/bin/leapview",
		"COPY --from=leapview-payload /usr/local/libexec/leapviewctl /usr/local/libexec/leapviewctl",
		"COPY --from=leapview-payload --chown=author:author /app/evaluation /app/evaluation",
	} {
		if !strings.Contains(qualificationDockerfile, required) {
			t.Fatalf("qualification tooling Dockerfile missing %q", required)
		}
	}
	if strings.Contains(qualificationDockerfile, "FROM ${LEAPVIEW_IMAGE}\n\nUSER root") {
		t.Fatal("qualification tooling must not install packages into the production runtime")
	}

	snapshotPattern := regexp.MustCompile(`/([0-9]{8}T[0-9]{6}Z)/`)
	matches := snapshotPattern.FindAllStringSubmatch(sources, -1)
	if len(matches) != 2 || matches[0][1] != matches[1][1] {
		t.Fatalf("qualification sources must pin Debian and Debian security to one timestamp: %q", sources)
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
			t.Fatalf("qualification package sources missing %q", required)
		}
	}
	for _, movingSource := range []string{
		"deb.debian.org",
		"security.debian.org",
		"archive.ubuntu.com",
	} {
		if strings.Contains(sources, movingSource) {
			t.Fatalf("qualification package sources contain moving repository %q", movingSource)
		}
	}

	siteDockerfile, err := os.ReadFile(filepath.Join(root, "Dockerfile.site"))
	if err != nil {
		t.Fatalf("read Dockerfile.site: %v", err)
	}
	if strings.Contains(string(siteDockerfile), "apt-get install") {
		t.Fatal("distroless site runtime unexpectedly installs OS packages")
	}
}
