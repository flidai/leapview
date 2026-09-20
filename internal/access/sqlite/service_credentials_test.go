package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestServicePrincipalCredentialLifecycleAndRotation(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_lifecycle", DisplayName: "Lifecycle"})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	oldSecret, oldRow, err := repo.CreateServicePrincipalSecret(ctx, principal.ID, access.ServicePrincipalSecretInput{Name: "old"})
	if err != nil {
		t.Fatalf("create old secret: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, oldSecret); err != nil {
		t.Fatalf("authenticate old secret: %v", err)
	}
	metadata, err := repo.GetServicePrincipalSecret(ctx, principal.ID, oldRow.ID)
	if err != nil {
		t.Fatalf("read secret metadata: %v", err)
	}
	if metadata.LastUsedAt == "" {
		t.Fatal("last-used evidence is empty after successful authentication")
	}

	if _, err := repo.DisableServicePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("disable service principal: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, oldSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled old secret error = %v, want sql.ErrNoRows", err)
	}
	metadata, err = repo.GetServicePrincipalSecret(ctx, principal.ID, oldRow.ID)
	if err != nil {
		t.Fatalf("read revoked metadata: %v", err)
	}
	if metadata.RevokedAt == "" {
		t.Fatal("disable did not revoke the old service secret")
	}
	if _, err := repo.EnableServicePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("enable service principal: %v", err)
	}

	newSecret, newRow, err := repo.CreateServicePrincipalSecret(ctx, principal.ID, access.ServicePrincipalSecretInput{Name: "new"})
	if err != nil {
		t.Fatalf("create new secret: %v", err)
	}
	rotation, err := repo.RotateServicePrincipalSecret(ctx, access.ServicePrincipalSecretRotationInput{
		ServicePrincipalID: principal.ID,
		PreviousSecretID:   newRow.ID,
		Secret:             access.ServicePrincipalSecretInput{Name: "overlap"},
	})
	if err != nil {
		t.Fatalf("overlap rotation: %v", err)
	}
	if rotation.Secret == "" || rotation.Created.ID == newRow.ID {
		t.Fatalf("rotation result = %#v", rotation)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, newSecret); err != nil {
		t.Fatalf("previous secret should remain valid during overlap: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, rotation.Secret); err != nil {
		t.Fatalf("rotated secret: %v", err)
	}

	finalRotation, err := repo.RotateServicePrincipalSecret(ctx, access.ServicePrincipalSecretRotationInput{
		ServicePrincipalID: principal.ID,
		PreviousSecretID:   rotation.Created.ID,
		Secret:             access.ServicePrincipalSecretInput{Name: "final"},
		RevokePrevious:     true,
	})
	if err != nil {
		t.Fatalf("revoking rotation: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, rotation.Secret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked rotated secret error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, finalRotation.Secret); err != nil {
		t.Fatalf("final rotated secret: %v", err)
	}
	if err := repo.RevokeAllServicePrincipalCredentials(ctx, principal.ID); err != nil {
		t.Fatalf("revoke all service credentials: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, principal.ID, finalRotation.Secret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoke-all final secret error = %v, want sql.ErrNoRows", err)
	}
}
