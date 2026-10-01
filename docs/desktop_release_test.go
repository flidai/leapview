package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type desktopReleaseManifest struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Status        string                   `json:"status"`
	Product       desktopReleaseProduct    `json:"product"`
	Channel       desktopReleaseChannel    `json:"channel"`
	Support       []desktopReleasePlatform `json:"support"`
	Release       any                      `json:"release"`
}

type desktopReleaseProduct struct {
	Name          string `json:"name"`
	ApplicationID string `json:"applicationId"`
}

type desktopReleaseChannel struct {
	Name         string `json:"name"`
	UpdateOrigin string `json:"updateOrigin"`
	PathVersion  string `json:"pathVersion"`
}

type desktopReleasePlatform struct {
	Platform       string   `json:"platform"`
	Architectures  []string `json:"architectures"`
	MinimumVersion string   `json:"minimumVersion"`
}

func TestDesktopReleaseManifestWithdrawsUnavailablePreviewAndMatchesPolicy(t *testing.T) {
	contents, err := Files.ReadFile("desktop-release.json")
	if err != nil {
		t.Fatalf("read desktop release manifest: %v", err)
	}
	var manifest desktopReleaseManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatalf("decode desktop release manifest: %v", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Status != "withdrawn" || manifest.Release == nil {
		t.Fatalf("desktop release state = schema %d, status %q, release %#v; want withdrawn preview schema 1", manifest.SchemaVersion, manifest.Status, manifest.Release)
	}
	if manifest.Product.Name != "LeapView" || manifest.Product.ApplicationID != "dev.leapview.desktop" {
		t.Errorf("desktop product identity = %#v", manifest.Product)
	}
	if manifest.Channel.Name != "preview" ||
		manifest.Channel.UpdateOrigin != "" ||
		manifest.Channel.PathVersion != "v1" {
		t.Errorf("desktop channel identity = %#v", manifest.Channel)
	}

	policyContents, err := os.ReadFile("../desktop/release-policy.json")
	if err != nil {
		t.Fatalf("read desktop release policy: %v", err)
	}
	var policy struct {
		SupportMatrix []desktopReleasePlatform `json:"supportMatrix"`
	}
	if err := json.Unmarshal(policyContents, &policy); err != nil {
		t.Fatalf("decode desktop release policy: %v", err)
	}
	if len(manifest.Support) != len(policy.SupportMatrix) {
		t.Fatalf("desktop support entries = %d, policy = %d", len(manifest.Support), len(policy.SupportMatrix))
	}
	for index, support := range manifest.Support {
		want := policy.SupportMatrix[index]
		if support.Platform != want.Platform ||
			support.MinimumVersion != want.MinimumVersion ||
			strings.Join(support.Architectures, ",") != strings.Join(want.Architectures, ",") {
			t.Errorf("desktop support[%d] = %#v, want %#v", index, support, want)
		}
	}
}
