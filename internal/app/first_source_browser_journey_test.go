package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type firstSourceJourneyBrowser struct {
	fixture *sourceCredentialHTTPJourney
	cookies map[string]*http.Cookie
	csrf    string
}

func (b *firstSourceJourneyBrowser) signalCommand(t *testing.T, operation string, command projectsignals.FirstSourceCredentialCommandSignal) projectsignals.FirstSourceCredentialSignal {
	t.Helper()
	response := b.command(t, "/connections/connection:warehouse/first-source/commands/"+command.Action, operation, projectsignals.FirstSourceCredentialEnvelope{FirstSourceCredentials: projectsignals.FirstSourceCredentialSignal{Command: command}})
	require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
	if command.Password != "" {
		require.NotContains(t, response.Body.String(), command.Password)
	}
	for _, line := range strings.Split(response.Body.String(), "\n") {
		if raw, ok := strings.CutPrefix(line, "data: signals "); ok {
			var envelope projectsignals.FirstSourceCredentialEnvelope
			require.NoError(t, json.Unmarshal([]byte(raw), &envelope))
			require.Empty(t, envelope.FirstSourceCredentials.Error)
			require.Empty(t, envelope.FirstSourceCredentials.Command.Password)
			return envelope.FirstSourceCredentials
		}
	}
	t.Fatal("credential command did not publish typed signals")
	return projectsignals.FirstSourceCredentialSignal{}
}

func newFirstSourceJourneyBrowser(t *testing.T, f *sourceCredentialHTTPJourney, email, password string) *firstSourceJourneyBrowser {
	t.Helper()
	b := &firstSourceJourneyBrowser{fixture: f, cookies: map[string]*http.Cookie{}}
	page := b.request(t, http.MethodGet, "/login", "", "", nil)
	require.Equal(t, http.StatusOK, page.Code)
	match := regexp.MustCompile(`name="csrf-token" content="([^"]+)"`).FindStringSubmatch(page.Body.String())
	require.Len(t, match, 2)
	b.csrf = match[1]
	body := url.Values{"email": {email}, "password": {password}, "gorilla.csrf.Token": {b.csrf}}
	login := b.request(t, http.MethodPost, "/auth/local/login", "", "application/x-www-form-urlencoded", strings.NewReader(body.Encode()))
	require.Equal(t, http.StatusFound, login.Code, "real local login must succeed")
	require.Equal(t, "/", login.Header().Get("Location"))
	return b
}

func (b *firstSourceJourneyBrowser) request(t *testing.T, method, path, operation, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "https://localhost"+path, body)
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	r.Header.Set("Origin", "https://localhost")
	r.Header.Set("X-CSRF-Token", b.csrf)
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	if operation != "" {
		id, err := uuid.NewV7()
		require.NoError(t, err)
		r.Header.Set("X-Request-ID", id.String())
		r.Header.Set("X-LeapView-Operation-ID", operation)
		if operation == "createProjectRoleBinding" {
			r.Header.Set("Idempotency-Key", id.String())
		}
	}
	w := httptest.NewRecorder()
	b.fixture.target.Handler().ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		b.cookies[c.Name] = c
	}
	return w
}

func (b *firstSourceJourneyBrowser) command(t *testing.T, path, operation string, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	w := b.request(t, http.MethodPost, path, operation, "application/json", strings.NewReader(string(encoded)))
	require.Contains(t, []int{http.StatusOK, http.StatusCreated, http.StatusAccepted}, w.Code, "browser command %s failed", operation)
	return w
}
