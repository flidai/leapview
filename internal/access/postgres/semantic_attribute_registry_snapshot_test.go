package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestStableSemanticAttributeRegistrySnapshotRetriesOneConcurrentMutation(t *testing.T) {
	definition := func(id, name string) access.SemanticAttributeDefinition {
		return access.SemanticAttributeDefinition{ID: id, Name: name, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
			Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
			Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}}
	}
	firstDefinitions := []access.SemanticAttributeDefinition{definition("definition-a", "alpha")}
	secondDefinitions := append(append([]access.SemanticAttributeDefinition(nil), firstDefinitions...), definition("definition-b", "beta"))
	firstDigest, err := semanticAttributeRegistryDigest(semanticvalue.Profile, firstDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := semanticAttributeRegistryDigest(semanticvalue.Profile, secondDefinitions)
	if err != nil {
		t.Fatal(err)
	}
	states := []semanticAttributeRegistryStateRow{
		{Profile: semanticvalue.Profile, Revision: 1, Digest: firstDigest},
		{Profile: semanticvalue.Profile, Revision: 2, Digest: secondDigest},
		{Profile: semanticvalue.Profile, Revision: 2, Digest: secondDigest},
		{Profile: semanticvalue.Profile, Revision: 2, Digest: secondDigest},
	}
	stateCalls, definitionCalls := 0, 0
	snapshot, err := stableSemanticAttributeRegistrySnapshot(func() (semanticAttributeRegistryStateRow, error) {
		result := states[stateCalls]
		stateCalls++
		return result, nil
	}, func() ([]access.SemanticAttributeDefinition, error) {
		definitionCalls++
		if definitionCalls == 1 {
			return firstDefinitions, nil
		}
		return secondDefinitions, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stateCalls != 4 || definitionCalls != 2 || snapshot.State.Revision != 2 || snapshot.State.Digest != secondDigest || len(snapshot.Definitions) != 2 {
		t.Fatalf("snapshot/calls = %#v, states=%d definitions=%d", snapshot, stateCalls, definitionCalls)
	}
}

func TestStableSemanticAttributeRegistrySnapshotFailsAfterBoundedInstability(t *testing.T) {
	stateCalls, definitionCalls := 0, 0
	_, err := stableSemanticAttributeRegistrySnapshot(func() (semanticAttributeRegistryStateRow, error) {
		stateCalls++
		return semanticAttributeRegistryStateRow{Profile: semanticvalue.Profile, Revision: int64(stateCalls), Digest: "sha256:unstable"}, nil
	}, func() ([]access.SemanticAttributeDefinition, error) {
		definitionCalls++
		return nil, nil
	})
	if !errors.Is(err, access.ErrSemanticAttributeRegistryCorrupt) || stateCalls != 4 || definitionCalls != 2 {
		t.Fatalf("error/calls = %v, states=%d definitions=%d", err, stateCalls, definitionCalls)
	}
}
