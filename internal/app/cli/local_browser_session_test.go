package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	accesscli "github.com/flidai/leapview/internal/access/cli"
	adminoffline "github.com/flidai/leapview/internal/admin/offline"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/stretchr/testify/require"
)

type localAuthFixture struct {
	mu              sync.Mutex
	email           string
	password        string
	mustChange      bool
	passwordChanges int
	approvedCode    string
}

func (fixture *localAuthFixture) handler(w http.ResponseWriter, r *http.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/login":
		http.SetCookie(w, &http.Cookie{Name: "csrf", Value: "csrf-cookie", Path: "/"})
		_, _ = fmt.Fprint(w, `<meta name="csrf-token" content="csrf-token">`)
	case r.Method == http.MethodGet && r.URL.Path == "/device":
		_, _ = fmt.Fprint(w, `<input type="hidden" name="gorilla.csrf.Token" value="csrf-token">`)
	case r.Method == http.MethodPost && r.URL.Path == "/auth/local/login":
		_ = r.ParseForm()
		if r.Form.Get("email") != fixture.email || r.Form.Get("password") != fixture.password || r.Form.Get("gorilla.csrf.Token") != "csrf-token" {
			http.Redirect(w, r, "/login?error=invalid_credentials", http.StatusSeeOther)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: localBrowserSessionCookie, Value: "browser-session", Path: "/", HttpOnly: true})
		if fixture.mustChange {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/", http.StatusFound)
	case r.Method == http.MethodPost && r.URL.Path == "/auth/local/password":
		_ = r.ParseForm()
		if r.Form.Get("currentPassword") != fixture.password || r.Form.Get("newPassword") == "" || r.Form.Get("gorilla.csrf.Token") != "csrf-token" {
			http.Error(w, "invalid password change", http.StatusUnauthorized)
			return
		}
		fixture.password = r.Form.Get("newPassword")
		fixture.mustChange = false
		fixture.passwordChanges++
		http.SetCookie(w, &http.Cookie{Name: localBrowserSessionCookie, Value: "", Path: "/", MaxAge: -1})
		http.Redirect(w, r, "/login", http.StatusFound)
	case r.Method == http.MethodPost && r.URL.Path == "/device":
		_ = r.ParseForm()
		if r.Form.Get("decision") != "approve" || r.Form.Get("gorilla.csrf.Token") != "csrf-token" {
			http.Error(w, "invalid approval", http.StatusBadRequest)
			return
		}
		fixture.approvedCode = r.Form.Get("user_code")
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func TestEstablishLocalBrowserSessionRotatesBootstrapPasswordWithoutUserInput(t *testing.T) {
	fixture := &localAuthFixture{email: "owner@localhost", password: "temporary-password", mustChange: true}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	t.Cleanup(server.Close)
	request := localBrowserSessionRequest(t, server.URL, fixture.email, fixture.password)

	client, cookie, err := establishLocalBrowserSession(t.Context(), request, server.Client())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Equal(t, localBrowserSessionCookie, cookie.Name)
	require.Equal(t, "browser-session", cookie.Value)

	fixture.mu.Lock()
	rotated := fixture.password
	require.False(t, fixture.mustChange)
	require.Equal(t, 1, fixture.passwordChanges)
	fixture.mu.Unlock()
	require.NotEqual(t, "temporary-password", rotated)

	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(request.CredentialsPath), localBrowserCredentialsName))
	require.NoError(t, err)
	var retained localBrowserCredentials
	require.NoError(t, json.Unmarshal(encoded, &retained))
	require.Equal(t, fixture.email, retained.Email)
	require.Equal(t, rotated, retained.CurrentPassword)
	require.Empty(t, retained.PendingPassword)

	_, _, err = establishLocalBrowserSession(t.Context(), request, server.Client())
	require.NoError(t, err)
	fixture.mu.Lock()
	require.Equal(t, 1, fixture.passwordChanges)
	fixture.mu.Unlock()
}

func TestEstablishLocalBrowserSessionRecoversInterruptedPasswordRotation(t *testing.T) {
	fixture := &localAuthFixture{email: "owner@localhost", password: "already-rotated-password"}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	t.Cleanup(server.Close)
	request := localBrowserSessionRequest(t, server.URL, fixture.email, "temporary-password")
	statePath := filepath.Join(filepath.Dir(request.CredentialsPath), localBrowserCredentialsName)
	require.NoError(t, saveLocalBrowserCredentials(statePath, localBrowserCredentials{
		Version: localBrowserCredentialsVersion, Email: fixture.email,
		CurrentPassword: "temporary-password", PendingPassword: fixture.password,
	}))

	_, _, err := establishLocalBrowserSession(t.Context(), request, server.Client())
	require.NoError(t, err)
	encoded, err := os.ReadFile(statePath)
	require.NoError(t, err)
	var retained localBrowserCredentials
	require.NoError(t, json.Unmarshal(encoded, &retained))
	require.Equal(t, fixture.password, retained.CurrentPassword)
	require.Empty(t, retained.PendingPassword)
}

func TestApproveLocalDeviceAuthorizationUsesAuthenticatedBrowserSession(t *testing.T) {
	fixture := &localAuthFixture{email: "owner@localhost", password: "rotated-password"}
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	t.Cleanup(server.Close)
	origin, err := localSessionOrigin(server.URL)
	require.NoError(t, err)
	client, err := localSessionHTTPClient(server.Client())
	require.NoError(t, err)
	client.Jar.SetCookies(origin, []*http.Cookie{{Name: localBrowserSessionCookie, Value: "browser-session", Path: "/"}})

	require.NoError(t, approveLocalDeviceAuthorization(t.Context(), client, origin, accesscli.DeviceChallenge{UserCode: "ABCD-EFGH"}))
	fixture.mu.Lock()
	require.Equal(t, "ABCD-EFGH", fixture.approvedCode)
	fixture.mu.Unlock()
}

func TestOpenLocalBrowserSessionTransfersHttpOnlyCookieAcrossLoopbackPorts(t *testing.T) {
	seen := make(chan *http.Cookie, 1)
	application := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(localBrowserSessionCookie)
		seen <- cookie
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(application.Close)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second}

	err = openLocalBrowserSession(context.Background(), application.URL, &http.Cookie{
		Name: localBrowserSessionCookie, Value: "opaque-session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}, func(uri string) error {
		response, requestErr := browser.Get(uri)
		if response != nil {
			response.Body.Close()
		}
		return requestErr
	})
	require.NoError(t, err)
	select {
	case cookie := <-seen:
		require.NotNil(t, cookie)
		require.Equal(t, "opaque-session", cookie.Value)
	case <-time.After(2 * time.Second):
		t.Fatal("application did not receive the handed-off browser session")
	}
}

func localBrowserSessionRequest(t *testing.T, origin, email, password string) localruntime.SessionRequest {
	t.Helper()
	root := t.TempDir()
	credentials := adminoffline.InitialCredentials{
		Email: email, TemporaryPassword: password, PublisherToken: "publisher-token",
		PublisherTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	encoded, err := json.Marshal(credentials)
	require.NoError(t, err)
	path := filepath.Join(root, "initial-credentials.json")
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	return localruntime.SessionRequest{
		TargetName: "local", Origin: origin, InstanceID: "instance", Environment: "dev",
		ProjectID: "lvproject_test", CredentialsPath: path,
	}
}

func TestLocalSessionOriginRejectsRemoteAndAmbiguousEndpoints(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1:8080", "http://localhost:8080", "http://example.com:8080", "http://127.0.0.1"} {
		_, err := localSessionOrigin(raw)
		require.Error(t, err, raw)
	}
	origin, err := localSessionOrigin("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:8080", origin.Host)
}
