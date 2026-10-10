//go:build leapview_static_excel && duckdb_use_static_lib

package duckdb

import (
	"path/filepath"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
)

// Exercise the canonical admitted reader against independently opened sessions,
// without permitting extension installation or falling back to a signed payload.
func TestNativeExcelSourceReadAndFreshSession(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.xlsx")
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{"local": {Kind: "managed", Root: root}}}
	for attempt := 0; attempt < 2; attempt++ {
		db := openNativeConnectorDB(t, "excel")
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_extensions() WHERE extension_name='excel' AND installed AND install_mode='STATICALLY_LINKED' AND extension_version='f4c72b5ef04a03b3a78a95b5a2ee94ba93e3178d'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("selected Excel registration count=%d: %v", count, err)
		}
		if attempt == 0 {
			if _, err := db.ExecContext(t.Context(), "COPY (SELECT 1::BIGINT AS id, 'x' AS value) TO '"+SQLString(path)+"' (FORMAT XLSX, HEADER true)"); err != nil {
				t.Fatal(err)
			}
		}
		location := testPathLocation("excel", "fixture.xlsx")
		source := semanticmodel.Source{Connection: "local", Path: "fixture.xlsx", Format: "excel", EffectivePathLocation: location}
		assertNativeConnectorRow(t, db, model, source)
		sheet := "Sheet1"
		location.Value.(*projectcontracts.ExcelPathSourceLocation).Options.Sheet = &sheet
		assertNativeConnectorRow(t, db, model, source)
		sheet = "missing_qualification_sheet"
		relation, err := SourceRelation(model, source)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.QueryContext(t.Context(), relation)
		if err == nil {
			rows.Close()
			t.Fatal("missing Excel sheet accepted")
		}
		if !strings.Contains(err.Error(), sheet) {
			t.Fatalf("unexpected missing-sheet failure: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
