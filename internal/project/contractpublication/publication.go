// Package contractpublication owns the pure domain value for an immutable
// leapview.contract/v1 publication.  It is deliberately below persistence,
// lifecycle, audit, and deployment policy: a PostgreSQL adapter may use these
// values as its transaction input, but this package never opens a database or
// allocates a control-plane identity.
package contractpublication

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/project/contractprojection"
	"github.com/flidai/leapview/internal/project/contractversion"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	ocidigest "github.com/opencontainers/go-digest"
)

const (
	ValidationEvidenceVersion = 1
	PolicyEvidenceVersion     = 1
	MaxCanonicalBytes         = 16 << 20
	MaxValidationBytes        = 64 << 10
	MaxPolicyEvidenceBytes    = 16 << 10
)

var (
	ErrInvalidPublication  = errors.New("invalid contract publication")
	ErrPublicationConflict = errors.New("contract publication conflict")
	ErrInvalidPolicy       = errors.New("invalid contract publication policy evidence")
	ErrApprovalRequired    = errors.New("security widening requires explicit approval")
	ErrApprovalMismatch    = errors.New("security widening approval does not match publication evidence")
	ErrApprovalExpired     = errors.New("security widening approval is expired or not yet effective")
	ErrApprovalUnexpected  = errors.New("approval is not applicable to a non-widening publication")
)

// ValidationOutcome is intentionally small.  A warning remains successful
// validation evidence; a failed check must never be persisted as publication
// evidence.
type ValidationOutcome string

const (
	ValidationPassed  ValidationOutcome = "passed"
	ValidationWarning ValidationOutcome = "warning"
)

type ValidationCheck struct {
	Name      string            `json:"name"`
	Outcome   ValidationOutcome `json:"outcome"`
	Reference string            `json:"reference"`
	Digest    string            `json:"digest,omitempty"`
}

// ValidationEvidence is normalized before it is retained.  PolicyEvidence
// and ApprovalEvidence are optional so historical validation-only records can
// remain readable while qualified records retain their complete evidence.
type ValidationEvidence struct {
	Version          int                       `json:"version"`
	Checks           []ValidationCheck         `json:"checks"`
	PolicyEvidence   *PolicyEvidence           `json:"policyEvidence,omitempty"`
	ApprovalEvidence *WideningApprovalEvidence `json:"approvalEvidence,omitempty"`
}

// PublicationIdentity is the immutable instance-qualified contract identity.
// Control-plane UIDs are intentionally absent: authored ResourceID/Kind are
// the only identity types this package is allowed to reference.
type PublicationIdentity struct {
	InstanceID        string                  `json:"instanceId"`
	AuthoredID        projectgraph.ResourceID `json:"authoredId"`
	ResourceKind      projectgraph.Kind       `json:"resourceKind"`
	Version           string                  `json:"version"`
	VersionBaseline   string                  `json:"versionBaseline"`
	ProjectionProfile string                  `json:"projectionProfile"`
	Digest            string                  `json:"digest"`
}

// PolicyPublicationIdentity is a descriptive alias used when an identity is
// embedded in policy evidence; it is not a second identity type.
type PolicyPublicationIdentity = PublicationIdentity

// ContractPublication is immutable exact-replay evidence.  Callers receive
// defensive copies from Clone and Canonical; adapters should store the exact
// bytes and fields rather than reconstructing them from source objects.
type ContractPublication struct {
	InstanceID        string                  `json:"instanceId"`
	AuthoredID        projectgraph.ResourceID `json:"authoredId"`
	ResourceKind      projectgraph.Kind       `json:"resourceKind"`
	Version           string                  `json:"version"`
	VersionBaseline   string                  `json:"versionBaseline"`
	ProjectionProfile string                  `json:"projectionProfile"`
	CanonicalBytes    []byte                  `json:"canonicalBytes"`
	Digest            string                  `json:"digest"`
	PublishedAt       time.Time               `json:"publishedAt,omitempty"`
	Validation        ValidationEvidence      `json:"validation"`
}

// ContractPublicationInput supplies only caller-owned projection and checks.
// Baseline and policy are derived separately because first publication and
// update are distinct domain operations.
type ContractPublicationInput struct {
	InstanceID string
	Projection contractprojection.Projection
	Validation ValidationEvidence
}

// PrepareContractPublication derives identity, canonical bytes, digest, and
// normalized validation from the FAI-620 sealed projection.
func PrepareContractPublication(input ContractPublicationInput) (ContractPublication, error) {
	if !validToken(input.InstanceID, 255) {
		return ContractPublication{}, fmt.Errorf("%w: instance id", ErrInvalidPublication)
	}
	canonical, err := contractprojection.CanonicalBytes(input.Projection)
	if err != nil {
		return ContractPublication{}, fmt.Errorf("%w: canonical projection: %v", ErrInvalidPublication, err)
	}
	if len(canonical) == 0 || len(canonical) > MaxCanonicalBytes {
		return ContractPublication{}, fmt.Errorf("%w: canonical bytes exceed bounds", ErrInvalidPublication)
	}
	identity, err := deriveIdentity(input.InstanceID, canonical)
	if err != nil {
		return ContractPublication{}, err
	}
	validation, err := NormalizeValidationEvidence(input.Validation)
	if err != nil {
		return ContractPublication{}, err
	}
	publication := ContractPublication{
		InstanceID: identity.InstanceID, AuthoredID: identity.AuthoredID,
		ResourceKind: identity.ResourceKind, Version: identity.Version,
		VersionBaseline: identity.VersionBaseline, ProjectionProfile: identity.ProjectionProfile,
		CanonicalBytes: append([]byte(nil), canonical...), Digest: identity.Digest,
		Validation: validation,
	}
	if err := publication.Validate(); err != nil {
		return ContractPublication{}, err
	}
	return publication, nil
}

// Prepare is a concise alias for PrepareContractPublication.
func Prepare(input ContractPublicationInput) (ContractPublication, error) {
	return PrepareContractPublication(input)
}

func deriveIdentity(instanceID string, canonical []byte) (PublicationIdentity, error) {
	var envelope struct {
		Profile  string `json:"profile"`
		Kind     string `json:"kind"`
		Metadata struct {
			ID       string `json:"id"`
			Contract struct {
				Version string `json:"version"`
			} `json:"contract"`
		} `json:"metadata"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	if err := decoder.Decode(&envelope); err != nil {
		return PublicationIdentity{}, fmt.Errorf("%w: decode canonical envelope: %v", ErrInvalidPublication, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PublicationIdentity{}, fmt.Errorf("%w: canonical envelope has trailing data", ErrInvalidPublication)
	}
	if envelope.Profile != contractprojection.Profile {
		return PublicationIdentity{}, fmt.Errorf("%w: unsupported projection profile %q", ErrInvalidPublication, envelope.Profile)
	}
	authoredID, err := projectgraph.NewResourceID(envelope.Metadata.ID)
	if err != nil {
		return PublicationIdentity{}, fmt.Errorf("%w: authored id: %v", ErrInvalidPublication, err)
	}
	kind, err := publicationKind(envelope.Kind)
	if err != nil {
		return PublicationIdentity{}, err
	}
	baseline, err := contractversion.SemverBaseline(envelope.Metadata.Contract.Version)
	if err != nil {
		return PublicationIdentity{}, fmt.Errorf("%w: version: %v", ErrInvalidPublication, err)
	}
	digest, err := digestCanonical(kind, canonical)
	if err != nil {
		return PublicationIdentity{}, err
	}
	return PublicationIdentity{
		InstanceID: instanceID, AuthoredID: authoredID, ResourceKind: kind,
		Version: envelope.Metadata.Contract.Version, VersionBaseline: strings.TrimPrefix(baseline, "v"),
		ProjectionProfile: envelope.Profile, Digest: digest,
	}, nil
}

func digestCanonical(kind projectgraph.Kind, canonical []byte) (string, error) {
	var (
		digest string
		err    error
	)
	switch kind {
	case projectgraph.KindSource:
		digest, err = contractprojection.DigestSourcePublication(canonical)
	case projectgraph.KindModel:
		digest, err = contractprojection.DigestModelPublication(canonical)
	case projectgraph.KindSemanticModel:
		digest, err = contractprojection.DigestSemanticModelPublication(canonical)
	default:
		return "", fmt.Errorf("%w: unsupported publication kind %q", ErrInvalidPublication, kind)
	}
	if err != nil {
		return "", fmt.Errorf("%w: canonical bytes: %v", ErrInvalidPublication, err)
	}
	if err := platformdigest.ValidateSHA256Identity(digest); err != nil {
		return "", fmt.Errorf("%w: canonical digest: %v", ErrInvalidPublication, err)
	}
	return digest, nil
}

func publicationKind(value string) (projectgraph.Kind, error) {
	switch value {
	case "Source":
		return projectgraph.KindSource, nil
	case "Model":
		return projectgraph.KindModel, nil
	case "SemanticModel":
		return projectgraph.KindSemanticModel, nil
	default:
		return "", fmt.Errorf("%w: unsupported projection kind %q", ErrInvalidPublication, value)
	}
}

// Identity returns a defensive immutable identity value.
func (p ContractPublication) Identity() PublicationIdentity {
	return PublicationIdentity{
		InstanceID: p.InstanceID, AuthoredID: p.AuthoredID, ResourceKind: p.ResourceKind,
		Version: p.Version, VersionBaseline: p.VersionBaseline,
		ProjectionProfile: p.ProjectionProfile, Digest: p.Digest,
	}
}

// Canonical returns a defensive copy of exact canonical bytes.
func (p ContractPublication) Canonical() []byte { return append([]byte(nil), p.CanonicalBytes...) }

// Clone returns a defensive copy of the publication value and all nested
// slices/pointers.  PublishedAt is retained as persisted evidence, not used
// in equality or any deterministic digest.
func (p ContractPublication) Clone() ContractPublication {
	p.CanonicalBytes = p.Canonical()
	p.Validation = cloneValidationEvidence(p.Validation)
	return p
}

// Validate verifies canonical FAI-620 bytes, all identity fields, and
// normalized validation evidence.  It does not inspect current time; expired
// historical evidence remains internally verifiable.
func (p ContractPublication) Validate() error {
	if !validToken(p.InstanceID, 255) {
		return fmt.Errorf("%w: instance id", ErrInvalidPublication)
	}
	if err := p.AuthoredID.Validate(); err != nil {
		return fmt.Errorf("%w: authored id: %v", ErrInvalidPublication, err)
	}
	if !validPublicationKind(p.ResourceKind) {
		return fmt.Errorf("%w: resource kind", ErrInvalidPublication)
	}
	if p.ProjectionProfile != contractprojection.Profile {
		return fmt.Errorf("%w: projection profile", ErrInvalidPublication)
	}
	if len(p.CanonicalBytes) == 0 || len(p.CanonicalBytes) > MaxCanonicalBytes {
		return fmt.Errorf("%w: canonical bytes bounds", ErrInvalidPublication)
	}
	digest, err := digestCanonical(p.ResourceKind, p.CanonicalBytes)
	if err != nil || digest != p.Digest {
		return fmt.Errorf("%w: canonical digest mismatch", ErrInvalidPublication)
	}
	derived, err := deriveIdentity(p.InstanceID, p.CanonicalBytes)
	if err != nil {
		return err
	}
	if !EqualPublicationIdentity(p.Identity(), derived) {
		return fmt.Errorf("%w: identity does not match canonical envelope", ErrInvalidPublication)
	}
	if err := NormalizeValidationEvidenceMustEqual(p.Validation); err != nil {
		return err
	}
	return nil
}

// NormalizeValidationEvidence returns a value with sorted checks and cloned
// slices.  It rejects invalid outcomes, duplicate check names, malformed
// digests, and oversized serialized evidence.
func NormalizeValidationEvidence(value ValidationEvidence) (ValidationEvidence, error) {
	return normalizeValidationEvidence(value, false)
}

func normalizeValidationEvidence(value ValidationEvidence, allowDerived bool) (ValidationEvidence, error) {
	if value.Version != ValidationEvidenceVersion || len(value.Checks) == 0 || len(value.Checks) > 256 {
		return ValidationEvidence{}, fmt.Errorf("%w: validation evidence version 1 with checks is required", ErrInvalidPublication)
	}
	if !allowDerived && (value.PolicyEvidence != nil || value.ApprovalEvidence != nil) {
		return ValidationEvidence{}, fmt.Errorf("%w: policy and approval evidence are server-derived", ErrInvalidPublication)
	}
	result := cloneValidationEvidence(value)
	for index := range result.Checks {
		check := &result.Checks[index]
		if !validEvidenceText(check.Name, 128) || !validEvidenceText(check.Reference, 2048) {
			return ValidationEvidence{}, fmt.Errorf("%w: validation check identity is invalid", ErrInvalidPublication)
		}
		if check.Outcome != ValidationPassed && check.Outcome != ValidationWarning {
			return ValidationEvidence{}, fmt.Errorf("%w: validation check %q did not pass", ErrInvalidPublication, check.Name)
		}
		if check.Digest != "" {
			if err := platformdigest.ValidateSHA256Identity(check.Digest); err != nil {
				return ValidationEvidence{}, fmt.Errorf("%w: validation check %q digest: %v", ErrInvalidPublication, check.Name, err)
			}
		}
	}
	sort.Slice(result.Checks, func(i, j int) bool {
		if result.Checks[i].Name != result.Checks[j].Name {
			return result.Checks[i].Name < result.Checks[j].Name
		}
		return result.Checks[i].Reference < result.Checks[j].Reference
	})
	for index := 1; index < len(result.Checks); index++ {
		if result.Checks[index-1].Name == result.Checks[index].Name {
			return ValidationEvidence{}, fmt.Errorf("%w: duplicate validation check %q", ErrInvalidPublication, result.Checks[index].Name)
		}
	}
	if result.PolicyEvidence != nil {
		if err := result.PolicyEvidence.Validate(); err != nil {
			return ValidationEvidence{}, err
		}
	}
	if result.ApprovalEvidence != nil {
		if err := result.ApprovalEvidence.Validate(); err != nil {
			return ValidationEvidence{}, err
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > MaxValidationBytes {
		return ValidationEvidence{}, fmt.Errorf("%w: validation evidence exceeds bounds", ErrInvalidPublication)
	}
	return result, nil
}

// NormalizeValidationEvidenceMustEqual verifies that value is already in its
// canonical normalized form.  This prevents a caller from changing evidence
// after preparation while retaining a superficially valid publication.
func NormalizeValidationEvidenceMustEqual(value ValidationEvidence) error {
	normalized, err := normalizeValidationEvidence(value, true)
	if err != nil {
		return err
	}
	left, _ := json.Marshal(value)
	right, _ := json.Marshal(normalized)
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%w: validation evidence is not normalized", ErrInvalidPublication)
	}
	return nil
}

// Validate verifies normalized validation evidence.  Derived policy and
// approval fields are accepted here for replay; caller-supplied preparation
// must use NormalizeValidationEvidence, which rejects those server-owned
// fields.
func (value ValidationEvidence) Validate() error {
	return NormalizeValidationEvidenceMustEqual(value)
}

func cloneValidationEvidence(value ValidationEvidence) ValidationEvidence {
	result := value
	result.Checks = append([]ValidationCheck(nil), value.Checks...)
	if value.PolicyEvidence != nil {
		policy := value.PolicyEvidence.Clone()
		result.PolicyEvidence = &policy
	}
	if value.ApprovalEvidence != nil {
		approval := value.ApprovalEvidence.Clone()
		result.ApprovalEvidence = &approval
	}
	return result
}

// EqualPublicationIdentity compares every immutable identity field.
func EqualPublicationIdentity(left, right PublicationIdentity) bool {
	return left.InstanceID == right.InstanceID && left.AuthoredID == right.AuthoredID &&
		left.ResourceKind == right.ResourceKind && left.Version == right.Version &&
		left.VersionBaseline == right.VersionBaseline && left.ProjectionProfile == right.ProjectionProfile &&
		left.Digest == right.Digest
}

// Equal compares immutable identity fields.
func (i PublicationIdentity) Equal(other PublicationIdentity) bool {
	return EqualPublicationIdentity(i, other)
}

// EqualContractPublicationContent excludes PublishedAt because exact retries
// replay the first persisted timestamp.
func EqualContractPublicationContent(left, right ContractPublication) bool {
	return EqualPublicationIdentity(left.Identity(), right.Identity()) &&
		bytes.Equal(left.CanonicalBytes, right.CanonicalBytes) && equalJSON(left.Validation, right.Validation)
}

func validPublicationKind(kind projectgraph.Kind) bool {
	return kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel
}

func validToken(value string, limit int) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= limit && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validEvidenceText(value string, limit int) bool {
	return validToken(value, limit)
}

func equalJSON(left, right any) bool {
	l, errLeft := json.Marshal(left)
	r, errRight := json.Marshal(right)
	return errLeft == nil && errRight == nil && bytes.Equal(l, r)
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return ocidigest.FromBytes(encoded).String(), nil
}
