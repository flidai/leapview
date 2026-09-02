package postgresducklake

import (
	"strings"
	"testing"
)

func TestCredentialBootstrapRejectsPlaintextAndMissingAdmission(t *testing.T) {
	for _, url := range []string{
		"postgres://user:secret@db/leapview_ducklake?sslmode=disable",
		"postgres://user@db/leapview_ducklake?sslmode=require",
		"postgres://user:secret@db/leapview_ducklake?application_name=leapview",
	} {
		if _, err := NewCredentialBootstrap(CredentialConfig{PostgresURL: url}); err == nil {
			t.Fatalf("unsafe credential URL accepted: %s", strings.ReplaceAll(url, "secret", "redacted"))
		}
	}
}
