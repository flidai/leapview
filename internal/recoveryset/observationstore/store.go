// Package observationstore persists provider observation evidence outside the
// control database.  It is deliberately an evidence store only: it does not
// publish a recovery frontier or participate in restore/startup.
package observationstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/flidai/leapview/internal/recoveryset"
	"github.com/flidai/leapview/internal/recoveryset/observation"
	postgrescapture "github.com/flidai/leapview/internal/recoveryset/postgres"
)

const (
	SchemaVersion           int32 = 1
	DefaultMaxSourceBytes         = 8 << 30
	DefaultOperationTimeout       = 5 * time.Minute
	maxDescriptorBytes            = 8 << 20
	maxFrontierBytes              = 1 << 20
)

var (
	ErrInvalid   = errors.New("observation store input is invalid")
	ErrBackend   = errors.New("observation store backend failed")
	ErrIntegrity = errors.New("observation store integrity check failed")
	ErrNotFound  = errors.New("observation store object not found")
	ErrConflict  = errors.New("observation store immutable object conflicts")
	ErrRetention = errors.New("observation store retention requirement failed")
)

// Client is intentionally the small set of pinned S3 operations required by
// this store.  *s3.Client satisfies it; keeping the interface here also makes
// protocol regressions testable without weakening production verification.
type Client interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	GetObjectRetention(context.Context, *s3.GetObjectRetentionInput, ...func(*s3.Options)) (*s3.GetObjectRetentionOutput, error)
}

// Config is all provider namespace authority for an evidence store.  Endpoint
// and Region are checked against observations, but requests always use the
// client supplied at construction; caller-provided observation endpoints are
// never used to choose a client or route a request.
type Config struct {
	Endpoint       string
	Region         string
	SourceBucket   string
	EvidenceBucket string
	Prefix         string

	// MaxSourceBytes bounds one streamed source-object verification.  The
	// default is the qualification ceiling; callers may lower it to bound
	// provider responses more tightly.
	MaxSourceBytes int64

	// Clock is an optional trusted clock for deterministic qualification tests.
	// Callers cannot supply a reload time that bypasses this clock.
	Clock func() time.Time

	OperationTimeout time.Duration
}

type Store struct {
	client           Client
	endpoint         string
	region           string
	sourceBucket     string
	evidenceBucket   string
	prefix           string
	maxSourceBytes   int64
	clock            func() time.Time
	operationTimeout time.Duration
}

// New constructs a store with one already configured/pinned AWS client.
func New(client Client, config Config) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: S3 client is required", ErrInvalid)
	}
	endpoint, err := validateEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	region, err := validateText(config.Region, "region", 128)
	if err != nil {
		return nil, err
	}
	source, err := validateBucket(config.SourceBucket, "source bucket")
	if err != nil {
		return nil, err
	}
	evidence, err := validateBucket(config.EvidenceBucket, "evidence bucket")
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(config.Prefix, "/")
	if len(prefix) > 1024 || strings.ContainsAny(prefix, "\x00\r\n") {
		return nil, fmt.Errorf("%w: evidence prefix is invalid", ErrInvalid)
	}
	maxSource := config.MaxSourceBytes
	if maxSource == 0 {
		maxSource = DefaultMaxSourceBytes
	}
	if maxSource <= 0 || maxSource > DefaultMaxSourceBytes {
		return nil, fmt.Errorf("%w: maximum source object size is outside qualification bounds", ErrInvalid)
	}
	timeout := config.OperationTimeout
	if timeout == 0 {
		timeout = DefaultOperationTimeout
	}
	if timeout <= 0 || timeout > 24*time.Hour {
		return nil, fmt.Errorf("%w: operation timeout is invalid", ErrInvalid)
	}
	return &Store{client: client, endpoint: endpoint, region: region, sourceBucket: source,
		evidenceBucket: evidence, prefix: prefix, maxSourceBytes: maxSource, clock: config.Clock, operationTimeout: timeout}, nil
}

// ObjectRef identifies one immutable evidence object.  VersionID is required
// even though the content-addressed key already includes SHA256: key identity
// and provider version identity are separate checks.
type ObjectRef struct {
	Bucket    string `json:"bucket"`
	Key       string `json:"key"`
	VersionID string `json:"version_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

func (r ObjectRef) Validate() error {
	if _, err := validateBucket(r.Bucket, "object bucket"); err != nil {
		return err
	}
	if _, err := validateText(r.Key, "object key", 2048); err != nil {
		return err
	}
	if r.VersionID == "" || r.VersionID != strings.TrimSpace(r.VersionID) || strings.EqualFold(r.VersionID, "null") || len(r.VersionID) > 1024 || !utf8.ValidString(r.VersionID) || strings.IndexFunc(r.VersionID, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: object version ID is invalid", ErrInvalid)
	}
	if !rawSHA256(r.SHA256) {
		return fmt.Errorf("%w: object SHA-256 is invalid", ErrInvalid)
	}
	if r.Size < 0 {
		return fmt.Errorf("%w: object size is negative", ErrInvalid)
	}
	return nil
}

func (r ObjectRef) equal(other ObjectRef) bool { return r == other }

// Ref is the portable handle returned by Save and accepted by Reload.  It
// contains no endpoint or client, so a separately configured client can reload
// it while the store retains routing authority.
type Ref struct {
	SchemaVersion int32     `json:"schema_version"`
	Manifest      ObjectRef `json:"manifest"`
	Boundary      ObjectRef `json:"boundary"`
	Descriptor    ObjectRef `json:"descriptor"`
}

func (r Ref) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported evidence reference schema version %d", ErrInvalid, r.SchemaVersion)
	}
	for _, object := range []ObjectRef{r.Manifest, r.Boundary, r.Descriptor} {
		if err := object.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Evidence is the fully verified observation bundle returned by Reload.
type Evidence struct {
	Manifest   observation.Manifest
	Boundary   observation.Boundary
	Descriptor Descriptor
	Ref        Ref
}

// Descriptor is the immutable, non-circular evidence index.  It binds the
// exact manifest and boundary object refs, source bytes/protections, and the
// required horizon.  Its own digest/ref intentionally do not appear here.
type Descriptor struct {
	SchemaVersion       int32                    `json:"schema_version"`
	ManifestDigest      string                   `json:"manifest_digest"`
	BoundaryDigest      string                   `json:"boundary_digest"`
	Manifest            ObjectRef                `json:"manifest"`
	Boundary            ObjectRef                `json:"boundary"`
	RequiredRetainUntil time.Time                `json:"required_retain_until"`
	SourceProtections   []observation.Protection `json:"source_protections"`
}

func (d Descriptor) Validate() error {
	if d.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported descriptor schema version %d", ErrInvalid, d.SchemaVersion)
	}
	if !digest(d.ManifestDigest) || !digest(d.BoundaryDigest) {
		return fmt.Errorf("%w: descriptor digests are invalid", ErrInvalid)
	}
	if err := d.Manifest.Validate(); err != nil {
		return err
	}
	if err := d.Boundary.Validate(); err != nil {
		return err
	}
	if d.Manifest.SHA256 != rawDigest(d.ManifestDigest) || d.Boundary.SHA256 != rawDigest(d.BoundaryDigest) {
		return fmt.Errorf("%w: descriptor object ref digest mismatch", ErrIntegrity)
	}
	if d.RequiredRetainUntil.IsZero() {
		return fmt.Errorf("%w: descriptor retention horizon is required", ErrInvalid)
	}
	if d.SourceProtections == nil || len(d.SourceProtections) > observation.MaxManifestObjects {
		return fmt.Errorf("%w: descriptor source protections must be an explicit bounded array", ErrInvalid)
	}
	for _, protection := range d.SourceProtections {
		if err := protection.ValidateAt(d.RequiredRetainUntil); err != nil {
			return err
		}
	}
	return nil
}

func (d Descriptor) CanonicalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	copyOf := d
	copyOf.RequiredRetainUntil = copyOf.RequiredRetainUntil.UTC()
	if d.SourceProtections != nil {
		copyOf.SourceProtections = make([]observation.Protection, len(d.SourceProtections))
		copy(copyOf.SourceProtections, d.SourceProtections)
		for i := range copyOf.SourceProtections {
			copyOf.SourceProtections[i].RetainUntil = copyOf.SourceProtections[i].RetainUntil.UTC()
		}
		sort.Slice(copyOf.SourceProtections, func(i, j int) bool {
			left, right := copyOf.SourceProtections[i], copyOf.SourceProtections[j]
			if keyLeft, keyRight := providerKey(left.Object), providerKey(right.Object); keyLeft != keyRight {
				return keyLeft < keyRight
			}
			if left.Mode != right.Mode {
				return left.Mode < right.Mode
			}
			return left.RetainUntil.Before(right.RetainUntil)
		})
	}
	// Reload additionally compares protections as a keyed set, so an
	// independently authored descriptor cannot smuggle duplicate identities
	// through ordering.
	return json.Marshal(copyOf)
}

func (d Descriptor) Digest() (string, error) {
	b, err := d.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return digestBytes(b), nil
}

// Save verifies the exact captured source closure, then writes canonical
// manifest, boundary, and descriptor objects with compliance retention.
// captured is intentionally the opaque adapter type; no caller-supplied
// boundary or inventory is accepted.
func (s *Store) Save(ctx context.Context, captured *postgrescapture.CapturedObservation, observations []observation.Observation, until time.Time) (Ref, error) {
	if err := validContext(ctx); err != nil {
		return Ref{}, err
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if captured == nil {
		return Ref{}, fmt.Errorf("%w: trusted captured observation is required", ErrInvalid)
	}
	if until.IsZero() {
		return Ref{}, fmt.Errorf("%w: retention horizon is required", ErrInvalid)
	}
	until = ceilSecond(until.UTC())
	if !until.After(s.now()) {
		return Ref{}, fmt.Errorf("%w: retention horizon must be in the future", ErrInvalid)
	}
	boundary := captured.Boundary()
	inventory := captured.Inventory()
	objects := append([]observation.Observation(nil), observations...)
	if objects == nil {
		objects = make([]observation.Observation, 0)
	}
	manifest := observation.Manifest{SchemaVersion: observation.SchemaVersion, Boundary: boundary, Inventory: inventory, Objects: objects}
	normalized, err := manifest.Normalize()
	if err != nil {
		return Ref{}, err
	}
	for _, item := range normalized.Objects {
		if item.Object.Endpoint != s.endpoint || item.Object.Region != s.region || item.Object.Bucket != s.sourceBucket {
			return Ref{}, fmt.Errorf("%w: observation provider namespace does not match the configured source", ErrInvalid)
		}
	}
	canonicalManifest, err := normalized.CanonicalJSON()
	if err != nil {
		return Ref{}, err
	}
	canonicalBoundary, err := normalized.Boundary.CanonicalJSON()
	if err != nil {
		return Ref{}, err
	}
	if len(canonicalManifest) > observation.MaxManifestBytes || len(canonicalBoundary) > observation.MaxManifestBytes {
		return Ref{}, fmt.Errorf("%w: evidence document exceeds bounded size", ErrInvalid)
	}

	protections := make([]observation.Protection, 0, len(normalized.Objects))
	for _, item := range normalized.Objects {
		protection, err := s.verifySource(ctx, item.Object, until)
		if err != nil {
			return Ref{}, err
		}
		protections = append(protections, protection)
	}
	manifestDigest := normalizedDigest(canonicalManifest)
	boundaryDigest := normalizedDigest(canonicalBoundary)
	manifestRef, err := s.putEvidence(ctx, "manifest", manifestDigest, canonicalManifest, until)
	if err != nil {
		return Ref{}, err
	}
	boundaryRef, err := s.putEvidence(ctx, "boundary", boundaryDigest, canonicalBoundary, until)
	if err != nil {
		return Ref{}, err
	}
	descriptor := Descriptor{SchemaVersion: SchemaVersion, ManifestDigest: manifestDigest, BoundaryDigest: boundaryDigest,
		Manifest: manifestRef, Boundary: boundaryRef, RequiredRetainUntil: until.UTC(), SourceProtections: protections}
	descriptorBytes, err := descriptor.CanonicalJSON()
	if err != nil {
		return Ref{}, err
	}
	if len(descriptorBytes) > maxDescriptorBytes {
		return Ref{}, fmt.Errorf("%w: descriptor exceeds bounded size", ErrInvalid)
	}
	descriptorDigest := digestBytes(descriptorBytes)
	descriptorRef, err := s.putEvidence(ctx, "descriptor", descriptorDigest, descriptorBytes, until)
	if err != nil {
		return Ref{}, err
	}
	ref := Ref{SchemaVersion: SchemaVersion, Manifest: manifestRef, Boundary: boundaryRef, Descriptor: descriptorRef}
	if err := ref.Validate(); err != nil {
		return Ref{}, err
	}
	if !until.After(s.now()) {
		return Ref{}, fmt.Errorf("%w: retention horizon elapsed during save", ErrRetention)
	}
	return ref, nil
}

// Reload verifies descriptor, boundary, manifest, every source object, and
// every retention record. An optional argument raises the required retention
// horizon; expiry still uses the trusted store clock.
func (s *Store) Reload(ctx context.Context, ref Ref, horizons ...time.Time) (Evidence, error) {
	if err := validContext(ctx); err != nil {
		return Evidence{}, err
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if err := ref.Validate(); err != nil {
		return Evidence{}, err
	}
	if ref.Manifest.Key != s.evidenceKey("manifest", "sha256:"+ref.Manifest.SHA256) || ref.Boundary.Key != s.evidenceKey("boundary", "sha256:"+ref.Boundary.SHA256) || ref.Descriptor.Key != s.evidenceKey("descriptor", "sha256:"+ref.Descriptor.SHA256) {
		return Evidence{}, fmt.Errorf("%w: evidence refs are not content-addressed", ErrIntegrity)
	}
	now := s.now()
	if len(horizons) > 1 {
		return Evidence{}, fmt.Errorf("%w: reload accepts one required horizon", ErrInvalid)
	}
	required := time.Time{}
	if len(horizons) == 1 && !horizons[0].IsZero() {
		required = ceilSecond(horizons[0].UTC())
	}
	descriptorBytes, _, err := s.getEvidence(ctx, ref.Descriptor, now, required)
	if err != nil {
		return Evidence{}, err
	}
	descriptor, err := parseDescriptor(descriptorBytes)
	if err != nil {
		return Evidence{}, err
	}
	if required.IsZero() {
		required = descriptor.RequiredRetainUntil
	}
	// The initial descriptor read proves it is live now.  Recheck it against
	// the descriptor's recorded horizon before accepting the bundle.
	if _, _, err := s.getEvidence(ctx, ref.Descriptor, now, descriptor.RequiredRetainUntil); err != nil {
		return Evidence{}, err
	}
	if descriptor.RequiredRetainUntil.Before(required) {
		return Evidence{}, fmt.Errorf("%w: descriptor horizon is shorter than requested horizon", ErrRetention)
	}
	if !descriptor.Manifest.equal(ref.Manifest) || !descriptor.Boundary.equal(ref.Boundary) {
		return Evidence{}, fmt.Errorf("%w: descriptor does not bind supplied manifest and boundary refs", ErrIntegrity)
	}
	manifestBytes, _, err := s.getEvidence(ctx, ref.Manifest, now, required)
	if err != nil {
		return Evidence{}, err
	}
	boundaryBytes, _, err := s.getEvidence(ctx, ref.Boundary, now, required)
	if err != nil {
		return Evidence{}, err
	}
	manifest, err := observation.ParseManifest(manifestBytes)
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: parse manifest: %v", ErrIntegrity, err)
	}
	boundary, err := observation.ParseBoundary(boundaryBytes)
	if err != nil {
		return Evidence{}, fmt.Errorf("%w: parse boundary: %v", ErrIntegrity, err)
	}
	canonicalManifest, manifestErr := manifest.CanonicalJSON()
	canonicalBoundary, boundaryErr := boundary.CanonicalJSON()
	if manifestErr != nil || boundaryErr != nil || !bytes.Equal(manifestBytes, canonicalManifest) || !bytes.Equal(boundaryBytes, canonicalBoundary) {
		return Evidence{}, fmt.Errorf("%w: manifest or boundary bytes are not canonical", ErrIntegrity)
	}
	if !manifest.Boundary.Matches(boundary) || descriptor.ManifestDigest != normalizedDigest(manifestBytes) || descriptor.BoundaryDigest != normalizedDigest(boundaryBytes) {
		return Evidence{}, fmt.Errorf("%w: manifest, boundary, and descriptor digests are not bound", ErrIntegrity)
	}
	if err := compareProtections(manifest, descriptor.SourceProtections); err != nil {
		return Evidence{}, err
	}
	for _, protection := range descriptor.SourceProtections {
		if _, err := s.verifySourceAt(ctx, protection.Object, protection.RetainUntil, now, required); err != nil {
			return Evidence{}, err
		}
	}
	return Evidence{Manifest: manifest, Boundary: boundary, Descriptor: descriptor, Ref: ref}, nil
}

// FrontierRef is an optional, qualification-only persisted recovery-set
// pointer. It never changes the v1 SQL activation path.
type FrontierRef struct {
	SchemaVersion int32     `json:"schema_version"`
	SetID         string    `json:"set_id"`
	Digest        string    `json:"digest"`
	Frontier      ObjectRef `json:"frontier"`
	SetIDObject   ObjectRef `json:"set_id_object"`
	Evidence      Ref       `json:"evidence"`
}

// PersistFrontier binds and immutably persists a schema-2 recovery set after
// independently reloading its managed evidence. The set-ID object rejects a
// conflicting replay even when a different content digest is supplied.
func (s *Store) PersistFrontier(ctx context.Context, set recoveryset.RecoverySet, evidence Ref, until time.Time) (FrontierRef, error) {
	if err := validContext(ctx); err != nil {
		return FrontierRef{}, err
	}
	ctx, cancel := s.operationContext(ctx)
	defer cancel()
	if set.SchemaVersion != recoveryset.EvidenceSchemaVersion || set.ManagedEvidence == nil {
		return FrontierRef{}, fmt.Errorf("%w: only schema-2 prepared recovery sets can be persisted off-host", ErrInvalid)
	}
	if err := set.Validate(); err != nil {
		return FrontierRef{}, err
	}
	until = ceilSecond(until.UTC())
	if !until.After(s.now()) {
		return FrontierRef{}, fmt.Errorf("%w: retention horizon must be in the future", ErrInvalid)
	}
	loaded, err := s.Reload(ctx, evidence, until)
	if err != nil {
		return FrontierRef{}, err
	}
	binding := set.ManagedEvidence
	if binding.ManifestDigest != loaded.Descriptor.ManifestDigest || binding.BoundaryDigest != loaded.Descriptor.BoundaryDigest || binding.DescriptorDigest != "sha256:"+evidence.Descriptor.SHA256 || !binding.Boundary.Matches(loaded.Boundary) {
		return FrontierRef{}, fmt.Errorf("%w: recovery set managed evidence does not match loaded descriptor", ErrIntegrity)
	}
	if set.FrontierDigest == "" {
		set.FrontierDigest, err = set.Digest()
		if err != nil {
			return FrontierRef{}, err
		}
	}
	frontierBytes, err := set.CanonicalJSON()
	if err != nil {
		return FrontierRef{}, err
	}
	if len(frontierBytes) > maxFrontierBytes {
		return FrontierRef{}, fmt.Errorf("%w: recovery frontier exceeds bounded size", ErrInvalid)
	}
	digest := normalizedDigest(frontierBytes)
	frontier, err := s.putEvidenceAt(ctx, s.frontierKey(digest), digest, frontierBytes, until)
	if err != nil {
		return FrontierRef{}, err
	}
	setObject, err := s.putEvidenceAt(ctx, s.setIDKey(set.ID), digest, frontierBytes, until)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return FrontierRef{}, err
		}
		return FrontierRef{}, err
	}
	if !until.After(s.now()) {
		return FrontierRef{}, fmt.Errorf("%w: retention horizon elapsed during frontier persistence", ErrRetention)
	}
	return FrontierRef{SchemaVersion: SchemaVersion, SetID: set.ID, Digest: digest, Frontier: frontier, SetIDObject: setObject, Evidence: evidence}, nil
}

func (s *Store) putEvidence(ctx context.Context, kind, digestValue string, body []byte, until time.Time) (ObjectRef, error) {
	return s.putEvidenceAt(ctx, s.evidenceKey(kind, digestValue), digestValue, body, until)
}

func (s *Store) putEvidenceAt(ctx context.Context, key, digestValue string, body []byte, until time.Time) (ObjectRef, error) {
	if err := validContext(ctx); err != nil {
		return ObjectRef{}, err
	}
	if !rawSHA256(digestValue) && !digest(digestValue) {
		return ObjectRef{}, fmt.Errorf("%w: evidence digest is invalid", ErrInvalid)
	}
	raw := rawDigest(digestValue)
	if raw == "" {
		return ObjectRef{}, fmt.Errorf("%w: evidence digest is invalid", ErrInvalid)
	}
	if normalizedDigest(body) != "sha256:"+raw {
		return ObjectRef{}, fmt.Errorf("%w: evidence body does not match digest", ErrIntegrity)
	}
	if until.IsZero() {
		return ObjectRef{}, fmt.Errorf("%w: evidence retention horizon is required", ErrInvalid)
	}
	setIDKey := strings.HasPrefix(key, s.evidencePrefix()+"/frontiers/set-id/")
	if setIDKey {
		// A current delete marker can hide an older immutable set-ID version.
		// Prove idempotence first; otherwise inspect all versions and fail closed
		// before attempting a new write.
		if existing, existingErr := s.verifyEvidence(ctx, key, nil, "sha256:"+raw, int64(len(body)), until); existingErr == nil {
			if err := s.ensureSetIDVersionIsOnly(ctx, key, existing.VersionID); err != nil {
				return ObjectRef{}, err
			}
			return existing, nil
		} else if !errors.Is(existingErr, ErrNotFound) && !errors.Is(existingErr, ErrIntegrity) {
			return ObjectRef{}, existingErr
		} else if errors.Is(existingErr, ErrIntegrity) {
			return ObjectRef{}, fmt.Errorf("%w: existing immutable set-ID differs", ErrConflict)
		}
		if err := s.ensureSetIDAbsent(ctx, key); err != nil {
			return ObjectRef{}, err
		}
	}
	checksum := sha256.Sum256(body)
	checksum64 := base64.StdEncoding.EncodeToString(checksum[:])
	put, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: ptr(s.evidenceBucket), Key: ptr(key), Body: bytes.NewReader(body), ContentLength: ptr(int64(len(body))), ChecksumSHA256: ptr(checksum64), IfNoneMatch: ptr("*"), ObjectLockMode: types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: ptr(until.UTC()), Metadata: map[string]string{"leapview-sha256": "sha256:" + raw}})
	if err != nil && !precondition(err) {
		return ObjectRef{}, backendError(ctx, "put evidence object", err)
	}
	var version *string
	if err == nil {
		if put == nil || put.VersionId == nil || stringValue(put.VersionId) == "" || strings.EqualFold(stringValue(put.VersionId), "null") {
			return ObjectRef{}, fmt.Errorf("%w: S3 did not return an immutable evidence version", ErrIntegrity)
		}
		version = put.VersionId
	}
	// On a precondition failure this verifies the extant bytes and retention;
	// it never overwrites an immutable object.
	ref, existingErr := s.verifyEvidence(ctx, key, version, "sha256:"+raw, int64(len(body)), until)
	if existingErr != nil {
		if err != nil && precondition(err) {
			if errors.Is(existingErr, ErrIntegrity) || errors.Is(existingErr, ErrConflict) {
				return ObjectRef{}, fmt.Errorf("%w: existing immutable evidence differs", ErrConflict)
			}
		}
		return ObjectRef{}, existingErr
	}
	if setIDKey {
		if err := s.ensureSetIDVersionIsOnly(ctx, key, ref.VersionID); err != nil {
			return ObjectRef{}, err
		}
	}
	return ref, nil
}

type versionLister interface {
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
}

// ensureSetIDAbsent closes the delete-marker hole on the fixed set-ID index.
// A current HEAD miss is not sufficient evidence that the key has never been
// used, because S3 may hide retained versions behind a delete marker.
func (s *Store) ensureSetIDAbsent(ctx context.Context, key string) error {
	lister, ok := s.client.(versionLister)
	if !ok {
		return fmt.Errorf("%w: set-ID immutability requires version listing support", ErrBackend)
	}
	var keyMarker, versionMarker *string
	for {
		result, err := lister.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: ptr(s.evidenceBucket), Prefix: ptr(key), KeyMarker: keyMarker, VersionIdMarker: versionMarker, MaxKeys: ptr(int32(1000))})
		if err != nil {
			return backendError(ctx, "list set-ID object versions", err)
		}
		if result == nil {
			return fmt.Errorf("%w: set-ID version listing is incomplete", ErrBackend)
		}
		for _, version := range result.Versions {
			if stringValue(version.Key) == key {
				return fmt.Errorf("%w: set-ID key already has an immutable version", ErrConflict)
			}
		}
		for _, marker := range result.DeleteMarkers {
			if stringValue(marker.Key) == key {
				return fmt.Errorf("%w: set-ID key has a delete marker", ErrConflict)
			}
		}
		if result.IsTruncated == nil || !*result.IsTruncated {
			return nil
		}
		if result.NextKeyMarker == nil || result.NextVersionIdMarker == nil || *result.NextKeyMarker == "" || *result.NextVersionIdMarker == "" || keyMarker != nil && *result.NextKeyMarker == *keyMarker && versionMarker != nil && *result.NextVersionIdMarker == *versionMarker {
			return fmt.Errorf("%w: set-ID version listing pagination is invalid", ErrBackend)
		}
		keyMarker, versionMarker = result.NextKeyMarker, result.NextVersionIdMarker
	}
}

// ensureSetIDVersionIsOnly detects a delete marker or older version that may
// have appeared during the write race. The immutable object itself remains
// retained; this operation only fails closed and never deletes anything.
func (s *Store) ensureSetIDVersionIsOnly(ctx context.Context, key, version string) error {
	lister, ok := s.client.(versionLister)
	if !ok {
		return fmt.Errorf("%w: set-ID immutability requires version listing support", ErrBackend)
	}
	result, err := lister.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: ptr(s.evidenceBucket), Prefix: ptr(key), MaxKeys: ptr(int32(1000))})
	if err != nil {
		return backendError(ctx, "list set-ID object versions", err)
	}
	if result == nil {
		return fmt.Errorf("%w: set-ID version listing is incomplete", ErrBackend)
	}
	if result.IsTruncated != nil && *result.IsTruncated {
		return fmt.Errorf("%w: set-ID version listing exceeds bounded identity history", ErrBackend)
	}
	seen := false
	for _, item := range result.Versions {
		if stringValue(item.Key) == key {
			if stringValue(item.VersionId) != version {
				return fmt.Errorf("%w: set-ID key has a conflicting immutable version", ErrConflict)
			}
			seen = true
		}
	}
	for _, marker := range result.DeleteMarkers {
		if stringValue(marker.Key) == key {
			return fmt.Errorf("%w: set-ID key has a delete marker", ErrConflict)
		}
	}
	if !seen {
		return fmt.Errorf("%w: set-ID write version is absent from immutable history", ErrIntegrity)
	}
	return nil
}

func (s *Store) verifyEvidence(ctx context.Context, key string, version *string, digestValue string, size int64, required time.Time) (ObjectRef, error) {
	if size > maxDescriptorBytes {
		return ObjectRef{}, fmt.Errorf("%w: evidence object exceeds bounded size", ErrInvalid)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: ptr(s.evidenceBucket), Key: ptr(key), VersionId: version})
	if err != nil {
		if apiCode(err) == "NoSuchKey" || apiCode(err) == "NotFound" {
			return ObjectRef{}, ErrNotFound
		}
		return ObjectRef{}, backendError(ctx, "head evidence object", err)
	}
	if head == nil || head.ContentLength == nil || *head.ContentLength != size || stringValue(head.VersionId) == "" || strings.EqualFold(stringValue(head.VersionId), "null") || version != nil && stringValue(head.VersionId) != stringValue(version) {
		return ObjectRef{}, fmt.Errorf("%w: evidence object metadata is incomplete", ErrIntegrity)
	}
	retention, err := s.retention(ctx, s.evidenceBucket, key, stringValue(head.VersionId))
	if err != nil {
		return ObjectRef{}, err
	}
	if retention.Mode != types.ObjectLockRetentionModeCompliance || retention.RetainUntilDate == nil || retention.RetainUntilDate.Before(required) {
		return ObjectRef{}, fmt.Errorf("%w: evidence object is not COMPLIANCE-retained through required horizon", ErrRetention)
	}
	get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: ptr(s.evidenceBucket), Key: ptr(key), VersionId: head.VersionId})
	if err != nil {
		return ObjectRef{}, backendError(ctx, "get evidence object", err)
	}
	if get != nil && get.Body != nil && (stringValue(get.VersionId) != stringValue(head.VersionId) || get.ContentLength == nil || *get.ContentLength != size) {
		_ = get.Body.Close()
		return ObjectRef{}, fmt.Errorf("%w: evidence object response metadata differs", ErrIntegrity)
	}
	if get == nil || get.Body == nil {
		return ObjectRef{}, fmt.Errorf("%w: evidence object body is missing", ErrBackend)
	}
	actualSize, actualHash, readErr := hashBody(get.Body, size)
	closeErr := get.Body.Close()
	if readErr != nil || closeErr != nil || actualSize != size || actualHash != rawDigest(digestValue) {
		return ObjectRef{}, fmt.Errorf("%w: evidence object bytes differ", ErrIntegrity)
	}
	return ObjectRef{Bucket: s.evidenceBucket, Key: key, VersionID: stringValue(head.VersionId), SHA256: rawDigest(digestValue), Size: size}, nil
}

// getEvidence verifies an exact evidence ref. It returns bytes only after the
// provider version, content length/hash, and retention have all matched.
func (s *Store) getEvidence(ctx context.Context, ref ObjectRef, now, required time.Time) ([]byte, ObjectRef, error) {
	if err := s.validateEvidenceRef(ref); err != nil {
		return nil, ObjectRef{}, err
	}
	if ref.Size > maxDescriptorBytes {
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref exceeds bounded size", ErrInvalid)
	}
	head, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: ptr(s.evidenceBucket), Key: ptr(ref.Key), VersionId: ptr(ref.VersionID)})
	if err != nil {
		return nil, ObjectRef{}, backendError(ctx, "head evidence ref", err)
	}
	if head == nil || head.ContentLength == nil || *head.ContentLength != ref.Size || stringValue(head.VersionId) != ref.VersionID {
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref metadata differs", ErrIntegrity)
	}
	retention, err := s.retention(ctx, s.evidenceBucket, ref.Key, ref.VersionID)
	if err != nil {
		return nil, ObjectRef{}, err
	}
	if retention.Mode != types.ObjectLockRetentionModeCompliance || retention.RetainUntilDate == nil || retention.RetainUntilDate.Before(required) || (!now.IsZero() && !retention.RetainUntilDate.After(now)) {
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref retention is insufficient", ErrRetention)
	}
	get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: ptr(s.evidenceBucket), Key: ptr(ref.Key), VersionId: ptr(ref.VersionID)})
	if err != nil {
		return nil, ObjectRef{}, backendError(ctx, "get evidence ref", err)
	}
	if get != nil && get.Body != nil && (stringValue(get.VersionId) != ref.VersionID || get.ContentLength == nil || *get.ContentLength != ref.Size) {
		_ = get.Body.Close()
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref response metadata differs", ErrIntegrity)
	}
	if get == nil || get.Body == nil {
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref body is missing", ErrBackend)
	}
	body, readErr := io.ReadAll(io.LimitReader(get.Body, ref.Size+1))
	closeErr := get.Body.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) != ref.Size || normalizedDigest(body) != "sha256:"+ref.SHA256 {
		return nil, ObjectRef{}, fmt.Errorf("%w: evidence ref bytes differ", ErrIntegrity)
	}
	return body, ObjectRef{Bucket: s.evidenceBucket, Key: ref.Key, VersionID: ref.VersionID, SHA256: ref.SHA256, Size: ref.Size}, nil
}

func (s *Store) validateEvidenceRef(ref ObjectRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Bucket != s.evidenceBucket || !strings.HasPrefix(ref.Key, s.evidencePrefix()+"/") {
		return fmt.Errorf("%w: evidence ref is outside configured bucket and prefix", ErrInvalid)
	}
	return nil
}

func compareProtections(manifest observation.Manifest, protections []observation.Protection) error {
	if len(manifest.Objects) != len(protections) {
		return fmt.Errorf("%w: descriptor source protection count differs from manifest", ErrIntegrity)
	}
	byKey := make(map[string][]observation.Protection, len(protections))
	for _, protection := range protections {
		key := providerKey(protection.Object)
		byKey[key] = append(byKey[key], protection)
	}
	for _, item := range manifest.Objects {
		key := providerKey(item.Object)
		entries := byKey[key]
		if len(entries) == 0 {
			return fmt.Errorf("%w: descriptor source protection does not bind manifest object", ErrIntegrity)
		}
		protection := entries[0]
		byKey[key] = entries[1:]
		if protection.Mode != string(types.ObjectLockRetentionModeCompliance) || protection.Object != item.Object {
			return fmt.Errorf("%w: descriptor source protection does not bind manifest object", ErrIntegrity)
		}
	}
	for _, entries := range byKey {
		if len(entries) != 0 {
			return fmt.Errorf("%w: descriptor has an unreferenced source protection", ErrIntegrity)
		}
	}
	return nil
}

func providerKey(object observation.ProviderObject) string {
	return object.Endpoint + "\x00" + object.Region + "\x00" + object.Bucket + "\x00" + object.Key + "\x00" + object.VersionID + "\x00" + object.SHA256 + fmt.Sprintf("\x00%d", object.Size)
}
