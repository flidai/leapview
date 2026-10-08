package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
)

// Credentials are provisioned through the real repository, rather than an
// identity provider. SCIM mutations and subsequent cookie/bearer authentication
// cross the mounted application router over HTTP with no injected principal.
func TestSCIMLifecycleKeepsOtherPrincipalsAuthenticated(t *testing.T) {
	store := testStore(t)
	repo := store.fixture.Graph.Access
	ctx := t.Context()
	const scimToken = "scim-lifecycle-fixture-bearer"
	auth := testAuth(store, accessmodule.AuthConfig{LocalAuth: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{
		Auth: auth, SCIMBearerToken: scimToken,
	}))
	httpServer := httptest.NewServer(server.Routes())
	t.Cleanup(httpServer.Close)
	client := httpServer.Client()
	client.Timeout = 10 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	t.Cleanup(client.CloseIdleConnections)

	request := func(method, path, bearer, session string, payload any, want int) (http.Header, []byte) {
		t.Helper()
		var body []byte
		if payload != nil {
			var err error
			body, err = json.Marshal(payload)
			if err != nil {
				t.Fatalf("encode %s %s: %v", method, path, err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, httpServer.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
			req.Header.Set("Accept", "application/json")
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/scim+json")
		}
		if session != "" {
			req.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		body, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatalf("read %s %s: %v", method, path, err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s status = %d, want %d, body=%s", method, path, response.StatusCode, want, body)
		}
		return response.Header, body
	}
	resourceID := func(body []byte) string {
		t.Helper()
		var resource struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &resource); err != nil || resource.ID == "" {
			t.Fatalf("SCIM resource missing identity: err=%v body=%s", err, body)
		}
		return resource.ID
	}
	provision := func(name string) string {
		t.Helper()
		_, body := request(http.MethodPost, "/scim/v2/Users", scimToken, "", map[string]any{
			"schemas":    []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
			"externalId": name, "userName": name + "@example.test", "displayName": name, "active": true,
		}, http.StatusCreated)
		return resourceID(body)
	}
	targetID, controlID := provision("lifecycle-target"), provision("lifecycle-control")
	_, groupBody := request(http.MethodPost, "/scim/v2/Groups", scimToken, "", map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
		"externalId": "lifecycle-shared-group", "displayName": "Lifecycle shared group",
		"members": []map[string]string{{"value": targetID}, {"value": controlID}},
	}, http.StatusCreated)
	groupID := resourceID(groupBody)
	assertMembers := func(want ...string) {
		t.Helper()
		members, err := repo.ListSCIMGroupMembers(ctx, groupID)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[string]bool, len(members))
		for _, member := range members {
			got[member.PrincipalID] = true
		}
		if len(members) != len(want) {
			t.Fatalf("SCIM group members = %v, want %v", got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("SCIM group members = %v, missing %s", got, id)
			}
		}
	}
	type credentials struct{ principalID, session, token string }
	issueCredentials := func(principalID string) credentials {
		t.Helper()
		session, err := repo.CreateSession(ctx, principalID, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		token, _ := testScopedAPIToken(t, ctx, store, access.ScopedAPITokenInput{
			PrincipalID: principalID, Name: "lifecycle-token", Permissions: []access.PermissionPair{},
		})
		return credentials{principalID, session, token}
	}
	assertAuthenticated := func(label string, credential credentials) {
		t.Helper()
		t.Logf("checking authenticated requests: %s", label)
		request(http.MethodGet, "/admin/profile", "", credential.session, nil, http.StatusOK)
		_, body := request(http.MethodGet, "/api/v1/me", credential.token, "", nil, http.StatusOK)
		var principal struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &principal); err != nil || principal.ID != credential.principalID {
			t.Fatalf("%s authenticated principal = %s, want %s, err=%v", label, body, credential.principalID, err)
		}
	}
	assertRevoked := func(credential credentials) {
		t.Helper()
		headers, _ := request(http.MethodGet, "/admin/profile", "", credential.session, nil, http.StatusFound)
		if got := headers.Get("Location"); got != "/login?error=session_expired" {
			t.Fatalf("revoked cookie redirect = %q, want session_expired recovery", got)
		}
		request(http.MethodGet, "/api/v1/me", credential.token, "", nil, http.StatusUnauthorized)
	}
	setActive := func(active bool) {
		t.Helper()
		_, body := request(http.MethodPatch, "/scim/v2/Users/"+url.PathEscape(targetID), scimToken, "", map[string]any{
			"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
			"Operations": []map[string]any{{"op": "replace", "path": "active", "value": active}},
		}, http.StatusOK)
		var user struct {
			ID     string `json:"id"`
			Active *bool  `json:"active"`
		}
		if err := json.Unmarshal(body, &user); err != nil || user.ID != targetID || user.Active == nil || *user.Active != active {
			t.Fatalf("SCIM lifecycle response = %s, want target active=%v, err=%v", body, active, err)
		}
	}

	target, control := issueCredentials(targetID), issueCredentials(controlID)
	assertAuthenticated("target_before_disable", target)
	assertAuthenticated("control_before_disable", control)
	assertMembers(targetID, controlID)

	setActive(false)
	assertRevoked(target)
	assertAuthenticated("control_after_target_disable", control)
	assertMembers(controlID)

	setActive(true)
	assertRevoked(target)
	assertAuthenticated("control_after_target_reactivation", control)
	assertMembers(controlID)

	fresh := issueCredentials(targetID)
	if fresh.session == target.session || fresh.token == target.token {
		t.Fatal("reactivation reused a revoked credential")
	}
	assertAuthenticated("fresh_target_after_reactivation", fresh)
	assertRevoked(target)
	assertAuthenticated("control_after_target_recovery", control)
	assertMembers(controlID)
}
