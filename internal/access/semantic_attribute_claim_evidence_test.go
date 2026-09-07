package access

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticAttributeEvidenceVerifier struct {
	claims trustedclaims.VerifiedClaims
}

func (verifier semanticAttributeEvidenceVerifier) SourceKind() trustedclaims.SourceKind {
	return trustedclaims.SourceOIDC
}

func (verifier semanticAttributeEvidenceVerifier) Verify(context.Context, []byte) (trustedclaims.VerifiedClaims, error) {
	return verifier.claims, nil
}

func TestSemanticAttributeClaimEvidenceBindsMappedValuesAndControl(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	claims := trustedclaims.VerifiedClaims{Provider: "corp", Issuer: "https://issuer.example", Audience: "leapview", Subject: "principal-1",
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), TokenFingerprint: strings.Repeat("a", 64),
		Claims: []trustedclaims.Claim{{Name: "regions", Value: []string{"west", "east"}}}}
	envelope, err := trustedclaims.Verify(t.Context(), trustedclaims.NewRawEvidence(trustedclaims.SourceOIDC, []byte("signed")),
		semanticAttributeEvidenceVerifier{claims: claims}, trustedclaims.VerifyOptions{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	definition := SemanticAttributeDefinition{ID: "def-regions", Name: "regions", Type: semanticvalue.TypeString, Shape: SemanticAttributeList,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: SemanticAttributeActive, Enabled: true}
	values, valueDigest, err := CanonicalSemanticAttributeValues(definition, []string{"east", "west"})
	if err != nil {
		t.Fatal(err)
	}
	attribute := EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: 1,
		Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "trusted_claim"}
	mapping := TrustedClaimMapping{ID: "mapping-1", SourceKind: TrustedClaimSourceOIDC, Provider: claims.Provider, Issuer: claims.Issuer,
		Audience: claims.Audience, Claim: "regions", DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: 1, Type: definition.Type, Shape: definition.Shape, MappingVersion: 1}
	control := SemanticAttributeControlSnapshot{Mappings: []TrustedClaimMapping{mapping}}
	control.State = SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 3}
	control.State.Digest, err = SemanticAttributeControlDigest(control.Assignments, control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := NewSemanticAttributeClaimEvidence("instance-1", "principal-1", control, []EffectiveSemanticAttribute{attribute}, envelope, now)
	if err != nil || !evidence.Matches("instance-1", "principal-1", control.State, []EffectiveSemanticAttribute{attribute}) || !evidence.ValidAt(now) {
		t.Fatalf("valid evidence = %#v, error = %v", evidence, err)
	}

	tampered := attribute
	tampered.CanonicalValues, tampered.ValueDigest, err = CanonicalSemanticAttributeValues(definition, []string{"north"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSemanticAttributeClaimEvidence("instance-1", "principal-1", control, []EffectiveSemanticAttribute{tampered}, envelope, now); err == nil {
		t.Fatal("claim evidence accepted an effective value absent from the verified envelope")
	}
	corrupt := control
	corrupt.State.Digest = "sha256:" + strings.Repeat("b", 64)
	if _, err := NewSemanticAttributeClaimEvidence("instance-1", "principal-1", corrupt, []EffectiveSemanticAttribute{attribute}, envelope, now); err == nil {
		t.Fatal("claim evidence accepted a corrupt control snapshot")
	}
}

func TestSemanticAttributeRegistryDigestIsIndependentOfRowOrder(t *testing.T) {
	definitions := []SemanticAttributeDefinition{{ID: "b", Name: "beta"}, {ID: "a", Name: "alpha"}}
	first, err := SemanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SemanticAttributeRegistryDigest(semanticvalue.Profile, []SemanticAttributeDefinition{definitions[1], definitions[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("registry digest depends on row order: %q != %q", first, second)
	}
}
