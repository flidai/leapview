package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestResolveSemanticAttributesPostgreSQL18ReturnsCoherentAuthorityAndClosure(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	mutation := access.SemanticAttributeMutationContext{ActorPrincipalID: auditActorID}
	region, err := repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "resolution_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation,
	})
	if err != nil {
		t.Fatal(err)
	}
	team, err := repo.RegisterSemanticAttribute(t.Context(), access.RegisterSemanticAttributeInput{
		Name: "resolution_team", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Mutation: mutation,
	})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertGroup(t.Context(), access.GroupInput{Name: "resolution-group"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(t.Context(), group.ID, auditActorID); err != nil {
		t.Fatal(err)
	}
	principalSubject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID}
	groupSubject := access.SubjectRef{Kind: access.SubjectKindGroup, ID: group.ID}
	if _, err := repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		DefinitionID: region.ID, Subject: principalSubject, Values: "west", Mutation: mutation,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetSemanticAttributeAssignment(t.Context(), access.SemanticAttributeAssignmentInput{
		DefinitionID: team.ID, Subject: groupSubject, Values: "sales", Mutation: mutation,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveSemanticAttributes(t.Context(), principalSubject)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Subject != principalSubject {
		t.Fatalf("subject = %#v, want %#v", resolved.Subject, principalSubject)
	}
	if len(resolved.Subjects) != 2 || resolved.Subjects[0] != principalSubject || resolved.Subjects[1] != groupSubject {
		t.Fatalf("subject closure = %#v, want principal followed by active group", resolved.Subjects)
	}
	if resolved.Registry.State.Revision != 2 || len(resolved.Registry.Definitions) != 2 {
		t.Fatalf("registry = %#v, want two definitions at revision 2", resolved.Registry)
	}
	if resolved.Control.State.Revision != 2 || resolved.Control.State.Digest == "" || len(resolved.Control.Assignments) != 2 {
		t.Fatalf("control = %#v, want two assignments at revision 2", resolved.Control)
	}
	if len(resolved.Attributes) != 2 {
		t.Fatalf("effective attributes = %#v, want direct and group values", resolved.Attributes)
	}
	if resolved.Attributes[0].DefinitionName != "resolution_region" || resolved.Attributes[0].CanonicalValues[0] != "west" ||
		resolved.Attributes[1].DefinitionName != "resolution_team" || resolved.Attributes[1].CanonicalValues[0] != "sales" {
		t.Fatalf("effective attributes = %#v", resolved.Attributes)
	}
	if resolved.ObservedAt.IsZero() || !resolved.ObservedAt.Equal(resolved.ObservedAt.UTC()) {
		t.Fatalf("observed at = %v, want non-zero UTC", resolved.ObservedAt)
	}
}

func TestResolveSemanticAttributesPostgreSQL18RejectsNonLivePrincipal(t *testing.T) {
	db := newAuditDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DisableProvisionedPrincipal(t.Context(), auditActorID); err != nil {
		t.Fatal(err)
	}
	_, err = repo.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID})
	if !errors.Is(err, access.ErrSemanticAttributeSourceConflict) {
		t.Fatalf("disabled principal error = %v, want source conflict", err)
	}
}

func TestResolveSemanticAttributesPostgreSQL18RejectsCallerOwnedTransaction(t *testing.T) {
	db := newAuditDatabase(t)
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	repo := &Repository{db: tx}
	_, err = repo.ResolveSemanticAttributes(t.Context(), access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: auditActorID})
	if err == nil || !strings.Contains(err.Error(), "caller-owned") {
		t.Fatalf("caller-owned transaction error = %v, want explicit ownership rejection", err)
	}
}
