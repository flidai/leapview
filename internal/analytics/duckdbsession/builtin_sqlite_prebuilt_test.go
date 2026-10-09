//go:build leapview_static_sqlite && !duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func TestSQLiteBuiltinVerifierRejectsUnmodifiedPrebuiltEngine(t *testing.T) {
	builtin, ok := extension.CompiledBuiltin("sqlite", "linux_amd64")
	if !ok {
		t.Fatal("tagged registry missing")
	}
	identity := extension.Identity{Builtin: true, Name: "sqlite", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: builtin.Platform, Digest: builtin.Digest(), SupportProfile: "test"}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !errors.Is(VerifyCompiledBuiltin(context.Background(), db, identity), extension.ErrExtensionIntegrity) {
		t.Fatal("unmodified engine accepted compiled SQLite evidence")
	}
}
