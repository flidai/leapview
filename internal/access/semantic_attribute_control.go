package access

// This file contains the control-plane half of semantic access.  It is kept
// separate from semantic_attributes.go deliberately: a caller that only
// qualifies definitions must not accidentally receive authority to mutate
// subject assignments or trusted provider mappings.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

var (
	ErrSemanticAttributeAssignmentConflict = errors.New("semantic attribute assignment conflicts with current state")
	ErrSemanticAttributeMappingConflict    = errors.New("trusted claim mapping conflicts with current state")
	ErrSemanticAttributeSourceConflict     = errors.New("semantic attribute sources conflict")
	ErrSemanticAttributeNotFound           = errors.New("semantic attribute control row was not found")
	ErrSemanticAttributeControlCorrupt     = errors.New("semantic attribute control state is corrupt")
)

// SemanticAttributeAssignment is one direct principal/group value.  Values
// are already canonical and are safe to project into authorization decisions;
// callers must never mutate the returned slice.
type SemanticAttributeAssignment struct {
	ID                string
	DefinitionID      string
	DefinitionName    string
	DefinitionVersion int64
	Type              semanticvalue.Type
	Shape             SemanticAttributeShape
	Subject           SubjectRef
	CanonicalValues   []string
	ValueDigest       string
	AssignmentVersion int64
	Tombstoned        bool
	TombstonedAt      string
	CreatedAt         string
	UpdatedAt         string
}

// SemanticAttributeAssignmentInput addresses an assignment by definition and
// subject. DefinitionID is preferred; DefinitionName is retained as a
// convenient name-qualified boundary for control-plane callers. Exactly one
// is required. ExpectedVersion is zero for creation and otherwise must match
// the current row version.
type SemanticAttributeAssignmentInput struct {
	AssignmentID    string
	DefinitionID    string
	DefinitionName  string
	Subject         SubjectRef
	Values          any
	ExpectedVersion int64
	Mutation        SemanticAttributeMutationContext
}

// SetSemanticAttributeAssignmentInput is the explicit command name used by
// command adapters. Keep the shorter input above as a source-compatible alias.
type SetSemanticAttributeAssignmentInput = SemanticAttributeAssignmentInput

// SemanticAttributeAssignmentFilter limits durable assignment reads. A zero
// filter returns every current and tombstoned row in deterministic order.
type SemanticAttributeAssignmentFilter struct {
	DefinitionID      string
	Subject           SubjectRef
	IncludeTombstones bool
}

// TrustedClaimMapping maps one trusted provider claim to one definition. The
// provider and claim path are immutable identity fields; remapping requires a
// tombstone followed by a new mapping.
type TrustedClaimMapping struct {
	ID                string
	SourceKind        TrustedClaimSourceKind
	Provider          string
	Issuer            string
	Audience          string
	Claim             string
	DefinitionID      string
	DefinitionName    string
	DefinitionVersion int64
	Type              semanticvalue.Type
	Shape             SemanticAttributeShape
	MappingVersion    int64
	Tombstoned        bool
	TombstonedAt      string
	CreatedAt         string
	UpdatedAt         string
}

type TrustedClaimMappingInput struct {
	MappingID       string
	SourceKind      TrustedClaimSourceKind
	Provider        string
	Issuer          string
	Audience        string
	Claim           string
	DefinitionID    string
	DefinitionName  string
	ExpectedVersion int64
	Mutation        SemanticAttributeMutationContext
}

type SetTrustedClaimMappingInput = TrustedClaimMappingInput

type TrustedClaimMappingFilter struct {
	SourceKind        TrustedClaimSourceKind
	Provider          string
	Issuer            string
	Audience          string
	Claim             string
	IncludeTombstones bool
}

// TrustedClaimSourceKind is part of the trust identity. A provider named
// "corp" in OIDC is not interchangeable with a provider named
// "corp" in an embed or service-token source.
type TrustedClaimSourceKind string

const (
	TrustedClaimSourceSAML         TrustedClaimSourceKind = "saml"
	TrustedClaimSourceOIDC         TrustedClaimSourceKind = "oidc"
	TrustedClaimSourceEmbed        TrustedClaimSourceKind = "embed"
	TrustedClaimSourceServiceToken TrustedClaimSourceKind = "service_token"
)

func (kind TrustedClaimSourceKind) Valid() bool {
	return kind == TrustedClaimSourceSAML || kind == TrustedClaimSourceOIDC || kind == TrustedClaimSourceEmbed || kind == TrustedClaimSourceServiceToken
}

type TrustedClaimSource struct {
	Kind     TrustedClaimSourceKind
	Provider string
	Issuer   string
	Audience string
}

// SemanticAttributeControlState is independent from
// SemanticAttributeRegistryState. Registry metadata changes therefore do not
// invalidate an assignment/control snapshot unless a control row changed.
type SemanticAttributeControlState struct {
	Profile   string
	Revision  int64
	Digest    string
	UpdatedAt string
}

type SemanticAttributeControlSnapshot struct {
	State       SemanticAttributeControlState
	Assignments []SemanticAttributeAssignment
	Mappings    []TrustedClaimMapping
}

// EffectiveSemanticAttribute is one resolved value and its source. A source
// conflict is an error; callers must not guess which source wins.
type EffectiveSemanticAttribute struct {
	DefinitionID      string
	DefinitionName    string
	DefinitionVersion int64
	Type              semanticvalue.Type
	Shape             SemanticAttributeShape
	CanonicalValues   []string
	ValueDigest       string
	Source            string
}

type SemanticAttributeAssignmentReader interface {
	SemanticAttributeAssignments(context.Context, SemanticAttributeAssignmentFilter) ([]SemanticAttributeAssignment, error)
	EffectiveDirectSemanticAttributeAssignments(context.Context, SubjectRef) ([]EffectiveSemanticAttribute, error)
	EffectiveSemanticAttributeAssignments(context.Context, SubjectRef, trustedclaims.Envelope) ([]EffectiveSemanticAttribute, error)
}

type SemanticAttributeAssignmentWriter interface {
	SetSemanticAttributeAssignment(context.Context, SemanticAttributeAssignmentInput) (SemanticAttributeAssignment, error)
	TombstoneSemanticAttributeAssignment(context.Context, string, int64, SemanticAttributeMutationContext) (SemanticAttributeAssignment, error)
}

type TrustedClaimMappingReader interface {
	TrustedClaimMappings(context.Context, TrustedClaimMappingFilter) ([]TrustedClaimMapping, error)
}

type TrustedClaimMappingWriter interface {
	SetTrustedClaimMapping(context.Context, TrustedClaimMappingInput) (TrustedClaimMapping, error)
	TombstoneTrustedClaimMapping(context.Context, string, int64, SemanticAttributeMutationContext) (TrustedClaimMapping, error)
}

// These aggregate interfaces are intentionally composed from the narrow
// pieces. Definition registry users do not need to depend on either one.
type SemanticAttributeControlReader interface {
	SemanticAttributeAssignmentReader
	TrustedClaimMappingReader
	SemanticAttributeControl(context.Context) (SemanticAttributeControlSnapshot, error)
}

type SemanticAttributeControlWriter interface {
	SemanticAttributeAssignmentWriter
	TrustedClaimMappingWriter
}

// CanonicalSemanticAttributeValues applies the shared semantic-access v1
// canonicalization profile to one assignment. Maps, functions, nested lists,
// and all other executable/structured values are rejected by the scalar
// canonicalizer; list values are homogeneous, deduplicated, sorted, and
// bounded at semanticvalue.MaxSetValues.
func CanonicalSemanticAttributeValues(definition SemanticAttributeDefinition, input any) ([]string, string, error) {
	if !definition.Enabled {
		return nil, "", fmt.Errorf("%w: %s", ErrSemanticAttributeDisabled, definition.Name)
	}
	if definition.Shape == SemanticAttributeScalar {
		value := reflect.ValueOf(input)
		if value.IsValid() && (value.Kind() == reflect.Array || value.Kind() == reflect.Slice || value.Kind() == reflect.Map || value.Kind() == reflect.Func) {
			return nil, "", fmt.Errorf("%w: scalar assignment cannot be a collection or executable value", semanticvalue.ErrInvalidValue)
		}
		canonical, err := semanticvalue.Canonicalize(definition.Type, input)
		if err != nil {
			return nil, "", err
		}
		return []string{canonical.Canonical()}, canonical.Digest(), nil
	}
	if definition.Shape != SemanticAttributeList {
		return nil, "", fmt.Errorf("%w: semantic attribute shape %q is invalid", semanticvalue.ErrInvalidValue, definition.Shape)
	}
	if input == nil {
		return nil, "", fmt.Errorf("%w: list assignment is nil", semanticvalue.ErrInvalidValue)
	}
	value := reflect.ValueOf(input)
	if value.Kind() != reflect.Array && value.Kind() != reflect.Slice {
		return nil, "", fmt.Errorf("%w: list assignment of type %T is not a list", semanticvalue.ErrInvalidValue, input)
	}
	values := make([]any, value.Len())
	for i := 0; i < value.Len(); i++ {
		item := value.Index(i)
		if !item.IsValid() {
			return nil, "", fmt.Errorf("%w: list item %d is invalid", semanticvalue.ErrInvalidValue, i)
		}
		values[i] = item.Interface()
	}
	set, err := semanticvalue.CanonicalizeSet(definition.Type, values)
	if err != nil {
		return nil, "", err
	}
	canonical := set.Values()
	result := make([]string, len(canonical))
	for i := range canonical {
		result[i] = canonical[i].Canonical()
	}
	return result, set.Digest(), nil
}

func ValidateSemanticAttributeSubject(subject SubjectRef) error {
	if subject.Kind != SubjectKindPrincipal && subject.Kind != SubjectKindGroup {
		return fmt.Errorf("%w: unsupported subject kind %q", ErrInvalidSubjectRef, subject.Kind)
	}
	if strings.TrimSpace(subject.ID) == "" {
		return fmt.Errorf("%w: subject id is required", ErrInvalidSubjectRef)
	}
	return nil
}
