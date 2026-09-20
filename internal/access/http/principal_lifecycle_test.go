package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestPrincipalAdministrationResponsesExposeSourceAwareCapabilities(t *testing.T) {
	store := openAccessHTTPTestStore(t)
	repository := store.repository
	local, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{
		Email: "local-admin@example.test", DisplayName: "Local Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	external, err := repository.ResolveExternalPrincipal(t.Context(), access.ExternalIdentityInput{
		Provider: "oidc", TenantID: "https://issuer.example", Subject: "external-admin",
		Email: "external-admin@example.test", DisplayName: "External Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{Email: "capability-admin@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
	}}

	for _, test := range []struct {
		name      string
		principal access.Principal
		source    string
		provider  string
		canUpdate bool
		canReset  bool
		canDelete bool
	}{
		{name: "local", principal: local.Principal, source: "local", canUpdate: true, canReset: true, canDelete: true},
		{name: "external", principal: external, source: "external", provider: "oidc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.GetPrincipal(recorder, principalRequest(stdhttp.MethodGet, "/api/v1/principals/"+test.principal.ID, test.principal.ID))
			if recorder.Code != stdhttp.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				IdentityManagement struct {
					Source   string `json:"source"`
					Provider string `json:"provider"`
				} `json:"identityManagement"`
				Capabilities struct {
					CanUpdateProfile       bool `json:"canUpdateProfile"`
					CanResetPassword       bool `json:"canResetPassword"`
					CanBlock               bool `json:"canBlock"`
					CanDelete              bool `json:"canDelete"`
					CanManageSessions      bool `json:"canManageSessions"`
					CanManageAuthorization bool `json:"canManageAuthorization"`
				} `json:"capabilities"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.IdentityManagement.Source != test.source || response.IdentityManagement.Provider != test.provider {
				t.Fatalf("identity management=%#v", response.IdentityManagement)
			}
			if response.Capabilities.CanUpdateProfile != test.canUpdate || response.Capabilities.CanResetPassword != test.canReset || response.Capabilities.CanDelete != test.canDelete {
				t.Fatalf("mutation capabilities=%#v", response.Capabilities)
			}
			if !response.Capabilities.CanBlock || !response.Capabilities.CanManageSessions || !response.Capabilities.CanManageAuthorization {
				t.Fatalf("common capabilities=%#v", response.Capabilities)
			}
		})
	}
}

func TestExternalPrincipalProfileAndDeletionAreManagedByProvider(t *testing.T) {
	store := openAccessHTTPTestStore(t)
	repository := store.repository
	external, err := repository.ResolveExternalPrincipal(t.Context(), access.ExternalIdentityInput{
		Provider: "scim", Subject: "managed-user", Email: "managed@example.test", DisplayName: "Managed User",
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{Email: "admin@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
	}}

	updateRequest := principalRequest(stdhttp.MethodPatch, "/api/v1/principals/"+external.ID, external.ID)
	updateRequest.Body = io.NopCloser(bytes.NewBufferString(`{"displayName":"Locally Changed"}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("If-Match", resourceETag(principalDTO(external)))
	updateRecorder := httptest.NewRecorder()
	handler.UpdatePrincipal(updateRecorder, updateRequest)
	if updateRecorder.Code != stdhttp.StatusUnprocessableEntity {
		t.Fatalf("update status=%d body=%s", updateRecorder.Code, updateRecorder.Body.String())
	}

	deleteRecorder := httptest.NewRecorder()
	handler.DeletePrincipal(deleteRecorder, principalRequest(stdhttp.MethodDelete, "/api/v1/principals/"+external.ID, external.ID))
	if deleteRecorder.Code != stdhttp.StatusUnprocessableEntity {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
}

func TestPrincipalLifecycleIsAuditedAndDisableRejectsCredentials(t *testing.T) {
	store := openAccessHTTPTestStore(t)
	repository := store.repository
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: actor.ID, Email: actor.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	targetReset, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{
		Email: "member@example.test", DisplayName: "Member",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := targetReset.Principal
	sessionToken, err := repository.CreateSession(t.Context(), target.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID:  target.ID,
		Name:         "before-disable",
		Capabilities: access.LegacyProjectCapabilities(),
		ExpiresAt:    time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	authoringSessionID := uuid.NewString()
	authoringCredentialID := uuid.NewString()
	if _, err := store.pool.Exec(t.Context(), `
INSERT INTO access.authoring_session (
  id, kind, client_id, principal_id, target_id, project_id, capabilities,
  created_at, expires_at
) VALUES ($1, 'human_cli', 'leapview-cli', $2::uuid, 'lvinst_test', 'test', '[]'::jsonb, $3, $4)`,
		authoringSessionID, target.ID, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `
INSERT INTO access.authoring_credential (
  id, session_id, access_token_hash, refresh_token_hash, access_expires_at,
  refresh_expires_at, active, created_at
) VALUES ($1, $2, $3, $4, $5, $6, true, $7)`,
		authoringCredentialID, authoringSessionID, strings.Repeat("a", 64), strings.Repeat("b", 64),
		now.Add(15*time.Minute), now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: actor.ID, Kind: access.PrincipalKindUser, Email: actor.Email, DisplayName: actor.DisplayName}, true
		},
	}

	disableRequest := principalRequest(stdhttp.MethodPost, "/api/v1/principals/member/disable", target.ID)
	disableContract, ok := accessgen.GetAPIGenCommandRuntimeContract(accessgen.GenCommandOperationDisablePrincipal().APIGenOperationID())
	if !ok {
		t.Fatal("missing generated disable-principal command contract")
	}
	disableContext, disableGuard, err := apigencommand.Begin(disableRequest.Context(), disableContract)
	if err != nil {
		t.Fatal(err)
	}
	disableRecorder := httptest.NewRecorder()
	handler.DisablePrincipal(disableRecorder, disableRequest.WithContext(disableContext))
	if disableRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("disable status=%d body=%s", disableRecorder.Code, disableRecorder.Body.String())
	}
	if !disableGuard.Completed() {
		t.Fatal("principal disable bypassed its generated transactional command")
	}
	var disabled map[string]any
	if err := json.Unmarshal(disableRecorder.Body.Bytes(), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled["blockedAt"] == nil || disableRecorder.Header().Get("ETag") == "" {
		t.Fatalf("disabled principal response=%v headers=%v", disabled, disableRecorder.Header())
	}
	if _, err := repository.PrincipalForToken(t.Context(), sessionToken); err == nil {
		t.Fatal("disabled principal session remained usable")
	}

	enableRecorder := httptest.NewRecorder()
	handler.EnablePrincipal(enableRecorder, principalRequest(stdhttp.MethodPost, "/api/v1/principals/member/enable", target.ID))
	if enableRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("enable status=%d body=%s", enableRecorder.Code, enableRecorder.Body.String())
	}
	var enabled map[string]any
	if err := json.Unmarshal(enableRecorder.Body.Bytes(), &enabled); err != nil {
		t.Fatal(err)
	}
	if enabled["blockedAt"] != nil {
		t.Fatalf("enabled principal retained blockedAt: %v", enabled)
	}
	if _, err := repository.PrincipalForToken(t.Context(), sessionToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("browser session revived after enable: %v", err)
	}
	if _, err := repository.PrincipalForAPIToken(t.Context(), apiToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("API token revived after enable: %v", err)
	}
	if _, err := repository.AuthoringCredentialByAccessTokenHash(t.Context(), strings.Repeat("a", 64), now.Add(time.Minute)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("authoring access credential revived after enable: %v", err)
	}
	var active bool
	var revokedAt sql.NullString
	if err := store.pool.QueryRow(t.Context(), `
SELECT c.active, s.revoked_at
FROM access.authoring_credential c
JOIN access.authoring_session s ON s.id = c.session_id
WHERE c.id = $1`, authoringCredentialID).Scan(&active, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if active || !revokedAt.Valid {
		t.Fatalf("authoring credential/session after enable = active %t, revokedAt %#v", active, revokedAt)
	}

	deleteRecorder := httptest.NewRecorder()
	handler.DeletePrincipal(deleteRecorder, principalRequest(stdhttp.MethodDelete, "/api/v1/principals/member", target.ID))
	if deleteRecorder.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := repository.PrincipalByID(t.Context(), target.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("deleted principal lookup error=%v, want pgx.ErrNoRows", err)
	}

	for _, action := range []string{"principal.blocked", "principal.unblocked", "principal.deleted"} {
		events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{Action: action, ResourceID: target.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].PrincipalID != actor.ID || events[0].Status != "success" {
			t.Fatalf("%s audit events=%#v", action, events)
		}
	}
}

func TestPrincipalLifecycleRejectsSelfDisableAndDelete(t *testing.T) {
	store := openAccessHTTPTestStore(t)
	repository := store.repository
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: actor.ID, Email: actor.Email, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: actor.ID, Kind: access.PrincipalKindUser}, true
		},
	}
	for _, test := range []struct {
		name string
		call func(stdhttp.ResponseWriter, *stdhttp.Request)
		path string
	}{
		{name: "disable", call: handler.DisablePrincipal, path: "/api/v1/principals/admin/disable"},
		{name: "delete", call: handler.DeletePrincipal, path: "/api/v1/principals/admin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			test.call(recorder, principalRequest(stdhttp.MethodPost, test.path, actor.ID))
			if recorder.Code != stdhttp.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func principalRequest(method, path, principalID string) *stdhttp.Request {
	request := httptest.NewRequest(method, path, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("principal", principalID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
