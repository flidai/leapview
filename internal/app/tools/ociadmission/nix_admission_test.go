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

func nixProducerFixture(t *testing.T) (nixAdmissionOptions, []string) {
	t.Helper()
	return nixAdmissionOptions{kind: "site-image", directory: t.TempDir(), sourceRoot: t.TempDir(), architecture: "amd64",
			binaryVerifier: "/trusted/verifier", output: filepath.Join(t.TempDir(), "bundle"), releaseID: "qualified-site", runID: 123, attempt: 2},
		testEnv(map[string]string{"GITHUB_REPOSITORY": repositoryIdentity, "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_REF": "refs/heads/main",
			"GITHUB_WORKFLOW_REF": artifactadmission.NixAdmissionWorkflow + "@refs/heads/main", "GITHUB_SHA": strings.Repeat("b", 40), "GITHUB_RUN_ID": "456", "GITHUB_RUN_ATTEMPT": "1"})
}

func TestNixIssuerRequiresExactProtectedProducer(t *testing.T) {
	opts, env := nixProducerFixture(t)
	if revision, err := validateNixProducer(opts, env); err != nil || revision != strings.Repeat("b", 40) {
		t.Fatalf("revision=%s err=%v", revision, err)
	}
	for key, value := range map[string]string{"GITHUB_REPOSITORY": "foreign/leapview", "GITHUB_EVENT_NAME": "pull_request", "GITHUB_REF": "refs/heads/feature", "GITHUB_WORKFLOW_REF": artifactsWorkflow + "@refs/heads/main", "GITHUB_SHA": "bad", "GITHUB_RUN_ATTEMPT": "0"} {
		t.Run(key, func(t *testing.T) {
			if _, err := validateNixProducer(opts, setEnv(env, key, value)); err == nil {
				t.Fatal("unprotected producer accepted")
			}
		})
	}
	for _, kind := range []string{"application-image", "application-archive", "site-image"} {
		changed := opts
		changed.kind = kind
		if kind == "site-image" {
			changed.nativeDirectory = "/borrowed/native"
		}
		if _, err := validateNixProducer(changed, env); err == nil {
			t.Fatal("missing or foreign coverage accepted")
		}
	}
}

func TestNixCanonicalMatchesPythonIdentity(t *testing.T) {
	got, err := nixCanonicalJSON(map[string]any{"z": "<é😀>", "a": "\\\n\u2028"})
	if err != nil || string(got) != `{"a":"\\\n\u2028","z":"<\u00e9\ud83d\ude00>"}` {
		t.Fatalf("canonical=%s err=%v", got, err)
	}
}

func nixProducedFixture(t *testing.T, arch string) (nixAdmissionOptions, string, nixVerifiedEvidence) {
	t.Helper()
	opts, env := nixProducerFixture(t)
	opts.architecture = arch
	fresh := t.TempDir()
	files := map[string][]byte{"original-sbom.spdx.json": []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[{"name":"site"}]}`),
		"runtime-security-policy.json": []byte(`{"policy":"runtime"}`), "security-exceptions.yaml": []byte("exceptions: []\n"), "security-coverage.yaml": []byte("version: 1\n"),
		"runtime/summary.json": []byte(`{"coverageQualified":true}`), "go/site/govulncheck.json": []byte(`{"progress":{}}`)}
	verified := nixVerifiedEvidence{SchemaVersion: 1, Kind: opts.kind, SourceRevision: strings.Repeat("a", 40), SignerRevision: strings.Repeat("c", 40),
		Image: "ghcr.io/flidai/leapview-site@sha256:" + strings.Repeat("d", 64), Platform: "linux/" + arch, Version: "0.3.0-alpha.1", FileHashes: map[string]string{},
		ArchiveSHA256: evidenceDigest([]byte("archive")), CandidateDigest: evidenceDigest([]byte("candidate")), RegistryBindingDigest: evidenceDigest([]byte("registry")),
		SignedEvidenceBindingDigest: evidenceDigest([]byte("signed")), QualifiedEvidenceBindingDigest: evidenceDigest([]byte("qualified")),
		RuntimeEvidence: json.RawMessage(`{"scope":"nix-runtime-only"}`), GoEvidence: json.RawMessage(`{"entrypoints":["site"]}`), NativeEvidence: json.RawMessage(`null`)}
	for name, data := range files {
		path := filepath.Join(fresh, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		verified.FileHashes[name] = evidenceDigest(data)
	}
	verified.OriginalProducer = json.RawMessage(`{"schemaVersion":1,"kind":"site-image","runId":123,"attempt":2,"architecture":"` + arch + `","signerRevision":"` + verified.SignerRevision + `"}`)
	encoded, _ := json.Marshal(verified)
	var identity map[string]any
	if err := json.Unmarshal(encoded, &identity); err != nil {
		t.Fatal(err)
	}
	delete(identity, "verificationDigest")
	canonical, _ := nixCanonicalJSON(identity)
	verified.VerificationDigest = evidenceDigest(append([]byte("leapview/nix-release-verification/v1\n"), canonical...))
	identity["verificationDigest"] = verified.VerificationDigest
	verification, _ := nixCanonicalJSON(identity)
	parsed, err := parseNixVerification(verification, opts)
	if err != nil || parsed.SourceRevision != verified.SourceRevision {
		t.Fatalf("parse=%+v err=%v", parsed, err)
	}
	scan := []byte(`{"ArtifactName":"` + verified.Image + `","ArtifactType":"container_image","Metadata":{"ImageConfig":{"architecture":"` + arch + `","os":"linux","config":{"Labels":{"org.opencontainers.image.revision":"` + verified.SourceRevision + `","org.opencontainers.image.version":"` + verified.Version + `","org.opencontainers.image.source":"https://github.com/flidai/leapview","dev.leapview.build.dirty":"false","dev.leapview.build.kind":"site-image"}}}},"Results":[]}`)
	report := vulnerabilityReport{Outcome: outcomePassed, Image: verified.Image, Platform: verified.Platform, ExpectedSourceRevision: verified.SourceRevision}
	var output bytes.Buffer
	if err := writeNixBundle(opts, env, fresh, verification, verified, strings.Repeat("b", 40), []byte(`{"scanner":"trivy"}`), scan, report, &output); err != nil {
		t.Fatal(err)
	}
	return opts, fresh, verified
}

func TestNixBundleRetainsBoundReportsAndSeparatesProducerSource(t *testing.T) {
	opts, fresh, verified := nixProducedFixture(t, "amd64")
	var output bytes.Buffer
	document, err := os.ReadFile(filepath.Join(opts.output, "admission.json"))
	if err != nil {
		t.Fatal(err)
	}
	admission, err := artifactadmission.ParseCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Release.Distribution != "nix" || admission.Release.SourceRevision != verified.SourceRevision || admission.Provenance.Workflow != "flidai/leapview/.github/workflows/nix-site-candidate.yml" || admission.NixEvidence.VerifierRevision != strings.Repeat("b", 40) || admission.NixEvidence.SignerRevision != verified.SignerRevision {
		t.Fatalf("admission=%+v", admission)
	}
	var binding admissionBinding
	data, _ := os.ReadFile(filepath.Join(opts.output, "binding.json"))
	if err := json.Unmarshal(data, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.Workflow != artifactadmission.NixAdmissionWorkflow || binding.SourceRevision != verified.SourceRevision || binding.ProducerRevision != strings.Repeat("b", 40) {
		t.Fatalf("binding=%s", data)
	}
	for name, hash := range binding.Files {
		data, err := os.ReadFile(filepath.Join(opts.output, name))
		if err != nil || evidenceDigest(data) != hash {
			t.Fatalf("unbound file %s", name)
		}
	}
	if _, ok := binding.Files["evidence/go/site/govulncheck.json"]; !ok {
		t.Fatal("raw scanner output missing")
	}
	digest, _ := admission.Digest()
	args := []string{"--bundle", opts.output, "--image", verified.Image, "--source-revision", verified.SourceRevision, "--platform", verified.Platform,
		"--expected-workflow", artifactadmission.NixAdmissionWorkflow, "--producer-revision", strings.Repeat("b", 40), "--admission-digest", digest}
	if err := runVerifyReceipt(args, &output, &output); err != nil {
		t.Fatalf("authenticated receipt roundtrip: %v", err)
	}
	if err := runVerifyReceipt(append(args, "--producer-revision", strings.Repeat("c", 40)), &output, &output); err == nil {
		t.Fatal("original signer borrowed as admission verifier")
	}
	if err := os.WriteFile(filepath.Join(fresh, "go/site/govulncheck.json"), []byte(`{"changed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nixEvidenceFiles(fresh, verified.FileHashes); err == nil {
		t.Fatal("changed report accepted")
	}
}
