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

func TestAdmissionCanonicalGoldenVector(t *testing.T) {
	document, err := testAdmission().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	const expectedDocument = `{"version":"oci-artifact-admission/v1","release":{"releaseId":"release-1","version":"v1.2.3","sourceRevision":"cccccccccccccccccccccccccccccccccccccccc","image":"ghcr.io/flidai/leapview@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","distribution":"distroless","platform":"linux/amd64"},"architectureMarker":"postgres-control/v1","repository":"ghcr.io/flidai/leapview","ociDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","decision":"admitted","provenance":{"reference":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repository":"flidai/leapview","workflow":"flidai/leapview/.github/workflows/release.yml","sourceRevision":"cccccccccccccccccccccccccccccccccccccccc","verified":true},"sbom":{"reference":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","predicateType":"https://spdx.dev/Document/v2.3","producer":"docker/buildx","verified":true},"securityPolicy":{"version":"oci-security-policy/v1","reference":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","scanner":"trivy","passed":true},"admittedAt":"2026-09-13T12:00:00Z"}`
	if string(document) != expectedDocument {
		t.Fatalf("canonical admission changed:\n got: %s\nwant: %s", document, expectedDocument)
	}
	digest, err := testAdmission().Digest()
	if err != nil {
		t.Fatal(err)
	}
	const expectedDigest = "sha256:c202689b2676b062433efccec6a868850d22cdb2cc94d3984be615de7fb0e43e"
	if digest != expectedDigest {
		t.Fatalf("admission digest = %q, want %q", digest, expectedDigest)
	}
	parsed, err := ParseCanonical([]byte(expectedDocument))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Provenance.Repository != SourceRepository || parsed.SBOM.Producer != SBOMProducerBuildx {
		t.Fatalf("parsed trust profile = %#v", parsed)
	}
}

func TestAdmissionRejectsUntrustedOrMutableEvidence(t *testing.T) {
	tests := map[string]func(*Admission){
		"unsupported version":        func(a *Admission) { a.Version = "oci-artifact-admission/v2" },
		"mutable image":              func(a *Admission) { a.Release.Image = "ghcr.io/flidai/leapview:latest" },
		"tagged digest":              func(a *Admission) { a.Release.Image = "ghcr.io/flidai/leapview:v1@" + digest("a") },
		"repository mismatch":        func(a *Admission) { a.Repository = "ghcr.io/flidai/other" },
		"digest mismatch":            func(a *Admission) { a.OCIDigest = digest("b") },
		"not admitted":               func(a *Admission) { a.Decision = "rejected" },
		"missing provenance":         func(a *Admission) { a.Provenance.Reference = "" },
		"fake provenance repository": func(a *Admission) { a.Provenance.Repository = "attacker/example" },
		"fake provenance workflow":   func(a *Admission) { a.Provenance.Workflow = "flidai/leapview/.github/workflows/untrusted.yml" },
		"mutable source revision": func(a *Admission) {
			a.Release.SourceRevision, a.Provenance.SourceRevision = "refs/heads/main", "refs/heads/main"
		},
		"invalid source revision": func(a *Admission) {
			a.Release.SourceRevision, a.Provenance.SourceRevision = strings.Repeat("A", 40), strings.Repeat("A", 40)
		},
		"missing sbom":                func(a *Admission) { a.SBOM.Reference = "" },
		"unsupported sbom predicate":  func(a *Admission) { a.SBOM.PredicateType = "https://cyclonedx.org/schema" },
		"non-SPDX sbom producer":      func(a *Admission) { a.SBOM.Producer = "unknown/producer" },
		"unapproved scanner":          func(a *Admission) { a.SecurityPolicy.Scanner = "unknown-scanner" },
		"unsupported security policy": func(a *Admission) { a.SecurityPolicy.Version = "oci-security-policy/v2" },
		"fabricated provenance result": func(a *Admission) {
			a.Provenance.Reference, a.Provenance.Verified = "", true
		},
		"fabricated sbom result": func(a *Admission) {
			a.SBOM.Reference, a.SBOM.Verified = "", true
		},
		"fabricated security result": func(a *Admission) {
			a.SecurityPolicy.Reference, a.SecurityPolicy.Passed = "", true
		},
		"missing admission time": func(a *Admission) { a.AdmittedAt = time.Time{} },
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

func TestAdmissionDerivesResultFlagsFromValidatedEvidence(t *testing.T) {
	record := testAdmission()
	record.Provenance.Verified = false
	record.SBOM.Verified = false
	record.SecurityPolicy.Passed = false
	document, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCanonical(document)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Provenance.Verified || !parsed.SBOM.Verified || !parsed.SecurityPolicy.Passed {
		t.Fatalf("derived result flags = provenance:%t sbom:%t security:%t", parsed.Provenance.Verified, parsed.SBOM.Verified, parsed.SecurityPolicy.Passed)
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
			Reference: digest("b"), Repository: SourceRepository, Workflow: "flidai/leapview/.github/workflows/release.yml", SourceRevision: strings.Repeat("c", 40), Verified: true,
		},
		SBOM: SBOMResult{Reference: digest("d"), PredicateType: SBOMPredicateSPDX, Producer: SBOMProducerBuildx, Verified: true},
		SecurityPolicy: SecurityPolicyResult{
			Version: SecurityPolicyVersion, Reference: digest("e"), Scanner: SecurityScannerTrivy, Passed: true,
		},
		AdmittedAt: time.Date(2026, 9, 13, 12, 0, 0, 123, time.UTC),
	}
}

func digest(ch string) string { return "sha256:" + strings.Repeat(ch, 64) }
