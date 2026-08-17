package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
)

type platformAdminTestRepository struct {
	access.Repository
	admin bool
	err   error
}

func (r platformAdminTestRepository) ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error) {
	return []access.Principal{}, nil
}

func (r platformAdminTestRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, r.err
}

func TestPlatformAdminGuard(t *testing.T) {
	request := func() *stdhttp.Request { return httptest.NewRequest(stdhttp.MethodGet, "/api/v1/principals", nil) }
	tests := []struct {
		name          string
		principal     Principal
		authenticated bool
		repository    platformAdminTestRepository
		want          int
	}{
		{name: "unauthenticated", repository: platformAdminTestRepository{}, want: stdhttp.StatusUnauthorized},
		{name: "non admin", authenticated: true, principal: Principal{ID: "email_user", Kind: access.PrincipalKindUser}, repository: platformAdminTestRepository{}, want: stdhttp.StatusForbidden},
		{name: "repository failure", authenticated: true, principal: Principal{ID: "email_user", Kind: access.PrincipalKindUser}, repository: platformAdminTestRepository{err: context.DeadlineExceeded}, want: stdhttp.StatusInternalServerError},
		{name: "admin", authenticated: true, principal: Principal{ID: "email_admin", Kind: access.PrincipalKindUser}, repository: platformAdminTestRepository{admin: true}, want: stdhttp.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := Handler{
				Repository: func() (access.Repository, error) { return test.repository, nil },
				CurrentEffectiveCapabilities: func(ctx context.Context, principalID string) ([]access.Capability, error) {
					if test.repository.err != nil {
						return nil, test.repository.err
					}
					if test.repository.admin {
						return []access.Capability{access.CapabilityProjectAdmin}, nil
					}
					return nil, nil
				},
				CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
					return test.principal, test.authenticated
				},
			}
			response := httptest.NewRecorder()
			handler.ListPrincipals(response, request())
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.want, response.Body.String())
			}
		})
	}
}

func TestPlatformAdminGuardDurableRoleCredentialAttenuation(t *testing.T) {
	credential := access.APICredential{}
	handler := Handler{
		Repository: func() (access.Repository, error) { return platformAdminTestRepository{}, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "admin", Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return credential, credential.Authoring != nil || credential.Token.ID != ""
		},
	}
	call := func() int {
		response := httptest.NewRecorder()
		handler.ListPrincipals(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/principals", nil))
		return response.Code
	}
	if got := call(); got != stdhttp.StatusOK {
		t.Fatalf("session status = %d, want %d", got, stdhttp.StatusOK)
	}
	credential = access.APICredential{Authoring: &access.AuthoringSession{}}
	if got := call(); got != stdhttp.StatusForbidden {
		t.Fatalf("authoring status = %d, want %d", got, stdhttp.StatusForbidden)
	}
	credential = access.APICredential{Token: access.APIToken{ID: "empty", Capabilities: []access.Capability{}}}
	if got := call(); got != stdhttp.StatusForbidden {
		t.Fatalf("empty token status = %d, want %d", got, stdhttp.StatusForbidden)
	}
	credential = access.APICredential{Token: access.APIToken{ID: "narrow", Capabilities: []access.Capability{access.CapabilityResourceRead}}}
	if got := call(); got != stdhttp.StatusForbidden {
		t.Fatalf("narrow token status = %d, want %d", got, stdhttp.StatusForbidden)
	}
	credential = access.APICredential{Token: access.APIToken{ID: "admin", Capabilities: []access.Capability{access.CapabilityProjectAdmin}}}
	if got := call(); got != stdhttp.StatusOK {
		t.Fatalf("project admin token status = %d, want %d", got, stdhttp.StatusOK)
	}
}
