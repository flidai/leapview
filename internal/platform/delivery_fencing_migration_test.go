package platform

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryFencingMigrationCreatesDurablePoolFenceAndRootRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "fencing.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()
	for _, table := range []string{"delivery_pool_fences", "delivery_writer_leases", "delivery_gc_leases", "delivery_root_registry"} {
		assertTableCount(t, ctx, store, table, 1)
	}
	var duplicate int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='delivery_pool_writer_leases'`).Scan(&duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate != 0 {
		t.Fatal("a second per-pool writer lease table was created")
	}
	for _, table := range []string{"delivery_pool_fences", "delivery_writer_leases", "delivery_gc_leases", "delivery_root_registry"} {
		rows, err := store.SQLDB().QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
		if err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		for rows.Next() {
			var cid, notNull, primary int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &primary); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			for _, forbidden := range []string{"file_membership", "table_membership", "reference_count", "data_file", "delete_file"} {
				if strings.Contains(strings.ToLower(name), forbidden) {
					rows.Close()
					t.Fatalf("forbidden membership column %q on %s", name, table)
				}
			}
		}
		rows.Close()
	}
}
