// Command ducklakeprepare provisions the pinned DuckLake extension used by
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

type installer func(context.Context, string, string) error

func main() {
	root := flag.String("root", "", "private DuckDB extension fixture cache")
	flag.Parse()

	resolved, err := fixtureRoot(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare DuckLake fixture extension: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	version, platform, err := runtimeTarget(ctx)
	var path string
	if err == nil {
		path, err = prepare(ctx, resolved, version, platform, installDuckLake)
	}
	var digest string
	if err == nil {
		digest, err = verifyDuckLake(ctx, path)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "prepare DuckLake fixture extension: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("prepared DuckLake fixture %s sha256:%s\n", path, digest)
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

func prepare(ctx context.Context, root, version, platform string, install installer) (string, error) {
	if err := ensurePrivateRoot(root); err != nil {
		return "", err
	}
	if path, err := locateArtifact(root, version, platform); err == nil {
		return path, nil
	}
	if install == nil {
		return "", fmt.Errorf("DuckLake fixture installer is required")
	}
	if err := install(ctx, root, platform); err != nil {
		return "", err
	}
	path, err := locateArtifact(root, version, platform)
	if err != nil {
		return "", fmt.Errorf("installed DuckLake artifact: %w", err)
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

func locateArtifact(root, version, platform string) (string, error) {
	path := filepath.Join(root, version, platform, "ducklake.duckdb_extension")
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("DuckLake artifact is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("DuckLake artifact must be a non-empty regular non-symlink file")
	}
	return path, nil
}

func verifyDuckLake(ctx context.Context, path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read DuckLake artifact: %w", err)
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
		return "", fmt.Errorf("verify pinned DuckLake fixture extension: %w", err)
	}
	return hex.EncodeToString(digest[:]), nil
}

func installDuckLake(ctx context.Context, root, _ string) error {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open DuckDB extension provisioner: %w", err)
	}
	defer db.Close()
	escapedRoot := strings.ReplaceAll(root, "'", "''")
	if _, err := db.ExecContext(ctx, "SET extension_directory = '"+escapedRoot+"'"); err != nil {
		return fmt.Errorf("set DuckDB fixture extension directory: %w", err)
	}
	if _, err := db.ExecContext(ctx, "INSTALL ducklake FROM core"); err != nil {
		return fmt.Errorf("install pinned DuckLake fixture extension: %w", err)
	}
	return nil
}
