package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteBrowserAuthorizationErrorRendersRouteAwareNavigationRecovery(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/models/model:orders/details?tab=lineage", nil)
	request.Header.Set("Accept", "text/html")
	WriteBrowserAuthorizationError(recorder, request, http.StatusForbidden)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		"LeapView",
		"You're signed in, but you don't have access to this data page",
		"A LeapView administrator needs to assign your account the required role or grant",
		`method="post"`,
		`action="/auth/switch-account"`,
		`name="return_to" value="/models/model:orders/details?tab=lineage"`,
		`name="gorilla.csrf.Token"`,
		"Sign in with a different account",
		`data-color-mode="auto"`,
		`data-light-theme="light"`,
		`data-dark-theme="dark"`,
		`src="/static/theme.js"`,
	} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("body does not contain %q: %s", want, recorder.Body.String())
		}
	}
}

func TestWriteBrowserAuthorizationErrorNamesInsightsWithoutOfferingAForbiddenLoop(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Accept", "text/html")
	WriteBrowserAuthorizationError(recorder, request, http.StatusForbidden)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	for _, want := range []string{"access to Insights", "Sign in with a different account"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("body does not contain %q: %s", want, recorder.Body.String())
		}
	}
	if strings.Contains(recorder.Body.String(), `href="/">Return to Insights`) {
		t.Fatalf("root denial offers a link back to the same forbidden route: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `name="return_to" value="/"`) {
		t.Fatalf("root denial does not preserve Insights as the authentication return target: %s", recorder.Body.String())
	}
}

func TestWriteBrowserAuthorizationErrorPreservesCommandAndJSONSemantics(t *testing.T) {
	for _, test := range []struct{ name, method, path, accept, contentType, datastarRequest string }{
		{name: "command", method: http.MethodPost, path: "/admin", accept: "text/html"},
		{name: "json", method: http.MethodGet, path: "/admin", accept: "application/json"},
		{name: "stream", method: http.MethodGet, path: "/updates", accept: "text/event-stream"},
		{name: "datastar form command", method: http.MethodPost, path: "/dashboards/new", accept: "text/event-stream, text/html, application/json", contentType: "application/x-www-form-urlencoded", datastarRequest: "true"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Accept", test.accept)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Datastar-Request", test.datastarRequest)
			WriteBrowserAuthorizationError(recorder, request, http.StatusForbidden)
			if recorder.Code != http.StatusForbidden || strings.Contains(recorder.Header().Get("Content-Type"), "text/html") || recorder.Body.String() != "Forbidden\n" {
				t.Fatalf("response = %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
		})
	}
}

func TestWriteBrowserAuthorizationErrorRendersFormNavigationRecovery(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/dashboards/new", strings.NewReader("title=Sales"))
	request.Header.Set("Accept", "text/html")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	WriteBrowserAuthorizationError(recorder, request, http.StatusForbidden)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") || !strings.Contains(recorder.Body.String(), "You're signed in, but you don't have access to this dashboard") {
		t.Fatalf("response = %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}
