package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedGooseBaselineIsTheOnlyImmutableMigration(t *testing.T) {
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	var sqlFiles []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sql") {
			sqlFiles = append(sqlFiles, entry.Name())
		}
	}
	if len(sqlFiles) != 1 || sqlFiles[0] != "001_control_plane.sql" {
		t.Fatalf("embedded Goose migrations = %v", sqlFiles)
	}
	contents, err := fs.ReadFile(MigrationFS(), sqlFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	if got, want := hex.EncodeToString(sum[:]), "65b76c91366e2d4903b75d3cf722453890029328f4708aa1dbf41c3afcfb9106"; got != want {
		t.Fatalf("immutable Goose baseline digest = %s, want %s", got, want)
	}
	text := string(contents)
	for _, required := range []string{"-- +goose Up", "SET LOCAL ROLE leapview_control_owner", "CREATE TABLE IF NOT EXISTS event.event_log"} {
		if !strings.Contains(text, required) {
			t.Errorf("Goose baseline missing %q", required)
		}
	}
	for _, forbidden := range []string{"platform.schema_revision", "watermill", "cache.cache_l3", "CREATE SCHEMA IF NOT EXISTS cache"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("Goose baseline retains removed contract %q", forbidden)
		}
	}
}

func TestGooseEntryPointsRejectNilDatabase(t *testing.T) {
	if _, err := NewProvider(nil); err == nil {
		t.Fatal("NewProvider(nil) unexpectedly succeeded")
	}
	if err := ApplyGoose(t.Context(), nil); err == nil {
		t.Fatal("ApplyGoose(nil) unexpectedly succeeded")
	}
	if err := VerifyGoose(t.Context(), nil); err == nil {
		t.Fatal("VerifyGoose(nil) unexpectedly succeeded")
	}
	if err := ReconcileRolePolicy(t.Context(), nil, "SELECT 1"); err == nil {
		t.Fatal("ReconcileRolePolicy(nil) unexpectedly succeeded")
	}
}
