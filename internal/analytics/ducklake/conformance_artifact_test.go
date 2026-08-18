package ducklake

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

func artifactEvidence(t *testing.T) physicalpool.Evidence {
	t.Helper()
	checks := make([]physicalpool.EvidenceCheck, 0, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		sum := sha256.Sum256([]byte(name))
		checks = append(checks, physicalpool.EvidenceCheck{ID: name, Passed: true, ObservationDigest: fmt.Sprintf("sha256:%x", sum)})
	}
	evidence, err := physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: physicalpool.Compatibility{
			DuckDBRuntime: "duckdb:1.5.4", DuckLakeExtension: "ducklake:0.3.0",
			CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "s3",
			ObjectNamingContract: "uuidv7:v1",
		},
		ConformanceVersion: SharedPoolConformanceVersion, Checks: checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestSharedPoolEvidenceArtifactIsCompleteAndPortable(t *testing.T) {
	evidence := artifactEvidence(t)
	encoded, err := MarshalSharedPoolEvidence(evidence)
	if err != nil {
		t.Fatalf("marshal evidence: %v", err)
	}
	decoded, err := physicalpool.UnmarshalEvidenceArtifact(encoded)
	if err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if decoded.Digest != evidence.Digest {
		t.Fatalf("digest changed across artifact: %q vs %q", decoded.Digest, evidence.Digest)
	}
	var streamed bytes.Buffer
	if err := WriteSharedPoolEvidence(&streamed, evidence); err != nil {
		t.Fatalf("write evidence: %v", err)
	}
	if streamed.String() != string(encoded) {
		t.Fatalf("streamed artifact differs from marshal output")
	}
}

func TestSharedPoolEvidenceArtifactRejectsUnknownChecklist(t *testing.T) {
	evidence := artifactEvidence(t)
	evidence.Checks[0].ID = "unknown-check"
	if _, err := MarshalSharedPoolEvidence(evidence); err == nil {
		t.Fatal("unknown checklist item unexpectedly accepted")
	}
}
