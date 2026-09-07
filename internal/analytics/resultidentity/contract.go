// Package resultidentity defines stable, storage- and engine-independent
// identity contracts for analytical query results.
package resultidentity

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	// DependencyVersion is the canonical dependency serialization version.
	DependencyVersion = 1
	// PartitionVersion is the canonical result partition serialization version.
	PartitionVersion = 2
	// CacheKeyFormatVersion is the version of the composite cache key
	// serialization, without coupling this package to a cache implementation.
	CacheKeyFormatVersion = 2

	// DependencyDigestDomain separates dependency digests from other SHA-256
	// content identities. The digest preimage is this value, one NUL byte, and
	// the canonical dependency serialization.
	DependencyDigestDomain = "flid.resultidentity.dependency.v1"
)

var (
	// ErrInvalidDependency indicates a dependency input that cannot be given a
	// canonical, collision-resistant identity.
	ErrInvalidDependency = errors.New("invalid result dependency")
	// ErrInvalidPartition indicates a malformed production or candidate result
	// partition.
	ErrInvalidPartition = errors.New("invalid result partition")
)

// PartitionKind identifies the serving namespace in which a result belongs.
type PartitionKind string

const (
	PartitionProduction PartitionKind = "production"
	PartitionCandidate  PartitionKind = "candidate"
)

// PartitionInput supplies the stable target/project scope for one result
// partition. TargetID is required for both production and candidate
// partitions. CandidateID must be empty for production and non-empty for
// candidates.
type PartitionInput struct {
	Kind        PartitionKind
	TargetID    string
	ProjectID   projectgraph.ResourceID
	Environment string
	CandidateID string
}

// Partition is an immutable, opaque result namespace. Its canonical bytes are
// owned by the value and accessors never expose mutable storage.
type Partition struct {
	kind        PartitionKind
	targetID    string
	projectID   projectgraph.ResourceID
	environment string
	candidateID string
	canonical   []byte
}

// NewPartition validates and canonically serializes a result partition.
func NewPartition(input PartitionInput) (Partition, error) {
	if err := projectgraph.ValidateServingScope(input.ProjectID, input.Environment); err != nil {
		return Partition{}, fmt.Errorf("%w: %v", ErrInvalidPartition, err)
	}
	if err := validateOpaqueText(input.TargetID); err != nil {
		return Partition{}, fmt.Errorf("%w: target ID: %v", ErrInvalidPartition, err)
	}

	switch input.Kind {
	case PartitionProduction:
		if input.CandidateID != "" {
			return Partition{}, fmt.Errorf("%w: production candidate ID must be empty", ErrInvalidPartition)
		}
	case PartitionCandidate:
		if err := validateOpaqueText(input.CandidateID); err != nil {
			return Partition{}, fmt.Errorf("%w: candidate ID: %v", ErrInvalidPartition, err)
		}
	default:
		return Partition{}, fmt.Errorf("%w: unknown kind %q", ErrInvalidPartition, input.Kind)
	}

	wire := partitionWire{
		Version:     PartitionVersion,
		Kind:        input.Kind,
		TargetID:    input.TargetID,
		ProjectID:   input.ProjectID.String(),
		Environment: input.Environment,
		CandidateID: input.CandidateID,
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Partition{}, fmt.Errorf("%w: serialize: %v", ErrInvalidPartition, err)
	}
	return Partition{
		kind:        input.Kind,
		targetID:    input.TargetID,
		projectID:   input.ProjectID,
		environment: input.Environment,
		candidateID: input.CandidateID,
		canonical:   append([]byte(nil), canonical...),
	}, nil
}

// Version returns the canonical serialization version.
func (p Partition) Version() int {
	if len(p.canonical) == 0 {
		return 0
	}
	return PartitionVersion
}

// Kind returns the partition namespace kind.
func (p Partition) Kind() PartitionKind { return p.kind }

// TargetID returns the stable delivery target identity.
func (p Partition) TargetID() string { return p.targetID }

// ProjectID returns the stable project identity.
func (p Partition) ProjectID() projectgraph.ResourceID { return p.projectID }

// Environment returns the stable serving environment.
func (p Partition) Environment() string { return p.environment }

// CandidateID returns the candidate identity, or an empty string for a
// production partition.
func (p Partition) CandidateID() string { return p.candidateID }

// Canonical returns a defensive copy of the versioned canonical serialization.
func (p Partition) Canonical() []byte { return append([]byte(nil), p.canonical...) }

type partitionWire struct {
	Version     int           `json:"version"`
	Kind        PartitionKind `json:"kind"`
	TargetID    string        `json:"targetId"`
	ProjectID   string        `json:"projectId"`
	Environment string        `json:"environment"`
	CandidateID string        `json:"candidateId,omitempty"`
}

// RelationRevision identifies the exact content revision of one physical
// relation referenced by a query result.
type RelationRevision struct {
	RelationID     projectgraph.ResourceID
	RevisionDigest string
}

// ExecutionIdentity contains independently versionable identities for every
// result-affecting execution concern.
type ExecutionIdentity struct {
	PlannerDigest    string
	RuntimeDigest    string
	CapabilityDigest string
	SettingsDigest   string
}

// ResultFormat identifies the result representation contract.
type ResultFormat struct {
	Name    string
	Version uint32
}

// DependencyInput supplies the complete result-affecting dependency set.
// Dashboard presentation, serving generations, cache storage, and execution
// engine objects are deliberately absent.
type DependencyInput struct {
	SemanticModelID     projectgraph.ResourceID
	SemanticModelDigest string
	Relations           []RelationRevision
	BindingFingerprint  string
	Execution           ExecutionIdentity
	ResultFormat        ResultFormat
}

// Dependency is an immutable, opaque result dependency identity. Construction
// owns the canonical bytes and digest; accessors do not expose mutable storage.
type Dependency struct {
	canonical []byte
	digest    string
}

// NewDependency validates, orders, serializes, and hashes a complete result
// dependency set.
func NewDependency(input DependencyInput) (Dependency, error) {
	if err := input.SemanticModelID.Validate(); err != nil {
		return Dependency{}, fmt.Errorf("%w: semantic model ID: %v", ErrInvalidDependency, err)
	}
	if err := validateDigest("semantic model digest", input.SemanticModelDigest); err != nil {
		return Dependency{}, err
	}
	if len(input.Relations) == 0 {
		return Dependency{}, fmt.Errorf("%w: at least one relation revision is required", ErrInvalidDependency)
	}
	if err := validateDigest("binding fingerprint", input.BindingFingerprint); err != nil {
		return Dependency{}, err
	}
	if err := validateExecution(input.Execution); err != nil {
		return Dependency{}, err
	}
	if err := validateOpaqueText(input.ResultFormat.Name); err != nil {
		return Dependency{}, fmt.Errorf("%w: result format name: %v", ErrInvalidDependency, err)
	}
	if input.ResultFormat.Version == 0 {
		return Dependency{}, fmt.Errorf("%w: result format version must be positive", ErrInvalidDependency)
	}

	relations := make([]relationWire, len(input.Relations))
	for index, relation := range input.Relations {
		if err := relation.RelationID.Validate(); err != nil {
			return Dependency{}, fmt.Errorf("%w: relation %d ID: %v", ErrInvalidDependency, index, err)
		}
		if err := validateDigest(fmt.Sprintf("relation %q revision digest", relation.RelationID), relation.RevisionDigest); err != nil {
			return Dependency{}, err
		}
		relations[index] = relationWire{ID: relation.RelationID.String(), RevisionDigest: relation.RevisionDigest}
	}
	sort.Slice(relations, func(left, right int) bool { return relations[left].ID < relations[right].ID })
	for index := 1; index < len(relations); index++ {
		if relations[index-1].ID == relations[index].ID {
			return Dependency{}, fmt.Errorf("%w: duplicate relation ID %q", ErrInvalidDependency, relations[index].ID)
		}
	}

	wire := dependencyWire{
		Version: DependencyVersion,
		SemanticModel: semanticModelWire{
			ID:     input.SemanticModelID.String(),
			Digest: input.SemanticModelDigest,
		},
		Relations:          relations,
		BindingFingerprint: input.BindingFingerprint,
		Execution: executionWire{
			PlannerDigest:    input.Execution.PlannerDigest,
			RuntimeDigest:    input.Execution.RuntimeDigest,
			CapabilityDigest: input.Execution.CapabilityDigest,
			SettingsDigest:   input.Execution.SettingsDigest,
		},
		ResultFormat: resultFormatWire{Name: input.ResultFormat.Name, Version: input.ResultFormat.Version},
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return Dependency{}, fmt.Errorf("%w: serialize: %v", ErrInvalidDependency, err)
	}

	preimageCapacity, err := dependencyPreimageCapacity(len(canonical))
	if err != nil {
		return Dependency{}, err
	}
	preimage := make([]byte, 0, preimageCapacity)
	preimage = append(preimage, DependencyDigestDomain...)
	preimage = append(preimage, 0)
	preimage = append(preimage, canonical...)
	sum := sha256.Sum256(preimage)

	return Dependency{
		canonical: append([]byte(nil), canonical...),
		digest:    fmt.Sprintf("sha256:%x", sum),
	}, nil
}

// Version returns the canonical serialization version.
func (d Dependency) Version() int {
	if len(d.canonical) == 0 {
		return 0
	}
	return DependencyVersion
}

// Canonical returns a defensive copy of the versioned canonical serialization.
func (d Dependency) Canonical() []byte { return append([]byte(nil), d.canonical...) }

// Digest returns the canonical SHA-256 identity of the domain-separated
// dependency preimage.
func (d Dependency) Digest() string { return d.digest }

type dependencyWire struct {
	Version            int               `json:"version"`
	SemanticModel      semanticModelWire `json:"semanticModel"`
	Relations          []relationWire    `json:"relations"`
	BindingFingerprint string            `json:"bindingFingerprint"`
	Execution          executionWire     `json:"execution"`
	ResultFormat       resultFormatWire  `json:"resultFormat"`
}

type semanticModelWire struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type relationWire struct {
	ID             string `json:"id"`
	RevisionDigest string `json:"revisionDigest"`
}

type executionWire struct {
	PlannerDigest    string `json:"plannerDigest"`
	RuntimeDigest    string `json:"runtimeDigest"`
	CapabilityDigest string `json:"capabilityDigest"`
	SettingsDigest   string `json:"settingsDigest"`
}

type resultFormatWire struct {
	Name    string `json:"name"`
	Version uint32 `json:"version"`
}

func validateExecution(identity ExecutionIdentity) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "planner digest", value: identity.PlannerDigest},
		{name: "runtime digest", value: identity.RuntimeDigest},
		{name: "capability digest", value: identity.CapabilityDigest},
		{name: "settings digest", value: identity.SettingsDigest},
	} {
		if err := validateDigest(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func dependencyPreimageCapacity(canonicalLength int) (int, error) {
	maximumInt := int(^uint(0) >> 1)
	domainLength := len(DependencyDigestDomain)
	if canonicalLength < 0 || domainLength > maximumInt-1 {
		return 0, fmt.Errorf("%w: dependency preimage size cannot be represented", ErrInvalidDependency)
	}
	overhead := domainLength + 1
	if canonicalLength > maximumInt-overhead {
		return 0, fmt.Errorf("%w: dependency preimage size cannot be represented", ErrInvalidDependency)
	}
	return overhead + canonicalLength, nil
}

func validateDigest(name, value string) error {
	if err := platformdigest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidDependency, name, err)
	}
	return nil
}

func validateOpaqueText(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	if value != strings.TrimSpace(value) {
		return errors.New("must not contain surrounding whitespace")
	}
	if !utf8.ValidString(value) {
		return errors.New("must be valid UTF-8")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}
