package access

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestTrustedClaimSourceKindValid(t *testing.T) {
	for _, kind := range []TrustedClaimSourceKind{
		TrustedClaimSourceSAML,
		TrustedClaimSourceOIDC,
		TrustedClaimSourceEmbed,
		TrustedClaimSourceServiceToken,
	} {
		if !kind.Valid() {
			t.Errorf("TrustedClaimSourceKind(%q).Valid() = false", kind)
		}
	}
	for _, kind := range []TrustedClaimSourceKind{"", "SAML", "oidc ", "browser"} {
		if kind.Valid() {
			t.Errorf("TrustedClaimSourceKind(%q).Valid() = true", kind)
		}
	}
}

func TestValidateSemanticAttributeSubject(t *testing.T) {
	for _, subject := range []SubjectRef{
		{Kind: SubjectKindPrincipal, ID: "principal-1"},
		{Kind: SubjectKindGroup, ID: "group-1"},
	} {
		if err := ValidateSemanticAttributeSubject(subject); err != nil {
			t.Errorf("ValidateSemanticAttributeSubject(%#v) = %v", subject, err)
		}
	}
	for _, subject := range []SubjectRef{
		{},
		{Kind: SubjectKind("domain"), ID: "domain-1"},
		{Kind: SubjectKindPrincipal},
		{Kind: SubjectKindGroup, ID: "  \t"},
	} {
		if err := ValidateSemanticAttributeSubject(subject); !errors.Is(err, ErrInvalidSubjectRef) {
			t.Errorf("ValidateSemanticAttributeSubject(%#v) = %v, want invalid subject", subject, err)
		}
	}
}

func TestCanonicalSemanticAttributeValuesRejectsInvalidShapesAndInputs(t *testing.T) {
	if _, _, err := CanonicalSemanticAttributeValues(SemanticAttributeDefinition{Name: "disabled"}, "value"); !errors.Is(err, ErrSemanticAttributeDisabled) {
		t.Fatalf("disabled definition error = %v, want disabled error", err)
	}
	if _, _, err := CanonicalSemanticAttributeValues(SemanticAttributeDefinition{Enabled: true, Shape: SemanticAttributeShape("map")}, "value"); !errors.Is(err, semanticvalue.ErrInvalidValue) {
		t.Fatalf("invalid shape error = %v, want invalid value", err)
	}

	list := SemanticAttributeDefinition{Enabled: true, Type: semanticvalue.TypeString, Shape: SemanticAttributeList}
	for _, input := range []any{nil, "not-a-list", []any{nil}, []any{1}} {
		if _, _, err := CanonicalSemanticAttributeValues(list, input); !errors.Is(err, semanticvalue.ErrInvalidValue) {
			t.Errorf("list input %#v error = %v, want invalid value", input, err)
		}
	}
	if values, _, err := CanonicalSemanticAttributeValues(list, [2]string{"b", "a"}); err != nil || strings.Join(values, ",") != "a,b" {
		t.Fatalf("array list canonicalization = %#v, %v", values, err)
	}

	scalar := SemanticAttributeDefinition{Enabled: true, Type: semanticvalue.TypeString, Shape: SemanticAttributeScalar}
	if values, _, err := CanonicalSemanticAttributeValues(scalar, "value"); err != nil || len(values) != 1 || values[0] != "value" {
		t.Fatalf("scalar canonicalization = %#v, %v", values, err)
	}
	for _, input := range []any{nil, map[string]string{"blocked": "value"}, func() {}} {
		if _, _, err := CanonicalSemanticAttributeValues(scalar, input); !errors.Is(err, semanticvalue.ErrInvalidValue) {
			t.Errorf("scalar input %#v error = %v, want invalid value", input, err)
		}
	}
}
