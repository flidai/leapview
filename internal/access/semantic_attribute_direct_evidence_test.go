package access

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAttributeDirectEvidenceBindsSubjectClosureAndAssignments(t *testing.T) {
	definition := SemanticAttributeDefinition{ID: "definition-region", Name: "regions", Type: semanticvalue.TypeString,
		Shape: SemanticAttributeList, Profile: semanticvalue.Profile, DefinitionVersion: 2,
		LifecycleState: SemanticAttributeActive, Enabled: true}
	values, valueDigest, err := CanonicalSemanticAttributeValues(definition, []string{"west", "east"})
	if err != nil {
		t.Fatal(err)
	}
	attribute := EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
		CanonicalValues: values, ValueDigest: valueDigest, Source: "direct"}
	assignment := SemanticAttributeAssignment{ID: "assignment-region", DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: definition.DefinitionVersion, Type: definition.Type, Shape: definition.Shape,
		Subject: SubjectRef{Kind: SubjectKindGroup, ID: "group-sales"}, CanonicalValues: append([]string(nil), values...),
		ValueDigest: valueDigest, AssignmentVersion: 4}
	control := SemanticAttributeControlSnapshot{Assignments: []SemanticAttributeAssignment{assignment}}
	control.State = SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 9}
	control.State.Digest, err = SemanticAttributeControlDigest(control.Assignments, control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	subjects := []SubjectRef{{Kind: SubjectKindGroup, ID: "group-sales"}, {Kind: SubjectKindPrincipal, ID: "principal-1"}}
	evidence, err := NewSemanticAttributeDirectEvidence("instance-1", "principal-1", subjects, control, []EffectiveSemanticAttribute{attribute})
	if err != nil || !evidence.Matches("instance-1", "principal-1", control.State, []EffectiveSemanticAttribute{attribute}) {
		t.Fatalf("valid direct evidence = %#v, error = %v", evidence, err)
	}
	if !strings.HasPrefix(evidence.Digest(), "sha256:") {
		t.Fatalf("direct evidence digest = %q", evidence.Digest())
	}

	if _, err := NewSemanticAttributeDirectEvidence("instance-1", "principal-1",
		[]SubjectRef{{Kind: SubjectKindPrincipal, ID: "principal-1"}}, control, []EffectiveSemanticAttribute{attribute}); err == nil {
		t.Fatal("direct evidence accepted a group assignment outside the subject closure")
	}
	conflicting := assignment
	conflicting.ID = "assignment-conflict"
	conflicting.Subject = SubjectRef{Kind: SubjectKindPrincipal, ID: "principal-1"}
	conflicting.CanonicalValues, conflicting.ValueDigest, err = CanonicalSemanticAttributeValues(definition, []string{"north"})
	if err != nil {
		t.Fatal(err)
	}
	conflictControl := control
	conflictControl.Assignments = append(append([]SemanticAttributeAssignment(nil), control.Assignments...), conflicting)
	conflictControl.State.Digest, err = SemanticAttributeControlDigest(conflictControl.Assignments, conflictControl.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSemanticAttributeDirectEvidence("instance-1", "principal-1", subjects, conflictControl,
		[]EffectiveSemanticAttribute{attribute}); err == nil {
		t.Fatal("direct evidence accepted conflicting assignments in the subject closure")
	}
}

func TestSemanticAttributeDirectEvidenceRejectsCorruptControlAndTamperedValue(t *testing.T) {
	definition := SemanticAttributeDefinition{ID: "definition-department", Name: "department", Type: semanticvalue.TypeString,
		Shape: SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1,
		LifecycleState: SemanticAttributeActive, Enabled: true}
	values, valueDigest, err := CanonicalSemanticAttributeValues(definition, "sales")
	if err != nil {
		t.Fatal(err)
	}
	attribute := EffectiveSemanticAttribute{DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: 1, Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct"}
	assignment := SemanticAttributeAssignment{ID: "assignment-department", DefinitionID: definition.ID, DefinitionName: definition.Name,
		DefinitionVersion: 1, Type: definition.Type, Shape: definition.Shape,
		Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "principal-1"}, CanonicalValues: append([]string(nil), values...),
		ValueDigest: valueDigest, AssignmentVersion: 1}
	control := SemanticAttributeControlSnapshot{Assignments: []SemanticAttributeAssignment{assignment},
		State: SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1}}
	control.State.Digest, err = SemanticAttributeControlDigest(control.Assignments, control.Mappings)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := control
	corrupt.State.Digest = "sha256:" + strings.Repeat("f", 64)
	if _, err := NewSemanticAttributeDirectEvidence("instance-1", "principal-1",
		[]SubjectRef{{Kind: SubjectKindPrincipal, ID: "principal-1"}}, corrupt, []EffectiveSemanticAttribute{attribute}); err == nil {
		t.Fatal("direct evidence accepted a corrupt control snapshot")
	}
	tampered := attribute
	tampered.CanonicalValues, tampered.ValueDigest, err = CanonicalSemanticAttributeValues(definition, "finance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSemanticAttributeDirectEvidence("instance-1", "principal-1",
		[]SubjectRef{{Kind: SubjectKindPrincipal, ID: "principal-1"}}, control, []EffectiveSemanticAttribute{tampered}); err == nil {
		t.Fatal("direct evidence accepted a value absent from the assignment authority")
	}
}
