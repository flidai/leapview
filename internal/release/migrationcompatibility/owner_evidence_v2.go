// Package migrationcompatibility defines immutable, owner-produced migration
// compatibility evidence for exact release transitions. Version 2 is a new
// contract and does not reinterpret the historical version 1 projection.
package migrationcompatibility

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/release/transitionpreflight"
)

const (
	EvidenceVersionV2    = "migration-compatibility/v2"
	DigestDomainV2       = "leapview/migration-compatibility/v2\n"
	TargetDigestDomainV2 = "leapview/release-transition-target/v1\n"
	MaxEvidenceBytesV2   = 64 << 10

	GooseOwnerIdentityV2        = "leapview.postgres.goose"
	GooseOwnerContractV2        = "goose-owner-evidence/v1"
	RiverJobsOwnerIdentityV2    = "river.postgres+leapview.jobs"
	RiverJobsOwnerContractV2    = "river-jobs-owner-evidence/v1"
	DuckLakeOwnerIdentityV2     = "leapview.ducklake.catalog"
	DuckLakeOwnerContractV2     = "ducklake-owner-evidence/v1"
	PhysicalPoolOwnerIdentityV2 = "leapview.physical-pool"
	PhysicalPoolOwnerContractV2 = "physical-pool-owner-evidence/v1"
)

var (
	ErrEvidenceV2Invalid             = errors.New("migration compatibility v2 evidence is invalid")
	ErrOwnerEvidenceV2Missing        = errors.New("migration compatibility v2 owner evidence is missing")
	ErrOwnerEvidenceV2Invalid        = errors.New("migration compatibility v2 owner evidence is invalid")
	ErrOwnerEvidenceV2Conflict       = errors.New("migration compatibility v2 owner evidence conflicts")
	ErrOwnerContractV2Unsupported    = errors.New("migration compatibility v2 owner contract is unsupported")
	ErrOwnerEvidenceV2DigestMismatch = errors.New("migration compatibility v2 owner evidence digest mismatch")
	ErrOwnerEvidenceV2NonCanonical   = errors.New("migration compatibility v2 evidence is not canonical")
)

// BindingV2 identifies exactly one admitted transition. OCI admission digests
// are used instead of mutable image references; TargetIdentityDigest is the
// digest of the owner-produced deployment-target identity.
type BindingV2 struct {
	PredecessorOCIAdmissionDigest string `json:"predecessorOCIAdmissionDigest"`
	CandidateOCIAdmissionDigest   string `json:"candidateOCIAdmissionDigest"`
	TargetIdentityDigest          string `json:"targetIdentityDigest"`
}

// DeploymentTargetIdentityV2 is the immutable target projection read from
// the PostgreSQL deployment owner. It intentionally matches the transition
// preflight target identity boundary without depending on the still-isolated
// resolver implementation.
type DeploymentTargetIdentityV2 struct {
	TargetID       string `json:"targetId"`
	TargetRevision int64  `json:"targetRevision"`
}

func (identity DeploymentTargetIdentityV2) Digest() (string, error) {
	if identity.TargetID == "" || identity.TargetID != strings.TrimSpace(identity.TargetID) || len(identity.TargetID) > 255 || strings.IndexFunc(identity.TargetID, unicode.IsControl) >= 0 || identity.TargetRevision <= 0 {
		return "", fmt.Errorf("%w: deployment target identity", ErrEvidenceV2Invalid)
	}
	document, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encode deployment target identity: %w", err)
	}
	return domainDigest(TargetDigestDomainV2, document), nil
}

type OwnerMetadataV2 struct {
	Identity        string `json:"identity"`
	ContractVersion string `json:"contractVersion"`
}

type GooseStateV2 struct {
	PredecessorSchemaVersion string `json:"predecessorSchemaVersion"`
	CandidateSchemaVersion   string `json:"candidateSchemaVersion"`
}

type RiverJobsStateV2 struct {
	PredecessorSchemaVersion     string `json:"predecessorSchemaVersion"`
	CandidateSchemaVersion       string `json:"candidateSchemaVersion"`
	PredecessorJobHistoryVersion string `json:"predecessorJobHistoryVersion"`
	CandidateJobHistoryVersion   string `json:"candidateJobHistoryVersion"`
}

type DuckLakeStateV2 struct {
	Predecessor                     physicalpool.Compatibility `json:"predecessor"`
	Candidate                       physicalpool.Compatibility `json:"candidate"`
	PredecessorCatalogSchemaVersion string                     `json:"predecessorCatalogSchemaVersion"`
	CandidateCatalogSchemaVersion   string                     `json:"candidateCatalogSchemaVersion"`
}

type PhysicalPoolStateV2 struct {
	Predecessor physicalpool.Compatibility `json:"predecessor"`
	Candidate   physicalpool.Compatibility `json:"candidate"`
}

type GooseOwnerEvidenceV2 struct {
	Owner          OwnerMetadataV2                        `json:"owner"`
	Binding        BindingV2                              `json:"binding"`
	Compatibility  transitionpreflight.CompatibilityState `json:"compatibility"`
	State          GooseStateV2                           `json:"state"`
	EvidenceDigest string                                 `json:"evidenceDigest"`
}

type RiverJobsOwnerEvidenceV2 struct {
	Owner          OwnerMetadataV2                        `json:"owner"`
	Binding        BindingV2                              `json:"binding"`
	Compatibility  transitionpreflight.CompatibilityState `json:"compatibility"`
	State          RiverJobsStateV2                       `json:"state"`
	EvidenceDigest string                                 `json:"evidenceDigest"`
}

type DuckLakeOwnerEvidenceV2 struct {
	Owner          OwnerMetadataV2                        `json:"owner"`
	Binding        BindingV2                              `json:"binding"`
	Compatibility  transitionpreflight.CompatibilityState `json:"compatibility"`
	State          DuckLakeStateV2                        `json:"state"`
	EvidenceDigest string                                 `json:"evidenceDigest"`
}

type PhysicalPoolOwnerEvidenceV2 struct {
	Owner          OwnerMetadataV2                        `json:"owner"`
	Binding        BindingV2                              `json:"binding"`
	Compatibility  transitionpreflight.CompatibilityState `json:"compatibility"`
	State          PhysicalPoolStateV2                    `json:"state"`
	EvidenceDigest string                                 `json:"evidenceDigest"`
}

// EvidenceV2 is the complete four-owner compatibility document. Owner
// envelopes are independently content-addressed, so a top-level caller binding
// without matching owner evidence can never form a valid document.
type EvidenceV2 struct {
	Version              string                                 `json:"version"`
	Binding              BindingV2                              `json:"binding"`
	OverallCompatibility transitionpreflight.CompatibilityState `json:"overallCompatibility"`
	Goose                GooseOwnerEvidenceV2                   `json:"goose"`
	RiverJobs            RiverJobsOwnerEvidenceV2               `json:"riverJobs"`
	DuckLake             DuckLakeOwnerEvidenceV2                `json:"duckLake"`
	PhysicalPool         PhysicalPoolOwnerEvidenceV2            `json:"physicalPool"`
}

// NewGooseOwnerEvidenceV2 constructs evidence only after the Goose owner has
// independently resolved and verified binding. Calling this constructor does
// not make a caller-provided binding authoritative.
func NewGooseOwnerEvidenceV2(binding BindingV2, compatibility transitionpreflight.CompatibilityState, state GooseStateV2) (GooseOwnerEvidenceV2, error) {
	value := GooseOwnerEvidenceV2{Owner: OwnerMetadataV2{Identity: GooseOwnerIdentityV2, ContractVersion: GooseOwnerContractV2}, Binding: binding, Compatibility: compatibility, State: state}
	digest, err := value.payloadDigest()
	if err != nil {
		return GooseOwnerEvidenceV2{}, err
	}
	value.EvidenceDigest = digest
	if err := value.Validate(); err != nil {
		return GooseOwnerEvidenceV2{}, err
	}
	return value, nil
}

// NewRiverJobsOwnerEvidenceV2 must be called only at the River/jobs owner
// boundary after that owner has independently verified binding.
func NewRiverJobsOwnerEvidenceV2(binding BindingV2, compatibility transitionpreflight.CompatibilityState, state RiverJobsStateV2) (RiverJobsOwnerEvidenceV2, error) {
	value := RiverJobsOwnerEvidenceV2{Owner: OwnerMetadataV2{Identity: RiverJobsOwnerIdentityV2, ContractVersion: RiverJobsOwnerContractV2}, Binding: binding, Compatibility: compatibility, State: state}
	digest, err := value.payloadDigest()
	if err != nil {
		return RiverJobsOwnerEvidenceV2{}, err
	}
	value.EvidenceDigest = digest
	if err := value.Validate(); err != nil {
		return RiverJobsOwnerEvidenceV2{}, err
	}
	return value, nil
}

// NewDuckLakeOwnerEvidenceV2 must be called only at the DuckLake owner
// boundary after that owner has independently verified binding.
func NewDuckLakeOwnerEvidenceV2(binding BindingV2, compatibility transitionpreflight.CompatibilityState, state DuckLakeStateV2) (DuckLakeOwnerEvidenceV2, error) {
	value := DuckLakeOwnerEvidenceV2{Owner: OwnerMetadataV2{Identity: DuckLakeOwnerIdentityV2, ContractVersion: DuckLakeOwnerContractV2}, Binding: binding, Compatibility: compatibility, State: state}
	digest, err := value.payloadDigest()
	if err != nil {
		return DuckLakeOwnerEvidenceV2{}, err
	}
	value.EvidenceDigest = digest
	if err := value.Validate(); err != nil {
		return DuckLakeOwnerEvidenceV2{}, err
	}
	return value, nil
}

// NewPhysicalPoolOwnerEvidenceV2 must be called only at the physical-pool
// owner boundary after that owner has independently verified binding.
func NewPhysicalPoolOwnerEvidenceV2(binding BindingV2, compatibility transitionpreflight.CompatibilityState, state PhysicalPoolStateV2) (PhysicalPoolOwnerEvidenceV2, error) {
	value := PhysicalPoolOwnerEvidenceV2{Owner: OwnerMetadataV2{Identity: PhysicalPoolOwnerIdentityV2, ContractVersion: PhysicalPoolOwnerContractV2}, Binding: binding, Compatibility: compatibility, State: state}
	digest, err := value.payloadDigest()
	if err != nil {
		return PhysicalPoolOwnerEvidenceV2{}, err
	}
	value.EvidenceDigest = digest
	if err := value.Validate(); err != nil {
		return PhysicalPoolOwnerEvidenceV2{}, err
	}
	return value, nil
}

// NewEvidenceV2 assembles independently produced owner envelopes. The
// aggregate binding is derived from the Goose owner envelope and checked
// against every other owner; it is never supplied separately by a caller.
func NewEvidenceV2(goose GooseOwnerEvidenceV2, river RiverJobsOwnerEvidenceV2, duckLake DuckLakeOwnerEvidenceV2, pool PhysicalPoolOwnerEvidenceV2) (EvidenceV2, error) {
	if ownerMissing(goose.Owner) || ownerMissing(river.Owner) || ownerMissing(duckLake.Owner) || ownerMissing(pool.Owner) {
		return EvidenceV2{}, ErrOwnerEvidenceV2Missing
	}
	overall, err := combinedCompatibility(goose.Compatibility, river.Compatibility, duckLake.Compatibility, pool.Compatibility)
	if err != nil {
		return EvidenceV2{}, err
	}
	value := EvidenceV2{Version: EvidenceVersionV2, Binding: goose.Binding, OverallCompatibility: overall, Goose: goose, RiverJobs: river, DuckLake: duckLake, PhysicalPool: pool}
	if err := value.Validate(); err != nil {
		return EvidenceV2{}, err
	}
	return value, nil
}

func (value EvidenceV2) Validate() error {
	if value.Version != EvidenceVersionV2 {
		return fmt.Errorf("%w: evidence version", ErrEvidenceV2Invalid)
	}
	if err := value.Binding.Validate(); err != nil {
		return err
	}
	if err := value.Goose.Validate(); err != nil {
		return err
	}
	if err := value.RiverJobs.Validate(); err != nil {
		return err
	}
	if err := value.DuckLake.Validate(); err != nil {
		return err
	}
	if err := value.PhysicalPool.Validate(); err != nil {
		return err
	}
	for _, item := range []struct {
		owner   string
		binding BindingV2
	}{
		{"goose", value.Goose.Binding},
		{"river/jobs", value.RiverJobs.Binding},
		{"ducklake", value.DuckLake.Binding},
		{"physical pool", value.PhysicalPool.Binding},
	} {
		if item.binding != value.Binding {
			return fmt.Errorf("%w: %s transition binding", ErrOwnerEvidenceV2Invalid, item.owner)
		}
	}
	if value.DuckLake.State.Predecessor != value.PhysicalPool.State.Predecessor || value.DuckLake.State.Candidate != value.PhysicalPool.State.Candidate {
		return fmt.Errorf("%w: DuckLake and physical-pool tuples", ErrOwnerEvidenceV2Conflict)
	}
	overall, err := combinedCompatibility(value.Goose.Compatibility, value.RiverJobs.Compatibility, value.DuckLake.Compatibility, value.PhysicalPool.Compatibility)
	if err != nil {
		return err
	}
	if overall != value.OverallCompatibility {
		return fmt.Errorf("%w: overall compatibility", ErrOwnerEvidenceV2Conflict)
	}
	return nil
}

func (value EvidenceV2) CanonicalJSON() ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	document, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode migration compatibility v2 evidence: %w", err)
	}
	if len(document) > MaxEvidenceBytesV2 {
		return nil, fmt.Errorf("%w: canonical document exceeds %d bytes", ErrEvidenceV2Invalid, MaxEvidenceBytesV2)
	}
	return document, nil
}

func (value EvidenceV2) Digest() (string, error) {
	document, err := value.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return domainDigest(DigestDomainV2, document), nil
}

// ParseEvidenceV2 proves canonical encoding, internal binding consistency, and
// digest integrity. It does not prove that the envelopes came from trusted
// subsystem owners; production callers must obtain them from concrete owner
// boundaries.
func ParseEvidenceV2(document []byte) (EvidenceV2, error) {
	if len(document) == 0 || len(document) > MaxEvidenceBytesV2 {
		return EvidenceV2{}, fmt.Errorf("%w: canonical document size", ErrEvidenceV2Invalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var value EvidenceV2
	if err := decoder.Decode(&value); err != nil {
		return EvidenceV2{}, fmt.Errorf("%w: decode: %v", ErrEvidenceV2Invalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return EvidenceV2{}, fmt.Errorf("%w: trailing data", ErrEvidenceV2Invalid)
	}
	canonical, err := value.CanonicalJSON()
	if err != nil {
		return EvidenceV2{}, err
	}
	if !bytes.Equal(document, canonical) {
		return EvidenceV2{}, ErrOwnerEvidenceV2NonCanonical
	}
	return value, nil
}

func (binding BindingV2) Validate() error {
	if platformdigest.ValidateSHA256Identity(binding.PredecessorOCIAdmissionDigest) != nil || platformdigest.ValidateSHA256Identity(binding.CandidateOCIAdmissionDigest) != nil || platformdigest.ValidateSHA256Identity(binding.TargetIdentityDigest) != nil || binding.PredecessorOCIAdmissionDigest == binding.CandidateOCIAdmissionDigest {
		return fmt.Errorf("%w: transition binding", ErrEvidenceV2Invalid)
	}
	return nil
}

func (value GooseOwnerEvidenceV2) Validate() error {
	if err := validateOwner(value.Owner, GooseOwnerIdentityV2, GooseOwnerContractV2); err != nil {
		return err
	}
	if err := value.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: Goose binding", ErrOwnerEvidenceV2Invalid)
	}
	if err := validateCompatibility(value.Compatibility); err != nil {
		return err
	}
	if err := validateVersions(value.State.PredecessorSchemaVersion, value.State.CandidateSchemaVersion); err != nil {
		return fmt.Errorf("%w: Goose state", ErrOwnerEvidenceV2Invalid)
	}
	return verifyPayloadDigest(value.EvidenceDigest, value.payloadDigest)
}

func (value RiverJobsOwnerEvidenceV2) Validate() error {
	if err := validateOwner(value.Owner, RiverJobsOwnerIdentityV2, RiverJobsOwnerContractV2); err != nil {
		return err
	}
	if err := value.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: River/jobs binding", ErrOwnerEvidenceV2Invalid)
	}
	if err := validateCompatibility(value.Compatibility); err != nil {
		return err
	}
	if err := validateVersions(value.State.PredecessorSchemaVersion, value.State.CandidateSchemaVersion, value.State.PredecessorJobHistoryVersion, value.State.CandidateJobHistoryVersion); err != nil {
		return fmt.Errorf("%w: River/jobs state", ErrOwnerEvidenceV2Invalid)
	}
	return verifyPayloadDigest(value.EvidenceDigest, value.payloadDigest)
}

func (value DuckLakeOwnerEvidenceV2) Validate() error {
	if err := validateOwner(value.Owner, DuckLakeOwnerIdentityV2, DuckLakeOwnerContractV2); err != nil {
		return err
	}
	if err := value.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: DuckLake binding", ErrOwnerEvidenceV2Invalid)
	}
	if err := validateCompatibility(value.Compatibility); err != nil {
		return err
	}
	if value.State.Predecessor.Validate() != nil || value.State.Candidate.Validate() != nil || validateVersions(value.State.PredecessorCatalogSchemaVersion, value.State.CandidateCatalogSchemaVersion) != nil {
		return fmt.Errorf("%w: DuckLake state", ErrOwnerEvidenceV2Invalid)
	}
	return verifyPayloadDigest(value.EvidenceDigest, value.payloadDigest)
}

func (value PhysicalPoolOwnerEvidenceV2) Validate() error {
	if err := validateOwner(value.Owner, PhysicalPoolOwnerIdentityV2, PhysicalPoolOwnerContractV2); err != nil {
		return err
	}
	if err := value.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: physical-pool binding", ErrOwnerEvidenceV2Invalid)
	}
	if err := validateCompatibility(value.Compatibility); err != nil {
		return err
	}
	if value.State.Predecessor.Validate() != nil || value.State.Candidate.Validate() != nil {
		return fmt.Errorf("%w: physical-pool state", ErrOwnerEvidenceV2Invalid)
	}
	return verifyPayloadDigest(value.EvidenceDigest, value.payloadDigest)
}

func (value GooseOwnerEvidenceV2) payloadDigest() (string, error) {
	payload := struct {
		Owner         OwnerMetadataV2                        `json:"owner"`
		Binding       BindingV2                              `json:"binding"`
		Compatibility transitionpreflight.CompatibilityState `json:"compatibility"`
		State         GooseStateV2                           `json:"state"`
	}{value.Owner, value.Binding, value.Compatibility, value.State}
	return ownerPayloadDigest(GooseOwnerIdentityV2, payload)
}

func (value RiverJobsOwnerEvidenceV2) payloadDigest() (string, error) {
	payload := struct {
		Owner         OwnerMetadataV2                        `json:"owner"`
		Binding       BindingV2                              `json:"binding"`
		Compatibility transitionpreflight.CompatibilityState `json:"compatibility"`
		State         RiverJobsStateV2                       `json:"state"`
	}{value.Owner, value.Binding, value.Compatibility, value.State}
	return ownerPayloadDigest(RiverJobsOwnerIdentityV2, payload)
}

func (value DuckLakeOwnerEvidenceV2) payloadDigest() (string, error) {
	payload := struct {
		Owner         OwnerMetadataV2                        `json:"owner"`
		Binding       BindingV2                              `json:"binding"`
		Compatibility transitionpreflight.CompatibilityState `json:"compatibility"`
		State         DuckLakeStateV2                        `json:"state"`
	}{value.Owner, value.Binding, value.Compatibility, value.State}
	return ownerPayloadDigest(DuckLakeOwnerIdentityV2, payload)
}

func (value PhysicalPoolOwnerEvidenceV2) payloadDigest() (string, error) {
	payload := struct {
		Owner         OwnerMetadataV2                        `json:"owner"`
		Binding       BindingV2                              `json:"binding"`
		Compatibility transitionpreflight.CompatibilityState `json:"compatibility"`
		State         PhysicalPoolStateV2                    `json:"state"`
	}{value.Owner, value.Binding, value.Compatibility, value.State}
	return ownerPayloadDigest(PhysicalPoolOwnerIdentityV2, payload)
}

func ownerPayloadDigest(owner string, value any) (string, error) {
	document, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode %s owner evidence: %w", owner, err)
	}
	return domainDigest("leapview/migration-compatibility/v2/owner/"+owner+"\n", document), nil
}

func verifyPayloadDigest(stored string, calculate func() (string, error)) error {
	if platformdigest.ValidateSHA256Identity(stored) != nil {
		return ErrOwnerEvidenceV2DigestMismatch
	}
	want, err := calculate()
	if err != nil {
		return err
	}
	if stored != want {
		return ErrOwnerEvidenceV2DigestMismatch
	}
	return nil
}

func validateOwner(owner OwnerMetadataV2, identity, version string) error {
	if owner.Identity == "" && owner.ContractVersion == "" {
		return ErrOwnerEvidenceV2Missing
	}
	if owner.ContractVersion != version {
		return fmt.Errorf("%w: %s", ErrOwnerContractV2Unsupported, identity)
	}
	if owner.Identity != identity {
		return fmt.Errorf("%w: owner identity", ErrOwnerEvidenceV2Invalid)
	}
	return nil
}

func validateCompatibility(value transitionpreflight.CompatibilityState) error {
	if value != transitionpreflight.CompatibilityBackwardCompatible && value != transitionpreflight.CompatibilityIncompatible {
		return fmt.Errorf("%w: ambiguous compatibility", ErrOwnerEvidenceV2Invalid)
	}
	return nil
}

func combinedCompatibility(values ...transitionpreflight.CompatibilityState) (transitionpreflight.CompatibilityState, error) {
	overall := transitionpreflight.CompatibilityBackwardCompatible
	for _, value := range values {
		if err := validateCompatibility(value); err != nil {
			return "", err
		}
		if value == transitionpreflight.CompatibilityIncompatible {
			overall = transitionpreflight.CompatibilityIncompatible
		}
	}
	return overall, nil
}

func validateVersions(values ...string) error {
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return ErrOwnerEvidenceV2Invalid
		}
	}
	return nil
}

func ownerMissing(owner OwnerMetadataV2) bool {
	return owner.Identity == "" && owner.ContractVersion == ""
}

func domainDigest(domain string, document []byte) string {
	sum := sha256.Sum256(append([]byte(domain), document...))
	return "sha256:" + hex.EncodeToString(sum[:])
}
