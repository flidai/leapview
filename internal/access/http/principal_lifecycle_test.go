package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/go-chi/chi/v5"
)

func TestPrincipalLifecycleIsAuditedAndDisableRejectsCredentials(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "admin", Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "member", Kind: access.PrincipalKindUser, Email: "member@example.test", DisplayName: "Member",
	})
	if err != nil {
		t.Fatal(err)
	}
	sessionToken, err := repository.CreateSession(t.Context(), target.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, _, err := repository.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{
		PrincipalID: target.ID, Name: "before-disable",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO oauth_authoring_sessions (
  id, kind, client_id, principal_id, target_id, project_id, privileges_json,
  created_at, expires_at
) VALUES (?, 'human_cli', 'leapview-cli', ?, 'lvinst_test', 'test', '[]', ?, ?)`,
		"authoring_before_disable", target.ID, now.Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(t.Context(), `
INSERT INTO oauth_authoring_credentials (
  id, session_id, access_token_hash, refresh_token_hash, access_expires_at,
  refresh_expires_at, active, created_at
) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		"credential_before_disable", "authoring_before_disable", "access_hash_before_disable", "refresh_hash_before_disable",
		now.Add(15*time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: actor.ID, Kind: actor.Kind, Email: actor.Email, DisplayName: actor.DisplayName}, true
		},
	}

	disableRecorder := httptest.NewRecorder()
	handler.DisablePrincipal(disableRecorder, principalRequest(stdhttp.MethodPost, "/api/v1/principals/member/disable", target.ID))
	if disableRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("disable status=%d body=%s", disableRecorder.Code, disableRecorder.Body.String())
	}
	var disabled map[string]any
	if err := json.Unmarshal(disableRecorder.Body.Bytes(), &disabled); err != nil {
		t.Fatal(err)
	}
	if disabled["disabledAt"] == nil || disableRecorder.Header().Get("ETag") == "" {
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
	if _, exists := enabled["disabledAt"]; exists {
		t.Fatalf("enabled principal retained disabledAt: %v", enabled)
	}
	if _, err := repository.PrincipalForToken(t.Context(), sessionToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("browser session revived after enable: %v", err)
	}
	if _, err := repository.PrincipalForAPIToken(t.Context(), apiToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("API token revived after enable: %v", err)
	}
	if _, err := repository.AuthoringCredentialByAccessTokenHash(t.Context(), "access_hash_before_disable", now.Add(time.Minute)); !errors.Is(err, access.ErrInvalidAuthoringCredential) {
		t.Fatalf("authoring access credential revived after enable: %v", err)
	}
	var active int
	var revokedAt sql.NullString
	if err := store.SQLDB().QueryRowContext(t.Context(), `
SELECT c.active, s.revoked_at
FROM oauth_authoring_credentials c
JOIN oauth_authoring_sessions s ON s.id = c.session_id
WHERE c.id = ?`, "credential_before_disable").Scan(&active, &revokedAt); err != nil {
		t.Fatal(err)
	}
	if active != 0 || !revokedAt.Valid {
		t.Fatalf("authoring credential/session after enable = active %d, revokedAt %#v", active, revokedAt)
	}

	deleteRecorder := httptest.NewRecorder()
	handler.DeletePrincipal(deleteRecorder, principalRequest(stdhttp.MethodDelete, "/api/v1/principals/member", target.ID))
	if deleteRecorder.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, err := repository.PrincipalByID(t.Context(), target.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted principal lookup error=%v, want sql.ErrNoRows", err)
	}

	for _, action := range []string{"principal.disabled", "principal.enabled", "principal.deleted"} {
		events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{Action: action, TargetID: target.ID})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || events[0].PrincipalID != actor.ID || events[0].Status != "success" {
			t.Fatalf("%s audit events=%#v", action, events)
		}
	}
}

func TestPrincipalLifecycleRejectsSelfDisableAndDelete(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "admin", Kind: access.PrincipalKindUser, Email: "admin@example.test", DisplayName: "Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:       func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: actor.ID}, true },
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

func TestPrincipalDeletionMapsOwnedSecurableToConflictAndRollsBack(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "admin", Kind: access.PrincipalKindUser, Email: "admin@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "owner", Kind: access.PrincipalKindUser, Email: "owner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "owned")
	if _, err := repository.UpsertSecurableObject(t.Context(), object, owner.ID); err != nil {
		t.Fatal(err)
	}
	handler := Handler{
		Repository:       func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: actor.ID}, true },
	}
	recorder := httptest.NewRecorder()
	handler.DeletePrincipal(recorder, principalRequest(stdhttp.MethodDelete, "/api/v1/principals/owner", owner.ID))
	if recorder.Code != stdhttp.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if _, err := repository.PrincipalByID(t.Context(), owner.ID); err != nil {
		t.Fatalf("owner was deleted despite conflict: %v", err)
	}
	events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{Action: "principal.deleted", TargetID: owner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("failed deletion wrote success audit: %#v", events)
	}
}

func principalRequest(method, path, principalID string) *stdhttp.Request {
	request := httptest.NewRequest(method, path, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("principal", principalID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
