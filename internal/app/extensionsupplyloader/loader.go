package extensionsupplyloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
)

const MaxExtensionSupplyDocumentBytes = 1 << 20

// PackagedSupplyPath is the only runtime default. It is baked into the
// server image and is intentionally not present in authored/project config.
// A sidecar digest is accepted only for this immutable image path.
const (
	PackagedSupplyPath       = "/usr/local/share/leapview/extensions/extension-supply.json"
	PackagedSupplyDigestPath = "/usr/local/share/leapview/extensions/extension-supply.json.sha256"
)

// loadExtensionSupply is the only application composition point for the
// target-owned extension supply. The document digest is checked before JSON
// decoding, and the resulting admission/preparation object is shared by the
// analytics and release modules.
func Load(ctx context.Context, cfg config.Config) (*extensionsupply.Supply, error) {
	rawPath := strings.TrimSpace(cfg.DuckDBExtensionSupplyPath)
	if rawPath == "" {
		// An image may omit the env override entirely; select the packaged
		// default only when the fixed immutable file is actually present. Local
		// development and tests still fail closed when no reviewed supply exists.
		if info, statErr := os.Lstat(PackagedSupplyPath); statErr == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			rawPath = PackagedSupplyPath
		}
	}
	path, err := absoluteSupplyPath(rawPath)
	if err != nil {
		return nil, err
	}
	payload, err := ReadSupplyDocument(path)
	if err != nil {
		return nil, err
	}
	digest := strings.TrimSpace(cfg.DuckDBExtensionSupplySHA256)
	if digest == "" && path == PackagedSupplyPath {
		var digestErr error
		digest, digestErr = ReadPackagedSupplyDigest(PackagedSupplyDigestPath)
		if digestErr != nil {
			return nil, digestErr
		}
	}
	if err := verifySupplyDigest(payload, digest); err != nil {
		return nil, err
	}
	var manifest extensionsupply.Manifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("duckdb extension supply document: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("duckdb extension supply document: exactly one JSON value is required")
		}
		return nil, fmt.Errorf("duckdb extension supply document has trailing data: %w", err)
	}
	duckDBVersion, duckDBPlatform, err := RuntimeTarget(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateSupplyManifest(manifest, duckDBVersion, duckDBPlatform); err != nil {
		return nil, err
	}

	origins := make([]extensionsupply.Origin, 0, len(manifest.Origins))
	seenOrigins := make(map[string]struct{}, len(manifest.Origins))
	for _, declared := range manifest.Origins {
		originPath, pathErr := absoluteOriginPath(declared.Path)
		if pathErr != nil {
			return nil, fmt.Errorf("duckdb extension origin %q: %w", declared.ID, pathErr)
		}
		if !declared.Reviewed {
			return nil, fmt.Errorf("%w: duckdb extension origin %q is not reviewed", extension.ErrExtensionUnapproved, declared.ID)
		}
		if _, exists := seenOrigins[declared.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate duckdb extension origin %q", extension.ErrInvalidManifest, declared.ID)
		}
		seenOrigins[declared.ID] = struct{}{}
		origins = append(origins, extensionsupply.Origin{
			ID:       declared.ID,
			URL:      "file://" + originPath,
			Reviewed: true,
			Fetch:    packagedOriginFetcher(originPath),
		})
	}
	for _, artifact := range manifest.Artifacts {
		for _, originID := range artifact.Origins {
			if _, exists := seenOrigins[originID]; !exists {
				return nil, fmt.Errorf("%w: artifact %q names unknown origin %q", extension.ErrInvalidManifest, artifact.Identity.Name, originID)
			}
		}
	}

	cacheDir := strings.TrimSpace(cfg.DuckDBExtensionCacheDir)
	if cacheDir == "" {
		cacheDir, err = filepath.Abs(filepath.Join(cfg.HomeDir, "duckdb-extension-cache"))
		if err != nil {
			return nil, fmt.Errorf("duckdb extension cache directory: %w", err)
		}
	} else {
		cacheDir, err = absoluteConfiguredPath(cacheDir, "duckdb extension cache directory")
		if err != nil {
			return nil, err
		}
	}
	return extensionsupply.New(extensionsupply.Config{
		DuckDBVersion:  duckDBVersion,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Platform:       duckDBPlatform,
		SupportProfile: manifest.SupportProfile,
		CacheDir:       cacheDir,
		Manifest:       manifest,
		Origins:        origins,
		// The immutable image supply is offline by construction. Explicit
		// bounded admin/test supplies may still use their reviewed local origin
		// adapter; loader validation never permits a network URL.
		Offline: path == PackagedSupplyPath,
		// The packaged document's whole-file digest is the external trust
		// anchor. Signature/provenance references are still required and are
		// rechecked by Supply for every cached or fetched artifact.
		VerifySignature: func(_ context.Context, artifact extensionsupply.Artifact, _ []byte) error {
			if strings.TrimSpace(artifact.Signature) == "" || strings.TrimSpace(artifact.Provenance) == "" {
				return extension.ErrExtensionUnsigned
			}
			return nil
		},
		VerifySignatureAtPath: verifyDuckDBArtifactAtPath,
	})
}

// verifyDuckDBArtifactAtPath asks the pinned engine to verify the exact staged
// file. DuckDB's LOAD path performs official extension signature/provenance
// verification. Automatic acquisition is disabled first, so a dependency
// cannot turn this build/runtime check into a network install. The callback is
// run before Supply links the staged file into cache.
func verifyDuckDBArtifactAtPath(ctx context.Context, _ extensionsupply.Artifact, path string) error {
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open DuckDB extension verifier: %w", err)
	}
	defer db.Close()
	for _, statement := range []string{
		"SET autoinstall_known_extensions = false",
		"SET autoload_known_extensions = false",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure DuckDB extension verifier: %w", err)
		}
	}
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := db.ExecContext(ctx, "LOAD '"+escaped+"'"); err != nil {
		return fmt.Errorf("LOAD exact DuckDB extension artifact: %w", err)
	}
	return nil
}

// ReadPackagedSupplyDigest reads the build-generated SHA-256 sidecar. It is
// deliberately narrow: callers cannot point it at arbitrary paths or follow
// a symlink into an operator-controlled tree.
func ReadPackagedSupplyDigest(path string) (string, error) {
	if path != PackagedSupplyDigestPath {
		return "", fmt.Errorf("%w: packaged DuckDB extension supply digest path is fixed", extension.ErrExtensionConfiguration)
	}
	if err := rejectSymlinkAncestors(path); err != nil {
		return "", fmt.Errorf("%w: packaged DuckDB extension supply digest path: %v", extension.ErrExtensionConfiguration, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("%w: read packaged DuckDB extension supply digest", extension.ErrExtensionConfiguration)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > 256 {
		return "", fmt.Errorf("%w: packaged DuckDB extension supply digest must be a private regular file", extension.ErrExtensionConfiguration)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: read packaged DuckDB extension supply digest", extension.ErrExtensionConfiguration)
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil || len(contents) > 256 {
		return "", fmt.Errorf("%w: read packaged DuckDB extension supply digest", extension.ErrExtensionConfiguration)
	}
	fields := strings.Fields(string(contents))
	if len(fields) != 1 {
		return "", fmt.Errorf("%w: packaged DuckDB extension supply digest is not canonical", extension.ErrExtensionConfiguration)
	}
	return fields[0], nil
}

func absoluteSupplyPath(raw string) (string, error) {
	return absoluteConfiguredPath(raw, "duckdb extension supply document")
}

func absoluteConfiguredPath(raw, label string) (string, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%w: %s must be an absolute path", extension.ErrExtensionConfiguration, label)
	}
	clean := filepath.Clean(raw)
	if clean != raw {
		return "", fmt.Errorf("%w: %s must be canonical", extension.ErrExtensionConfiguration, label)
	}
	if err := rejectSymlinkAncestors(clean); err != nil {
		return "", fmt.Errorf("%w: %s: %v", extension.ErrExtensionConfiguration, label, err)
	}
	return clean, nil
}

func ReadSupplyDocument(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read duckdb extension supply document: %v", extension.ErrExtensionConfiguration, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: duckdb extension supply document must be a regular non-symlink file", extension.ErrExtensionConfiguration)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read duckdb extension supply document", extension.ErrExtensionConfiguration)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, MaxExtensionSupplyDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read duckdb extension supply document", extension.ErrExtensionConfiguration)
	}
	if len(payload) > MaxExtensionSupplyDocumentBytes {
		return nil, fmt.Errorf("%w: duckdb extension supply document exceeds %d bytes", extension.ErrExtensionConfiguration, MaxExtensionSupplyDocumentBytes)
	}
	return payload, nil
}

func verifySupplyDigest(payload []byte, raw string) error {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return fmt.Errorf("%w: duckdb extension supply SHA-256 must be canonical", extension.ErrExtensionConfiguration)
	}
	if strings.HasPrefix(raw, "sha256:") {
		raw = strings.TrimPrefix(raw, "sha256:")
	}
	if len(raw) != sha256.Size*2 || strings.ToLower(raw) != raw {
		return fmt.Errorf("%w: duckdb extension supply SHA-256 must be lowercase hexadecimal", extension.ErrExtensionConfiguration)
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return fmt.Errorf("%w: duckdb extension supply SHA-256 is invalid", extension.ErrExtensionConfiguration)
	}
	hash := sha256.Sum256(payload)
	if hex.EncodeToString(hash[:]) != raw {
		return fmt.Errorf("%w: duckdb extension supply document digest mismatch", extension.ErrExtensionIntegrity)
	}
	return nil
}

func validateSupplyManifest(manifest extensionsupply.Manifest, duckDBVersion, duckDBPlatform string) error {
	if manifest.Version != extensionsupply.ManifestVersion {
		return fmt.Errorf("%w: unsupported duckdb extension supply manifest version %d", extension.ErrInvalidManifest, manifest.Version)
	}
	if duckDBVersion == "" || duckDBPlatform == "" || manifest.DuckDBVersion != duckDBVersion || manifest.GOOS != runtime.GOOS || manifest.GOARCH != runtime.GOARCH || manifest.Platform != duckDBPlatform {
		return fmt.Errorf("%w: duckdb extension supply target does not match this runtime", extension.ErrInvalidManifest)
	}
	if strings.TrimSpace(manifest.SupportProfile) == "" || manifest.SupportProfile != strings.TrimSpace(manifest.SupportProfile) {
		return fmt.Errorf("%w: duckdb extension supply support profile is required", extension.ErrInvalidManifest)
	}
	if len(manifest.Origins) == 0 {
		return fmt.Errorf("%w: duckdb extension supply requires a reviewed local origin", extension.ErrInvalidManifest)
	}
	for _, origin := range manifest.Origins {
		if origin.ID == "" || origin.ID != strings.TrimSpace(origin.ID) {
			return fmt.Errorf("%w: duckdb extension origin id is invalid", extension.ErrInvalidManifest)
		}
	}
	return nil
}

func RuntimeTarget(ctx context.Context) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return "", "", fmt.Errorf("%w: open DuckDB runtime probe", extension.ErrExtensionConfiguration)
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return "", "", fmt.Errorf("%w: read DuckDB runtime version", extension.ErrExtensionConfiguration)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA platform").Scan(&platform); err != nil {
		return "", "", fmt.Errorf("%w: read DuckDB runtime platform", extension.ErrExtensionConfiguration)
	}
	version = strings.TrimSpace(version)
	platform = strings.TrimSpace(platform)
	if version != extensionsupply.CurrentDuckDBVersion {
		return "", "", fmt.Errorf("%w: DuckDB runtime %q does not match pinned extension ABI %q", extension.ErrExtensionConfiguration, version, extensionsupply.CurrentDuckDBVersion)
	}
	return version, platform, nil
}

func absoluteOriginPath(raw string) (string, error) {
	path, err := absoluteConfiguredPath(raw, "origin path")
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("origin path is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return "", fmt.Errorf("origin path must be a regular file or directory")
	}
	return path, nil
}

func packagedOriginFetcher(root string) func(context.Context, extensionsupply.Artifact) (io.ReadCloser, error) {
	return func(ctx context.Context, artifact extensionsupply.Artifact) (io.ReadCloser, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := root
		info, err := os.Lstat(root)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			name := extension.ArtifactFilenameStem(artifact.Identity.Name)
			path = filepath.Join(root, name+"-"+artifact.Identity.ExtensionVersion+"-"+artifact.Identity.Platform+".duckdb_extension")
		}
		if err := rejectSymlinkAncestors(path); err != nil {
			return nil, err
		}
		info, err = os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("origin artifact is not a regular file")
		}
		return os.Open(path)
	}
}

func rejectSymlinkAncestors(path string) error {
	path = filepath.Clean(path)
	current := string(filepath.Separator)
	if volume := filepath.VolumeName(path); volume != "" {
		current = volume + string(filepath.Separator)
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path contains a symlink")
		}
	}
	return nil
}
