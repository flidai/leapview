package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
	"github.com/flidai/leapview/internal/release/artifactadmission"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const releaseWorkflow = "flidai/leapview/.github/workflows/release.yml"
const artifactsWorkflow = "flidai/leapview/.github/workflows/artifacts.yml"

var runNumberPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// These checks constrain the producer invocation. Trust during transport comes
// from the authenticated GitHub run and artifact, never from these env values
// or the receipt's result flags supplied by an importing operator.
func validateBundleOptions(opts admissionOptions, env []string) error {
	if opts.admissionBundlePath == "" {
		if opts.releaseID != "" || opts.releaseVersion != "" {
			return errors.New("release identity requires --admission-bundle")
		}
		return nil
	}
	if opts.mode != "live" {
		return errors.New("canonical admission bundles require live verification")
	}
	if opts.platform == "" || opts.releaseID == "" || opts.releaseVersion == "" {
		return errors.New("canonical admission bundle requires platform, release ID and version")
	}
	if opts.OCIRepository != "ghcr.io/flidai/leapview" {
		return errors.New("canonical admission bundle requires the LeapView application image")
	}
	value := func(key string) string { v, _ := envValue(env, key); return v }
	event := "push"
	switch opts.expectedWorkflow {
	case artifactsWorkflow:
	case releaseWorkflow:
		event = "workflow_dispatch"
	default:
		return errors.New("canonical admission bundle workflow is unsupported")
	}
	if value("GITHUB_REPOSITORY") != repositoryIdentity || value("GITHUB_EVENT_NAME") != event || value("GITHUB_REF") != "refs/heads/main" || value("GITHUB_SHA") != opts.sourceRevision || value("GITHUB_WORKFLOW_REF") != opts.expectedWorkflow+"@refs/heads/main" || !runNumberPattern.MatchString(value("GITHUB_RUN_ID")) || !runNumberPattern.MatchString(value("GITHUB_RUN_ATTEMPT")) {
		return errors.New("canonical admission bundle requires an exact authorized GitHub main producer run")
	}
	if _, err := os.Lstat(opts.admissionBundlePath); !os.IsNotExist(err) {
		return errors.New("canonical admission bundle output must not already exist")
	}
	// A summary/report inside the bundle would mutate a file after its binding
	// was hashed. Keep every independently written output outside the directory.
	bundle, err := filepath.Abs(opts.admissionBundlePath)
	if err != nil {
		return err
	}
	for _, other := range []string{opts.outputPath, opts.vulnerabilityReportPath, value("GITHUB_OUTPUT"), opts.policyPath} {
		if other == "" {
			continue
		}
		absolute, err := filepath.Abs(other)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(bundle, absolute)
		if err != nil {
			return err
		}
		if relative == "." || (!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && relative != "..") {
			return errors.New("canonical admission bundle must be separate from other inputs and outputs")
		}
	}
	return nil
}

type bundleImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Labels map[string]string `json:"Labels"`
	} `json:"config"`
}

func verifyBundleImage(opts admissionOptions, imageConfig, sbom []byte) error {
	if len(imageConfig) == 0 || len(imageConfig) > maxVulnerabilityJSONBytes || len(sbom) == 0 || len(sbom) > maxVulnerabilityJSONBytes {
		return errors.New("image configuration or SPDX evidence exceeds its bound")
	}
	var config bundleImageConfig
	if err := json.Unmarshal(imageConfig, &config); err != nil {
		return errors.New("image configuration is not valid JSON")
	}
	if config.Architecture == "" {
		var platforms map[string]json.RawMessage
		if json.Unmarshal(imageConfig, &platforms) != nil || len(platforms[opts.platform]) == 0 || json.Unmarshal(platforms[opts.platform], &config) != nil {
			return errors.New("exact platform image configuration is missing")
		}
	}
	labels := config.Config.Labels
	releaseLabel := "false"
	if opts.expectedWorkflow == releaseWorkflow {
		releaseLabel = "true"
	}
	if config.OS+"/"+config.Architecture != opts.platform || labels["org.opencontainers.image.revision"] != opts.sourceRevision || labels["org.opencontainers.image.version"] != opts.releaseVersion || labels["org.opencontainers.image.source"] != "https://github.com/"+repositoryIdentity || labels["dev.leapview.build.dirty"] != "false" || labels["dev.leapview.build.release"] != releaseLabel {
		return errors.New("exact image platform or release labels do not match the requested identity")
	}
	// Buildx returns an SPDX document per image platform. Do not accept an SPDX
	// marker recursively found in another architecture's document.
	var documents map[string]json.RawMessage
	if json.Unmarshal(sbom, &documents) != nil {
		return errors.New("SPDX evidence is not valid JSON")
	}
	selected := sbom
	if _, ok := documents["SPDX"]; !ok {
		selected = documents[opts.platform]
	}
	var platformSBOM struct {
		SPDX struct {
			Version string `json:"spdxVersion"`
			ID      string `json:"SPDXID"`
		} `json:"SPDX"`
	}
	if json.Unmarshal(selected, &platformSBOM) != nil || platformSBOM.SPDX.Version != "SPDX-2.3" || platformSBOM.SPDX.ID != "SPDXRef-DOCUMENT" {
		return errors.New("exact platform Buildx SPDX 2.3 document is missing")
	}
	return nil
}

type admissionBinding struct {
	SchemaVersion    int               `json:"schemaVersion"`
	Repository       string            `json:"repository"`
	Workflow         string            `json:"workflow"`
	SourceRevision   string            `json:"sourceRevision"`
	ProducerRevision string            `json:"producerRevision,omitempty"`
	Image            string            `json:"image"`
	Platform         string            `json:"platform"`
	AdmissionDigest  string            `json:"admissionDigest"`
	RunID            string            `json:"runId"`
	RunAttempt       string            `json:"runAttempt"`
	Event            string            `json:"event"`
	Ref              string            `json:"ref"`
	Files            map[string]string `json:"files"`
}

func verifyBundleScan(opts admissionOptions, trivy, sbom []byte) error {
	var scan struct {
		ArtifactName string `json:"ArtifactName"`
		ArtifactType string `json:"ArtifactType"`
		Metadata     struct {
			ImageConfig json.RawMessage `json:"ImageConfig"`
		} `json:"Metadata"`
	}
	if json.Unmarshal(trivy, &scan) != nil || scan.ArtifactName != opts.image || scan.ArtifactType != "container_image" {
		return errors.New("vulnerability scan is not bound to the exact container image")
	}
	if err := verifyBundleImage(opts, scan.Metadata.ImageConfig, sbom); err != nil {
		return fmt.Errorf("vulnerability scan image identity: %w", err)
	}
	return nil
}

func evidenceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeAdmissionBundle(opts admissionOptions, env []string, policy, attestation, sbom, imageConfig, trivy []byte, report vulnerabilityReport) error {
	admission := artifactadmission.Admission{
		Version:            artifactadmission.AdmissionVersion,
		Release:            compatibility.ReleaseIdentity{ReleaseID: opts.releaseID, Version: opts.releaseVersion, SourceRevision: opts.sourceRevision, Image: opts.image, Distribution: "distroless", Platform: opts.platform},
		ArchitectureMarker: transitionpreflight.ArchitecturePostgreSQL,
		Repository:         opts.OCIRepository, OCIDigest: strings.TrimPrefix(opts.image, opts.OCIRepository+"@"), Decision: artifactadmission.DecisionAdmitted,
		Provenance:     artifactadmission.ProvenanceResult{Reference: evidenceDigest(attestation), Repository: repositoryIdentity, Workflow: opts.expectedWorkflow, SourceRevision: opts.sourceRevision},
		SBOM:           artifactadmission.SBOMResult{Reference: evidenceDigest(sbom), PredicateType: artifactadmission.SBOMPredicateSPDX, Producer: artifactadmission.SBOMProducerBuildx},
		SecurityPolicy: artifactadmission.SecurityPolicyResult{Version: artifactadmission.SecurityPolicyVersion, Reference: evidenceDigest(policy), Scanner: artifactadmission.SecurityScannerTrivy},
		AdmittedAt:     time.Now().UTC(),
	}
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
	files := map[string][]byte{"admission.json": canonical, "admission.digest": []byte(digest + "\n"), "verified-attestation.json": attestation, "sbom.json": sbom, "image-config.json": imageConfig, "container-vulnerability-policy.json": policy, "vulnerability-report.json": reportBytes, "trivy-report.json": trivy}
	value := func(key string) string { v, _ := envValue(env, key); return v }
	binding := admissionBinding{SchemaVersion: 1, Repository: repositoryIdentity, Workflow: opts.expectedWorkflow, SourceRevision: opts.sourceRevision, Image: opts.image, Platform: opts.platform, AdmissionDigest: digest, RunID: value("GITHUB_RUN_ID"), RunAttempt: value("GITHUB_RUN_ATTEMPT"), Event: value("GITHUB_EVENT_NAME"), Ref: value("GITHUB_REF"), Files: make(map[string]string)}
	for name, data := range files {
		if len(data) == 0 || len(data) > maxVulnerabilityJSONBytes {
			return fmt.Errorf("admission evidence %s is empty or exceeds its bound", name)
		}
		binding.Files[name] = evidenceDigest(data)
	}
	bindingBytes, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	files["binding.json"] = bindingBytes
	parent := filepath.Dir(opts.admissionBundlePath)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".oci-admission-bundle-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(temporary, name), data, 0600); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(opts.admissionBundlePath); !os.IsNotExist(err) {
		return errors.New("canonical admission bundle output appeared during verification")
	}
	if err := os.Rename(temporary, opts.admissionBundlePath); err != nil {
		return err
	}
	return nil
}
