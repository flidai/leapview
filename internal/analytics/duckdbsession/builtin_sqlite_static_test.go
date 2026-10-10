//go:build leapview_static_sqlite && duckdb_use_static_lib

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

func TestCompiledSQLiteWritesAndReadsWithoutExtensionFiles(t *testing.T) {
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
	builtin, ok := extension.CompiledBuiltin("sqlite", platform)
	if !ok {
		t.Fatalf("unsupported compiled platform %q", platform)
	}
	identity := extension.Identity{Builtin: true, Name: "sqlite", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Digest: builtin.Digest(), SupportProfile: "test"}
	if err := VerifyCompiledBuiltin(ctx, db, identity); err != nil {
		t.Fatal(err)
	}
	path := "'" + strings.ReplaceAll(filepath.Join(t.TempDir(), "sample.sqlite"), "'", "''") + "'"
	for _, statement := range []string{"ATTACH " + path + " AS saved (TYPE SQLITE)", "CREATE TABLE saved.sample(id BIGINT)", "INSERT INTO saved.sample VALUES(10),(20)", "DETACH saved"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	second, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := VerifyCompiledBuiltin(ctx, second, identity); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ExecContext(ctx, "ATTACH "+path+" AS reopened (TYPE SQLITE, READ_ONLY)"); err != nil {
		t.Fatal(err)
	}
	// sqlite_query retains its SQLite transaction connection between binding
	// and execution. Keep that connection alive across duckdb-go's separate
	// prepare/execute calls; autocommit can release it after preparation.
	identityTx, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer identityTx.Rollback()
	var sqliteVersion, sqliteSourceID string
	if err := identityTx.QueryRowContext(ctx, "SELECT * FROM sqlite_query('reopened', 'SELECT sqlite_version(), sqlite_source_id()')").Scan(&sqliteVersion, &sqliteSourceID); err != nil || sqliteVersion != "3.53.4" || sqliteSourceID != builtin.SQLiteSourceID {
		t.Fatalf("linked SQLite identity = %q / %q, error = %v", sqliteVersion, sqliteSourceID, err)
	}
	if err := identityTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var sum int64
	if err := second.QueryRowContext(ctx, "SELECT sum(id) FROM reopened.sample").Scan(&sum); err != nil || sum != 30 {
		t.Fatalf("independent SQLite session readback = %d, error = %v", sum, err)
	}
}
