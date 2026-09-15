package http

import (
	"net/http"
	"testing"
)

func TestFirstNonEmptyHeaderPreservesOrderedTrimmedPreference(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-First", "  \t")
	request.Header.Set("X-Second", "  selected  ")
	request.Header.Set("X-Third", "ignored")
	if got, want := FirstNonEmptyHeader(request, "X-First", "X-Second", "X-Third"), "selected"; got != want {
		t.Fatalf("FirstNonEmptyHeader() = %q, want %q", got, want)
	}
	if got := FirstNonEmptyHeader(nil, "X-Second"); got != "" {
		t.Fatalf("FirstNonEmptyHeader(nil) = %q, want empty", got)
	}
}

func TestAuditRequestIdentityPreservesHeaderAliasesAndFallback(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Request-Id", " legacy-request ")
	request.Header.Set("X-Request-ID", " canonical-request ")
	request.Header.Set("X-Correlation-Id", " legacy-correlation ")
	request.Header.Set("X-Correlation-ID", " canonical-correlation ")
	if requestID, correlationID := AuditRequestIdentity(request); requestID != "canonical-request" || correlationID != "canonical-correlation" {
		t.Fatalf("AuditRequestIdentity() = (%q, %q)", requestID, correlationID)
	}
	request.Header.Del("X-Correlation-ID")
	request.Header.Del("X-Correlation-Id")
	if requestID, correlationID := AuditRequestIdentity(request); requestID != "canonical-request" || correlationID != requestID {
		t.Fatalf("AuditRequestIdentity() fallback = (%q, %q)", requestID, correlationID)
	}
}
