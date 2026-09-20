package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestOwnershipAuthorityBlocksPrincipalAndServiceOffboardingPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "owned-user@example.test", DisplayName: "Owned User"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "Owned Service"})
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []access.Principal{owner, service} {
		if _, err := db.runtime.Exec(ctx, `
INSERT INTO access.semantic_attribute_definition
 (definition_id,name,value_type,value_shape,profile,owner_kind,owner_id)
			VALUES (uuidv7(), $1, 'String', 'scalar', 'leapview.semantic-access/v1', 'principal', $2::uuid)`, "owned_"+strings.ReplaceAll(principal.ID, "-", ""), principal.ID); err != nil {
			t.Fatal(err)
		}
		report, err := repo.ListOwnedObjects(ctx, principal.ID)
		if err != nil {
			t.Fatalf("list owned objects for %s: %v", principal.Kind, err)
		}
		if len(report.Objects) != 1 || report.Objects[0].OwnerPrincipalID != principal.ID || report.Objects[0].Kind != "semantic_attribute" {
			t.Fatalf("ownership report for %s = %#v", principal.Kind, report)
		}
		if err := repo.EnsureOffboardingSafe(ctx, principal.ID); !errors.Is(err, access.ErrOwnershipConflict) {
			t.Fatalf("offboarding guard for %s = %v, want ownership conflict", principal.Kind, err)
		}
		var deleteErr error
		if principal.Kind == access.PrincipalKindServicePrincipal {
			deleteErr = repo.DeleteServicePrincipal(ctx, principal.ID)
		} else {
			deleteErr = repo.DeletePrincipal(ctx, principal.ID)
		}
		if !errors.Is(deleteErr, access.ErrOwnershipConflict) {
			t.Fatalf("%s deletion error = %v, want ownership conflict", principal.Kind, deleteErr)
		}
	}
}

func TestOwnershipAuthorityAllowsOffboardingAfterOwnerChangedPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "transferred-owner@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	definitionID := "018f4f2e-0000-7000-0000-000000001201"
	if _, err := db.runtime.Exec(ctx, `
INSERT INTO access.semantic_attribute_definition
 (definition_id,name,value_type,value_shape,profile,owner_kind,owner_id)
VALUES ($1::uuid,'transferred_attribute','String','scalar','leapview.semantic-access/v1','principal',$2::uuid)`, definitionID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `
UPDATE access.semantic_attribute_definition
SET owner_kind='instance', owner_id=NULL, definition_version=definition_version+1
WHERE definition_id=$1::uuid`, definitionID); err != nil {
		t.Fatal(err)
	}
	report, err := repo.ListOwnedObjects(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Objects) != 0 {
		t.Fatalf("transferred ownership remained visible: %#v", report)
	}
	if err := repo.DeletePrincipal(ctx, owner.ID); err != nil {
		t.Fatalf("delete after ownership transfer: %v", err)
	}
	if _, err := repo.PrincipalByID(ctx, owner.ID); err == nil {
		t.Fatal("offboarded principal remained readable")
	}
}

func TestOwnershipAuthorityProtectsLastPlatformAdministratorPostgreSQL18(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{Kind: access.PrincipalKindUser, Email: "last-admin@example.test", DisplayName: "Last Admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: admin.ID, Email: admin.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureOffboardingSafe(ctx, admin.ID); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("offboarding guard=%v, want last-admin conflict", err)
	}
	if err := repo.DeletePrincipal(ctx, admin.ID); !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("deletion=%v, want last-admin conflict", err)
	}
}
