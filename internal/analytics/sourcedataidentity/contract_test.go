package sourcedataidentity

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEvidenceCanonicalSerializationAndDigest(t *testing.T) {
	t.Parallel()

	evidence, err := NewEvidence(EvidenceInput{
		SourceID:       "source:orders",
		RevisionDigest: testDigest("a"),
	})
	if err != nil {
		t.Fatalf("NewEvidence() error = %v", err)
	}

	wantCanonical := `{"version":1,"sourceId":"source:orders","revisionDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	if got := string(evidence.Canonical()); got != wantCanonical {
		t.Fatalf("Canonical() = %s, want %s", got, wantCanonical)
	}
	preimage := append([]byte(EvidenceDigestDomain), 0)
	preimage = append(preimage, wantCanonical...)
	wantDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(preimage))
	if got := evidence.EquivalenceDigest(); got != wantDigest {
		t.Fatalf("EquivalenceDigest() = %q, want %q", got, wantDigest)
	}
	if got := evidence.SourceID(); got != "source:orders" {
		t.Fatalf("SourceID() = %q, want source:orders", got)
	}
	if evidence.Version() != EvidenceVersion {
		t.Fatalf("Version() = %d, want %d", evidence.Version(), EvidenceVersion)
	}
}

func TestEvidenceIsDeterministicAndRotatesWithItsInputs(t *testing.T) {
	t.Parallel()

	input := EvidenceInput{SourceID: "source:orders", RevisionDigest: testDigest("a")}
	first, err := NewEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.EquivalenceDigest() != second.EquivalenceDigest() || string(first.Canonical()) != string(second.Canonical()) {
		t.Fatal("identical evidence inputs produced different identities")
	}

	revisionChanged, err := NewEvidence(EvidenceInput{SourceID: input.SourceID, RevisionDigest: testDigest("b")})
	if err != nil {
		t.Fatal(err)
	}
	if revisionChanged.EquivalenceDigest() == first.EquivalenceDigest() {
		t.Fatal("revision change did not rotate source equivalence identity")
	}

	sourceChanged, err := NewEvidence(EvidenceInput{SourceID: "source:returns", RevisionDigest: input.RevisionDigest})
	if err != nil {
		t.Fatal(err)
	}
	if sourceChanged.EquivalenceDigest() == first.EquivalenceDigest() {
		t.Fatal("source change did not rotate source equivalence identity")
	}
}

func TestEvidenceRejectsInvalidOrMissingInputs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input EvidenceInput
	}{
		{name: "missing source", input: EvidenceInput{RevisionDigest: testDigest("a")}},
		{name: "invalid source", input: EvidenceInput{SourceID: "source orders", RevisionDigest: testDigest("a")}},
		{name: "missing revision", input: EvidenceInput{SourceID: "source:orders"}},
		{name: "uppercase revision", input: EvidenceInput{SourceID: "source:orders", RevisionDigest: "sha256:" + strings.Repeat("A", 64)}},
		{name: "wrong algorithm", input: EvidenceInput{SourceID: "source:orders", RevisionDigest: "sha512:" + strings.Repeat("a", 128)}},
		{name: "malformed revision", input: EvidenceInput{SourceID: "source:orders", RevisionDigest: "sha256:not-a-digest"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if evidence, err := NewEvidence(test.input); !errors.Is(err, ErrInvalidEvidence) || evidence.Available() {
				t.Fatalf("NewEvidence() = %#v, %v; want unavailable ErrInvalidEvidence", evidence, err)
			}
		})
	}

	if (Evidence{}).Available() || (Evidence{}).EquivalenceDigest() != "" || (Evidence{}).Version() != 0 {
		t.Fatal("zero evidence must remain unavailable and must not expose a fallback identity")
	}
}

func TestEvidenceCanonicalBytesAreDefensiveCopies(t *testing.T) {
	t.Parallel()

	evidence, err := NewEvidence(EvidenceInput{SourceID: "source:orders", RevisionDigest: testDigest("a")})
	if err != nil {
		t.Fatal(err)
	}
	original := evidence.Canonical()
	returned := evidence.Canonical()
	returned[0] = 'x'
	if got := evidence.Canonical(); string(got) != string(original) {
		t.Fatalf("Canonical() exposed mutable storage: %s", got)
	}
}

func TestEvidencePreimageCapacityRejectsOverflow(t *testing.T) {
	t.Parallel()

	maximumInt := int(^uint(0) >> 1)
	if _, err := evidencePreimageCapacity(maximumInt); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("evidencePreimageCapacity(maxInt) error = %v, want ErrInvalidEvidence", err)
	}
}

func testDigest(value string) string {
	return "sha256:" + strings.Repeat(value, 64)
}
