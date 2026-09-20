package postgres

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessownership "github.com/flidai/leapview/internal/access/ownership"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAttributeOwnershipTransferIsIdempotentAndGuardCompatiblePostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "semantic-owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "semantic-target@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := repo.RegisterSemanticAttribute(ctx, access.RegisterSemanticAttributeInput{
		Name: "ownership_region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerPrincipal, ID: owner.ID}, DisplayName: "Ownership region"},
		Mutation: access.SemanticAttributeMutationContext{ActorPrincipalID: owner.ID, RequestID: "semantic-ownership-register"},
	})
	if err != nil {
		t.Fatal(err)
	}

	authority := NewSemanticAttributeOwnershipAuthority(db.runtime)
	first, err := authority.TransferOwnedObjects(ctx, owner.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Objects) != 1 || first.Objects[0].ID != definition.ID || first.Objects[0].OwnerPrincipalID != owner.ID {
		t.Fatalf("first transfer report = %#v", first)
	}
	second, err := authority.TransferOwnedObjects(ctx, owner.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Objects) != 0 {
		t.Fatalf("transfer retry report = %#v, want no-op", second)
	}

	transferred, err := repo.SemanticAttributeDefinition(ctx, definition.Name)
	if err != nil {
		t.Fatal(err)
	}
	if transferred.Metadata.Owner.Kind != access.SemanticAttributeOwnerPrincipal || transferred.Metadata.Owner.ID != target.ID || transferred.DefinitionVersion != definition.DefinitionVersion+1 {
		t.Fatalf("transferred definition = %#v", transferred)
	}
	registry, err := repo.SemanticAttributeRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if registry.State.Revision != 2 || len(registry.Definitions) != 1 || registry.Definitions[0].Metadata.Owner.ID != target.ID {
		t.Fatalf("transferred registry = %#v", registry)
	}

	inventory, err := accessownership.New(authority)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetOwnershipGuard(inventory)
	if err := repo.DeletePrincipal(ctx, owner.ID); err != nil {
		t.Fatalf("delete source after transfer: %v", err)
	}
	if err := repo.DeletePrincipal(ctx, target.ID); !errors.Is(err, access.ErrOwnershipConflict) {
		t.Fatalf("delete target = %v, want semantic ownership conflict", err)
	}
}
