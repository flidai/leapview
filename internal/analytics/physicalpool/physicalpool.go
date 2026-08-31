// Package physicalpool owns the control-plane contract for shared immutable
// DuckLake physical storage. It deliberately contains no table/file manifest
// and no credential values: the sealed DuckLake catalog remains authoritative
// for physical membership and credential references are target-owned handles.
package physicalpool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// AdmissionContract is the verified restart contract for one pool tuple.
// Implementations reconstruct every field from the PostgreSQL authority and
// run domain validators before returning it. The contract is target-native;
// legacy adapters may retain their own compatibility-shaped row type.
type AdmissionContract struct {
	Pool      PhysicalPool
	Admission PoolAdmission
	Evidence  Evidence
}

// PoolAdmissionRepository is the narrow control-plane authority consumed by
// runtime composition. Implementations must treat pool identity and evidence
// as immutable; writes are idempotent only for byte-for-byte equivalent
// canonical identities/evidence.
type PoolAdmissionRepository interface {
	CreateAndAdmit(context.Context, PhysicalPool, Evidence) (PhysicalPool, PoolAdmission, error)
	CreateAndAdmitWithOwnership(context.Context, PhysicalPool, Evidence, string, NamespaceOwnership) (PhysicalPool, PoolAdmission, error)
	Admit(context.Context, PhysicalPool, Evidence) (PoolAdmission, error)
	LoadAdmissionContract(context.Context, PoolID, Compatibility) (AdmissionContract, error)
	LoadAdmissionContractByCompatibilityDigest(context.Context, PoolID, string) (AdmissionContract, error)
	LoadAdmissionByEvidence(context.Context, PoolID, string) (AdmissionContract, error)
}

var (
	ErrInvalidPool           = errors.New("physical pool is invalid")
	ErrInvalidCompatibility  = errors.New("physical pool compatibility is invalid")
	ErrCompatibilityMismatch = errors.New("physical pool compatibility mismatch")
	ErrEvidenceInvalid       = errors.New("physical pool admission evidence is invalid")
	ErrPoolNotAdmitted       = errors.New("physical pool is not admitted")
	ErrInvalidCatalog        = errors.New("catalog binding is invalid")
	ErrSealedBinding         = errors.New("catalog binding is sealed")
	ErrPoolMismatch          = errors.New("physical pool mismatch")
)

// DiagnosticCode is a stable, non-secret reason suitable for control-plane
// audit and API responses. Values are intentionally independent of user data.
type DiagnosticCode string

const (
	DiagnosticMissingField     DiagnosticCode = "missing_field"
	DiagnosticInvalidField     DiagnosticCode = "invalid_field"
	DiagnosticTupleMismatch    DiagnosticCode = "compatibility_tuple_mismatch"
	DiagnosticEvidenceMismatch DiagnosticCode = "evidence_digest_mismatch"
	DiagnosticFailedCheck      DiagnosticCode = "evidence_check_failed"
	DiagnosticPoolMismatch     DiagnosticCode = "physical_pool_mismatch"
	DiagnosticBaseMismatch     DiagnosticCode = "base_catalog_mismatch"
	DiagnosticSealedMutation   DiagnosticCode = "sealed_binding_mutation"
)

// Diagnostic identifies one fail-closed validation reason. Field is a stable
// schema field name; no values are included so diagnostics cannot leak paths,
// credentials, or object keys.
type Diagnostic struct {
	Code  DiagnosticCode `json:"code"`
	Field string         `json:"field,omitempty"`
}

// DiagnosticsError is returned for invalid input and admission failures. It
// exposes only stable codes and fields and is safe to persist as evidence.
type DiagnosticsError struct {
	Cause       error
	Diagnostics []Diagnostic
}

func (e *DiagnosticsError) Error() string {
	if e == nil {
		return "physical pool validation failed"
	}
	parts := make([]string, 0, len(e.Diagnostics))
	for _, diagnostic := range e.Diagnostics {
		if diagnostic.Field == "" {
			parts = append(parts, string(diagnostic.Code))
		} else {
			parts = append(parts, string(diagnostic.Code)+"."+diagnostic.Field)
		}
	}
	if len(parts) == 0 {
		return "physical pool validation failed"
	}
	return "physical pool validation failed: " + strings.Join(parts, ",")
}

func (e *DiagnosticsError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func diagnostics(cause error, items ...Diagnostic) error {
	return &DiagnosticsError{Cause: cause, Diagnostics: append([]Diagnostic(nil), items...)}
}

// DiagnosticsOf returns a copy of fail-closed diagnostics carried by err.
func DiagnosticsOf(err error) []Diagnostic {
	var diagnosticErr *DiagnosticsError
	if !errors.As(err, &diagnosticErr) || diagnosticErr == nil {
		return nil
	}
	return append([]Diagnostic(nil), diagnosticErr.Diagnostics...)
}

// PoolID is the SHA-256 identity of a canonical PoolIdentity.
type PoolID string

func (id PoolID) String() string { return string(id) }

// Compatibility is the immutable tuple admitted for every writer and reader
// sharing one physical pool. Every member is result-affecting and participates
// in admission evidence; only its storage implementation and object-naming
// contract contribute to the stable pool identity.
type Compatibility struct {
	DuckDBRuntime         string `json:"duckdb_runtime"`
	DuckLakeExtension     string `json:"ducklake_extension"`
	CatalogFormat         string `json:"catalog_format"`
	StorageImplementation string `json:"storage_implementation"`
	ObjectNamingContract  string `json:"object_naming_contract"`
}

func (c Compatibility) Validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{"duckdb_runtime", c.DuckDBRuntime},
		{"ducklake_extension", c.DuckLakeExtension},
		{"catalog_format", c.CatalogFormat},
		{"storage_implementation", c.StorageImplementation},
		{"object_naming_contract", c.ObjectNamingContract},
	}
	for _, field := range fields {
		if err := validateCanonicalString(field.value); err != nil {
			return diagnostics(ErrInvalidCompatibility, Diagnostic{Code: DiagnosticInvalidField, Field: field.name})
		}
	}
	return nil
}

func (c Compatibility) Equal(other Compatibility) bool {
	return c == other
}

// StableEqual compares only the storage contract that names a deletable
// namespace. Runtime, extension, and catalog-format upgrades are admitted as
// new immutable evidence records and therefore intentionally do not affect a
// pool's stable identity.
func (c Compatibility) StableEqual(other Compatibility) bool {
	return c.StorageImplementation == other.StorageImplementation && c.ObjectNamingContract == other.ObjectNamingContract
}

func (c Compatibility) validateStable() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"storage_implementation", c.StorageImplementation},
		{"object_naming_contract", c.ObjectNamingContract},
	} {
		if err := validateCanonicalString(field.value); err != nil {
			return diagnostics(ErrInvalidCompatibility, Diagnostic{Code: DiagnosticInvalidField, Field: field.name})
		}
	}
	return nil
}

func (c Compatibility) CanonicalJSON() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal compatibility: %w", err)
	}
	return string(encoded), nil
}

func (c Compatibility) Digest() (string, error) {
	canonical, err := c.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return digestBytes([]byte(canonical)), nil
}

// RetentionPolicy is target-controlled policy metadata. It contains no roots,
// table membership, or object names.
type RetentionPolicy struct {
	OrphanGracePeriodSeconds int64 `json:"orphan_grace_period_seconds"`
	ReaderGracePeriodSeconds int64 `json:"reader_grace_period_seconds"`
	BuildGracePeriodSeconds  int64 `json:"build_grace_period_seconds"`
}

func (p RetentionPolicy) Validate() error {
	if p.OrphanGracePeriodSeconds < 0 || p.ReaderGracePeriodSeconds < 0 || p.BuildGracePeriodSeconds < 0 {
		return diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "retention_policy"})
	}
	return nil
}

// PoolIdentity contains the durable, non-secret identity of a physical pool.
// CredentialReference and EncryptionKeyRef are opaque target-owned references,
// never secret values.
type PoolIdentity struct {
	StorageLocation     string          `json:"storage_location"`
	StorageNamespace    string          `json:"storage_namespace"`
	Region              string          `json:"region"`
	Tenant              string          `json:"tenant"`
	EncryptionDomain    string          `json:"encryption_domain"`
	IsolationBoundary   string          `json:"isolation_boundary"`
	EncryptionKeyRef    string          `json:"encryption_key_ref,omitempty"`
	CredentialReference string          `json:"credential_reference,omitempty"`
	RetentionAuthority  string          `json:"retention_authority"`
	RetentionPolicy     RetentionPolicy `json:"retention_policy"`
	Compatibility       Compatibility   `json:"compatibility"`
}

func (i PoolIdentity) Validate() error {
	fields := []struct {
		name  string
		value string
		req   bool
	}{
		{"storage_location", i.StorageLocation, true},
		{"storage_namespace", i.StorageNamespace, true},
		{"region", i.Region, false},
		{"tenant", i.Tenant, false},
		{"encryption_domain", i.EncryptionDomain, true},
		{"isolation_boundary", i.IsolationBoundary, true},
		{"encryption_key_ref", i.EncryptionKeyRef, false},
		{"credential_reference", i.CredentialReference, false},
		{"retention_authority", i.RetentionAuthority, true},
	}
	for _, field := range fields {
		if field.value == "" && !field.req {
			continue
		}
		if err := validateCanonicalString(field.value); err != nil || (field.req && field.value == "") {
			return diagnostics(ErrInvalidPool, Diagnostic{Code: invalidFieldCode(field.value == ""), Field: field.name})
		}
	}
	if err := validateStorageLocation(i.StorageLocation); err != nil {
		return diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
	}
	if err := i.RetentionPolicy.Validate(); err != nil {
		return err
	}
	return i.Compatibility.validateStable()
}

func (i PoolIdentity) CanonicalJSON() (string, error) {
	if err := i.Validate(); err != nil {
		return "", err
	}
	canonicalLocation, err := canonicalStorageLocation(i.StorageLocation)
	if err != nil {
		return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
	}
	// Runtime, extension, and catalog format are deliberately omitted: those
	// exact tuple members are versioned through append-only admissions.
	encoded, err := json.Marshal(struct {
		StorageLocation       string          `json:"storage_location"`
		StorageNamespace      string          `json:"storage_namespace"`
		Region                string          `json:"region"`
		Tenant                string          `json:"tenant"`
		EncryptionDomain      string          `json:"encryption_domain"`
		IsolationBoundary     string          `json:"isolation_boundary"`
		EncryptionKeyRef      string          `json:"encryption_key_ref,omitempty"`
		CredentialReference   string          `json:"credential_reference,omitempty"`
		RetentionAuthority    string          `json:"retention_authority"`
		RetentionPolicy       RetentionPolicy `json:"retention_policy"`
		StorageImplementation string          `json:"storage_implementation"`
		ObjectNamingContract  string          `json:"object_naming_contract"`
	}{canonicalLocation, i.StorageNamespace, i.Region, i.Tenant, i.EncryptionDomain, i.IsolationBoundary, i.EncryptionKeyRef, i.CredentialReference, i.RetentionAuthority, i.RetentionPolicy, i.Compatibility.StorageImplementation, i.Compatibility.ObjectNamingContract})
	if err != nil {
		return "", fmt.Errorf("marshal pool identity: %w", err)
	}
	return string(encoded), nil
}

func (i PoolIdentity) Digest() (string, error) {
	canonical, err := i.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return digestBytes([]byte(canonical)), nil
}

// PhysicalPool is a validated stable pool identity. Exact runtime tuples are
// admitted separately so an upgrade can join this same physical namespace.
type PhysicalPool struct {
	ID       PoolID       `json:"id"`
	Identity PoolIdentity `json:"identity"`
	// Compatibility is the tuple used when the pool was first constructed. It is
	// retained for compatibility with callers, but is not part of the stable ID
	// and must not be treated as the current admission.
	Compatibility   Compatibility `json:"compatibility"`
	Admitted        bool          `json:"admitted"`
	AdmissionDigest string        `json:"admission_digest,omitempty"`
}

func NewPhysicalPool(identity PoolIdentity) (PhysicalPool, error) {
	canonicalLocation, err := canonicalStorageLocation(identity.StorageLocation)
	if err != nil {
		return PhysicalPool{}, diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
	}
	identity.StorageLocation = canonicalLocation
	digest, err := identity.Digest()
	if err != nil {
		return PhysicalPool{}, err
	}
	return PhysicalPool{ID: PoolID(digest), Identity: identity, Compatibility: identity.Compatibility}, nil
}

func (p PhysicalPool) Validate() error {
	if p.ID == "" {
		return diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticMissingField, Field: "id"})
	}
	expected, err := p.Identity.Digest()
	if err != nil {
		return err
	}
	if string(p.ID) != expected {
		return diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "id"})
	}
	compatibility := p.Compatibility
	if compatibility == (Compatibility{}) {
		compatibility = p.Identity.Compatibility
	}
	if !compatibility.StableEqual(p.Identity.Compatibility) {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	if p.Admitted {
		if err := validateDigest(p.AdmissionDigest); err != nil {
			return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "admission_digest"})
		}
	}
	return nil
}

func (p PhysicalPool) IdentityDigest() string { return string(p.ID) }

// DataPath resolves the canonical DuckLake DATA_PATH owned by this pool. The
// storage namespace is always joined beneath the storage location, for both
// local paths and object-store URLs; callers must compare their configured
// path to this exact result before attaching a catalog.
func (p PhysicalPool) DataPath() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	namespace := strings.Trim(strings.TrimSpace(p.Identity.StorageNamespace), "/")
	if namespace == "" || strings.ContainsAny(namespace, `\\?#`) {
		return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_namespace"})
	}
	cleanNamespace := path.Clean(namespace)
	if cleanNamespace == "." || cleanNamespace == ".." || strings.HasPrefix(cleanNamespace, "../") || strings.Contains(cleanNamespace, "/../") {
		return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_namespace"})
	}
	location := p.Identity.StorageLocation
	if !strings.Contains(location, "://") {
		absolute, err := filepath.Abs(filepath.Join(location, filepath.FromSlash(cleanNamespace)))
		if err != nil {
			return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
		}
		return filepath.Clean(absolute), nil
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Scheme == "file" {
		if parsed.Host != "" || parsed.Path == "" {
			return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
		}
		absolute, err := filepath.Abs(filepath.Join(parsed.Path, filepath.FromSlash(cleanNamespace)))
		if err != nil {
			return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
		}
		return filepath.Clean(absolute), nil
	}
	if parsed.Host == "" || (parsed.Scheme != "s3" && parsed.Scheme != "gs" && parsed.Scheme != "az" && parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", diagnostics(ErrInvalidPool, Diagnostic{Code: DiagnosticInvalidField, Field: "storage_location"})
	}
	parsed.Path = path.Join(parsed.Path, cleanNamespace)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func (p PhysicalPool) CanonicalIdentity() string {
	canonical, _ := p.Identity.CanonicalJSON()
	return canonical
}

// EvidenceCheck contains only a check identifier, outcome, and digest of any
// external observation. Raw logs, paths, credentials, and file membership do
// not belong in admission evidence.
type EvidenceCheck struct {
	ID                string `json:"id"`
	Passed            bool   `json:"passed"`
	ObservationDigest string `json:"observation_digest,omitempty"`
}

type EvidenceInput struct {
	Compatibility      Compatibility   `json:"compatibility"`
	ConformanceVersion string          `json:"conformance_version"`
	Checks             []EvidenceCheck `json:"checks"`
}

// Evidence is a content-addressed compatibility conformance result.
type Evidence struct {
	Compatibility      Compatibility   `json:"compatibility"`
	ConformanceVersion string          `json:"conformance_version"`
	Checks             []EvidenceCheck `json:"checks"`
	Digest             string          `json:"digest"`
}

// EvidenceArtifactSchemaVersion identifies the machine-readable admission
// artifact envelope. Keeping the schema version outside Evidence lets the
// conformance producer evolve its transport without changing the
// content-addressed digest.
const EvidenceArtifactSchemaVersion = 1

// EvidenceArtifact is the only file shape accepted by offline pool bootstrap.
// It contains the compatibility tuple, versioned named check digests, and the
// canonical evidence digest; raw observations and credentials never belong in
// the artifact.
type EvidenceArtifact struct {
	SchemaVersion int      `json:"schema_version"`
	Evidence      Evidence `json:"evidence"`
}

// MarshalEvidenceArtifact verifies the immutable evidence before writing it.
func MarshalEvidenceArtifact(evidence Evidence) ([]byte, error) {
	if err := evidence.Verify(); err != nil {
		return nil, err
	}
	return json.Marshal(EvidenceArtifact{SchemaVersion: EvidenceArtifactSchemaVersion, Evidence: evidence})
}

// UnmarshalEvidenceArtifact decodes and verifies an operator-supplied
// evidence artifact. The trailing-token check prevents concatenated or
// partially replaced files from being accepted.
func UnmarshalEvidenceArtifact(encoded []byte) (Evidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var artifact EvidenceArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return Evidence{}, fmt.Errorf("decode evidence artifact: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "artifact"})
		}
		return Evidence{}, fmt.Errorf("decode evidence artifact: %w", err)
	}
	if artifact.SchemaVersion != EvidenceArtifactSchemaVersion {
		return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "schema_version"})
	}
	if err := artifact.Evidence.Verify(); err != nil {
		return Evidence{}, err
	}
	return artifact.Evidence, nil
}

func NewEvidence(input EvidenceInput) (Evidence, error) {
	if err := input.Compatibility.Validate(); err != nil {
		return Evidence{}, err
	}
	if err := validateCanonicalString(input.ConformanceVersion); err != nil {
		return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "conformance_version"})
	}
	checks := append([]EvidenceCheck(nil), input.Checks...)
	if len(checks) == 0 {
		return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticMissingField, Field: "checks"})
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if err := validateCanonicalString(check.ID); err != nil {
			return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "checks.id"})
		}
		if _, ok := seen[check.ID]; ok {
			return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "checks.id"})
		}
		seen[check.ID] = struct{}{}
		if check.ObservationDigest != "" {
			if err := validateDigest(check.ObservationDigest); err != nil {
				return Evidence{}, diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "checks.observation_digest"})
			}
		}
	}
	canonical, err := json.Marshal(struct {
		Compatibility      Compatibility   `json:"compatibility"`
		ConformanceVersion string          `json:"conformance_version"`
		Checks             []EvidenceCheck `json:"checks"`
	}{input.Compatibility, input.ConformanceVersion, checks})
	if err != nil {
		return Evidence{}, fmt.Errorf("marshal admission evidence: %w", err)
	}
	return Evidence{Compatibility: input.Compatibility, ConformanceVersion: input.ConformanceVersion, Checks: checks, Digest: digestBytes(canonical)}, nil
}

func (e Evidence) Verify() error {
	computed, err := NewEvidence(EvidenceInput{Compatibility: e.Compatibility, ConformanceVersion: e.ConformanceVersion, Checks: e.Checks})
	if err != nil {
		return err
	}
	if computed.Digest != e.Digest {
		return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticEvidenceMismatch, Field: "digest"})
	}
	for _, check := range e.Checks {
		if !check.Passed {
			return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticFailedCheck, Field: "checks"})
		}
	}
	return nil
}

// PoolAdmission records the exact immutable evidence that admitted one tuple
// into one pool. It is control metadata, not a physical manifest.
type PoolAdmission struct {
	PoolID              PoolID        `json:"pool_id"`
	Compatibility       Compatibility `json:"compatibility"`
	CompatibilityDigest string        `json:"compatibility_digest"`
	EvidenceDigest      string        `json:"evidence_digest"`
	ConformanceVersion  string        `json:"conformance_version"`
}

func (a PoolAdmission) Validate() error {
	if err := validateDigest(string(a.PoolID)); err != nil {
		return diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticInvalidField, Field: "physical_pool_id"})
	}
	if err := a.Compatibility.Validate(); err != nil {
		return err
	}
	compatibilityDigest, err := a.Compatibility.Digest()
	if err != nil || compatibilityDigest != a.CompatibilityDigest {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility_digest"})
	}
	if err := validateDigest(a.EvidenceDigest); err != nil {
		return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "evidence_digest"})
	}
	if err := validateCanonicalString(a.ConformanceVersion); err != nil {
		return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticInvalidField, Field: "conformance_version"})
	}
	return nil
}

func (p PhysicalPool) Admit(evidence Evidence) (PoolAdmission, error) {
	if err := p.Validate(); err != nil {
		return PoolAdmission{}, err
	}
	if err := evidence.Compatibility.Validate(); err != nil {
		return PoolAdmission{}, err
	}
	if !p.Identity.Compatibility.StableEqual(evidence.Compatibility) {
		return PoolAdmission{}, diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	if err := evidence.Verify(); err != nil {
		return PoolAdmission{}, err
	}
	compatibilityDigest, err := evidence.Compatibility.Digest()
	if err != nil {
		return PoolAdmission{}, err
	}
	return PoolAdmission{PoolID: p.ID, Compatibility: evidence.Compatibility, CompatibilityDigest: compatibilityDigest, EvidenceDigest: evidence.Digest, ConformanceVersion: evidence.ConformanceVersion}, nil
}

// ApplyAdmission marks a validated pool with its exact admission evidence.
// The returned value is a copy; callers must persist it transactionally with
// the admission record. A different evidence digest cannot replace an
// existing admission implicitly.
func (p PhysicalPool) ApplyAdmission(admission PoolAdmission) (PhysicalPool, error) {
	if err := p.Validate(); err != nil {
		return p, err
	}
	if err := admission.Validate(); err != nil {
		return p, err
	}
	if admission.PoolID != p.ID {
		return p, diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "physical_pool_id"})
	}
	if !p.Identity.Compatibility.StableEqual(admission.Compatibility) {
		return p, diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	p.Admitted = true
	// This is diagnostic convenience only. Admissions are append-only;
	// VerifyAdmission accepts any matching immutable admission record.
	p.AdmissionDigest = admission.EvidenceDigest
	return p, nil
}

// VerifyAdmission is the fail-closed gate used immediately before attaching a
// catalog or starting a writer. It checks the stable boundary, exact tuple,
// immutable admission evidence, and the pool's admitted state.
func VerifyAdmission(pool PhysicalPool, tuple Compatibility, admission PoolAdmission, evidence Evidence) error {
	if !pool.Admitted {
		return diagnostics(ErrPoolNotAdmitted, Diagnostic{Code: DiagnosticMissingField, Field: "admission"})
	}
	if err := pool.Validate(); err != nil {
		return err
	}
	if err := tuple.Validate(); err != nil {
		return err
	}
	if err := admission.Validate(); err != nil {
		return err
	}
	if !pool.Identity.Compatibility.StableEqual(tuple) {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	if admission.PoolID != pool.ID {
		return diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "physical_pool_id"})
	}
	if !admission.Compatibility.Equal(tuple) {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	compatibilityDigest, err := tuple.Digest()
	if err != nil || compatibilityDigest != admission.CompatibilityDigest {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility_digest"})
	}
	if admission.ConformanceVersion != evidence.ConformanceVersion {
		return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticEvidenceMismatch, Field: "conformance_version"})
	}
	if evidence.Digest != admission.EvidenceDigest || !evidence.Compatibility.Equal(tuple) {
		return diagnostics(ErrEvidenceInvalid, Diagnostic{Code: DiagnosticEvidenceMismatch, Field: "evidence_digest"})
	}
	if err := evidence.Verify(); err != nil {
		return err
	}
	return nil
}

// CatalogBinding is control metadata for one immutable catalog artifact. The
// artifact's table/file membership is intentionally not represented here.
type CatalogBinding struct {
	PhysicalPoolID      PoolID        `json:"physical_pool_id"`
	CatalogDigest       string        `json:"catalog_digest"`
	ObjectKey           string        `json:"object_key"`
	SizeBytes           int64         `json:"size_bytes"`
	Compatibility       Compatibility `json:"compatibility"`
	CompatibilityDigest string        `json:"compatibility_digest"`
	BaseCatalogDigest   string        `json:"base_catalog_digest,omitempty"`
	BasePhysicalPoolID  PoolID        `json:"base_physical_pool_id,omitempty"`
	EvidenceDigest      string        `json:"evidence_digest"`
	Sealed              bool          `json:"sealed"`
}

type CatalogBindingInput struct {
	PhysicalPoolID      PoolID
	CatalogDigest       string
	ObjectKey           string
	SizeBytes           int64
	Compatibility       Compatibility
	CompatibilityDigest string
	BaseCatalogDigest   string
	BasePhysicalPoolID  PoolID
	EvidenceDigest      string
}

func NewCatalogBinding(input CatalogBindingInput) (CatalogBinding, error) {
	binding := CatalogBinding{PhysicalPoolID: input.PhysicalPoolID, CatalogDigest: input.CatalogDigest, ObjectKey: input.ObjectKey, SizeBytes: input.SizeBytes, Compatibility: input.Compatibility, CompatibilityDigest: input.CompatibilityDigest, BaseCatalogDigest: input.BaseCatalogDigest, BasePhysicalPoolID: input.BasePhysicalPoolID, EvidenceDigest: input.EvidenceDigest}
	if err := binding.Validate(); err != nil {
		return CatalogBinding{}, err
	}
	return binding, nil
}

func (b CatalogBinding) Validate() error {
	if err := validateDigest(string(b.PhysicalPoolID)); err != nil {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "physical_pool_id"})
	}
	if err := validateDigest(b.CatalogDigest); err != nil {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "catalog_digest"})
	}
	if err := validateCatalogKey(b.ObjectKey); err != nil {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "object_key"})
	}
	if b.SizeBytes < 0 {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "size_bytes"})
	}
	if err := b.Compatibility.Validate(); err != nil {
		return err
	}
	if err := validateDigest(b.CompatibilityDigest); err != nil {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "compatibility_digest"})
	}
	if digest, err := b.Compatibility.Digest(); err != nil || digest != b.CompatibilityDigest {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility_digest"})
	}
	if err := validateDigest(b.EvidenceDigest); err != nil {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "evidence_digest"})
	}
	if (b.BaseCatalogDigest == "") != (b.BasePhysicalPoolID == "") {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticBaseMismatch, Field: "base_catalog_digest"})
	}
	if b.BaseCatalogDigest != "" {
		if err := validateDigest(b.BaseCatalogDigest); err != nil {
			return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "base_catalog_digest"})
		}
		if err := validateDigest(string(b.BasePhysicalPoolID)); err != nil {
			return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "base_physical_pool_id"})
		}
		if b.BasePhysicalPoolID != b.PhysicalPoolID {
			return diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "base_physical_pool_id"})
		}
	}
	return nil
}

// Seal returns a sealed value after binding it to an admitted pool tuple.
// Sealed values have no mutating methods; persistence must reject updates to
// their pool, digest, key, size, or compatibility tuple.
func (b CatalogBinding) Seal(admission PoolAdmission) (CatalogBinding, error) {
	if b.Sealed {
		return b, diagnostics(ErrSealedBinding, Diagnostic{Code: DiagnosticSealedMutation, Field: "sealed"})
	}
	if b.SizeBytes == 0 {
		return CatalogBinding{}, diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "size_bytes"})
	}
	if err := b.Validate(); err != nil {
		return CatalogBinding{}, err
	}
	if admission.PoolID != b.PhysicalPoolID || admission.EvidenceDigest != b.EvidenceDigest {
		return CatalogBinding{}, diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "physical_pool_id"})
	}
	if b.CompatibilityDigest != admission.CompatibilityDigest {
		return CatalogBinding{}, diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	b.Sealed = true
	return b, nil
}

// RebindPool is intentionally a no-op for unsealed values and a fail-closed
// error for sealed artifacts. It returns a copy to make ownership explicit.
func (b CatalogBinding) RebindPool(poolID PoolID) (CatalogBinding, error) {
	if b.Sealed {
		return b, diagnostics(ErrSealedBinding, Diagnostic{Code: DiagnosticSealedMutation, Field: "physical_pool_id"})
	}
	if err := validateDigest(string(poolID)); err != nil {
		return b, diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticInvalidField, Field: "physical_pool_id"})
	}
	if b.BasePhysicalPoolID != "" && b.BasePhysicalPoolID != poolID {
		return b, diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "base_physical_pool_id"})
	}
	b.PhysicalPoolID = poolID
	return b, nil
}

// ValidateZeroCopyBaseChild verifies that a child can reuse a sealed base's
// physical objects without crossing retention or compatibility boundaries.
func ValidateZeroCopyBaseChild(base, child CatalogBinding) error {
	if err := base.Validate(); err != nil {
		return err
	}
	if !base.Sealed {
		return diagnostics(ErrInvalidCatalog, Diagnostic{Code: DiagnosticInvalidField, Field: "base.sealed"})
	}
	if err := child.Validate(); err != nil {
		return err
	}
	if child.PhysicalPoolID != base.PhysicalPoolID {
		return diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticPoolMismatch, Field: "physical_pool_id"})
	}
	if !child.Compatibility.Equal(base.Compatibility) {
		return diagnostics(ErrCompatibilityMismatch, Diagnostic{Code: DiagnosticTupleMismatch, Field: "compatibility"})
	}
	if child.BaseCatalogDigest != base.CatalogDigest || child.BasePhysicalPoolID != base.PhysicalPoolID {
		return diagnostics(ErrPoolMismatch, Diagnostic{Code: DiagnosticBaseMismatch, Field: "base_catalog_digest"})
	}
	return nil
}

func invalidFieldCode(missing bool) DiagnosticCode {
	if missing {
		return DiagnosticMissingField
	}
	return DiagnosticInvalidField
}

func validateCanonicalString(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("not canonical")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("control character")
		}
	}
	return nil
}

func validateDigest(value string) error {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return errors.New("invalid digest")
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err
}

func validateCatalogKey(value string) error {
	if err := validateCanonicalString(value); err != nil || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return errors.New("invalid object key")
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return errors.New("invalid object key")
	}
	return nil
}

func validateStorageLocation(value string) error {
	if value == "" || strings.ContainsAny(value, "?#") {
		return errors.New("invalid storage location")
	}
	// Plain local paths are valid, but never accept URL delimiters that could
	// hide an embedded credential or an unbound query/fragment.
	if !strings.Contains(value, "://") {
		if !filepath.IsAbs(value) {
			return errors.New("relative local storage location")
		}
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("invalid storage location")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "file":
		if parsed.Host != "" || parsed.Path == "" {
			return errors.New("invalid storage location")
		}
	case "s3", "gs", "az", "http", "https":
		if parsed.Host == "" {
			return errors.New("invalid storage location")
		}
	default:
		return errors.New("invalid storage location")
	}
	return nil
}

func canonicalStorageLocation(value string) (string, error) {
	if err := validateStorageLocation(value); err != nil {
		return "", err
	}
	if !strings.Contains(value, "://") {
		clean := path.Clean(value)
		if clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return "", errors.New("invalid storage location")
		}
		return clean, nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	if parsed.Path != "" {
		clean := path.Clean(parsed.Path)
		if strings.Contains(clean, "/../") || strings.HasPrefix(clean, "../") {
			return "", errors.New("invalid storage location")
		}
		if clean == "." || ((parsed.Scheme == "s3" || parsed.Scheme == "gs" || parsed.Scheme == "az") && clean == "/") {
			clean = ""
		}
		parsed.Path = clean
	}
	return parsed.String(), nil
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}
