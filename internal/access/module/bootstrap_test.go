package module

import (
	"context"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type bootstrapCredentialRepository struct {
	access.Repository
	token access.APIToken
	err   error
}

func (r bootstrapCredentialRepository) BootstrapAPITokenEvidence(context.Context, string, string, time.Time) (access.APIToken, error) {
	return r.token, r.err
}

func TestAuthorizeBootstrapCredentialRequiresExactExpiry(t *testing.T) {
	expiresAt := time.Date(2026, 8, 16, 12, 34, 56, 123456789, time.UTC)
	repository := bootstrapCredentialRepository{token: access.APIToken{
		ID: "credential_1", PrincipalID: "principal_1", ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}}
	module, err := newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return repository, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.AuthorizeBootstrapCredential(t.Context(), "principal_1", "credential_1", expiresAt, expiresAt.Add(-time.Minute)); err != nil {
		t.Fatalf("exact expiry authorization = %v, want success", err)
	}
	if err := module.AuthorizeBootstrapCredential(t.Context(), "principal_1", "credential_1", expiresAt.Add(time.Nanosecond), expiresAt.Add(-time.Minute)); err == nil {
		t.Fatal("mismatched expiry authorization succeeded")
	}
	otherID := bootstrapCredentialRepository{token: access.APIToken{
		ID: "credential_other", PrincipalID: "principal_1", ExpiresAt: expiresAt.Format(time.RFC3339Nano),
	}}
	module, err = newSurface(surfaceConfig{Repository: func() (access.Repository, error) { return otherID, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.AuthorizeBootstrapCredential(t.Context(), "principal_1", "credential_1", expiresAt, expiresAt.Add(-time.Minute)); err == nil {
		t.Fatal("mismatched credential ID authorization succeeded")
	}
}
