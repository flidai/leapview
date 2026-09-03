// Package l3 coordinates immutable object storage with the PostgreSQL result
// cache authority. PostgreSQL remains the only source of admission, fill,
// retention, and invalidation state; this package owns only the object-first
// protocol and bounded orphan reconciliation.
package l3

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const (
	// DefaultMaxObjectBytes bounds buffering for the pre-admission digest pass
	// and for exact reads from an untrusted object store.
	DefaultMaxObjectBytes   int64 = 128 << 20
	MaxObjectBytesLimit     int64 = 1 << 30
	DefaultObjectFenceLease       = 5 * time.Minute
	MinObjectFenceLease           = time.Second
	maxMetadataBytes              = 16 << 10
	maxPrefixBytes                = 512
)

var (
	ErrInvalid         = errors.New("invalid L3 cache input")
	ErrDisabled        = errors.New("L3 cache is disabled")
	ErrMiss            = errors.New("L3 cache miss")
	ErrObjectCorrupt   = errors.New("L3 cache object is missing or corrupt")
	ErrSecurityDomain  = errors.New("L3 cache security-domain mismatch")
	ErrObjectExists    = errors.New("L3 object already exists")
	ErrObjectAmbiguous = errors.New("L3 object acknowledgement is ambiguous")
	ErrReconciliation  = errors.New("L3 cache reconciliation failed")
)

// ObjectMetadata is the exact, provider-neutral metadata bound to an object.
// Metadata is canonical JSON and SecurityDomain is intentionally repeated in
// the store contract so a provider cannot silently cross isolation domains.
type ObjectMetadata struct {
	SecurityDomain string
	Metadata       json.RawMessage
	MetadataDigest string
	// Digest and Size are pre-committed body evidence. Publication callers
	// that already performed the bounded digest pass populate these fields so
	// provider adapters can delegate buffering and verification without a
	// second read of the body.
	Digest string
	Size   int64
}

// ObjectInfo is returned by object stores for both writes and exact reads.
// CreatedAt is required for bounded orphan collection; a zero timestamp is
// treated as unsafe to delete.
type ObjectInfo struct {
	Key            string
	SecurityDomain string
	Digest         string
	Size           int64
	Metadata       json.RawMessage
	MetadataDigest string
	VersionID      string
	ETag           string
	CreatedAt      time.Time
}

// Object is an exact-key immutable object and its provider metadata.
type Object struct {
	Body io.ReadCloser
	Info ObjectInfo
}

// PublicationStore is the non-destructive object capability supplied to a
// serving cache. PutImmutable must be create-only: it may return
// ErrObjectExists when the key already exists and ErrObjectAmbiguous when the
// acknowledgement is lost. In either case the cache reconciles by opening the
// exact key before any manifest is admitted.
type PublicationStore interface {
	PutImmutable(context.Context, string, io.Reader, ObjectMetadata) (ObjectInfo, error)
	Open(context.Context, string) (Object, error)
}

// MaintenanceStore is the destructive object capability supplied only to the
// pool collector. It is deliberately absent from Cache.
type MaintenanceStore interface {
	DeleteExact(context.Context, ObjectInfo) error
	// List returns at most limit objects in strictly increasing key order after
	// the key cursor. A non-empty next cursor must equal the final object key.
	// A GC pass intentionally consumes one page and leaves that cursor to the
	// next pass.
	List(context.Context, string, string, int) ([]ObjectInfo, string, error)
}

// ObjectStore is the complete adapter implemented by provider clients. App
// composition immediately narrows it to PublicationStore or MaintenanceStore.
type ObjectStore interface {
	PublicationStore
	MaintenanceStore
}

type ObjectFenceAuthority interface {
	AcquireL3ObjectFence(context.Context, cachepostgres.AcquireL3ObjectFenceInput) (cachepostgres.L3ObjectFence, error)
	RenewL3ObjectFence(context.Context, cachepostgres.L3ObjectFence, time.Duration) error
	ReleaseL3ObjectFence(context.Context, cachepostgres.L3ObjectFence) error
}

// Authority is the accepted PostgreSQL authority surface needed by L3. The
// concrete cache/postgres Repository implements this interface. Keeping this
// interface small also makes deterministic adversarial-store tests possible.
type Authority interface {
	ObjectFenceAuthority
	AcquireFill(context.Context, cachepostgres.AcquireFillInput) (cachepostgres.FillLease, error)
	Publish(context.Context, cachepostgres.PublishInput) (cachepostgres.Manifest, error)
	Lookup(context.Context, cachepostgres.LookupInput) (cachepostgres.Manifest, bool, error)
}

// FillLeaseAuthority is the optional fence-renew/release surface. The
// PostgreSQL repository implements it; keeping it optional allows read-only
// cache test doubles and deployments that hand lease lifecycle to a worker.
type FillLeaseAuthority interface {
	RenewFill(context.Context, cachepostgres.FillLease, time.Duration) error
	ReleaseFill(context.Context, cachepostgres.FillLease) error
}

// ManifestRetirementAuthority records exact lifecycle evidence for one
// manifest. L3 requires this operation for object reconciliation; it never
// falls back to broad namespace invalidation.
type ManifestRetirementAuthority interface {
	RetireManifest(context.Context, uuid.UUID, json.RawMessage) error
}

// Config defines one immutable L3 security boundary. Object keys are always
// derived from this boundary and a manifest key; callers cannot supply an
// arbitrary object key.
type Config struct {
	Authority      Authority
	Store          PublicationStore
	Namespace      cachepostgres.Namespace
	SecurityDomain string
	// OriginSnapshotSealID is immutable provenance for every manifest admitted
	// through this cache. It is deliberately excluded from namespace and
	// object/cache-key identity.
	OriginSnapshotSealID string
	Prefix               string
	Enabled              bool
	MaxObjectBytes       int64
	ObjectFenceLease     time.Duration
	Now                  func() time.Time
}

// Cache is a domain-scoped L3 coordinator.
type Cache struct {
	authority            Authority
	store                PublicationStore
	namespace            cachepostgres.Namespace
	securityDomain       string
	originSnapshotSealID string
	objectPrefix         string
	maxObjectBytes       int64
	objectFenceLease     time.Duration
	now                  func() time.Time
	enabled              bool
}

// New validates a configuration. Disabled caches intentionally accept nil
// dependencies so deployments can turn L3 off without constructing storage
// clients or a database connection.
func New(cfg Config) (*Cache, error) {
	if !cfg.Enabled {
		return &Cache{enabled: false}, nil
	}
	if cfg.Authority == nil || cfg.Store == nil {
		return nil, fmt.Errorf("%w: authority and store are required", ErrInvalid)
	}
	if err := validateNamespace(cfg.Namespace); err != nil {
		return nil, err
	}
	if err := validateCanonicalUUID(cfg.OriginSnapshotSealID, "origin snapshot seal id"); err != nil {
		return nil, err
	}
	if platformdigest.ValidateSHA256Identity(cfg.SecurityDomain) != nil {
		return nil, fmt.Errorf("%w: storage security domain", ErrInvalid)
	}
	prefix, err := normalizePrefix(cfg.Prefix)
	if err != nil {
		return nil, err
	}
	maxBytes := cfg.MaxObjectBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxObjectBytes
	}
	if maxBytes > MaxObjectBytesLimit {
		return nil, fmt.Errorf("%w: max object bytes exceeds absolute limit", ErrInvalid)
	}
	objectFenceLease := cfg.ObjectFenceLease
	if objectFenceLease <= 0 {
		objectFenceLease = DefaultObjectFenceLease
	}
	if objectFenceLease < MinObjectFenceLease || objectFenceLease > 24*time.Hour {
		return nil, fmt.Errorf("%w: object-fence lease duration is out of bounds", ErrInvalid)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Cache{
		authority:            cfg.Authority,
		store:                cfg.Store,
		namespace:            cfg.Namespace,
		securityDomain:       cfg.SecurityDomain,
		originSnapshotSealID: cfg.OriginSnapshotSealID,
		objectPrefix:         prefix + "sd/" + cfg.SecurityDomain + "/",
		maxObjectBytes:       maxBytes,
		objectFenceLease:     objectFenceLease,
		now:                  now,
		enabled:              true,
	}, nil
}

func validateNamespace(n cachepostgres.Namespace) error {
	if n.PartitionKind != cachepostgres.PartitionProduction && n.PartitionKind != cachepostgres.PartitionCandidate {
		return fmt.Errorf("%w: namespace partition", ErrInvalid)
	}
	if !validScopeLiteral(n.TargetID) || !validScopeLiteral(n.ProjectID) || !validScopeLiteral(n.Environment) || len(n.TargetID) > 255 || len(n.ProjectID) > 255 || len(n.Environment) > 255 {
		return fmt.Errorf("%w: namespace scope", ErrInvalid)
	}
	if n.CandidateID != "" && (!validScopeLiteral(n.CandidateID) || len(n.CandidateID) > 255) {
		return fmt.Errorf("%w: candidate scope", ErrInvalid)
	}
	if n.PartitionKind == cachepostgres.PartitionProduction && n.CandidateID != "" {
		return fmt.Errorf("%w: production namespace cannot have candidate", ErrInvalid)
	}
	if n.PartitionKind == cachepostgres.PartitionCandidate && n.CandidateID == "" {
		return fmt.Errorf("%w: candidate namespace requires candidate", ErrInvalid)
	}
	return nil
}

func validateCanonicalUUID(value, name string) error {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return fmt.Errorf("%w: invalid %s", ErrInvalid, name)
	}
	return nil
}

func validScopeLiteral(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func normalizePrefix(prefix string) (string, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "cache/l3/"
	}
	if len(prefix) > maxPrefixBytes || strings.ContainsAny(prefix, "\\\x00\r\n") || strings.HasPrefix(prefix, "/") {
		return "", fmt.Errorf("%w: invalid object prefix", ErrInvalid)
	}
	for _, segment := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if !safePrefixSegment(segment) {
			return "", fmt.Errorf("%w: invalid object prefix", ErrInvalid)
		}
	}
	return strings.TrimSuffix(prefix, "/") + "/", nil
}

func safePrefixSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, ch := range segment {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' {
			continue
		}
		return false
	}
	return true
}

// ObjectKey derives the only object key this cache may access for a manifest
// key and object digest. The storage security domain is part of the path even
// though it is also carried in object metadata and the SQL manifest.
func (c *Cache) ObjectKey(key cachepostgres.ManifestKey, objectDigest string) (string, error) {
	if c == nil || !c.enabled {
		return "", ErrDisabled
	}
	if !sameNamespace(key, c.namespace) {
		return "", fmt.Errorf("%w: manifest namespace", ErrSecurityDomain)
	}
	keyDigest, err := key.CacheKeyDigest()
	if err != nil {
		return "", err
	}
	if platformdigest.ValidateSHA256Identity(objectDigest) != nil {
		return "", fmt.Errorf("%w: object digest", ErrInvalid)
	}
	return c.objectPrefix + keyDigest + "/" + objectDigest, nil
}

// AcquireFill obtains an authority fill fence for this cache's namespace.
func (c *Cache) AcquireFill(ctx context.Context, key cachepostgres.ManifestKey, ownerID string, lease time.Duration) (cachepostgres.FillLease, error) {
	if c == nil || !c.enabled {
		return cachepostgres.FillLease{}, ErrDisabled
	}
	if !sameNamespace(key, c.namespace) {
		return cachepostgres.FillLease{}, fmt.Errorf("%w: manifest namespace", ErrSecurityDomain)
	}
	keyDigest, err := key.CacheKeyDigest()
	if err != nil {
		return cachepostgres.FillLease{}, err
	}
	return c.authority.AcquireFill(ctx, cachepostgres.AcquireFillInput{CacheKey: keyDigest, OwnerID: ownerID, Lease: lease, Namespace: c.namespace})
}

// RenewFill and ReleaseFill expose the authority fence operations while
// keeping callers from selecting a different namespace through this cache.
func (c *Cache) RenewFill(ctx context.Context, lease cachepostgres.FillLease, duration time.Duration) error {
	if c == nil || !c.enabled {
		return ErrDisabled
	}
	if lease.Namespace.Key() != c.namespace.Key() {
		return ErrSecurityDomain
	}
	fills, ok := c.authority.(FillLeaseAuthority)
	if !ok {
		return fmt.Errorf("%w: authority cannot renew fills", ErrInvalid)
	}
	return fills.RenewFill(ctx, lease, duration)
}

func (c *Cache) ReleaseFill(ctx context.Context, lease cachepostgres.FillLease) error {
	if c == nil || !c.enabled {
		return ErrDisabled
	}
	if lease.Namespace.Key() != c.namespace.Key() {
		return ErrSecurityDomain
	}
	fills, ok := c.authority.(FillLeaseAuthority)
	if !ok {
		return fmt.Errorf("%w: authority cannot release fills", ErrInvalid)
	}
	return fills.ReleaseFill(ctx, lease)
}

// Publish performs the object-first protocol. Bytes are bounded and hashed
// before the immutable PUT; the exact stored object is then reopened and
// verified before PostgreSQL admission. A successful PUT followed by a lost
// manifest acknowledgement is safe to retry: the create-only PUT is
// reconciled and PostgreSQL's fence-aware Publish is idempotent.
type PublishInput struct {
	Key       cachepostgres.ManifestKey
	Lease     cachepostgres.FillLease
	Body      io.Reader
	Metadata  json.RawMessage
	ExpiresAt *time.Time
}

func (c *Cache) Publish(ctx context.Context, in PublishInput) (cachepostgres.Manifest, error) {
	if c == nil || !c.enabled {
		return cachepostgres.Manifest{}, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if in.Body == nil {
		return cachepostgres.Manifest{}, fmt.Errorf("%w: body is required", ErrInvalid)
	}
	if !sameNamespace(in.Key, c.namespace) {
		return cachepostgres.Manifest{}, fmt.Errorf("%w: manifest namespace", ErrSecurityDomain)
	}
	if in.Lease.Namespace.Key() != c.namespace.Key() {
		return cachepostgres.Manifest{}, fmt.Errorf("%w: fill namespace", ErrSecurityDomain)
	}
	keyDigest, err := in.Key.CacheKeyDigest()
	if err != nil {
		return cachepostgres.Manifest{}, err
	}
	if in.Lease.CacheKey != keyDigest {
		return cachepostgres.Manifest{}, fmt.Errorf("%w: fill key", ErrSecurityDomain)
	}
	metadata, err := canonicalMetadata(in.Metadata)
	if err != nil {
		return cachepostgres.Manifest{}, err
	}
	body, err := readBounded(in.Body, c.maxObjectBytes)
	if err != nil {
		return cachepostgres.Manifest{}, err
	}
	digest := digestBytes(body)
	if len(body) == 0 {
		return cachepostgres.Manifest{}, fmt.Errorf("%w: object body must be non-empty", ErrInvalid)
	}
	objectKey, err := c.ObjectKey(in.Key, digest)
	if err != nil {
		return cachepostgres.Manifest{}, err
	}
	objectFence, err := c.authority.AcquireL3ObjectFence(ctx, cachepostgres.AcquireL3ObjectFenceInput{
		StorageSecurityDomain: c.securityDomain,
		ObjectKey:             objectKey,
		OwnerID:               in.Lease.OwnerID,
		Lease:                 c.objectFenceLease,
	})
	if err != nil {
		return cachepostgres.Manifest{}, err
	}
	guard := newObjectFenceGuard(ctx, c.authority, objectFence, c.objectFenceLease, 0)
	// The exact external-object fence is independent from the logical fill
	// fence. It remains held through read-back verification and manifest
	// admission. The heartbeat keeps slow provider operations safe, while
	// PostgreSQL atomically locks and validates this fence during admission.
	// A failed release after a committed manifest is harmless: the fence
	// expires and reachability now retains the object.
	defer func() {
		_ = guard.stopAndRelease(ctx)
	}()
	metadataDigest := digestBytes(metadata)
	expected := ObjectInfo{Key: objectKey, SecurityDomain: c.securityDomain, Digest: digest, Size: int64(len(body)), Metadata: metadata, MetadataDigest: metadataDigest}
	putInfo, putErr := c.store.PutImmutable(guard.ctx, objectKey, bytes.NewReader(body), ObjectMetadata{SecurityDomain: c.securityDomain, Metadata: metadata, MetadataDigest: metadataDigest, Digest: digest, Size: int64(len(body))})
	if putErr != nil && !errors.Is(putErr, ErrObjectExists) && !errors.Is(putErr, ErrObjectAmbiguous) {
		return cachepostgres.Manifest{}, putErr
	}
	// Always reopen after PUT. This verifies both a normal acknowledgement and
	// an existing/ambiguous key, and prevents a dishonest provider response from
	// reaching manifest admission.
	if _, err := c.verifyObject(guard.ctx, objectKey, expected); err != nil {
		if putErr != nil {
			return cachepostgres.Manifest{}, fmt.Errorf("%w: %v (put: %v)", ErrObjectCorrupt, err, putErr)
		}
		return cachepostgres.Manifest{}, err
	}
	if err := guard.renew(); err != nil {
		return cachepostgres.Manifest{}, err
	}
	if err := guard.failure(); err != nil {
		return cachepostgres.Manifest{}, err
	}
	_ = putInfo // verification intentionally trusts the exact reopened object
	return c.authority.Publish(guard.ctx, cachepostgres.PublishInput{Key: in.Key, OriginSnapshotSealID: c.originSnapshotSealID, StorageSecurityDomain: c.securityDomain, ObjectDigest: digest, ObjectKey: objectKey, ByteSize: int64(len(body)), Metadata: metadata, ExpiresAt: in.ExpiresAt, Lease: in.Lease, ObjectFence: objectFence})
}

// ReadResult distinguishes a hit from a safe miss. Missing or corrupt objects
// are misses after a durable invalidation signal has been recorded.
type ReadResult struct {
	Hit  bool
	Body []byte
	// Metadata is the verified canonical manifest metadata associated with the
	// object. It is returned on hits so higher-level tiers can carry
	// generation-neutral result metadata without embedding it in Arrow bytes.
	Metadata json.RawMessage
	// Admission carries the exact manifest and lookup key validated for a hit.
	// Consumers that reject semantic payloads can retire this one manifest
	// without broad namespace invalidation.
	Admission  AdmissionSnapshot
	Reconciled bool
	MissReason string
}

// AdmissionSnapshot bundles the exact ManifestKey used by an authority
// lookup with its admitted row. Supplying the key prevents a caller from
// accidentally validating a row under a different lookup identity.
type AdmissionSnapshot struct {
	Key      cachepostgres.ManifestKey
	Manifest cachepostgres.Manifest
}

// Read validates a supplied local admission snapshot and the exact key used
// to obtain it, without consulting PostgreSQL. The key is mandatory so a
// caller cannot pass an admitted row under an unrelated lookup identity.
func (c *Cache) Read(ctx context.Context, manifest cachepostgres.Manifest, lookupKey cachepostgres.ManifestKey) (ReadResult, error) {
	if c == nil || !c.enabled {
		return ReadResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if manifest.Key != lookupKey {
		return ReadResult{}, fmt.Errorf("%w: admission key mismatch", ErrSecurityDomain)
	}
	if !sameNamespace(manifest.Key, c.namespace) || manifest.StorageSecurityDomain != c.securityDomain || manifest.State != cachepostgres.StateAdmitted {
		return ReadResult{}, fmt.Errorf("%w: manifest admission snapshot", ErrSecurityDomain)
	}
	if manifest.ExpiresAt != nil && !manifest.ExpiresAt.After(c.now()) {
		return ReadResult{MissReason: "manifest expired"}, nil
	}
	expectedKey, err := c.ObjectKey(manifest.Key, manifest.ObjectDigest)
	if err != nil {
		return ReadResult{}, err
	}
	if manifest.ObjectKey != expectedKey || manifest.ByteSize <= 0 || manifest.ByteSize > c.maxObjectBytes {
		return c.reconcileMiss(ctx, manifest, "manifest object identity mismatch")
	}
	metadata, err := canonicalMetadata(manifest.Metadata)
	if err != nil {
		return c.reconcileMiss(ctx, manifest, "manifest metadata is invalid")
	}
	body, err := c.verifyObject(ctx, expectedKey, ObjectInfo{Key: expectedKey, SecurityDomain: c.securityDomain, Digest: manifest.ObjectDigest, Size: manifest.ByteSize, Metadata: metadata, MetadataDigest: digestBytes(metadata)})
	if err != nil {
		return c.reconcileMiss(ctx, manifest, err.Error())
	}
	return ReadResult{Hit: true, Body: body, Metadata: metadata, Admission: AdmissionSnapshot{Key: lookupKey, Manifest: manifest}}, nil
}

// LookupAndRead is the explicit round-trip variant. Read itself never calls
// Lookup, allowing dashboard hot paths to use a local admission snapshot.
func (c *Cache) LookupAndRead(ctx context.Context, key cachepostgres.ManifestKey) (ReadResult, error) {
	if c == nil || !c.enabled {
		return ReadResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manifest, found, err := c.authority.Lookup(ctx, key)
	if err != nil || !found {
		return ReadResult{MissReason: "manifest not admitted"}, err
	}
	return c.Read(ctx, manifest, key)
}

// ReadSnapshot validates an explicitly bundled admission snapshot without a
// PostgreSQL round trip.
func (c *Cache) ReadSnapshot(ctx context.Context, snapshot AdmissionSnapshot) (ReadResult, error) {
	return c.Read(ctx, snapshot.Manifest, snapshot.Key)
}

// Reject retires one previously validated admitted manifest after a higher
// tier detects semantic corruption (for example invalid Arrow IPC or result
// metadata). The admission snapshot is checked again so callers cannot retire
// an unrelated manifest by swapping keys or namespaces.
func (c *Cache) Reject(ctx context.Context, snapshot AdmissionSnapshot, reason string) (ReadResult, error) {
	if c == nil || !c.enabled {
		return ReadResult{}, ErrDisabled
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if snapshot.Manifest.Key != snapshot.Key || !sameNamespace(snapshot.Key, c.namespace) || snapshot.Manifest.StorageSecurityDomain != c.securityDomain || snapshot.Manifest.State != cachepostgres.StateAdmitted {
		return ReadResult{}, fmt.Errorf("%w: rejection admission snapshot", ErrSecurityDomain)
	}
	return c.reconcileMiss(ctx, snapshot.Manifest, reason)
}

// Get is a compact helper for callers that only need bytes and a hit bit. It
// still requires the exact admission snapshot key.
func (c *Cache) Get(ctx context.Context, snapshot AdmissionSnapshot) ([]byte, bool, error) {
	result, err := c.ReadSnapshot(ctx, snapshot)
	return result.Body, result.Hit, err
}

func (c *Cache) reconcileMiss(ctx context.Context, manifest cachepostgres.Manifest, reason string) (ReadResult, error) {
	evidence, _ := json.Marshal(map[string]any{"version": 1, "reason": "L3 object missing or corrupt: " + truncateReason(reason)})
	retirer, ok := c.authority.(ManifestRetirementAuthority)
	if !ok {
		return ReadResult{MissReason: reason}, fmt.Errorf("%w: authority cannot retire exact manifest", ErrReconciliation)
	}
	err := retirer.RetireManifest(ctx, manifest.ManifestID, evidence)
	if err != nil {
		return ReadResult{MissReason: reason}, fmt.Errorf("%w: %v", ErrReconciliation, err)
	}
	return ReadResult{Reconciled: true, MissReason: reason}, nil
}

func truncateReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 512 {
		return reason[:512]
	}
	return reason
}

func (c *Cache) verifyObject(ctx context.Context, key string, expected ObjectInfo) ([]byte, error) {
	obj, err := c.store.Open(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("%w: open object: %v", ErrObjectCorrupt, err)
	}
	if obj.Body == nil {
		return nil, fmt.Errorf("%w: object body is nil", ErrObjectCorrupt)
	}
	defer obj.Body.Close()
	if obj.Info.Key != expected.Key || obj.Info.SecurityDomain != expected.SecurityDomain || obj.Info.Digest != expected.Digest || obj.Info.Size != expected.Size {
		return nil, fmt.Errorf("%w: object metadata identity mismatch", ErrObjectCorrupt)
	}
	metadataDigest := expected.MetadataDigest
	if metadataDigest == "" {
		metadataDigest = digestBytes(expected.Metadata)
	}
	if obj.Info.MetadataDigest != metadataDigest {
		return nil, fmt.Errorf("%w: object metadata mismatch", ErrObjectCorrupt)
	}
	if len(obj.Info.Metadata) > 0 {
		metadata, metadataErr := canonicalMetadata(obj.Info.Metadata)
		if metadataErr != nil || !bytes.Equal(metadata, expected.Metadata) {
			return nil, fmt.Errorf("%w: object metadata mismatch", ErrObjectCorrupt)
		}
	}
	body, err := readBounded(obj.Body, c.maxObjectBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != expected.Size || digestBytes(body) != expected.Digest {
		return nil, fmt.Errorf("%w: object bytes digest or size mismatch", ErrObjectCorrupt)
	}
	return body, nil
}

func sameNamespace(key cachepostgres.ManifestKey, n cachepostgres.Namespace) bool {
	return key.PartitionKind == n.PartitionKind && key.TargetID == n.TargetID && key.ProjectID == n.ProjectID && key.Environment == n.Environment && key.CandidateID == n.CandidateID
}

func canonicalMetadata(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{}`), nil
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("%w: metadata is too large", ErrInvalid)
	}
	var object map[string]any
	if err := strictjson.DecodeWithOptions(raw, &object, strictjson.Options{MaxBytes: maxMetadataBytes, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil || object == nil {
		return nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
	}
	canonical, err := json.Marshal(object)
	if err != nil || len(canonical) > maxMetadataBytes {
		return nil, fmt.Errorf("%w: metadata serialization", ErrInvalid)
	}
	return canonical, nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("%w: max object bytes", ErrInvalid)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: object exceeds byte limit", ErrInvalid)
	}
	return data, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
