package postgres

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/trustedclaims"
)

func TestTrustedClaimSourceIdentityUsesVerifierLimits(t *testing.T) {
	source := access.TrustedClaimSource{
		Kind:     access.TrustedClaimSourceOIDC,
		Provider: strings.Repeat("p", trustedclaims.MaxProviderBytes),
		Issuer:   strings.Repeat("i", trustedclaims.MaxIssuerBytes),
		Audience: strings.Repeat("a", trustedclaims.MaxAudienceBytes),
	}
	if _, err := canonicalTrustedClaimSource(source); err != nil {
		t.Fatalf("source identity at verifier limits rejected: %v", err)
	}

	for _, test := range []struct {
		name string
		edit func(*access.TrustedClaimSource)
	}{
		{name: "provider", edit: func(source *access.TrustedClaimSource) { source.Provider += "p" }},
		{name: "issuer", edit: func(source *access.TrustedClaimSource) { source.Issuer += "i" }},
		{name: "audience", edit: func(source *access.TrustedClaimSource) { source.Audience += "a" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := source
			test.edit(&candidate)
			if _, err := canonicalTrustedClaimSource(candidate); err == nil {
				t.Fatal("source identity above verifier limit was accepted")
			}
		})
	}
}

func TestSemanticAttributeControlMigrationUsesVerifierIssuerLimit(t *testing.T) {
	constraint := "octet_length(issuer) BETWEEN 1 AND 1024"
	if !strings.Contains(SchemaSQL(), constraint) {
		t.Fatalf("semantic attribute control migration is missing %q", constraint)
	}
}

func TestEffectiveResolutionRejectsSourceAboveVerifierLimitBeforeDatabaseAccess(t *testing.T) {
	repo := &Repository{}
	source := access.TrustedClaimSource{
		Kind:     access.TrustedClaimSourceOIDC,
		Provider: "provider",
		Issuer:   strings.Repeat("i", trustedclaims.MaxIssuerBytes+1),
		Audience: "audience",
	}
	_, err := repo.effectiveSemanticAttributeAssignments(t.Context(), access.SubjectRef{
		Kind: access.SubjectKindPrincipal,
		ID:   controlSubjectID,
	}, source, nil)
	if err == nil {
		t.Fatal("effective resolution accepted a source identity above the verifier limit")
	}
}
