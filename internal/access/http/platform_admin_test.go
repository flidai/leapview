package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
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

type platformAdminMutationRepository struct {
	access.Repository
	state      access.PlatformAdministratorState
	principals map[string]access.Principal
	nextID     int
}

func (r *platformAdminMutationRepository) ListPlatformAdministrators(context.Context) (access.PlatformAdministratorState, error) {
	state := r.state
	state.Administrators = append([]access.PlatformAdministrator(nil), r.state.Administrators...)
	return state, nil
}

func (r *platformAdminMutationRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	_, err := mutation(r)
	return err
}

func (r *platformAdminMutationRepository) GrantPlatformAdmin(_ context.Context, input access.PlatformAdminGrantInput) (access.PlatformAdminGrantResult, error) {
	if strings.Trim(input.ExpectedRevision, `"`) != r.state.Revision {
		return access.PlatformAdminGrantResult{}, access.ErrPlatformAdminStaleRevision
	}
	principal, ok := r.principals[input.PrincipalID]
	if !ok {
		return access.PlatformAdminGrantResult{}, access.ErrPlatformAdminNotFound
	}
	for _, item := range r.state.Administrators {
		if item.Principal.ID == principal.ID {
			return access.PlatformAdminGrantResult{Administrator: item, State: r.state}, nil
		}
	}
	r.nextID++
	item := access.PlatformAdministrator{
		BindingID: fmt.Sprintf("binding-%d", r.nextID), Principal: principal,
		Role: access.PlatformRoleAdmin, CreatedAt: "2026-09-16T00:00:00Z",
	}
	r.state.Administrators = append(r.state.Administrators, item)
	if err := r.recomputeRevision(); err != nil {
		return access.PlatformAdminGrantResult{}, err
	}
	return access.PlatformAdminGrantResult{Administrator: item, State: r.state}, nil
}

func (r *platformAdminMutationRepository) RevokePlatformAdmin(_ context.Context, input access.PlatformAdminRevokeInput) (access.PlatformAdministratorState, error) {
	if strings.Trim(input.ExpectedRevision, `"`) != r.state.Revision {
		return access.PlatformAdministratorState{}, access.ErrPlatformAdminStaleRevision
	}
	index := -1
	for i, item := range r.state.Administrators {
		if item.Principal.ID == input.PrincipalID {
			index = i
			break
		}
	}
	if index < 0 {
		return access.PlatformAdministratorState{}, access.ErrPlatformAdminNotFound
	}
	if len(r.state.Administrators) == 1 {
		return access.PlatformAdministratorState{}, access.ErrPlatformAdminLastAdmin
	}
	r.state.Administrators = append(r.state.Administrators[:index], r.state.Administrators[index+1:]...)
	if err := r.recomputeRevision(); err != nil {
		return access.PlatformAdministratorState{}, err
	}
	return r.state, nil
}

func (r *platformAdminMutationRepository) recomputeRevision() error {
	revision, err := access.PlatformAdministratorRevision(r.state.Administrators)
	if err != nil {
		return err
	}
	r.state.Revision = revision
	return nil
}

type platformAdminGeneratedInvocation struct {
	request *stdhttp.Request
	guard   *apigencommand.Guard
}

func beginPlatformAdminGeneratedInvocation(t *testing.T, method, path, operation, principal, idempotencyKey, ifMatch string) platformAdminGeneratedInvocation {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("If-Match", ifMatch)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("principal", principal)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		t.Fatalf("generated command contract %q is unavailable", operation)
	}
	ctx, guard, err := apigencommand.BeginInvocation(request.Context(), contract, apigencommand.Invocation{
		Surface: apigencommand.SurfaceAPI, TargetValues: map[string]string{"principal": principal},
		IdempotencyKey: idempotencyKey, ConcurrencyToken: ifMatch,
	})
	if err != nil {
		t.Fatalf("begin generated invocation %q: %v", operation, err)
	}
	return platformAdminGeneratedInvocation{request: request.WithContext(ctx), guard: guard}
}

func TestPlatformAdministratorMutationsExecuteGeneratedContracts(t *testing.T) {
	admin := access.Principal{ID: "admin", Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Admin"}
	first := access.Principal{ID: "first", Kind: access.PrincipalKindUser, Email: "first@example.test", DisplayName: "First"}
	second := access.Principal{ID: "second", Kind: access.PrincipalKindUser, Email: "second@example.test", DisplayName: "Second"}
	repository := &platformAdminMutationRepository{
		principals: map[string]access.Principal{admin.ID: admin, first.ID: first, second.ID: second},
		state: access.PlatformAdministratorState{Administrators: []access.PlatformAdministrator{{
			BindingID: "binding-admin", Principal: admin, Role: access.PlatformRoleAdmin, CreatedAt: "2026-09-15T00:00:00Z",
		}}},
	}
	if err := repository.recomputeRevision(); err != nil {
		t.Fatal(err)
	}
	initialRevision := repository.state.Revision
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
	}

	grantInvocation := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodPut, "/api/v1/platform-administrators/first", "grantPlatformAdministrator", first.ID, "grant-first", strconv.Quote(repository.state.Revision))
	grantResponse := httptest.NewRecorder()
	handler.GrantPlatformAdministrator(grantResponse, grantInvocation.request)
	if grantResponse.Code != stdhttp.StatusOK || !grantInvocation.guard.Completed() {
		t.Fatalf("grant status=%d completed=%v body=%s", grantResponse.Code, grantInvocation.guard.Completed(), grantResponse.Body.String())
	}

	staleInvocation := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodPut, "/api/v1/platform-administrators/second", "grantPlatformAdministrator", second.ID, "grant-second-stale", strconv.Quote(initialRevision))
	staleResponse := httptest.NewRecorder()
	handler.GrantPlatformAdministrator(staleResponse, staleInvocation.request)
	if staleResponse.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("stale grant status=%d body=%s, want 412", staleResponse.Code, staleResponse.Body.String())
	}
	if len(repository.state.Administrators) != 2 {
		t.Fatalf("stale grant mutated administrators: %#v", repository.state.Administrators)
	}

	grantSecond := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodPut, "/api/v1/platform-administrators/second", "grantPlatformAdministrator", second.ID, "grant-second", strconv.Quote(repository.state.Revision))
	grantSecondResponse := httptest.NewRecorder()
	handler.GrantPlatformAdministrator(grantSecondResponse, grantSecond.request)
	if grantSecondResponse.Code != stdhttp.StatusOK || !grantSecond.guard.Completed() {
		t.Fatalf("second grant status=%d completed=%v body=%s", grantSecondResponse.Code, grantSecond.guard.Completed(), grantSecondResponse.Body.String())
	}

	revokeFirst := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodDelete, "/api/v1/platform-administrators/first", "revokePlatformAdministrator", first.ID, "revoke-first", strconv.Quote(repository.state.Revision))
	revokeFirstResponse := httptest.NewRecorder()
	handler.RevokePlatformAdministrator(revokeFirstResponse, revokeFirst.request)
	if revokeFirstResponse.Code != stdhttp.StatusNoContent || !revokeFirst.guard.Completed() {
		t.Fatalf("revoke status=%d completed=%v body=%s", revokeFirstResponse.Code, revokeFirst.guard.Completed(), revokeFirstResponse.Body.String())
	}

	revokeSecond := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodDelete, "/api/v1/platform-administrators/second", "revokePlatformAdministrator", second.ID, "revoke-second", strconv.Quote(repository.state.Revision))
	revokeSecondResponse := httptest.NewRecorder()
	handler.RevokePlatformAdministrator(revokeSecondResponse, revokeSecond.request)
	if revokeSecondResponse.Code != stdhttp.StatusNoContent || !revokeSecond.guard.Completed() {
		t.Fatalf("second revoke status=%d completed=%v body=%s", revokeSecondResponse.Code, revokeSecond.guard.Completed(), revokeSecondResponse.Body.String())
	}

	lastInvocation := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodDelete, "/api/v1/platform-administrators/admin", "revokePlatformAdministrator", admin.ID, "revoke-last", strconv.Quote(repository.state.Revision))
	lastResponse := httptest.NewRecorder()
	handler.RevokePlatformAdministrator(lastResponse, lastInvocation.request)
	if lastResponse.Code != stdhttp.StatusConflict {
		t.Fatalf("last-admin revoke status=%d body=%s, want 409", lastResponse.Code, lastResponse.Body.String())
	}
	if len(repository.state.Administrators) != 1 || repository.state.Administrators[0].Principal.ID != admin.ID {
		t.Fatalf("last-admin revoke changed administrators: %#v", repository.state.Administrators)
	}
}

func TestPlatformAdministratorBrowserMutationRequiresRecentInteractiveAuthentication(t *testing.T) {
	admin := access.Principal{ID: "admin", Kind: access.PrincipalKindUser, Email: "admin@example.test"}
	target := access.Principal{ID: "target", Kind: access.PrincipalKindUser, Email: "target@example.test"}
	repository := &platformAdminMutationRepository{
		principals: map[string]access.Principal{admin.ID: admin, target.ID: target},
		state:      access.PlatformAdministratorState{Administrators: []access.PlatformAdministrator{{BindingID: "binding-admin", Principal: admin, Role: access.PlatformRoleAdmin, CreatedAt: "2026-09-15T00:00:00Z"}}},
	}
	if err := repository.recomputeRevision(); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
		InteractiveAuthentication: func(*stdhttp.Request) (time.Time, bool) {
			return time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC), true
		},
		Now: func() time.Time { return time.Date(2026, 9, 18, 0, 10, 0, 0, time.UTC) },
	}
	invocation := beginPlatformAdminGeneratedInvocation(t, stdhttp.MethodPut, "/api/v1/platform-administrators/target", "grantPlatformAdministrator", target.ID, "browser-stale", strconv.Quote(repository.state.Revision))
	response := httptest.NewRecorder()
	handler.GrantPlatformAdministrator(response, invocation.request)
	if response.Code != stdhttp.StatusUnauthorized || !strings.Contains(response.Body.String(), "RECENT_AUTHENTICATION_REQUIRED") {
		t.Fatalf("status=%d body=%s, want typed recent-authentication failure", response.Code, response.Body.String())
	}
	if len(repository.state.Administrators) != 1 {
		t.Fatalf("stale browser mutation changed administrators: %#v", repository.state.Administrators)
	}
}
