package managedrecovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/ducklake/metadata"
	"github.com/flidai/leapview/internal/recoveryset"
)

func managedClosureFixture(t *testing.T) (recoveryset.SnapshotSeal, metadata.NativeSnapshotClosureEvidence, FileManifest) {
	t.Helper()
	evidence := metadata.NativeSnapshotClosureEvidence{
		CatalogID: "catalog", SnapshotID: 17, ObjectRoot: "/var/lib/leapview/data", RelationNamespace: "_candidate_1",
		Relations: []metadata.BaseTable{{Schema: "_candidate_1", Table: "orders"}},
		Objects:   []metadata.NativeSnapshotObject{{Kind: metadata.DeleteFile, Path: "/var/lib/leapview/data/deletes/rows.puffin"}, {Kind: metadata.DataFile, Path: "/var/lib/leapview/data/part.parquet"}},
	}
	evidence.RelationManifestJSON, _ = json.Marshal(struct {
		Namespace string               `json:"relation_namespace"`
		Relations []metadata.BaseTable `json:"relations"`
	}{evidence.RelationNamespace, evidence.Relations})
	evidence.ClosureJSON, _ = json.Marshal(struct {
		Objects []metadata.NativeSnapshotObject `json:"objects"`
	}{evidence.Objects})
	evidence.RelationManifestDigest = digestBytes(evidence.RelationManifestJSON)
	evidence.ClosureDigest = digestBytes(evidence.ClosureJSON)
	evidence.ObjectRootDigest = digestBytes([]byte(evidence.ObjectRoot))
	evidence.CanonicalJSON, _ = json.Marshal(struct {
		Schema         int                             `json:"schema_version"`
		Catalog        string                          `json:"catalog_id"`
		Snapshot       int64                           `json:"snapshot_id"`
		Root           string                          `json:"object_root"`
		Namespace      string                          `json:"relation_namespace"`
		Relations      []metadata.BaseTable            `json:"relations"`
		Objects        []metadata.NativeSnapshotObject `json:"objects"`
		RelationDigest string                          `json:"relation_manifest_digest"`
		ClosureDigest  string                          `json:"closure_digest"`
		RootDigest     string                          `json:"object_root_digest"`
	}{metadata.NativeSnapshotClosureSchemaVersion, evidence.CatalogID, evidence.SnapshotID, evidence.ObjectRoot, evidence.RelationNamespace, evidence.Relations, evidence.Objects, evidence.RelationManifestDigest, evidence.ClosureDigest, evidence.ObjectRootDigest})
	if err := metadata.VerifyNativeSnapshotClosureEvidence(evidence); err != nil {
		t.Fatal(err)
	}
	seal := recoveryset.SnapshotSeal{CatalogID: evidence.CatalogID, DuckLakeSnapshotID: evidence.SnapshotID, RelationNamespace: evidence.RelationNamespace, ObjectRoot: evidence.ObjectRoot, ObjectRootDigest: evidence.ObjectRootDigest, RelationManifestDigest: evidence.RelationManifestDigest, ClosureDigest: evidence.ClosureDigest}
	manifest := FileManifest{SchemaVersion: 1, Files: []FileContent{{Path: "deletes/rows.puffin", Size: 3, SHA256: strings.Repeat("a", 64)}, {Path: "part.parquet", Size: 9, SHA256: strings.Repeat("b", 64)}, {Path: "older-retained.parquet", Size: 4, SHA256: strings.Repeat("c", 64)}}}
	return seal, evidence, manifest
}

func TestManagedClosureRequiresEverySealedDataAndDeleteObject(t *testing.T) {
	seal, closure, manifest := managedClosureFixture(t)
	if err := VerifyManagedClosure(seal, closure, manifest); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		missing := FileManifest{SchemaVersion: 1, Files: append([]FileContent{}, manifest.Files[:index]...)}
		missing.Files = append(missing.Files, manifest.Files[index+1:]...)
		// A valid manifest by itself says nothing about closure coverage.
		if _, err := missing.Digest(); err != nil {
			t.Fatal(err)
		}
		if err := VerifyManagedClosure(seal, closure, missing); err == nil {
			t.Fatal("valid manifest omitting sealed object accepted")
		}
	}
}

func TestManagedClosureRejectsForeignSealAndMalformedContent(t *testing.T) {
	tests := map[string]func(*recoveryset.SnapshotSeal, *metadata.NativeSnapshotClosureEvidence, *FileManifest){
		"catalog": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.CatalogID = "foreign"
		},
		"snapshot": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.DuckLakeSnapshotID++
		},
		"namespace": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.RelationNamespace = "_other"
		},
		"root": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.ObjectRoot = "/var/lib/other"
		},
		"path identity": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, m *FileManifest) {
			s.ObjectRootDigest, _ = m.Digest()
		},
		"closure": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.ClosureDigest = digestBytes([]byte("other"))
		},
		"relations": func(s *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			s.RelationManifestDigest = digestBytes([]byte("other"))
		},
		"forged canonical bytes": func(_ *recoveryset.SnapshotSeal, c *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			c.CanonicalJSON = []byte("{}")
		},
		"escape": func(_ *recoveryset.SnapshotSeal, c *metadata.NativeSnapshotClosureEvidence, _ *FileManifest) {
			c.Objects[0].Path = "/var/lib/elsewhere/rows.puffin"
		},
		"invalid content": func(_ *recoveryset.SnapshotSeal, _ *metadata.NativeSnapshotClosureEvidence, m *FileManifest) {
			m.Files[0].SHA256 = ""
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			seal, closure, manifest := managedClosureFixture(t)
			mutate(&seal, &closure, &manifest)
			if err := VerifyManagedClosure(seal, closure, manifest); err == nil {
				t.Fatal("foreign or malformed closure accepted")
			}
		})
	}
}
