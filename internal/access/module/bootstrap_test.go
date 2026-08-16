package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type bootstrapCredentialRepository struct {
	access.Repository
	token access.APIToken
	err   error
}

func TestAuthorizeBootstrapRequestAllowsOnlyConfiguredLocalDevelopmentBearer(t *testing.T) {
	module := browserGuardModule(nil, Principal{ID: "dev", DevBypass: true}, true)
	module.auth = NewAuth(nil, AuthConfig{DevBypass: true, DevAPIToken: "local-secret"})
	serve := func(token string) (bool, error) {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/project_demo/connections/connection_demo/upload-sessions", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		var allowed bool
		var authorizeErr error
		module.Authenticate(http.HandlerFunc(func(_ http.ResponseWriter, authenticated *http.Request) {
			allowed, authorizeErr = module.AuthorizeBootstrapRequest(authenticated.Context(), authenticated, access.CapabilityResourceEdit)
		})).ServeHTTP(httptest.NewRecorder(), request)
		return allowed, authorizeErr
	}

	allowed, err := serve("local-secret")
	if err != nil || !allowed {
		t.Fatalf("configured local bearer authorization = %t, %v; want true, nil", allowed, err)
	}
	allowed, err = serve("wrong-secret")
	if err != nil || allowed {
		t.Fatalf("wrong local bearer authorization = %t, %v; want false, nil", allowed, err)
	}
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
