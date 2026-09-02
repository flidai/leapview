package postgresducklake

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

type postgresExtensionAdmission struct{}

func (postgresExtensionAdmission) AdmitExtension(context.Context, string) (extension.AdmittedExtension, error) {
	return extension.AdmittedExtension{
		Name: "postgres", Identity: "fixture/postgres", Version: "fixture", Platform: "linux_amd64",
		Path:   "/opt/leapview/extensions/postgres_scanner.duckdb_extension",
		Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}, nil
}

type queryEchoingExecer struct{ calls int }

func (e *queryEchoingExecer) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	e.calls++
	if e.calls == 1 {
		return driver.RowsAffected(0), nil
	}
	return nil, fmt.Errorf("executor rejected SQL: %s", query)
}

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

func TestCredentialBootstrapSanitizesCredentialStatementErrors(t *testing.T) {
	const (
		password = "super-secret-password"
		url      = "postgres://catalog_user:" + password + "@db/leapview_ducklake?sslmode=require"
	)
	bootstrap, err := NewCredentialBootstrap(CredentialConfig{
		PostgresURL: url, ExtensionAdmission: postgresExtensionAdmission{},
	})
	if err != nil {
		t.Fatal(err)
	}
	execer := &queryEchoingExecer{}
	err = bootstrap(t.Context(), execer)
	if err == nil {
		t.Fatal("credential bootstrap unexpectedly succeeded")
	}
	if got := err.Error(); got != "create temporary PostgreSQL DuckDB secret" {
		t.Fatalf("credential error = %q, want sanitized context", got)
	}
	for _, secret := range []string{password, url, "CREATE OR REPLACE TEMPORARY SECRET", "PASSWORD"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("credential error contains sensitive SQL material %q: %q", secret, err)
		}
	}
}
