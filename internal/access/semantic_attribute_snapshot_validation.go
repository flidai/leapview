package access

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access/trustedclaims"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// ValidateSemanticAttributeRegistrySnapshot verifies both canonical identity
// and lifecycle/ownership invariants that are intentionally not duplicated in
// the persisted FAI-636 digest format.
func ValidateSemanticAttributeRegistrySnapshot(snapshot SemanticAttributeRegistrySnapshot) error {
	if snapshot.State.Profile != semanticvalue.Profile || snapshot.State.Revision < 0 || !canonicalSemanticAttributeDigest(snapshot.State.Digest) {
		return fmt.Errorf("%w: registry state identity is invalid", ErrSemanticAttributeRegistryCorrupt)
	}
	names := make(map[string]struct{}, len(snapshot.Definitions))
	ids := make(map[string]struct{}, len(snapshot.Definitions))
	for _, definition := range snapshot.Definitions {
		if !canonicalSemanticAttributeIdentity(definition.ID) || definition.Profile != semanticvalue.Profile || definition.DefinitionVersion <= 0 ||
			!semanticAttributeTypeValid(definition.Type) || !definition.Shape.Valid() {
			return fmt.Errorf("%w: definition %q identity, profile, version, type, or shape is invalid", ErrSemanticAttributeRegistryCorrupt, definition.Name)
		}
		if err := semanticvalue.ValidateAttributeName(definition.Name); err != nil {
			return fmt.Errorf("%w: definition name is invalid", ErrSemanticAttributeRegistryCorrupt)
		}
		if _, duplicate := ids[definition.ID]; duplicate {
			return fmt.Errorf("%w: duplicate definition id %q", ErrSemanticAttributeRegistryCorrupt, definition.ID)
		}
		if _, duplicate := names[definition.Name]; duplicate {
			return fmt.Errorf("%w: duplicate definition name %q", ErrSemanticAttributeRegistryCorrupt, definition.Name)
		}
		ids[definition.ID], names[definition.Name] = struct{}{}, struct{}{}
		owner := definition.Metadata.Owner
		if !owner.Kind.Valid() || owner.ID != strings.TrimSpace(owner.ID) ||
			(owner.Kind == SemanticAttributeOwnerInstance && owner.ID != "") || (owner.Kind != SemanticAttributeOwnerInstance && owner.ID == "") {
			return fmt.Errorf("%w: definition %q owner is invalid", ErrSemanticAttributeRegistryCorrupt, definition.Name)
		}
		switch definition.LifecycleState {
		case SemanticAttributeActive:
			if !definition.Enabled || definition.DisabledAt != "" {
				return fmt.Errorf("%w: definition %q active lifecycle projection is inconsistent", ErrSemanticAttributeRegistryCorrupt, definition.Name)
			}
		case SemanticAttributeDisabled:
			if definition.Enabled || definition.DisabledAt == "" {
				return fmt.Errorf("%w: definition %q disabled lifecycle projection is inconsistent", ErrSemanticAttributeRegistryCorrupt, definition.Name)
			}
		default:
			return fmt.Errorf("%w: definition %q lifecycle state is invalid", ErrSemanticAttributeRegistryCorrupt, definition.Name)
		}
	}
	digest, err := SemanticAttributeRegistryDigest(snapshot.State.Profile, snapshot.Definitions)
	if err != nil || digest != snapshot.State.Digest {
		return fmt.Errorf("%w: registry digest does not match definitions", ErrSemanticAttributeRegistryCorrupt)
	}
	return nil
}

// ValidateSemanticAttributeControlSnapshot verifies canonical identity and
// the row invariants whose derived projections are not part of the persisted
// FAI-637 digest format. It does not reinterpret missing authority as empty.
func ValidateSemanticAttributeControlSnapshot(snapshot SemanticAttributeControlSnapshot) error {
	if snapshot.State.Profile != semanticvalue.Profile || snapshot.State.Revision < 0 || !canonicalSemanticAttributeDigest(snapshot.State.Digest) {
		return fmt.Errorf("%w: control state identity is invalid", ErrSemanticAttributeControlCorrupt)
	}
	assignmentIDs := make(map[string]struct{}, len(snapshot.Assignments))
	for _, assignment := range snapshot.Assignments {
		if !canonicalSemanticAttributeIdentity(assignment.ID) || !canonicalSemanticAttributeIdentity(assignment.DefinitionID) ||
			assignment.DefinitionVersion <= 0 || assignment.AssignmentVersion <= 0 || !semanticAttributeTypeValid(assignment.Type) || !assignment.Shape.Valid() ||
			!canonicalSemanticAttributeDigest(assignment.ValueDigest) {
			return fmt.Errorf("%w: assignment %q identity or value metadata is invalid", ErrSemanticAttributeControlCorrupt, assignment.ID)
		}
		if err := semanticvalue.ValidateAttributeName(assignment.DefinitionName); err != nil {
			return fmt.Errorf("%w: assignment %q definition name is invalid", ErrSemanticAttributeControlCorrupt, assignment.ID)
		}
		if err := assignment.Subject.Validate(); err != nil {
			return fmt.Errorf("%w: assignment %q subject is invalid", ErrSemanticAttributeControlCorrupt, assignment.ID)
		}
		if _, duplicate := assignmentIDs[assignment.ID]; duplicate {
			return fmt.Errorf("%w: duplicate assignment id %q", ErrSemanticAttributeControlCorrupt, assignment.ID)
		}
		assignmentIDs[assignment.ID] = struct{}{}
		if assignment.Tombstoned != (assignment.TombstonedAt != "") {
			return fmt.Errorf("%w: assignment %q tombstone projection is inconsistent", ErrSemanticAttributeControlCorrupt, assignment.ID)
		}
		if err := validateSemanticAttributeCanonicalProjection(assignment.Type, assignment.Shape, assignment.CanonicalValues, assignment.ValueDigest); err != nil {
			return fmt.Errorf("%w: assignment %q values are invalid: %v", ErrSemanticAttributeControlCorrupt, assignment.ID, err)
		}
	}
	mappingIDs := make(map[string]struct{}, len(snapshot.Mappings))
	for _, mapping := range snapshot.Mappings {
		if !canonicalSemanticAttributeIdentity(mapping.ID) || !canonicalSemanticAttributeIdentity(mapping.DefinitionID) ||
			mapping.DefinitionVersion <= 0 || mapping.MappingVersion <= 0 || !semanticAttributeTypeValid(mapping.Type) || !mapping.Shape.Valid() ||
			!mapping.SourceKind.Valid() || !canonicalSemanticAttributeText(mapping.Claim) {
			return fmt.Errorf("%w: mapping %q identity or value metadata is invalid", ErrSemanticAttributeControlCorrupt, mapping.ID)
		}
		if err := semanticvalue.ValidateAttributeName(mapping.DefinitionName); err != nil {
			return fmt.Errorf("%w: mapping %q definition name is invalid", ErrSemanticAttributeControlCorrupt, mapping.ID)
		}
		if err := trustedclaims.ValidateSourceIdentity(mapping.Provider, mapping.Issuer, mapping.Audience); err != nil {
			return fmt.Errorf("%w: mapping %q source identity is invalid", ErrSemanticAttributeControlCorrupt, mapping.ID)
		}
		if _, duplicate := mappingIDs[mapping.ID]; duplicate {
			return fmt.Errorf("%w: duplicate mapping id %q", ErrSemanticAttributeControlCorrupt, mapping.ID)
		}
		mappingIDs[mapping.ID] = struct{}{}
		if mapping.Tombstoned != (mapping.TombstonedAt != "") {
			return fmt.Errorf("%w: mapping %q tombstone projection is inconsistent", ErrSemanticAttributeControlCorrupt, mapping.ID)
		}
	}
	digest, err := SemanticAttributeControlDigest(snapshot.Assignments, snapshot.Mappings)
	if err != nil || digest != snapshot.State.Digest {
		return fmt.Errorf("%w: control digest does not match contents", ErrSemanticAttributeControlCorrupt)
	}
	return nil
}

func semanticAttributeTypeValid(value semanticvalue.Type) bool {
	switch value {
	case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger, semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
		return true
	default:
		return false
	}
}

func canonicalSemanticAttributeDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == 32
}

func canonicalSemanticAttributeIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func canonicalSemanticAttributeText(value string) bool {
	return canonicalSemanticAttributeIdentity(value)
}

func validateSemanticAttributeCanonicalProjection(typeName semanticvalue.Type, shape SemanticAttributeShape, values []string, digest string) error {
	if len(values) == 0 || len(values) > semanticvalue.MaxSetValues || shape == SemanticAttributeScalar && len(values) != 1 {
		return fmt.Errorf("invalid cardinality")
	}
	inputs := make([]any, len(values))
	for index, value := range values {
		var input any
		switch typeName {
		case semanticvalue.TypeString, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
			input = value
		case semanticvalue.TypeBoolean:
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			input = parsed
		case semanticvalue.TypeInteger, semanticvalue.TypeDecimal:
			input = json.Number(value)
		default:
			return semanticvalue.ErrInvalidType
		}
		inputs[index] = input
	}
	definition := SemanticAttributeDefinition{Type: typeName, Shape: shape, Profile: semanticvalue.Profile,
		LifecycleState: SemanticAttributeActive, Enabled: true}
	var input any = inputs
	if shape == SemanticAttributeScalar {
		input = inputs[0]
	}
	canonical, computed, err := CanonicalSemanticAttributeValues(definition, input)
	if err != nil || computed != digest || !reflect.DeepEqual(canonical, values) {
		return fmt.Errorf("canonical values or digest mismatch")
	}
	return nil
}
