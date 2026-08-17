package http

import (
	"context"
	"database/sql"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/avatar"
)

type currentUserRepository struct {
	access.Repository
	principal  access.Principal
	management access.PrincipalIdentityManagement
	audit      access.AuditEventInput
	password   struct{ current, next string }
}

func (r *currentUserRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return true, nil
}

func (r *currentUserRepository) CreateAPITokenWithMetadata(_ context.Context, input access.APITokenInput) (string, access.APIToken, error) {
	return "secret", access.APIToken{ID: "token_created", PrincipalID: input.PrincipalID, Name: input.Name, CreatedAt: "2026-08-10T12:00:00Z"}, nil
}

func (r *currentUserRepository) PrincipalByID(_ context.Context, principalID string) (access.Principal, error) {
	if principalID != r.principal.ID {
		return access.Principal{}, sql.ErrNoRows
	}
	return r.principal, nil
}

func (r *currentUserRepository) PrincipalIdentityManagement(_ context.Context, principalID string) (access.PrincipalIdentityManagement, error) {
	if principalID != r.principal.ID {
		return access.PrincipalIdentityManagement{}, sql.ErrNoRows
	}
	return r.management, nil
}

func (r *currentUserRepository) UpsertPrincipal(_ context.Context, input access.PrincipalInput) (access.Principal, error) {
	r.principal.DisplayName = input.DisplayName
	r.principal.UpdatedAt = "2026-08-10T12:05:00Z"
	return r.principal, nil
}

func (r *currentUserRepository) ChangeLocalPassword(_ context.Context, principalID, currentPassword, newPassword string) (access.LocalCredential, error) {
	if principalID != r.principal.ID || currentPassword != "old-secret" {
		return access.LocalCredential{}, sql.ErrNoRows
	}
	r.password.current, r.password.next = currentPassword, newPassword
	return access.LocalCredential{PrincipalID: principalID}, nil
}

func (r *currentUserRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(r)
	r.audit = event
	return err
}

func TestGetCurrentPrincipalReturnsIdentityCapabilitiesAndAvatar(t *testing.T) {
	repository := &currentUserRepository{
		principal: access.Principal{
			ID: "principal_me", Kind: access.PrincipalKindUser, Email: "me@example.com", DisplayName: "Me",
			CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z",
		},
		management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
	}
	digest := strings.Repeat("a", 64)
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: repository.principal.ID, Kind: access.PrincipalKindUser}, true
		},
		LocalPasswordEnabled: true,
		Avatar: &fakeAvatarService{metadata: avatar.Metadata{
			PrincipalID: repository.principal.ID, SHA256: digest, MediaType: "image/png",
			SizeBytes: 123, Width: 256, Height: 256, UpdatedAt: "2026-08-10T12:01:00Z",
		}},
	}
	response := httptest.NewRecorder()
	handler.GetCurrentPrincipal(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me", nil))
	if response.Code != stdhttp.StatusOK || response.Header().Get("ETag") == "" {
		t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var body struct {
		IdentityManagement struct {
			Source   string `json:"source"`
			Provider string `json:"provider"`
		} `json:"identityManagement"`
		Capabilities map[string]bool `json:"capabilities"`
		Avatar       struct {
			SHA256 string `json:"sha256"`
			URL    string `json:"url"`
		} `json:"avatar"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.IdentityManagement.Source != "local" || body.IdentityManagement.Provider != "" {
		t.Fatalf("identity management = %#v", body.IdentityManagement)
	}
	for _, capability := range []string{"canUpdateDisplayName", "canChangePassword", "canManageAvatar", "canManageSessions", "canManageApiTokens"} {
		if !body.Capabilities[capability] {
			t.Fatalf("capability %q = false; body=%s", capability, response.Body.String())
		}
	}
	if body.Capabilities["canManageAuthoringSessions"] {
		t.Fatalf("authoring sessions unexpectedly available; body=%s", response.Body.String())
	}
	if body.Avatar.SHA256 != digest || body.Avatar.URL == "" {
		t.Fatalf("avatar = %#v", body.Avatar)
	}
}

func TestUpdateCurrentPrincipalRequiresSelfManagedIdentityAndCurrentETag(t *testing.T) {
	repository := &currentUserRepository{
		principal: access.Principal{
			ID: "principal_me", Kind: access.PrincipalKindUser, Email: "me@example.com", DisplayName: "Before",
			CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z",
		},
		management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: repository.principal.ID, Kind: access.PrincipalKindUser}, true
		},
		LocalPasswordEnabled: true,
	}
	getResponse := httptest.NewRecorder()
	handler.GetCurrentPrincipal(getResponse, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me", nil))

	request := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/me", strings.NewReader(`{"displayName":"After"}`))
	request.Header.Set("If-Match", getResponse.Header().Get("ETag"))
	response := httptest.NewRecorder()
	handler.UpdateCurrentPrincipal(response, request)
	if response.Code != stdhttp.StatusOK || repository.principal.DisplayName != "After" {
		t.Fatalf("response = %d body=%s principal=%#v", response.Code, response.Body.String(), repository.principal)
	}
	if repository.audit.Action != "principal.profile.updated" || repository.audit.PrincipalID != repository.principal.ID {
		t.Fatalf("audit = %#v", repository.audit)
	}

	repository.management = access.PrincipalIdentityManagement{Source: access.IdentityManagementExternal, Provider: "scim"}
	denied := httptest.NewRecorder()
	deniedRequest := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/me", strings.NewReader(`{"displayName":"Provider owned"}`))
	deniedRequest.Header.Set("If-Match", "*")
	handler.UpdateCurrentPrincipal(denied, deniedRequest)
	if denied.Code != stdhttp.StatusUnprocessableEntity || repository.principal.DisplayName != "After" {
		t.Fatalf("external update = %d body=%s principal=%#v", denied.Code, denied.Body.String(), repository.principal)
	}
}

func TestChangeCurrentPasswordIsAvailableOnlyForLocalCredential(t *testing.T) {
	repository := &currentUserRepository{
		principal:  access.Principal{ID: "principal_me", Kind: access.PrincipalKindUser},
		management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: repository.principal.ID, Kind: access.PrincipalKindUser}, true
		},
		LocalPasswordEnabled: true,
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/me/password", strings.NewReader(`{"currentPassword":"old-secret","newPassword":"new-secret"}`))
	response := httptest.NewRecorder()
	handler.ChangeCurrentPassword(response, request)
	if response.Code != stdhttp.StatusNoContent || repository.password.next != "new-secret" {
		t.Fatalf("response = %d body=%s password=%#v", response.Code, response.Body.String(), repository.password)
	}
	if repository.audit.Action != "password.changed" {
		t.Fatalf("audit = %#v", repository.audit)
	}

	wrong := httptest.NewRecorder()
	handler.ChangeCurrentPassword(wrong, httptest.NewRequest(stdhttp.MethodPost, "/api/v1/me/password", strings.NewReader(`{"currentPassword":"wrong","newPassword":"other"}`)))
	if wrong.Code != stdhttp.StatusUnauthorized || !strings.Contains(wrong.Body.String(), "unauthorized") || strings.Contains(wrong.Body.String(), "no rows") {
		t.Fatalf("wrong-current-password response = %d body=%s", wrong.Code, wrong.Body.String())
	}

	repository.management = access.PrincipalIdentityManagement{Source: access.IdentityManagementExternal, Provider: "azure"}
	denied := httptest.NewRecorder()
	handler.ChangeCurrentPassword(denied, httptest.NewRequest(stdhttp.MethodPost, "/api/v1/me/password", strings.NewReader(`{"currentPassword":"old-secret","newPassword":"other"}`)))
	if denied.Code != stdhttp.StatusUnprocessableEntity {
		t.Fatalf("external password change = %d body=%s", denied.Code, denied.Body.String())
	}
}
