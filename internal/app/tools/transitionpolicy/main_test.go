package main

import (
	"encoding/json"
	"errors"
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
	admissionPath := filepath.Join(root, "candidate-admission.json")
	outputPath := filepath.Join(root, "release-transition-policy.json")
	predecessorEvidencePath := filepath.Join(root, "predecessor-verification.json")
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
	writeCandidateAdmission(t, admissionPath, image, strings.Repeat("b", 40))
	base, err := compatibility.EmbeddedPolicy()
	if err != nil {
		t.Fatal(err)
	}
	predecessor, ok := base.ReleaseByID("v0.2.0-rc.1")
	if !ok {
		t.Fatal("embedded predecessor is missing")
	}
	resolved := 0
	if err := bindWithResolver(identityPath, admissionPath, outputPath, predecessorEvidencePath, func(image string) (string, error) {
		resolved++
		return image[strings.LastIndex(image, "@")+1:], nil
	}); err != nil {
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
	predecessor, ok = policy.ReleaseByID("v0.2.0-rc.1")
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
	if resolved != 1 {
		t.Fatalf("unique predecessor resolutions = %d, want 1", resolved)
	}
	var evidence predecessorVerification
	readJSON(t, predecessorEvidencePath, &evidence)
	if len(evidence.Predecessors) != 2 {
		t.Fatalf("predecessor evidence = %#v", evidence)
	}
	for _, item := range evidence.Predecessors {
		if item.Image != predecessor.IdentityForPlatform(item.Platform).Image || item.ManifestSHA256 == "" {
			t.Fatalf("predecessor evidence item = %#v", item)
		}
	}
}

func TestBindRejectsUnadmittedCandidateAndUnresolvedPredecessor(t *testing.T) {
	root := t.TempDir()
	image := "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("c", 64)
	revision := strings.Repeat("b", 40)
	identityPath := filepath.Join(root, "release-identity.json")
	identity, err := json.Marshal(map[string]any{
		"version": "0.3.0", "revision": revision, "buildTime": "2026-08-25T00:00:00Z",
		"dirty": false, "development": false, "image": image,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identityPath, identity, 0o600); err != nil {
		t.Fatal(err)
	}
	admissionPath := filepath.Join(root, "candidate-admission.json")
	outputPath := filepath.Join(root, "release-transition-policy.json")
	evidencePath := filepath.Join(root, "predecessor-verification.json")

	writeCandidateAdmission(t, admissionPath, "ghcr.io/flidai/leapview@sha256:"+strings.Repeat("d", 64), revision)
	err = bindWithResolver(identityPath, admissionPath, outputPath, evidencePath, func(string) (string, error) {
		t.Fatal("predecessor resolution ran for an unadmitted candidate")
		return "", nil
	})
	if err == nil || !strings.Contains(err.Error(), "does not authorize") {
		t.Fatalf("unadmitted candidate error = %v", err)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("unadmitted candidate wrote policy: %v", statErr)
	}

	writeCandidateAdmission(t, admissionPath, image, revision)
	err = bindWithResolver(identityPath, admissionPath, outputPath, evidencePath, func(string) (string, error) {
		return "", errors.New("registry unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("unresolved predecessor error = %v", err)
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("unresolved predecessor wrote policy: %v", statErr)
	}
}

func writeCandidateAdmission(t *testing.T, path, image, revision string) {
	t.Helper()
	digest := image[strings.LastIndex(image, "@")+1:]
	record := map[string]any{
		"schemaVersion": 1, "image": image, "digest": digest, "registryDigest": digest,
		"attestation": map[string]any{
			"verified": true, "repository": "flidai/leapview",
			"workflow": "flidai/leapview/.github/workflows/release.yml", "sourceRevision": revision,
		},
		"sbom":                map[string]any{"discoverable": true},
		"vulnerabilityPolicy": map[string]any{"passed": true},
	}
	contents, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatal(err)
	}
}
