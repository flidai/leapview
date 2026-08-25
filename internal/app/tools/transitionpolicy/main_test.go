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
		"version": "0.3.0", "revision": strings.Repeat("b", 40),
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
	if policy.CandidateRelease != "v0.3.0" {
		t.Fatalf("candidate release = %q", policy.CandidateRelease)
	}
	release, ok := policy.ReleaseByID("v0.3.0")
	if !ok || release.IdentityForPlatform("linux/arm64").Image != image {
		t.Fatalf("candidate identity = %#v, %v", release, ok)
	}
	predecessor, ok := policy.ReleaseByID("v0.2.0-rc.1")
	if !ok {
		t.Fatal("bound policy is missing reviewed predecessor")
	}
	for _, test := range []struct {
		operation compatibility.Operation
		current   compatibility.ReleaseIdentity
		next      compatibility.ReleaseIdentity
	}{
		{operation: compatibility.OperationUpgrade, current: predecessor.IdentityForPlatform("linux/amd64"), next: release.IdentityForPlatform("linux/amd64")},
		{operation: compatibility.OperationRollback, current: release.IdentityForPlatform("linux/amd64"), next: predecessor.IdentityForPlatform("linux/amd64")},
	} {
		decision := policy.Evaluate(compatibility.Request{Operation: test.operation, Current: test.current, Next: test.next})
		if err := decision.Err(); err != nil {
			t.Fatalf("%s decision = %#v: %v", test.operation, decision, err)
		}
	}
}
