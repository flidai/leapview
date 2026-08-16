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

func (r platformAdminTestRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return r.admin, r.err
}

func (r platformAdminTestRepository) ListPrincipals(context.Context, access.PrincipalFilter) ([]access.Principal, error) {
	return []access.Principal{}, nil
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
					admin, err := test.repository.IsPlatformAdmin(ctx, principalID)
					if err != nil {
						return nil, err
					}
					if admin {
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
