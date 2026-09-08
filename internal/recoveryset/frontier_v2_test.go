package recoveryset

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/recoveryset/observation"
)

func TestV1FrontierDigestCompatibility(t *testing.T) {
	set := testSet(t)
	got, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:a1f8f8b06d9ed369f9a4b720489465a73b7c386687408f05fea0d2153d0aa7eb" {
		t.Fatalf("v1 frontier digest changed: %s", got)
	}
}

func v2Set(t *testing.T) RecoverySet {
	t.Helper()
	set := testSet(t)
	boundary := observation.Boundary{ProtocolVersion: 1, DatabaseIdentity: "control-db", SystemIdentity: "123456789", Timeline: 1, LSN: "0/100", RestorePointName: "leapview_test_marker", InventoryDigest: testDigest('a')}
	boundaryDigest, err := boundary.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set.SchemaVersion = 2
	set.ManagedEvidence = &ManagedEvidence{ManifestDigest: testDigest('b'), BoundaryDigest: boundaryDigest, DescriptorDigest: testDigest('c'), Boundary: boundary}
	set.ClusterPoints[0].ClusterIdentity = "postgres:" + boundary.SystemIdentity
	set.ClusterPoints[0].RecoveryIdentity = boundary.RecoveryIdentity()
	set.ClusterPoints[1].ClusterIdentity = "separate-ducklake-cluster"
	return set
}

func TestV2FrontierRequiresExactManagedBoundary(t *testing.T) {
	set := v2Set(t)
	raw, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRecoverySet(raw)
	if err != nil || !parsed.FrontierEqual(set) {
		t.Fatalf("roundtrip: %v", err)
	}
	for name, mutate := range map[string]func(*RecoverySet){
		"missing":         func(s *RecoverySet) { s.ManagedEvidence = nil },
		"manifest digest": func(s *RecoverySet) { s.ManagedEvidence.ManifestDigest = "" },
		"boundary digest": func(s *RecoverySet) { s.ManagedEvidence.BoundaryDigest = testDigest('f') },
		"wrong LSN":       func(s *RecoverySet) { s.ManagedEvidence.Boundary.LSN = "0/101" },
		"wrong database":  func(s *RecoverySet) { s.ClusterPoints[0].DatabaseIdentity = "other" },
		"wrong system":    func(s *RecoverySet) { s.ClusterPoints[0].ClusterIdentity = "other" },
		"unknown version": func(s *RecoverySet) { s.SchemaVersion = 3 },
		"downgrade":       func(s *RecoverySet) { s.SchemaVersion = 1 },
		"activation":      func(s *RecoverySet) { s.Status = StatusPublished },
	} {
		t.Run(name, func(t *testing.T) {
			s := v2Set(t)
			mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid frontier accepted")
			}
		})
	}
	if _, err := NewValidationEvidenceEnvelope(set, "018f3f83-7b2f-7b37-9f9e-000000000111"); err == nil {
		t.Fatal("v2 silently converted to v1 validation evidence")
	}
}

func TestRecoverySetParserRejectsAmbiguousEvidence(t *testing.T) {
	raw, err := v2Set(t).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"schema_version":2`, `"schema_version":2,"schema_version":1`, 1),
		strings.Replace(string(raw), `"schema_version":2`, `"schema_version":99`, 1),
		strings.Replace(string(raw), `"managed_evidence":`, `"unknown":`, 1),
		string(raw) + `{}`,
	} {
		if _, err := ParseRecoverySet([]byte(invalid)); err == nil {
			t.Fatal("ambiguous frontier accepted")
		}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "managed_evidence")
	missing, _ := json.Marshal(fields)
	if _, err := ParseRecoverySet(missing); err == nil {
		t.Fatal("missing binding accepted")
	}
}

func TestV2DigestBindsDescriptorAndIgnoresOrdering(t *testing.T) {
	set := v2Set(t)
	want, err := set.Digest()
	if err != nil {
		t.Fatal(err)
	}
	set.ClusterPoints[0], set.ClusterPoints[1] = set.ClusterPoints[1], set.ClusterPoints[0]
	got, err := set.Digest()
	if err != nil || got != want {
		t.Fatalf("ordering changed digest: %v", err)
	}
	set.ManagedEvidence.DescriptorDigest = testDigest('d')
	got, err = set.Digest()
	if err != nil || got == want {
		t.Fatalf("descriptor not bound: %v", err)
	}
}
