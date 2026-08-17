package manageddata

import "testing"

func TestCollectionIDRejectsNonCanonicalWhitespace(t *testing.T) {
	if err := ValidateCollectionID(" collection"); err == nil {
		t.Fatal("ValidateCollectionID accepted leading whitespace")
	}
	if err := ValidateCollectionID("collection"); err != nil {
		t.Fatalf("ValidateCollectionID(valid) error = %v", err)
	}
}

func TestOperationalIDsRemainDistinctFromGraphIDs(t *testing.T) {
	revision, err := ParseRevisionID("revision_1")
	if err != nil {
		t.Fatal(err)
	}
	if revision.String() != "revision_1" {
		t.Fatalf("revision.String() = %q", revision.String())
	}
	if _, err := ParseRevisionID(" revision_1"); err == nil {
		t.Fatal("ParseRevisionID accepted noncanonical whitespace")
	}
}
