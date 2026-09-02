package access

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAttributeShapeAndOwnerKindValidation(t *testing.T) {
	for _, shape := range []SemanticAttributeShape{SemanticAttributeScalar, SemanticAttributeList} {
		if !shape.Valid() {
			t.Errorf("SemanticAttributeShape(%q).Valid() = false", shape)
		}
	}
	if SemanticAttributeShape("map").Valid() {
		t.Fatal("unsupported semantic attribute shape is valid")
	}

	for _, kind := range []SemanticAttributeOwnerKind{
		SemanticAttributeOwnerInstance,
		SemanticAttributeOwnerPrincipal,
		SemanticAttributeOwnerGroup,
	} {
		if !kind.Valid() {
			t.Errorf("SemanticAttributeOwnerKind(%q).Valid() = false", kind)
		}
	}
	if SemanticAttributeOwnerKind("project").Valid() {
		t.Fatal("unsupported semantic attribute owner kind is valid")
	}
}

func TestValidateSemanticAttributeCompatibilityPreservesStableIdentityAndType(t *testing.T) {
	current := SemanticAttributeDefinition{
		ID:      "0198ff98-e3e0-7f27-9059-39d08735feda",
		Name:    "region",
		Type:    semanticvalue.TypeString,
		Shape:   SemanticAttributeScalar,
		Profile: semanticvalue.Profile,
	}
	compatible := current
	compatible.Metadata.DisplayName = "Sales region"
	compatible.DefinitionVersion = 2
	compatible.Enabled = false
	compatible.LifecycleState = SemanticAttributeDisabled
	if err := ValidateSemanticAttributeCompatibility(current, compatible); err != nil {
		t.Fatalf("compatible metadata and lifecycle change: %v", err)
	}

	for name, mutate := range map[string]func(*SemanticAttributeDefinition){
		"id":      func(candidate *SemanticAttributeDefinition) { candidate.ID = "0198ff98-e3e0-7f27-9059-39d08735fedb" },
		"name":    func(candidate *SemanticAttributeDefinition) { candidate.Name = "country" },
		"type":    func(candidate *SemanticAttributeDefinition) { candidate.Type = semanticvalue.TypeInteger },
		"shape":   func(candidate *SemanticAttributeDefinition) { candidate.Shape = SemanticAttributeList },
		"profile": func(candidate *SemanticAttributeDefinition) { candidate.Profile = "semantic-value/v2" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := current
			mutate(&candidate)
			if err := ValidateSemanticAttributeCompatibility(current, candidate); !errors.Is(err, ErrSemanticAttributeConflict) {
				t.Fatalf("error = %v, want semantic attribute conflict", err)
			}
		})
	}
}
