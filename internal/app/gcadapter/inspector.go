// Package gcadapter wires deployment global-GC ports to the admitted
// read-only DuckLake runtime. Keeping this in app composition avoids a
// deployment use-case dependency on analytics implementations.
package gcadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/gc"
)

type Inspector struct {
	Store               gc.PoolStore
	PoolContract        *ducklake.PoolContract
	StagingRoot         string
	CredentialBootstrap ducklake.CredentialBootstrap
	MaxConnections      int
	MemoryMaxBytes      int64
	TempMaxBytes        int64
	MaxThreads          int
	TempDir             string
}

func (i Inspector) Inspect(ctx context.Context, root deployment.DeliveryRoot) (gc.CatalogReachability, error) {
	if i.Store == nil || i.PoolContract == nil {
		return gc.CatalogReachability{}, fmt.Errorf("GC catalog inspector requires store and pool contract")
	}
	if root.PhysicalPoolID != i.PoolContract.Pool.ID.String() {
		return gc.CatalogReachability{}, fmt.Errorf("catalog root is bound to a different physical pool")
	}
	object, err := i.Store.Open(ctx, root.ObjectKey)
	if err != nil || object.Body == nil {
		return gc.CatalogReachability{}, fmt.Errorf("open rooted catalog: %w", err)
	}
	defer object.Body.Close()
	stagingRoot := i.StagingRoot
	if stagingRoot == "" {
		stagingRoot = filepath.Join(os.TempDir(), "leapview-gc")
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return gc.CatalogReachability{}, err
	}
	staging, err := os.MkdirTemp(stagingRoot, ".catalog-")
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "catalog.duckdb")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	hash := sha256.New()
	n, copyErr := io.Copy(file, io.TeeReader(object.Body, hash))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return gc.CatalogReachability{}, fmt.Errorf("read rooted catalog: %v %v", copyErr, closeErr)
	}
	if object.Size > 0 && n != object.Size {
		return gc.CatalogReachability{}, fmt.Errorf("rooted catalog size mismatch")
	}
	digest := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if digest != root.CatalogDigest {
		return gc.CatalogReachability{}, fmt.Errorf("rooted catalog digest mismatch")
	}
	if object.Metadata != nil {
		if value := object.Metadata[catalogseal.MetadataDigest]; value != "" && value != digest {
			return gc.CatalogReachability{}, fmt.Errorf("rooted catalog metadata digest mismatch")
		}
		if value := object.Metadata["sha256"]; value != "" && value != digest {
			return gc.CatalogReachability{}, fmt.Errorf("rooted catalog metadata digest mismatch")
		}
	}
	dataPath, err := i.PoolContract.Pool.DataPath()
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	env, err := ducklake.Open(ctx, ducklake.Config{RootDir: staging, CatalogPath: path, DataPath: dataPath, PhysicalPoolID: root.PhysicalPoolID, SharedPool: true, Compatibility: i.PoolContract.Tuple, PoolContract: i.PoolContract, ReadOnly: true, CredentialBootstrap: i.CredentialBootstrap, MaxConnections: i.MaxConnections, MemoryMaxBytes: i.MemoryMaxBytes, TempMaxBytes: i.TempMaxBytes, MaxThreads: i.MaxThreads, TempDir: i.TempDir})
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	defer env.Close()
	policy, err := env.DataInliningPolicy(ctx)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	if err := policy.ValidateZero(); err != nil {
		return gc.CatalogReachability{}, err
	}
	inline, err := env.LegacyInlineTables(ctx)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	if err := ducklake.ValidateNoLiveInlineData(inline); err != nil {
		return gc.CatalogReachability{}, err
	}
	snapshots, err := env.Snapshots(ctx)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	if len(snapshots) != 1 {
		return gc.CatalogReachability{}, fmt.Errorf("rooted catalog must retain exactly one snapshot, found %d", len(snapshots))
	}
	_, tables, closure, err := env.CurrentFileClosure(ctx, root.CatalogDigest)
	if err != nil {
		return gc.CatalogReachability{}, err
	}
	reach := gc.CatalogReachability{CatalogKey: root.ObjectKey, CatalogDigest: root.CatalogDigest}
	_ = tables // CurrentFileClosure has already enumerated every visible table.
	for _, ref := range closure.DataFiles {
		key, err := canonicalPoolObjectKey(i.PoolContract, ref)
		if err != nil {
			return gc.CatalogReachability{}, err
		}
		if err := verifyReferencedObject(ctx, i.Store, root.PhysicalPoolID, key); err != nil {
			return gc.CatalogReachability{}, fmt.Errorf("catalog data file %q is not an immutable pool object: %w", key, err)
		}
		reach.DataFiles = append(reach.DataFiles, key)
	}
	for _, ref := range closure.DeleteFiles {
		key, err := canonicalPoolObjectKey(i.PoolContract, ref)
		if err != nil {
			return gc.CatalogReachability{}, err
		}
		if err := verifyReferencedObject(ctx, i.Store, root.PhysicalPoolID, key); err != nil {
			return gc.CatalogReachability{}, fmt.Errorf("catalog delete file %q is not an immutable pool object: %w", key, err)
		}
		reach.DeleteFiles = append(reach.DeleteFiles, key)
	}
	return reach, nil
}

func verifyReferencedObject(ctx context.Context, store gc.PoolStore, poolID, key string) error {
	object, err := store.Stat(ctx, poolID, key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("referenced object is missing")
		}
		return err
	}
	if object.Digest == "" {
		return fmt.Errorf("referenced object has no immutable digest")
	}
	return nil
}

// canonicalPoolObjectKey translates DuckLake's local absolute/relative paths
// and object-store URIs into the pool-store's relative key namespace. Any
// sibling namespace or traversal is rejected before it can become a GC mark.
func canonicalPoolObjectKey(contract *ducklake.PoolContract, reference string) (string, error) {
	if contract == nil || strings.TrimSpace(reference) == "" {
		return "", gc.ErrObjectOutsidePool
	}
	base, err := contract.Pool.DataPath()
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(reference)
	if strings.Contains(ref, "\x00") {
		return "", gc.ErrObjectOutsidePool
	}
	if strings.Contains(base, "://") {
		baseURL, err := url.Parse(base)
		if err != nil {
			return "", err
		}
		refURL, err := url.Parse(ref)
		if err == nil && refURL.Scheme != "" {
			if strings.ToLower(refURL.Scheme) != strings.ToLower(baseURL.Scheme) || !strings.EqualFold(refURL.Host, baseURL.Host) {
				return "", gc.ErrObjectOutsidePool
			}
			basePath := strings.TrimSuffix(path.Clean(baseURL.Path), "/")
			refPath := path.Clean(refURL.Path)
			if refPath != basePath && !strings.HasPrefix(refPath, basePath+"/") {
				return "", gc.ErrObjectOutsidePool
			}
			key := strings.TrimPrefix(strings.TrimPrefix(refPath, basePath), "/")
			if key == "" || key == "." || key == ".." || strings.HasPrefix(key, "../") {
				return "", gc.ErrObjectOutsidePool
			}
			return key, nil
		}
		key := path.Clean(strings.TrimPrefix(strings.ReplaceAll(ref, "\\", "/"), "/"))
		if key == "." || key == ".." || strings.HasPrefix(key, "../") || strings.Contains(key, "/../") {
			return "", gc.ErrObjectOutsidePool
		}
		return key, nil
	}
	if strings.HasPrefix(ref, "file://") {
		parsed, err := url.Parse(ref)
		if err != nil || parsed.Host != "" {
			return "", gc.ErrObjectOutsidePool
		}
		ref = parsed.Path
	}
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	refPath := ref
	if !filepath.IsAbs(refPath) {
		refPath = filepath.Join(baseAbs, filepath.FromSlash(refPath))
	}
	refAbs, err := filepath.Abs(refPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, refAbs)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", gc.ErrObjectOutsidePool
	}
	return filepath.ToSlash(rel), nil
}
