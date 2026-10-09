package duckdb

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

// Compiling option SQL alone does not establish that the shipped reader
// defaults bind to the native function or preserve whole-file content.
func TestNativeManagedPathReaderDefaults(t *testing.T) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	root := t.TempDir()
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": {Kind: "managed", Root: root}}}
	text := "id\tvalue\n1\tx\n"
	blob := []byte{0, 1, 255, 10}
	for name, content := range map[string][]byte{
		"fixture.csv":  []byte("id,value\n1,x\n"),
		"fixture.json": []byte(`[{"id":1,"value":"x"}]`),
		"fixture.text": []byte(text),
		"fixture.blob": blob,
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), "COPY (SELECT 1 AS id, 'x' AS value) TO '"+SQLString(filepath.Join(root, "fixture.parquet"))+"' (FORMAT PARQUET)"); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"csv", "json", "parquet", "text", "blob"} {
		t.Run(format, func(t *testing.T) {
			location := testPathLocation(format, "fixture."+format)
			if format == "csv" {
				location = testCSVPathLocationWithHeader("fixture.csv", true)
			}
			relation, err := SourceRelation(model, semanticmodel.Source{Connection: "local", Path: "fixture." + format, Format: format, EffectivePathLocation: location})
			if err != nil {
				t.Fatal(err)
			}
			switch format {
			case "text":
				var got string
				if err := db.QueryRowContext(t.Context(), "SELECT content FROM ("+relation+")").Scan(&got); err != nil {
					t.Fatalf("native relation %s: %v", relation, err)
				}
				if got != text {
					t.Fatalf("text content = %q, want %q", got, text)
				}
			case "blob":
				var got []byte
				if err := db.QueryRowContext(t.Context(), "SELECT content FROM ("+relation+")").Scan(&got); err != nil {
					t.Fatalf("native relation %s: %v", relation, err)
				}
				if !bytes.Equal(got, blob) {
					t.Fatalf("blob content = %v, want %v", got, blob)
				}
			default:
				var id int
				var value string
				if err := db.QueryRowContext(t.Context(), "SELECT id, value FROM ("+relation+")").Scan(&id, &value); err != nil {
					t.Fatalf("native relation %s: %v", relation, err)
				}
				if id != 1 || value != "x" {
					t.Fatalf("native row = (%d, %q), want (1, x)", id, value)
				}
			}
		})
	}
}
