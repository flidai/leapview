package observation

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/pkg/strictjson"
)

// Validate checks the complete captured contract. In particular, it verifies
// that the provider observations form a bijection with the files in the
// ready-revision inventory. It does not contact a provider and does not make
// an authenticity or recovery-frontier claim.
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported manifest schema version %d", ErrInvalid, m.SchemaVersion)
	}
	if err := m.Boundary.Validate(); err != nil {
		return err
	}
	if err := m.Inventory.Validate(); err != nil {
		return err
	}
	inventoryDigest, err := m.Inventory.Digest()
	if err != nil {
		return err
	}
	if m.Boundary.InventoryDigest != inventoryDigest {
		return fmt.Errorf("%w: boundary inventory digest does not match inventory", ErrInvalid)
	}
	if m.Objects == nil {
		return fmt.Errorf("%w: manifest objects must be an explicit array", ErrInvalid)
	}
	if len(m.Objects) > MaxManifestObjects {
		return fmt.Errorf("%w: provider observation count exceeds limit %d", ErrInvalid, MaxManifestObjects)
	}

	files := make(map[string]File)
	for _, revision := range m.Inventory.Revisions {
		for _, file := range revision.Files {
			files[observationKey(revision.RevisionID, file.Path)] = file
		}
	}
	observed := make(map[string]struct{}, len(m.Objects))
	providerObjects := make(map[string]providerObservation, len(m.Objects))
	for index, observation := range m.Objects {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation %d: %v", ErrInvalid, index, err)
		}
		key := observationKey(observation.RevisionID, observation.Path)
		if _, duplicate := observed[key]; duplicate {
			return fmt.Errorf("%w: duplicate observation for revision %q path %q", ErrInvalid, observation.RevisionID, observation.Path)
		}
		observed[key] = struct{}{}
		file, exists := files[key]
		if !exists {
			return fmt.Errorf("%w: observation %q/%q has no inventory file", ErrInvalid, observation.RevisionID, observation.Path)
		}
		bucket, objectKey, err := storageKeyParts(file.StorageKey)
		if err != nil {
			return err
		}
		object := observation.Object
		if object.Bucket != bucket || object.Key != objectKey {
			return fmt.Errorf("%w: observation %q/%q does not match storage key", ErrInvalid, observation.RevisionID, observation.Path)
		}
		if object.SHA256 != file.SHA256 {
			return fmt.Errorf("%w: observation %q/%q hash does not match inventory", ErrInvalid, observation.RevisionID, observation.Path)
		}
		if object.Size != file.Size {
			return fmt.Errorf("%w: observation %q/%q size does not match inventory", ErrInvalid, observation.RevisionID, observation.Path)
		}
		providerKey := providerObjectIdentity(object)
		if previous, exists := providerObjects[providerKey]; exists && (previous.SHA256 != object.SHA256 || previous.Size != object.Size) {
			return fmt.Errorf("%w: provider object %q is observed with conflicting bytes", ErrInvalid, providerKey)
		}
		providerObjects[providerKey] = providerObservation{SHA256: object.SHA256, Size: object.Size}
	}
	if len(observed) != len(files) {
		return fmt.Errorf("%w: inventory has %d files but manifest has %d observations", ErrInvalid, len(files), len(observed))
	}
	missing := make([]string, 0)
	for key := range files {
		if _, exists := observed[key]; !exists {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: inventory file %q has no provider observation", ErrInvalid, missing[0])
	}
	return nil
}

// Normalize returns a copy in canonical ordering. Duplicate identities are
// rejected by Validate before any sorting occurs.
func (m Manifest) Normalize() (Manifest, error) {
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	normalized := m
	boundary, err := m.Boundary.normalize()
	if err != nil {
		return Manifest{}, err
	}
	normalized.Boundary = boundary
	inventory, err := m.Inventory.Normalize()
	if err != nil {
		return Manifest{}, err
	}
	normalized.Inventory = inventory
	normalized.Objects = make([]Observation, len(m.Objects))
	copy(normalized.Objects, m.Objects)
	sort.Slice(normalized.Objects, func(i, j int) bool {
		left, right := normalized.Objects[i], normalized.Objects[j]
		if left.RevisionID != right.RevisionID {
			return left.RevisionID < right.RevisionID
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		return providerObjectKey(left.Object) < providerObjectKey(right.Object)
	})
	return normalized, nil
}

func (i Inventory) Validate() error {
	if i.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported inventory schema version %d", ErrInvalid, i.SchemaVersion)
	}
	if i.Revisions == nil {
		return fmt.Errorf("%w: inventory revisions must be an explicit array", ErrInvalid)
	}
	if len(i.Revisions) > MaxInventoryRevisions {
		return fmt.Errorf("%w: revision count exceeds limit %d", ErrInvalid, MaxInventoryRevisions)
	}
	seen := make(map[string]struct{}, len(i.Revisions))
	for index, revision := range i.Revisions {
		if _, duplicate := seen[revision.RevisionID]; duplicate {
			return fmt.Errorf("%w: duplicate revision ID %q", ErrInvalid, revision.RevisionID)
		}
		seen[revision.RevisionID] = struct{}{}
		if err := revision.Validate(); err != nil {
			return fmt.Errorf("%w: revision %d: %v", ErrInvalid, index, err)
		}
	}
	return nil
}

func (i Inventory) Normalize() (Inventory, error) {
	if err := i.Validate(); err != nil {
		return Inventory{}, err
	}
	normalized := Inventory{SchemaVersion: i.SchemaVersion, Revisions: make([]Revision, len(i.Revisions))}
	for index, revision := range i.Revisions {
		normalized.Revisions[index] = revision
		normalized.Revisions[index].Files = make([]File, len(revision.Files))
		copy(normalized.Revisions[index].Files, revision.Files)
		sort.Slice(normalized.Revisions[index].Files, func(left, right int) bool {
			return normalized.Revisions[index].Files[left].Path < normalized.Revisions[index].Files[right].Path
		})
	}
	sort.Slice(normalized.Revisions, func(left, right int) bool {
		return normalized.Revisions[left].RevisionID < normalized.Revisions[right].RevisionID
	})
	return normalized, nil
}

func (i Inventory) CanonicalJSON() ([]byte, error) {
	normalized, err := i.Normalize()
	if err != nil {
		return nil, err
	}
	return marshalBounded(normalized)
}

func (i Inventory) Digest() (string, error) {
	canonical, err := i.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return sha256Digest(canonical), nil
}

func (r Revision) Validate() error {
	if _, err := manageddata.ParseRevisionID(r.RevisionID); err != nil {
		return fmt.Errorf("%w: revision ID: %v", ErrInvalid, err)
	}
	if err := validateDigest(r.ManifestDigest, "revision manifest digest"); err != nil {
		return err
	}
	if r.Files == nil {
		return fmt.Errorf("%w: revision %q files must be an explicit array", ErrInvalid, r.RevisionID)
	}
	if len(r.Files) > MaxRevisionFiles {
		return fmt.Errorf("%w: revision %q file count exceeds limit %d", ErrInvalid, r.RevisionID, MaxRevisionFiles)
	}
	exactSeen := make(map[string]struct{}, len(r.Files))
	foldedSeen := make(map[string]struct{}, len(r.Files))
	managedFiles := make([]manageddata.File, 0, len(r.Files))
	for index, file := range r.Files {
		if _, duplicate := exactSeen[file.Path]; duplicate {
			return fmt.Errorf("%w: revision %q has duplicate file path %q", ErrInvalid, r.RevisionID, file.Path)
		}
		folded := strings.ToLower(file.Path)
		if _, duplicate := foldedSeen[folded]; duplicate {
			return fmt.Errorf("%w: revision %q has case-folded file path collision at %q", ErrInvalid, r.RevisionID, file.Path)
		}
		exactSeen[file.Path] = struct{}{}
		foldedSeen[folded] = struct{}{}
		if _, err := canonicalText(file.Path, "revision file path", 1024); err != nil {
			return fmt.Errorf("%w: revision %q file %d: %v", ErrInvalid, r.RevisionID, index, err)
		}
		if err := validateRawHash(file.SHA256, "revision file SHA-256"); err != nil {
			return err
		}
		if _, err := canonicalText(file.StorageKey, "revision file storage key", 2048); err != nil {
			return err
		}
		if _, _, err := storageKeyParts(file.StorageKey); err != nil {
			return err
		}
		if file.Size < 0 {
			return fmt.Errorf("%w: revision file size must be nonnegative", ErrInvalid)
		}
		managedFiles = append(managedFiles, manageddata.File{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
	}
	managedManifest := manageddata.Manifest{Files: managedFiles}
	if err := managedManifest.Validate(manageddata.Limits{MaxFiles: MaxRevisionFiles}); err != nil {
		return fmt.Errorf("%w: revision manifest: %v", ErrInvalid, err)
	}
	if managedManifest.RevisionID() != r.ManifestDigest {
		return fmt.Errorf("%w: revision %q manifest digest does not match files", ErrInvalid, r.RevisionID)
	}
	return nil
}

func (r Revision) Normalize() (Revision, error) {
	if err := r.Validate(); err != nil {
		return Revision{}, err
	}
	normalized := r
	normalized.Files = make([]File, len(r.Files))
	copy(normalized.Files, r.Files)
	sort.Slice(normalized.Files, func(left, right int) bool {
		return normalized.Files[left].Path < normalized.Files[right].Path
	})
	return normalized, nil
}

func (f File) Validate() error {
	if _, err := canonicalText(f.Path, "file path", 1024); err != nil {
		return err
	}
	if err := validateRawHash(f.SHA256, "file SHA-256"); err != nil {
		return err
	}
	if _, err := canonicalText(f.StorageKey, "file storage key", 2048); err != nil {
		return err
	}
	if _, _, err := storageKeyParts(f.StorageKey); err != nil {
		return err
	}
	if f.Size < 0 {
		return fmt.Errorf("%w: file size must be nonnegative", ErrInvalid)
	}
	return nil
}

// Parse strictly decodes one bounded observation manifest and validates all
// cross-object identities before returning it.
func Parse(raw []byte) (Manifest, error) {
	if len(raw) < 2 || len(raw) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest JSON is out of bounds", ErrInvalid)
	}
	var manifest Manifest
	if err := strictjson.DecodeWithOptions(raw, &manifest, strictjson.Options{
		MaxBytes: MaxManifestBytes, MaxDepth: 32,
		DuplicateKeys: strictjson.CaseFoldedKeys, AllowUnknownFields: false,
	}); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalid, err)
	}
	if err := requireExactJSONShape(raw, manifest); err != nil {
		return Manifest{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ParseManifest is an explicit alias for callers that prefer a type-specific
// parser name.
func ParseManifest(raw []byte) (Manifest, error) { return Parse(raw) }

func (m Manifest) CanonicalJSON() ([]byte, error) {
	normalized, err := m.Normalize()
	if err != nil {
		return nil, err
	}
	return marshalBounded(normalized)
}

func (m Manifest) Digest() (string, error) {
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return sha256Digest(canonical), nil
}

func observationKey(revisionID, path string) string { return revisionID + "\x00" + path }

func providerObjectKey(object ProviderObject) string {
	return object.Endpoint + "\x00" + object.Region + "\x00" + object.Bucket + "\x00" + object.Key + "\x00" + object.VersionID + "\x00" + object.SHA256 + fmt.Sprintf("\x00%d", object.Size)
}

type providerObservation struct {
	SHA256 string
	Size   int64
}

func providerObjectIdentity(object ProviderObject) string {
	return object.Endpoint + "\x00" + object.Region + "\x00" + object.Bucket + "\x00" + object.Key + "\x00" + object.VersionID
}

func storageKeyParts(value string) (string, string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "s3" || u.User != nil || u.Host == "" || u.Path == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", fmt.Errorf("%w: storage key must be a credential-free s3 URI", ErrInvalid)
	}
	if u.RawPath != "" {
		return "", "", fmt.Errorf("%w: storage key must use canonical path encoding", ErrInvalid)
	}
	bucket := u.Host
	objectKey := strings.TrimPrefix(u.Path, "/")
	if objectKey == "" {
		return "", "", fmt.Errorf("%w: storage key must include an object key", ErrInvalid)
	}
	if _, err := canonicalName(bucket, "storage bucket"); err != nil {
		return "", "", err
	}
	if _, err := canonicalText(objectKey, "storage object key", 1024); err != nil {
		return "", "", err
	}
	return bucket, objectKey, nil
}

func marshalBounded(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: canonical JSON exceeds %d bytes", ErrInvalid, MaxManifestBytes)
	}
	return encoded, nil
}

// requireExactJSONShape prevents encoding/json's case-insensitive field
// matching and zero-value behavior from turning omitted, aliased, or null
// fields into an apparently valid contract. Values are intentionally ignored:
// semantic validation remains the responsibility of the typed Validate calls.
func requireExactJSONShape(raw []byte, decoded any) error {
	var supplied, expected any
	if err := json.Unmarshal(raw, &supplied); err != nil {
		return fmt.Errorf("%w: malformed JSON shape: %v", ErrInvalid, err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("%w: marshal JSON shape: %v", ErrInvalid, err)
	}
	if err := json.Unmarshal(encoded, &expected); err != nil {
		return fmt.Errorf("%w: decode JSON shape: %v", ErrInvalid, err)
	}
	if !sameJSONShape(supplied, expected) {
		return fmt.Errorf("%w: JSON fields do not match the exact observation schema", ErrInvalid)
	}
	return nil
}

func sameJSONShape(supplied, expected any) bool {
	switch value := expected.(type) {
	case map[string]any:
		got, ok := supplied.(map[string]any)
		if !ok || len(got) != len(value) {
			return false
		}
		for key, child := range value {
			actual, exists := got[key]
			if !exists || !sameJSONShape(actual, child) {
				return false
			}
		}
	case []any:
		got, ok := supplied.([]any)
		if !ok || len(got) != len(value) {
			return false
		}
		for index, child := range value {
			if !sameJSONShape(got[index], child) {
				return false
			}
		}
	default:
		if (expected == nil) != (supplied == nil) {
			return false
		}
	}
	return true
}
