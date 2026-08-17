package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

func TestSessionDTOExposesBoundedDesktopDeviceMetadataWithoutSecrets(t *testing.T) {
	dto := sessionDTO(access.Session{
		ID:                "session_0123456789abcdef0123456789abcdef",
		PrincipalID:       "principal_secret",
		Kind:              access.SessionKindDesktop,
		InstanceID:        "instance_0123456789abcdef0123456789abcdef",
		ProfileID:         "profile_0123456789abcdef0123456789abcdef",
		ClientID:          "leapview-desktop",
		ExpiresAt:         "2026-07-29T18:00:00Z",
		AbsoluteExpiresAt: "2026-07-29T18:00:00Z",
		CreatedAt:         "2026-07-29T10:00:00Z",
		LastSeenAt:        "2026-07-29T10:15:00Z",
	})

	for key, want := range map[string]any{
		"id":                "session_0123456789abcdef0123456789abcdef",
		"kind":              access.SessionKindDesktop,
		"instanceId":        "instance_0123456789abcdef0123456789abcdef",
		"profileId":         "profile_0123456789abcdef0123456789abcdef",
		"clientId":          "leapview-desktop",
		"absoluteExpiresAt": "2026-07-29T18:00:00Z",
	} {
		if got := dto[key]; got != want {
			t.Fatalf("session DTO %q = %#v, want %#v", key, got, want)
		}
	}
	for _, forbidden := range []string{
		"principalId", "token", "tokenFingerprint", "tokenVerifier",
	} {
		if _, ok := dto[forbidden]; ok {
			t.Fatalf("session DTO exposes forbidden field %q", forbidden)
		}
	}
}

type sessionLifecycleRepository struct {
	access.Repository
	sessions           []access.Session
	listPrincipalID    string
	revokedPrincipalID string
	revokedSessionID   string
	audit              access.AuditEventInput
}

func (r *sessionLifecycleRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return true, nil
}

func (r *sessionLifecycleRepository) ListSessions(_ context.Context, principalID string) ([]access.Session, error) {
	r.listPrincipalID = principalID
	return r.sessions, nil
}

func (r *sessionLifecycleRepository) RevokeSessionForPrincipal(_ context.Context, principalID, sessionID string) error {
	r.revokedPrincipalID = principalID
	r.revokedSessionID = sessionID
	return nil
}

func (r *sessionLifecycleRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(r)
	r.audit = event
	return err
}

func TestAdministratorListsPrincipalSessionsWithoutCredentialMaterial(t *testing.T) {
	repository := &sessionLifecycleRepository{sessions: []access.Session{{
		ID:                "session_device",
		PrincipalID:       "principal_target",
		Kind:              access.SessionKindDesktop,
		InstanceID:        "instance_acme",
		ProfileID:         "profile_acme",
		ClientID:          "leapview-desktop",
		CreatedAt:         "2026-07-29T10:00:00Z",
		ExpiresAt:         "2026-07-29T18:00:00Z",
		AbsoluteExpiresAt: "2026-07-29T18:00:00Z",
	}}}
	handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal_admin"}, true }}
	request := requestWithRouteParam(stdhttp.MethodGet, "/api/v1/principals/principal_target/sessions", "principal", "principal_target")
	response := httptest.NewRecorder()

	handler.ListPrincipalSessions(response, request)

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, stdhttp.StatusOK, response.Body.String())
	}
	if repository.listPrincipalID != "principal_target" {
		t.Fatalf("listed principal = %q, want principal_target", repository.listPrincipalID)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["kind"] != string(access.SessionKindDesktop) {
		t.Fatalf("items = %#v, want one desktop session", body.Items)
	}
	for _, forbidden := range []string{"token", "tokenFingerprint", "tokenVerifier", "principalId"} {
		if strings.Contains(response.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("response exposes forbidden field %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestCurrentSessionListMarksOnlyTheCredentialUsedForTheRequest(t *testing.T) {
	repository := &sessionLifecycleRepository{sessions: []access.Session{
		{ID: "session_current", PrincipalID: "principal_me", Kind: access.SessionKindBrowser, CreatedAt: "2026-07-29T10:00:00Z", ExpiresAt: "2026-07-29T18:00:00Z"},
		{ID: "session_other", PrincipalID: "principal_me", Kind: access.SessionKindDesktop, CreatedAt: "2026-07-28T10:00:00Z", ExpiresAt: "2026-07-28T18:00:00Z"},
	}}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_me"}, true
		},
		CurrentSession: func(*stdhttp.Request) (string, bool) { return "session_current", true },
	}
	response := httptest.NewRecorder()
	handler.ListCurrentSessions(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me/sessions", nil))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("response = %d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || !body.Items[0].Current || body.Items[1].Current {
		t.Fatalf("sessions = %#v", body.Items)
	}
}

func TestAdministratorRevokesOnlyTheTargetPrincipalsSession(t *testing.T) {
	repository := &sessionLifecycleRepository{}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_admin"}, true
		},
	}
	request := requestWithRouteParams(
		stdhttp.MethodDelete,
		"/api/v1/principals/principal_target/sessions/session_device",
		map[string]string{"principal": "principal_target", "session": "session_device"},
	)
	response := httptest.NewRecorder()

	handler.RevokePrincipalSession(response, request)

	if response.Code != stdhttp.StatusNoContent {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, stdhttp.StatusNoContent, response.Body.String())
	}
	if repository.revokedPrincipalID != "principal_target" || repository.revokedSessionID != "session_device" {
		t.Fatalf("revoked principal/session = %q/%q, want principal_target/session_device", repository.revokedPrincipalID, repository.revokedSessionID)
	}
	if repository.audit.PrincipalID != "principal_admin" || repository.audit.ResourceKind != "session" || repository.audit.ResourceID != "session_device" {
		t.Fatalf("audit actor/resource = %q/%q/%q, want principal_admin/session/session_device", repository.audit.PrincipalID, repository.audit.ResourceKind, repository.audit.ResourceID)
	}
}

func requestWithRouteParam(method, target, key, value string) *stdhttp.Request {
	return requestWithRouteParams(method, target, map[string]string{key: value})
}

func requestWithRouteParams(method, target string, params map[string]string) *stdhttp.Request {
	request := httptest.NewRequest(method, target, nil)
	routeContext := chi.NewRouteContext()
	for key, value := range params {
		routeContext.URLParams.Add(key, value)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
