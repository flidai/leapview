package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompliancePageIsNotPublished(t *testing.T) {
	handler := NewHandler()
	for _, path := range []string{"/compliance", "/compliance/", "/compliance/overview"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want 404", path, response.Code)
			}
			page := response.Body.String()
			for _, draftContent := range []string{"site-compliance", "Current assurance state", "Pending approval"} {
				if strings.Contains(page, draftContent) {
					t.Errorf("GET %s exposes draft content %q", path, draftContent)
				}
			}
			if !strings.Contains(page, `name="robots" content="noindex,follow"`) {
				t.Error("unpublished page must not be indexed")
			}
		})
	}
}

func TestComplianceIsAbsentFromPublicLinks(t *testing.T) {
	handler := NewHandler()
	for _, path := range []string{"/", "/docs", "/docs/security/authorization", "/docs/security/audit", "/docs/security/tokens", "/sitemap.xml"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200", path, response.Code)
			}
			if strings.Contains(response.Body.String(), "/compliance") {
				t.Errorf("GET %s still links to the unpublished compliance section", path)
			}
		})
	}
}
