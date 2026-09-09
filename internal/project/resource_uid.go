package project

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/flidai/leapview/internal/platform/instanceidentity"
	"github.com/flidai/leapview/internal/project/contractprojection"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

// ResourceUID is an opaque, instance-local identity allocated when a
// generation is activated. It is deliberately not a portable authored field.
type ResourceUID string

var (
	ErrInvalidResourceUID         = errors.New("invalid resource UID")
	ErrResourceUIDConflict        = errors.New("resource UID conflict")
	ErrResourceUIDNotFound        = errors.New("resource UID not found")
	ErrResourceUIDKindConflict    = errors.New("resource UID kind conflict")
	ErrResourceUIDTombstoned      = errors.New("resource UID is tombstoned")
	ErrResourceUIDRestoreRequired = errors.New("resource UID restore authorization required")
)

const (
	maxResourceUIDTextBytes = 255
	contractProfile         = contractprojection.Profile
)

// ParseResourceUID accepts only canonical, non-nil UUID text.
func ParseResourceUID(value string) (ResourceUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return "", fmt.Errorf("%w %q", ErrInvalidResourceUID, value)
	}
	return ResourceUID(value), nil
}

func (uid ResourceUID) String() string { return string(uid) }

func (uid ResourceUID) Validate() error {
	_, err := ParseResourceUID(uid.String())
	return err
}

type ResourceUIDState string

const (
	ResourceUIDActive     ResourceUIDState = "active"
	ResourceUIDTombstoned ResourceUIDState = "tombstoned"
)

// ResourceUIDRecord is the current instance/project binding for one authored
// ID. Generation fields are retained so rollback and tombstone behavior never
// need to infer identity from a path, name, or display value.
type ResourceUIDRecord struct {
	UID                 ResourceUID
	InstanceID          string
	ProjectID           string
	AuthoredID          string
	Kind                projectgraph.Kind
	State               ResourceUIDState
	FirstGeneration     string
	LatestGeneration    string
	CurrentGeneration   string
	RemovedInGeneration string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ResourceUIDBinding is immutable evidence that a generation used one UID.
// Contract evidence is present for the FAI-620 versioned resource kinds and
// absent for resources without a canonical contract projection.
type ResourceUIDBinding struct {
	ResourceUID     ResourceUID
	InstanceID      string
	ProjectID       string
	Environment     string
	TargetID        string
	GenerationID    string
	AuthoredID      string
	Kind            projectgraph.Kind
	ContractStatus  string
	ContractProfile string
	ContractVersion string
	ContractDigest  string
	ContractBytes   []byte
	BoundAt         time.Time
}

// ResourceUIDRestoreAuthorization is audited evidence granting one exact
// tombstoned ID restore. Its consumed transition is guarded by PostgreSQL.
type ResourceUIDRestoreAuthorization struct {
	RestoreID     string
	ResourceUID   ResourceUID
	InstanceID    string
	ProjectID     string
	Environment   string
	TargetID      string
	GenerationID  string
	AuthoredID    string
	Kind          projectgraph.Kind
	ActorID       string
	RequestDigest string
	Status        string
	CreatedAt     time.Time
	ConsumedAt    time.Time
}

func authoredResourceKindValid(kind projectgraph.Kind) bool {
	switch kind {
	case projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel,
		projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard:
		return true
	default:
		return false
	}
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed != uuid.Nil && parsed.String() == value
}

func canonicalDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func boundedResourceText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= maxResourceUIDTextBytes &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func boundedResourceID(value string) bool {
	return boundedResourceText(value) && projectgraph.ResourceID(value).Valid()
}

// authoredResourceIDValid follows the graph resource-ID grammar directly.
// Unlike control-plane project IDs, authored IDs are not bounded by the
// storage metadata text limit; PostgreSQL text has no such 255-byte ceiling.
func authoredResourceIDValid(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\x00\r\n") && projectgraph.ResourceID(value).Valid()
}

func registryIdentityValid(instanceID, projectID, authoredID string, kind projectgraph.Kind) bool {
	return instanceidentity.Valid(instanceID) && boundedResourceID(projectID) &&
		authoredResourceIDValid(authoredID) && authoredResourceKindValid(kind)
}

func validContractEvidence(kind projectgraph.Kind, profile, version, digest, authoredID string, bytes []byte) bool {
	// Version syntax and canonical structure belong to FAI-620, not to a
	// second registry-specific parser. Canonical evidence is an exact tuple.
	if profile != contractProfile || version == "" || !canonicalDigest(digest) || len(bytes) == 0 {
		return false
	}
	return validPublishedContract(kind, version, digest, authoredID, bytes)
}

// validPublishedContract delegates structural validation to the FAI-620
// publication decoders. Registry rows must not merely contain hash-shaped
// bytes: the bytes must be the canonical publication for this authored ID and
// resource kind. The caller checks the authored ID separately because the
// generated views intentionally expose it under kind-specific metadata.
func validPublishedContract(kind projectgraph.Kind, version, digest, authoredID string, bytes []byte) bool {
	switch kind {
	case projectgraph.KindSource:
		value, err := contractprojection.DecodeSourcePublication(bytes)
		publishedDigest, digestErr := contractprojection.DigestSourcePublication(bytes)
		return err == nil && digestErr == nil && publishedDigest == digest && value.Profile == contractprojection.Profile && value.Metadata.Contract.Version == version && value.Metadata.ID == authoredID
	case projectgraph.KindModel:
		value, err := contractprojection.DecodeModelPublication(bytes)
		publishedDigest, digestErr := contractprojection.DigestModelPublication(bytes)
		return err == nil && digestErr == nil && publishedDigest == digest && value.Profile == contractprojection.Profile && value.Metadata.Contract.Version == version && value.Metadata.ID == authoredID
	case projectgraph.KindSemanticModel:
		value, err := contractprojection.DecodeSemanticModelPublication(bytes)
		publishedDigest, digestErr := contractprojection.DigestSemanticModelPublication(bytes)
		return err == nil && digestErr == nil && publishedDigest == digest && value.Profile == contractprojection.Profile && value.Metadata.Contract.Version == version && value.Metadata.ID == authoredID
	default:
		return false
	}
}

func validContractStatus(kind projectgraph.Kind, status, profile, version, digest, authoredID string, bytes []byte) bool {
	switch status {
	case "canonical":
		return validContractEvidence(kind, profile, version, digest, authoredID, bytes) && profile != ""
	case "unversioned":
		return (kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel) && profile == "" && version == "" && digest == "" && len(bytes) == 0
	case "not_contract_bearing":
		return (kind == projectgraph.KindConnection || kind == projectgraph.KindPipeline || kind == projectgraph.KindDashboard) && profile == "" && version == "" && digest == "" && len(bytes) == 0
	default:
		return false
	}
}

func (record ResourceUIDRecord) Validate() error {
	if record.UID.Validate() != nil || !registryIdentityValid(record.InstanceID, record.ProjectID, record.AuthoredID, record.Kind) ||
		!canonicalUUID(record.FirstGeneration) || !canonicalUUID(record.LatestGeneration) {
		return ErrInvalidResourceUID
	}
	switch record.State {
	case ResourceUIDActive:
		if record.RemovedInGeneration != "" || !canonicalUUID(record.CurrentGeneration) || record.CurrentGeneration != record.LatestGeneration {
			return ErrInvalidResourceUID
		}
	case ResourceUIDTombstoned:
		if record.CurrentGeneration != "" || !canonicalUUID(record.RemovedInGeneration) || record.RemovedInGeneration != record.LatestGeneration {
			return ErrInvalidResourceUID
		}
	default:
		return ErrInvalidResourceUID
	}
	return nil
}

func (binding ResourceUIDBinding) Validate() error {
	contractBytes := binding.ContractBytes
	if binding.ResourceUID.Validate() != nil || !registryIdentityValid(binding.InstanceID, binding.ProjectID, binding.AuthoredID, binding.Kind) ||
		!boundedResourceText(binding.TargetID) || binding.TargetID != binding.InstanceID ||
		projectgraph.ValidateServingEnvironment(binding.Environment) != nil || !canonicalUUID(binding.GenerationID) ||
		!validContractStatus(binding.Kind, binding.ContractStatus, binding.ContractProfile, binding.ContractVersion, binding.ContractDigest, binding.AuthoredID, contractBytes) {
		return ErrInvalidResourceUID
	}
	return nil
}

func (restore ResourceUIDRestoreAuthorization) Validate() error {
	if !canonicalUUID(restore.RestoreID) || restore.ResourceUID.Validate() != nil ||
		!registryIdentityValid(restore.InstanceID, restore.ProjectID, restore.AuthoredID, restore.Kind) ||
		projectgraph.ValidateServingEnvironment(restore.Environment) != nil || restore.TargetID != restore.InstanceID ||
		!canonicalUUID(restore.GenerationID) || !boundedResourceText(restore.ActorID) ||
		strings.IndexFunc(restore.ActorID, unicode.IsControl) >= 0 || !canonicalDigest(restore.RequestDigest) {
		return ErrInvalidResourceUID
	}
	if restore.Status == "pending" {
		if !restore.ConsumedAt.IsZero() {
			return ErrInvalidResourceUID
		}
		return nil
	}
	if restore.Status == "consumed" {
		if restore.ConsumedAt.IsZero() {
			return ErrInvalidResourceUID
		}
		return nil
	}
	return ErrInvalidResourceUID
}
