package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCredentialAuthorityTransactionLocksCurrentRoleAndCredential(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "credential-authority@example.test", DisplayName: "Credential authority"})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewInstancePermissionPair(access.ActionPlatformSettingsUpdate, "instance_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{PrincipalID: principal.ID, Name: "credential authority", Permissions: []access.PermissionPair{pair}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, err := repo.CreateSession(t.Context(), principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	session, err := repo.CredentialForSessionToken(t.Context(), sessionToken)
	if err != nil {
		t.Fatal(err)
	}
	apiIssuer := access.GrantIssuerEvidence{PrincipalID: principal.ID, Credential: access.GrantCredentialEvidence{Class: "api_token", ID: token.ID, Fingerprint: token.TokenFingerprint}}
	sessionIssuer := access.GrantIssuerEvidence{PrincipalID: principal.ID, Credential: access.GrantCredentialEvidence{Class: "session", ID: session.ID, Fingerprint: session.TokenFingerprint}}
	begin := func() pgx.Tx {
		tx, e := db.runtime.Begin(t.Context())
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return tx
	}
	beforeRole := begin()
	if err = RecheckCredentialAuthorityTx(t.Context(), beforeRole, apiIssuer, []access.PermissionPair{pair}); err != nil {
		t.Fatal(err)
	}
	if err = LockPlatformAdministratorTx(t.Context(), beforeRole, principal.ID); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("token substituted for durable role: %v", err)
	}
	_ = beforeRole.Rollback(t.Context())
	roleID := uuid.NewString()
	if _, err = db.admin.Exec(t.Context(), `INSERT INTO access.platform_role_binding(id,principal_id,role) VALUES($1::uuid,$2::uuid,'platform_admin')`, roleID, principal.ID); err != nil {
		t.Fatal(err)
	}
	held := begin()
	for _, issuer := range []access.GrantIssuerEvidence{apiIssuer, sessionIssuer} {
		if err = RecheckCredentialAuthorityTx(t.Context(), held, issuer, []access.PermissionPair{pair}); err != nil {
			t.Fatalf("current %s: %v", issuer.Credential.Class, err)
		}
	}
	if err = LockPlatformAdministratorTx(t.Context(), held, principal.ID); err != nil {
		t.Fatal(err)
	}
	other := pair
	other.Target.InstanceID = "instance_ffffffffffffffffffffffffffffffff"
	if err = RecheckCredentialAuthorityTx(t.Context(), held, apiIssuer, []access.PermissionPair{other}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("live token ceiling widened: %v", err)
	}
	wrong := apiIssuer
	wrong.Credential.Fingerprint = strings.Repeat("0", 64)
	if err = RecheckCredentialAuthorityTx(t.Context(), held, wrong, []access.PermissionPair{pair}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("credential fingerprint substitution accepted: %v", err)
	}
	for _, mutation := range []struct{ name, sql, id string }{
		{"principal", `UPDATE access.principal SET blocked_at=clock_timestamp() WHERE id=$1::uuid`, principal.ID},
		{"platform role", `UPDATE access.platform_role_binding SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, roleID},
		{"API token", `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, token.ID},
		{"session", `UPDATE access.session SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, session.ID},
	} {
		t.Run(mutation.name+" revocation waits for mutation transaction", func(t *testing.T) {
			revocation, e := db.admin.Begin(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer revocation.Rollback(context.Background())
			if _, e = revocation.Exec(t.Context(), `SET LOCAL lock_timeout='100ms'`); e != nil {
				t.Fatal(e)
			}
			_, e = revocation.Exec(t.Context(), mutation.sql, mutation.id)
			var databaseError *pgconn.PgError
			if !errors.As(e, &databaseError) || databaseError.Code != "55P03" {
				t.Fatalf("revocation crossed transaction-held authority: %v", e)
			}
		})
	}
	if err = held.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.admin.Exec(t.Context(), `UPDATE access.api_token SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, token.ID); err != nil {
		t.Fatal(err)
	}
	revoked := begin()
	if err = RecheckCredentialAuthorityTx(t.Context(), revoked, apiIssuer, []access.PermissionPair{pair}); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("stale authenticated token evidence survived revocation: %v", err)
	}
	_ = revoked.Rollback(t.Context())
	if _, err = db.admin.Exec(t.Context(), `UPDATE access.platform_role_binding SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, roleID); err != nil {
		t.Fatal(err)
	}
	noRole := begin()
	if err = RecheckCredentialAuthorityTx(t.Context(), noRole, sessionIssuer, []access.PermissionPair{pair}); err != nil {
		t.Fatal(err)
	}
	if err = LockPlatformAdministratorTx(t.Context(), noRole, principal.ID); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("stale administrator snapshot survived revocation: %v", err)
	}
}

func TestCredentialAuthorityTransactionRejectsInvalidBoundaryBeforeQueries(t *testing.T) {
	if err := RecheckCredentialAuthorityTx(t.Context(), nil, access.GrantIssuerEvidence{}, nil); !errors.Is(err, access.ErrForbidden) {
		t.Fatal(err)
	}
	if err := LockPlatformAdministratorTx(t.Context(), nil, "actor"); !errors.Is(err, access.ErrForbidden) {
		t.Fatal(err)
	}
}
