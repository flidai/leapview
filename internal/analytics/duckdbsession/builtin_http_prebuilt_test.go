//go:build leapview_static_http && !duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func TestHTTPBuiltinsRejectUnmodifiedPrebuiltEngine(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"httpfs", "quack"} {
		b, ok := extension.CompiledBuiltin(name, "linux_amd64")
		if !ok {
			t.Fatal("missing tagged registry")
		}
		id := extension.Identity{Builtin: true, Name: name, DuckDBVersion: b.DuckDBVersion, ExtensionVersion: b.SourceRevision, GOOS: "linux", GOARCH: "amd64", Platform: b.Platform, Digest: b.Digest(), SupportProfile: "test"}
		if !errors.Is(VerifyCompiledBuiltin(context.Background(), db, id), extension.ErrExtensionIntegrity) {
			t.Fatalf("unmodified engine accepted compiled %s descriptor", name)
		}
	}
}
