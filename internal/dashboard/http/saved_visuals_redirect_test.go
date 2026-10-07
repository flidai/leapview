package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSavedVisualRedirectStaysInBuilder(t *testing.T) {
	for _, target := range []string{"https://evil.example/dashboard", "//evil.example/dashboards/x", `\\evil.example/dashboards/x`, "javascript:alert(1)", "/explore", "/%2f%2fevil.example", "://invalid"} {
		w := httptest.NewRecorder()
		redirectSavedVisualToBuilder(w, httptest.NewRequest("POST", "/import", nil), target)
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" {
			t.Fatalf("unsafe destination accepted: %q", target)
		}
	}
	page := "//evil.example/?page=other&embed=invalid"
	target := dashboardBuilderDraftRoute("dashboard-id", "draft-id", "/edit") + "&embed=chat&page=" + url.QueryEscape(page)
	w := httptest.NewRecorder()
	redirectSavedVisualToBuilder(w, httptest.NewRequest("POST", "/import", nil), target)
	result, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusSeeOther || result.Host != "" || result.Path != "/dashboards/dashboard-id/edit" || result.Query().Get("page") != page || result.Query().Get("embed") != "chat" || result.Query().Get("draft") != "draft-id" {
		t.Fatalf("builder context not retained: %v %q", err, w.Header().Get("Location"))
	}
}
