package duckdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

func TestNativeManagedExtensionReaderOptions(t *testing.T) {
	fixture, err := filepath.Abs("testdata/iceberg")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	// The upstream fixture's manifests deliberately contain relative paths.
	t.Chdir(root)
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{"SET threads = 2", "SET memory_limit = '256MiB'"} {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	admission := newDuckDBTestExtensionAdmission(t, "excel", "vortex", "lance", "delta", "iceberg")
	for _, name := range []string{"excel", "vortex", "lance", "delta", "iceberg"} {
		artifact, err := admission.AdmitExtension(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), loadExtensionStatement(artifact.Path)); err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
	}
	functions, err := db.QueryContext(t.Context(), "SELECT function_name, parameters::VARCHAR, parameter_types::VARCHAR FROM duckdb_functions() WHERE function_name IN ('read_xlsx', 'read_vortex', 'delta_scan', 'iceberg_scan') ORDER BY function_name, parameters::VARCHAR")
	if err != nil {
		t.Fatal(err)
	}
	for functions.Next() {
		var name, parameters, types string
		if err := functions.Scan(&name, &parameters, &types); err != nil {
			t.Fatal(err)
		}
		t.Logf("native %s parameters=%s types=%s", name, parameters, types)
	}
	if err := functions.Err(); err != nil {
		t.Fatal(err)
	}
	if err := functions.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ path, options string }{
		{"fixture.xlsx", "FORMAT xlsx, HEADER true"},
		{"fixture.vortex", "FORMAT vortex"},
		{"fixture.lance", "FORMAT lance, MODE 'overwrite'"},
	} {
		if _, err := db.ExecContext(t.Context(), "COPY (SELECT 1::BIGINT AS id, 'x' AS value) TO '"+SQLString(filepath.Join(root, fixture.path))+"' ("+fixture.options+")"); err != nil {
			t.Fatalf("write %s: %v", fixture.path, err)
		}
	}
	createNativeDeltaFixture(t, db, filepath.Join(root, "fixture.delta"))
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": {Kind: "managed", Root: root}}}
	icebergPath := "data/persistent/case_sensitive_names/default.db/case_sensitive_names/metadata/00001-a7a3a44c-4aac-4619-bebd-11be37b27351.metadata.json"
	for _, tc := range []struct{ format, path string }{
		{"excel", "fixture.xlsx"}, {"vortex", "fixture.vortex"}, {"lance", "fixture.lance"}, {"delta", "fixture.delta"}, {"iceberg", icebergPath},
	} {
		t.Run(tc.format, func(t *testing.T) {
			location := testPathLocation(tc.format, tc.path)
			query := func(location *projectcontracts.PathSourceLocation) string {
				relation, err := SourceRelation(model, semanticmodel.Source{Connection: "local", Path: tc.path, Format: tc.format, EffectivePathLocation: location})
				if err != nil {
					t.Fatal(err)
				}
				return relation
			}
			assertRows := func(relation string) {
				t.Helper()
				rows, err := db.QueryContext(t.Context(), relation+" ORDER BY ALL")
				if err != nil {
					t.Fatalf("native relation %s: %v", relation, err)
				}
				defer rows.Close()
				count := 0
				for rows.Next() {
					var id int
					var value string
					if err := rows.Scan(&id, &value); err != nil {
						t.Fatal(err)
					}
					count++
					want := "x"
					if tc.format == "iceberg" {
						want = fmt.Sprintf("user_%d", count)
					}
					if id != count || value != want {
						t.Fatalf("row = (%d, %q), want (%d, %q)", id, value, count, want)
					}
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				want := 1
				if tc.format == "iceberg" {
					want = 3
				}
				if count != want {
					t.Fatalf("row count = %d, want %d", count, want)
				}
			}
			assertRows(query(location))
			switch v := location.Value.(type) {
			case *projectcontracts.ExcelPathSourceLocation:
				sheet := "Sheet1"
				v.Options.Sheet = &sheet
			case *projectcontracts.DeltaPathSourceLocation:
				version := "0"
				v.Options = &projectcontracts.DeltaReaderOptions{Version: &version}
			case *projectcontracts.IcebergPathSourceLocation:
				snapshot := "5298355539581857556"
				v.Options = &projectcontracts.IcebergReaderOptions{Snapshot: &snapshot}
			}
			assertRows(query(location))
			if tc.format == "delta" || tc.format == "iceberg" {
				// The native parameter is UBIGINT, but Delta reserves UINT64_MAX
				// for latest and Iceberg stores signed snapshot IDs internally.
				// Preserve the exact supported upper boundary without asking Delta
				// to enumerate an enormous range of absent transaction-log files.
				var nativeID string
				if err := db.QueryRowContext(t.Context(), "SELECT CAST('9223372036854775807' AS UBIGINT)::VARCHAR").Scan(&nativeID); err != nil || nativeID != "9223372036854775807" {
					t.Fatalf("native boundary ID = %q, error = %v", nativeID, err)
				}
				ids := []string{"9223372036854775807", "0"}
				if tc.format == "delta" {
					ids = []string{"1"} // Version zero is populated; one is absent.
				}
				for _, id := range ids {
					missing := testPathLocation(tc.format, tc.path)
					switch v := missing.Value.(type) {
					case *projectcontracts.DeltaPathSourceLocation:
						v.Options = &projectcontracts.DeltaReaderOptions{Version: &id}
					case *projectcontracts.IcebergPathSourceLocation:
						v.Options = &projectcontracts.IcebergReaderOptions{Snapshot: &id}
					}
					rows, err := db.QueryContext(t.Context(), query(missing))
					if err == nil {
						_ = rows.Close()
						t.Fatalf("absent %s ID %s unexpectedly read rows", tc.format, id)
					}
					if strings.Contains(err.Error(), "Binder Error:") {
						t.Fatalf("valid %s ID %s failed native parameter binding: %v", tc.format, id, err)
					}
					t.Logf("absent %s ID %s: %v", tc.format, id, err)
				}
				for _, id := range []string{"9223372036854775808", "18446744073709551615", "18446744073709551616"} {
					invalid := testPathLocation(tc.format, tc.path)
					switch v := invalid.Value.(type) {
					case *projectcontracts.DeltaPathSourceLocation:
						v.Options = &projectcontracts.DeltaReaderOptions{Version: &id}
					case *projectcontracts.IcebergPathSourceLocation:
						v.Options = &projectcontracts.IcebergReaderOptions{Snapshot: &id}
					}
					if _, err := SourceRelation(model, semanticmodel.Source{Connection: "local", Path: tc.path, Format: tc.format, EffectivePathLocation: invalid}); err == nil {
						t.Fatalf("out-of-range %s ID %s reached native SQL", tc.format, id)
					}
				}
				assertRows(query(location)) // Failed selection leaves the reader usable.
			}
		})
	}
}

func createNativeDeltaFixture(t *testing.T, db *sql.DB, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "_delta_log"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "COPY (SELECT 1::BIGINT AS id, 'x' AS value) TO '"+SQLString(filepath.Join(root, "data.parquet"))+"' (FORMAT PARQUET)"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "data.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(root, "_delta_log", "00000000000000000000.json"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, action := range []any{
		map[string]any{"protocol": map[string]any{"minReaderVersion": 1, "minWriterVersion": 2}},
		map[string]any{"metaData": map[string]any{"id": "0099e84e-5734-4c30-a56b-e55513cbe1a1", "format": map[string]any{"provider": "parquet", "options": map[string]any{}}, "schemaString": `{"type":"struct","fields":[{"name":"id","type":"long","nullable":false,"metadata":{}},{"name":"value","type":"string","nullable":true,"metadata":{}}]}`, "partitionColumns": []string{}, "configuration": map[string]any{}, "createdTime": 1}},
		map[string]any{"add": map[string]any{"path": "data.parquet", "partitionValues": map[string]any{}, "size": info.Size(), "modificationTime": 1, "dataChange": true}},
	} {
		if err := encoder.Encode(action); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
