package artifactadmission

import (
	"bytes"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
	"strings"
	"testing"
)

func nixTestAdmission(kind string) Admission {
	a := testAdmission()
	a.Release.Distribution = "nix"
	a.ArchitectureMarker = transitionpreflight.ArchitecturePostgreSQL
	a.SBOM.Producer = SBOMProducerSyft
	a.SecurityPolicy.Version, a.SecurityPolicy.Scanner = NixSecurityPolicyVersion, NixSecurityScanner
	a.Provenance.Workflow = "flidai/leapview/.github/workflows/nix-candidate.yml"
	a.NixEvidence = &NixEvidence{Version: NixProfileVersion, Kind: kind,
		ArchiveSHA256: digest("a"), CandidateDigest: digest("b"), RegistryBindingDigest: digest("c"),
		SignedEvidenceBindingDigest: digest("d"), QualifiedEvidenceBindingDigest: digest("e"),
		RuntimeEvidenceDigest: digest("f"), GoEvidenceDigest: digest("a"), OCIEvidenceDigest: digest("b"),
		SignerRevision: strings.Repeat("a", 40), VerifierRevision: strings.Repeat("b", 40)}
	if kind == "application-image" {
		a.NixEvidence.NativeEvidenceDigest = digest("c")
	} else {
		a.Repository = "ghcr.io/flidai/leapview-site"
		a.Release.Image = a.Repository + "@" + a.OCIDigest
		a.ArchitectureMarker = "public-site/v1"
		a.Provenance.Workflow = "flidai/leapview/.github/workflows/nix-site-candidate.yml"
	}
	return a
}

func TestNixProfileCanonicalRoundTripPreservesDistinctSignerAndSource(t *testing.T) {
	for _, kind := range []string{"application-image", "site-image"} {
		a := nixTestAdmission(kind)
		canonical, err := a.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseCanonical(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.NixEvidence.SignerRevision == parsed.Release.SourceRevision || parsed.NixEvidence.VerifierRevision == parsed.Release.SourceRevision {
			t.Fatal("protected signer/verifier must remain distinct from build source")
		}
		identity, err := parsed.ArtifactIdentity()
		if err != nil || identity.Release.Image != a.Release.Image {
			t.Fatalf("identity: %+v %v", identity, err)
		}
	}
}

func TestNixProfileCannotBorrowLegacyOrAnotherOutputsEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*Admission){
		"missing profile":          func(a *Admission) { a.NixEvidence = nil },
		"missing native":           func(a *Admission) { a.NixEvidence.NativeEvidenceDigest = "" },
		"wrong repository":         func(a *Admission) { a.NixEvidence.Kind = "site-image" },
		"legacy SBOM":              func(a *Admission) { a.SBOM.Producer = SBOMProducerBuildx },
		"legacy scanner":           func(a *Admission) { a.SecurityPolicy.Scanner = SecurityScannerTrivy },
		"foreign signer":           func(a *Admission) { a.Provenance.Workflow = NixAdmissionWorkflow },
		"invalid binding":          func(a *Admission) { a.NixEvidence.GoEvidenceDigest = "clean" },
		"unsupported platform":     func(a *Admission) { a.Release.Platform = "linux/riscv64" },
		"development distribution": func(a *Admission) { a.Release.Distribution = "distroless" },
		"invalid verifier":         func(a *Admission) { a.NixEvidence.VerifierRevision = "main" },
	} {
		t.Run(name, func(t *testing.T) {
			a := nixTestAdmission("application-image")
			mutate(&a)
			if _, err := a.CanonicalJSON(); err == nil {
				t.Fatal("unsupported evidence admitted")
			}
		})
	}
	a := testAdmission()
	a.NixEvidence = nixTestAdmission("application-image").NixEvidence
	if _, err := a.CanonicalJSON(); err == nil {
		t.Fatal("legacy profile accepted Nix declarations")
	}
	a = nixTestAdmission("site-image")
	a.NixEvidence.NativeEvidenceDigest = digest("c")
	if _, err := a.CanonicalJSON(); err == nil {
		t.Fatal("site borrowed application native evidence")
	}
}

func TestLegacyAdmissionCanonicalBytesHaveNoNewNixFields(t *testing.T) {
	a := testAdmission()
	document, err := a.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(document, []byte("nixEvidence")) {
		t.Fatal("legacy canonical identity changed")
	}
}
