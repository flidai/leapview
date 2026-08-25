package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestBindWritesPolicyForAdmittedCandidate(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64)
	identityPath := filepath.Join(root, "release-identity.json")
	outputPath := filepath.Join(root, "release-transition-policy.json")
	identity, err := json.Marshal(map[string]any{
		"version": "0.2.0", "revision": strings.Repeat("b", 40),
		"buildTime": "2026-08-25T00:00:00Z", "dirty": false,
		"development": false, "image": image,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := bind(identityPath, outputPath); err != nil {
		t.Fatal(err)
	}
	policy, _, err := compatibility.LoadPolicy(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if policy.CandidateRelease != "v0.2.0" {
		t.Fatalf("candidate release = %q", policy.CandidateRelease)
	}
	release, ok := policy.ReleaseByID("v0.2.0")
	if !ok || release.IdentityForPlatform("linux/arm64").Image != image {
		t.Fatalf("candidate identity = %#v, %v", release, ok)
	}
}
