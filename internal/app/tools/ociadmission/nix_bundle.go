package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const maxNixEvidenceBytes = 128 * 1024 * 1024

func writeNixBundle(opts nixAdmissionOptions, env []string, fresh string, verification []byte, verified nixVerifiedEvidence, verifierRevision string, policy, scan []byte, report vulnerabilityReport, stdout io.Writer) error {
	if report.Outcome != outcomePassed || report.UnresolvedCount != 0 || report.Image != verified.Image || report.Platform != verified.Platform || report.ExpectedSourceRevision != verified.SourceRevision {
		return errors.New("Nix admission requires its completed exact live OCI scan")
	}
	files, err := nixEvidenceFiles(fresh, verified.FileHashes)
	if err != nil {
		return err
	}
	spdx := files["evidence/original-sbom.spdx.json"]
	if len(spdx) == 0 || len(files["evidence/runtime-security-policy.json"]) == 0 || len(files["evidence/security-exceptions.yaml"]) == 0 || len(files["evidence/security-coverage.yaml"]) == 0 {
		return errors.New("Nix admission is missing exact SPDX or current policy evidence")
	}
	policyBinding := map[string]any{"version": artifactadmission.NixSecurityPolicyVersion, "containerPolicy": json.RawMessage(policy),
		"runtimePolicyDigest": verified.FileHashes["runtime-security-policy.json"], "exceptionsDigest": verified.FileHashes["security-exceptions.yaml"],
		"coverageDigest": verified.FileHashes["security-coverage.yaml"], "verifierRevision": verifierRevision}
	policyEnvelope, err := json.Marshal(policyBinding)
	if err != nil {
		return err
	}
	workflow, marker := "flidai/leapview/.github/workflows/nix-site-candidate.yml", "public-site/v1"
	profile := &artifactadmission.NixEvidence{Version: artifactadmission.NixProfileVersion, Kind: verified.Kind, ArchiveSHA256: verified.ArchiveSHA256,
		CandidateDigest: verified.CandidateDigest, RegistryBindingDigest: verified.RegistryBindingDigest, SignedEvidenceBindingDigest: verified.SignedEvidenceBindingDigest,
		QualifiedEvidenceBindingDigest: verified.QualifiedEvidenceBindingDigest, RuntimeEvidenceDigest: evidenceDigest(verified.RuntimeEvidence),
		GoEvidenceDigest: evidenceDigest(verified.GoEvidence), OCIEvidenceDigest: evidenceDigest(scan), SignerRevision: verified.SignerRevision, VerifierRevision: verifierRevision}
	if verified.Kind == "application-image" {
		workflow, marker = "flidai/leapview/.github/workflows/nix-candidate.yml", transitionpreflight.ArchitecturePostgreSQL
		var native struct {
			VerifiedDigest string `json:"verifiedDigest"`
		}
		if json.Unmarshal(verified.NativeEvidence, &native) != nil || !digestPattern.MatchString(native.VerifiedDigest) {
			return errors.New("application Nix admission requires complete native verifier evidence")
		}
		profile.NativeEvidenceDigest = native.VerifiedDigest
	}
	admission := artifactadmission.Admission{Version: artifactadmission.AdmissionVersion,
		Release:            compatibility.ReleaseIdentity{ReleaseID: opts.releaseID, Version: verified.Version, SourceRevision: verified.SourceRevision, Image: verified.Image, Distribution: "nix", Platform: verified.Platform},
		ArchitectureMarker: marker, Repository: strings.Split(verified.Image, "@")[0], OCIDigest: strings.Split(verified.Image, "@")[1], Decision: artifactadmission.DecisionAdmitted,
		Provenance:     artifactadmission.ProvenanceResult{Reference: evidenceDigest(verification), Repository: repositoryIdentity, Workflow: workflow, SourceRevision: verified.SourceRevision},
		SBOM:           artifactadmission.SBOMResult{Reference: evidenceDigest(spdx), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerSyft},
		SecurityPolicy: artifactadmission.SecurityPolicyResult{Version: artifactadmission.NixSecurityPolicyVersion, Reference: evidenceDigest(policyEnvelope), Scanner: artifactadmission.NixSecurityScanner},
		NixEvidence:    profile, AdmittedAt: time.Now().UTC()}
	canonical, err := admission.CanonicalJSON()
	if err != nil {
		return err
	}
	digest, err := admission.Digest()
	if err != nil {
		return err
	}
	reportBytes, err := json.Marshal(report)
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"admission.json": canonical, "admission.digest": []byte(digest + "\n"), "verified-attestation.json": verification,
		"sbom.json": spdx, "container-vulnerability-policy.json": policyEnvelope, "vulnerability-report.json": reportBytes, "trivy-report.json": scan} {
		files[name] = data
	}
	value := func(key string) string { v, _ := envValue(env, key); return v }
	binding := admissionBinding{SchemaVersion: 1, Repository: repositoryIdentity, Workflow: artifactadmission.NixAdmissionWorkflow,
		SourceRevision: verified.SourceRevision, ProducerRevision: verifierRevision, Image: verified.Image, Platform: verified.Platform, AdmissionDigest: digest,
		RunID: value("GITHUB_RUN_ID"), RunAttempt: value("GITHUB_RUN_ATTEMPT"), Event: value("GITHUB_EVENT_NAME"), Ref: value("GITHUB_REF")}
	if err := persistNixBundle(opts.output, files, binding); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}

// Require the entire closed evidence inventory, not merely a summary receipt.
func nixEvidenceFiles(root string, expected map[string]string) (map[string][]byte, error) {
	if len(expected) > 4088 {
		return nil, errors.New("Nix raw evidence inventory exceeds transport bound")
	}
	files := make(map[string][]byte)
	total := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "verification.json" || relative == "oci-vulnerability.json" {
			return nil
		}
		if _, ok := expected[relative]; !ok {
			return errors.New("Nix evidence contains an unbound report")
		}
		data, err := readBundleFile(root, relative, maxNixEvidenceBytes)
		if err != nil || evidenceDigest(data) != expected[relative] {
			return errors.New("Nix evidence report differs from verified inventory")
		}
		total += len(data)
		if total > 512*1024*1024 {
			return errors.New("Nix raw evidence bytes exceed transport bound")
		}
		files["evidence/"+relative] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) != len(expected) {
		return nil, errors.New("Nix evidence report inventory is incomplete")
	}
	return files, nil
}

func persistNixBundle(directory string, files map[string][]byte, binding admissionBinding) error {
	binding.Files = make(map[string]string, len(files))
	total := 0
	for name, data := range files {
		if len(data) == 0 || len(data) > maxNixEvidenceBytes {
			return fmt.Errorf("Nix evidence %s exceeds bound", name)
		}
		total += len(data)
		if total > 512*1024*1024 {
			return errors.New("Nix bundle bytes exceed transport bound")
		}
		binding.Files[name] = evidenceDigest(data)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	if len(files)+1 > 4096 || total+len(encoded) > 512*1024*1024 {
		return errors.New("Nix bundle inventory exceeds transport bound")
	}
	files["binding.json"] = encoded
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".nix-admission-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for name, data := range files {
		path := filepath.Join(temporary, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		return errors.New("Nix bundle output appeared during verification")
	}
	return os.Rename(temporary, directory)
}
