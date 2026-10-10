//go:build leapview_static_avro && duckdb_use_static_lib

package duckdb

import (
	"path/filepath"
	"testing"
)

// Iceberg requires Avro as an admitted runtime dependency; there is no public
// Avro path-source format. Exercise its reader in independent admitted sessions.
// Codec-specific compression is additionally exercised by the static C consumer;
// this pinned wrapper's COPY writer does not expose compression options.
func TestNativeAvroReadAndFreshSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.avro")
	for attempt := 0; attempt < 2; attempt++ {
		db := openNativeConnectorDB(t, "avro")
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='avro' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version='f9d590297485f0318f480372c70bdd852826e258'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("selected Avro registration count=%d: %v", count, err)
		}
		if attempt == 0 {
			if _, err := db.ExecContext(t.Context(), "COPY (SELECT 1::BIGINT AS id, 'x' AS value, 123.45::DECIMAL(18,2) AS amount, DATE '2026-10-01' AS day, TIMESTAMP '2026-10-01 12:34:56.123456' AS instant) TO '"+SQLString(path)+"' (FORMAT AVRO, FIELD_IDS {id: 1, value: 2, amount: 3, day: 4, instant: 5})"); err != nil {
				t.Fatal(err)
			}
		}
		relation := "SELECT * FROM read_avro('" + SQLString(path) + "')"
		columns, err := describeRelationSchema(t.Context(), db, relation)
		if err != nil {
			t.Fatal(err)
		}
		if len(columns) != 5 || columns[0].Name != "id" || columns[1].Name != "value" || columns[2].Name != "amount" || columns[3].Name != "day" || columns[4].Name != "instant" {
			t.Fatalf("Avro schema changed: %#v", columns)
		}
		var id int
		var value, amount, day, instant string
		if err := db.QueryRowContext(t.Context(), "SELECT id, value, CAST(amount AS VARCHAR), CAST(day AS VARCHAR), CAST(instant AS VARCHAR) FROM ("+relation+")").Scan(&id, &value, &amount, &day, &instant); err != nil {
			t.Fatal(err)
		}
		if id != 1 || value != "x" || amount != "123.45" || day != "2026-10-01" || instant != "2026-10-01 12:34:56.123456" {
			t.Fatalf("Avro logical schema changed: %q %q %q", amount, day, instant)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
