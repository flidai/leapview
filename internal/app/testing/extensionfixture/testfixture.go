// Package testfixture provides exact, test-owned DuckDB extension admissions.
// It is deliberately separate from production composition: tests must stage
// immutable artifacts and pass their admission explicitly, just as runtime
// startup does with the reviewed deployment supply.
package extensionfixture

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/extension"
)

const pinnedDuckDBVersion = "v1.5.4"

// Fixture is an exact admission plus a packaged supply document suitable for
// app.Build tests. All paths are test-owned and immutable for the test run.
type Fixture struct {
	Admission    extension.Admission
	SupplyPath   string
	SupplySHA256 string
	CacheDir     string
}

type manifestDocument struct {
	Version        int                `json:"version"`
	DuckDBVersion  string             `json:"duckdbVersion"`
	GOOS           string             `json:"goos"`
	GOARCH         string             `json:"goarch"`
	Platform       string             `json:"platform"`
	SupportProfile string             `json:"supportProfile"`
	Origins        []manifestOrigin   `json:"origins"`
	Artifacts      []manifestArtifact `json:"artifacts"`
}

type manifestOrigin struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Reviewed bool   `json:"reviewed"`
}

type manifestArtifact struct {
	Identity   extension.Identity `json:"identity"`
	Origins    []string           `json:"origins"`
	Provenance string             `json:"provenance"`
	Signature  string             `json:"signature"`
}

type admission struct {
	paths map[string]extension.AdmittedExtension
}

var _ extension.Admission = admission{}
var _ extension.Preparation = admission{}

func (a admission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	value, ok := a.paths[name]
	if !ok {
		return extension.AdmittedExtension{}, fmt.Errorf("test extension %q was not admitted", name)
	}
	return value, nil
}

func (a admission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	result := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		value, err := a.AdmitExtension(ctx, name)
		if err != nil {
			return nil, err
		}
		result = append(result, value.Evidence())
	}
	return result, nil
}

// New creates exact, test-owned admissions for names. It reuses a reviewed
// local extension when available and otherwise installs into a private test
// directory; it never relies on DuckDB's implicit extension search path.
func New(t testing.TB, names ...string) Fixture {
	t.Helper()
	version, platform := runtimeTarget(t)
	root := t.TempDir()
	paths := make(map[string]extension.AdmittedExtension, len(names))
	for _, name := range names {
		source := findExtension(name, version, platform)
		if source == "" {
			installExtension(t, name, root)
			source = findExtensionInRoot(root, name, version, platform)
		}
		if source == "" {
			t.Fatalf("reviewed local extension %q is unavailable for DuckDB %s/%s", name, version, platform)
		}
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read test extension %q: %v", name, err)
		}
		digest := sha256.Sum256(contents)
		digestValue := "sha256:" + hex.EncodeToString(digest[:])
		pathName := name
		if name == "sqlite" {
			pathName = "sqlite_scanner"
		}
		path := filepath.Join(root, pathName+".duckdb_extension")
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatalf("stage test extension %q: %v", name, err)
		}
		originPath := filepath.Join(root, name+"-test-fixture-"+platform+".duckdb_extension")
		if err := os.WriteFile(originPath, contents, 0o600); err != nil {
			t.Fatalf("stage packaged test extension %q: %v", name, err)
		}
		identity, err := (extension.Identity{
			DuckDBVersion: version, ExtensionVersion: "test-fixture", GOOS: runtime.GOOS,
			GOARCH: runtime.GOARCH, Platform: platform, Name: name, Digest: digestValue,
			SupportProfile: "test-fixture",
		}).Canonical()
		if err != nil {
			t.Fatalf("canonicalize test extension %q: %v", name, err)
		}
		paths[name] = extension.AdmittedExtension{
			Name: name, Identity: identity, Version: "test-fixture", ExtensionVersion: "test-fixture",
			DuckDBVersion: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform,
			SupportProfile: "test-fixture", Digest: digestValue, Path: path,
			Origin: "reviewed-local-test-fixture", Provenance: "attest:test-fixture", Signature: "sig:test-fixture",
		}
	}
	fixture := Fixture{Admission: admission{paths: paths}, CacheDir: filepath.Join(root, "cache")}
	if len(paths) > 0 {
		manifest := manifestDocument{Version: 1, DuckDBVersion: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, SupportProfile: "test-fixture", Origins: []manifestOrigin{{ID: "reviewed-local-test-fixture", Path: root, Reviewed: true}}}
		for _, name := range sortedNames(paths) {
			value := paths[name]
			identity := extension.Identity{DuckDBVersion: version, ExtensionVersion: "test-fixture", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Name: name, Digest: value.Digest, SupportProfile: "test-fixture"}
			manifest.Artifacts = append(manifest.Artifacts, manifestArtifact{Identity: identity, Origins: []string{"reviewed-local-test-fixture"}, Provenance: value.Provenance, Signature: value.Signature})
		}
		payload, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("marshal test extension supply: %v", err)
		}
		fixture.SupplyPath = filepath.Join(root, "extension-supply.json")
		if err := os.WriteFile(fixture.SupplyPath, payload, 0o600); err != nil {
			t.Fatalf("write test extension supply: %v", err)
		}
		sum := sha256.Sum256(payload)
		fixture.SupplySHA256 = hex.EncodeToString(sum[:])
	}
	return fixture
}

func sortedNames(values map[string]extension.AdmittedExtension) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func runtimeTarget(t testing.TB) (string, string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB runtime probe: %v", err)
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRow("SELECT version()").Scan(&version); err != nil {
		t.Fatalf("read DuckDB runtime version: %v", err)
	}
	if err := db.QueryRow("PRAGMA platform").Scan(&platform); err != nil {
		t.Fatalf("read DuckDB runtime platform: %v", err)
	}
	if version != pinnedDuckDBVersion {
		t.Fatalf("DuckDB runtime = %q, want pinned %q", version, pinnedDuckDBVersion)
	}
	return strings.TrimSpace(version), strings.TrimSpace(platform)
}

func extensionFilename(name string) string {
	if name == "sqlite" {
		return "sqlite_scanner.duckdb_extension"
	}
	return name + ".duckdb_extension"
}

func findExtension(name, version, platform string) string {
	if configured := strings.TrimSpace(os.Getenv("DUCKDB_EXTENSION_DIRECTORY")); configured != "" {
		return findExtensionInRoot(configured, name, version, platform)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return findExtensionInRoot(filepath.Join(home, ".duckdb", "extensions"), name, version, platform)
	}
	return ""
}

func findExtensionInRoot(root, name, version, platform string) string {
	filename := extensionFilename(name)
	root = filepath.Clean(root)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	var candidates []string
	for _, candidate := range []string{
		filepath.Join(root, version, strings.ReplaceAll(platform, "-", "_"), filename),
		filepath.Join(root, version, platform, filename),
		filepath.Join(root, runtime.GOOS+"_"+runtime.GOARCH, filename),
		filepath.Join(root, filename),
	} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || entry.Name() != filename {
				return nil
			}
			pathSlash := filepath.ToSlash(path)
			if strings.Contains(pathSlash, "/"+version+"/") && (strings.Contains(pathSlash, "/"+platform+"/") || strings.Contains(pathSlash, "/"+strings.ReplaceAll(platform, "-", "_")+"/")) {
				if entry.Type().IsRegular() {
					candidates = append(candidates, path)
				}
			}
			return nil
		})
	}
	if len(candidates) == 0 {
		return ""
	}
	sort.Strings(candidates)
	return candidates[0]
}

func installExtension(t testing.TB, name, root string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB extension installer: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("SET extension_directory = '" + strings.ReplaceAll(root, "'", "''") + "'"); err != nil {
		t.Fatalf("set test extension directory: %v", err)
	}
	if _, err := db.Exec("INSTALL " + name + " FROM core"); err != nil {
		t.Fatalf("install test extension %q: %v", name, err)
	}
}
