package postgres

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/recoveryset/successor"
)

func TestSuccessorVerificationMetadataIsCanonicalAndGenerationStable(t *testing.T) {
	generation := TrustGeneration{
		IncarnationID: "018f3f83-7b2f-7b37-9f9e-000000000099",
		Revision:      7,
		PolicyDigest:  "sha256:" + "a" + "bcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
	}
	verifiedAt := time.Date(2026, 9, 8, 1, 2, 3, 456789000, time.UTC)
	raw, err := marshalSuccessorVerificationMetadata(generation, verifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseSuccessorVerificationMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Generation != generation || !parsed.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("parsed metadata = %#v, want generation %#v at %s", parsed, generation, verifiedAt)
	}
	if !successorVerificationMetadataGenerationEqual(raw, generation) {
		t.Fatal("exact generation should match")
	}
	if err := validateSuccessorVerificationMetadataRead(raw, verifiedAt.Add(-time.Microsecond)); err == nil {
		t.Fatal("future archival verification clock was accepted")
	}
	if err := validateSuccessorVerificationMetadataRead(raw, verifiedAt.Add(time.Microsecond)); err != nil {
		t.Fatalf("historical verification clock rejected: %v", err)
	}

	withWhitespace := append(append([]byte(nil), raw...), ' ')
	if _, err := parseSuccessorVerificationMetadata(withWhitespace); err == nil {
		t.Fatal("non-canonical metadata whitespace was accepted")
	}
	unknown := bytes.Replace(raw, []byte(`"verified_at"`), []byte(`"unknown":"x","verified_at"`), 1)
	if _, err := parseSuccessorVerificationMetadata(unknown); err == nil {
		t.Fatal("unknown metadata field was accepted")
	}
	duplicate := bytes.Replace(raw, []byte(`"revision":7`), []byte(`"revision":7,"revision":7`), 1)
	if _, err := parseSuccessorVerificationMetadata(duplicate); err == nil {
		t.Fatal("duplicate metadata field was accepted")
	}
	missingClock := bytes.Replace(raw, []byte(`,"verified_at":"2026-09-08T01:02:03.456789Z"`), nil, 1)
	if _, err := parseSuccessorVerificationMetadata(missingClock); err == nil {
		t.Fatal("missing verification clock was accepted")
	}

	retry, err := marshalSuccessorVerificationMetadata(generation, verifiedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !successorVerificationMetadataGenerationEqual(retry, generation) {
		t.Fatal("retry with the same generation should compare equal")
	}
}

func TestSuccessorSet3ScalarAndRootIntegrityComparisons(t *testing.T) {
	set, trust := successorIntegrityFixture(t)
	canonical, err := set.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want, err := successorSet3ScalarsForOwner(set, TrustInput{Evidence: trust}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !successorSet3ScalarsEqual(want, want) {
		t.Fatal("owner scalar tuple did not compare equal to itself")
	}
	badScalars := want
	badScalars.FrontierDigest = "sha256:" + "0" + "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if successorSet3ScalarsEqual(badScalars, want) {
		t.Fatal("frontier scalar corruption was accepted")
	}
	badCanonical := want
	badCanonical.CanonicalBytes = append([]byte(nil), want.CanonicalBytes...)
	badCanonical.CanonicalBytes[len(badCanonical.CanonicalBytes)-1] ^= 1
	if successorSet3ScalarsEqual(badCanonical, want) {
		t.Fatal("canonical set corruption was accepted")
	}
	if _, err := successorSet3ScalarsForOwner(set, TrustInput{Evidence: trust}, []byte("{}")); err == nil {
		t.Fatal("caller-supplied non-owner canonical bytes were accepted")
	}

	wantRoots, err := successorSet3RootRowsForOwner(set)
	if err != nil {
		t.Fatal(err)
	}
	if !successorSet3RootRowsEqual(wantRoots, wantRoots) {
		t.Fatal("owner root rows did not compare equal to themselves")
	}
	badRoots := append([]successorSet3RootRow(nil), wantRoots...)
	badRoots[0].RootDigest = "sha256:" + "0" + "123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if successorSet3RootRowsEqual(badRoots, wantRoots) {
		t.Fatal("root digest corruption was accepted")
	}
	badRoots = append([]successorSet3RootRow(nil), wantRoots...)
	badRoots[0].CanonicalBytes = append([]byte(nil), wantRoots[0].CanonicalBytes...)
	badRoots[0].CanonicalBytes[len(badRoots[0].CanonicalBytes)-1] ^= 1
	if successorSet3RootRowsEqual(badRoots, wantRoots) {
		t.Fatal("root canonical-byte corruption was accepted")
	}
	if successorSet3RootRowsEqual(wantRoots[:1], wantRoots) {
		t.Fatal("missing root was accepted")
	}
}

func successorIntegrityFixture(t *testing.T) (successor.RecoverySet3, successor.Evidence) {
	t.Helper()
	read := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join("..", "successor", "testdata", "frozen", "minimal-"+name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSuffix(raw, []byte{'\n'})
	}
	set, err := successor.ParseRecoverySet3(read("set"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := successor.ParseManagedManifest2(read("manifest"))
	if err != nil {
		t.Fatal(err)
	}
	anchor, err := successor.ParseSourceAnchor(read("anchor"))
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := successor.ParseProviderProfileSet(read("profiles"))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := successor.ParseReceipt(read("receipt"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := successor.ParseAuthorityRegistry(read("authority"))
	if err != nil {
		t.Fatal(err)
	}
	evidence := successor.Evidence{
		Manifest:    manifest,
		Anchor:      anchor,
		Profiles:    profiles,
		Receipt:     receipt,
		Authorities: authority,
		ExpectedScope: successor.ExpectedScope{
			SetID:                      set.ID,
			SourceFrontierAnchorDigest: set.SourceFrontierAnchorDigest,
			ManagedClosureDigest:       manifest.ManagedClosureDigest,
		},
		VerificationTime: time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC),
	}
	if err := set.ValidateEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	return set, evidence
}
