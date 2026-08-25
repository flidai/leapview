package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteBrowserAuthorizationErrorRendersRouteAwareNavigationRecovery(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/models/model:orders/details", nil)
	request.Header.Set("Accept", "text/html")
	WriteBrowserAuthorizationError(recorder, request, http.StatusForbidden)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	for _, want := range []string{"LeapView", "data page", "Return to Insights", "No changes were made"} {
		if !strings.Contains(recorder.Body.String(), want) {
			t.Fatalf("body does not contain %q: %s", want, recorder.Body.String())
		}
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
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") || !strings.Contains(recorder.Body.String(), "You don't have access to this dashboard") {
		t.Fatalf("response = %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
}
