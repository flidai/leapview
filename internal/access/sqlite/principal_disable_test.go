package sqlite

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

func TestDisableThenEnableDoesNotRevivePrincipalCredentials(t *testing.T) {
	_, repository := openAccessRepo(t, t.Context())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "disable_credentials", Kind: access.PrincipalKindUser,
		Email: "disable-credentials@example.test", DisplayName: "Disable Credentials",
	})
	if err != nil {
		t.Fatal(err)
	}
	browserSecret, err := repository.CreateSession(t.Context(), principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: principal.ID, Name: "before-disable",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	authoringSession := access.AuthoringSession{
		ID: "authoring_before_disable", Kind: access.AuthoringSessionHumanCLI,
		ClientID: access.AuthoringCLIClientID, PrincipalID: principal.ID,
		Scope: access.AuthoringScope{
			TargetID: "lvinst_test", ProjectID: "test",
			Privileges: []access.Privilege{access.PrivilegeAuthorProject},
		},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := createAuthoringSession(t.Context(), repository.q, authoringSession); err != nil {
		t.Fatal(err)
	}
	if err := createAuthoringCredential(
		t.Context(), repository.q, "credential_before_disable", authoringSession.ID,
		"access_hash_before_disable", "refresh_hash_before_disable",
		now.Add(15*time.Minute), now.Add(time.Hour), now,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.PrincipalForToken(t.Context(), browserSecret); err != nil {
		t.Fatalf("browser credential was not initially usable: %v", err)
	}
	if _, err := repository.PrincipalForAPIToken(t.Context(), apiSecret); err != nil {
		t.Fatalf("API credential was not initially usable: %v", err)
	}
	if _, err := repository.AuthoringCredentialByAccessTokenHash(t.Context(), "access_hash_before_disable", now); err != nil {
		t.Fatalf("authoring credential was not initially usable: %v", err)
	}

	if _, err := repository.DisablePrincipal(t.Context(), principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.EnablePrincipal(t.Context(), principal.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.PrincipalForToken(t.Context(), browserSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("browser credential after re-enable error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repository.PrincipalForAPIToken(t.Context(), apiSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("API credential after re-enable error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repository.AuthoringCredentialByAccessTokenHash(t.Context(), "access_hash_before_disable", now.Add(time.Minute)); !errors.Is(err, access.ErrInvalidAuthoringCredential) {
		t.Fatalf("authoring access credential after re-enable error = %v, want ErrInvalidAuthoringCredential", err)
	}
	if _, err := repository.RotateAuthoringCredential(t.Context(), access.AuthoringCredentialRotation{
		RefreshTokenHash: "refresh_hash_before_disable", Now: now.Add(time.Minute),
		CredentialID: "credential_after_disable", AccessTokenHash: "access_hash_after_disable",
		RefreshTokenHashNew: "refresh_hash_after_disable", AccessExpiresAt: now.Add(20 * time.Minute),
		RefreshExpiresAt: now.Add(2 * time.Hour),
	}); err == nil {
		t.Fatal("authoring refresh credential became usable after re-enable")
	}
}

func TestLocalPrincipalBlockSurvivesSCIMReactivation(t *testing.T) {
	store, repository := openAccessRepo(t, t.Context())
	user, err := repository.UpsertSCIMUser(t.Context(), access.SCIMUserInput{
		ExternalID: "directory-user", UserName: "directory@example.test", DisplayName: "Directory User", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DisablePrincipal(t.Context(), user.Principal.ID); err != nil {
		t.Fatal(err)
	}
	var disabledAt, blockedAt sql.NullString
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT disabled_at, blocked_at FROM principals WHERE id = ?`, user.Principal.ID).Scan(&disabledAt, &blockedAt); err != nil {
		t.Fatal(err)
	}
	if disabledAt.Valid || !blockedAt.Valid {
		t.Fatalf("after local block disabledAt=%#v blockedAt=%#v", disabledAt, blockedAt)
	}

	if _, err := repository.UpsertSCIMUser(t.Context(), access.SCIMUserInput{
		ID: user.Principal.ID, ExternalID: "directory-user", UserName: "directory@example.test", DisplayName: "Directory User", Active: false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertSCIMUser(t.Context(), access.SCIMUserInput{
		ID: user.Principal.ID, ExternalID: "directory-user", UserName: "directory@example.test", DisplayName: "Directory User", Active: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(t.Context(), `SELECT disabled_at, blocked_at FROM principals WHERE id = ?`, user.Principal.ID).Scan(&disabledAt, &blockedAt); err != nil {
		t.Fatal(err)
	}
	if disabledAt.Valid || !blockedAt.Valid {
		t.Fatalf("after SCIM reactivation disabledAt=%#v blockedAt=%#v", disabledAt, blockedAt)
	}

	token, err := repository.CreateSession(t.Context(), user.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("locally blocked SCIM principal authenticated: %v", err)
	}
	if _, err := repository.EnablePrincipal(t.Context(), user.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), token); err != nil {
		t.Fatalf("unblocked active SCIM principal did not authenticate: %v", err)
	}
}

func TestDisablePrincipalRollsBackStatusAndRevocationsTogether(t *testing.T) {
	store, repository := openAccessRepo(t, t.Context())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "disable_rollback", Kind: access.PrincipalKindUser,
		Email: "disable-rollback@example.test", DisplayName: "Disable Rollback",
	})
	if err != nil {
		t.Fatal(err)
	}
	browserSecret, err := repository.CreateSession(t.Context(), principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: principal.ID, Name: "rollback",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	authoringSession := access.AuthoringSession{
		ID: "authoring_disable_rollback", Kind: access.AuthoringSessionHumanCLI,
		ClientID: access.AuthoringCLIClientID, PrincipalID: principal.ID,
		Scope: access.AuthoringScope{
			TargetID: "lvinst_test", ProjectID: "test",
			Privileges: []access.Privilege{access.PrivilegeAuthorProject},
		},
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := createAuthoringSession(t.Context(), repository.q, authoringSession); err != nil {
		t.Fatal(err)
	}
	if err := createAuthoringCredential(
		t.Context(), repository.q, "credential_disable_rollback", authoringSession.ID,
		"access_hash_disable_rollback", "refresh_hash_disable_rollback",
		now.Add(15*time.Minute), now.Add(time.Hour), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
CREATE TRIGGER fail_authoring_credential_deactivation
BEFORE UPDATE OF active ON oauth_authoring_credentials
WHEN NEW.active = 0
BEGIN
  SELECT RAISE(ABORT, 'forced authoring deactivation failure');
END`); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.DisablePrincipal(t.Context(), principal.ID); err == nil {
		t.Fatal("DisablePrincipal succeeded despite forced credential revocation failure")
	}
	stored, err := repository.PrincipalByID(t.Context(), principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BlockedAt != "" {
		t.Fatalf("principal status was not rolled back: %#v", stored)
	}
	if _, err := repository.PrincipalForToken(t.Context(), browserSecret); err != nil {
		t.Fatalf("browser credential revocation was not rolled back: %v", err)
	}
	if _, err := repository.PrincipalForAPIToken(t.Context(), apiSecret); err != nil {
		t.Fatalf("API credential revocation was not rolled back: %v", err)
	}
	if _, err := repository.AuthoringCredentialByAccessTokenHash(t.Context(), "access_hash_disable_rollback", now.Add(time.Minute)); err != nil {
		t.Fatalf("authoring credential changed despite rollback: %v", err)
	}
}
