package postgres

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingdb "github.com/flidai/leapview/internal/servingstate/postgres/internal/db"
)

func bundleFromGetRow(row servingdb.GetBundleRow) (Bundle, error) {
	pid, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return Bundle{}, err
	}
	manifest, access, publications, appearances, err := canonicalBundleDocuments(row.BManifestJson, row.BAccessPolicyJson, row.BDashboardPublicationsJson, row.BDashboardAppearancesJson)
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{GenerationID: row.BGenerationID, ProjectID: pid, Environment: servingstate.Environment(row.Environment), ArtifactID: row.ArtifactID, ArtifactDigest: row.ArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, ArtifactFormat: row.ArtifactFormat, ArtifactLocator: row.ArtifactLocator, StorageSecurityDomain: row.StorageSecurityDomain, ArtifactContentType: row.ArtifactContentType, ArtifactMetadataDigest: row.ArtifactMetadataDigest, ManifestJSON: manifest, ProjectDigest: row.ProjectDigest, AccessPolicyJSON: access, DashboardPublicationsJSON: publications, DashboardAppearancesJSON: appearances, SizeBytes: row.SizeBytes, DuckLakeSnapshotID: row.DucklakeSnapshotID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)}
	if row.CommittedAt.Valid {
		bundle.ActivatedAt = row.CommittedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	return bundle, nil
}

func bundleFromActiveRow(row servingdb.GetActiveBundleRow) (Bundle, error) {
	pid, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return Bundle{}, err
	}
	manifest, access, publications, appearances, err := canonicalBundleDocuments(row.BManifestJson, row.BAccessPolicyJson, row.BDashboardPublicationsJson, row.BDashboardAppearancesJson)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{GenerationID: row.BGenerationID, ProjectID: pid, Environment: servingstate.Environment(row.Environment), ArtifactID: row.ArtifactID, ArtifactDigest: row.ArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, ArtifactFormat: row.ArtifactFormat, ArtifactLocator: row.ArtifactLocator, StorageSecurityDomain: row.StorageSecurityDomain, ArtifactContentType: row.ArtifactContentType, ArtifactMetadataDigest: row.ArtifactMetadataDigest, ManifestJSON: manifest, ProjectDigest: row.ProjectDigest, AccessPolicyJSON: access, DashboardPublicationsJSON: publications, DashboardAppearancesJSON: appearances, SizeBytes: row.SizeBytes, DuckLakeSnapshotID: row.DucklakeSnapshotID, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano), ActivatedAt: row.CommittedAt.Time.UTC().Format(time.RFC3339Nano)}, nil
}

// canonicalBundleDocuments re-encodes JSONB documents at the database read
// boundary. PostgreSQL preserves JSON semantics but may change object key
// order, whitespace, and nested object formatting in jsonb::text output.
func canonicalBundleDocuments(manifest, access, publications, appearances string) (string, string, string, string, error) {
	documents := []struct {
		name string
		raw  string
	}{
		{name: "manifest", raw: manifest},
		{name: "access policy", raw: access},
		{name: "dashboard publications", raw: publications},
		{name: "dashboard appearances", raw: appearances},
	}
	canonical := [4]string{}
	for i := range documents {
		value, err := canonicalDatabaseObject(documents[i].raw)
		if err != nil {
			return "", "", "", "", fmt.Errorf("canonicalize persisted serving %s: %w", documents[i].name, err)
		}
		canonical[i] = value
	}
	return canonical[0], canonical[1], canonical[2], canonical[3], nil
}

func canonicalDatabaseObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		raw = "{}"
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("serving-state JSON contains trailing data")
		}
		return "", err
	}
	if object, ok := value.(map[string]any); !ok || object == nil {
		return "", errors.New("serving-state JSON must be an object")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", errors.New("serving-state JSON exceeds 1 MiB")
	}
	return string(b), nil
}
