//go:build leapview_static_vortex && duckdb_use_static_lib

package duckdb

import (
	"path/filepath"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Exercise the canonical admitted path reader and both ABI directions: COPY
// writes through the Rust/C++ bridge, then fresh sessions filter and read it.
func TestNativeVortexSourceReadAndFreshSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.vortex")
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": {Kind: "managed", Root: root}}}
	for attempt := 0; attempt < 2; attempt++ {
		db := openNativeConnectorDB(t, "vortex")
		var registered int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='vortex' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version='275ac230e1d9afd08926b6989ec2467f92fae6e3'").Scan(&registered); err != nil || registered != 1 {
			t.Fatalf("selected Vortex registration count=%d: %v", registered, err)
		}
		if attempt == 0 {
			if _, err := db.ExecContext(t.Context(), "COPY (SELECT i::BIGINT AS id, CASE WHEN i % 7 = 0 THEN NULL ELSE 'value-' || i::VARCHAR END AS value, (i / 100.0)::DECIMAL(18,2) AS amount FROM range(256) t(i)) TO '"+SQLString(path)+"' (FORMAT vortex)"); err != nil {
				t.Fatal(err)
			}
		}
		source := semanticmodel.Source{Connection: "local", Path: "fixture.vortex", Format: "vortex", EffectivePathLocation: testPathLocation("vortex", "fixture.vortex")}
		relation, err := SourceRelation(model, source)
		if err != nil {
			t.Fatal(err)
		}
		var count, sum, nulls int
		var amount string
		if err := db.QueryRowContext(t.Context(), "SELECT count(*), sum(id), count(*) FILTER (WHERE value IS NULL), sum(amount)::VARCHAR FROM ("+relation+") WHERE id >= 128 AND id < 192").Scan(&count, &sum, &nulls, &amount); err != nil {
			t.Fatal(err)
		}
		if count != 64 || sum != 10208 || nulls != 9 || amount != "102.08" {
			t.Fatalf("Vortex filtered values changed: count=%d sum=%d nulls=%d amount=%s", count, sum, nulls, amount)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
