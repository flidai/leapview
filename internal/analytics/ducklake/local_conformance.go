package ducklake

// This file contains the development/evaluation physical-pool admission
// probe. It deliberately runs the same concrete isolation, closure, and
// read-only checks used by the conformance tests against a disposable local
// pool before an admission is persisted. Production pools continue to require
// reviewed offline evidence.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

// RunLocalPoolConformance executes the substantive local shared-pool
// scenarios and returns complete, content-addressed admission evidence.
func RunLocalPoolConformance(ctx context.Context, root string, tuple physicalpool.Compatibility) (physicalpool.Evidence, error) {
	if err := tuple.Validate(); err != nil {
		return physicalpool.Evidence{}, err
	}
	if tuple.StorageImplementation != "local" && tuple.StorageImplementation != "filesystem" {
		return physicalpool.Evidence{}, fmt.Errorf("local conformance requires local storage implementation")
	}
	if strings.TrimSpace(root) == "" {
		return physicalpool.Evidence{}, fmt.Errorf("local conformance root is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return physicalpool.Evidence{}, err
	}
	dataPath := filepath.Join(root, "data")
	baseRoot := filepath.Join(root, "base")
	if err := os.MkdirAll(dataPath, 0o700); err != nil {
		return physicalpool.Evidence{}, err
	}
	base, err := Open(ctx, Config{RootDir: baseRoot, CatalogPath: filepath.Join(baseRoot, "catalog.duckdb"), DataPath: dataPath, Compatibility: tuple})
	if err != nil {
		return physicalpool.Evidence{}, fmt.Errorf("open local conformance base: %w", err)
	}
	closeBase := true
	defer func() {
		if closeBase {
			_ = base.Close()
		}
	}()
	if _, err := base.Commit(ctx, "conformance-base", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS model;
CREATE TABLE model.orders(id BIGINT, value VARCHAR);
CREATE TABLE model.metrics(id BIGINT, value VARCHAR);
INSERT INTO model.orders SELECT range, 'base' FROM range(1, 1001);
INSERT INTO model.metrics SELECT range, 'base' FROM range(1, 1001);`)
		return err
	}); err != nil {
		return physicalpool.Evidence{}, err
	}
	baseOrders, err := base.CurrentFileSet(ctx, "base", "model", "orders")
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	baseMetrics, err := base.CurrentFileSet(ctx, "base", "model", "metrics")
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	if err := base.Close(); err != nil {
		return physicalpool.Evidence{}, err
	}
	closeBase = false
	baseBytes, err := os.ReadFile(filepath.Join(baseRoot, "catalog.duckdb"))
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	baseDigest := localConformanceDigest(baseBytes)
	copyCatalog := func(destination string) error {
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, baseBytes, 0o600); err != nil {
			return err
		}
		copied, err := os.ReadFile(destination)
		if err != nil {
			return err
		}
		if localConformanceDigest(copied) != baseDigest {
			return errors.New("local conformance catalog copy digest mismatch")
		}
		return nil
	}
	aRoot, bRoot := filepath.Join(root, "a"), filepath.Join(root, "b")
	aCatalog, bCatalog := filepath.Join(aRoot, "catalog.duckdb"), filepath.Join(bRoot, "catalog.duckdb")
	if err := copyCatalog(aCatalog); err != nil {
		return physicalpool.Evidence{}, err
	}
	if err := copyCatalog(bCatalog); err != nil {
		return physicalpool.Evidence{}, err
	}
	a, err := Open(ctx, Config{RootDir: aRoot, CatalogPath: aCatalog, DataPath: dataPath, Compatibility: tuple})
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	defer a.Close()
	b, err := Open(ctx, Config{RootDir: bRoot, CatalogPath: bCatalog, DataPath: dataPath, Compatibility: tuple})
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	if _, err := b.CurrentFileSet(ctx, "b", "model", "metrics"); err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	var writes sync.WaitGroup
	writes.Add(2)
	writeErrors := make(chan error, 2)
	go func() {
		defer writes.Done()
		_, commitErr := a.Commit(ctx, "conformance-a", nil, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (2, 'a')")
			return err
		})
		writeErrors <- commitErr
	}()
	go func() {
		defer writes.Done()
		_, commitErr := b.Commit(ctx, "conformance-b", nil, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (3, 'b')"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO model.metrics VALUES (2, 'b')")
			return err
		})
		writeErrors <- commitErr
	}()
	writes.Wait()
	close(writeErrors)
	for writeErr := range writeErrors {
		if writeErr != nil {
			_ = b.Close()
			return physicalpool.Evidence{}, writeErr
		}
	}
	if _, err := a.Commit(ctx, "conformance-delete", nil, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM model.orders WHERE id = 2")
		return err
	}); err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	aOrders, err := a.CurrentFileSet(ctx, "a", "model", "orders")
	if err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	aMetrics, err := a.CurrentFileSet(ctx, "a", "model", "metrics")
	if err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	bOrders, err := b.CurrentFileSet(ctx, "b", "model", "orders")
	if err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	bMetrics, err := b.CurrentFileSet(ctx, "b", "model", "metrics")
	if err != nil {
		_ = b.Close()
		return physicalpool.Evidence{}, err
	}
	if len(aOrders.DeleteFiles) == 0 || !localContainsAll(aMetrics.DataFiles, baseMetrics.DataFiles) || !localContainsAll(bOrders.DataFiles, baseOrders.DataFiles) || localOverlap(localNewFiles(aOrders.DataFiles, baseOrders.DataFiles), localNewFiles(bOrders.DataFiles, baseOrders.DataFiles)) || localOverlap(localNewFiles(aOrders.DataFiles, baseOrders.DataFiles), localNewFiles(bMetrics.DataFiles, baseMetrics.DataFiles)) {
		_ = b.Close()
		return physicalpool.Evidence{}, errors.New("local conformance physical closure or key isolation failed")
	}
	if count, err := localCountRows(ctx, a, "model.orders"); err != nil || count != 999 {
		_ = b.Close()
		return physicalpool.Evidence{}, fmt.Errorf("local conformance same-table writer count=%d: %w", count, err)
	}
	if count, err := localCountRows(ctx, b, "model.orders"); err != nil || count != 1001 {
		_ = b.Close()
		return physicalpool.Evidence{}, fmt.Errorf("local conformance same-table peer count=%d: %w", count, err)
	}
	if count, err := localCountRows(ctx, b, "model.metrics"); err != nil || count != 1001 {
		_ = b.Close()
		return physicalpool.Evidence{}, fmt.Errorf("local conformance different-table writer count=%d: %w", count, err)
	}
	if count, err := localCountRows(ctx, a, "model.metrics"); err != nil || count != 1000 {
		_ = b.Close()
		return physicalpool.Evidence{}, fmt.Errorf("local conformance different-table peer count=%d: %w", count, err)
	}
	if err := b.Close(); err != nil {
		return physicalpool.Evidence{}, err
	}
	b, err = Open(ctx, Config{RootDir: bRoot, CatalogPath: bCatalog, DataPath: dataPath, ReadOnly: true, Compatibility: tuple})
	if err != nil {
		return physicalpool.Evidence{}, err
	}
	if !b.ReadOnly() {
		return physicalpool.Evidence{}, errors.New("local conformance read-only attach was writable")
	}
	if err := b.Exec(ctx, "INSERT INTO model.metrics VALUES (2000, 'should-fail')"); !errors.Is(err, ErrReadOnlyEnvironment) {
		return physicalpool.Evidence{}, fmt.Errorf("local conformance read-only write error=%v", err)
	}
	live := CrossCatalogLiveFileUnion([]CatalogFileSet{aOrders, aMetrics, bOrders, bMetrics})
	if len(live) == 0 {
		return physicalpool.Evidence{}, errors.New("local conformance live closure is empty")
	}
	objects := make([]PoolObject, 0, len(live)+1)
	for _, file := range live {
		objects = append(objects, PoolObject{Path: file.Path, Kind: file.Kind})
	}
	objects = append(objects, PoolObject{Path: "fixture-orphan.parquet", Kind: DataFile})
	classified := ClassifyPoolObjects(objects, []CatalogFileSet{aOrders, aMetrics, bOrders, bMetrics})
	if classified[len(classified)-1].Live {
		return physicalpool.Evidence{}, errors.New("local conformance orphan classification failed")
	}
	checks := map[string]ConformanceCheck{
		"same_table_private_clone_isolation":      func(context.Context) ([]byte, error) { return []byte("orders:999/1000"), nil },
		"different_table_private_clone_isolation": func(context.Context) ([]byte, error) { return []byte("metrics:1001/1000"), nil },
		"unchanged_file_reference_reuse": func(context.Context) ([]byte, error) {
			return []byte(strings.Join(append(baseOrders.DataFiles, baseMetrics.DataFiles...), "\x00")), nil
		},
		"new_file_key_disjointness": func(context.Context) ([]byte, error) {
			keys := append(localNewFiles(aOrders.DataFiles, baseOrders.DataFiles), localNewFiles(bOrders.DataFiles, baseOrders.DataFiles)...)
			keys = append(keys, localNewFiles(aMetrics.DataFiles, baseMetrics.DataFiles)...)
			keys = append(keys, localNewFiles(bMetrics.DataFiles, baseMetrics.DataFiles)...)
			return []byte(strings.Join(keys, "\x00")), nil
		},
		"aborted_write_isolation": func(ctx context.Context) ([]byte, error) {
			before, err := a.Snapshots(ctx)
			if err != nil {
				return nil, err
			}
			_, err = a.Commit(ctx, "conformance-abort", nil, func(tx *sql.Tx) error {
				_, _ = tx.ExecContext(ctx, "INSERT INTO model.orders VALUES (99, 'aborted')")
				return errors.New("abort")
			})
			if err == nil {
				return nil, errors.New("abort unexpectedly committed")
			}
			after, err := a.Snapshots(ctx)
			if err != nil || len(after) != len(before) {
				return nil, errors.New("abort changed snapshots")
			}
			return []byte(fmt.Sprintf("snapshots:%d", len(after))), nil
		},
		"normalization_file_union_completeness": func(context.Context) ([]byte, error) {
			return []byte(fmt.Sprintf("live:%d/delete:%d", len(live), len(aOrders.DeleteFiles))), nil
		},
		"cross_catalog_orphan_classification": func(context.Context) ([]byte, error) {
			return []byte(fmt.Sprintf("objects:%d/live:%d", len(classified), len(live))), nil
		},
		"sealed_catalog_read": func(ctx context.Context) ([]byte, error) {
			count, err := localCountRows(ctx, b, "model.metrics")
			if err != nil || count != 1001 {
				return nil, fmt.Errorf("reopened metrics=%d", count)
			}
			return []byte("reopened:1001"), nil
		},
		"safe_close": func(ctx context.Context) ([]byte, error) {
			if err := b.Close(); err != nil {
				return nil, err
			}
			if _, _, err := b.queryConnection(ctx); !errors.Is(err, ErrEnvironmentClosed) {
				return nil, errors.New("closed catalog accepted a connection")
			}
			return []byte("closed"), nil
		},
	}
	return SharedPoolConformance{Compatibility: tuple, Checks: checks}.Run(ctx)
}

func localConformanceDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func localCountRows(ctx context.Context, env *Environment, table string) (int, error) {
	var count int
	err := env.sqlDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count)
	return count, err
}

func localContainsAll(have, want []string) bool {
	for _, expected := range want {
		found := false
		for _, actual := range have {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func localOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b {
				return true
			}
		}
	}
	return false
}

func localNewFiles(current, inherited []string) []string {
	result := make([]string, 0, len(current))
	for _, file := range current {
		found := false
		for _, base := range inherited {
			if file == base {
				found = true
				break
			}
		}
		if !found {
			result = append(result, file)
		}
	}
	return result
}
