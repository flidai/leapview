// Package migrationcapability defines immutable, artifact-owned migration
// capability records consumed by future migration-compatibility authorities.
package migrationcapability

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
)

const (
	Version           = "migration-capability/v1"
	DigestDomain      = "leapview/migration-capability/v1\n"
	MaxCanonicalBytes = 64 << 10
	MaxVersionEntries = 256

	SubsystemGoose        Subsystem = "goose"
	SubsystemRiverJobs    Subsystem = "river-jobs"
	SubsystemDuckLake     Subsystem = "ducklake"
	SubsystemPhysicalPool Subsystem = "physical-pool"

	GooseOwnerIdentity            = "leapview.postgres.goose"
	GooseOwnerContractVersion     = "goose-owner-capability/v1"
	RiverJobsOwnerIdentity        = "river.postgres+leapview.jobs"
	RiverJobsOwnerContractVersion = "river-jobs-owner-capability/v1"
	DuckLakeOwnerIdentity         = "leapview.ducklake.catalog"
	DuckLakeOwnerContractVersion  = "ducklake-owner-capability/v1"
	PhysicalPoolOwnerIdentity     = "leapview.physical-pool"
	PhysicalPoolContractVersion   = "physical-pool-owner-capability/v1"
)

var (
	ErrInvalid      = errors.New("migration capability is invalid")
	ErrNonCanonical = errors.New("migration capability is not canonical")
)

// Authority is the read-only runtime boundary. Callers provide only exact
// content identities; they cannot supply or alter a capability projection.
type Authority interface {
	ResolveMigrationCapability(context.Context, string, string, Subsystem) (Capability, error)
}

type Subsystem string

type Owner struct {
	Identity        string `json:"identity"`
	ContractVersion string `json:"contractVersion"`
}

type GooseCapability struct {
	SchemaVersion          string   `json:"schemaVersion"`
	RunnableSchemaVersions []string `json:"runnableSchemaVersions"`
	MigrationGraphDigest   string   `json:"migrationGraphDigest"`
}

type RiverJobsCapability struct {
	SchemaVersion              string   `json:"schemaVersion"`
	RunnableSchemaVersions     []string `json:"runnableSchemaVersions"`
	JobHistoryVersion          string   `json:"jobHistoryVersion"`
	RunnableJobHistoryVersions []string `json:"runnableJobHistoryVersions"`
	MigrationGraphDigest       string   `json:"migrationGraphDigest"`
}

type DuckLakeCapability struct {
	Compatibility                 physicalpool.Compatibility `json:"compatibility"`
	CatalogSchemaVersion          string                     `json:"catalogSchemaVersion"`
	RunnableCatalogSchemaVersions []string                   `json:"runnableCatalogSchemaVersions"`
	MigrationGraphDigest          string                     `json:"migrationGraphDigest"`
}

type PhysicalPoolCapability struct {
	Compatibility          physicalpool.Compatibility `json:"compatibility"`
	CompatibleTupleDigests []string                   `json:"compatibleTupleDigests"`
}

// Capability is one subsystem owner's complete statement about one admitted
// artifact at one immutable deployment target. Exactly one subsystem payload
// is present; explicit nulls keep the wire shape stable and unambiguous.
type Capability struct {
	Version                 string                  `json:"version"`
	ArtifactAdmissionDigest string                  `json:"artifactAdmissionDigest"`
	TargetIdentityDigest    string                  `json:"targetIdentityDigest"`
	Subsystem               Subsystem               `json:"subsystem"`
	Owner                   Owner                   `json:"owner"`
	Goose                   *GooseCapability        `json:"goose"`
	RiverJobs               *RiverJobsCapability    `json:"riverJobs"`
	DuckLake                *DuckLakeCapability     `json:"duckLake"`
	PhysicalPool            *PhysicalPoolCapability `json:"physicalPool"`
}

func (c Capability) CanonicalJSON() ([]byte, error) {
	normalized, err := c.normalized()
	if err != nil {
		return nil, err
	}
	document, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode migration capability: %w", err)
	}
	if len(document) > MaxCanonicalBytes {
		return nil, fmt.Errorf("%w: canonical document exceeds %d bytes", ErrInvalid, MaxCanonicalBytes)
	}
	return document, nil
}

func (c Capability) Digest() (string, error) {
	document, err := c.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(DigestDomain), document...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (c Capability) Validate() error {
	_, err := c.normalized()
	return err
}

func ParseCanonical(document []byte) (Capability, error) {
	if len(document) == 0 || len(document) > MaxCanonicalBytes {
		return Capability{}, fmt.Errorf("%w: canonical document size", ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var capability Capability
	if err := decoder.Decode(&capability); err != nil {
		return Capability{}, fmt.Errorf("%w: decode: %v", ErrInvalid, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Capability{}, fmt.Errorf("%w: trailing data", ErrInvalid)
	}
	canonical, err := capability.CanonicalJSON()
	if err != nil {
		return Capability{}, err
	}
	if !bytes.Equal(document, canonical) {
		return Capability{}, ErrNonCanonical
	}
	return capability.normalized()
}

func (c Capability) normalized() (Capability, error) {
	if c.Version != Version {
		return Capability{}, fmt.Errorf("%w: unsupported version", ErrInvalid)
	}
	if platformdigest.ValidateSHA256Identity(c.ArtifactAdmissionDigest) != nil {
		return Capability{}, fmt.Errorf("%w: artifact admission digest", ErrInvalid)
	}
	if platformdigest.ValidateSHA256Identity(c.TargetIdentityDigest) != nil {
		return Capability{}, fmt.Errorf("%w: target identity digest", ErrInvalid)
	}
	wantOwner, wantContract, err := ownerFor(c.Subsystem)
	if err != nil {
		return Capability{}, err
	}
	if c.Owner.Identity != wantOwner || c.Owner.ContractVersion != wantContract {
		return Capability{}, fmt.Errorf("%w: subsystem owner", ErrInvalid)
	}
	if err := validateText(c.Owner.Identity); err != nil {
		return Capability{}, fmt.Errorf("%w: owner identity", ErrInvalid)
	}
	if err := validateText(c.Owner.ContractVersion); err != nil {
		return Capability{}, fmt.Errorf("%w: owner contract version", ErrInvalid)
	}

	normalized := c
	payloads := 0
	if c.Goose != nil {
		payloads++
		value := *c.Goose
		value.RunnableSchemaVersions, err = normalizeVersions(value.RunnableSchemaVersions)
		if err != nil || validateText(value.SchemaVersion) != nil || !slices.Contains(value.RunnableSchemaVersions, value.SchemaVersion) || platformdigest.ValidateSHA256Identity(value.MigrationGraphDigest) != nil {
			return Capability{}, fmt.Errorf("%w: Goose capability", ErrInvalid)
		}
		normalized.Goose = &value
	}
	if c.RiverJobs != nil {
		payloads++
		value := *c.RiverJobs
		value.RunnableSchemaVersions, err = normalizeVersions(value.RunnableSchemaVersions)
		if err != nil {
			return Capability{}, fmt.Errorf("%w: River schema versions", ErrInvalid)
		}
		value.RunnableJobHistoryVersions, err = normalizeVersions(value.RunnableJobHistoryVersions)
		if err != nil || validateText(value.SchemaVersion) != nil || validateText(value.JobHistoryVersion) != nil || !slices.Contains(value.RunnableSchemaVersions, value.SchemaVersion) || !slices.Contains(value.RunnableJobHistoryVersions, value.JobHistoryVersion) || platformdigest.ValidateSHA256Identity(value.MigrationGraphDigest) != nil {
			return Capability{}, fmt.Errorf("%w: River/jobs capability", ErrInvalid)
		}
		normalized.RiverJobs = &value
	}
	if c.DuckLake != nil {
		payloads++
		value := *c.DuckLake
		value.RunnableCatalogSchemaVersions, err = normalizeVersions(value.RunnableCatalogSchemaVersions)
		if err != nil || value.Compatibility.Validate() != nil || validateText(value.CatalogSchemaVersion) != nil || !slices.Contains(value.RunnableCatalogSchemaVersions, value.CatalogSchemaVersion) || platformdigest.ValidateSHA256Identity(value.MigrationGraphDigest) != nil {
			return Capability{}, fmt.Errorf("%w: DuckLake capability", ErrInvalid)
		}
		normalized.DuckLake = &value
	}
	if c.PhysicalPool != nil {
		payloads++
		value := *c.PhysicalPool
		value.CompatibleTupleDigests, err = normalizeDigests(value.CompatibleTupleDigests)
		tupleDigest, digestErr := value.Compatibility.Digest()
		if err != nil || digestErr != nil || !slices.Contains(value.CompatibleTupleDigests, tupleDigest) {
			return Capability{}, fmt.Errorf("%w: PhysicalPool capability", ErrInvalid)
		}
		normalized.PhysicalPool = &value
	}
	if payloads != 1 || !payloadMatches(c.Subsystem, normalized) {
		return Capability{}, fmt.Errorf("%w: subsystem payload", ErrInvalid)
	}
	return normalized, nil
}

func ownerFor(subsystem Subsystem) (string, string, error) {
	switch subsystem {
	case SubsystemGoose:
		return GooseOwnerIdentity, GooseOwnerContractVersion, nil
	case SubsystemRiverJobs:
		return RiverJobsOwnerIdentity, RiverJobsOwnerContractVersion, nil
	case SubsystemDuckLake:
		return DuckLakeOwnerIdentity, DuckLakeOwnerContractVersion, nil
	case SubsystemPhysicalPool:
		return PhysicalPoolOwnerIdentity, PhysicalPoolContractVersion, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported subsystem", ErrInvalid)
	}
}

func payloadMatches(subsystem Subsystem, c Capability) bool {
	switch subsystem {
	case SubsystemGoose:
		return c.Goose != nil
	case SubsystemRiverJobs:
		return c.RiverJobs != nil
	case SubsystemDuckLake:
		return c.DuckLake != nil
	case SubsystemPhysicalPool:
		return c.PhysicalPool != nil
	default:
		return false
	}
}

func normalizeVersions(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxVersionEntries {
		return nil, errors.New("version list is empty or too large")
	}
	normalized := append([]string(nil), values...)
	for _, value := range normalized {
		if err := validateText(value); err != nil {
			return nil, err
		}
	}
	sort.Strings(normalized)
	for i := 1; i < len(normalized); i++ {
		if normalized[i] == normalized[i-1] {
			return nil, errors.New("version list contains a duplicate")
		}
	}
	return normalized, nil
}

func normalizeDigests(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxVersionEntries {
		return nil, errors.New("digest list is empty or too large")
	}
	normalized := append([]string(nil), values...)
	for _, value := range normalized {
		if platformdigest.ValidateSHA256Identity(value) != nil {
			return nil, errors.New("digest list contains an invalid identity")
		}
	}
	sort.Strings(normalized)
	for i := 1; i < len(normalized); i++ {
		if normalized[i] == normalized[i-1] {
			return nil, errors.New("digest list contains a duplicate")
		}
	}
	return normalized, nil
}

func validateText(value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 512 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("text is not canonical")
	}
	return nil
}
