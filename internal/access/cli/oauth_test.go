package cli

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
)

func TestStandardOAuthClientUsesRFC8628DeviceFlow(t *testing.T) {
	var deviceRequest, tokenRequest url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		switch r.URL.Path {
		case "/oauth/device/code":
			deviceRequest = r.Form
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "device-secret",
				"user_code":                 "ABCD-EFGH",
				"verification_uri":          serverURL(r) + "/device",
				"verification_uri_complete": serverURL(r) + "/device?user_code=ABCD-EFGH",
				"expires_in":                60,
				"interval":                  1,
			})
		case "/oauth/token":
			tokenRequest = r.Form
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access-secret",
				"refresh_token": "refresh-secret",
				"token_type":    "Bearer",
				"expires_in":    900,
				"session_id":    "session-1",
				"session_kind":  "human_cli",
				"target_id":     "lvinst_prod",
				"project_id":    "analytics",
				"scope":         "RESOURCE_EDIT RESOURCE_PUBLISH",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := StandardOAuthClient{HTTPClient: server.Client()}
	authorization, err := client.Begin(context.Background(), DeviceAuthorizationRequest{
		Origin:       server.URL,
		ProjectID:    "analytics",
		Capabilities: []string{"RESOURCE_EDIT", "RESOURCE_PUBLISH"},
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	challenge := authorization.Challenge()
	if challenge.UserCode != "ABCD-EFGH" || !strings.HasSuffix(challenge.VerificationURI, "/device") {
		t.Fatalf("challenge = %+v", challenge)
	}
	token, err := authorization.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error = %v", err)
	}
	if token.AccessToken != "access-secret" || token.RefreshToken != "refresh-secret" {
		t.Fatalf("token = %+v", token)
	}
	details, err := oauthAuthoringTokenDetails(token)
	if err != nil {
		t.Fatalf("oauthAuthoringTokenDetails() error = %v", err)
	}
	if details.SessionID != "session-1" ||
		details.Kind != access.AuthoringSessionHumanCLI ||
		details.TargetID != "lvinst_prod" ||
		details.ProjectID != "analytics" {
		t.Fatalf("details = %+v", details)
	}
	if deviceRequest.Get("client_id") != access.AuthoringCLIClientID ||
		deviceRequest.Get("project_id") != "analytics" ||
		deviceRequest.Get("scope") != "RESOURCE_EDIT RESOURCE_PUBLISH" {
		t.Fatalf("device request = %v", deviceRequest)
	}
	if tokenRequest.Get("grant_type") != authoringDeviceGrantType ||
		tokenRequest.Get("client_id") != access.AuthoringCLIClientID ||
		tokenRequest.Get("device_code") != "device-secret" {
		t.Fatalf("token request = %v", tokenRequest)
	}
}

func TestStandardOAuthClientRefreshesThroughTokenSource(t *testing.T) {
	var request url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		request = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-rotated",
			"refresh_token": "refresh-rotated",
			"token_type":    "Bearer",
			"expires_in":    900,
			"session_id":    "session-1",
			"session_kind":  "human_cli",
			"target_id":     "lvinst_prod",
			"project_id":    "analytics",
			"scope":         "RESOURCE_EDIT",
		})
	}))
	defer server.Close()

	token, err := (StandardOAuthClient{HTTPClient: server.Client()}).Refresh(
		context.Background(),
		OAuthRefreshRequest{Origin: server.URL, RefreshToken: "refresh-secret"},
	)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if token.AccessToken != "access-rotated" || token.RefreshToken != "refresh-rotated" {
		t.Fatalf("token=%+v", token)
	}
	if request.Get("grant_type") != "refresh_token" ||
		request.Get("refresh_token") != "refresh-secret" ||
		request.Get("client_id") != access.AuthoringCLIClientID {
		t.Fatalf("request=%v", request)
	}
}

func TestStandardOAuthClientUsesClientCredentialsForWorkloadIdentity(t *testing.T) {
	var request url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		request = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "workload-access",
			"token_type":   "Bearer",
			"expires_in":   600,
			"session_id":   "session-workload",
			"session_kind": "workload",
			"target_id":    "lvinst_prod",
			"project_id":   "analytics",
			"scope":        "RESOURCE_EDIT RESOURCE_PUBLISH",
		})
	}))
	defer server.Close()

	token, err := (StandardOAuthClient{HTTPClient: server.Client()}).Workload(
		context.Background(),
		WorkloadIdentityRequest{
			Origin: server.URL, InstanceID: "lvinst_prod", ProjectID: "analytics",
			ClientID: "sp-ci", ClientSecret: "service-secret",
			Capabilities: []string{"RESOURCE_EDIT", "RESOURCE_PUBLISH"}, Lifetime: 10 * time.Minute,
		},
	)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	if token.AccessToken != "workload-access" || token.RefreshToken != "" {
		t.Fatalf("token=%+v", token)
	}
	if request.Get("grant_type") != "client_credentials" ||
		request.Get("client_id") != "sp-ci" ||
		request.Get("client_secret") != "service-secret" ||
		request.Get("project_id") != "analytics" ||
		request.Get("scope") != "RESOURCE_EDIT RESOURCE_PUBLISH" ||
		request.Get("lifetime_seconds") != "600" {
		t.Fatalf("request=%v", request)
	}
}

func TestStandardOAuthClientRevokesThroughRFC7009Endpoint(t *testing.T) {
	var request url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		request = r.Form
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := (StandardOAuthClient{HTTPClient: server.Client()}).Revoke(
		context.Background(),
		OAuthRevokeRequest{Origin: server.URL, AccessToken: "access-secret"},
	)
	if err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if request.Get("client_id") != access.AuthoringCLIClientID ||
		request.Get("token") != "access-secret" ||
		request.Get("token_type_hint") != "access_token" {
		t.Fatalf("request=%v", request)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
