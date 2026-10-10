//go:build leapview_static_lance && duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func TestCompiledLanceWritesAndReadsWithoutExtensionFiles(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var platform string
	if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
		t.Fatal(err)
	}
	builtin, ok := extension.CompiledBuiltin("lance", platform)
	if !ok {
		t.Fatalf("unsupported compiled platform %q", platform)
	}
	identity := extension.Identity{Builtin: true, Name: "lance", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Digest: builtin.Digest(), SupportProfile: "test"}
	if err := VerifyCompiledBuiltin(ctx, db, identity); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sample.lance")
	quoted := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	if _, err := db.ExecContext(ctx, "COPY (SELECT 10::BIGINT AS id UNION ALL SELECT 20::BIGINT) TO "+quoted+" (FORMAT lance)"); err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := db.QueryRowContext(ctx, "SELECT sum(id) FROM "+quoted).Scan(&sum); err != nil || sum != 30 {
		t.Fatalf("Lance readback = %d, error = %v", sum, err)
	}
	// Each independent runtime must load the compiled implementation into its
	// own session; a packaging verifier's earlier load is not inherited.
	second, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := VerifyCompiledBuiltin(ctx, second, identity); err != nil {
		t.Fatal(err)
	}
	if err := second.QueryRowContext(ctx, "SELECT sum(id) FROM "+quoted).Scan(&sum); err != nil || sum != 30 {
		t.Fatalf("independent Lance session readback = %d, error = %v", sum, err)
	}
}
