package ducklake

// Native snapshot closure evidence is the small, storage-neutral identity
// boundary used by PostgreSQL-backed DuckLake delivery seals.  It describes
// only values observed through DuckLake metadata: no catalog file is opened,
// copied, or hashed here.  In particular, PostgreSQL-backed catalogs do not
// have a catalog.duckdb artifact to include in this evidence.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	// NativeSnapshotClosureSchemaVersion identifies the canonical document
	// shape.  It is deliberately independent from the DuckLake extension
	// version: changing either shape or extension is a qualification change.
	NativeSnapshotClosureSchemaVersion = 2

	// NativeSnapshotClosureMaxBytes bounds each returned canonical document.
	// Closure evidence is an identity record, not an unbounded metadata channel.
	NativeSnapshotClosureMaxBytes = 64 * 1024
	// NativeSnapshotClosureMaxFieldBytes prevents a malformed catalog reference
	// from consuming disproportionate memory while the bounded document is
	// assembled.
	NativeSnapshotClosureMaxFieldBytes = 4096
	// NativeSnapshotClosureMaxEntries bounds the number of value entries before
	// JSON encoding.  The byte bound remains the final authority as field sizes
	// vary by storage backend.
	NativeSnapshotClosureMaxEntries = 100_000
)

// NativeSnapshotClosureRequest selects one exact current PostgreSQL DuckLake
// state. ObjectRoot must identify the DATA_PATH expected by the delivery
// target; it is canonicalized with CanonicalDataPath before comparison.
//
// The type contains no SQL capability or credentials and is safe to pass
// across package boundaries.
type NativeSnapshotClosureRequest struct {
	CatalogID  string
	SnapshotID int64
	ObjectRoot string
	// RelationNamespace is the authority-derived candidate schema whose
	// relations and file closure are eligible for native delivery evidence.
	// Native callers must provide it; an omitted namespace must never fall back
	// to the shared model schema.
	RelationNamespace string
}

// NativeSnapshotObject is one canonical data or delete object reference.
// Kind is part of the identity: a path cannot be both a data and delete file.
type NativeSnapshotObject struct {
	Kind FileKind `json:"kind"`
	Path string   `json:"path"`
}

// NativeSnapshotClosureEvidence is bounded, value-only evidence for one exact
// PostgreSQL-backed DuckLake snapshot. Relations and Objects are canonical
// sorted sets. The three JSON fields are the canonical bytes used to derive
// the corresponding digests; CanonicalJSON is a bounded envelope suitable for
// persistence as qualification evidence.
type NativeSnapshotClosureEvidence struct {
	CatalogID         string `json:"catalog_id"`
	SnapshotID        int64  `json:"snapshot_id"`
	ObjectRoot        string `json:"object_root"`
	RelationNamespace string `json:"relation_namespace"`

	Relations []BaseTable            `json:"relations"`
	Objects   []NativeSnapshotObject `json:"objects"`

	RelationManifestJSON json.RawMessage `json:"-"`
	ClosureJSON          json.RawMessage `json:"-"`
	CanonicalJSON        json.RawMessage `json:"-"`

	RelationManifestDigest string `json:"relation_manifest_digest"`
	ClosureDigest          string `json:"closure_digest"`
	ObjectRootDigest       string `json:"object_root_digest"`
}

// nativeSnapshotClosureJSON is the stable envelope persisted in
// NativeSnapshotClosureEvidence.CanonicalJSON. Keeping this separate from the
// public evidence type prevents JSON byte fields from being base64 encoded.
type nativeSnapshotClosureJSON struct {
	SchemaVersion          int                    `json:"schema_version"`
	CatalogID              string                 `json:"catalog_id"`
	SnapshotID             int64                  `json:"snapshot_id"`
	ObjectRoot             string                 `json:"object_root"`
	RelationNamespace      string                 `json:"relation_namespace"`
	Relations              []BaseTable            `json:"relations"`
	Objects                []NativeSnapshotObject `json:"objects"`
	RelationManifestDigest string                 `json:"relation_manifest_digest"`
	ClosureDigest          string                 `json:"closure_digest"`
	ObjectRootDigest       string                 `json:"object_root_digest"`
}

type nativeRelationManifestJSON struct {
	RelationNamespace string      `json:"relation_namespace"`
	Relations         []BaseTable `json:"relations"`
}

type nativeClosureManifestJSON struct {
	Objects []NativeSnapshotObject `json:"objects"`
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
	if err := validateNativeIdentityField("catalog id", request.CatalogID); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if err := validateNativeRelationNamespace(request.RelationNamespace); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	expectedRoot, err := CanonicalDataPath(request.ObjectRoot)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("canonicalize expected DuckLake DATA_PATH: %w", err)
	}
	if err := validateNativeIdentityField("object root", expectedRoot); err != nil {
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

	relations, err := canonicalNativeRelations(tables, request.RelationNamespace)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	objects, err := canonicalNativeObjects(expectedRoot, files)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	evidence, err := newNativeSnapshotClosureEvidence(request.CatalogID, request.SnapshotID, expectedRoot, request.RelationNamespace, relations, objects)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if err := VerifyNativeSnapshotClosureEvidence(evidence); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	return evidence, nil
}

func newNativeSnapshotClosureEvidence(catalogID string, snapshotID int64, objectRoot, relationNamespace string, relations []BaseTable, objects []NativeSnapshotObject) (NativeSnapshotClosureEvidence, error) {
	if err := validateNativeIdentityField("catalog id", catalogID); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if snapshotID <= 0 {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("snapshot id must be positive")
	}
	canonicalRoot, err := CanonicalDataPath(objectRoot)
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("canonicalize object root: %w", err)
	}
	if canonicalRoot != objectRoot {
		objectRoot = canonicalRoot
	}
	if err := validateNativeIdentityField("object root", objectRoot); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	if err := validateNativeRelationNamespace(relationNamespace); err != nil {
		return NativeSnapshotClosureEvidence{}, err
	}
	relationJSON, err := json.Marshal(nativeRelationManifestJSON{RelationNamespace: relationNamespace, Relations: relations})
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("marshal relation manifest: %w", err)
	}
	closureJSON, err := json.Marshal(nativeClosureManifestJSON{Objects: objects})
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("marshal DuckLake closure: %w", err)
	}
	if len(relationJSON) > NativeSnapshotClosureMaxBytes || len(closureJSON) > NativeSnapshotClosureMaxBytes {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake snapshot closure evidence exceeds %d bytes", NativeSnapshotClosureMaxBytes)
	}
	relationDigest := nativeSnapshotDigest(relationJSON)
	closureDigest := nativeSnapshotDigest(closureJSON)
	rootDigest := nativeSnapshotDigest([]byte(objectRoot))
	// Keep this validation tied to the platform identity convention even though
	// the values were generated locally. It makes accidental format changes
	// fail at the boundary instead of reaching a seal repository.
	for name, value := range map[string]string{"relation manifest": relationDigest, "closure": closureDigest, "object root": rootDigest} {
		if err := platformdigest.ValidateSHA256Identity(value); err != nil {
			return NativeSnapshotClosureEvidence{}, fmt.Errorf("%s digest: %w", name, err)
		}
	}
	evidence := NativeSnapshotClosureEvidence{
		CatalogID: catalogID, SnapshotID: snapshotID, ObjectRoot: objectRoot, RelationNamespace: relationNamespace,
		Relations: cloneBaseTables(relations), Objects: cloneNativeObjects(objects),
		RelationManifestJSON:   append(json.RawMessage(nil), relationJSON...),
		ClosureJSON:            append(json.RawMessage(nil), closureJSON...),
		RelationManifestDigest: relationDigest, ClosureDigest: closureDigest, ObjectRootDigest: rootDigest,
	}
	envelope, err := json.Marshal(nativeSnapshotClosureJSON{SchemaVersion: NativeSnapshotClosureSchemaVersion, CatalogID: evidence.CatalogID, SnapshotID: evidence.SnapshotID, ObjectRoot: evidence.ObjectRoot, RelationNamespace: evidence.RelationNamespace, Relations: evidence.Relations, Objects: evidence.Objects, RelationManifestDigest: evidence.RelationManifestDigest, ClosureDigest: evidence.ClosureDigest, ObjectRootDigest: evidence.ObjectRootDigest})
	if err != nil {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("marshal snapshot closure evidence: %w", err)
	}
	if len(envelope) > NativeSnapshotClosureMaxBytes {
		return NativeSnapshotClosureEvidence{}, fmt.Errorf("DuckLake snapshot closure evidence exceeds %d bytes", NativeSnapshotClosureMaxBytes)
	}
	evidence.CanonicalJSON = append(json.RawMessage(nil), envelope...)
	return evidence, nil
}

// VerifyNativeSnapshotClosureEvidence re-canonicalizes value evidence at a
// package boundary. Callers must not trust an implementation merely because
// its JSON and digests are internally self-consistent: relation sets must be
// sorted, unique, namespace-bound, and object paths must remain under the
// admitted root. A native materialization request always contains at least one
// model table, so an empty relation manifest is not qualifying evidence.
func VerifyNativeSnapshotClosureEvidence(evidence NativeSnapshotClosureEvidence) error {
	if len(evidence.Relations) == 0 {
		return fmt.Errorf("DuckLake native relation manifest is empty")
	}
	relations, err := canonicalNativeRelations(evidence.Relations, evidence.RelationNamespace)
	if err != nil {
		return err
	}
	if len(relations) != len(evidence.Relations) {
		return fmt.Errorf("DuckLake native relation manifest is not a canonical set")
	}
	for index := range relations {
		if relations[index] != evidence.Relations[index] {
			return fmt.Errorf("DuckLake native relation manifest is not canonically ordered")
		}
	}
	files := CatalogFileSet{CatalogID: evidence.CatalogID}
	for _, object := range evidence.Objects {
		switch object.Kind {
		case DataFile:
			files.DataFiles = append(files.DataFiles, object.Path)
		case DeleteFile:
			files.DeleteFiles = append(files.DeleteFiles, object.Path)
		default:
			return fmt.Errorf("DuckLake native object kind %q is invalid", object.Kind)
		}
	}
	objects, err := canonicalNativeObjects(evidence.ObjectRoot, files)
	if err != nil {
		return err
	}
	if len(objects) != len(evidence.Objects) {
		return fmt.Errorf("DuckLake native object manifest is not a canonical set")
	}
	for index := range objects {
		if objects[index] != evidence.Objects[index] {
			return fmt.Errorf("DuckLake native object manifest is not canonically ordered")
		}
	}
	expected, err := newNativeSnapshotClosureEvidence(evidence.CatalogID, evidence.SnapshotID, evidence.ObjectRoot, evidence.RelationNamespace, relations, objects)
	if err != nil {
		return err
	}
	if !bytes.Equal(evidence.RelationManifestJSON, expected.RelationManifestJSON) ||
		!bytes.Equal(evidence.ClosureJSON, expected.ClosureJSON) ||
		!bytes.Equal(evidence.CanonicalJSON, expected.CanonicalJSON) ||
		evidence.RelationManifestDigest != expected.RelationManifestDigest ||
		evidence.ClosureDigest != expected.ClosureDigest ||
		evidence.ObjectRootDigest != expected.ObjectRootDigest {
		return fmt.Errorf("DuckLake native snapshot closure evidence differs from canonical values")
	}
	return nil
}

func nativeSnapshotDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalNativeRelations(input []BaseTable, relationNamespaces ...string) ([]BaseTable, error) {
	if len(input) > NativeSnapshotClosureMaxEntries {
		return nil, fmt.Errorf("DuckLake relation manifest has %d entries, maximum is %d", len(input), NativeSnapshotClosureMaxEntries)
	}
	var relationNamespace string
	if len(relationNamespaces) > 1 {
		return nil, fmt.Errorf("DuckLake relation manifest received more than one relation namespace")
	}
	if len(relationNamespaces) == 1 {
		relationNamespace = relationNamespaces[0]
		if err := validateNativeRelationNamespace(relationNamespace); err != nil {
			return nil, err
		}
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]BaseTable, 0, len(input))
	for _, table := range input {
		if err := validateNativeIdentityField("schema", table.Schema); err != nil {
			return nil, err
		}
		if relationNamespace != "" && table.Schema != relationNamespace {
			return nil, fmt.Errorf("DuckLake relation %s.%s is outside expected relation namespace %q", table.Schema, table.Table, relationNamespace)
		}
		if err := validateNativeIdentityField("table", table.Table); err != nil {
			return nil, err
		}
		key := table.Schema + "\x00" + table.Table
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, table)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Schema == result[j].Schema {
			return result[i].Table < result[j].Table
		}
		return result[i].Schema < result[j].Schema
	})
	return result, nil
}

// validateNativeRelationNamespace enforces the canonical SQL identifier
// shape used by authority-derived candidate schemas. The derivation itself is
// owned by the deployment authority; this boundary only accepts a normalized,
// bounded value and never interpolates an unchecked namespace into SQL.
func validateNativeRelationNamespace(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("DuckLake relation namespace is empty or not normalized")
	}
	if len(value) > 63 {
		return fmt.Errorf("DuckLake relation namespace exceeds 63 bytes")
	}
	if value != strings.ToLower(value) {
		return fmt.Errorf("DuckLake relation namespace %q must be lowercase canonical", value)
	}
	for i, r := range value {
		if (r < 'a' || r > 'z') && r != '_' && (i == 0 || r < '0' || r > '9') {
			return fmt.Errorf("DuckLake relation namespace %q is not a canonical SQL identifier", value)
		}
	}
	return nil
}

func canonicalNativeObjects(root string, files CatalogFileSet) ([]NativeSnapshotObject, error) {
	canonicalRoot, err := CanonicalDataPath(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize expected object root: %w", err)
	}
	if err := validateNativeIdentityField("object root", canonicalRoot); err != nil {
		return nil, err
	}
	root = canonicalRoot
	if len(files.DataFiles) > NativeSnapshotClosureMaxEntries || len(files.DeleteFiles) > NativeSnapshotClosureMaxEntries-len(files.DataFiles) {
		return nil, fmt.Errorf("DuckLake object closure exceeds %d entries", NativeSnapshotClosureMaxEntries)
	}
	byPath := make(map[string]FileKind, len(files.DataFiles)+len(files.DeleteFiles))
	add := func(raw string, kind FileKind) error {
		canonical, err := canonicalNativeObjectPath(root, raw)
		if err != nil {
			return err
		}
		if previous, ok := byPath[canonical]; ok {
			if previous != kind {
				return fmt.Errorf("DuckLake object reference %q has conflicting kinds %q and %q", canonical, previous, kind)
			}
			return nil
		}
		byPath[canonical] = kind
		return nil
	}
	for _, raw := range files.DataFiles {
		if err := add(raw, DataFile); err != nil {
			return nil, err
		}
	}
	for _, raw := range files.DeleteFiles {
		if err := add(raw, DeleteFile); err != nil {
			return nil, err
		}
	}
	result := make([]NativeSnapshotObject, 0, len(byPath))
	for objectPath, kind := range byPath {
		result = append(result, NativeSnapshotObject{Kind: kind, Path: objectPath})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func validateNativeIdentityField(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("DuckLake %s identity is empty or not normalized", name)
	}
	if len(value) > NativeSnapshotClosureMaxFieldBytes {
		return fmt.Errorf("DuckLake %s identity exceeds %d bytes", name, NativeSnapshotClosureMaxFieldBytes)
	}
	for _, r := range value {
		if r == '\x00' || unicode.IsControl(r) {
			return fmt.Errorf("DuckLake %s identity contains a control character", name)
		}
	}
	return nil
}

func canonicalNativeObjectPath(root, raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("DuckLake object reference is empty or not normalized")
	}
	if len(raw) > NativeSnapshotClosureMaxFieldBytes {
		return "", fmt.Errorf("DuckLake object reference exceeds %d bytes", NativeSnapshotClosureMaxFieldBytes)
	}
	for _, r := range raw {
		if r == '\x00' || unicode.IsControl(r) {
			return "", fmt.Errorf("DuckLake object reference contains a control character")
		}
	}
	rootIsURL := strings.Contains(root, "://")
	if rootIsURL {
		return canonicalNativeURLObjectPath(root, raw)
	}
	candidate := raw
	if strings.Contains(raw, "://") {
		// CanonicalDataPath intentionally supports file:// URLs for local
		// DATA_PATH values. Other URL schemes cannot be rooted beneath a local
		// filesystem path.
		canonical, err := CanonicalDataPath(raw)
		if err != nil || strings.Contains(canonical, "://") {
			return "", fmt.Errorf("DuckLake object reference %q uses a different storage root", raw)
		}
		candidate = canonical
	} else if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	canonical, err := CanonicalDataPath(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, canonical)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("DuckLake object reference %q is outside expected object root", raw)
	}
	return canonical, nil
}

func canonicalNativeURLObjectPath(root, raw string) (string, error) {
	rootURL, err := url.Parse(root)
	if err != nil {
		return "", fmt.Errorf("parse expected object root: %w", err)
	}
	var candidate *url.URL
	if strings.Contains(raw, "://") {
		candidate, err = url.Parse(raw)
		if err != nil {
			return "", fmt.Errorf("parse DuckLake object reference: %w", err)
		}
	} else {
		if strings.HasPrefix(raw, "/") {
			return "", fmt.Errorf("DuckLake object reference %q is not a root-relative key", raw)
		}
		candidate = &url.URL{Scheme: rootURL.Scheme, Host: rootURL.Host, Path: path.Join(rootURL.Path, raw)}
	}
	if candidate.User != nil || candidate.RawQuery != "" || candidate.Fragment != "" {
		return "", fmt.Errorf("DuckLake object reference %q has unsupported URL components", raw)
	}
	candidate.Scheme = strings.ToLower(candidate.Scheme)
	candidate.Host = strings.ToLower(candidate.Host)
	if candidate.Scheme != rootURL.Scheme || candidate.Host != rootURL.Host {
		return "", fmt.Errorf("DuckLake object reference %q is outside expected object root", raw)
	}
	candidate.Path = path.Clean(candidate.Path)
	candidate.RawPath = ""
	rootPath := path.Clean(rootURL.Path)
	if rootPath == "." {
		rootPath = ""
	}
	if rootPath == "/" {
		if candidate.Path == "/" {
			return "", fmt.Errorf("DuckLake object reference %q names the object root", raw)
		}
	} else if candidate.Path == rootPath || !strings.HasPrefix(candidate.Path, rootPath+"/") {
		return "", fmt.Errorf("DuckLake object reference %q is outside expected object root", raw)
	}
	return candidate.String(), nil
}

func cloneBaseTables(input []BaseTable) []BaseTable {
	if input == nil {
		return nil
	}
	result := make([]BaseTable, len(input))
	copy(result, input)
	return result
}

func cloneNativeObjects(input []NativeSnapshotObject) []NativeSnapshotObject {
	if input == nil {
		return nil
	}
	result := make([]NativeSnapshotObject, len(input))
	copy(result, input)
	return result
}
