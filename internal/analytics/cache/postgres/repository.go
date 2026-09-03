// Package postgres persists shared result-cache metadata and fill fences.
// Result bytes are never written here: callers publish an immutable object and
// then commit its manifest through the same database transaction that removes
// the fill fence.
package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	analyticscache "github.com/flidai/leapview/internal/analytics/cache"
	cachedb "github.com/flidai/leapview/internal/analytics/cache/postgres/internal/db"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// MaintenanceDBTX is the native PostgreSQL surface for the separately
// authenticated maintenance connection. It mirrors DBTX so pools and
// caller-owned transactions can invoke the bounded facade without adapters;
// runtime repositories never retain this value.
type MaintenanceDBTX interface {
	DBTX
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Tx interface {
	DBTX
	Commit(context.Context) error
	Rollback(context.Context) error
}

// Maintenance owns destructive cache-coordination retention. Runtime
// Repository intentionally has no prune method.
type Maintenance struct{ db MaintenanceDBTX }

var (
	ErrInvalid    = errors.New("invalid cache input")
	ErrNotFound   = errors.New("cache manifest not found")
	ErrBusy       = errors.New("cache fill is owned by another worker")
	ErrStaleFence = errors.New("cache fill fence is stale")
	ErrConflict   = errors.New("cache manifest conflict")
	ErrEpoch      = errors.New("cache namespace epoch conflict")
)

const (
	StateAdmitted    = "admitted"
	StateRetiring    = "retiring"
	StateExpired     = "expired"
	maxMetadataBytes = 16384
	maxEvidenceBytes = 4096
	maxLeaseDuration = 24 * time.Hour
)

type PartitionKind string

const (
	PartitionProduction PartitionKind = "production"
	PartitionCandidate  PartitionKind = "candidate"
)

// ManifestKey addresses metadata without including object storage identity.
type ManifestKey struct {
	PartitionKind          PartitionKind
	TargetID               string
	ProjectID              string
	Environment            string
	CandidateID            string
	PartitionFormatVersion int64
	DependencyDigest       string
	PolicyFingerprint      string
	CanonicalQueryDigest   string
	KeyFormatVersion       int64
}

// CacheKeyDigest derives the durable fill identity from the exact manifest
// key columns. The digest is independent of object storage bytes and is used
// to prove that a fill fence cannot publish an unrelated key.
func (k ManifestKey) CacheKeyDigest() (string, error) {
	if err := validateKey(k); err != nil {
		return "", err
	}
	partition, err := k.partition()
	if err != nil {
		return "", err
	}
	key, err := analyticscache.NewKeyFromDigests(partition, k.DependencyDigest, k.PolicyFingerprint, k.CanonicalQueryDigest)
	if err != nil {
		return "", fmt.Errorf("%w: cache key: %v", ErrInvalid, err)
	}
	return key.Digest(), nil
}

func (k ManifestKey) partition() (resultidentity.Partition, error) {
	kind := resultidentity.PartitionKind(k.PartitionKind)
	if kind != resultidentity.PartitionProduction && kind != resultidentity.PartitionCandidate {
		return resultidentity.Partition{}, fmt.Errorf("%w: invalid partition kind", ErrInvalid)
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: kind, TargetID: k.TargetID, ProjectID: projectgraph.ResourceID(k.ProjectID), Environment: k.Environment, CandidateID: k.CandidateID,
	})
	if err != nil {
		return resultidentity.Partition{}, fmt.Errorf("%w: partition: %v", ErrInvalid, err)
	}
	return partition, nil
}

// ManifestKeyFromIdentity projects a canonical cache.Key into the typed
// partition and digest columns used by durable manifests.
func ManifestKeyFromIdentity(key analyticscache.Key) (ManifestKey, error) {
	if key.Version() == 0 {
		return ManifestKey{}, ErrInvalid
	}
	kind := PartitionProduction
	if key.Partition().Kind() == resultidentity.PartitionCandidate {
		kind = PartitionCandidate
	}
	partition := key.Partition()
	return ManifestKey{PartitionKind: kind, TargetID: partition.TargetID(), ProjectID: partition.ProjectID().String(), Environment: partition.Environment(), CandidateID: partition.CandidateID(), PartitionFormatVersion: int64(partition.Version()), DependencyDigest: key.DependencyDigest(), PolicyFingerprint: key.PolicyFingerprint(), CanonicalQueryDigest: key.CanonicalQueryDigest(), KeyFormatVersion: int64(key.Version())}, nil
}

type Manifest struct {
	ManifestID            uuid.UUID
	Key                   ManifestKey
	OriginSnapshotSealID  string
	StorageSecurityDomain string
	ObjectDigest          string
	ObjectKey             string
	ByteSize              int64
	Metadata              json.RawMessage
	State                 string
	CreatedAt             time.Time
	ExpiresAt             *time.Time
	RetiredAt             *time.Time
	ExpiredAt             *time.Time
	RetireEvidence        json.RawMessage
	ExpireEvidence        json.RawMessage
}

// lifecycleEvidence is a bounded, canonical transition record. Version 1
// requires at least one fact in addition to the version marker so operators
// can explain why a manifest or retention root moved state.

func lifecycleEvidence(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || len(raw) > maxEvidenceBytes {
		return nil, fmt.Errorf("%w: lifecycle evidence must be bounded and non-empty", ErrInvalid)
	}
	var object map[string]any
	if err := strictjson.DecodeWithOptions(raw, &object, strictjson.Options{MaxBytes: maxEvidenceBytes, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return nil, fmt.Errorf("%w: lifecycle evidence must be strict JSON: %v", ErrInvalid, err)
	}
	if object == nil || len(object) < 2 {
		return nil, fmt.Errorf("%w: lifecycle evidence requires version and a reason", ErrInvalid)
	}
	version, ok := object["version"].(float64)
	if !ok || version != 1 {
		return nil, fmt.Errorf("%w: unsupported lifecycle evidence version", ErrInvalid)
	}
	reason, ok := object["reason"].(string)
	if !ok || strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: lifecycle evidence reason is required", ErrInvalid)
	}
	canonical, err := json.Marshal(object)
	if err != nil || len(canonical) > maxEvidenceBytes {
		return nil, fmt.Errorf("%w: lifecycle evidence serialization", ErrInvalid)
	}
	return canonical, nil
}

func requiredEvidence(input json.RawMessage) ([]byte, error) {
	return lifecycleEvidence(input)
}

func sameLifecycleEvidence(stored, canonical []byte) bool {
	normalized, err := lifecycleEvidence(stored)
	return err == nil && bytes.Equal(normalized, canonical)
}

type LookupInput = ManifestKey

type AcquireFillInput struct {
	CacheKey  string
	OwnerID   string
	Lease     time.Duration
	Namespace Namespace
}

type FillLease struct {
	LeaseID        uuid.UUID
	CacheKey       string
	Namespace      Namespace
	NamespaceEpoch int64
	OwnerID        string
	FencingEpoch   int64
	ExpiresAt      time.Time
	AcquiredAt     time.Time
}

type PublishInput struct {
	Key                   ManifestKey
	OriginSnapshotSealID  string
	StorageSecurityDomain string
	ObjectDigest          string
	ObjectKey             string
	ByteSize              int64
	Metadata              json.RawMessage
	ExpiresAt             *time.Time
	Lease                 FillLease
}

// Namespace identifies the exact stable production or candidate cache scope.
// Serving generation identity is deliberately excluded: compatible results
// survive deployment cutovers and are invalidated by dependency revision.
type Namespace struct {
	PartitionKind PartitionKind
	TargetID      string
	ProjectID     string
	Environment   string
	CandidateID   string
}

// NamespaceKey is the canonical, bounded representation persisted by the
// coordination tables. It intentionally contains no result bytes.
func (n Namespace) Key() string {
	var canonical bytes.Buffer
	canonical.WriteString(`{"v":2,"k":`)
	writeNamespaceJSONString(&canonical, string(n.PartitionKind))
	canonical.WriteString(`,"t":`)
	writeNamespaceJSONString(&canonical, n.TargetID)
	canonical.WriteString(`,"p":`)
	writeNamespaceJSONString(&canonical, n.ProjectID)
	canonical.WriteString(`,"e":`)
	writeNamespaceJSONString(&canonical, n.Environment)
	if n.CandidateID != "" {
		canonical.WriteString(`,"c":`)
		writeNamespaceJSONString(&canonical, n.CandidateID)
	}
	canonical.WriteByte('}')
	return "ns2_" + base64.RawURLEncoding.EncodeToString(canonical.Bytes())
}

// writeNamespaceJSONString matches PostgreSQL to_json(text): JSON control,
// quote, and slash escaping without JavaScript-specific HTML or line-separator
// escaping. Namespace fields are validated as canonical UTF-8 before use.
func writeNamespaceJSONString(dst *bytes.Buffer, value string) {
	const hex = "0123456789abcdef"
	dst.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			dst.WriteByte('\\')
			dst.WriteRune(character)
		case '\b':
			dst.WriteString(`\b`)
		case '\f':
			dst.WriteString(`\f`)
		case '\n':
			dst.WriteString(`\n`)
		case '\r':
			dst.WriteString(`\r`)
		case '\t':
			dst.WriteString(`\t`)
		default:
			if character < 0x20 {
				dst.WriteString(`\u00`)
				dst.WriteByte(hex[byte(character)>>4])
				dst.WriteByte(hex[byte(character)&0x0f])
				continue
			}
			dst.WriteRune(character)
		}
	}
	dst.WriteByte('"')
}

type DependencyKind string

const (
	DependencySource        DependencyKind = "source"
	DependencyProject       DependencyKind = "project"
	DependencySemanticModel DependencyKind = "semantic_model"
	DependencyDeployment    DependencyKind = "deployment"
	DependencyCustom        DependencyKind = "custom"
)

type DependencyRevision struct {
	Namespace      Namespace
	Kind           DependencyKind
	DependencyID   string
	Revision       int64
	RevisionDigest string
	UpdatedAt      time.Time
}

type DependencyRevisionInput struct {
	Namespace        Namespace
	Kind             DependencyKind
	DependencyID     string
	RevisionDigest   string
	ExpectedRevision int64
	IdempotencyKey   string
	Evidence         json.RawMessage
}

type NamespaceInvalidationInput struct {
	Namespace        Namespace
	Kind             DependencyKind
	DependencyID     string
	DependencyDigest string
	ExpectedEpoch    int64
	IdempotencyKey   string
	Reason           string
}

type InvalidationResult struct {
	InvalidationID   uuid.UUID
	EventID          int64
	Namespace        Namespace
	NamespaceEpoch   int64
	RetiredManifests int64
	CreatedAt        time.Time
}

type Invalidation struct {
	InvalidationResult
	Kind             DependencyKind
	DependencyID     string
	DependencyDigest string
	Reason           string
	Evidence         json.RawMessage
}

type NotificationHint struct {
	EventID      int64
	NamespaceKey string
}

type ReconcileOptions struct {
	AfterEventID int64
	Limit        int
}

type PruneOptions struct {
	Before time.Time
	Limit  int
}

type PruneStats struct {
	Invalidations int64
	ExpiredLeases int64
}

type CacheStats struct {
	AdmittedManifests  int64
	RetiringManifests  int64
	ExpiredManifests   int64
	ActiveFills        int64
	InvalidationEvents int64
	MaxEpoch           int64
}

const (
	NotificationChannel = "leapview_cache_invalidation"
	maxReconcileBatch   = 1000
	maxPruneBatch       = 1000
)

type Repository struct {
	db    DBTX
	lease time.Duration
}

// NewMaintenance constructs the bounded cache-retention facade.
func NewMaintenance(db MaintenanceDBTX) *Maintenance { return &Maintenance{db: db} }

func dbUUID(id uuid.UUID) pgtype.UUID { return pgtype.UUID{Bytes: id, Valid: true} }

func dbTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func timeValue(value pgtype.Timestamptz) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	t := value.Time.UTC()
	return &t, nil
}

func manifestFromRow(row cachedb.CacheCacheManifest) (Manifest, error) {
	if !row.ManifestID.Valid || !row.CreatedAt.Valid {
		return Manifest{}, fmt.Errorf("%w: persisted manifest identity or timestamp is null", ErrInvalid)
	}
	if !row.OriginSnapshotSealID.Valid {
		return Manifest{}, fmt.Errorf("%w: persisted origin snapshot seal is null", ErrInvalid)
	}
	expires, err := timeValue(row.ExpiresAt)
	if err != nil {
		return Manifest{}, err
	}
	retired, err := timeValue(row.RetiredAt)
	if err != nil {
		return Manifest{}, err
	}
	expired, err := timeValue(row.ExpiredAt)
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{
		ManifestID:            row.ManifestID.Bytes,
		Key:                   ManifestKey{PartitionKind: PartitionKind(row.PartitionKind), TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment, CandidateID: valueOrEmpty(row.CandidateID), PartitionFormatVersion: row.PartitionFormatVersion, DependencyDigest: row.DependencyDigest, PolicyFingerprint: row.PolicyFingerprint, CanonicalQueryDigest: row.CanonicalQueryDigest, KeyFormatVersion: row.KeyFormatVersion},
		OriginSnapshotSealID:  uuid.UUID(row.OriginSnapshotSealID.Bytes).String(),
		StorageSecurityDomain: row.StorageSecurityDomain, ObjectDigest: row.ObjectDigest, ObjectKey: row.ObjectKey, ByteSize: row.ByteSize,
		Metadata: append(json.RawMessage(nil), row.Metadata...), State: row.State, CreatedAt: row.CreatedAt.Time.UTC(), ExpiresAt: expires, RetiredAt: retired, ExpiredAt: expired,
		RetireEvidence: append(json.RawMessage(nil), row.RetireEvidence...), ExpireEvidence: append(json.RawMessage(nil), row.ExpireEvidence...),
	}, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

//go:embed schema.sql
var schemaSQL string

func SchemaSQL() string { return schemaSQL }

func ApplySchema(ctx context.Context, tx Tx) error {
	if tx == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: schema-ddl. Capability-owned schema DDL is embedded and
	// applied by migration runners rather than generated query code.
	_, err := tx.Exec(ctx, schemaSQL)
	return err
}

func New(db DBTX) *Repository { return &Repository{db: db, lease: 30 * time.Second} }
func NewWithConfig(db DBTX, lease time.Duration) *Repository {
	r := New(db)
	if lease > 0 {
		r.lease = lease
	}
	return r
}

func validateNamespace(n Namespace) error {
	if n.PartitionKind != PartitionProduction && n.PartitionKind != PartitionCandidate {
		return fmt.Errorf("%w: invalid namespace partition", ErrInvalid)
	}
	if err := validatePartitionScope(ManifestKey{PartitionKind: n.PartitionKind, TargetID: n.TargetID, ProjectID: n.ProjectID, Environment: n.Environment, CandidateID: n.CandidateID, PartitionFormatVersion: resultidentity.PartitionVersion}); err != nil {
		return err
	}
	if len(n.Key()) > 2048 {
		return fmt.Errorf("%w: namespace key is too long", ErrInvalid)
	}
	return nil
}

func validateDependencyInput(in DependencyRevisionInput) error {
	if err := validateNamespace(in.Namespace); err != nil {
		return err
	}
	if in.Kind != DependencySource && in.Kind != DependencyProject && in.Kind != DependencySemanticModel && in.Kind != DependencyDeployment && in.Kind != DependencyCustom {
		return fmt.Errorf("%w: invalid dependency kind", ErrInvalid)
	}
	if !literal(in.DependencyID, 255) || platformdigest.ValidateSHA256Identity(in.RevisionDigest) != nil {
		return fmt.Errorf("%w: invalid dependency revision", ErrInvalid)
	}
	if in.ExpectedRevision < 0 {
		return fmt.Errorf("%w: invalid expected dependency revision", ErrInvalid)
	}
	if in.IdempotencyKey != "" && !literal(in.IdempotencyKey, 255) {
		return fmt.Errorf("%w: invalid idempotency key", ErrInvalid)
	}
	return nil
}

func sameNamespaceKey(key ManifestKey, namespace Namespace) bool {
	return key.PartitionKind == namespace.PartitionKind &&
		key.TargetID == namespace.TargetID &&
		key.ProjectID == namespace.ProjectID &&
		key.Environment == namespace.Environment &&
		key.CandidateID == namespace.CandidateID
}

func ensureNamespaceTx(ctx context.Context, tx Tx, n Namespace) (int64, error) {
	if err := validateNamespace(n); err != nil {
		return 0, err
	}
	return cachedb.New(tx).EnsureNamespace(ctx, cachedb.EnsureNamespaceParams{NamespaceKey: n.Key(), PartitionKind: string(n.PartitionKind), TargetID: n.TargetID, ProjectID: n.ProjectID, Environment: n.Environment, CandidateID: nullableString(n.CandidateID)})
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// candidateArg is retained for capability-owned test/setup SQL that binds a
// nullable candidate identifier directly.
func candidateArg(candidateID string) any {
	if candidateID == "" {
		return nil
	}
	return candidateID
}

// CurrentEpoch returns the durable invalidation epoch for an exact namespace.
// Missing namespaces begin at epoch one without mutating the database.
func (r *Repository) CurrentEpoch(ctx context.Context, n Namespace) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrInvalid
	}
	if err := validateNamespace(n); err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	epoch, err := cachedb.New(r.db).GetNamespaceEpoch(ctx, n.Key())
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	return epoch, err
}

func validateKey(k ManifestKey) error {
	if err := validatePartitionScope(k); err != nil {
		return err
	}
	for name, digest := range map[string]string{"dependency": k.DependencyDigest, "policy": k.PolicyFingerprint, "query": k.CanonicalQueryDigest} {
		if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
			return fmt.Errorf("%w: %s digest: %v", ErrInvalid, name, err)
		}
	}
	if k.KeyFormatVersion != int64(analyticscache.CacheKeyVersion) {
		return fmt.Errorf("%w: unsupported key format version", ErrInvalid)
	}
	return nil
}

func validatePartitionScope(k ManifestKey) error {
	if k.PartitionKind != PartitionProduction && k.PartitionKind != PartitionCandidate {
		return fmt.Errorf("%w: invalid partition kind", ErrInvalid)
	}
	partition, err := k.partition()
	if err != nil {
		return err
	}
	if int64(partition.Version()) != k.PartitionFormatVersion {
		return fmt.Errorf("%w: unsupported partition format version", ErrInvalid)
	}
	return nil
}

func validatePublish(in PublishInput) error {
	if err := validateKey(in.Key); err != nil {
		return err
	}
	if platformdigest.ValidateSHA256Identity(in.StorageSecurityDomain) != nil || !literal(in.ObjectKey, 2048) {
		return fmt.Errorf("%w: invalid storage identity", ErrInvalid)
	}
	if _, err := parseSnapshotSealID(in.OriginSnapshotSealID); err != nil {
		return err
	}
	if err := platformdigest.ValidateSHA256Identity(in.ObjectDigest); err != nil {
		return fmt.Errorf("%w: object digest: %v", ErrInvalid, err)
	}
	if in.ByteSize <= 0 {
		return fmt.Errorf("%w: object size must be positive", ErrInvalid)
	}
	if _, err := canonicalMetadata(in.Metadata); err != nil {
		return err
	}
	if in.Lease.LeaseID == uuid.Nil || !literal(in.Lease.CacheKey, 255) || !literal(in.Lease.OwnerID, 255) || in.Lease.FencingEpoch <= 0 {
		return fmt.Errorf("%w: invalid fill fence", ErrInvalid)
	}
	if !sameNamespaceKey(in.Key, in.Lease.Namespace) {
		return fmt.Errorf("%w: fill namespace does not match manifest partition", ErrStaleFence)
	}
	expected, err := in.Key.CacheKeyDigest()
	if err != nil || in.Lease.CacheKey != expected {
		return fmt.Errorf("%w: fill fence key does not match manifest key", ErrStaleFence)
	}
	return nil
}

func parseSnapshotSealID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return uuid.Nil, fmt.Errorf("%w: invalid origin snapshot seal id", ErrInvalid)
	}
	return id, nil
}

func canonicalMetadata(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte(`{}`), nil
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("%w: metadata must be bounded JSON", ErrInvalid)
	}
	var object map[string]any
	if err := strictjson.DecodeWithOptions(raw, &object, strictjson.Options{MaxBytes: maxMetadataBytes, DuplicateKeys: strictjson.CaseSensitiveKeys}); err != nil {
		return nil, fmt.Errorf("%w: metadata must be strict JSON: %v", ErrInvalid, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%w: metadata must be a JSON object", ErrInvalid)
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: metadata serialization: %v", ErrInvalid, err)
	}
	if len(canonical) > maxMetadataBytes {
		return nil, fmt.Errorf("%w: metadata must be bounded JSON", ErrInvalid)
	}
	return canonical, nil
}

func normalizedExpiry(expiry *time.Time) *time.Time {
	if expiry == nil {
		return nil
	}
	value := expiry.UTC().Truncate(time.Microsecond)
	return &value
}

func sameExpiry(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.UTC().Equal(right.UTC())
}

func literal(value string, max int) bool {
	return value != "" && len(value) <= max && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func (r *Repository) Lookup(ctx context.Context, in LookupInput) (Manifest, bool, error) {
	if r == nil || r.db == nil {
		return Manifest{}, false, ErrInvalid
	}
	if err := validateKey(in); err != nil {
		return Manifest{}, false, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	row, err := cachedb.New(r.db).GetManifest(ctx, cachedb.GetManifestParams{PartitionKind: string(in.PartitionKind), TargetID: in.TargetID, ProjectID: in.ProjectID, Environment: in.Environment, CandidateID: nullableString(in.CandidateID), PartitionFormatVersion: in.PartitionFormatVersion, DependencyDigest: in.DependencyDigest, PolicyFingerprint: in.PolicyFingerprint, CanonicalQueryDigest: in.CanonicalQueryDigest, KeyFormatVersion: in.KeyFormatVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	manifest, err := manifestFromRow(row)
	return manifest, err == nil, err
}

func (r *Repository) ListByDependency(ctx context.Context, partitionKind PartitionKind, targetID, projectID, environment, candidateID, dependency string, limit int) ([]Manifest, error) {
	if platformdigest.ValidateSHA256Identity(dependency) != nil || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	if err := validatePartitionScope(ManifestKey{PartitionKind: partitionKind, TargetID: targetID, ProjectID: projectID, Environment: environment, CandidateID: candidateID, PartitionFormatVersion: resultidentity.PartitionVersion}); err != nil {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := cachedb.New(r.db).ListManifestsByDependency(ctx, cachedb.ListManifestsByDependencyParams{PartitionKind: string(partitionKind), TargetID: targetID, ProjectID: projectID, Environment: environment, CandidateID: nullableString(candidateID), PartitionFormatVersion: resultidentity.PartitionVersion, DependencyDigest: dependency, LimitCount: int32(limit)})
	if err != nil {
		return nil, err
	}
	result := make([]Manifest, 0, limit)
	for _, row := range rows {
		m, e := manifestFromRow(row)
		if e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, nil
}

// ObjectReachable reports whether an object is still protected by the durable
// cache authority. Admitted and retiring manifests remain reachable; an
// expired manifest is also reachable while any live or retiring retention
// root points at it. The check is deliberately scoped by namespace and
// storage-security domain so an object from another isolation boundary can
// never become a reason to retain or delete this namespace's object.
func (r *Repository) ObjectReachable(ctx context.Context, n Namespace, securityDomain, objectKey string) (bool, error) {
	if r == nil || r.db == nil {
		return false, ErrInvalid
	}
	if err := validateNamespace(n); err != nil {
		return false, err
	}
	if platformdigest.ValidateSHA256Identity(securityDomain) != nil || !literal(objectKey, 2048) {
		return false, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return cachedb.New(r.db).ObjectReachable(ctx, cachedb.ObjectReachableParams{PartitionKind: string(n.PartitionKind), TargetID: n.TargetID, ProjectID: n.ProjectID, Environment: n.Environment, CandidateID: nullableString(n.CandidateID), StorageSecurityDomain: securityDomain, ObjectKey: objectKey})
}

// InvalidateNamespace records a durable, idempotent invalidation and advances
// the namespace epoch in the same PostgreSQL transaction that retires matching
// manifests. A notification is emitted by the schema trigger only after the
// transaction commits; consumers must reconcile by event_id.
func (r *Repository) InvalidateNamespace(ctx context.Context, in NamespaceInvalidationInput, evidence json.RawMessage) (InvalidationResult, error) {
	if r == nil || r.db == nil {
		return InvalidationResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return InvalidationResult{}, fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return InvalidationResult{}, err
	}
	out, err := r.InvalidateNamespaceTx(ctx, tx, in, evidence)
	if err != nil {
		_ = tx.Rollback(ctx)
		return InvalidationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InvalidationResult{}, err
	}
	return out, nil
}

func (r *Repository) InvalidateNamespaceTx(ctx context.Context, tx Tx, in NamespaceInvalidationInput, evidence json.RawMessage) (InvalidationResult, error) {
	if tx == nil {
		return InvalidationResult{}, ErrInvalid
	}
	if err := validateNamespace(in.Namespace); err != nil {
		return InvalidationResult{}, err
	}
	if in.Kind != DependencySource && in.Kind != DependencyProject && in.Kind != DependencySemanticModel && in.Kind != DependencyDeployment && in.Kind != DependencyCustom {
		return InvalidationResult{}, fmt.Errorf("%w: invalid dependency kind", ErrInvalid)
	}
	if !literal(in.DependencyID, 255) {
		return InvalidationResult{}, fmt.Errorf("%w: invalid dependency id", ErrInvalid)
	}
	if in.DependencyDigest != "" && platformdigest.ValidateSHA256Identity(in.DependencyDigest) != nil {
		return InvalidationResult{}, ErrInvalid
	}
	if !literal(in.IdempotencyKey, 255) {
		return InvalidationResult{}, fmt.Errorf("%w: idempotency key is required", ErrInvalid)
	}
	canonicalEvidence, err := requiredEvidence(evidence)
	if err != nil {
		return InvalidationResult{}, err
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "dependency revision changed"
	}
	if !literal(reason, 255) {
		return InvalidationResult{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := ensureNamespaceTx(ctx, tx, in.Namespace); err != nil {
		return InvalidationResult{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return InvalidationResult{}, err
	}
	row, err := cachedb.New(tx).InvalidateNamespace(ctx, cachedb.InvalidateNamespaceParams{InvalidationID: dbUUID(id), NamespaceKey: in.Namespace.Key(), DependencyKind: string(in.Kind), DependencyID: in.DependencyID, DependencyDigest: nullableString(in.DependencyDigest), ExpectedEpoch: in.ExpectedEpoch, IdempotencyKey: in.IdempotencyKey, Reason: reason, Evidence: canonicalEvidence})
	if err != nil {
		if strings.Contains(err.Error(), "epoch conflict") {
			return InvalidationResult{}, ErrEpoch
		}
		if strings.Contains(err.Error(), "invalidation conflict") {
			return InvalidationResult{}, ErrConflict
		}
		return InvalidationResult{}, err
	}
	if !row.InvalidationID.Valid || !row.CreatedAt.Valid {
		return InvalidationResult{}, fmt.Errorf("%w: invalidation result is incomplete", ErrInvalid)
	}
	out := InvalidationResult{InvalidationID: row.InvalidationID.Bytes, EventID: row.EventID, NamespaceEpoch: row.NamespaceEpoch, RetiredManifests: row.RetiredManifests, CreatedAt: row.CreatedAt.Time.UTC()}
	out.Namespace = in.Namespace
	out.CreatedAt = out.CreatedAt.UTC()
	return out, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// RecordDependencyRevision publishes a source/project/semantic/deployment
// revision and, when its digest changes, invalidates the exact namespace in
// the same transaction. This is the transaction-aware hook used by control
// plane mutation code; callers that already own a transaction should use the
// Tx variant.
func (r *Repository) RecordDependencyRevision(ctx context.Context, in DependencyRevisionInput) (DependencyRevision, error) {
	if r == nil || r.db == nil {
		return DependencyRevision{}, ErrInvalid
	}
	if err := validateDependencyInput(in); err != nil {
		return DependencyRevision{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return DependencyRevision{}, fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return DependencyRevision{}, err
	}
	out, err := r.RecordDependencyRevisionTx(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback(ctx)
		return DependencyRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return DependencyRevision{}, err
	}
	return out, nil
}

func (r *Repository) RecordDependencyRevisionTx(ctx context.Context, tx Tx, in DependencyRevisionInput) (DependencyRevision, error) {
	if tx == nil {
		return DependencyRevision{}, ErrInvalid
	}
	if err := validateDependencyInput(in); err != nil {
		return DependencyRevision{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := ensureNamespaceTx(ctx, tx, in.Namespace); err != nil {
		return DependencyRevision{}, err
	}
	var revisionEvidence []byte
	if len(in.Evidence) > 0 {
		canonicalEvidence, evidenceErr := lifecycleEvidence(in.Evidence)
		if evidenceErr != nil {
			return DependencyRevision{}, evidenceErr
		}
		revisionEvidence = canonicalEvidence
	}
	key := in.IdempotencyKey
	if key == "" {
		key = generatedRevisionIdempotencyKey(in.Namespace, in.Kind, in.DependencyID, in.RevisionDigest)
	}
	invID, err := uuid.NewV7()
	if err != nil {
		return DependencyRevision{}, err
	}
	row, err := cachedb.New(tx).RecordDependencyRevision(ctx, cachedb.RecordDependencyRevisionParams{NamespaceKey: in.Namespace.Key(), DependencyKind: string(in.Kind), DependencyID: in.DependencyID, RevisionDigest: in.RevisionDigest, ExpectedRevision: in.ExpectedRevision, InvalidationID: dbUUID(invID), IdempotencyKey: key, Reason: "dependency revision changed", Evidence: revisionEvidence})
	if err != nil {
		if strings.Contains(err.Error(), "revision conflict") {
			return DependencyRevision{}, ErrConflict
		}
		if strings.Contains(err.Error(), "revision change evidence") {
			return DependencyRevision{}, fmt.Errorf("%w: revision change evidence is required", ErrInvalid)
		}
		return DependencyRevision{}, err
	}
	revision, digest := row.Revision, row.RevisionDigest
	var updated time.Time
	if row.UpdatedAt.Valid {
		updated = row.UpdatedAt.Time
	}
	if updated.IsZero() {
		stamp, err := cachedb.New(tx).GetDependencyRevisionUpdatedAt(ctx, cachedb.GetDependencyRevisionUpdatedAtParams{NamespaceKey: in.Namespace.Key(), DependencyKind: string(in.Kind), DependencyID: in.DependencyID})
		if err != nil {
			return DependencyRevision{}, err
		}
		if stamp.Valid {
			updated = stamp.Time
		}
	}
	return DependencyRevision{Namespace: in.Namespace, Kind: in.Kind, DependencyID: in.DependencyID, Revision: revision, RevisionDigest: digest, UpdatedAt: updated.UTC()}, nil
}

func generatedRevisionIdempotencyKey(n Namespace, kind DependencyKind, id, digest string) string {
	preimage := n.Key() + "\x00" + string(kind) + "\x00" + id + "\x00" + digest
	sum := sha256.Sum256([]byte(preimage))
	return "revision:sha256:" + hex.EncodeToString(sum[:])
}

// AdvanceDependencyRevision is a descriptive alias for callers that model
// source/project/semantic/deployment changes as monotonic revisions.
func (r *Repository) AdvanceDependencyRevision(ctx context.Context, in DependencyRevisionInput) (DependencyRevision, error) {
	return r.RecordDependencyRevision(ctx, in)
}

func (r *Repository) InvalidateSource(ctx context.Context, n Namespace, id, digest string, evidence json.RawMessage) (InvalidationResult, error) {
	return r.InvalidateNamespace(ctx, derivedInvalidationInput(n, DependencySource, id, digest, "source revision changed"), evidence)
}

func (r *Repository) InvalidateProject(ctx context.Context, n Namespace, id, digest string, evidence json.RawMessage) (InvalidationResult, error) {
	return r.InvalidateNamespace(ctx, derivedInvalidationInput(n, DependencyProject, id, digest, "project revision changed"), evidence)
}

func (r *Repository) InvalidateSemanticModel(ctx context.Context, n Namespace, id, digest string, evidence json.RawMessage) (InvalidationResult, error) {
	return r.InvalidateNamespace(ctx, derivedInvalidationInput(n, DependencySemanticModel, id, digest, "semantic model revision changed"), evidence)
}

func (r *Repository) InvalidateDeployment(ctx context.Context, n Namespace, id, digest string, evidence json.RawMessage) (InvalidationResult, error) {
	return r.InvalidateNamespace(ctx, derivedInvalidationInput(n, DependencyDeployment, id, digest, "deployment revision changed"), evidence)
}

func derivedInvalidationInput(n Namespace, kind DependencyKind, id, digest, reason string) NamespaceInvalidationInput {
	preimage := n.Key() + "\x00" + string(kind) + "\x00" + id + "\x00" + digest + "\x00" + reason
	sum := sha256.Sum256([]byte(preimage))
	return NamespaceInvalidationInput{
		Namespace: n, Kind: kind, DependencyID: id, DependencyDigest: digest,
		IdempotencyKey: "invalidation:sha256:" + hex.EncodeToString(sum[:]), Reason: reason,
	}
}

// ParseNotificationHint validates the bounded JSON emitted by the cache
// trigger. Unknown or oversized fields are ignored by design; event_id is the
// durable cursor and namespace is only a wake-up filter.
func ParseNotificationHint(payload string) (NotificationHint, error) {
	if len(payload) == 0 || len(payload) > 7900 {
		return NotificationHint{}, ErrInvalid
	}
	var wire struct {
		EventID   int64  `json:"event_id"`
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(payload), &wire); err != nil || wire.EventID <= 0 || !literal(wire.Namespace, 2048) {
		return NotificationHint{}, ErrInvalid
	}
	return NotificationHint{EventID: wire.EventID, NamespaceKey: wire.Namespace}, nil
}

// NotificationConn is the minimal pgx connection surface required to install
// the LISTEN hint. LISTEN state is connection-local and must be re-established
// after reconnect; durable reconciliation uses ReconcileInvalidations.
type NotificationConn interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func Listen(ctx context.Context, conn NotificationConn) error {
	if conn == nil {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// sqlc-exception: listen-protocol. LISTEN is connection-local PostgreSQL
	// protocol control and cannot be represented by sqlc.
	_, err := conn.Exec(ctx, `LISTEN `+NotificationChannel)
	return err
}

type NotificationWaiter interface {
	WaitForNotification(context.Context) (*pgconn.Notification, error)
}

func ReceiveNotification(ctx context.Context, conn NotificationWaiter) (NotificationHint, error) {
	if conn == nil {
		return NotificationHint{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		return NotificationHint{}, err
	}
	if n == nil || n.Channel != NotificationChannel {
		return NotificationHint{}, ErrInvalid
	}
	return ParseNotificationHint(n.Payload)
}

// ReconcileInvalidations reads the durable invalidation log after a cursor.
// It is safe to call after dropped notifications, reconnects, or duplicate
// hints; event_id ordering is the only delivery contract.
func (r *Repository) ReconcileInvalidations(ctx context.Context, opts ReconcileOptions) ([]Invalidation, error) {
	if r == nil || r.db == nil || opts.AfterEventID < 0 {
		return nil, ErrInvalid
	}
	limit := opts.Limit
	if limit == 0 {
		limit = maxReconcileBatch
	}
	if limit < 1 || limit > maxReconcileBatch {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := cachedb.New(r.db).ReconcileInvalidations(ctx, cachedb.ReconcileInvalidationsParams{AfterEventID: opts.AfterEventID, LimitCount: int32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]Invalidation, 0, limit)
	for _, row := range rows {
		var item Invalidation
		if !row.InvalidationID.Valid || !row.CreatedAt.Valid {
			return nil, ErrConflict
		}
		item.InvalidationResult = InvalidationResult{InvalidationID: row.InvalidationID.Bytes, EventID: row.EventID, Namespace: Namespace{PartitionKind: PartitionKind(row.PartitionKind), TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment, CandidateID: valueOrEmpty(row.CandidateID)}, NamespaceEpoch: row.NamespaceEpoch, RetiredManifests: row.RetiredManifests, CreatedAt: row.CreatedAt.Time.UTC()}
		item.Kind = DependencyKind(row.DependencyKind)
		item.DependencyID = row.DependencyID
		item.DependencyDigest = valueOrEmpty(row.DependencyDigest)
		item.Reason = row.Reason
		item.Evidence = append(json.RawMessage(nil), row.Evidence...)
		if row.NamespaceKey != item.Namespace.Key() {
			return nil, ErrConflict
		}
		out = append(out, item)
	}
	return out, nil
}

// Reconcile is a short alias used by listeners that treat NOTIFY as a wake
// hint and persist only the event cursor.
func (r *Repository) Reconcile(ctx context.Context, opts ReconcileOptions) ([]Invalidation, error) {
	return r.ReconcileInvalidations(ctx, opts)
}

func (r *Repository) Stats(ctx context.Context) (CacheStats, error) {
	if r == nil || r.db == nil {
		return CacheStats{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	manifestCounts, err := cachedb.New(r.db).CountManifestStates(ctx)
	if err != nil {
		return CacheStats{}, err
	}
	activeFills, err := cachedb.New(r.db).CountActiveFills(ctx)
	if err != nil {
		return CacheStats{}, err
	}
	eventCounts, err := cachedb.New(r.db).CountInvalidationEventsAndMaxEpoch(ctx)
	if err != nil {
		return CacheStats{}, err
	}
	return CacheStats{AdmittedManifests: manifestCounts.AdmittedManifests, RetiringManifests: manifestCounts.RetiringManifests, ExpiredManifests: manifestCounts.ExpiredManifests, ActiveFills: activeFills, InvalidationEvents: eventCounts.InvalidationEvents, MaxEpoch: eventCounts.MaxEpoch}, nil
}

// AcquireFill atomically claims a key or returns ErrBusy. Expired ownership
// is replaced with a strictly higher fencing epoch.
func (r *Repository) AcquireFill(ctx context.Context, in AcquireFillInput) (FillLease, error) {
	if r == nil || r.db == nil || platformdigest.ValidateSHA256Identity(in.CacheKey) != nil || !literal(in.OwnerID, 255) || validateNamespace(in.Namespace) != nil {
		return FillLease{}, ErrInvalid
	}
	lease := in.Lease
	if lease <= 0 {
		lease = r.lease
	}
	if lease <= 0 || lease > maxLeaseDuration {
		return FillLease{}, ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return FillLease{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return FillLease{}, fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return FillLease{}, err
	}
	namespaceEpoch, err := ensureNamespaceTx(ctx, tx, in.Namespace)
	if err != nil {
		_ = tx.Rollback(ctx)
		return FillLease{}, err
	}
	row, err := cachedb.New(tx).AcquireFill(ctx, cachedb.AcquireFillParams{LeaseID: dbUUID(id), CacheKey: in.CacheKey, NamespaceKey: in.Namespace.Key(), NamespaceEpoch: namespaceEpoch, OwnerID: in.OwnerID, LeaseMicroseconds: lease.Microseconds()})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return FillLease{}, ErrBusy
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return FillLease{}, err
	}
	if !row.LeaseID.Valid || !row.ExpiresAt.Valid || !row.AcquiredAt.Valid {
		_ = tx.Rollback(ctx)
		return FillLease{}, ErrBusy
	}
	if err := tx.Commit(ctx); err != nil {
		return FillLease{}, err
	}
	out := FillLease{LeaseID: row.LeaseID.Bytes, CacheKey: row.CacheKey, Namespace: in.Namespace, NamespaceEpoch: row.NamespaceEpoch, OwnerID: row.OwnerID, FencingEpoch: row.FencingEpoch, ExpiresAt: row.ExpiresAt.Time.UTC(), AcquiredAt: row.AcquiredAt.Time.UTC()}
	return out, nil
}

func (r *Repository) RenewFill(ctx context.Context, lease FillLease, duration time.Duration) error {
	if err := validLease(lease); err != nil || duration <= 0 || duration > maxLeaseDuration {
		return ErrInvalid
	}
	ok, err := cachedb.New(r.db).RenewFill(ctx, cachedb.RenewFillParams{LeaseID: dbUUID(lease.LeaseID), CacheKey: lease.CacheKey, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch, LeaseMicroseconds: duration.Microseconds()})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}
func (r *Repository) ReleaseFill(ctx context.Context, lease FillLease) error {
	if err := validLease(lease); err != nil {
		return ErrInvalid
	}
	// Retain the row as an expired fence so the next owner receives a strictly
	// higher epoch. A maintenance reconciler may remove old rows only after its
	// configured grace period.
	ok, err := cachedb.New(r.db).ReleaseFill(ctx, cachedb.ReleaseFillParams{LeaseID: dbUUID(lease.LeaseID), CacheKey: lease.CacheKey, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch})
	if err != nil {
		return err
	}
	if !ok {
		return ErrStaleFence
	}
	return nil
}
func validLease(l FillLease) error {
	if l.LeaseID == uuid.Nil || platformdigest.ValidateSHA256Identity(l.CacheKey) != nil || validateNamespace(l.Namespace) != nil || l.NamespaceEpoch <= 0 || !literal(l.OwnerID, 255) || l.FencingEpoch <= 0 {
		return ErrInvalid
	}
	return nil
}

// Publish commits the manifest and releases the exact fill fence atomically.
func (r *Repository) Publish(ctx context.Context, in PublishInput) (Manifest, error) {
	if err := validatePublish(in); err != nil {
		return Manifest{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return Manifest{}, fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return Manifest{}, err
	}
	m, err := r.PublishTx(ctx, tx, in)
	if err != nil {
		_ = tx.Rollback(ctx)
		return Manifest{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
func (r *Repository) PublishTx(ctx context.Context, tx Tx, in PublishInput) (Manifest, error) {
	if tx == nil {
		return Manifest{}, ErrInvalid
	}
	if err := validatePublish(in); err != nil {
		return Manifest{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	metadata, err := canonicalMetadata(in.Metadata)
	if err != nil {
		return Manifest{}, err
	}
	originSealID, err := parseSnapshotSealID(in.OriginSnapshotSealID)
	if err != nil {
		return Manifest{}, err
	}
	expiresAt := normalizedExpiry(in.ExpiresAt)
	var namespaceEpoch int64
	var namespaceKey string
	var fenceManifest *uuid.UUID
	var fenceActive bool
	_, err = cachedb.New(tx).GetNamespaceEpochForUpdate(ctx, in.Lease.Namespace.Key())
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrStaleFence
	} else if err != nil {
		return Manifest{}, err
	}
	leaseRow, err := cachedb.New(tx).GetFillLeaseForUpdate(ctx, cachedb.GetFillLeaseForUpdateParams{CacheKey: in.Lease.CacheKey, NamespaceKey: in.Lease.Namespace.Key(), OwnerID: in.Lease.OwnerID, FencingEpoch: in.Lease.FencingEpoch, LeaseID: dbUUID(in.Lease.LeaseID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrStaleFence
	} else if err != nil {
		return Manifest{}, err
	}
	namespaceKey, namespaceEpoch, fenceManifest, fenceActive = leaseRow.NamespaceKey, leaseRow.NamespaceEpoch, nil, leaseRow.Active
	if leaseRow.ManifestID.Valid {
		id := uuid.UUID(leaseRow.ManifestID.Bytes)
		fenceManifest = &id
	}
	if in.Lease.Namespace.Key() != namespaceKey {
		return Manifest{}, ErrStaleFence
	}
	if in.Lease.NamespaceEpoch != namespaceEpoch {
		return Manifest{}, ErrStaleFence
	}
	var currentEpoch int64
	currentEpoch, err = cachedb.New(tx).GetNamespaceEpoch(ctx, namespaceKey)
	if errors.Is(err, pgx.ErrNoRows) || currentEpoch != namespaceEpoch {
		return Manifest{}, ErrStaleFence
	} else if err != nil {
		return Manifest{}, err
	}
	if fenceManifest != nil {
		row, scanErr := cachedb.New(tx).GetManifestByID(ctx, dbUUID(*fenceManifest))
		m, convErr := manifestFromRow(row)
		if scanErr == nil {
			scanErr = convErr
		}
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return Manifest{}, ErrConflict
		}
		if scanErr != nil {
			return Manifest{}, scanErr
		}
		var admittedLive bool
		admittedValue, err := cachedb.New(tx).ManifestIsAdmittedLive(ctx, dbUUID(m.ManifestID))
		if err != nil {
			return Manifest{}, err
		}
		admittedLive = admittedValue != nil && *admittedValue
		if !admittedLive || !publishManifestEqual(m, in, metadata, expiresAt) {
			return Manifest{}, ErrConflict
		}
		return m, nil
	}
	if !fenceActive {
		return Manifest{}, ErrStaleFence
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Manifest{}, err
	}
	admittedValue, err := cachedb.New(tx).AdmitManifest(ctx, cachedb.AdmitManifestParams{ManifestID: dbUUID(id), LeaseID: dbUUID(in.Lease.LeaseID), CacheKey: in.Lease.CacheKey, OwnerID: in.Lease.OwnerID, FencingEpoch: in.Lease.FencingEpoch, NamespaceKey: in.Lease.Namespace.Key(), NamespaceEpoch: in.Lease.NamespaceEpoch, PartitionKind: string(in.Key.PartitionKind), TargetID: in.Key.TargetID, ProjectID: in.Key.ProjectID, Environment: in.Key.Environment, CandidateID: nullableString(in.Key.CandidateID), PartitionFormatVersion: in.Key.PartitionFormatVersion, DependencyDigest: in.Key.DependencyDigest, PolicyFingerprint: in.Key.PolicyFingerprint, CanonicalQueryDigest: in.Key.CanonicalQueryDigest, KeyFormatVersion: in.Key.KeyFormatVersion, StorageSecurityDomain: in.StorageSecurityDomain, ObjectDigest: in.ObjectDigest, ObjectKey: in.ObjectKey, ByteSize: in.ByteSize, Metadata: metadata, OriginSnapshotSealID: dbUUID(originSealID), ExpiresAt: dbTime(expiresAt)})
	if err != nil {
		if strings.Contains(err.Error(), "stale fill fence") {
			return Manifest{}, ErrStaleFence
		}
		if strings.Contains(err.Error(), "manifest conflict") {
			return Manifest{}, ErrConflict
		}
		return Manifest{}, err
	}
	if !admittedValue.Valid {
		return Manifest{}, ErrConflict
	}
	admittedID := admittedValue.Bytes
	m, found, err := lookupTx(ctx, tx, in.Key)
	if err != nil {
		return Manifest{}, err
	}
	if !found {
		return Manifest{}, ErrConflict
	}
	if m.ManifestID != admittedID {
		return Manifest{}, ErrConflict
	}
	if !publishManifestEqual(m, in, metadata, expiresAt) {
		return Manifest{}, ErrConflict
	}
	return m, nil
}

func publishManifestEqual(m Manifest, in PublishInput, metadata []byte, expiresAt *time.Time) bool {
	storedMetadata, err := canonicalMetadata(m.Metadata)
	if err != nil {
		return false
	}
	return m.Key == in.Key && m.StorageSecurityDomain == in.StorageSecurityDomain &&
		m.ObjectDigest == in.ObjectDigest && m.ObjectKey == in.ObjectKey &&
		m.ByteSize == in.ByteSize && bytes.Equal(storedMetadata, metadata) && sameExpiry(m.ExpiresAt, expiresAt)
}

func (r *Repository) AddRetentionRoot(ctx context.Context, rootID uuid.UUID, manifestID uuid.UUID, reason string) error {
	if r == nil || r.db == nil {
		return ErrInvalid
	}
	if rootID == uuid.Nil || manifestID == uuid.Nil || !literal(reason, 255) {
		return ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	admittedValue, err := cachedb.New(tx).GetAdmittedManifestForUpdate(ctx, dbUUID(manifestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	admitted := admittedValue != nil && *admittedValue
	if !admitted {
		return ErrNotFound
	}
	rootRow, rootErr := cachedb.New(tx).GetRetentionRootForUpdate(ctx, dbUUID(rootID))
	if rootErr == nil {
		if !rootRow.ManifestID.Valid {
			return ErrConflict
		}
		existingManifestID, existingState, existingReason := rootRow.ManifestID.Bytes, rootRow.State, rootRow.Reason
		if existingManifestID == manifestID && existingState == "live" && existingReason == reason {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			committed = true
			return nil
		}
		return ErrConflict
	}
	if !errors.Is(rootErr, pgx.ErrNoRows) {
		return rootErr
	}
	inserted, err := cachedb.New(tx).AddRetentionRoot(ctx, cachedb.AddRetentionRootParams{RootID: dbUUID(rootID), ManifestID: dbUUID(manifestID), Reason: reason})
	if err != nil {
		return err
	}
	if !inserted {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
func (r *Repository) RetireRetentionRoot(ctx context.Context, rootID uuid.UUID, evidence json.RawMessage) error {
	if r == nil || r.db == nil || rootID == uuid.Nil {
		return ErrInvalid
	}
	canonicalEvidence, err := requiredEvidence(evidence)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	rootRow, err := cachedb.New(tx).GetRetentionRootRetireForUpdate(ctx, dbUUID(rootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	state, previous := rootRow.State, rootRow.RetireEvidence
	if state == "retiring" {
		if sameLifecycleEvidence(previous, canonicalEvidence) {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			committed = true
			return nil
		}
		return ErrConflict
	}
	if state != "live" {
		return ErrConflict
	}
	updated, err := cachedb.New(tx).RetireRetentionRoot(ctx, cachedb.RetireRetentionRootParams{RootID: dbUUID(rootID), Evidence: canonicalEvidence})
	if err != nil {
		return err
	}
	if !updated {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// ExpireRetentionRoot records the terminal lifecycle evidence for a retired
// root. A live root must be retired explicitly before it can expire.
func (r *Repository) ExpireRetentionRoot(ctx context.Context, rootID uuid.UUID, evidence json.RawMessage) error {
	if r == nil || r.db == nil || rootID == uuid.Nil {
		return ErrInvalid
	}
	canonicalEvidence, err := requiredEvidence(evidence)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	rootRow, err := cachedb.New(tx).GetRetentionRootExpireForUpdate(ctx, dbUUID(rootID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	state, previous := rootRow.State, rootRow.ExpireEvidence
	if state == "expired" {
		if sameLifecycleEvidence(previous, canonicalEvidence) {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			committed = true
			return nil
		}
		return ErrConflict
	}
	if state != "retiring" {
		return ErrConflict
	}
	updated, err := cachedb.New(tx).ExpireRetentionRoot(ctx, cachedb.ExpireRetentionRootParams{RootID: dbUUID(rootID), Evidence: canonicalEvidence})
	if err != nil {
		return err
	}
	if !updated {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
func (r *Repository) ExpireManifest(ctx context.Context, manifestID uuid.UUID, evidence json.RawMessage) error {
	if r == nil || r.db == nil || manifestID == uuid.Nil {
		return ErrInvalid
	}
	canonicalEvidence, err := requiredEvidence(evidence)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	manifestRow, err := cachedb.New(tx).GetManifestExpireForUpdate(ctx, dbUUID(manifestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	} else if err != nil {
		return err
	}
	state, previous := manifestRow.State, manifestRow.ExpireEvidence
	if state == StateExpired {
		if sameLifecycleEvidence(previous, canonicalEvidence) {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			committed = true
			return nil
		}
		return ErrConflict
	}
	if state != StateRetiring {
		return ErrConflict
	}
	rootsLive, err := cachedb.New(tx).ManifestHasLiveRoots(ctx, dbUUID(manifestID))
	if err != nil {
		return err
	}
	if rootsLive {
		return ErrConflict
	}
	updated, err := cachedb.New(tx).ExpireManifest(ctx, cachedb.ExpireManifestParams{ManifestID: dbUUID(manifestID), Evidence: canonicalEvidence})
	if err != nil {
		return err
	}
	if !updated {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// RetireManifest records durable lifecycle evidence for one exact manifest.
// It is used by L3 object reconciliation so a missing result object cannot
// invalidate unrelated manifests that share a dependency digest.
func (r *Repository) RetireManifest(ctx context.Context, manifestID uuid.UUID, evidence json.RawMessage) error {
	if r == nil || r.db == nil || manifestID == uuid.Nil {
		return ErrInvalid
	}
	canonicalEvidence, err := requiredEvidence(evidence)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	manifestRow, err := cachedb.New(tx).GetManifestRetireForUpdate(ctx, dbUUID(manifestID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	state, previous := manifestRow.State, manifestRow.RetireEvidence
	if state == StateRetiring {
		if !sameLifecycleEvidence(previous, canonicalEvidence) {
			return ErrConflict
		}
	} else if state != StateAdmitted {
		return ErrConflict
	} else {
		updated, err := cachedb.New(tx).RetireManifest(ctx, cachedb.RetireManifestParams{ManifestID: dbUUID(manifestID), Evidence: canonicalEvidence})
		if err != nil {
			if strings.Contains(err.Error(), "retirement conflict") {
				return ErrConflict
			}
			return err
		}
		if !updated {
			return ErrConflict
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func lookupTx(ctx context.Context, tx Tx, key ManifestKey) (Manifest, bool, error) {
	row, err := cachedb.New(tx).GetManifest(ctx, cachedb.GetManifestParams{PartitionKind: string(key.PartitionKind), TargetID: key.TargetID, ProjectID: key.ProjectID, Environment: key.Environment, CandidateID: nullableString(key.CandidateID), PartitionFormatVersion: key.PartitionFormatVersion, DependencyDigest: key.DependencyDigest, PolicyFingerprint: key.PolicyFingerprint, CanonicalQueryDigest: key.CanonicalQueryDigest, KeyFormatVersion: key.KeyFormatVersion})
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	m, err := manifestFromRow(row)
	return m, err == nil, err
}
