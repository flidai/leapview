package artifactadmission

import (
	"errors"
	"fmt"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const (
	NixProfileVersion        = "nix-oci-artifact-evidence/v1"
	NixSecurityPolicyVersion = "nix-oci-security-policy/v1"
	SBOMProducerSyft         = "anchore/syft"
	NixSecurityScanner       = "grype+govulncheck+trivy"
	NixAdmissionWorkflow     = "flidai/leapview/.github/workflows/nix-output-admission.yml"
)

// NixEvidence records the closed evidence profile verified by the protected
// admission producer. Digest-shaped values alone do not authenticate evidence:
// the live verifier and authenticated producer transport establish that boundary.
// SignerRevision names the original candidate signer; VerifierRevision names the
// separately authorized admission implementation. Neither replaces source identity.
type NixEvidence struct {
	Version                        string `json:"version"`
	Kind                           string `json:"kind"`
	ArchiveSHA256                  string `json:"archiveSha256"`
	CandidateDigest                string `json:"candidateDigest"`
	RegistryBindingDigest          string `json:"registryBindingDigest"`
	SignedEvidenceBindingDigest    string `json:"signedEvidenceBindingDigest"`
	QualifiedEvidenceBindingDigest string `json:"qualifiedEvidenceBindingDigest"`
	RuntimeEvidenceDigest          string `json:"runtimeEvidenceDigest"`
	GoEvidenceDigest               string `json:"goEvidenceDigest"`
	NativeEvidenceDigest           string `json:"nativeEvidenceDigest,omitempty"`
	OCIEvidenceDigest              string `json:"ociEvidenceDigest"`
	SignerRevision                 string `json:"signerRevision"`
	VerifierRevision               string `json:"verifierRevision"`
}

func (a Admission) validateNixEvidence() error {
	e := a.NixEvidence
	if e == nil || e.Version != NixProfileVersion || a.Release.Distribution != "nix" ||
		(a.Release.Platform != "linux/amd64" && a.Release.Platform != "linux/arm64") ||
		a.SBOM.PredicateType != SBOMPredicateSPDX || a.SBOM.Producer != SBOMProducerSyft ||
		a.SecurityPolicy.Version != NixSecurityPolicyVersion || a.SecurityPolicy.Scanner != NixSecurityScanner ||
		!sourceRevisionPattern.MatchString(e.SignerRevision) || !sourceRevisionPattern.MatchString(e.VerifierRevision) {
		return errors.New("OCI artifact admission Nix evidence profile is unsupported")
	}
	wantRepository, wantWorkflow, wantMarker := "", "", ""
	switch e.Kind {
	case "application-image":
		wantRepository, wantWorkflow, wantMarker = "ghcr.io/flidai/leapview", "flidai/leapview/.github/workflows/nix-candidate.yml", transitionpreflight.ArchitecturePostgreSQL
		if err := platformdigest.ValidateSHA256Identity(e.NativeEvidenceDigest); err != nil {
			return fmt.Errorf("OCI artifact admission Nix embedded native coverage: %w", err)
		}
	case "site-image":
		wantRepository, wantWorkflow, wantMarker = "ghcr.io/flidai/leapview-site", "flidai/leapview/.github/workflows/nix-site-candidate.yml", "public-site/v1"
		if e.NativeEvidenceDigest != "" {
			return errors.New("OCI artifact admission site cannot borrow embedded native evidence")
		}
	default:
		return errors.New("OCI artifact admission Nix output kind is unsupported")
	}
	if a.Repository != wantRepository || a.Provenance.Workflow != wantWorkflow || a.ArchitectureMarker != wantMarker {
		return errors.New("OCI artifact admission Nix evidence belongs to another output")
	}
	for name, digest := range map[string]string{
		"archive": e.ArchiveSHA256, "candidate": e.CandidateDigest, "registry": e.RegistryBindingDigest,
		"signature": e.SignedEvidenceBindingDigest, "qualification": e.QualifiedEvidenceBindingDigest,
		"runtime": e.RuntimeEvidenceDigest, "Go": e.GoEvidenceDigest, "OCI": e.OCIEvidenceDigest,
	} {
		if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
			return fmt.Errorf("OCI artifact admission Nix %s evidence: %w", name, err)
		}
	}
	return nil
}
