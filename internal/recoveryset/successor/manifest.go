package successor

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
)

// ManagedManifest2 is the successor managed-data observation document. It
// retains every logical membership while allowing retrieval to deduplicate
// only after shared-object observations have been compared.
type ManagedManifest2 struct {
	ManifestVersion            int32      `json:"manifest_version"`
	SetID                      string     `json:"set_id"`
	SourceFrontierAnchorDigest string     `json:"source_frontier_anchor_digest"`
	Capture                    Capture    `json:"capture"`
	ManagedClosureDigest       string     `json:"managed_closure_digest"`
	Revisions                  []Revision `json:"revisions"`
}

type Capture struct {
	AuthorityID   string `json:"authority_id"`
	CaptureID     string `json:"capture_id"`
	StartedAt     string `json:"started_at"`
	CompletedAt   string `json:"completed_at"`
	ReceiptDigest string `json:"receipt_digest"`
}

type Revision struct {
	ProjectID              string `json:"project_id"`
	CollectionID           string `json:"collection_id"`
	RevisionID             string `json:"revision_id"`
	RevisionManifestDigest string `json:"revision_manifest_digest"`
	Files                  []File `json:"files"`
}

type File struct {
	Path     string           `json:"path"`
	SHA256   string           `json:"sha256"`
	Size     int64            `json:"size"`
	Provider ProviderIdentity `json:"provider"`
}

type ProviderIdentity struct {
	Implementation  string `json:"implementation"`
	AccountIdentity string `json:"account_identity"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	Key             string `json:"key"`
	VersionID       string `json:"version_id"`
}

// ExpectedScope is selected independently by the managed-data owner. The
// submitted manifest cannot establish scope merely by repeating these values.
// Revisions may be supplied as a provider-free trusted closure for a complete
// membership comparison; nil means the caller has independently checked the
// closure digest through its owner boundary.
type ExpectedScope struct {
	SetID                      string
	SourceFrontierAnchorDigest string
	ManagedClosureDigest       string
	Revisions                  []Revision
	EmptyScopeVerified         bool
}

func (m ManagedManifest2) Validate() error {
	if m.ManifestVersion != ManagedManifestVersion {
		return fmt.Errorf("%w: manifest version %d", ErrUnsupportedVersion, m.ManifestVersion)
	}
	if !canonicalUUID(m.SetID) {
		return invalid("manifest set_id must be a canonical UUID")
	}
	if !digest(m.SourceFrontierAnchorDigest) || !digest(m.ManagedClosureDigest) || !digest(m.Capture.ReceiptDigest) {
		return invalid("manifest digest fields are malformed")
	}
	if err := id(m.Capture.AuthorityID, "capture authority_id"); err != nil {
		return err
	}
	if err := id(m.Capture.CaptureID, "capture capture_id"); err != nil {
		return err
	}
	start, err := canonicalTime(m.Capture.StartedAt)
	if err != nil {
		return err
	}
	end, err := canonicalTime(m.Capture.CompletedAt)
	if err != nil || end.Before(start) {
		return invalid("capture chronology is invalid")
	}
	if m.Revisions == nil || len(m.Revisions) > MaxMembers {
		return invalid("revisions must be an explicit bounded array")
	}
	if _, err := canonicalRevisions(m.Revisions, true); err != nil {
		return err
	}
	return nil
}

func (m ManagedManifest2) Normalize() (ManagedManifest2, error) {
	if err := m.Validate(); err != nil {
		return ManagedManifest2{}, err
	}
	copy := m
	var err error
	copy.Revisions, err = canonicalRevisions(m.Revisions, true)
	if err != nil {
		return ManagedManifest2{}, err
	}
	return copy, nil
}

func (m ManagedManifest2) CanonicalJSON() ([]byte, error) {
	normalized, err := m.Normalize()
	if err != nil {
		return nil, err
	}
	closure, err := closureDigest(normalized.Revisions)
	if err != nil {
		return nil, err
	}
	if closure != normalized.ManagedClosureDigest {
		return nil, invalid("managed closure digest does not match revisions")
	}
	return marshal(normalized, MaxDocumentBytes)
}

func (m ManagedManifest2) Digest() (string, error) {
	raw, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return hash(manifestDomain, raw), nil
}

func (m ManagedManifest2) ObservationProjectionJSON() ([]byte, error) {
	normalized, err := m.Normalize()
	if err != nil {
		return nil, err
	}
	return marshal(normalized.Revisions, MaxDocumentBytes)
}

func (m ManagedManifest2) ObservationProjectionDigest() (string, error) {
	raw, err := m.ObservationProjectionJSON()
	if err != nil {
		return "", err
	}
	return hash(projectionDomain, raw), nil
}

func (m ManagedManifest2) ObservationCounts() (memberships, objects int64, err error) {
	normalized, err := m.Normalize()
	if err != nil {
		return 0, 0, err
	}
	seen := make(map[string]struct{})
	for _, revision := range normalized.Revisions {
		memberships += int64(len(revision.Files))
		for _, file := range revision.Files {
			seen[physicalIdentity(file.Provider)] = struct{}{}
		}
	}
	return memberships, int64(len(seen)), nil
}

// ClosureDigest commits membership/content but intentionally excludes provider
// observations. It is useful when the independently selected closure is owned
// by a database boundary rather than this package.
func ClosureDigest(revisions []Revision) (string, error) { return closureDigest(revisions) }

func closureDigest(revisions []Revision) (string, error) {
	sorted, err := canonicalRevisions(revisions, false)
	if err != nil {
		return "", err
	}
	type closureFile struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	type closureRevision struct {
		ProjectID              string        `json:"project_id"`
		CollectionID           string        `json:"collection_id"`
		RevisionID             string        `json:"revision_id"`
		RevisionManifestDigest string        `json:"revision_manifest_digest"`
		Files                  []closureFile `json:"files"`
	}
	projection := make([]closureRevision, 0, len(sorted))
	for _, revision := range sorted {
		entry := closureRevision{ProjectID: revision.ProjectID, CollectionID: revision.CollectionID, RevisionID: revision.RevisionID, RevisionManifestDigest: revision.RevisionManifestDigest, Files: make([]closureFile, 0, len(revision.Files))}
		for _, file := range revision.Files {
			entry.Files = append(entry.Files, closureFile{file.Path, file.SHA256, file.Size})
		}
		projection = append(projection, entry)
	}
	raw, err := marshal(projection, MaxDocumentBytes)
	if err != nil {
		return "", err
	}
	return hash(closureDomain, raw), nil
}

func canonicalRevisions(input []Revision, observations bool) ([]Revision, error) {
	if input == nil || len(input) > MaxMembers {
		return nil, invalid("revision scope must be explicit and bounded")
	}
	result := make([]Revision, len(input))
	seenRevision := make(map[string]struct{})
	seenMembership := make(map[string]struct{})
	type observed struct {
		Provider ProviderIdentity
		SHA256   string
		Size     int64
	}
	objects := make(map[string]observed)
	count := 0
	for i, revision := range input {
		if err := id(revision.ProjectID, "revision project_id"); err != nil {
			return nil, err
		}
		if err := id(revision.CollectionID, "revision collection_id"); err != nil {
			return nil, err
		}
		if err := id(revision.RevisionID, "revision revision_id"); err != nil {
			return nil, err
		}
		if !digest(revision.RevisionManifestDigest) {
			return nil, invalid("revision manifest digest is malformed")
		}
		revisionKey := revision.ProjectID + "\x00" + revision.CollectionID + "\x00" + revision.RevisionID
		if _, duplicate := seenRevision[revisionKey]; duplicate {
			return nil, invalid("duplicate revision")
		}
		seenRevision[revisionKey] = struct{}{}
		if revision.Files == nil {
			return nil, invalid("revision files must be an explicit array")
		}
		count += len(revision.Files)
		if count > MaxMembers {
			return nil, invalid("file membership limit exceeded")
		}
		ownerFiles := make([]manageddata.File, 0, len(revision.Files))
		exactPaths, foldedPaths := map[string]struct{}{}, map[string]struct{}{}
		revision.Files = make([]File, len(revision.Files))
		copy(revision.Files, input[i].Files)
		for j, file := range revision.Files {
			if err := validateFile(file); err != nil {
				return nil, fmt.Errorf("%w: revision %d file %d: %v", ErrInvalid, i, j, err)
			}
			if _, exists := exactPaths[file.Path]; exists {
				return nil, invalid("duplicate file membership path")
			}
			exactPaths[file.Path] = struct{}{}
			folded := strings.ToLower(file.Path)
			if _, exists := foldedPaths[folded]; exists {
				return nil, invalid("case-folded file path collision")
			}
			foldedPaths[folded] = struct{}{}
			ownerFiles = append(ownerFiles, manageddata.File{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
			if observations {
				provider := canonicalProvider(file.Provider)
				if provider.Endpoint == "" {
					return nil, invalid("provider observation is invalid")
				}
				key := physicalIdentity(provider)
				if previous, exists := objects[key]; exists && (previous.Provider.VersionID != provider.VersionID || previous.Provider.Region != provider.Region || previous.SHA256 != file.SHA256 || previous.Size != file.Size) {
					return nil, invalid("shared physical object has conflicting observation")
				}
				objects[key] = observed{Provider: provider, SHA256: file.SHA256, Size: file.Size}
				revision.Files[j].Provider = provider
			}
		}
		owner := manageddata.Manifest{Files: ownerFiles}
		if err := owner.Validate(manageddata.Limits{}); err != nil || owner.RevisionID() != revision.RevisionManifestDigest {
			return nil, invalid("revision manifest does not match file membership")
		}
		sort.Slice(revision.Files, func(a, b int) bool { return revision.Files[a].Path < revision.Files[b].Path })
		for _, file := range revision.Files {
			key := revisionKey + "\x00" + file.Path
			if _, duplicate := seenMembership[key]; duplicate {
				return nil, invalid("duplicate logical membership")
			}
			seenMembership[key] = struct{}{}
		}
		result[i] = revision
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ProjectID != result[j].ProjectID {
			return result[i].ProjectID < result[j].ProjectID
		}
		if result[i].CollectionID != result[j].CollectionID {
			return result[i].CollectionID < result[j].CollectionID
		}
		return result[i].RevisionID < result[j].RevisionID
	})
	return result, nil
}

func validateFile(file File) error {
	if err := text(file.Path, "file path", MaxTextBytes); err != nil {
		return err
	}
	if err := (manageddata.Manifest{Files: []manageddata.File{{Path: file.Path, SHA256: file.SHA256, Size: file.Size}}}).Validate(manageddata.Limits{}); err != nil {
		return err
	}
	if file.Size < 0 {
		return invalid("file size must be nonnegative")
	}
	return nil
}

func canonicalProvider(provider ProviderIdentity) ProviderIdentity {
	if provider.Implementation != "s3" || text(provider.AccountIdentity, "provider account", MaxTextBytes) != nil || text(provider.Region, "provider region", MaxTextBytes) != nil || text(provider.Bucket, "provider bucket", 63) != nil || text(provider.Key, "provider key", MaxTextBytes) != nil || text(provider.VersionID, "provider version", MaxTextBytes) != nil || strings.EqualFold(provider.VersionID, "null") {
		return ProviderIdentity{}
	}
	if !validBucket(provider.Bucket) || (provider.Prefix != "" && (text(provider.Prefix, "provider prefix", MaxTextBytes) != nil || strings.HasSuffix(provider.Prefix, "/") || !strings.HasPrefix(provider.Key, provider.Prefix+"/"))) {
		return ProviderIdentity{}
	}
	endpoint, err := canonicalEndpoint(provider.Endpoint)
	if err != nil {
		return ProviderIdentity{}
	}
	provider.Endpoint = endpoint
	return provider
}

func validateProvider(provider ProviderIdentity) error {
	canonical := canonicalProvider(provider)
	if canonical == (ProviderIdentity{}) || canonical != provider {
		return invalid("provider observation is not canonical")
	}
	return nil
}

func physicalIdentity(provider ProviderIdentity) string {
	return provider.Implementation + "\x00" + provider.AccountIdentity + "\x00" + provider.Endpoint + "\x00" + provider.Bucket + "\x00" + provider.Key
}

func validBucket(value string) bool {
	if len(value) < 3 || len(value) > 63 || strings.Contains(value, "..") || value[0] == '-' || value[0] == '.' || value[len(value)-1] == '-' || value[len(value)-1] == '.' || net.ParseIP(value) != nil {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

func canonicalEndpoint(value string) (string, error) {
	if err := text(value, "provider endpoint", 2048); err != nil {
		return "", err
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Host == "" {
		return "", invalid("provider endpoint must be credential-free HTTPS")
	}
	host := u.Hostname()
	if host == "" || strings.HasSuffix(host, ".") {
		return "", invalid("provider endpoint host")
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
	} else {
		if host != strings.ToLower(host) || !validHost(host) {
			return "", invalid("provider endpoint DNS host")
		}
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", invalid("provider endpoint port")
		}
		if n == 443 {
			port = ""
		}
	}
	u.Host = host
	if port != "" {
		u.Host += ":" + port
	}
	if u.String() != value {
		return "", invalid("provider endpoint is not canonical")
	}
	return value, nil
}

func validHost(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func (m ManagedManifest2) ValidateFor(scope ExpectedScope) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if scope.SetID != m.SetID || scope.SourceFrontierAnchorDigest != m.SourceFrontierAnchorDigest || scope.ManagedClosureDigest != m.ManagedClosureDigest {
		return invalid("manifest does not match independently selected scope")
	}
	if scope.Revisions != nil {
		want, err := closureDigest(scope.Revisions)
		if err != nil {
			return err
		}
		if want != m.ManagedClosureDigest {
			return invalid("manifest closure differs from selected revisions")
		}
		if !sameClosure(scope.Revisions, m.Revisions) {
			return invalid("manifest membership differs from selected revisions")
		}
	}
	empty := true
	for _, revision := range m.Revisions {
		if len(revision.Files) != 0 {
			empty = false
			break
		}
	}
	if empty != scope.EmptyScopeVerified {
		return invalid("empty scope was not independently verified")
	}
	return nil
}

func sameClosure(left, right []Revision) bool {
	a, errA := canonicalRevisions(left, false)
	b, errB := canonicalRevisions(right, false)
	if errA != nil || errB != nil || len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ProjectID != b[i].ProjectID || a[i].CollectionID != b[i].CollectionID || a[i].RevisionID != b[i].RevisionID || a[i].RevisionManifestDigest != b[i].RevisionManifestDigest || len(a[i].Files) != len(b[i].Files) {
			return false
		}
		for j := range a[i].Files {
			if a[i].Files[j].Path != b[i].Files[j].Path || a[i].Files[j].SHA256 != b[i].Files[j].SHA256 || a[i].Files[j].Size != b[i].Files[j].Size {
				return false
			}
		}
	}
	return true
}

func ParseManagedManifest2(raw []byte) (ManagedManifest2, error) {
	var manifest ManagedManifest2
	if err := strictDecode(raw, &manifest, MaxDocumentBytes); err != nil {
		return manifest, err
	}
	if _, err := exactObject(raw, map[string]bool{"manifest_version": true, "set_id": true, "source_frontier_anchor_digest": true, "capture": true, "managed_closure_digest": true, "revisions": true}, map[string]bool{"manifest_version": true, "set_id": true, "source_frontier_anchor_digest": true, "capture": true, "managed_closure_digest": true, "revisions": true}, "manifest"); err != nil {
		return manifest, err
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		return manifest, err
	}
	if !bytes.Equal(raw, canonical) {
		return manifest, invalid("manifest is not canonical JSON")
	}
	return manifest, nil
}

// Keep the provider-bearing projection's content conflict checks separate from
// closure checks so a caller cannot hide a divergent shared version by changing
// the logical membership that points to it.
func (m ManagedManifest2) validateObservations() error {
	normalized, err := m.Normalize()
	if err != nil {
		return err
	}
	for _, revision := range normalized.Revisions {
		for _, file := range revision.Files {
			if err := validateProvider(file.Provider); err != nil {
				return err
			}
		}
	}
	return nil
}

// ProviderObservationDigest is an explicit alias for the complete projection
// identity used by capture receipts.
func (m ManagedManifest2) ProviderObservationDigest() (string, error) {
	return m.ObservationProjectionDigest()
}

// RevisionFileDigest is useful to callers constructing independent closure
// expectations without exposing a second owner of managed-data hashes.
func RevisionFileDigest(files []File) string {
	owner := manageddata.Manifest{Files: make([]manageddata.File, 0, len(files))}
	for _, file := range files {
		owner.Files = append(owner.Files, manageddata.File{Path: file.Path, SHA256: file.SHA256, Size: file.Size})
	}
	return owner.RevisionID()
}
