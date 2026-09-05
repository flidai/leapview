package ducklake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// PostgresSnapshotSealEvidence is bounded, queryable evidence for a
// PostgreSQL-backed DuckLake snapshot. It intentionally contains no catalog
// file path, object key, byte digest, or serialized catalog payload.
type PostgresSnapshotSealEvidence struct {
	CatalogType      string `json:"catalog_type"`
	MetadataSchema   string `json:"metadata_schema"`
	DataPath         string `json:"data_path"`
	ExtensionVersion string `json:"extension_version"`
	CatalogVersion   string `json:"catalog_version"`
	SnapshotID       int64  `json:"snapshot_id"`
	CommitMarker     string `json:"commit_marker,omitempty"`
}

// SnapshotSealEvidence reads the attached catalog's settings/options and the
// exact snapshot marker. It is intended for PostgreSQL delivery seal records;
// callers persist the returned bounded JSON as evidence, never catalog bytes.
func (e *Environment) SnapshotSealEvidence(ctx context.Context, snapshotID int64) (PostgresSnapshotSealEvidence, error) {
	if e == nil || e.db == nil || !e.postgresCatalog {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("PostgreSQL DuckLake environment is required")
	}
	if snapshotID <= 0 {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("snapshot id must be positive")
	}
	if e.postgresSnapshot > 0 && snapshotID != e.postgresSnapshot {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("snapshot %d is not the attached SNAPSHOT_VERSION %d", snapshotID, e.postgresSnapshot)
	}
	conn, release, err := e.queryConnection(ctx)
	if err != nil {
		return PostgresSnapshotSealEvidence{}, err
	}
	defer release()
	var evidence PostgresSnapshotSealEvidence
	evidence.MetadataSchema = e.postgresMetadata
	evidence.SnapshotID = snapshotID
	if err := conn.QueryRowContext(ctx, "SELECT catalog_type, extension_version, data_path FROM lake.settings() LIMIT 1").Scan(&evidence.CatalogType, &evidence.ExtensionVersion, &evidence.DataPath); err != nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("read DuckLake PostgreSQL settings: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(evidence.CatalogType), "postgres") {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("DuckLake catalog type %q is not PostgreSQL", evidence.CatalogType)
	}
	if evidence.DataPath, err = CanonicalDataPath(evidence.DataPath); err != nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("canonicalize DuckLake DATA_PATH: %w", err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT CAST(value AS VARCHAR) FROM lake.options() WHERE lower(option_name) = 'version' AND upper(scope) = 'GLOBAL' LIMIT 1").Scan(&evidence.CatalogVersion); err != nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("read DuckLake catalog version: %w", err)
	}
	if strings.TrimSpace(evidence.CatalogVersion) == "" {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("DuckLake catalog version is empty")
	}
	if e.commitMarker == nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("DuckLake snapshot seal requires the writer commit marker")
	}
	if err := conn.QueryRowContext(ctx, "SELECT CAST(commit_extra_info AS VARCHAR) FROM lake.snapshots() WHERE snapshot_id = ?", snapshotID).Scan(&evidence.CommitMarker); err != nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("read DuckLake commit marker: %w", err)
	}
	parsed, err := ParseCommitMarker(evidence.CommitMarker)
	if err != nil {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("parse DuckLake commit marker: %w", err)
	}
	canonical, err := parsed.CanonicalJSON()
	if err != nil || canonical != mustCanonicalMarker(e.commitMarker) {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("DuckLake snapshot %d does not carry the exact commit marker", snapshotID)
	}
	if err := validateSnapshotSealEvidenceFields(evidence); err != nil {
		return PostgresSnapshotSealEvidence{}, err
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return PostgresSnapshotSealEvidence{}, err
	}
	if len(encoded) > 16*1024 {
		return PostgresSnapshotSealEvidence{}, fmt.Errorf("DuckLake snapshot seal evidence exceeds 16384 bytes")
	}
	return evidence, nil
}

// validateSnapshotSealEvidenceFields keeps scalar seal identities bounded by
// the marker field limit while allowing the canonical marker document itself
// to use the larger document limit. The marker contract deliberately allows a
// valid document to exceed one scalar field's bound once JSON keys and all
// identities are included.
func validateSnapshotSealEvidenceFields(evidence PostgresSnapshotSealEvidence) error {
	for name, value := range map[string]string{
		"catalog type": evidence.CatalogType, "metadata schema": evidence.MetadataSchema,
		"data path": evidence.DataPath, "extension version": evidence.ExtensionVersion,
		"catalog version": evidence.CatalogVersion, "commit marker": evidence.CommitMarker,
	} {
		limit := MaxCommitMarkerFieldBytes
		if name == "commit marker" {
			limit = MaxCommitMarkerBytes
		}
		if len(value) > limit {
			return fmt.Errorf("DuckLake %s evidence exceeds bounded field size", name)
		}
	}
	return nil
}

func mustCanonicalMarker(marker *CommitMarker) string {
	if marker == nil {
		return ""
	}
	value, _ := marker.CanonicalJSON()
	return value
}
