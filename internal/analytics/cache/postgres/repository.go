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
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type DBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type Tx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

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
		Kind: kind, ProjectID: projectgraph.ResourceID(k.ProjectID), Environment: k.Environment, CandidateID: k.CandidateID,
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
	return ManifestKey{PartitionKind: kind, ProjectID: partition.ProjectID().String(), Environment: partition.Environment(), CandidateID: partition.CandidateID(), PartitionFormatVersion: int64(partition.Version()), DependencyDigest: key.DependencyDigest(), PolicyFingerprint: key.PolicyFingerprint(), CanonicalQueryDigest: key.CanonicalQueryDigest(), KeyFormatVersion: int64(key.Version())}, nil
}

type Manifest struct {
	ManifestID            uuid.UUID
	Key                   ManifestKey
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
	ProjectID     string
	Environment   string
	CandidateID   string
}

// NamespaceKey is the canonical, bounded representation persisted by the
// coordination tables. It intentionally contains no result bytes.
func (n Namespace) Key() string {
	wire := struct {
		Version   int           `json:"v"`
		Kind      PartitionKind `json:"k"`
		Project   string        `json:"p"`
		Env       string        `json:"e"`
		Candidate string        `json:"c,omitempty"`
	}{1, n.PartitionKind, n.ProjectID, n.Environment, n.CandidateID}
	b, _ := json.Marshal(wire)
	return "ns1_" + base64.RawURLEncoding.EncodeToString(b)
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
	if err := validatePartitionScope(ManifestKey{PartitionKind: n.PartitionKind, ProjectID: n.ProjectID, Environment: n.Environment, CandidateID: n.CandidateID, PartitionFormatVersion: resultidentity.PartitionVersion}); err != nil {
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
		key.ProjectID == namespace.ProjectID &&
		key.Environment == namespace.Environment &&
		key.CandidateID == namespace.CandidateID
}

func ensureNamespaceTx(ctx context.Context, tx Tx, n Namespace) (int64, error) {
	if err := validateNamespace(n); err != nil {
		return 0, err
	}
	key := n.Key()
	_, err := tx.Exec(ctx, `INSERT INTO cache.cache_namespace_epoch (namespace_key,partition_kind,project_id,environment,candidate_id,epoch) VALUES ($1,$2,$3,$4,$5,1) ON CONFLICT (namespace_key) DO NOTHING`, key, n.PartitionKind, n.ProjectID, n.Environment, candidateArg(n.CandidateID))
	if err != nil {
		return 0, err
	}
	var epoch int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key=$1 FOR UPDATE`, key).Scan(&epoch); err != nil {
		return 0, err
	}
	return epoch, nil
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
	var epoch int64
	err := r.db.QueryRow(ctx, `SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key=$1`, n.Key()).Scan(&epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	return epoch, err
}

// BumpNamespaceEpochTx advances the DB-owned epoch under a row lock. An
// optional expected value makes retries and compare-and-swap callers explicit.
func (r *Repository) BumpNamespaceEpochTx(ctx context.Context, tx Tx, n Namespace, expected int64) (int64, error) {
	if tx == nil {
		return 0, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current, err := ensureNamespaceTx(ctx, tx, n)
	if err != nil {
		return 0, err
	}
	if expected > 0 && current != expected {
		return 0, ErrEpoch
	}
	var next int64
	if err := tx.QueryRow(ctx, `UPDATE cache.cache_namespace_epoch SET epoch=epoch+1,updated_at=clock_timestamp() WHERE namespace_key=$1 RETURNING epoch`, n.Key()).Scan(&next); err != nil {
		return 0, err
	}
	return next, nil
}

func (r *Repository) BumpNamespaceEpoch(ctx context.Context, n Namespace, expected int64) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := r.db.(beginner)
	if !ok {
		return 0, fmt.Errorf("%w: repository requires transaction-capable DB", ErrInvalid)
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return 0, err
	}
	next, err := r.BumpNamespaceEpochTx(ctx, tx, n, expected)
	if err != nil {
		_ = tx.Rollback(ctx)
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return next, nil
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

func candidateArg(candidateID string) any {
	if candidateID == "" {
		return nil
	}
	return candidateID
}

func validatePublish(in PublishInput) error {
	if err := validateKey(in.Key); err != nil {
		return err
	}
	if platformdigest.ValidateSHA256Identity(in.StorageSecurityDomain) != nil || !literal(in.ObjectKey, 2048) {
		return fmt.Errorf("%w: invalid storage identity", ErrInvalid)
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
	manifest, err := scanManifest(r.db.QueryRow(ctx, manifestSelect+manifestKeyWhere+` AND state='admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp())`, in.PartitionKind, in.ProjectID, in.Environment, candidateArg(in.CandidateID), in.PartitionFormatVersion, in.DependencyDigest, in.PolicyFingerprint, in.CanonicalQueryDigest, in.KeyFormatVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func (r *Repository) ListByDependency(ctx context.Context, partitionKind PartitionKind, projectID, environment, candidateID, dependency string, limit int) ([]Manifest, error) {
	if platformdigest.ValidateSHA256Identity(dependency) != nil || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	if err := validatePartitionScope(ManifestKey{PartitionKind: partitionKind, ProjectID: projectID, Environment: environment, CandidateID: candidateID, PartitionFormatVersion: resultidentity.PartitionVersion}); err != nil {
		return nil, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rows, err := r.db.Query(ctx, `SELECT `+manifestColumns+` FROM cache.cache_manifest WHERE partition_kind=$1 AND project_id=$2 AND environment=$3 AND candidate_id IS NOT DISTINCT FROM $4 AND partition_format_version=$5 AND dependency_digest=$6 AND state='admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp()) ORDER BY created_at, manifest_id LIMIT $7`, partitionKind, projectID, environment, candidateArg(candidateID), resultidentity.PartitionVersion, dependency, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Manifest, 0, limit)
	for rows.Next() {
		m, e := scanManifest(rows)
		if e != nil {
			return nil, e
		}
		result = append(result, m)
	}
	return result, rows.Err()
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
	previous, err := ensureNamespaceTx(ctx, tx, in.Namespace)
	if err != nil {
		return InvalidationResult{}, err
	}
	// The namespace row lock serializes concurrent retries. Recheck the scoped
	// idempotency key after acquiring it so a lost commit acknowledgement or a
	// simultaneous retry cannot advance the epoch twice.
	var prior InvalidationResult
	var priorKind string
	var priorID, priorReason string
	var priorDigest *string
	var priorEvidence []byte
	rowErr := tx.QueryRow(ctx, `SELECT invalidation_id,event_id,dependency_kind,dependency_id,dependency_digest,namespace_epoch,retired_manifests,reason,evidence,created_at FROM cache.cache_invalidation WHERE namespace_key=$1 AND idempotency_key=$2`, in.Namespace.Key(), in.IdempotencyKey).Scan(&prior.InvalidationID, &prior.EventID, &priorKind, &priorID, &priorDigest, &prior.NamespaceEpoch, &prior.RetiredManifests, &priorReason, &priorEvidence, &prior.CreatedAt)
	if rowErr == nil {
		priorDigestValue := ""
		if priorDigest != nil {
			priorDigestValue = *priorDigest
		}
		if priorKind != string(in.Kind) || priorID != in.DependencyID || priorDigestValue != in.DependencyDigest || priorReason != reason || !sameLifecycleEvidence(priorEvidence, canonicalEvidence) {
			return InvalidationResult{}, ErrConflict
		}
		prior.Namespace = in.Namespace
		prior.CreatedAt = prior.CreatedAt.UTC()
		return prior, nil
	}
	if !errors.Is(rowErr, pgx.ErrNoRows) {
		return InvalidationResult{}, rowErr
	}
	if in.ExpectedEpoch > 0 && previous != in.ExpectedEpoch {
		return InvalidationResult{}, ErrEpoch
	}
	var epoch int64
	if err := tx.QueryRow(ctx, `UPDATE cache.cache_namespace_epoch SET epoch=epoch+1,updated_at=clock_timestamp() WHERE namespace_key=$1 RETURNING epoch`, in.Namespace.Key()).Scan(&epoch); err != nil {
		return InvalidationResult{}, err
	}
	where := `partition_kind=$1 AND project_id=$2 AND environment=$3 AND candidate_id IS NOT DISTINCT FROM $4 AND partition_format_version=$5 AND state='admitted'`
	args := []any{in.Namespace.PartitionKind, in.Namespace.ProjectID, in.Namespace.Environment, candidateArg(in.Namespace.CandidateID), resultidentity.PartitionVersion}
	if in.DependencyDigest != "" {
		where += ` AND dependency_digest=$6`
		args = append(args, in.DependencyDigest)
	}
	tag, err := tx.Exec(ctx, `UPDATE cache.cache_manifest SET state='retiring',retired_at=clock_timestamp(),retire_evidence=$`+fmt.Sprint(len(args)+1)+`::jsonb WHERE `+where, append(args, canonicalEvidence)...)
	if err != nil {
		return InvalidationResult{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return InvalidationResult{}, err
	}
	var eventID int64
	var created time.Time
	_, err = tx.Exec(ctx, `INSERT INTO cache.cache_invalidation (invalidation_id,namespace_key,dependency_kind,dependency_id,dependency_digest,namespace_epoch,retired_manifests,idempotency_key,reason,evidence) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)`, id, in.Namespace.Key(), in.Kind, in.DependencyID, nullableDigest(in.DependencyDigest), epoch, tag.RowsAffected(), in.IdempotencyKey, reason, canonicalEvidence)
	if err != nil {
		// A concurrent retry can win the idempotency key. Surface a stable
		// conflict so callers can re-read and replay through the normal path.
		if isUniqueViolation(err) {
			return InvalidationResult{}, ErrConflict
		}
		return InvalidationResult{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT event_id,created_at FROM cache.cache_invalidation WHERE invalidation_id=$1`, id).Scan(&eventID, &created); err != nil {
		return InvalidationResult{}, err
	}
	return InvalidationResult{InvalidationID: id, EventID: eventID, Namespace: in.Namespace, NamespaceEpoch: epoch, RetiredManifests: tag.RowsAffected(), CreatedAt: created.UTC()}, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullableDigest(value string) any {
	if value == "" {
		return nil
	}
	return value
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
	var revision int64
	var digest string
	var oldDigest string
	var updated time.Time
	rowErr := tx.QueryRow(ctx, `SELECT revision,revision_digest,updated_at FROM cache.cache_dependency_revision WHERE namespace_key=$1 AND dependency_kind=$2 AND dependency_id=$3 FOR UPDATE`, in.Namespace.Key(), in.Kind, in.DependencyID).Scan(&revision, &digest, &updated)
	if rowErr == nil {
		if in.ExpectedRevision > 0 && revision != in.ExpectedRevision {
			return DependencyRevision{}, ErrConflict
		}
		if digest == in.RevisionDigest {
			return DependencyRevision{Namespace: in.Namespace, Kind: in.Kind, DependencyID: in.DependencyID, Revision: revision, RevisionDigest: digest, UpdatedAt: updated.UTC()}, nil
		}
		oldDigest = digest
		if len(in.Evidence) == 0 {
			return DependencyRevision{}, fmt.Errorf("%w: revision change evidence is required", ErrInvalid)
		}
		revision++
		if err := tx.QueryRow(ctx, `UPDATE cache.cache_dependency_revision SET revision=$4,revision_digest=$5,updated_at=clock_timestamp() WHERE namespace_key=$1 AND dependency_kind=$2 AND dependency_id=$3 RETURNING updated_at`, in.Namespace.Key(), in.Kind, in.DependencyID, revision, in.RevisionDigest).Scan(&updated); err != nil {
			return DependencyRevision{}, err
		}
	} else if errors.Is(rowErr, pgx.ErrNoRows) {
		if in.ExpectedRevision > 0 {
			return DependencyRevision{}, ErrConflict
		}
		revision = 1
		if err := tx.QueryRow(ctx, `INSERT INTO cache.cache_dependency_revision (namespace_key,dependency_kind,dependency_id,revision,revision_digest) VALUES ($1,$2,$3,$4,$5) RETURNING updated_at`, in.Namespace.Key(), in.Kind, in.DependencyID, revision, in.RevisionDigest).Scan(&updated); err != nil {
			return DependencyRevision{}, err
		}
		digest = in.RevisionDigest
	} else {
		return DependencyRevision{}, rowErr
	}
	if revision > 1 {
		evidence := in.Evidence
		key := in.IdempotencyKey
		if key == "" {
			key = generatedRevisionIdempotencyKey(in.Namespace, in.Kind, in.DependencyID, in.RevisionDigest)
		}
		if _, err := r.InvalidateNamespaceTx(ctx, tx, NamespaceInvalidationInput{Namespace: in.Namespace, Kind: in.Kind, DependencyID: in.DependencyID, DependencyDigest: oldDigest, IdempotencyKey: key, Reason: "dependency revision changed"}, evidence); err != nil {
			return DependencyRevision{}, err
		}
		digest = in.RevisionDigest
	}
	if revision == 1 && digest == "" {
		digest = in.RevisionDigest
	}
	if updated.IsZero() {
		if err := tx.QueryRow(ctx, `SELECT updated_at FROM cache.cache_dependency_revision WHERE namespace_key=$1 AND dependency_kind=$2 AND dependency_id=$3`, in.Namespace.Key(), in.Kind, in.DependencyID).Scan(&updated); err != nil {
			return DependencyRevision{}, err
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
	rows, err := r.db.Query(ctx, `SELECT i.invalidation_id,i.event_id,i.namespace_key,e.partition_kind,e.project_id,e.environment,e.candidate_id,i.dependency_kind,i.dependency_id,i.dependency_digest,i.namespace_epoch,i.retired_manifests,i.reason,i.evidence,i.created_at FROM cache.cache_invalidation i JOIN cache.cache_namespace_epoch e ON e.namespace_key=i.namespace_key WHERE i.event_id>$1 ORDER BY i.event_id LIMIT $2`, opts.AfterEventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Invalidation, 0, limit)
	for rows.Next() {
		var item Invalidation
		var partition PartitionKind
		var namespaceKey string
		var candidate *string
		var digest *string
		var created time.Time
		if err := rows.Scan(&item.InvalidationID, &item.EventID, &namespaceKey, &partition, &item.Namespace.ProjectID, &item.Namespace.Environment, &candidate, &item.Kind, &item.DependencyID, &digest, &item.NamespaceEpoch, &item.RetiredManifests, &item.Reason, &item.Evidence, &created); err != nil {
			return nil, err
		}
		item.Namespace.PartitionKind = partition
		if candidate != nil {
			item.Namespace.CandidateID = *candidate
		}
		item.DependencyDigest = ""
		if digest != nil {
			item.DependencyDigest = *digest
		}
		item.CreatedAt = created.UTC()
		if namespaceKey != item.Namespace.Key() {
			return nil, ErrConflict
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Reconcile is a short alias used by listeners that treat NOTIFY as a wake
// hint and persist only the event cursor.
func (r *Repository) Reconcile(ctx context.Context, opts ReconcileOptions) ([]Invalidation, error) {
	return r.ReconcileInvalidations(ctx, opts)
}

// Prune removes only durable coordination rows that are outside the caller's
// retention boundary. Manifest rows remain lifecycle evidence and are never
// deleted by this method. A bounded batch keeps vacuum and lock impact small.
func (r *Repository) Prune(ctx context.Context, opts PruneOptions) (PruneStats, error) {
	if r == nil || r.db == nil || opts.Before.IsZero() {
		return PruneStats{}, ErrInvalid
	}
	limit := opts.Limit
	if limit == 0 {
		limit = maxPruneBatch
	}
	if limit < 1 || limit > maxPruneBatch {
		return PruneStats{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var stats PruneStats
	if err := r.db.QueryRow(ctx, `SELECT invalidations,expired_leases FROM cache.prune_coordination($1,$2)`, opts.Before.UTC(), limit).Scan(&stats.Invalidations, &stats.ExpiredLeases); err != nil {
		return PruneStats{}, err
	}
	return stats, nil
}

func (r *Repository) Stats(ctx context.Context) (CacheStats, error) {
	if r == nil || r.db == nil {
		return CacheStats{}, ErrInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var stats CacheStats
	if err := r.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE state='admitted'),count(*) FILTER (WHERE state='retiring'),count(*) FILTER (WHERE state='expired') FROM cache.cache_manifest`).Scan(&stats.AdmittedManifests, &stats.RetiringManifests, &stats.ExpiredManifests); err != nil {
		return CacheStats{}, err
	}
	if err := r.db.QueryRow(ctx, `SELECT count(*) FROM cache.cache_fill_lease WHERE expires_at>clock_timestamp()`).Scan(&stats.ActiveFills); err != nil {
		return CacheStats{}, err
	}
	if err := r.db.QueryRow(ctx, `SELECT count(*),coalesce(max(epoch),0) FROM cache.cache_invalidation i JOIN cache.cache_namespace_epoch e ON e.namespace_key=i.namespace_key`).Scan(&stats.InvalidationEvents, &stats.MaxEpoch); err != nil {
		return CacheStats{}, err
	}
	return stats, nil
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
	var out FillLease
	err = tx.QueryRow(ctx, `WITH claim_time AS (SELECT clock_timestamp() AS now)
INSERT INTO cache.cache_fill_lease (lease_id,cache_key,namespace_key,namespace_epoch,owner_id,fencing_epoch,expires_at,acquired_at)
SELECT $1,$2,$3,$4,$5,1,claim_time.now+($6::bigint*interval '1 microsecond'),claim_time.now
FROM claim_time
ON CONFLICT (cache_key) DO UPDATE SET lease_id=EXCLUDED.lease_id,manifest_id=NULL,namespace_key=EXCLUDED.namespace_key,namespace_epoch=EXCLUDED.namespace_epoch,owner_id=EXCLUDED.owner_id,fencing_epoch=cache.cache_fill_lease.fencing_epoch+1,expires_at=EXCLUDED.expires_at,acquired_at=EXCLUDED.acquired_at
WHERE cache.cache_fill_lease.expires_at <= (SELECT now FROM claim_time)
RETURNING lease_id,cache_key,namespace_epoch,owner_id,fencing_epoch,expires_at,acquired_at`, id, in.CacheKey, in.Namespace.Key(), namespaceEpoch, in.OwnerID, lease.Microseconds()).Scan(&out.LeaseID, &out.CacheKey, &out.NamespaceEpoch, &out.OwnerID, &out.FencingEpoch, &out.ExpiresAt, &out.AcquiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return FillLease{}, ErrBusy
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return FillLease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return FillLease{}, err
	}
	out.Namespace = in.Namespace
	out.ExpiresAt = out.ExpiresAt.UTC()
	out.AcquiredAt = out.AcquiredAt.UTC()
	return out, nil
}

func (r *Repository) RenewFill(ctx context.Context, lease FillLease, duration time.Duration) error {
	if err := validLease(lease); err != nil || duration <= 0 || duration > maxLeaseDuration {
		return ErrInvalid
	}
	tag, err := r.db.Exec(ctx, `UPDATE cache.cache_fill_lease SET expires_at=GREATEST(expires_at, clock_timestamp()+($5::bigint*interval '1 microsecond')) WHERE lease_id=$1 AND cache_key=$2 AND owner_id=$3 AND fencing_epoch=$4 AND expires_at>clock_timestamp()`, lease.LeaseID, lease.CacheKey, lease.OwnerID, lease.FencingEpoch, duration.Microseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	tag, err := r.db.Exec(ctx, `UPDATE cache.cache_fill_lease SET expires_at=clock_timestamp() WHERE lease_id=$1 AND cache_key=$2 AND owner_id=$3 AND fencing_epoch=$4`, lease.LeaseID, lease.CacheKey, lease.OwnerID, lease.FencingEpoch)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	expiresAt := normalizedExpiry(in.ExpiresAt)
	var fenceID uuid.UUID
	var epoch int64
	var namespaceEpoch int64
	var namespaceKey string
	var fenceManifest *uuid.UUID
	var fenceActive bool
	if err := tx.QueryRow(ctx, `SELECT lease_id,fencing_epoch,namespace_key,namespace_epoch,manifest_id,(expires_at > clock_timestamp()) FROM cache.cache_fill_lease WHERE cache_key=$1 AND owner_id=$2 AND fencing_epoch=$3 AND lease_id=$4 FOR UPDATE`, in.Lease.CacheKey, in.Lease.OwnerID, in.Lease.FencingEpoch, in.Lease.LeaseID).Scan(&fenceID, &epoch, &namespaceKey, &namespaceEpoch, &fenceManifest, &fenceActive); errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrStaleFence
	} else if err != nil {
		return Manifest{}, err
	}
	if in.Lease.Namespace.Key() != namespaceKey {
		return Manifest{}, ErrStaleFence
	}
	if in.Lease.NamespaceEpoch != namespaceEpoch {
		return Manifest{}, ErrStaleFence
	}
	var currentEpoch int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM cache.cache_namespace_epoch WHERE namespace_key=$1`, namespaceKey).Scan(&currentEpoch); errors.Is(err, pgx.ErrNoRows) || currentEpoch != namespaceEpoch {
		return Manifest{}, ErrStaleFence
	} else if err != nil {
		return Manifest{}, err
	}
	if fenceManifest != nil {
		m, scanErr := scanManifest(tx.QueryRow(ctx, manifestSelect+` WHERE manifest_id=$1`, *fenceManifest))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return Manifest{}, ErrConflict
		}
		if scanErr != nil {
			return Manifest{}, scanErr
		}
		if !publishManifestEqual(m, in, metadata, expiresAt) {
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
	if _, err := tx.Exec(ctx, `INSERT INTO cache.cache_manifest (manifest_id,partition_kind,project_id,environment,candidate_id,partition_format_version,dependency_digest,policy_fingerprint,canonical_query_digest,key_format_version,storage_security_domain,object_digest,object_key,byte_size,metadata,state,expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,'admitted',$16) ON CONFLICT (partition_kind,project_id,environment,candidate_id,partition_format_version,dependency_digest,policy_fingerprint,canonical_query_digest,key_format_version) DO NOTHING`, id, in.Key.PartitionKind, in.Key.ProjectID, in.Key.Environment, candidateArg(in.Key.CandidateID), in.Key.PartitionFormatVersion, in.Key.DependencyDigest, in.Key.PolicyFingerprint, in.Key.CanonicalQueryDigest, in.Key.KeyFormatVersion, in.StorageSecurityDomain, in.ObjectDigest, in.ObjectKey, in.ByteSize, metadata, expiresAt); err != nil {
		return Manifest{}, err
	}
	m, found, err := lookupTx(ctx, tx, in.Key)
	if err != nil {
		return Manifest{}, err
	}
	if !found {
		return Manifest{}, ErrConflict
	}
	if !publishManifestEqual(m, in, metadata, expiresAt) {
		return Manifest{}, ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE cache.cache_fill_lease SET manifest_id=$1, expires_at=clock_timestamp() WHERE lease_id=$2 AND cache_key=$3 AND owner_id=$4 AND fencing_epoch=$5`, m.ManifestID, in.Lease.LeaseID, in.Lease.CacheKey, in.Lease.OwnerID, epoch); err != nil {
		return Manifest{}, err
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
	var admitted bool
	if err := tx.QueryRow(ctx, `SELECT (state='admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp())) FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifestID).Scan(&admitted); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if !admitted {
		return ErrNotFound
	}
	var existingManifestID uuid.UUID
	var existingState, existingReason string
	rootErr := tx.QueryRow(ctx, `SELECT manifest_id,state,reason FROM cache.cache_retention_root WHERE root_id=$1 FOR UPDATE`, rootID).Scan(&existingManifestID, &existingState, &existingReason)
	if rootErr == nil {
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
	tag, err := tx.Exec(ctx, `INSERT INTO cache.cache_retention_root (root_id,manifest_id,state,reason) VALUES ($1,$2,'live',$3) ON CONFLICT (root_id) DO NOTHING`, rootID, manifestID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	var state string
	var previous []byte
	if err := tx.QueryRow(ctx, `SELECT state,retire_evidence FROM cache.cache_retention_root WHERE root_id=$1 FOR UPDATE`, rootID).Scan(&state, &previous); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
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
	if _, err := tx.Exec(ctx, `UPDATE cache.cache_retention_root SET state='retiring', retired_at=clock_timestamp(), retire_evidence=$2::jsonb WHERE root_id=$1`, rootID, canonicalEvidence); err != nil {
		return err
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
	var state string
	var previous []byte
	if err := tx.QueryRow(ctx, `SELECT state,expire_evidence FROM cache.cache_retention_root WHERE root_id=$1 FOR UPDATE`, rootID).Scan(&state, &previous); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
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
	if _, err := tx.Exec(ctx, `UPDATE cache.cache_retention_root SET state='expired', expired_at=clock_timestamp(), expire_evidence=$2::jsonb WHERE root_id=$1`, rootID, canonicalEvidence); err != nil {
		return err
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
	var state string
	var previous []byte
	if err := tx.QueryRow(ctx, `SELECT state,expire_evidence FROM cache.cache_manifest WHERE manifest_id=$1 FOR UPDATE`, manifestID).Scan(&state, &previous); errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	} else if err != nil {
		return err
	}
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
	var rootsLive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cache.cache_retention_root WHERE manifest_id=$1 AND state IN ('live','retiring'))`, manifestID).Scan(&rootsLive); err != nil {
		return err
	}
	if rootsLive {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE cache.cache_manifest SET state='expired', expired_at=clock_timestamp(), expire_evidence=$2::jsonb WHERE manifest_id=$1 AND state='retiring'`, manifestID, canonicalEvidence)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

const manifestColumns = `manifest_id,partition_kind,project_id,environment,candidate_id,partition_format_version,dependency_digest,policy_fingerprint,canonical_query_digest,key_format_version,storage_security_domain,object_digest,object_key,byte_size,metadata,state,created_at,expires_at,retired_at,expired_at,retire_evidence,expire_evidence`
const manifestKeyWhere = ` WHERE partition_kind=$1 AND project_id=$2 AND environment=$3 AND candidate_id IS NOT DISTINCT FROM $4 AND partition_format_version=$5 AND dependency_digest=$6 AND policy_fingerprint=$7 AND canonical_query_digest=$8 AND key_format_version=$9`
const manifestSelect = `SELECT ` + manifestColumns + ` FROM cache.cache_manifest`

type rowScanner interface{ Scan(...any) error }

func scanManifest(row rowScanner) (Manifest, error) {
	var m Manifest
	var kind string
	var candidate *string
	var metadata []byte
	var expires, retired, expired *time.Time
	var retireEvidence, expireEvidence []byte
	err := row.Scan(&m.ManifestID, &kind, &m.Key.ProjectID, &m.Key.Environment, &candidate, &m.Key.PartitionFormatVersion, &m.Key.DependencyDigest, &m.Key.PolicyFingerprint, &m.Key.CanonicalQueryDigest, &m.Key.KeyFormatVersion, &m.StorageSecurityDomain, &m.ObjectDigest, &m.ObjectKey, &m.ByteSize, &metadata, &m.State, &m.CreatedAt, &expires, &retired, &expired, &retireEvidence, &expireEvidence)
	if err != nil {
		return Manifest{}, err
	}
	m.Key.PartitionKind = PartitionKind(kind)
	if candidate != nil {
		m.Key.CandidateID = *candidate
	}
	m.Metadata = append(json.RawMessage(nil), metadata...)
	m.CreatedAt = m.CreatedAt.UTC()
	if expires != nil {
		value := expires.UTC()
		m.ExpiresAt = &value
	}
	if retired != nil {
		value := retired.UTC()
		m.RetiredAt = &value
	}
	if expired != nil {
		value := expired.UTC()
		m.ExpiredAt = &value
	}
	m.RetireEvidence = append(json.RawMessage(nil), retireEvidence...)
	m.ExpireEvidence = append(json.RawMessage(nil), expireEvidence...)
	return m, nil
}
func lookupTx(ctx context.Context, tx Tx, key ManifestKey) (Manifest, bool, error) {
	m, err := scanManifest(tx.QueryRow(ctx, manifestSelect+manifestKeyWhere+` AND state='admitted' AND (expires_at IS NULL OR expires_at > clock_timestamp())`, key.PartitionKind, key.ProjectID, key.Environment, candidateArg(key.CandidateID), key.PartitionFormatVersion, key.DependencyDigest, key.PolicyFingerprint, key.CanonicalQueryDigest, key.KeyFormatVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, false, nil
	}
	return m, err == nil, err
}
