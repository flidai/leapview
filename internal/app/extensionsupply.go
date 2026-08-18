package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
)

const maxExtensionSupplyDocumentBytes = 1 << 20

// loadExtensionSupply is the only application composition point for the
// target-owned extension supply. The document digest is checked before JSON
// decoding, and the resulting admission/preparation object is shared by the
// analytics and release modules.
func loadExtensionSupply(cfg config.Config) (*extensionsupply.Supply, error) {
	path, err := absoluteSupplyPath(cfg.DuckDBExtensionSupplyPath)
	if err != nil {
		return nil, err
	}
	payload, err := readSupplyDocument(path)
	if err != nil {
		return nil, err
	}
	if err := verifySupplyDigest(payload, cfg.DuckDBExtensionSupplySHA256); err != nil {
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
	if err := validateSupplyManifest(manifest); err != nil {
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
		DuckDBVersion:  extensionsupply.CurrentDuckDBVersion,
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		Platform:       runtime.GOOS + "-" + runtime.GOARCH,
		SupportProfile: manifest.SupportProfile,
		CacheDir:       cacheDir,
		Manifest:       manifest,
		Origins:        origins,
		// The packaged document's whole-file digest is the external trust
		// anchor. Signature/provenance references are still required and are
		// rechecked by Supply for every cached or fetched artifact.
		VerifySignature: func(_ context.Context, artifact extensionsupply.Artifact, _ []byte) error {
			if strings.TrimSpace(artifact.Signature) == "" || strings.TrimSpace(artifact.Provenance) == "" {
				return extension.ErrExtensionUnsigned
			}
			return nil
		},
	})
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

func readSupplyDocument(path string) ([]byte, error) {
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
	payload, err := io.ReadAll(io.LimitReader(file, maxExtensionSupplyDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read duckdb extension supply document", extension.ErrExtensionConfiguration)
	}
	if len(payload) > maxExtensionSupplyDocumentBytes {
		return nil, fmt.Errorf("%w: duckdb extension supply document exceeds %d bytes", extension.ErrExtensionConfiguration, maxExtensionSupplyDocumentBytes)
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

func validateSupplyManifest(manifest extensionsupply.Manifest) error {
	if manifest.Version != extensionsupply.ManifestVersion {
		return fmt.Errorf("%w: unsupported duckdb extension supply manifest version %d", extension.ErrInvalidManifest, manifest.Version)
	}
	if manifest.DuckDBVersion != extensionsupply.CurrentDuckDBVersion || manifest.GOOS != runtime.GOOS || manifest.GOARCH != runtime.GOARCH || manifest.Platform != runtime.GOOS+"-"+runtime.GOARCH {
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
			path = filepath.Join(root, artifact.Identity.Name+"-"+artifact.Identity.ExtensionVersion+"-"+artifact.Identity.Platform+".duckdb_extension")
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
