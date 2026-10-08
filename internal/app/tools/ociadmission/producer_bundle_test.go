package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/release/artifactadmission"
)

func bundleFixture(t *testing.T) ([]string, []string, string) {
	t.Helper()
	policy, _ := testPolicy(t)
	dir := liveTools(t)
	config := `{"architecture":"arm64","os":"linux","config":{"Labels":{"org.opencontainers.image.revision":"` + testRevision + `","org.opencontainers.image.version":"1.2.3+main.0123456789ab","org.opencontainers.image.source":"https://github.com/flidai/leapview","dev.leapview.build.dirty":"false","dev.leapview.build.release":"false"}}}`
	script := `#!/bin/sh
set -eu
case "$*" in
 *'.Image'*)
  case "$OCI_TEST_MODE" in
   wrong-platform) printf '%s' '` + strings.ReplaceAll(config, `"arm64"`, `"amd64"`) + `';;
   wrong-label) printf '%s' '` + strings.ReplaceAll(config, testRevision, strings.Repeat("f", 40)) + `';;
   wrong-version) printf '%s' '` + strings.ReplaceAll(config, "1.2.3+main.0123456789ab", "9.9.9") + `';;
   *) printf '%s' '` + config + `';;
  esac;;
 *'imagetools inspect'*)
  [ "$OCI_TEST_MODE" = missing-sbom ] && printf '{}' || printf '{"linux/arm64":{"SPDX":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT"}}}';;
 *) exit 64;;
esac
`
	writeTool(t, filepath.Join(dir, "docker"), script)
	scannerPath := filepath.Join(dir, "trivy")
	scanner, err := os.ReadFile(scannerPath)
	if err != nil {
		t.Fatal(err)
	}
	envelope := `{"ArtifactName":"` + testImage + `","ArtifactType":"container_image","Metadata":{"ImageConfig":` + config + `},"Results"`
	writeTool(t, scannerPath, strings.ReplaceAll(string(scanner), `{"Results"`, envelope))
	bundle := filepath.Join(t.TempDir(), "bundle")
	args := append(liveArgs(policy), "--admission-bundle", bundle, "--release-id", "main-"+testRevision, "--release-version", "1.2.3+main.0123456789ab")
	env := testEnv(map[string]string{"PATH": dir, "GH_TOKEN": "fixture-token", "GITHUB_REPOSITORY": repositoryIdentity, "OCI_TEST_MODE": "valid", "GITHUB_RUN_ID": "12345", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": testRevision, "GITHUB_WORKFLOW_REF": testWorkflow + "@refs/heads/main"})
	return args, env, bundle
}

func TestLiveCanonicalBundleRoundTrip(t *testing.T) {
	args, env, dir := bundleFixture(t)
	var output bytes.Buffer
	if err := runAdmission(args, env, &output, &output); err != nil {
		t.Fatal(err)
	}
	document, err := os.ReadFile(filepath.Join(dir, "admission.json"))
	if err != nil {
		t.Fatal(err)
	}
	admission, err := artifactadmission.ParseCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := admission.Digest()
	if err != nil {
		t.Fatal(err)
	}
	digestBytes, err := os.ReadFile(filepath.Join(dir, "admission.digest"))
	if err != nil || string(digestBytes) != digest+"\n" {
		t.Fatalf("digest=%s err=%v", digestBytes, err)
	}
	if admission.Release.Image != testImage || admission.Release.Platform != "linux/arm64" || admission.Release.Version != "1.2.3+main.0123456789ab" {
		t.Fatalf("release = %#v", admission.Release)
	}
	var binding struct {
		Files           map[string]string `json:"files"`
		AdmissionDigest string            `json:"admissionDigest"`
		RunID           string            `json:"runId"`
	}
	data, err := os.ReadFile(filepath.Join(dir, "binding.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.AdmissionDigest != digest || binding.RunID != "12345" || len(binding.Files) != 8 {
		t.Fatalf("binding=%s", data)
	}
	for filename, expected := range binding.Files {
		data, err := os.ReadFile(filepath.Join(dir, filename))
		if err != nil || evidenceDigest(data) != expected {
			t.Fatalf("evidence %s hash mismatch: %v", filename, err)
		}
	}
	for filename, expected := range map[string]string{"verified-attestation.json": admission.Provenance.Reference, "sbom.json": admission.SBOM.Reference, "container-vulnerability-policy.json": admission.SecurityPolicy.Reference} {
		if binding.Files[filename] != expected {
			t.Fatalf("receipt reference %s differs", filename)
		}
	}
}

func TestLiveCanonicalBundleRejectsUnverifiedInputs(t *testing.T) {
	for _, mode := range []string{"wrong-repository", "wrong-workflow", "wrong-revision", "missing-sbom", "unavailable", "vulnerable", "wrong-platform", "wrong-label", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			args, env, dir := bundleFixture(t)
			env = setEnv(env, "OCI_TEST_MODE", mode)
			var output bytes.Buffer
			if err := runAdmission(args, env, &output, &output); err == nil {
				t.Fatal("accepted invalid evidence")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed verification left bundle: %v", err)
			}
		})
	}
}

func TestCanonicalBundleRejectsHermeticAndUntrustedProducer(t *testing.T) {
	for _, change := range []string{"hermetic", "dispatch", "source", "workflow", "run", "platform", "existing"} {
		t.Run(change, func(t *testing.T) {
			args, env, dir := bundleFixture(t)
			switch change {
			case "hermetic":
				args = append(args, "--mode", "hermetic", "--evidence", "unused")
			case "dispatch":
				env = setEnv(env, "GITHUB_EVENT_NAME", "workflow_dispatch")
			case "source":
				env = setEnv(env, "GITHUB_SHA", strings.Repeat("f", 40))
			case "workflow":
				env = setEnv(env, "GITHUB_WORKFLOW_REF", "other@refs/heads/main")
			case "run":
				env = setEnv(env, "GITHUB_RUN_ID", "")
			case "platform":
				args = append(args, "--platform", "")
			case "existing":
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := runAdmission(args, env, &output, &output); err == nil {
				t.Fatal("accepted untrusted producer")
			}
		})
	}
}

func TestCanonicalBundleReleaseMainDispatch(t *testing.T) {
	args, env, dir := bundleFixture(t)
	args = append(args, "--expected-workflow", releaseWorkflow)
	env = setEnv(env, "GITHUB_EVENT_NAME", "workflow_dispatch")
	env = setEnv(env, "GITHUB_WORKFLOW_REF", releaseWorkflow+"@refs/heads/main")
	bin, _ := envValue(env, "PATH")
	for _, name := range []string{"gh", "docker", "trivy"} {
		path := filepath.Join(bin, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := strings.ReplaceAll(string(data), testWorkflow, releaseWorkflow)
		if name == "docker" || name == "trivy" {
			source = strings.ReplaceAll(source, `"dev.leapview.build.release":"false"`, `"dev.leapview.build.release":"true"`)
		}
		writeTool(t, path, source)
	}
	var output bytes.Buffer
	if err := runAdmission(args, env, &output, &output); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "admission.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := artifactadmission.ParseCanonical(data)
	if err != nil || receipt.Provenance.Workflow != releaseWorkflow {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestCanonicalBundleRequiresDatabaseMetadataAndPinnedScanner(t *testing.T) {
	for _, change := range []string{"database", "version"} {
		t.Run(change, func(t *testing.T) {
			args, env, dir := bundleFixture(t)
			bin, _ := envValue(env, "PATH")
			path := filepath.Join(bin, "trivy")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			if change == "database" {
				source = strings.ReplaceAll(source, "DownloadedAt", "omittedDownloadedAt")
			} else {
				source = strings.ReplaceAll(source, "0.74.0", "0.1.0")
			}
			writeTool(t, path, source)
			var output bytes.Buffer
			if err := runAdmission(args, env, &output, &output); err == nil {
				t.Fatal("accepted missing database metadata or wrong scanner")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed scan left bundle: %v", err)
			}
		})
	}
}

func TestCanonicalBundleRejectsSubstitutedPlatformSBOMAndDirtyImage(t *testing.T) {
	for _, change := range []string{"sbom-platform", "dirty", "release", "source"} {
		t.Run(change, func(t *testing.T) {
			args, env, dir := bundleFixture(t)
			bin, _ := envValue(env, "PATH")
			path := filepath.Join(bin, "docker")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			switch change {
			case "sbom-platform":
				source = strings.ReplaceAll(source, `"linux/arm64":{"SPDX"`, `"linux/amd64":{"SPDX"`)
			case "dirty":
				source = strings.ReplaceAll(source, `"dev.leapview.build.dirty":"false"`, `"dev.leapview.build.dirty":"true"`)
			case "release":
				source = strings.ReplaceAll(source, `"dev.leapview.build.release":"false"`, `"dev.leapview.build.release":"true"`)
			case "source":
				source = strings.ReplaceAll(source, "https://github.com/flidai/leapview", "https://github.com/other/repository")
			}
			writeTool(t, path, source)
			var output bytes.Buffer
			if err := runAdmission(args, env, &output, &output); err == nil {
				t.Fatal("accepted substituted evidence")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed verification left bundle: %v", err)
			}
		})
	}
}

func TestCanonicalBundleRejectsSubstitutedScanIdentity(t *testing.T) {
	for _, change := range []string{"digest", "platform", "revision", "missing"} {
		t.Run(change, func(t *testing.T) {
			args, env, dir := bundleFixture(t)
			bin, _ := envValue(env, "PATH")
			path := filepath.Join(bin, "trivy")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			switch change {
			case "digest":
				source = strings.ReplaceAll(source, testImage, "ghcr.io/flidai/leapview@sha256:"+strings.Repeat("b", 64))
			case "platform":
				source = strings.ReplaceAll(source, `"architecture":"arm64"`, `"architecture":"amd64"`)
			case "revision":
				source = strings.ReplaceAll(source, testRevision, strings.Repeat("f", 40))
			case "missing":
				source = strings.ReplaceAll(source, `"ArtifactName"`, `"MissingArtifactName"`)
			}
			writeTool(t, path, source)
			var output bytes.Buffer
			if err := runAdmission(args, env, &output, &output); err == nil {
				t.Fatal("accepted substituted scan identity")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("failed verification left bundle: %v", err)
			}
		})
	}
}
