package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/release/artifactadmission"
)

// Producer trust is established by authenticated GitHub transport before this
// content verifier runs. This port cannot mint or install an arbitrary receipt.
func verifyNixReceipt(bundle string, opts admissionOptions, admission artifactadmission.Admission, digest, producerRevision string, stdout io.Writer) error {
	actual, err := admission.Digest()
	if err != nil {
		return err
	}
	profile := admission.NixEvidence
	if actual != digest || admission.Release.Image != opts.image || admission.Release.SourceRevision != opts.sourceRevision || admission.Release.Platform != opts.platform || profile.VerifierRevision != producerRevision {
		return errors.New("Nix receipt differs from selected immutable output or authenticated verifier")
	}
	verification, err := readBundleFile(bundle, "verified-attestation.json", maxVulnerabilityJSONBytes)
	if err != nil || evidenceDigest(verification) != admission.Provenance.Reference {
		return errors.New("Nix receipt live verification reference differs")
	}
	verified, err := parseNixVerification(verification, nixAdmissionOptions{kind: profile.Kind, architecture: strings.TrimPrefix(opts.platform, "linux/")})
	if err != nil {
		return err
	}
	if verified.Image != opts.image || verified.SourceRevision != opts.sourceRevision || verified.Version != admission.Release.Version || verified.SignerRevision != profile.SignerRevision ||
		verified.ArchiveSHA256 != profile.ArchiveSHA256 || verified.CandidateDigest != profile.CandidateDigest || verified.RegistryBindingDigest != profile.RegistryBindingDigest ||
		verified.SignedEvidenceBindingDigest != profile.SignedEvidenceBindingDigest || verified.QualifiedEvidenceBindingDigest != profile.QualifiedEvidenceBindingDigest ||
		evidenceDigest(verified.RuntimeEvidence) != profile.RuntimeEvidenceDigest || evidenceDigest(verified.GoEvidence) != profile.GoEvidenceDigest {
		return errors.New("Nix receipt differs from original qualified output or fresh coverage")
	}
	for name, expected := range verified.FileHashes {
		data, err := readBundleFile(bundle, "evidence/"+name, maxNixEvidenceBytes)
		if err != nil || evidenceDigest(data) != expected {
			return fmt.Errorf("Nix raw evidence %s differs from closed inventory", name)
		}
	}
	if profile.Kind == "application-image" {
		var native struct {
			VerifiedDigest string `json:"verifiedDigest"`
		}
		if json.Unmarshal(verified.NativeEvidence, &native) != nil || native.VerifiedDigest != profile.NativeEvidenceDigest {
			return errors.New("Nix embedded native coverage differs")
		}
	}
	for name, expected := range map[string]string{"sbom.json": admission.SBOM.Reference, "container-vulnerability-policy.json": admission.SecurityPolicy.Reference, "trivy-report.json": profile.OCIEvidenceDigest} {
		data, err := readBundleFile(bundle, name, maxVulnerabilityJSONBytes)
		if err != nil || evidenceDigest(data) != expected {
			return fmt.Errorf("Nix receipt %s reference differs", name)
		}
	}
	sbom, err := readBundleFile(bundle, "sbom.json", maxVulnerabilityJSONBytes)
	if err != nil || evidenceDigest(sbom) != verified.FileHashes["original-sbom.spdx.json"] {
		return errors.New("Nix original signed SPDX differs from evidence inventory")
	}
	var document struct {
		SPDXVersion string            `json:"spdxVersion"`
		SPDXID      string            `json:"SPDXID"`
		Packages    []json.RawMessage `json:"packages"`
	}
	if json.Unmarshal(sbom, &document) != nil || document.SPDXVersion != "SPDX-2.3" || document.SPDXID != "SPDXRef-DOCUMENT" || len(document.Packages) == 0 {
		return errors.New("Nix exact original SPDX inventory is missing")
	}
	scan, err := readBundleFile(bundle, "trivy-report.json", maxVulnerabilityJSONBytes)
	if err != nil {
		return err
	}
	if err := verifyNixScanIdentity(scan, verified); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, digest)
	return err
}
