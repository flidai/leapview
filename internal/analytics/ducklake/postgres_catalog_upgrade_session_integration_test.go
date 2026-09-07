//go:build duckdb_arrow

package ducklake

import (
	"context"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/testing/extensionfixture"
)

func TestOpenPostgresCatalogUpgradeSessionLoadsOnlyAdmittedExtension(t *testing.T) {
	fixture := extensionfixture.New(t, "ducklake")
	dataPath := filepath.Join(t.TempDir(), "lake-data")
	tempDir := filepath.Join(t.TempDir(), "duckdb-tmp")
	bootstrapCalls := 0
	session, err := OpenPostgresCatalogUpgradeSession(t.Context(), PostgresCatalogUpgradeSessionConfig{
		DataPath: dataPath, TempDir: tempDir, MemoryMaxBytes: 128 << 20, TempMaxBytes: 256 << 20, MaxThreads: 1,
		ExtensionAdmission: fixture.Admission,
		CredentialBootstrap: func(ctx context.Context, execer driver.ExecerContext) error {
			bootstrapCalls++
			_, err := execer.ExecContext(ctx, "SET threads = 99", nil)
			return err
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if bootstrapCalls != 1 {
		t.Fatalf("credential bootstrap calls = %d, want 1", bootstrapCalls)
	}
	var loaded bool
	if err := session.Conn().QueryRowContext(t.Context(), "SELECT loaded FROM duckdb_extensions() WHERE extension_name = 'ducklake'").Scan(&loaded); err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("admitted DuckLake extension was not loaded")
	}
	var attached int
	if err := session.Conn().QueryRowContext(t.Context(), "SELECT count(*) FROM duckdb_databases() WHERE database_name = 'lake'").Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if attached != 0 {
		t.Fatalf("upgrade session attached %d lake catalogs", attached)
	}
	var memoryLimit string
	if err := session.Conn().QueryRowContext(t.Context(), "SELECT current_setting('memory_limit')").Scan(&memoryLimit); err != nil {
		t.Fatal(err)
	}
	if memoryLimit == "" {
		t.Fatal("memory limit was not applied")
	}
	var threads int
	if err := session.Conn().QueryRowContext(t.Context(), "SELECT current_setting('threads')").Scan(&threads); err != nil {
		t.Fatal(err)
	}
	if threads != 1 {
		t.Fatalf("credential bootstrap weakened thread bound to %d", threads)
	}
	var locked bool
	if err := session.Conn().QueryRowContext(t.Context(), "SELECT current_setting('lock_configuration')").Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("upgrade session configuration was not locked")
	}
	rows, err := session.Conn().QueryContext(t.Context(), "SELECT unnest(current_setting('allowed_directories'))")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	allowed := map[string]bool{}
	for rows.Next() {
		var directory string
		if err := rows.Scan(&directory); err != nil {
			t.Fatal(err)
		}
		allowed[strings.TrimSuffix(filepath.Clean(directory), string(filepath.Separator))] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !allowed[dataPath] || !allowed[tempDir] {
		t.Fatalf("canonical allowed directories = %#v, want %q and %q", allowed, dataPath, tempDir)
	}
}
