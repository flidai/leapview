package release

import (
	"strings"
	"testing"
)

func TestCandidateSourcesDataRevisionIsCanonicalAndOrderIndependent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	first, err := CandidateSourcesDataRevision(digest, []ManagedDataPin{{ConnectionID: "z", RevisionID: "rev-z"}, {ConnectionID: "a", RevisionID: "rev-a"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CandidateSourcesDataRevision(digest, []ManagedDataPin{{ConnectionID: "a", RevisionID: "rev-a"}, {ConnectionID: "z", RevisionID: "rev-z"}})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "sources:sha256:") {
		t.Fatalf("revisions differ or have wrong prefix: %q != %q", first, second)
	}
}

func TestCandidateSourcesDataRevisionRejectsInvalidArtifactAndPins(t *testing.T) {
	if _, err := CandidateSourcesDataRevision("sha256:artifact", nil); err == nil {
		t.Fatal("invalid artifact digest was accepted")
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	for _, pins := range [][]ManagedDataPin{
		{{ConnectionID: "", RevisionID: "rev"}},
		{{ConnectionID: "orders", RevisionID: ""}},
		{{ConnectionID: "orders", RevisionID: "rev-1"}, {ConnectionID: "orders", RevisionID: "rev-2"}},
	} {
		if _, err := CandidateSourcesDataRevision(digest, pins); err == nil {
			t.Fatalf("invalid pins were accepted: %#v", pins)
		}
	}
}
