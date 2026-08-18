//go:build duckdb_arrow

package ducklake

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
)

// TestCurrentFileClosureIncludesEveryRetainedDeleteFile exercises the same
// SQL contract used by DuckLake's GetFilesForTable implementation: one row
// per retained data/delete-file pair at an explicitly pinned snapshot.  A
// GC mark that only retained the data files (or only the last delete file)
// could not safely read this snapshot after sweep.
func TestCurrentFileClosureIncludesEveryRetainedDeleteFile(t *testing.T) {
	ctx := context.Background()
	env, err := Open(ctx, Config{RootDir: t.TempDir(), MaxConnections: 1})
	if extensionUnavailable(err) {
		t.Skipf("ducklake extension unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()

	if _, err := env.Commit(ctx, "create", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model;
CREATE TABLE model.orders(id INTEGER, label VARCHAR)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Separate insert commits force three physical data files. DuckLake's
	// list_files contract then returns one delete-file reference per affected
	// data file, which is the closure shape GC must preserve.
	for base := 0; base < 30; base += 10 {
		if _, err := env.Commit(ctx, fmt.Sprintf("insert-%d", base), nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.orders SELECT range + ?, 'retained' FROM range(0, 10)", base)
			return err
		}); err != nil {
			t.Fatalf("insert base %d: %v", base, err)
		}
	}
	// Keep each positional delete as a retained delete file instead of letting
	// auto-compaction rewrite the data file between commits.
	if err := env.Exec(ctx, "CALL ducklake_set_option('lake', 'auto_compact', false, schema => 'model', table_name => 'orders')"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{1, 11, 21} {
		if _, err := env.Commit(ctx, fmt.Sprintf("delete-%d", id), nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM model.orders WHERE id = ?", id)
			return err
		}); err != nil {
			t.Fatalf("delete id %d: %v", id, err)
		}
	}

	snapshot, tables, closure, err := env.CurrentFileClosure(ctx, "retained")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot <= 0 {
		t.Fatalf("current snapshot = %d, want positive", snapshot)
	}
	if len(tables) != 1 || tables[0] != (BaseTable{Schema: "model", Table: "orders"}) {
		t.Fatalf("visible tables = %#v, want model.orders", tables)
	}
	if len(closure.DataFiles) == 0 {
		t.Fatalf("closure = %#v, want retained data file", closure)
	}
	if len(closure.DeleteFiles) < 3 {
		t.Fatalf("closure = %#v, want all three retained delete files", closure)
	}

	// Compare both production paths against the direct DuckLake list_files
	// result at the exact snapshot returned by CurrentFileClosure.
	rows, err := env.sqlDB().QueryContext(ctx, "SELECT data_file, delete_file FROM ducklake_list_files(?, ?, schema => ?, snapshot_version => ?)", catalogAlias, "orders", "model", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	directData, directDelete := map[string]struct{}{}, map[string]struct{}{}
	for rows.Next() {
		var dataFile, deleteFile sql.NullString
		if err := rows.Scan(&dataFile, &deleteFile); err != nil {
			t.Fatal(err)
		}
		if dataFile.Valid {
			directData[dataFile.String] = struct{}{}
		}
		if deleteFile.Valid {
			directDelete[deleteFile.String] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !containsEveryPath(closure.DataFiles, directData) || !containsEveryPath(closure.DeleteFiles, directDelete) {
		t.Fatalf("CurrentFileClosure=%#v omitted direct DuckLake refs data=%v delete=%v", closure, directData, directDelete)
	}
	for _, path := range closure.DataFiles {
		if _, ok := directData[path]; !ok {
			t.Fatalf("CurrentFileClosure data path %q is not in direct DuckLake refs %v", path, directData)
		}
	}
	for _, path := range closure.DeleteFiles {
		if _, ok := directDelete[path]; !ok {
			t.Fatalf("CurrentFileClosure delete path %q is not in direct DuckLake refs %v", path, directDelete)
		}
	}

	set, err := env.CurrentFileSet(ctx, "retained", "model", "orders")
	if err != nil {
		t.Fatal(err)
	}
	if !samePathSet(closure.DataFiles, set.DataFiles) || !samePathSet(closure.DeleteFiles, set.DeleteFiles) {
		t.Fatalf("CurrentFileClosure=%#v and CurrentFileSet=%#v disagree", closure, set)
	}
	relation, err := QualifiedSnapshotRelation(snapshot, "orders")
	if err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := env.sqlDB().QueryRowContext(ctx, "SELECT count(*) FROM "+relation).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 27 {
		t.Fatalf("retained snapshot row count = %d, want 27", retained)
	}
}

func containsEveryPath(paths []string, want map[string]struct{}) bool {
	for path := range want {
		found := false
		for _, got := range paths {
			if got == path {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func samePathSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet, rightSet := map[string]struct{}{}, map[string]struct{}{}
	for _, path := range left {
		leftSet[path] = struct{}{}
	}
	for _, path := range right {
		rightSet[path] = struct{}{}
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for path := range leftSet {
		if _, ok := rightSet[path]; !ok {
			return false
		}
	}
	return true
}
