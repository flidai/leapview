package safetext

import (
	"strings"
	"testing"
)

func TestBoundedSummaryRemovesCredentialRepresentations(t *testing.T) {
	input := strings.Join([]string{
		`postgres://user:hunter2@db.example/prod`,
		`{"secret":"json-secret","safe":"visible"}`,
		`https://s3.example/object?X-Amz-Credential=access&X-Amz-Signature=signed`,
		`AWS_SECRET_ACCESS_KEY=provider-secret`,
		`Authorization: Bearer bearer-secret`,
		"second\nline",
	}, " ")
	got := BoundedSummary(input, 512)
	for _, secret := range []string{"hunter2", "json-secret", "access", "signed", "provider-secret", "bearer-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitized text contains %q: %s", secret, got)
		}
	}
	if strings.Contains(got, "\n") || !strings.Contains(got, "visible") {
		t.Fatalf("sanitized summary = %q", got)
	}
}
