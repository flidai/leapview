// Package poolcompatibility derives application-owned physical-pool tuples
// from target-admitted runtime artifacts.
package poolcompatibility

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/extension"
)

// LocalPool binds a disposable local pool to the exact DuckDB and DuckLake
// identities admitted by the target extension supply.
func LocalPool(ctx context.Context, admission extension.Admission) (physicalpool.Compatibility, error) {
	if admission == nil {
		return physicalpool.Compatibility{}, fmt.Errorf("local pool compatibility requires extension admission")
	}
	admitted, err := admission.AdmitExtension(ctx, "ducklake")
	if err != nil {
		return physicalpool.Compatibility{}, fmt.Errorf("admit DuckLake compatibility artifact: %w", err)
	}
	if admitted.Name != "ducklake" {
		return physicalpool.Compatibility{}, fmt.Errorf("admitted DuckLake compatibility artifact has name %q", admitted.Name)
	}
	duckdbVersion, err := runtimeComponent("duckdb", admitted.DuckDBVersion)
	if err != nil {
		return physicalpool.Compatibility{}, err
	}
	ducklakeVersion, err := runtimeComponent("ducklake", admitted.ExtensionVersion)
	if err != nil {
		return physicalpool.Compatibility{}, err
	}
	tuple := physicalpool.Compatibility{
		DuckDBRuntime: duckdbVersion, DuckLakeExtension: ducklakeVersion,
		CatalogFormat: "ducklake-catalog:v1", StorageImplementation: "local", ObjectNamingContract: "uuidv7:v1",
	}
	if err := tuple.Validate(); err != nil {
		return physicalpool.Compatibility{}, fmt.Errorf("validate local pool compatibility: %w", err)
	}
	return tuple, nil
}

func runtimeComponent(prefix, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("admitted %s compatibility version is invalid", prefix)
	}
	if index := strings.IndexByte(value, ':'); index >= 0 {
		if value[:index] != prefix {
			return "", fmt.Errorf("admitted %s compatibility version has prefix %q", prefix, value[:index])
		}
		value = value[index+1:]
	}
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return "", fmt.Errorf("admitted %s compatibility version is empty", prefix)
	}
	return prefix + ":" + value, nil
}
