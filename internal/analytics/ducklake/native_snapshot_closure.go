package ducklake

import (
	"context"
	"fmt"
	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"strings"
)

// Keep the runtime API on the same value-only contract consumed by controllers.
const (
	NativeSnapshotClosureSchemaVersion = metadata.NativeSnapshotClosureSchemaVersion
	NativeSnapshotClosureMaxBytes      = metadata.NativeSnapshotClosureMaxBytes
	NativeSnapshotClosureMaxFieldBytes = metadata.NativeSnapshotClosureMaxFieldBytes
	NativeSnapshotClosureMaxEntries    = metadata.NativeSnapshotClosureMaxEntries
)

type NativeSnapshotClosureRequest = metadata.NativeSnapshotClosureRequest
type NativeSnapshotObject = metadata.NativeSnapshotObject
type NativeSnapshotClosureEvidence = metadata.NativeSnapshotClosureEvidence

func VerifyNativeSnapshotClosureEvidence(evidence NativeSnapshotClosureEvidence) error {
	return metadata.VerifyNativeSnapshotClosureEvidence(evidence)
}

// NativeSnapshotClosureEvidence reads one pinned current closure and derives
// canonical, storage-neutral identities from it. It verifies the expected
// snapshot and DATA_PATH, and requires a PostgreSQL-backed environment. It
// never reads or hashes e.layout.CatalogPath (catalog.duckdb).
func (e *Environment) NativeSnapshotClosureEvidence(ctx context.Context, request NativeSnapshotClosureRequest) (NativeSnapshotClosureEvidence, error) {
	if e == nil || e.db == nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("ducklake environment is not initialized")
	}
	if !e.postgresCatalog {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("PostgreSQL DuckLake environment is required")
	}
	if request.SnapshotID <= 0 {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("snapshot id must be positive")
	}
	if err := metadata.ValidateNativeIdentityField("catalog id", request.CatalogID); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if err := metadata.ValidateNativeRelationNamespace(request.RelationNamespace); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	expectedRoot, err := CanonicalDataPath(request.ObjectRoot)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("canonicalize expected DuckLake DATA_PATH: %w", err)
	}
	if err := metadata.ValidateNativeIdentityField("object root", expectedRoot); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if e.postgresSnapshot > 0 && request.SnapshotID != e.postgresSnapshot {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("snapshot %d is not the attached SNAPSHOT_VERSION %d", request.SnapshotID, e.postgresSnapshot)
	}

	// CurrentFileClosure obtains the current snapshot and all table file refs
	// through one DuckDB connection. Re-check the current marker and settings
	// after it returns so a concurrent writer cannot make the result appear to
	// describe a different current state.
	snapshot, tables, files, err := e.CurrentFileClosure(ctx, request.CatalogID, request.RelationNamespace)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if snapshot != request.SnapshotID {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("current DuckLake snapshot is %d, want %d", snapshot, request.SnapshotID)
	}
	if files.CatalogID != request.CatalogID {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake closure catalog id %q does not match expected catalog id", files.CatalogID)
	}

	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	defer release()
	var catalogType, dataPath string
	if err := conn.QueryRowContext(ctx, "SELECT catalog_type, data_path FROM lake.settings() LIMIT 1").Scan(&catalogType, &dataPath); err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("read DuckLake PostgreSQL settings: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(catalogType), "postgres") {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake catalog type %q is not PostgreSQL", catalogType)
	}
	actualRoot, err := CanonicalDataPath(dataPath)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("canonicalize attached DuckLake DATA_PATH: %w", err)
	}
	if actualRoot != expectedRoot {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake DATA_PATH does not match expected object root")
	}
	var current int64
	if err := conn.QueryRowContext(ctx, "SELECT id FROM ducklake_current_snapshot(?)", catalogAlias).Scan(&current); err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("recheck DuckLake current snapshot: %w", err)
	}
	if current != request.SnapshotID {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake current snapshot changed to %d, want %d", current, request.SnapshotID)
	}

	relations, err := metadata.CanonicalNativeRelations(tables, request.RelationNamespace)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	objects, err := metadata.CanonicalNativeObjects(expectedRoot, files)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	evidence, err := metadata.NewNativeSnapshotClosureEvidence(request.CatalogID, request.SnapshotID, expectedRoot, request.RelationNamespace, relations, objects)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if err := VerifyNativeSnapshotClosureEvidence(evidence); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	return evidence, nil
}
