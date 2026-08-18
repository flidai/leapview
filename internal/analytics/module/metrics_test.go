package module

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	extensionsupply "github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
	"github.com/prometheus/client_golang/prometheus"
)

type moduleTestExtensionAdmission struct{ admitted extension.AdmittedExtension }

var _ extension.Admission = moduleTestExtensionAdmission{}
var _ extension.Preparation = moduleTestExtensionAdmission{}

func (a moduleTestExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	if name != a.admitted.Name {
		return extension.AdmittedExtension{}, fmt.Errorf("test extension %q was not admitted", name)
	}
	return a.admitted, nil
}

func (a moduleTestExtensionAdmission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	evidence := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		admitted, err := a.AdmitExtension(ctx, name)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, admitted.Evidence())
	}
	return evidence, nil
}

func newModuleTestExtensionAdmission(t *testing.T, name string) extension.Admission {
	t.Helper()
	version, platform := moduleTestRuntimeTarget(t)
	setupRoot := t.TempDir()
	sourcePath := findModuleTestExtension(name, version, platform)
	if sourcePath == "" {
		installModuleTestExtension(t, name, setupRoot)
		sourcePath = findModuleTestExtensionInRoot(setupRoot, name, version, platform)
	}
	if sourcePath == "" {
		t.Fatalf("reviewed local test extension %q is unavailable for DuckDB %s/%s", name, version, platform)
	}
	contents, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read test extension %q: %v", name, err)
	}
	ownedPath := filepath.Join(setupRoot, name+".duckdb_extension")
	if err := os.WriteFile(ownedPath, contents, 0o600); err != nil {
		t.Fatalf("stage test extension %q: %v", name, err)
	}
	digest := sha256.Sum256(contents)
	identity := extension.Identity{DuckDBVersion: version, ExtensionVersion: "test-fixture", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Name: name, Digest: "sha256:" + hex.EncodeToString(digest[:]), SupportProfile: "test-fixture"}
	canonical, err := identity.Canonical()
	if err != nil {
		t.Fatalf("canonicalize test extension %q: %v", name, err)
	}
	return moduleTestExtensionAdmission{admitted: extension.AdmittedExtension{Name: name, Identity: canonical, Version: "test-fixture", ExtensionVersion: "test-fixture", DuckDBVersion: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, SupportProfile: "test-fixture", Digest: "sha256:" + hex.EncodeToString(digest[:]), Path: ownedPath, Origin: "reviewed-local-test-fixture", Provenance: "attest:module-test", Signature: "sig:module-test"}}
}

func moduleTestRuntimeTarget(t *testing.T) (string, string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB runtime probe: %v", err)
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(t.Context(), "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("read DuckDB runtime version: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "PRAGMA platform").Scan(&platform); err != nil {
		t.Fatalf("read DuckDB runtime platform: %v", err)
	}
	if version != extensionsupply.CurrentDuckDBVersion {
		t.Fatalf("DuckDB runtime = %q, want pinned %q", version, extensionsupply.CurrentDuckDBVersion)
	}
	return strings.TrimSpace(version), strings.TrimSpace(platform)
}

func findModuleTestExtension(name, version, platform string) string {
	roots := []string{}
	if configured := strings.TrimSpace(os.Getenv("DUCKDB_EXTENSION_DIRECTORY")); configured != "" {
		roots = append(roots, configured)
	} else {
		if home, err := os.UserHomeDir(); err == nil {
			roots = append(roots, filepath.Join(home, ".duckdb", "extensions"))
		}
	}
	for _, root := range roots {
		if found := findModuleTestExtensionInRoot(root, name, version, platform); found != "" {
			return found
		}
	}
	return ""
}

func findModuleTestExtensionInRoot(root, name, version, platform string) string {
	filename := name + ".duckdb_extension"
	platformDir := strings.ReplaceAll(platform, "-", "_")
	for _, path := range []string{filepath.Join(root, version, platformDir, filename), filepath.Join(root, version, platform, filename)} {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return filepath.Clean(path)
		}
	}
	return ""
}

func installModuleTestExtension(t *testing.T, name, root string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open test extension installer: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("SET extension_directory = '" + strings.ReplaceAll(root, "'", "''") + "'"); err != nil {
		t.Fatalf("set test extension directory: %v", err)
	}
	if _, err := db.Exec("INSTALL " + name + " FROM core"); err != nil {
		t.Fatalf("install test extension %q: %v", name, err)
	}
}

func TestAnalyticalCollectorUsesBoundedLabels(t *testing.T) {
	admission := newModuleTestExtensionAdmission(t, "ducklake")
	database, err := analyticsducklake.Open(context.Background(), analyticsducklake.Config{RootDir: t.TempDir(), MaxConnections: 2, ExtensionAdmission: admission})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cache, err := resultcache.New(resultcache.Limits{RuntimeEntries: 1, RuntimeBytes: 1024, NodeEntries: 1, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	registry := prometheus.NewRegistry()
	registry.MustRegister(NewCollector(database, cache))
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"leapview_arrow_result_leases":     false,
		"leapview_arrow_transient_bytes":   false,
		"leapview_duckdb_connections_open": false,
		"leapview_query_cache_entries":     false,
		"leapview_query_cache_store_total": false,
	}
	for _, family := range families {
		if _, ok := want[family.GetName()]; !ok {
			continue
		}
		want[family.GetName()] = true
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				switch label.GetName() {
				case "workspace", "runtime", "generation", "operation", "request":
					t.Fatalf("unbounded label %q", label.GetName())
				}
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("metric %s missing", name)
		}
	}
}
