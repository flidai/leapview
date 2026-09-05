package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestDirectPGXSemanticAttributeSearchValidatesOwnerAndCursor(t *testing.T) {
	repo := &Repository{}
	for name, filter := range map[string]access.SemanticAttributeSearch{
		"long query":          {Query: strings.Repeat("x", 256)},
		"NUL query":           {Query: "region\x00secret"},
		"owner kind":          {OwnerKind: access.SemanticAttributeOwnerKind("tenant")},
		"partial cursor":      {AfterName: "region"},
		"invalid cursor id":   {AfterName: "region", AfterDefinitionID: "not-a-uuid"},
		"invalid cursor name": {AfterName: "not a valid attribute name", AfterDefinitionID: "10000000-0000-0000-0000-000000000001"},
	} {
		if _, err := repo.SearchSemanticAttributes(context.Background(), filter); err == nil {
			t.Errorf("accepted invalid %s filter", name)
		} else if !errors.Is(err, access.ErrSemanticAttributeSearchInvalid) {
			t.Errorf("invalid %s filter error = %v, want ErrSemanticAttributeSearchInvalid", name, err)
		}
	}
}

func TestDirectPGXSemanticAttributeControlDigestSeedAndOrdering(t *testing.T) {
	empty, err := semanticAttributeControlDigest(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	const wantEmpty = "sha256:e05005cdeee20cc98d9e8de8f32ed4b8da34a95f82872dc3b65a451ce7de4e37"
	if empty != wantEmpty {
		t.Fatalf("empty control digest = %q, want %q", empty, wantEmpty)
	}
	rows := []access.SemanticAttributeAssignment{
		{ID: "20000000-0000-0000-0000-000000000002", DefinitionID: "10000000-0000-0000-0000-000000000002", Subject: access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "30000000-0000-0000-0000-000000000001"}, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, CanonicalValues: []string{"b"}, ValueDigest: "sha256:" + strings.Repeat("b", 64), AssignmentVersion: 1},
		{ID: "20000000-0000-0000-0000-000000000001", DefinitionID: "10000000-0000-0000-0000-000000000001", Subject: access.SubjectRef{Kind: access.SubjectKindGroup, ID: "30000000-0000-0000-0000-000000000003"}, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, CanonicalValues: []string{"a"}, ValueDigest: "sha256:" + strings.Repeat("a", 64), AssignmentVersion: 1},
	}
	first, err := semanticAttributeControlDigest(rows, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := semanticAttributeControlDigest([]access.SemanticAttributeAssignment{rows[1], rows[0]}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("control digest depends on input order: %q != %q", first, second)
	}
}

func TestDirectPGXTrustedClaimMappingIdentityRejectsNormalization(t *testing.T) {
	valid := access.TrustedClaimSource{Kind: access.TrustedClaimSourceOIDC, Provider: "corp", Issuer: "https://issuer.example", Audience: "dashboard"}
	for name, source := range map[string]access.TrustedClaimSource{
		"provider whitespace": {Kind: valid.Kind, Provider: " corp", Issuer: valid.Issuer, Audience: valid.Audience},
		"issuer whitespace":   {Kind: valid.Kind, Provider: valid.Provider, Issuer: valid.Issuer + " ", Audience: valid.Audience},
		"audience control":    {Kind: valid.Kind, Provider: valid.Provider, Issuer: valid.Issuer, Audience: "dashboard\u0007"},
	} {
		if _, err := canonicalTrustedClaimSource(source); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	for _, claim := range []string{" claim", "claim ", "claim\u0007", ""} {
		if _, _, err := canonicalTrustedClaim(valid, claim); err == nil {
			t.Errorf("accepted invalid claim %q", claim)
		}
	}
}

func TestDirectPGXSemanticAttributeControlAuditProjectionOmitsValues(t *testing.T) {
	assignment := access.SemanticAttributeAssignment{
		ID: "assignment-1", DefinitionID: "definition-1", DefinitionName: "region",
		Subject:         access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		CanonicalValues: []string{"secret-region"}, ValueDigest: "sha256:" + strings.Repeat("a", 64), AssignmentVersion: 2,
	}
	event := semanticAttributeControlAudit(access.SemanticAttributeMutationContext{}, "semantic_attribute.assignment.set", assignment, semanticAttributeControlStateRow{Revision: 4, Digest: "sha256:" + strings.Repeat("b", 64)})
	var metadata map[string]any
	if err := json.Unmarshal([]byte(event.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["valueCount"] != float64(1) || metadata["definitionId"] != assignment.DefinitionID {
		t.Fatalf("stable audit metadata = %#v", metadata)
	}
	for _, forbidden := range []string{"canonicalValues", "valueDigest", "secret-region"} {
		if strings.Contains(event.MetadataJSON, forbidden) {
			t.Fatalf("assignment audit contains %q: %s", forbidden, event.MetadataJSON)
		}
	}
}

func TestSemanticAttributeControlDigestRejectsInvalidTombstoneTimestamp(t *testing.T) {
	_, err := semanticAttributeControlDigest([]access.SemanticAttributeAssignment{{ID: "assignment-1", TombstonedAt: "not-a-timestamp"}}, nil)
	if err == nil {
		t.Fatal("accepted invalid assignment tombstone timestamp")
	}
	if errors.Is(err, access.ErrSemanticAttributeControlCorrupt) {
		t.Fatal("digest helper should return input timestamp error, not control corruption")
	}
}
