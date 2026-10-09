//go:build leapview_static_lance && !duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

// A compile flag is insufficient: the conventional engine must still refuse
// a manifest that claims the patched static Lance is installed.
func TestBuiltinVerifierRejectsUnmodifiedPrebuiltEngine(t *testing.T) {
	builtin, ok := extension.CompiledBuiltin("lance", "linux_amd64")
	if !ok {
		t.Fatal("tagged registry missing")
	}
	identity := extension.Identity{Builtin: true, Name: "lance", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: builtin.Platform, Digest: builtin.Digest(), SupportProfile: "test"}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !errors.Is(VerifyCompiledBuiltin(context.Background(), db, identity), extension.ErrExtensionIntegrity) {
		t.Fatal("unmodified engine accepted compiled-Lance evidence")
	}
}
