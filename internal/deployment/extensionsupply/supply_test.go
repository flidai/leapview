package extensionsupply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

func TestSupplyAdmitsVerifiedArtifactAndOfflineLookup(t *testing.T) {
	content := []byte("pinned extension bytes")
	digest := digestFor(content)
	artifact := testArtifact("httpfs", digest, "linux-amd64")
	var fetches int
	supply := newSupply(t, Config{Manifest: Manifest{Version: ManifestVersion, Artifacts: []Artifact{artifact}}, Origins: []Origin{{ID: "vendor", URL: "file:///reviewed/httpfs", Reviewed: true, Fetch: func(context.Context, Artifact) (io.ReadCloser, error) {
		fetches++
		return io.NopCloser(bytes.NewReader(content)), nil
	}}}, VerifySignature: verifyOK})
	admitted, err := supply.AdmitExtension(context.Background(), "httpfs")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(admitted.Path) || admitted.Digest != digest || admitted.Origin != "vendor" {
		t.Fatalf("admitted = %#v", admitted)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want one", fetches)
	}
	// A second supply with no fetch adapter can deterministically use the
	// content-addressed cache while offline.
	config := Config{DuckDBVersion: "v1.4.0", GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable-v1", CacheDir: supply.config.CacheDir, Offline: true, Manifest: Manifest{Version: ManifestVersion, Artifacts: []Artifact{artifact}}, VerifySignature: verifyOK}
	offline, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	offlineAdmitted, err := offline.AdmitExtension(context.Background(), "httpfs")
	if err != nil {
		t.Fatal(err)
	}
	if offlineAdmitted.Path != admitted.Path {
		t.Fatalf("offline path = %q, online path = %q", offlineAdmitted.Path, admitted.Path)
	}
}

func TestSupplyRejectsAdversarialManifestAndBytes(t *testing.T) {
	content := []byte("correct bytes")
	digest := digestFor(content)
	tests := []struct {
		name   string
		mutate func(*Config)
		want   error
	}{
		{name: "missing signature", mutate: func(config *Config) { config.Manifest.Artifacts[0].Signature = "" }, want: extension.ErrExtensionUnsigned},
		{name: "wrong target version", mutate: func(config *Config) { config.Manifest.Artifacts[0].Identity.DuckDBVersion = "v9.9.9" }, want: extension.ErrExtensionUnavailable},
		{name: "wrong target platform", mutate: func(config *Config) { config.Manifest.Artifacts[0].Identity.Platform = "windows-arm64" }, want: extension.ErrInvalidManifest},
		{name: "unapproved origin", mutate: func(config *Config) { config.Origins[0].Reviewed = false }, want: extension.ErrExtensionUnapproved},
		{name: "missing verifier", mutate: func(config *Config) { config.VerifySignature = nil }, want: extension.ErrInvalidManifest},
		{name: "corrupt bytes", mutate: func(config *Config) {
			config.Origins[0].Fetch = func(context.Context, Artifact) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("tampered")), nil
			}
		}, want: extension.ErrExtensionUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Config{DuckDBVersion: "v1.4.0", GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable-v1", CacheDir: filepath.Join(t.TempDir(), "extensions"), Manifest: Manifest{Version: ManifestVersion, Artifacts: []Artifact{testArtifact("httpfs", digest, "linux-amd64")}}, Origins: []Origin{{ID: "vendor", URL: "file:///reviewed/httpfs", Reviewed: true, Fetch: func(context.Context, Artifact) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(content)), nil
			}}}, VerifySignature: verifyOK}
			test.mutate(&config)
			supply, err := New(config)
			if err != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("New() error = %v, want %v", err, test.want)
				}
				return
			}
			_, err = supply.AdmitExtension(context.Background(), "httpfs")
			if !errors.Is(err, test.want) {
				t.Fatalf("AdmitExtension() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSupplyDoesNotLeakConfiguredOriginSecrets(t *testing.T) {
	secret := "access_key=do-not-leak"
	content := []byte("bytes")
	config := Config{DuckDBVersion: "v1.4.0", GOOS: "linux", GOARCH: "amd64", Platform: "linux-amd64", SupportProfile: "stable-v1", CacheDir: filepath.Join(t.TempDir(), "extensions"), Manifest: Manifest{Version: ManifestVersion, Artifacts: []Artifact{testArtifact("httpfs", digestFor(content), "linux-amd64")}}, Origins: []Origin{{ID: "vendor", URL: "https://reviewed.invalid", Reviewed: true, Fetch: func(context.Context, Artifact) (io.ReadCloser, error) { return nil, errors.New(secret) }}}, VerifySignature: verifyOK}
	supply := newSupply(t, config)
	_, err := supply.AdmitExtension(context.Background(), "httpfs")
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "reviewed.invalid") {
		t.Fatalf("error leaked origin details: %v", err)
	}
}

func newSupply(t *testing.T, config Config) *Supply {
	t.Helper()
	if config.DuckDBVersion == "" {
		config.DuckDBVersion = "v1.4.0"
	}
	if config.GOOS == "" {
		config.GOOS = "linux"
	}
	if config.GOARCH == "" {
		config.GOARCH = "amd64"
	}
	if config.Platform == "" {
		config.Platform = "linux-amd64"
	}
	if config.SupportProfile == "" {
		config.SupportProfile = "stable-v1"
	}
	if config.CacheDir == "" {
		config.CacheDir = filepath.Join(t.TempDir(), "extensions")
	}
	supply, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return supply
}

func testArtifact(name, digest, platform string) Artifact {
	return Artifact{Identity: extension.Identity{DuckDBVersion: "v1.4.0", ExtensionVersion: "1.0.0", GOOS: "linux", GOARCH: "amd64", Platform: platform, Name: name, Digest: digest, SupportProfile: "stable-v1"}, Origins: []string{"vendor"}, Provenance: "attest:vendor-1", Signature: "sig:vendor-1"}
}

func digestFor(value []byte) string {
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(value))
}

func verifyOK(context.Context, Artifact, []byte) error { return nil }
