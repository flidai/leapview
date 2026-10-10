//go:build leapview_static_delta && duckdb_use_static_lib

package duckdb

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Reuse the canonical protocol/schema/Parquet fixture. A second transaction
// removes its first file and adds a replacement, exercising both snapshot
// selection and fresh-session readers through the admitted compiled extension.
func TestNativeDeltaSnapshotsAndFreshSession(t *testing.T) {
	root := filepath.Join(t.TempDir(), "table.delta")
	for attempt := 0; attempt < 2; attempt++ {
		db := openNativeConnectorDB(t, "delta")
		var registered int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='delta' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version='45c40878601b54b4188b09e08732fe0d576ad222'").Scan(&registered); err != nil || registered != 1 {
			t.Fatalf("selected Delta registration count=%d: %v", registered, err)
		}
		if attempt == 0 {
			createNativeDeltaFixture(t, db, root)
			path := filepath.Join(root, "replacement.parquet")
			if _, err := db.ExecContext(t.Context(), "COPY (SELECT 2::BIGINT AS id, 'replacement' AS value) TO '"+SQLString(path)+"' (FORMAT PARQUET)"); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(filepath.Join(root, "_delta_log", "00000000000000000001.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range []any{
				map[string]any{"remove": map[string]any{"path": "data.parquet", "deletionTimestamp": 2, "dataChange": true}},
				map[string]any{"add": map[string]any{"path": "replacement.parquet", "partitionValues": map[string]any{}, "size": info.Size(), "modificationTime": 2, "dataChange": true}},
			} {
				if err := json.NewEncoder(file).Encode(action); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			suffix, value string
			id            int
		}{{"", "replacement", 2}, {", version=0", "x", 1}, {", version=1", "replacement", 2}} {
			var id int
			var value string
			rows, err := db.QueryContext(t.Context(), "SELECT id, value FROM delta_scan('"+SQLString(root)+"'"+tc.suffix+")")
			if err != nil {
				t.Fatal(err)
			}
			if !rows.Next() {
				_ = rows.Close()
				t.Fatalf("Delta snapshot empty: %s", tc.suffix)
			}
			if err := rows.Scan(&id, &value); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			if rows.Next() || rows.Err() != nil || id != tc.id || value != tc.value {
				_ = rows.Close()
				t.Fatalf("Delta snapshot %s changed: %d %q, want %d %q", tc.suffix, id, value, tc.id, tc.value)
			}
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(t.Context(), "SELECT * FROM delta_scan('"+SQLString(root)+"', version=2)"); err == nil {
			t.Fatal("missing Delta snapshot accepted")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
