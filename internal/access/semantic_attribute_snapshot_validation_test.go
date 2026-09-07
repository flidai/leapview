package access

import (
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestValidateSemanticAttributeRegistrySnapshotRejectsDigestValidDerivedLifecycleMismatch(t *testing.T) {
	definition := SemanticAttributeDefinition{ID: "definition-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1,
		Metadata:       SemanticAttributeMetadata{Owner: SemanticAttributeOwner{Kind: SemanticAttributeOwnerInstance}},
		LifecycleState: SemanticAttributeActive, Enabled: true}
	snapshot := SemanticAttributeRegistrySnapshot{Definitions: []SemanticAttributeDefinition{definition},
		State: SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1}}
	var err error
	snapshot.State.Digest, err = SemanticAttributeRegistryDigest(snapshot.State.Profile, snapshot.Definitions)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSemanticAttributeRegistrySnapshot(snapshot); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}

	// Enabled is a derived projection and is intentionally absent from the
	// persisted digest wire. Admission must still reject the inconsistency.
	snapshot.Definitions[0].Enabled = false
	if err := ValidateSemanticAttributeRegistrySnapshot(snapshot); err == nil {
		t.Fatal("registry validation accepted a digest-valid lifecycle mismatch")
	}
}

func TestValidateSemanticAttributeControlSnapshotRejectsDigestValidTombstoneMismatch(t *testing.T) {
	definition := SemanticAttributeDefinition{Type: semanticvalue.TypeString, Shape: SemanticAttributeScalar,
		Profile: semanticvalue.Profile, LifecycleState: SemanticAttributeActive, Enabled: true}
	values, valueDigest, err := CanonicalSemanticAttributeValues(definition, "west")
	if err != nil {
		t.Fatal(err)
	}
	assignment := SemanticAttributeAssignment{ID: "assignment-region", DefinitionID: "definition-region", DefinitionName: "region",
		DefinitionVersion: 1, Type: semanticvalue.TypeString, Shape: SemanticAttributeScalar,
		Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "principal-1"}, CanonicalValues: values,
		ValueDigest: valueDigest, AssignmentVersion: 1}
	snapshot := SemanticAttributeControlSnapshot{Assignments: []SemanticAttributeAssignment{assignment},
		State: SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1}}
	snapshot.State.Digest, err = SemanticAttributeControlDigest(snapshot.Assignments, snapshot.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSemanticAttributeControlSnapshot(snapshot); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}

	// Tombstoned is derived from TombstonedAt and intentionally absent from
	// the persisted digest wire.
	snapshot.Assignments[0].Tombstoned = true
	if err := ValidateSemanticAttributeControlSnapshot(snapshot); err == nil {
		t.Fatal("control validation accepted a digest-valid tombstone mismatch")
	}
}
