package docs

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

type publicReleaseManifest struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Version       string                  `json:"version"`
	Tag           string                  `json:"tag"`
	Revision      string                  `json:"revision"`
	Image         string                  `json:"image"`
	ReleaseURL    string                  `json:"releaseUrl"`
	Artifacts     []publicReleaseArtifact `json:"artifacts"`
}

type publicReleaseArtifact struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	ArchiveURL   string `json:"archiveUrl"`
	ChecksumURL  string `json:"checksumUrl"`
}

type releaseTransitionTemplate struct {
	PredecessorRelease string `json:"predecessorRelease"`
}

func TestInstallationGuideMatchesCurrentPublicRelease(t *testing.T) {
	manifestContents, err := Files.ReadFile("public-release.json")
	if err != nil {
		t.Fatalf("read public release manifest: %v", err)
	}
	var manifest publicReleaseManifest
	if err := json.Unmarshal(manifestContents, &manifest); err != nil {
		t.Fatalf("decode public release manifest: %v", err)
	}
	guideContents, err := Files.ReadFile("articles/start/installation.md")
	if err != nil {
		t.Fatalf("read installation guide: %v", err)
	}
	guide := string(guideContents)

	versionContents, err := os.ReadFile("../VERSION")
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(versionContents))
	templateContents, err := os.ReadFile("../internal/platform/compatibility/release-transition-template.json")
	if err != nil {
		t.Fatalf("read release transition template: %v", err)
	}
	var template releaseTransitionTemplate
	if err := json.Unmarshal(templateContents, &template); err != nil {
		t.Fatalf("decode release transition template: %v", err)
	}
	if manifest.SchemaVersion != 1 {
		t.Errorf("public release schemaVersion = %d, want 1", manifest.SchemaVersion)
	}
	if template.PredecessorRelease != manifest.Tag {
		t.Errorf("reviewed predecessor = %q, public release tag = %q", template.PredecessorRelease, manifest.Tag)
	}
	if !semver.IsValid("v"+version) || semver.Compare("v"+version, manifest.Tag) <= 0 {
		t.Errorf("candidate VERSION %q must be newer than public predecessor %q", version, manifest.Version)
	}
	if manifest.Tag != "v"+manifest.Version {
		t.Errorf("public release tag = %q, want v%s", manifest.Tag, manifest.Version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(manifest.Revision) {
		t.Errorf("public release revision is not a full Git commit: %q", manifest.Revision)
	}
	if !regexp.MustCompile(`^ghcr\.io/flidai/leapview@sha256:[0-9a-f]{64}$`).MatchString(manifest.Image) {
		t.Errorf("public release image is not in the organization namespace: %q", manifest.Image)
	}
	wantReleaseURL := "https://github.com/flidai/leapview/releases/tag/" + manifest.Tag
	if manifest.ReleaseURL != wantReleaseURL {
		t.Errorf("public release URL = %q, want %q", manifest.ReleaseURL, wantReleaseURL)
	}

	if len(manifest.Artifacts) != 4 {
		t.Fatalf("public release has %d platform artifacts, want 4", len(manifest.Artifacts))
	}
	platforms := make(map[string]struct{}, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		platform := artifact.OS + "/" + artifact.Architecture
		if _, duplicate := platforms[platform]; duplicate {
			t.Errorf("duplicate public release platform %q", platform)
		}
		platforms[platform] = struct{}{}
		archive := fmt.Sprintf("leapview-compose-%s-%s-%s.tar.gz", manifest.Tag, artifact.OS, artifact.Architecture)
		wantArchiveURL := "https://github.com/flidai/leapview/releases/download/" + manifest.Tag + "/" + archive
		if artifact.ArchiveURL != wantArchiveURL {
			t.Errorf("%s archive URL = %q, want %q", platform, artifact.ArchiveURL, wantArchiveURL)
		}
		if artifact.ChecksumURL != wantArchiveURL+".sha256" {
			t.Errorf("%s checksum URL = %q, want %q", platform, artifact.ChecksumURL, wantArchiveURL+".sha256")
		}
		for _, required := range []string{artifact.ArchiveURL, artifact.ChecksumURL} {
			if !strings.Contains(guide, required) {
				t.Errorf("installation guide does not link directly to %s", path.Base(required))
			}
		}
	}
	for _, required := range []string{
		manifest.Version,
		manifest.Tag,
		manifest.Revision,
		manifest.Image,
		manifest.ReleaseURL,
	} {
		if !strings.Contains(guide, required) {
			t.Errorf("installation guide does not contain public release value %q", required)
		}
	}
	for _, owner := range []string{"flidai", "yacobolo"} {
		if strings.Contains(guide, "ghcr.io/"+owner+"/leapview:latest") {
			t.Errorf("installation guide must not send evaluators to a mutable %s image", owner)
		}
	}
}
