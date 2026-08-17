package http

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
)

type fakeAuthoringAuthentication struct {
	AuthoringAuthentication
	approve   string
	sessions  []access.AuthoringSession
	revokeErr error
}

func (service *fakeAuthoringAuthentication) InstanceID() string { return "lvinst_prod" }

func (service *fakeAuthoringAuthentication) ApproveDeviceAuthorization(_ context.Context, _ access.Principal, userCode string) error {
	service.approve = userCode
	return nil
}

func (service *fakeAuthoringAuthentication) ListSessions(context.Context, string) ([]access.AuthoringSession, error) {
	return service.sessions, nil
}

func (service *fakeAuthoringAuthentication) RevokeSession(context.Context, string, string) error {
	return service.revokeErr
}

func TestDeviceAuthorizationApprovalRejectsBearerCredentials(t *testing.T) {
	service := &fakeAuthoringAuthentication{}
	handler := Handler{
		AuthoringAuth: service,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-1", Email: "developer@example.com"}, true
		},
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return access.APICredential{}, true
		},
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/access/device-authorizations/approval", strings.NewReader(
		`{"userCode":"ABCD-EFGH","approved":true}`,
	))
	recorder := httptest.NewRecorder()
	handler.DecideDeviceAuthorization(recorder, request)
	if recorder.Code != stdhttp.StatusForbidden || service.approve != "" {
		t.Fatalf("status=%d approved=%q body=%s", recorder.Code, service.approve, recorder.Body.String())
	}
}

func TestCurrentAuthoringSessionListMarksTheBearerSession(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	service := &fakeAuthoringAuthentication{sessions: []access.AuthoringSession{
		{ID: "authoring_current", Kind: access.AuthoringSessionHumanCLI, ClientID: "cli", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: "authoring_other", Kind: access.AuthoringSessionHumanCLI, ClientID: "cli", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}}
	handler := Handler{
		AuthoringAuth: service,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return access.APICredential{Authoring: &service.sessions[0]}, true
		},
	}
	response := httptest.NewRecorder()
	handler.ListCurrentAuthoringSessions(response, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me/authoring-sessions", nil))
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

func TestRevokeCurrentAuthoringSessionHidesForeignSession(t *testing.T) {
	service := &fakeAuthoringAuthentication{revokeErr: access.ErrInvalidAuthoringCredential}
	handler := Handler{
		AuthoringAuth: service,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-1"}, true
		},
	}
	request := requestWithRouteParam(stdhttp.MethodDelete, "/api/v1/me/authoring-sessions/foreign", "session", "foreign")
	response := httptest.NewRecorder()
	handler.RevokeCurrentAuthoringSession(response, request)
	if response.Code != stdhttp.StatusBadRequest || !errors.Is(service.revokeErr, access.ErrInvalidAuthoringCredential) {
		t.Fatalf("response = %d body=%s", response.Code, response.Body.String())
	}
}
