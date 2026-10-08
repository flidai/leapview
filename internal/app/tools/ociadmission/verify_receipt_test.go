package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func producedReceiptFixture(t *testing.T) ([]string, string) {
	t.Helper()
	args, env, bundle := bundleFixture(t)
	var output bytes.Buffer
	if err := runAdmission(args, env, &output, &output); err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(filepath.Join(bundle, "admission.digest"))
	if err != nil {
		t.Fatal(err)
	}
	return []string{"--bundle", bundle, "--image", testImage, "--platform", "linux/arm64", "--source-revision", testRevision,
		"--expected-workflow", testWorkflow, "--admission-digest", strings.TrimSpace(string(digest))}, bundle
}

func TestVerifyProducedReceiptRoundTrip(t *testing.T) {
	args, _ := producedReceiptFixture(t)
	var output bytes.Buffer
	if err := runVerifyReceipt(args, &output, &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != args[len(args)-1] {
		t.Fatalf("digest output = %q", output.String())
	}
}

func TestVerifyReceiptRejectsSubstitutedCanonicalEvidence(t *testing.T) {
	for _, name := range []string{"admission.json", "verified-attestation.json", "sbom.json", "container-vulnerability-policy.json", "image-config.json", "trivy-report.json", "symlink"} {
		t.Run(name, func(t *testing.T) {
			args, bundle := producedReceiptFixture(t)
			if name == "symlink" {
				original := filepath.Join(bundle, "admission.json")
				outside := filepath.Join(t.TempDir(), "other.json")
				if err := os.Rename(original, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, original); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(bundle, name), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := runVerifyReceipt(args, &output, &output); err == nil {
				t.Fatal("accepted substituted receipt evidence")
			}
		})
	}
}

func TestVerifyReceiptRequiresExactSelectedIdentity(t *testing.T) {
	for flag, value := range map[string]string{
		"--image":           "ghcr.io/flidai/leapview@sha256:" + strings.Repeat("b", 64),
		"--source-revision": strings.Repeat("f", 40), "--platform": "linux/amd64",
		"--expected-workflow": releaseWorkflow, "--admission-digest": "sha256:" + strings.Repeat("c", 64),
	} {
		t.Run(flag, func(t *testing.T) {
			args, _ := producedReceiptFixture(t)
			args = append(args, flag, value)
			var output bytes.Buffer
			if err := runVerifyReceipt(args, &output, &output); err == nil {
				t.Fatal("accepted wrong expected identity")
			}
		})
	}
}
