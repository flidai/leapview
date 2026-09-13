package artifactadmission

import (
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/compatibility"
)

func TestAdmissionCanonicalIdentityIsDeterministic(t *testing.T) {
	record := testAdmission()
	first, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	second, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical admission changed:\n%s\n%s", first, second)
	}
	firstDigest, err := record.Digest()
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := record.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest != secondDigest || !strings.HasPrefix(firstDigest, "sha256:") {
		t.Fatalf("admission digests = %q/%q", firstDigest, secondDigest)
	}
	identity, err := record.ArtifactIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if identity.ArtifactAdmissionDigest != firstDigest || identity.Release != record.Release {
		t.Fatalf("artifact identity = %#v", identity)
	}
}

func TestAdmissionRejectsUntrustedOrMutableEvidence(t *testing.T) {
	tests := map[string]func(*Admission){
		"unsupported version":         func(a *Admission) { a.Version = "oci-artifact-admission/v2" },
		"mutable image":               func(a *Admission) { a.Release.Image = "ghcr.io/flidai/leapview:latest" },
		"tagged digest":               func(a *Admission) { a.Release.Image = "ghcr.io/flidai/leapview:v1@" + digest("a") },
		"repository mismatch":         func(a *Admission) { a.Repository = "ghcr.io/flidai/other" },
		"digest mismatch":             func(a *Admission) { a.OCIDigest = digest("b") },
		"not admitted":                func(a *Admission) { a.Decision = "rejected" },
		"missing provenance":          func(a *Admission) { a.Provenance.Reference = "" },
		"unverified provenance":       func(a *Admission) { a.Provenance.Verified = false },
		"missing sbom":                func(a *Admission) { a.SBOM.Reference = "" },
		"failed security policy":      func(a *Admission) { a.SecurityPolicy.Passed = false },
		"unsupported security policy": func(a *Admission) { a.SecurityPolicy.Version = "oci-security-policy/v2" },
		"missing admission time":      func(a *Admission) { a.AdmittedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			record := testAdmission()
			mutate(&record)
			if _, err := record.CanonicalJSON(); err == nil {
				t.Fatal("CanonicalJSON error = nil")
			}
		})
	}
}

func testAdmission() Admission {
	return Admission{
		Version: AdmissionVersion,
		Release: compatibility.ReleaseIdentity{
			ReleaseID: "release-1", Version: "v1.2.3", SourceRevision: strings.Repeat("c", 40),
			Image: "ghcr.io/flidai/leapview@" + digest("a"), Distribution: "distroless", Platform: "linux/amd64",
		},
		ArchitectureMarker: "postgres-control/v1",
		Repository:         "ghcr.io/flidai/leapview",
		OCIDigest:          digest("a"),
		Decision:           DecisionAdmitted,
		Provenance: ProvenanceResult{
			Reference: digest("b"), Repository: "flidai/leapview", Workflow: ".github/workflows/release.yml", SourceRevision: strings.Repeat("c", 40), Verified: true,
		},
		SBOM: SBOMResult{Reference: digest("d"), PredicateType: "https://spdx.dev/Document/v2.3", Verified: true},
		SecurityPolicy: SecurityPolicyResult{
			Version: SecurityPolicyVersion, Reference: digest("e"), Scanner: "trivy", Passed: true,
		},
		AdmittedAt: time.Date(2026, 9, 13, 12, 0, 0, 123, time.UTC),
	}
}

func digest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
