package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestProvisionDevelopmentLocalCredentialPreservesPrincipalAndCannotReplaceLogin(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("development-credential-test-key-0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: "00000000-0000-7000-8000-000000000001", Email: "dev@localhost",
		DisplayName: "Local Developer", Role: access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPermissions, err := access.InitialProjectClaimPermissions("instance_demo")
	if err != nil {
		t.Fatal(err)
	}
	publisherPermissions, err := access.InitialProjectPublisherPermissions("project_demo")
	if err != nil {
		t.Fatal(err)
	}
	prepared := false
	credentials, err := repo.ProvisionDevelopmentOperator(ctx, principal.ID, bootstrapPermissions, publisherPermissions, func(value DevelopmentCredentials) error {
		prepared = value.Email == principal.Email && value.Password != "" && value.BootstrapToken != "" && value.PublisherToken != ""
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared {
		t.Fatal("credential bundle was not prepared before commit")
	}
	loggedIn, credential, err := repo.VerifyLocalPassword(ctx, principal.Email, credentials.Password)
	if err != nil || loggedIn.ID != principal.ID || credential.MustChangePassword {
		t.Fatalf("login principal=%#v credential=%#v err=%v", loggedIn, credential, err)
	}
	publisher, err := repo.CredentialForAPIToken(ctx, credentials.PublisherToken)
	if err != nil || publisher.Principal.ID != principal.ID || publisher.Token.PermissionProfile != access.PermissionCatalogProfile {
		t.Fatalf("publisher token = %#v, err=%v", publisher, err)
	}
	rotated := ""
	if err := repo.ProvisionDevelopmentPublisherToken(ctx, principal.ID, publisherPermissions, publisher.Token.ID, func(secret string) error {
		rotated = secret
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CredentialForAPIToken(ctx, credentials.PublisherToken); err == nil {
		t.Fatal("replaced publisher token remained active")
	}
	if credential, err := repo.CredentialForAPIToken(ctx, rotated); err != nil || credential.Principal.ID != principal.ID {
		t.Fatalf("rotated publisher token = %#v, err=%v", credential, err)
	}
	if token, err := repo.CredentialForAPIToken(ctx, credentials.BootstrapToken); err != nil || token.Token.Name != access.APITokenNameInitialProjectClaim || token.Token.PermissionProfile != access.PermissionCatalogProfile || !exactPermissionSet(token.Token.Permissions, bootstrapPermissions) {
		t.Fatalf("bootstrap token = %#v, err=%v", token, err)
	}
	if _, err := repo.ProvisionDevelopmentOperator(ctx, principal.ID, bootstrapPermissions, publisherPermissions, nil); !errors.Is(err, access.ErrPrincipalAlreadyExists) {
		t.Fatalf("repeat provisioning error = %v, want conflict", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, principal.Email, credentials.Password); err != nil {
		t.Fatalf("original login was replaced: %v", err)
	}
	if _, err := repo.ResetLocalPassword(ctx, principal.ID); err != nil {
		t.Fatalf("simulate credential drift: %v", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, principal.Email, credentials.Password); err == nil {
		t.Fatal("old private bundle still matched after credential drift")
	}
	if err := repo.RestoreDevelopmentLogin(ctx, principal.ID, credentials.Password); err != nil {
		t.Fatalf("restore private development login: %v", err)
	}
	if restored, local, err := repo.VerifyLocalPassword(ctx, principal.Email, credentials.Password); err != nil || restored.ID != principal.ID || local.MustChangePassword {
		t.Fatalf("restored login principal=%#v credential=%#v err=%v", restored, local, err)
	}
	if token, err := repo.CredentialForAPIToken(ctx, rotated); err != nil || token.Principal.ID != principal.ID {
		t.Fatalf("publisher token after login restore = %#v, err=%v", token, err)
	}
}
