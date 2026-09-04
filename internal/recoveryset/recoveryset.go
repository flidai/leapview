// Package recoveryset owns the durable, exact recovery frontier contract.
//
// A recovery set is an immutable description of the PostgreSQL recovery
// points, delivery generation, DuckLake seal/commit, and immutable objects
// that must be present before the target can serve.  Publication changes only
// the status under an explicit fencing epoch; the identity of a set can never
// be replaced in place.
package recoveryset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/google/uuid"
)

const SchemaVersion int32 = 1

type Status string

const (
	StatusPrepared   Status = "prepared"
	StatusPublished  Status = "published"
	StatusSuperseded Status = "superseded"
	StatusInvalid    Status = "invalid"
)

const (
	ObjectRootDuckLake        = "ducklake"
	ObjectRootServingArtifact = "serving-artifact"
)

type DatabaseRole string

type ValidationStatus string

const (
	ValidationRunning ValidationStatus = "running"
	ValidationPassed  ValidationStatus = "passed"
	ValidationFailed  ValidationStatus = "failed"
)

type ValidationAttempt struct {
	AttemptID     string           `json:"attempt_id"`
	SetID         string           `json:"set_id"`
	OwnerID       string           `json:"owner_id"`
	FenceEpoch    int64            `json:"fence_epoch"`
	AuditIdentity string           `json:"audit_identity"`
	Status        ValidationStatus `json:"status"`
	ResultDigest  string           `json:"result_digest,omitempty"`
	Error         string           `json:"error,omitempty"`
	StartedAt     time.Time        `json:"started_at"`
	CompletedAt   time.Time        `json:"completed_at,omitempty"`
}

type ValidationResult struct {
	AttemptID    string    `json:"attempt_id"`
	ResultDigest string    `json:"result_digest"`
	Evidence     []byte    `json:"evidence"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// ValidationEvidenceSchemaVersion is the version of the typed recovery
// validation envelope. The envelope deliberately contains only immutable
// identities needed to prove that a validation run examined this exact
// frontier; provider probes remain outside the startup read path.
const ValidationEvidenceSchemaVersion int32 = 1

// ValidationEvidenceObjectRoot is the strict JSON representation of one
// immutable object root. ProviderRecoveryFrontier is intentionally not
// omitempty: local roots may use an explicit empty frontier, while remote
// roots must carry a provider version/frontier value.
type ValidationEvidenceObjectRoot struct {
	Kind                     string `json:"kind"`
	URI                      string `json:"uri"`
	VersionID                string `json:"version_id"`
	Digest                   string `json:"digest"`
	ProviderRecoveryFrontier string `json:"provider_recovery_frontier"`
}

// ValidationEvidenceEnvelope is the typed, digest-bound v1 validation
// evidence document. It is intentionally separate from RecoverySet so the
// validation result can be recorded after frontier creation without changing
// the frontier identity.
type ValidationEvidenceEnvelope struct {
	SchemaVersion          int32                          `json:"schema_version"`
	SetID                  string                         `json:"set_id"`
	AttemptID              string                         `json:"attempt_id"`
	FrontierDigest         string                         `json:"frontier_digest"`
	ClusterPoints          []ClusterRecoveryPoint         `json:"cluster_points"`
	ObjectRoots            []ValidationEvidenceObjectRoot `json:"object_roots"`
	RelationNamespace      string                         `json:"relation_namespace"`
	RelationManifestDigest string                         `json:"relation_manifest_digest"`
	ClosureDigest          string                         `json:"closure_digest"`
}

// NewValidationEvidenceEnvelope constructs evidence from the exact frontier
// and validation attempt. It never consults mutable/latest rows.
func NewValidationEvidenceEnvelope(set RecoverySet, attemptID string) (ValidationEvidenceEnvelope, error) {
	normalized, err := set.Normalize()
	if err != nil {
		return ValidationEvidenceEnvelope{}, err
	}
	if !canonicalUUID(attemptID) {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: validation attempt ID must be a canonical UUID", ErrInvalid)
	}
	frontierDigest := normalized.FrontierDigest
	if frontierDigest == "" {
		frontierDigest, err = normalized.Digest()
		if err != nil {
			return ValidationEvidenceEnvelope{}, err
		}
	}
	envelope := ValidationEvidenceEnvelope{
		SchemaVersion: ValidationEvidenceSchemaVersion,
		SetID:         normalized.ID, AttemptID: attemptID, FrontierDigest: frontierDigest,
		ClusterPoints:          normalized.CanonicalPoints(),
		RelationNamespace:      normalized.Serving.RelationNamespace,
		RelationManifestDigest: normalized.Serving.RelationManifestDigest,
		ClosureDigest:          normalized.Serving.ClosureDigest,
	}
	for _, root := range normalized.ObjectRoots {
		envelope.ObjectRoots = append(envelope.ObjectRoots, ValidationEvidenceObjectRoot{
			Kind: root.Kind, URI: root.URI, VersionID: root.VersionID, Digest: root.Digest,
			ProviderRecoveryFrontier: root.ProviderRecoveryFrontier,
		})
	}
	envelope.normalize()
	if err := envelope.Validate(); err != nil {
		return ValidationEvidenceEnvelope{}, err
	}
	return envelope, nil
}

// NewValidationResult packages an envelope as a durable result. Callers do
// not need to hand-build JSON or independently compute ResultDigest; both are
// derived from the validated canonical envelope.
func NewValidationResult(envelope ValidationEvidenceEnvelope, recordedAt time.Time) (ValidationResult, error) {
	evidence, err := envelope.CanonicalJSON()
	if err != nil {
		return ValidationResult{}, err
	}
	resultDigest, err := envelope.Digest()
	if err != nil {
		return ValidationResult{}, err
	}
	result := ValidationResult{AttemptID: envelope.AttemptID, ResultDigest: resultDigest, Evidence: evidence, RecordedAt: recordedAt}
	if err := result.Validate(); err != nil {
		return ValidationResult{}, err
	}
	return result, nil
}

// ParseValidationEvidenceEnvelope strictly parses one bounded v1 envelope.
// Unknown fields, duplicate keys (including case variants), malformed nested
// values, and missing required values are rejected before canonicalization.
func ParseValidationEvidenceEnvelope(raw []byte) (ValidationEvidenceEnvelope, error) {
	if len(raw) < 2 || len(raw) > 65536 {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: validation evidence must be bounded JSON", ErrInvalid)
	}
	var envelope ValidationEvidenceEnvelope
	if err := strictjson.DecodeWithOptions(raw, &envelope, strictjson.Options{
		MaxBytes: 65536, MaxDepth: 32, DuplicateKeys: strictjson.CaseFoldedKeys,
		AllowUnknownFields: false,
	}); err != nil {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: decode validation evidence envelope: %v", ErrInvalid, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: decode validation evidence fields: %v", ErrInvalid, err)
	}
	if err := validationEvidenceExactKeys(fields,
		"schema_version", "set_id", "attempt_id", "frontier_digest", "cluster_points", "object_roots",
		"relation_namespace", "relation_manifest_digest", "closure_digest",
	); err != nil {
		return ValidationEvidenceEnvelope{}, err
	}
	var rawPoints []map[string]json.RawMessage
	if err := json.Unmarshal(fields["cluster_points"], &rawPoints); err != nil {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: decode validation cluster points: %v", ErrInvalid, err)
	}
	for _, point := range rawPoints {
		if err := validationEvidenceExactKeys(point, "database_role", "cluster_identity", "database_identity", "recovery_identity"); err != nil {
			return ValidationEvidenceEnvelope{}, err
		}
	}
	// ProviderRecoveryFrontier is allowed to be empty only for an explicitly
	// local root. Require the key itself in every root so omission cannot be
	// confused with an intentional local empty frontier.
	var rawRoots []map[string]json.RawMessage
	if err := json.Unmarshal(fields["object_roots"], &rawRoots); err != nil {
		return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: decode validation evidence roots: %v", ErrInvalid, err)
	}
	for _, root := range rawRoots {
		if err := validationEvidenceExactKeys(root, "kind", "uri", "version_id", "digest", "provider_recovery_frontier"); err != nil {
			return ValidationEvidenceEnvelope{}, err
		}
		if strings.TrimSpace(string(root["provider_recovery_frontier"])) == "null" {
			return ValidationEvidenceEnvelope{}, fmt.Errorf("%w: validation object root provider recovery frontier must be a string", ErrInvalid)
		}
	}
	envelope.normalize()
	if err := envelope.Validate(); err != nil {
		return ValidationEvidenceEnvelope{}, err
	}
	return envelope, nil
}

func validationEvidenceExactKeys(fields map[string]json.RawMessage, required ...string) error {
	if len(fields) != len(required) {
		return fmt.Errorf("%w: validation evidence fields must use the exact v1 schema", ErrInvalid)
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("%w: validation evidence field %q is required", ErrInvalid, key)
		}
	}
	return nil
}

func (e *ValidationEvidenceEnvelope) normalize() {
	if e == nil {
		return
	}
	e.ClusterPoints = (RecoverySet{ClusterPoints: e.ClusterPoints}).CanonicalPoints()
	e.ObjectRoots = append([]ValidationEvidenceObjectRoot(nil), e.ObjectRoots...)
	sort.Slice(e.ObjectRoots, func(i, j int) bool {
		a, b := e.ObjectRoots[i], e.ObjectRoots[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		return a.VersionID < b.VersionID
	})
}

// Validate checks the envelope's complete typed shape. Provider frontier
// values are required for remote object roots (s3/gs/az); local paths may
// explicitly use an empty provider frontier.
func (e ValidationEvidenceEnvelope) Validate() error {
	if e.SchemaVersion != ValidationEvidenceSchemaVersion {
		return fmt.Errorf("%w: unsupported validation evidence schema version %d", ErrInvalid, e.SchemaVersion)
	}
	if !canonicalUUID(e.SetID) || !canonicalUUID(e.AttemptID) {
		return fmt.Errorf("%w: validation evidence identities must be canonical UUIDs", ErrInvalid)
	}
	if err := digest(e.FrontierDigest, "validation evidence frontier digest"); err != nil {
		return err
	}
	if len(e.ClusterPoints) != 2 {
		return fmt.Errorf("%w: validation evidence requires both cluster recovery points", ErrInvalid)
	}
	seenRoles := map[DatabaseRole]bool{}
	for _, point := range e.ClusterPoints {
		if !point.DatabaseRole.valid() || seenRoles[point.DatabaseRole] {
			return fmt.Errorf("%w: validation evidence cluster roles are incomplete or duplicated", ErrInvalid)
		}
		seenRoles[point.DatabaseRole] = true
		if err := canonicalText(point.ClusterIdentity, "validation cluster identity", 255); err != nil {
			return err
		}
		if err := canonicalText(point.DatabaseIdentity, "validation database identity", 255); err != nil {
			return err
		}
		if err := canonicalText(point.RecoveryIdentity, "validation recovery identity", 512); err != nil {
			return err
		}
	}
	if !seenRoles[DatabaseControl] || !seenRoles[DatabaseDuckLake] {
		return fmt.Errorf("%w: validation evidence requires control and ducklake points", ErrInvalid)
	}
	control, ducklake := e.ClusterPoints[0], e.ClusterPoints[1]
	for _, point := range e.ClusterPoints {
		if point.DatabaseRole == DatabaseControl {
			control = point
		} else {
			ducklake = point
		}
	}
	if control.ClusterIdentity == ducklake.ClusterIdentity && control.RecoveryIdentity != ducklake.RecoveryIdentity {
		return fmt.Errorf("%w: validation evidence shared-cluster recovery identities must match", ErrInvalid)
	}
	if len(e.ObjectRoots) == 0 || len(e.ObjectRoots) > 128 {
		return fmt.Errorf("%w: validation evidence object roots must contain between one and 128 entries", ErrInvalid)
	}
	seenRoots := make(map[string]struct{}, len(e.ObjectRoots))
	for _, root := range e.ObjectRoots {
		converted := ObjectRoot{Kind: root.Kind, URI: root.URI, VersionID: root.VersionID, Digest: root.Digest, ProviderRecoveryFrontier: root.ProviderRecoveryFrontier}
		if err := converted.Validate(); err != nil {
			return err
		}
		if validationRemoteObjectRoot(root.URI) && root.ProviderRecoveryFrontier == "" {
			return fmt.Errorf("%w: remote validation object roots require provider recovery frontier", ErrInvalid)
		}
		key := root.Kind + "\x00" + root.URI + "\x00" + root.VersionID
		if _, ok := seenRoots[key]; ok {
			return fmt.Errorf("%w: duplicate validation object root", ErrInvalid)
		}
		seenRoots[key] = struct{}{}
	}
	if err := canonicalText(e.RelationNamespace, "validation relation namespace", 512); err != nil {
		return err
	}
	if err := digest(e.RelationManifestDigest, "validation relation manifest digest"); err != nil {
		return err
	}
	if err := digest(e.ClosureDigest, "validation closure digest"); err != nil {
		return err
	}
	return nil
}

func validationRemoteObjectRoot(uri string) bool {
	if !strings.Contains(uri, "://") {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "s3", "gs", "az":
		return true
	default:
		return false
	}
}

// ValidateFor proves that the envelope is the exact evidence for set and
// attempt. The comparison includes every cluster point/root identity and all
// serving relation digests; it performs no provider I/O.
func (e ValidationEvidenceEnvelope) ValidateFor(set RecoverySet, attemptID string) error {
	e.normalize()
	if err := e.Validate(); err != nil {
		return err
	}
	normalized, err := set.Normalize()
	if err != nil {
		return err
	}
	frontierDigest := normalized.FrontierDigest
	if frontierDigest == "" {
		frontierDigest, err = normalized.Digest()
		if err != nil {
			return err
		}
	}
	if e.SetID != normalized.ID || e.AttemptID != attemptID || e.FrontierDigest != frontierDigest {
		return fmt.Errorf("%w: validation evidence does not match selected recovery set", ErrInvalid)
	}
	if !equalPoints(e.ClusterPoints, normalized.ClusterPoints) || len(e.ObjectRoots) != len(normalized.ObjectRoots) {
		return fmt.Errorf("%w: validation evidence recovery points or roots differ from selected set", ErrInvalid)
	}
	for i, root := range e.ObjectRoots {
		want := normalized.ObjectRoots[i]
		if root.Kind != want.Kind || root.URI != want.URI || root.VersionID != want.VersionID || root.Digest != want.Digest || root.ProviderRecoveryFrontier != want.ProviderRecoveryFrontier {
			return fmt.Errorf("%w: validation evidence object root differs from selected set", ErrInvalid)
		}
	}
	if e.RelationNamespace != normalized.Serving.RelationNamespace || e.RelationManifestDigest != normalized.Serving.RelationManifestDigest || e.ClosureDigest != normalized.Serving.ClosureDigest {
		return fmt.Errorf("%w: validation evidence relation identity differs from selected set", ErrInvalid)
	}
	return nil
}

// CanonicalJSON returns the deterministic envelope encoding used for hashing
// and durable retries.
func (e ValidationEvidenceEnvelope) CanonicalJSON() ([]byte, error) {
	e.normalize()
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Digest computes the sha256 digest of canonical envelope bytes.
func (e ValidationEvidenceEnvelope) Digest() (string, error) {
	b, err := e.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

const (
	DatabaseControl  DatabaseRole = "control"
	DatabaseDuckLake DatabaseRole = "ducklake"
)

// ClusterRecoveryPoint is the provider-native recovery identity for one
// database. A recovery set must include exactly one control and one DuckLake
// point. If both databases share a cluster, their recovery identities must be
// byte-for-byte equal because one PITR point restores the whole cluster.
type ClusterRecoveryPoint struct {
	DatabaseRole     DatabaseRole `json:"database_role"`
	ClusterIdentity  string       `json:"cluster_identity"`
	DatabaseIdentity string       `json:"database_identity"`
	RecoveryIdentity string       `json:"recovery_identity"`
}

// DeliveryPointer identifies the exact serving generation selected by a
// recovery set. It deliberately contains no mutable "current" lookup.
type DeliveryPointer struct {
	TargetID       string `json:"target_id"`
	GenerationID   string `json:"generation_id"`
	PublicationID  string `json:"publication_id"`
	TargetRevision int64  `json:"target_revision"`
}

// SnapshotSeal is the exact immutable serving seal bound by the frontier.
type SnapshotSeal struct {
	SealID                    string `json:"seal_id"`
	PhysicalPoolID            string `json:"physical_pool_id"`
	TenantDomain              string `json:"tenant_domain"`
	Region                    string `json:"region"`
	EncryptionDomain          string `json:"encryption_domain"`
	ObjectNamespace           string `json:"object_namespace"`
	CatalogDatabase           string `json:"catalog_database"`
	CatalogID                 string `json:"catalog_id"`
	CatalogUUID               string `json:"catalog_uuid"`
	CatalogVersion            int64  `json:"catalog_version"`
	DuckLakeSnapshotID        int64  `json:"ducklake_snapshot_id"`
	RelationManifestDigest    string `json:"relation_manifest_digest"`
	RelationNamespace         string `json:"relation_namespace"`
	ClosureDigest             string `json:"closure_digest"`
	ObjectRoot                string `json:"object_root"`
	ObjectRootDigest          string `json:"object_root_digest"`
	ArtifactRoot              string `json:"artifact_root"`
	ArtifactRootDigest        string `json:"artifact_root_digest"`
	ServingArtifactID         string `json:"serving_artifact_id"`
	ServingArtifactDigest     string `json:"serving_artifact_digest"`
	CompiledGraphDigest       string `json:"compiled_graph_digest"`
	CompiledConfigDigest      string `json:"compiled_config_digest"`
	SecurityDomainFingerprint string `json:"security_domain_fingerprint"`
	RequestDigest             string `json:"request_digest"`
	PlanDigest                string `json:"plan_digest"`
	CompatibilityDigest       string `json:"compatibility_digest"`
	DuckDBVersion             string `json:"duckdb_version"`
	RuntimeVersion            string `json:"runtime_version"`
	DuckLakeExtensionVersion  string `json:"ducklake_extension_version"`
	DuckLakeSpecVersion       string `json:"ducklake_spec_version"`
	CatalogSchemaVersion      string `json:"catalog_schema_version"`
}

// CatalogCommit identifies the DuckLake catalog commit that produced the
// sealed snapshot. Commit identity is retained independently of the numeric
// snapshot ID because a number alone is not globally unique.
type CatalogCommit struct {
	CatalogID       string `json:"catalog_id"`
	CatalogDatabase string `json:"catalog_database"`
	CatalogUUID     string `json:"catalog_uuid"`
	CatalogVersion  int64  `json:"catalog_version"`
	SnapshotID      int64  `json:"snapshot_id"`
}

// ObjectRoot is an immutable provider object root required by the selected
// generation. VersionID is required for versioned stores; Digest proves the
// exact bytes or manifest selected by the recovery point.
type ObjectRoot struct {
	Kind                     string `json:"kind"`
	URI                      string `json:"uri"`
	VersionID                string `json:"version_id"`
	Digest                   string `json:"digest"`
	ProviderRecoveryFrontier string `json:"provider_recovery_frontier,omitempty"`
}

// CompatibilityTuple is the exact runtime/storage tuple qualified for the
// selected snapshot. It aliases the physical-pool authority so digest and
// validation semantics cannot drift between admission and recovery.
type CompatibilityTuple = physicalpool.Compatibility

// RecoverySet is the durable frontier identity. ID and CreatedAt are assigned
// by the caller/provider and are immutable after insertion. FenceEpoch is the
// publication fence held by the writer that may perform the status transition.
type RecoverySet struct {
	ID             string                 `json:"id"`
	SchemaVersion  int32                  `json:"schema_version"`
	ClusterPoints  []ClusterRecoveryPoint `json:"cluster_points"`
	Delivery       DeliveryPointer        `json:"delivery"`
	Serving        SnapshotSeal           `json:"serving"`
	Catalog        CatalogCommit          `json:"catalog"`
	ObjectRoots    []ObjectRoot           `json:"object_roots"`
	Compatibility  CompatibilityTuple     `json:"compatibility"`
	FrontierDigest string                 `json:"frontier_digest"`
	FenceEpoch     int64                  `json:"fence_epoch"`
	AuditIdentity  string                 `json:"audit_identity"`
	Status         Status                 `json:"status"`
	// PublishedValidationAttemptID binds a published frontier to one exact,
	// immutable validation attempt. It is empty while a set is prepared, and
	// publication must set it atomically after proving the attempt has passed
	// and carries matching evidence.
	PublishedValidationAttemptID string    `json:"published_validation_attempt_id,omitempty"`
	CreatedBy                    string    `json:"created_by"`
	CreatedAt                    time.Time `json:"created_at"`
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	ErrInvalid  = errors.New("recovery set is invalid")
	ErrConflict = errors.New("recovery set identity conflicts with durable record")
	ErrFenced   = errors.New("recovery set publication is stale or fenced")
	ErrNotFound = errors.New("recovery set not found")
)

func (s Status) valid() bool {
	return s == StatusPrepared || s == StatusPublished || s == StatusSuperseded || s == StatusInvalid
}

func (s ValidationStatus) valid() bool {
	return s == ValidationRunning || s == ValidationPassed || s == ValidationFailed
}

func (a ValidationAttempt) Validate() error {
	if !canonicalUUID(a.AttemptID) || !canonicalUUID(a.SetID) {
		return fmt.Errorf("%w: validation identities must be canonical UUIDs", ErrInvalid)
	}
	if err := canonicalText(a.OwnerID, "validation owner", 255); err != nil {
		return err
	}
	if a.FenceEpoch <= 0 {
		return fmt.Errorf("%w: validation fence epoch must be positive", ErrInvalid)
	}
	if err := canonicalText(a.AuditIdentity, "validation audit identity", 255); err != nil {
		return err
	}
	if !a.Status.valid() {
		return fmt.Errorf("%w: invalid validation status", ErrInvalid)
	}
	if a.Status == ValidationRunning && !a.CompletedAt.IsZero() {
		return fmt.Errorf("%w: running validation cannot be completed", ErrInvalid)
	}
	if a.Status == ValidationRunning && (a.ResultDigest != "" || a.Error != "") {
		return fmt.Errorf("%w: running validation cannot carry a result", ErrInvalid)
	}
	if a.Status != ValidationRunning && a.CompletedAt.IsZero() {
		return fmt.Errorf("%w: terminal validation requires completion time", ErrInvalid)
	}
	if a.ResultDigest != "" {
		if err := digest(a.ResultDigest, "validation result digest"); err != nil {
			return err
		}
	}
	if a.Status == ValidationPassed && a.ResultDigest == "" {
		return fmt.Errorf("%w: passed validation requires a result digest", ErrInvalid)
	}
	if a.Status == ValidationPassed && a.Error != "" {
		return fmt.Errorf("%w: passed validation cannot carry an error", ErrInvalid)
	}
	if a.Status == ValidationFailed && a.Error == "" {
		return fmt.Errorf("%w: failed validation requires an error", ErrInvalid)
	}
	if len(a.Error) > 16384 {
		return fmt.Errorf("%w: validation error is too large", ErrInvalid)
	}
	if a.StartedAt.IsZero() {
		return fmt.Errorf("%w: validation start time is required", ErrInvalid)
	}
	return nil
}

func (r ValidationResult) Validate() error {
	if !canonicalUUID(r.AttemptID) {
		return fmt.Errorf("%w: validation attempt ID must be canonical UUID", ErrInvalid)
	}
	if err := digest(r.ResultDigest, "validation result digest"); err != nil {
		return err
	}
	envelope, err := ParseValidationEvidenceEnvelope(r.Evidence)
	if err != nil {
		return err
	}
	if envelope.AttemptID != r.AttemptID {
		return fmt.Errorf("%w: validation evidence attempt ID does not match result", ErrInvalid)
	}
	canonical, err := envelope.CanonicalJSON()
	if err != nil {
		return err
	}
	computed := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(computed[:])
	if r.ResultDigest != wantDigest {
		return fmt.Errorf("%w: validation result digest does not match canonical evidence", ErrInvalid)
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("%w: validation result time is required", ErrInvalid)
	}
	return nil
}

// Normalize canonicalizes JSON evidence and PostgreSQL timestamp precision so
// retries can prove exact identity rather than accepting a conflicting result.
func (r ValidationResult) Normalize() (ValidationResult, error) {
	if !canonicalUUID(r.AttemptID) {
		return ValidationResult{}, fmt.Errorf("%w: validation attempt ID must be canonical UUID", ErrInvalid)
	}
	if err := digest(r.ResultDigest, "validation result digest"); err != nil {
		return ValidationResult{}, err
	}
	if r.RecordedAt.IsZero() {
		return ValidationResult{}, fmt.Errorf("%w: validation result time is required", ErrInvalid)
	}
	envelope, err := ParseValidationEvidenceEnvelope(r.Evidence)
	if err != nil {
		return ValidationResult{}, err
	}
	if envelope.AttemptID != r.AttemptID {
		return ValidationResult{}, fmt.Errorf("%w: validation evidence attempt ID does not match result", ErrInvalid)
	}
	canonical, err := envelope.CanonicalJSON()
	if err != nil {
		return ValidationResult{}, err
	}
	computed := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(computed[:])
	if r.ResultDigest != wantDigest {
		return ValidationResult{}, fmt.Errorf("%w: validation result digest does not match canonical evidence", ErrInvalid)
	}
	r.Evidence = canonical
	r.RecordedAt = r.RecordedAt.UTC().Truncate(time.Microsecond)
	return r, nil
}

func (r DatabaseRole) valid() bool { return r == DatabaseControl || r == DatabaseDuckLake }

func (s RecoverySet) Validate() error {
	if !canonicalUUID(s.ID) {
		return fmt.Errorf("%w: id must be a UUID", ErrInvalid)
	}
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalid, s.SchemaVersion)
	}
	if len(s.ClusterPoints) != 2 {
		return fmt.Errorf("%w: exactly two cluster recovery points are required", ErrInvalid)
	}
	seenRoles := map[DatabaseRole]bool{}
	for _, point := range s.ClusterPoints {
		if !point.DatabaseRole.valid() {
			return fmt.Errorf("%w: unsupported database role %q", ErrInvalid, point.DatabaseRole)
		}
		if seenRoles[point.DatabaseRole] {
			return fmt.Errorf("%w: duplicate database role %q", ErrInvalid, point.DatabaseRole)
		}
		seenRoles[point.DatabaseRole] = true
		if err := canonicalText(point.ClusterIdentity, "cluster identity", 255); err != nil {
			return err
		}
		if err := canonicalText(point.DatabaseIdentity, "database identity", 255); err != nil {
			return err
		}
		if err := canonicalText(point.RecoveryIdentity, "recovery identity", 512); err != nil {
			return err
		}
	}
	if !seenRoles[DatabaseControl] || !seenRoles[DatabaseDuckLake] {
		return fmt.Errorf("%w: control and ducklake recovery points are required", ErrInvalid)
	}
	control, ducklake := s.ClusterPoints[0], s.ClusterPoints[1]
	for _, point := range s.ClusterPoints {
		if point.DatabaseRole == DatabaseControl {
			control = point
		} else {
			ducklake = point
		}
	}
	if control.ClusterIdentity == ducklake.ClusterIdentity && control.RecoveryIdentity != ducklake.RecoveryIdentity {
		return fmt.Errorf("%w: shared-cluster recovery identities must match", ErrInvalid)
	}
	if err := s.Delivery.validate(); err != nil {
		return err
	}
	if err := s.Serving.validate(); err != nil {
		return err
	}
	if err := s.Catalog.validate(); err != nil {
		return err
	}
	if s.Catalog.CatalogID != s.Serving.CatalogID || s.Catalog.CatalogDatabase != s.Serving.CatalogDatabase || s.Catalog.CatalogUUID != s.Serving.CatalogUUID || s.Catalog.CatalogVersion != s.Serving.CatalogVersion || s.Catalog.SnapshotID != s.Serving.DuckLakeSnapshotID {
		return fmt.Errorf("%w: catalog commit identity does not match serving snapshot seal", ErrInvalid)
	}
	if err := s.Compatibility.Validate(); err != nil {
		return err
	}
	compatDigest, err := s.Compatibility.Digest()
	if err != nil || s.Serving.CompatibilityDigest != compatDigest {
		return fmt.Errorf("%w: compatibility digest does not match tuple", ErrInvalid)
	}
	if len(s.ObjectRoots) == 0 || len(s.ObjectRoots) > 128 {
		return fmt.Errorf("%w: required immutable object roots must contain between one and 128 entries", ErrInvalid)
	}
	seenRoots := make(map[string]struct{}, len(s.ObjectRoots))
	for _, root := range s.ObjectRoots {
		if err := root.Validate(); err != nil {
			return err
		}
		key := root.Kind + "\x00" + root.URI + "\x00" + root.VersionID
		if _, ok := seenRoots[key]; ok {
			return fmt.Errorf("%w: duplicate object root", ErrInvalid)
		}
		seenRoots[key] = struct{}{}
	}
	if err := validateCanonicalObjectRoots(s.Serving, s.ObjectRoots); err != nil {
		return err
	}
	if s.FenceEpoch <= 0 {
		return fmt.Errorf("%w: fence epoch must be positive", ErrInvalid)
	}
	if err := canonicalText(s.AuditIdentity, "audit identity", 255); err != nil {
		return err
	}
	if !s.Status.valid() {
		return fmt.Errorf("%w: unsupported status %q", ErrInvalid, s.Status)
	}
	if s.Status == StatusPrepared && s.PublishedValidationAttemptID != "" {
		return fmt.Errorf("%w: prepared recovery set cannot carry a published validation attempt", ErrInvalid)
	}
	if s.Status == StatusPublished || s.Status == StatusSuperseded {
		if !canonicalUUID(s.PublishedValidationAttemptID) {
			return fmt.Errorf("%w: published recovery set requires a canonical validation attempt id", ErrInvalid)
		}
	} else if s.PublishedValidationAttemptID != "" && !canonicalUUID(s.PublishedValidationAttemptID) {
		return fmt.Errorf("%w: published validation attempt id must be a canonical UUID", ErrInvalid)
	}
	if err := canonicalText(s.CreatedBy, "created by", 255); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() || s.CreatedAt.Location() == nil {
		return fmt.Errorf("%w: created at is required", ErrInvalid)
	}
	if s.FrontierDigest != "" {
		computed, err := s.Digest()
		if err != nil || computed != s.FrontierDigest {
			return fmt.Errorf("%w: frontier digest does not match immutable evidence", ErrInvalid)
		}
	}
	return nil
}

// validateCanonicalObjectRoots keeps the recovery frontier aligned with the
// roots consumed by delivery startup. A serving seal has exactly one DuckLake
// data root and one immutable serving-artifact root; allowing any other child
// roots would produce a set that validates at publication but is guaranteed to
// fail the subsequent readiness check.
func validateCanonicalObjectRoots(seal SnapshotSeal, roots []ObjectRoot) error {
	if len(roots) != 2 {
		return fmt.Errorf("%w: recovery set requires exactly one ducklake and one serving-artifact root", ErrInvalid)
	}
	seenDuckLake, seenArtifact := false, false
	for _, root := range roots {
		switch root.Kind {
		case ObjectRootDuckLake:
			if seenDuckLake || root.URI != seal.ObjectRoot || root.Digest != seal.ObjectRootDigest {
				return fmt.Errorf("%w: ducklake object root must match the serving seal", ErrInvalid)
			}
			seenDuckLake = true
		case ObjectRootServingArtifact:
			if seenArtifact || root.URI != seal.ArtifactRoot || root.Digest != seal.ArtifactRootDigest {
				return fmt.Errorf("%w: serving-artifact root must match the serving seal", ErrInvalid)
			}
			seenArtifact = true
		default:
			return fmt.Errorf("%w: unsupported recovery object root kind %q", ErrInvalid, root.Kind)
		}
	}
	if !seenDuckLake || !seenArtifact {
		return fmt.Errorf("%w: recovery set requires ducklake and serving-artifact roots", ErrInvalid)
	}
	return nil
}

func (d DeliveryPointer) validate() error {
	if err := canonicalText(d.TargetID, "target id", 255); err != nil {
		return err
	}
	if !canonicalUUID(d.GenerationID) {
		return fmt.Errorf("%w: generation id must be a canonical UUID", ErrInvalid)
	}
	if !canonicalUUID(d.PublicationID) {
		return fmt.Errorf("%w: publication id must be a canonical UUID", ErrInvalid)
	}
	if d.TargetRevision <= 0 {
		return fmt.Errorf("%w: target revision must be positive", ErrInvalid)
	}
	return nil
}

func (s SnapshotSeal) validate() error {
	if !canonicalUUID(s.SealID) {
		return fmt.Errorf("%w: seal id must be a canonical UUID", ErrInvalid)
	}
	for label, value := range map[string]string{"physical pool id": s.PhysicalPoolID, "tenant domain": s.TenantDomain, "region": s.Region, "encryption domain": s.EncryptionDomain, "object namespace": s.ObjectNamespace, "catalog database": s.CatalogDatabase, "catalog id": s.CatalogID, "catalog uuid": s.CatalogUUID, "object root": s.ObjectRoot, "artifact root": s.ArtifactRoot, "relation namespace": s.RelationNamespace, "serving artifact id": s.ServingArtifactID, "duckdb version": s.DuckDBVersion, "runtime version": s.RuntimeVersion, "ducklake extension version": s.DuckLakeExtensionVersion, "ducklake spec version": s.DuckLakeSpecVersion, "catalog schema version": s.CatalogSchemaVersion} {
		if err := canonicalText(value, label, 512); err != nil {
			return err
		}
	}
	if s.CatalogVersion <= 0 || s.DuckLakeSnapshotID <= 0 {
		return fmt.Errorf("%w: catalog and snapshot versions must be positive", ErrInvalid)
	}
	for label, value := range map[string]string{"relation manifest digest": s.RelationManifestDigest, "closure digest": s.ClosureDigest, "object root digest": s.ObjectRootDigest, "artifact root digest": s.ArtifactRootDigest, "serving artifact digest": s.ServingArtifactDigest, "request digest": s.RequestDigest, "plan digest": s.PlanDigest, "compatibility digest": s.CompatibilityDigest, "compiled graph digest": s.CompiledGraphDigest, "compiled config digest": s.CompiledConfigDigest, "security domain fingerprint": s.SecurityDomainFingerprint} {
		if err := digest(value, label); err != nil {
			return err
		}
	}
	return nil
}

func (c CatalogCommit) validate() error {
	if err := canonicalText(c.CatalogID, "catalog id", 255); err != nil {
		return err
	}
	if err := canonicalText(c.CatalogDatabase, "catalog database", 255); err != nil {
		return err
	}
	if err := canonicalText(c.CatalogUUID, "catalog uuid", 255); err != nil {
		return err
	}
	if c.CatalogVersion <= 0 || c.SnapshotID <= 0 {
		return fmt.Errorf("%w: catalog commit versions must be positive", ErrInvalid)
	}
	return nil
}

func (r ObjectRoot) Validate() error {
	if err := canonicalText(r.Kind, "object root kind", 128); err != nil {
		return err
	}
	if len(r.URI) == 0 || len(r.URI) > 2048 || strings.TrimSpace(r.URI) != r.URI || strings.ContainsAny(r.URI, "\r\n\t") {
		return fmt.Errorf("%w: object root URI must be bounded and canonical", ErrInvalid)
	}
	if strings.Contains(r.URI, "..") {
		for _, part := range strings.FieldsFunc(r.URI, func(r rune) bool { return r == '/' || r == '\\' }) {
			if part == ".." {
				return fmt.Errorf("%w: object root path traversal is not allowed", ErrInvalid)
			}
		}
	}
	if strings.Contains(r.URI, "://") {
		u, err := url.Parse(r.URI)
		if err != nil || u == nil {
			return fmt.Errorf("%w: object root URI must be a supported absolute location without credentials, query, or fragment", ErrInvalid)
		}
		scheme := strings.ToLower(u.Scheme)
		if (scheme != "s3" && scheme != "gs" && scheme != "az" && scheme != "file") || (u.Host == "" && scheme != "file") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%w: object root URI must be a supported absolute location without credentials, query, or fragment", ErrInvalid)
		}
	} else if !strings.HasPrefix(r.URI, "/") && !strings.HasPrefix(r.URI, "./") && !strings.HasPrefix(r.URI, "objects/") && !strings.HasPrefix(r.URI, "artifacts/") {
		return fmt.Errorf("%w: object root must be an absolute or supported relative path", ErrInvalid)
	}
	if err := canonicalText(r.VersionID, "object root version", 512); err != nil {
		return err
	}
	if err := digest(r.Digest, "object root digest"); err != nil {
		return err
	}
	if r.ProviderRecoveryFrontier != "" {
		if err := canonicalText(r.ProviderRecoveryFrontier, "object root provider recovery frontier", 512); err != nil {
			return err
		}
	}
	return nil
}

func canonicalText(value, label string, max int) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > max {
		return fmt.Errorf("%w: %s must be non-empty, canonical, and at most %d bytes", ErrInvalid, label, max)
	}
	return nil
}

func digest(value, label string) error {
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%w: %s must be a lowercase SHA-256 digest", ErrInvalid, label)
	}
	return nil
}

func canonicalUUID(value string) bool {
	u, err := uuid.Parse(value)
	return err == nil && u.String() == value
}

// Normalize returns a validated copy with child rows in their persistence
// order. Callers should normalize before insertion or equality comparison.
func (s RecoverySet) Normalize() (RecoverySet, error) {
	if err := s.Validate(); err != nil {
		return RecoverySet{}, err
	}
	// PostgreSQL timestamptz stores microsecond precision; canonicalize before
	// hashing/insertion so exact replay does not differ on sub-microsecond
	// caller clock values.
	s.CreatedAt = s.CreatedAt.UTC().Truncate(time.Microsecond)
	s.sortChildren()
	return s, nil
}

func (s *RecoverySet) sortChildren() {
	s.ClusterPoints = s.CanonicalPoints()
	s.ObjectRoots = append([]ObjectRoot(nil), s.ObjectRoots...)
	sort.Slice(s.ObjectRoots, func(i, j int) bool {
		a, b := s.ObjectRoots[i], s.ObjectRoots[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.URI != b.URI {
			return a.URI < b.URI
		}
		return a.VersionID < b.VersionID
	})
}

// CanonicalJSON and Digest provide a stable evidence identity for audit and
// idempotent replay. Child collections are sorted before encoding.
func (s RecoverySet) CanonicalJSON() ([]byte, error) {
	n, err := s.Normalize()
	if err != nil {
		return nil, err
	}
	return json.Marshal(n)
}

func (s RecoverySet) Digest() (string, error) {
	// Digest covers only the immutable frontier projection. Publication status,
	// fence, audit actor, and creation metadata remain separate record fields.
	n := s
	n.Status, n.FrontierDigest, n.PublishedValidationAttemptID = StatusPrepared, "", ""
	if err := n.Validate(); err != nil {
		return "", err
	}
	n.FenceEpoch, n.AuditIdentity, n.CreatedBy, n.CreatedAt = 0, "", "", time.Time{}
	n.sortChildren()
	b, err := json.Marshal(n)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CanonicalPoints returns points in stable role order for persistence and
// equality comparisons.
func (s RecoverySet) CanonicalPoints() []ClusterRecoveryPoint {
	out := append([]ClusterRecoveryPoint(nil), s.ClusterPoints...)
	sort.Slice(out, func(i, j int) bool { return out[i].DatabaseRole < out[j].DatabaseRole })
	return out
}

// IdentityEqual reports whether an insert replay matches the durable record.
// Only lifecycle status is mutable; fence, audit, and creation metadata must
// remain exact for the same set ID.
func (s RecoverySet) IdentityEqual(other RecoverySet) bool {
	a, errA := s.Normalize()
	b, errB := other.Normalize()
	if errA != nil || errB != nil {
		return false
	}
	// Publication status is mutable metadata; all identity, fence, audit and
	// creation fields remain part of an exact insert replay comparison.
	a.Status, b.Status = StatusPrepared, StatusPrepared
	// The exact validation attempt is attached atomically at publication and
	// is therefore lifecycle metadata rather than part of create identity.
	a.PublishedValidationAttemptID, b.PublishedValidationAttemptID = "", ""
	if a.FrontierDigest == "" {
		a.FrontierDigest, _ = a.Digest()
	}
	if b.FrontierDigest == "" {
		b.FrontierDigest, _ = b.Digest()
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// FrontierEqual compares only the immutable recovery frontier projection.
func (s RecoverySet) FrontierEqual(other RecoverySet) bool {
	a, errA := s.Digest()
	b, errB := other.Digest()
	return errA == nil && errB == nil && a == b
}

func equalPoints(a, b []ClusterRecoveryPoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalRoots(a, b []ObjectRoot) bool {
	a = append([]ObjectRoot(nil), a...)
	b = append([]ObjectRoot(nil), b...)
	sort.Slice(a, func(i, j int) bool {
		if a[i].Kind != a[j].Kind {
			return a[i].Kind < a[j].Kind
		}
		if a[i].URI != a[j].URI {
			return a[i].URI < a[j].URI
		}
		return a[i].VersionID < a[j].VersionID
	})
	sort.Slice(b, func(i, j int) bool {
		if b[i].Kind != b[j].Kind {
			return b[i].Kind < b[j].Kind
		}
		if b[i].URI != b[j].URI {
			return b[i].URI < b[j].URI
		}
		return b[i].VersionID < b[j].VersionID
	})
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
