// Command ducklakeprepare provisions the pinned extension set used by
// deterministic documentation and CI fixtures. It is build tooling only: the
// application runtime never installs extensions.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
)

const fixtureCacheSuffix = ".cache/leapview/ci-duckdb-extensions"

var fixtureExtensions = [...]string{"ducklake", "spatial"}

type installer func(context.Context, string, string, string) error

func main() {
	root := flag.String("root", "", "private DuckDB extension fixture cache")
	flag.Parse()

	resolved, err := fixtureRoot(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare DuckDB fixture extensions: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	version, platform, err := runtimeTarget(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare DuckDB fixture extensions: %v\n", err)
		os.Exit(1)
	}
	for _, name := range fixtureExtensions {
		path, prepareErr := prepare(ctx, resolved, version, platform, name, installExtension)
		if prepareErr != nil {
			fmt.Fprintf(os.Stderr, "prepare DuckDB fixture extension %s: %v\n", name, prepareErr)
			os.Exit(1)
		}
		digest, verifyErr := verifyExtension(ctx, name, path)
		if verifyErr != nil {
			fmt.Fprintf(os.Stderr, "prepare DuckDB fixture extension %s: %v\n", name, verifyErr)
			os.Exit(1)
		}
		fmt.Printf("prepared DuckDB fixture %s %s sha256:%s\n", name, path, digest)
	}
}

func fixtureRoot(configured string) (string, error) {
	if strings.TrimSpace(configured) == "" {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("resolve user home directory")
		}
		configured = filepath.Join(home, filepath.FromSlash(fixtureCacheSuffix))
	}
	absolute, err := filepath.Abs(configured)
	if err != nil {
		return "", fmt.Errorf("resolve fixture cache: %w", err)
	}
	if configured != strings.TrimSpace(configured) || configured != absolute || filepath.Clean(absolute) != absolute {
		return "", fmt.Errorf("fixture cache must be an absolute canonical path")
	}
	return absolute, nil
}

func runtimeTarget(ctx context.Context) (string, string, error) {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return "", "", fmt.Errorf("open DuckDB runtime probe: %w", err)
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return "", "", fmt.Errorf("read DuckDB runtime version: %w", err)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
		return "", "", fmt.Errorf("read DuckDB runtime platform: %w", err)
	}
	version = strings.TrimSpace(version)
	platform = strings.TrimSpace(platform)
	if version != extensionsupply.CurrentDuckDBVersion {
		return "", "", fmt.Errorf("DuckDB runtime %q does not match pinned extension ABI %q", version, extensionsupply.CurrentDuckDBVersion)
	}
	if platform == "" || strings.ContainsAny(platform, `/\\`) {
		return "", "", fmt.Errorf("DuckDB runtime platform is invalid")
	}
	return version, platform, nil
}

func prepare(ctx context.Context, root, version, platform, name string, install installer) (string, error) {
	if !isFixtureExtension(name) {
		return "", fmt.Errorf("fixture extension %q is not approved", name)
	}
	if err := ensurePrivateRoot(root); err != nil {
		return "", err
	}
	if path, err := locateArtifact(root, version, platform, name); err == nil {
		return path, nil
	}
	if install == nil {
		return "", fmt.Errorf("%s fixture installer is required", name)
	}
	if err := install(ctx, root, platform, name); err != nil {
		return "", err
	}
	path, err := locateArtifact(root, version, platform, name)
	if err != nil {
		return "", fmt.Errorf("installed %s artifact: %w", name, err)
	}
	return path, nil
}

func ensurePrivateRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return fmt.Errorf("fixture cache must be an absolute canonical path")
	}
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("fixture cache must be a non-symlink directory")
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("fixture cache must be private")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect fixture cache: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create fixture cache: %w", err)
	}
	return os.Chmod(root, 0o700)
}

func locateArtifact(root, version, platform, name string) (string, error) {
	path := filepath.Join(root, version, platform, name+".duckdb_extension")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%s artifact is unavailable", name)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("%s artifact must be a non-empty regular non-symlink file", name)
	}
	return path, nil
}

func verifyExtension(ctx context.Context, name, path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s artifact: %w", name, err)
	}
	digest := sha256.Sum256(contents)
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return "", fmt.Errorf("open DuckDB extension verifier: %w", err)
	}
	defer db.Close()
	escapedPath := strings.ReplaceAll(path, "'", "''")
	// Loading the exact absolute artifact asks DuckDB to verify its official
	// extension signature. It does not enable installation in product runtime.
	if _, err := db.ExecContext(ctx, "LOAD '"+escapedPath+"'"); err != nil {
		return "", fmt.Errorf("verify pinned %s fixture extension: %w", name, err)
	}
	return hex.EncodeToString(digest[:]), nil
}

func installExtension(ctx context.Context, root, _, name string) error {
	if !isFixtureExtension(name) {
		return fmt.Errorf("fixture extension %q is not approved", name)
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open DuckDB extension provisioner: %w", err)
	}
	defer db.Close()
	escapedRoot := strings.ReplaceAll(root, "'", "''")
	if _, err := db.ExecContext(ctx, "SET extension_directory = '"+escapedRoot+"'"); err != nil {
		return fmt.Errorf("set DuckDB fixture extension directory: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSTALL "+name+" FROM core"); err != nil {
		return fmt.Errorf("install pinned %s fixture extension: %w", name, err)
	}
	return nil
}

func isFixtureExtension(name string) bool {
	for _, approved := range fixtureExtensions {
		if name == approved {
			return true
		}
	}
	return false
}
