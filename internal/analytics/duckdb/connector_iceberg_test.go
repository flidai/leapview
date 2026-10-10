package duckdb

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/flidai/leapview/internal/extension"
)

// Use the retained upstream manifest/Avro/Parquet fixture in fresh admitted
// sessions. The native build adds the exact compiled wrapper identity check;
// ordinary builds also exercise the workload against the signed control.
func TestNativeIcebergSnapshotsAndFreshSession(t *testing.T) {
	fixture, err := filepath.Abs("testdata/iceberg")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	// These retained manifests contain relative data paths.
	t.Chdir(root)
	path := "data/persistent/case_sensitive_names/default.db/case_sensitive_names/metadata/00001-a7a3a44c-4aac-4619-bebd-11be37b27351.metadata.json"
	for attempt := 0; attempt < 2; attempt++ {
		db := openNativeConnectorDB(t, "avro", "iceberg")
		if builtin, compiled := extension.CompiledBuiltin("iceberg", runtime.GOOS+"_"+runtime.GOARCH); compiled {
			var registered int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='iceberg' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version=?", builtin.SourceRevision).Scan(&registered); err != nil || registered != 1 {
				t.Fatalf("selected Iceberg registration count=%d: %v", registered, err)
			}
		}
		for _, suffix := range []string{"", ", snapshot_from_id=5298355539581857556"} {
			rows, err := db.QueryContext(t.Context(), "SELECT * FROM iceberg_scan('"+SQLString(path)+"'"+suffix+") ORDER BY ALL")
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for rows.Next() {
				var id int
				var value string
				if err := rows.Scan(&id, &value); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				count++
				if id != count || value != fmt.Sprintf("user_%d", count) {
					_ = rows.Close()
					t.Fatalf("Iceberg snapshot row %d changed: %d %q", count, id, value)
				}
			}
			if rows.Err() != nil || count != 3 {
				_ = rows.Close()
				t.Fatalf("Iceberg snapshot count=%d: %v", count, rows.Err())
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(t.Context(), "SELECT * FROM iceberg_scan('"+SQLString(path)+"', snapshot_from_id=0)"); err == nil {
			t.Fatal("missing Iceberg snapshot accepted")
		}
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM iceberg_scan('"+SQLString(path)+"')").Scan(&count); err != nil || count != 3 {
			t.Fatalf("Iceberg reader did not recover after absent snapshot: count=%d error=%v", count, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
