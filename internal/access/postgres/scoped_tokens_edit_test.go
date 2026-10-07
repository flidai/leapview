package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/jackc/pgx/v5"
)

func TestScopedTokenEditAndRotationPreserveAuthorityBoundaries(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "token-editor@example.com", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "other-token-editor@example.com", Password: "correct horse battery staple"})
	if err != nil {
		t.Fatal(err)
	}
	oldSecret, original, err := repo.CreateScopedAPITokenWithMetadata(t.Context(), access.ScopedAPITokenInput{
		PrincipalID: owner.Principal.ID, Name: "before", Permissions: []access.PermissionPair{}, ExpiresAt: time.Now().UTC().Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	modified, err := time.Parse(time.RFC3339Nano, original.ModifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	update := access.ScopedAPITokenUpdate{PrincipalID: owner.Principal.ID, TokenID: original.ID, Name: "after", Description: "automation", Permissions: []access.PermissionPair{}, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), ExpectedModifiedAt: modified}
	update.PrincipalID = other.Principal.ID
	if _, err := repo.UpdateScopedAPITokenForPrincipal(t.Context(), update); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("other owner edit = %v", err)
	}
	update.PrincipalID = owner.Principal.ID
	edited, err := repo.UpdateScopedAPITokenForPrincipal(t.Context(), update)
	if err != nil {
		t.Fatal(err)
	}
	if edited.ID != original.ID || edited.Name != "after" || edited.TokenFingerprint != original.TokenFingerprint || edited.ModifiedAt == original.ModifiedAt {
		t.Fatalf("edited token = %#v", edited)
	}
	if _, err := repo.UpdateScopedAPITokenForPrincipal(t.Context(), update); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale edit = %v", err)
	}
	if credential, err := repo.CredentialForAPIToken(t.Context(), oldSecret); err != nil || credential.Token.ID != original.ID {
		t.Fatalf("edited bearer = %#v, %v", credential, err)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.api_token SET token_fingerprint = decode('01', 'hex') WHERE id = $1::uuid`, original.ID); err == nil {
		t.Fatal("bearer fingerprint rewrite bypassed immutable identity trigger")
	}
	editedModified, err := time.Parse(time.RFC3339Nano, edited.ModifiedAt)
	if err != nil {
		t.Fatal(err)
	}
	rotation := access.ScopedAPITokenRotation{PrincipalID: owner.Principal.ID, TokenID: original.ID, ExpectedModifiedAt: editedModified}
	rotation.PrincipalID = other.Principal.ID
	if _, _, err := repo.RotateScopedAPITokenForPrincipal(t.Context(), rotation); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("other owner rotation = %v", err)
	}
	rotation.PrincipalID = owner.Principal.ID
	newSecret, replacement, err := repo.RotateScopedAPITokenForPrincipal(t.Context(), rotation)
	if err != nil {
		t.Fatal(err)
	}
	if newSecret == oldSecret || replacement.ID == original.ID || replacement.Name != edited.Name || replacement.ExpiresAt != edited.ExpiresAt {
		t.Fatalf("replacement = %#v", replacement)
	}
	if _, err := repo.CredentialForAPIToken(t.Context(), oldSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old bearer after rotation = %v", err)
	}
	if credential, err := repo.CredentialForAPIToken(t.Context(), newSecret); err != nil || credential.Token.ID != replacement.ID {
		t.Fatalf("new bearer = %#v, %v", credential, err)
	}
}
