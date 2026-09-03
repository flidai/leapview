package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/semanticvalue"
)

var (
	ErrSemanticAttributeConflict = errors.New("semantic attribute definition conflicts with the registry")
	ErrSemanticAttributeDisabled = errors.New("semantic attribute is disabled")
)

// SemanticAttributeShape distinguishes a scalar assignment from a homogeneous
// list assignment. The element type remains one of semanticvalue's closed v1
// logical types.
type SemanticAttributeShape string

const (
	SemanticAttributeScalar SemanticAttributeShape = "scalar"
	SemanticAttributeList   SemanticAttributeShape = "list"
)

func (shape SemanticAttributeShape) Valid() bool {
	return shape == SemanticAttributeScalar || shape == SemanticAttributeList
}

type SemanticAttributeOwnerKind string

const (
	SemanticAttributeOwnerInstance  SemanticAttributeOwnerKind = "instance"
	SemanticAttributeOwnerPrincipal SemanticAttributeOwnerKind = "principal"
	SemanticAttributeOwnerGroup     SemanticAttributeOwnerKind = "group"
)

func (kind SemanticAttributeOwnerKind) Valid() bool {
	return kind == SemanticAttributeOwnerInstance || kind == SemanticAttributeOwnerPrincipal || kind == SemanticAttributeOwnerGroup
}

type SemanticAttributeLifecycleState string

const (
	SemanticAttributeActive   SemanticAttributeLifecycleState = "active"
	SemanticAttributeDisabled SemanticAttributeLifecycleState = "disabled"
)

type SemanticAttributeOwner struct {
	Kind SemanticAttributeOwnerKind
	ID   string
}

type SemanticAttributeMetadata struct {
	Owner            SemanticAttributeOwner
	DisplayName      string
	Description      string
	DocumentationURL string
}

type SemanticAttributeDefinition struct {
	ID                string
	Name              string
	Type              semanticvalue.Type
	Shape             SemanticAttributeShape
	Profile           string
	DefinitionVersion int64
	Metadata          SemanticAttributeMetadata
	LifecycleState    SemanticAttributeLifecycleState
	Enabled           bool
	DisabledAt        string
	CreatedAt         string
	UpdatedAt         string
}

type SemanticAttributeRegistryState struct {
	Profile   string
	Revision  int64
	Digest    string
	UpdatedAt string
}

type SemanticAttributeRegistrySnapshot struct {
	State       SemanticAttributeRegistryState
	Definitions []SemanticAttributeDefinition
}

type SemanticAttributeMutationContext struct {
	ActorPrincipalID string
	RequestID        string
	CorrelationID    string
}

type RegisterSemanticAttributeInput struct {
	Name     string
	Type     semanticvalue.Type
	Shape    SemanticAttributeShape
	Metadata SemanticAttributeMetadata
	Mutation SemanticAttributeMutationContext
}

type UpdateSemanticAttributeMetadataInput struct {
	Name     string
	Metadata SemanticAttributeMetadata
	// ExpectedVersion is checked after the registry row is locked. Zero keeps
	// legacy internal callers source-compatible; version-aware command paths
	// must provide the version returned by the previous read.
	ExpectedVersion int64
	Mutation SemanticAttributeMutationContext
}

type SemanticAttributeSearch struct {
	Query string
	Limit int
}

// CanonicalSemanticAttributeValue is the profile-qualified value identity
// produced by the shared semanticvalue package. Scalar values contain exactly
// one canonical value; list values are deduplicated and sorted.
type CanonicalSemanticAttributeValue struct {
	DefinitionID      string
	DefinitionVersion int64
	Name              string
	Type              semanticvalue.Type
	Shape             SemanticAttributeShape
	CanonicalValues   []string
	Digest            string
}

// ValidateSemanticAttributeCompatibility rejects identity or logical-type
// rewrites. Metadata and lifecycle transitions are versioned registry changes;
// changing name, type, shape, profile, or stable ID requires a new attribute.
func ValidateSemanticAttributeCompatibility(current, candidate SemanticAttributeDefinition) error {
	if current.ID != candidate.ID || current.Name != candidate.Name || current.Type != candidate.Type ||
		current.Shape != candidate.Shape || current.Profile != candidate.Profile {
		return fmt.Errorf("%w: semantic attribute identity and logical type are immutable", ErrSemanticAttributeConflict)
	}
	return nil
}

// SemanticAttributeRegistry is the narrow control-plane boundary for typed
// definition lifecycle. Assignment, trusted-claim, and generation-reference
// interfaces are added separately so consumers cannot acquire mutation powers
// by depending on read-only registry qualification.
type SemanticAttributeRegistry interface {
	SemanticAttributeRegistry(context.Context) (SemanticAttributeRegistrySnapshot, error)
	SemanticAttributeDefinition(context.Context, string) (SemanticAttributeDefinition, error)
	SemanticAttributeDefinitionByID(context.Context, string) (SemanticAttributeDefinition, error)
	SearchSemanticAttributes(context.Context, SemanticAttributeSearch) ([]SemanticAttributeDefinition, error)
	RegisterSemanticAttribute(context.Context, RegisterSemanticAttributeInput) (SemanticAttributeDefinition, error)
	UpdateSemanticAttributeMetadata(context.Context, UpdateSemanticAttributeMetadataInput) (SemanticAttributeDefinition, error)
	SetSemanticAttributeEnabled(context.Context, string, bool, SemanticAttributeMutationContext) (SemanticAttributeDefinition, error)
	ValidateSemanticAttributeValue(context.Context, string, any) (CanonicalSemanticAttributeValue, error)
}

// VersionedSemanticAttributeRegistry is the optimistic-concurrency surface
// used by command adapters. It is separate so read-only registry consumers do
// not acquire lifecycle mutation authority.
type VersionedSemanticAttributeRegistry interface {
	UpdateSemanticAttributeMetadataExpected(context.Context, UpdateSemanticAttributeMetadataInput) (SemanticAttributeDefinition, error)
	SetSemanticAttributeEnabledExpected(context.Context, string, bool, int64, SemanticAttributeMutationContext) (SemanticAttributeDefinition, error)
}
