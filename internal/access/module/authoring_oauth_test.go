package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesshttp "github.com/flidai/leapview/internal/access/http"
)

type fakeAuthoringOAuth struct {
	accesshttp.AuthoringAuthentication
	beganScope    access.AuthoringScope
	exchangedCode string
	exchangeErr   error
	refreshToken  string
	refreshErr    error
	workloadInput access.WorkloadIdentityInput
	workloadErr   error
	revokedToken  string
	revokeErr     error
}

func (service *fakeAuthoringOAuth) InstanceID() string {
	return "lvinst_prod"
}

func (service *fakeAuthoringOAuth) BeginDeviceAuthorization(_ context.Context, scope access.AuthoringScope) (access.DeviceAuthorizationResponse, error) {
	service.beganScope = scope
	return access.DeviceAuthorizationResponse{
		DeviceCode:              "device-secret",
		UserCode:                "ABCD-EFGH",
		VerificationURI:         "https://prod.example.com/device",
		VerificationURIComplete: "https://prod.example.com/device?user_code=ABCD-EFGH",
		ExpiresIn:               600,
		Interval:                5,
	}, nil
}

func (service *fakeAuthoringOAuth) ExchangeDeviceCode(_ context.Context, code string) (access.AuthoringTokenSet, error) {
	service.exchangedCode = code
	if service.exchangeErr != nil {
		return access.AuthoringTokenSet{}, service.exchangeErr
	}
	return fakeAuthoringTokenSet(), nil
}

func (service *fakeAuthoringOAuth) Refresh(_ context.Context, token string) (access.AuthoringTokenSet, error) {
	service.refreshToken = token
	if service.refreshErr != nil {
		return access.AuthoringTokenSet{}, service.refreshErr
	}
	tokens := fakeAuthoringTokenSet()
	tokens.AccessToken = "access-rotated"
	tokens.RefreshToken = "refresh-rotated"
	return tokens, nil
}

func (service *fakeAuthoringOAuth) ExchangeWorkloadIdentity(_ context.Context, input access.WorkloadIdentityInput) (access.AuthoringTokenSet, error) {
	service.workloadInput = input
	if service.workloadErr != nil {
		return access.AuthoringTokenSet{}, service.workloadErr
	}
	tokens := fakeAuthoringTokenSet()
	tokens.AccessToken = "workload-access"
	tokens.RefreshToken = ""
	tokens.ExpiresIn = 600
	tokens.Session.Kind = access.AuthoringSessionWorkload
	tokens.Session.ClientID = input.ClientID
	return tokens, nil
}

func (service *fakeAuthoringOAuth) RevokeAccessToken(_ context.Context, token string) error {
	service.revokedToken = token
	return service.revokeErr
}

func fakeAuthoringTokenSet() access.AuthoringTokenSet {
	return access.AuthoringTokenSet{
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		TokenType:    "Bearer",
		ExpiresIn:    900,
		Session: access.AuthoringSession{
			ID:       "session-1",
			Kind:     access.AuthoringSessionHumanCLI,
			ClientID: access.AuthoringCLIClientID,
			Scope: access.AuthoringScope{
				TargetID:     "lvinst_prod",
				ProjectID:    "analytics",
				Capabilities: []access.Capability{access.CapabilityResourcePublish},
			},
		},
	}
}

func TestAuthoringDeviceAuthorizationUsesRFC8628WireFormat(t *testing.T) {
	service := &fakeAuthoringOAuth{}
	module := &Module{handler: accesshttp.Handler{AuthoringAuth: service}}
	form := url.Values{
		"client_id":  {access.AuthoringCLIClientID},
		"project_id": {"analytics"},
		"scope":      {string(access.CapabilityResourcePublish)},
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/device/code", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	module.AuthoringDeviceAuthorization(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers=%v", recorder.Header())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["device_code"] != "device-secret" ||
		response["user_code"] != "ABCD-EFGH" ||
		response["verification_uri"] != "https://prod.example.com/device" ||
		response["verification_uri_complete"] != "https://prod.example.com/device?user_code=ABCD-EFGH" ||
		response["expires_in"] != float64(600) ||
		response["interval"] != float64(5) {
		t.Fatalf("response=%v", response)
	}
	if service.beganScope.TargetID != "lvinst_prod" ||
		service.beganScope.ProjectID != "analytics" ||
		len(service.beganScope.Capabilities) != 1 ||
		service.beganScope.Capabilities[0] != access.CapabilityResourcePublish {
		t.Fatalf("scope=%+v", service.beganScope)
	}
}

func TestAuthoringDeviceTokenUsesOAuthErrorsAndTokenFields(t *testing.T) {
	for name, test := range map[string]struct {
		err       error
		status    int
		errorCode string
	}{
		"pending": {
			err:       access.ErrDeviceAuthorizationPending,
			status:    http.StatusBadRequest,
			errorCode: "authorization_pending",
		},
		"slow down": {
			err:       access.ErrDeviceAuthorizationSlowDown,
			status:    http.StatusBadRequest,
			errorCode: "slow_down",
		},
		"denied": {
			err:       access.ErrDeviceAuthorizationDenied,
			status:    http.StatusBadRequest,
			errorCode: "access_denied",
		},
		"expired": {
			err:       access.ErrDeviceAuthorizationExpired,
			status:    http.StatusBadRequest,
			errorCode: "expired_token",
		},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeAuthoringOAuth{exchangeErr: test.err}
			module := &Module{handler: accesshttp.Handler{AuthoringAuth: service}}
			form := url.Values{
				"client_id":   {access.AuthoringCLIClientID},
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
				"device_code": {"device-secret"},
			}
			request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()

			module.AuthoringOAuthToken(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Error != test.errorCode {
				t.Fatalf("error=%q want=%q body=%s", response.Error, test.errorCode, recorder.Body.String())
			}
		})
	}

	service := &fakeAuthoringOAuth{}
	module := &Module{handler: accesshttp.Handler{AuthoringAuth: service}}
	form := url.Values{
		"client_id":   {access.AuthoringCLIClientID},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		"device_code": {"device-secret"},
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	module.AuthoringOAuthToken(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers=%v", recorder.Header())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["access_token"] != "access-secret" ||
		response["refresh_token"] != "refresh-secret" ||
		response["token_type"] != "Bearer" ||
		response["expires_in"] != float64(900) ||
		response["session_id"] != "session-1" ||
		response["target_id"] != "lvinst_prod" ||
		response["project_id"] != "analytics" {
		t.Fatalf("response=%v", response)
	}
	if service.exchangedCode != "device-secret" {
		t.Fatalf("device code=%q", service.exchangedCode)
	}
}

func TestAuthoringOAuthRoutingIsUnambiguous(t *testing.T) {
	for name, values := range map[string]url.Values{
		"device grant": {
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":  {access.AuthoringCLIClientID},
		},
		"authoring client": {
			"grant_type": {"refresh_token"},
			"client_id":  {access.AuthoringCLIClientID},
		},
		"authoring workload": {
			"grant_type":       {"client_credentials"},
			"client_id":        {"sp-ci"},
			"project_id":       {"analytics"},
			"lifetime_seconds": {"600"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(values.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if !requestTargetsAuthoringOAuth(request) {
				t.Fatal("request was not routed to authoring OAuth")
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(url.Values{
		"grant_type": {"authorization_code"},
		"client_id":  {"mcp-client"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if requestTargetsAuthoringOAuth(request) {
		t.Fatal("MCP authorization-code request routed to authoring OAuth")
	}
}

func TestAuthoringOAuthRefreshRotatesTokensAndMapsReplayToInvalidGrant(t *testing.T) {
	form := url.Values{
		"client_id":     {access.AuthoringCLIClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {"refresh-secret"},
	}
	request := func() *http.Request {
		value := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		value.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return value
	}

	service := &fakeAuthoringOAuth{}
	recorder := httptest.NewRecorder()
	(&Module{handler: accesshttp.Handler{AuthoringAuth: service}}).AuthoringOAuthToken(recorder, request())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.refreshToken != "refresh-secret" {
		t.Fatalf("refresh token=%q", service.refreshToken)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["access_token"] != "access-rotated" || response["refresh_token"] != "refresh-rotated" {
		t.Fatalf("response=%v", response)
	}

	service = &fakeAuthoringOAuth{refreshErr: access.ErrAuthoringRefreshReplay}
	recorder = httptest.NewRecorder()
	(&Module{handler: accesshttp.Handler{AuthoringAuth: service}}).AuthoringOAuthToken(recorder, request())
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var replay struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &replay); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replay.Error != "invalid_grant" || strings.Contains(replay.ErrorDescription, "refresh-secret") {
		t.Fatalf("replay response=%+v", replay)
	}
}

func TestAuthoringOAuthClientCredentialsIssuesExactScopeWorkloadToken(t *testing.T) {
	form := url.Values{
		"grant_type":       {"client_credentials"},
		"client_id":        {"sp-ci"},
		"client_secret":    {"service-secret"},
		"project_id":       {"analytics"},
		"scope":            {"RESOURCE_PUBLISH RESOURCE_USE"},
		"lifetime_seconds": {"600"},
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	service := &fakeAuthoringOAuth{}
	recorder := httptest.NewRecorder()

	(&Module{handler: accesshttp.Handler{AuthoringAuth: service}}).AuthoringOAuthToken(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.workloadInput.ClientID != "sp-ci" ||
		service.workloadInput.ClientSecret != "service-secret" ||
		service.workloadInput.Scope.TargetID != "lvinst_prod" ||
		service.workloadInput.Scope.ProjectID != "analytics" ||
		service.workloadInput.Lifetime != 10*time.Minute {
		t.Fatalf("workload input=%+v", service.workloadInput)
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["access_token"] != "workload-access" ||
		response["session_kind"] != string(access.AuthoringSessionWorkload) ||
		response["refresh_token"] != nil {
		t.Fatalf("response=%v", response)
	}
}

func TestAuthoringOAuthRevocationUsesRFC7009WireFormat(t *testing.T) {
	form := url.Values{
		"client_id":       {access.AuthoringCLIClientID},
		"token":           {"access-secret"},
		"token_type_hint": {"access_token"},
	}
	request := httptest.NewRequest(http.MethodPost, "/oauth/revoke", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	service := &fakeAuthoringOAuth{}
	recorder := httptest.NewRecorder()

	(&Module{handler: accesshttp.Handler{AuthoringAuth: service}}).OAuthRevoke(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if service.revokedToken != "access-secret" {
		t.Fatalf("revoked token=%q", service.revokedToken)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}
