package http

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestCompliancePagePublishesBoundedAssuranceStatus(t *testing.T) {
	baseURL, err := url.Parse("https://leapview.dev")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithOptions(Options{BaseURL: baseURL})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/compliance", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /compliance status = %d, want 200", response.Code)
	}
	page := response.Body.String()
	for _, expected := range []string{
		`<link rel="canonical" href="https://leapview.dev/compliance">`,
		`<h1>Compliance &amp; Security</h1>`,
		`Current assurance state`,
		`Managed-service launch`,
		`Pending approval`,
		`Implemented capabilities`,
		`Qualification-tested capabilities`,
		`Pending verification &amp; approval`,
		`Certifications &amp; regulatory status`,
		`Not currently claimed`,
		`Last reviewed:`,
		`<main`,
		`id="main-content"`,
		`<footer`,
		`href="https://github.com/flidai/leapview/security/advisories/new"`,
	} {
		if !strings.Contains(page, expected) {
			t.Errorf("compliance page missing %q", expected)
		}
	}
	if strings.Count(page, "<h1>") != 1 {
		t.Errorf("compliance page has %d h1 headings, want one", strings.Count(page, "<h1>"))
	}
	for _, unsupported := range []string{
		`fully compliant`, `GDPR compliant`, `ISO certified`, `enterprise-grade compliance`,
		`production-ready`, `EU hosted`, `24/7 support`, `99.9% availability`,
		`RTO `, `RPO `, `linear.app`,
	} {
		if strings.Contains(page, unsupported) {
			t.Errorf("compliance page contains unsupported claim or private link %q", unsupported)
		}
	}
	if regexp.MustCompile(`(?i)(?:is|are)\s+(?:iso(?:/iec)?\s*27001\s+)?certified`).MatchString(page) {
		t.Error("compliance page implies current certification")
	}
}

func TestCompliancePageIsDiscoverableWithoutBreakingExistingRoutes(t *testing.T) {
	handler := NewHandler()
	for path, wantStatus := range map[string]int{
		"/":                            http.StatusOK,
		"/docs":                        http.StatusOK,
		"/docs/security/authorization": http.StatusOK,
		"/docs/security/audit":         http.StatusOK,
		"/docs/security/tokens":        http.StatusOK,
		"/compliance":                  http.StatusOK,
		"/not-a-route":                 http.StatusNotFound,
		"/sitemap.xml":                 http.StatusOK,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != wantStatus {
			t.Errorf("GET %s status = %d, want %d", path, response.Code, wantStatus)
		}
		if path == "/sitemap.xml" && !strings.Contains(response.Body.String(), "/compliance") {
			t.Error("sitemap does not include /compliance")
		}
	}
}
