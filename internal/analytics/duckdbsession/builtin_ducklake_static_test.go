//go:build leapview_static_ducklake && duckdb_use_static_lib

package duckdbsession

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

func TestCompiledDuckLakeDeletionVectorsPreserveReopenedSnapshots(t *testing.T) {
	ctx := context.Background()
	open := func() *sql.DB {
		db, err := sql.Open("duckdb", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		var platform string
		if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
			t.Fatal(err)
		}
		builtin, ok := extension.CompiledBuiltin("ducklake", platform)
		if !ok {
			t.Fatal("compiled DuckLake registry missing")
		}
		identity := extension.Identity{Builtin: true, Name: "ducklake", DuckDBVersion: builtin.DuckDBVersion, ExtensionVersion: builtin.SourceRevision, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Digest: builtin.Digest(), SupportProfile: "test"}
		if err := VerifyCompiledBuiltin(ctx, db, identity); err != nil {
			t.Fatal(err)
		}
		var version string
		if err := db.QueryRowContext(ctx, "SELECT extension_version FROM duckdb_extensions() WHERE extension_name = 'ducklake'").Scan(&version); err != nil || version != builtin.SourceRevision {
			t.Fatalf("compiled DuckLake version=%q want=%q: %v", version, builtin.SourceRevision, err)
		}
		return db
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	root := t.TempDir()
	attach := "ATTACH " + quote("ducklake:"+filepath.Join(root, "catalog.duckdb")) + " AS lake (DATA_PATH " + quote(filepath.Join(root, "data")) + ", DATA_INLINING_ROW_LIMIT 0, WRITE_DELETION_VECTORS true)"
	db := open()
	for _, statement := range []string{attach, "CREATE TABLE lake.sample AS SELECT i AS id FROM range(1000) rows(i)"} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	var before int64
	if err := db.QueryRowContext(ctx, "SELECT max(snapshot_id) FROM ducklake_snapshots('lake')").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM lake.sample WHERE id % 2 = 0"); err != nil {
		t.Fatal(err)
	}
	var format string
	if err := db.QueryRowContext(ctx, "SELECT DISTINCT format FROM __ducklake_metadata_lake.ducklake_delete_file WHERE end_snapshot IS NULL").Scan(&format); err != nil || format != "puffin" {
		t.Fatalf("expected actual CRoaring-backed deletion vector, format=%q error=%v", format, err)
	}
	if _, err := db.ExecContext(ctx, "DETACH lake"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A fresh engine session must load its own compiled implementation and
	// deserialize the deletion vector, retaining the historical snapshot.
	reopened := open()
	if _, err := reopened.ExecContext(ctx, attach); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		query      string
		count, sum int64
	}{
		{"SELECT count(*), sum(id) FROM lake.sample", 500, 250000},
		{fmt.Sprintf("SELECT count(*), sum(id) FROM lake.sample AT (VERSION => %d)", before), 1000, 499500},
	} {
		var count, sum int64
		if err := reopened.QueryRowContext(ctx, check.query).Scan(&count, &sum); err != nil || count != check.count || sum != check.sum {
			t.Fatalf("snapshot readback=(%d,%d), want=(%d,%d): %v", count, sum, check.count, check.sum, err)
		}
	}
	if _, err := reopened.ExecContext(ctx, "DELETE FROM lake.sample WHERE id < 100"); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := reopened.QueryRowContext(ctx, "SELECT count(*) FROM lake.sample").Scan(&remaining); err != nil || remaining != 450 {
		t.Fatalf("merged deletion vectors readback=%d: %v", remaining, err)
	}
}
