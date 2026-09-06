package access

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticAttributeRegistryDigestWire struct {
	Profile     string                                  `json:"profile"`
	Definitions []semanticAttributeDefinitionDigestWire `json:"definitions"`
}

type semanticAttributeDefinitionDigestWire struct {
	ID                string                          `json:"id"`
	Name              string                          `json:"name"`
	Type              semanticvalue.Type              `json:"type"`
	Shape             SemanticAttributeShape          `json:"shape"`
	DefinitionVersion int64                           `json:"definitionVersion"`
	OwnerKind         SemanticAttributeOwnerKind      `json:"ownerKind"`
	OwnerID           string                          `json:"ownerId"`
	DisplayName       string                          `json:"displayName"`
	Description       string                          `json:"description"`
	DocumentationURL  string                          `json:"documentationUrl"`
	LifecycleState    SemanticAttributeLifecycleState `json:"lifecycleState"`
}

// SemanticAttributeRegistryDigest is the canonical FAI-636 registry identity.
// Readers and consumers use the same function so a syntactically valid but
// mixed registry snapshot cannot cross the semantic authorization boundary.
func SemanticAttributeRegistryDigest(profile string, definitions []SemanticAttributeDefinition) (string, error) {
	definitions = append([]SemanticAttributeDefinition(nil), definitions...)
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].Name != definitions[j].Name {
			return definitions[i].Name < definitions[j].Name
		}
		return definitions[i].ID < definitions[j].ID
	})
	wire := semanticAttributeRegistryDigestWire{Profile: profile, Definitions: make([]semanticAttributeDefinitionDigestWire, len(definitions))}
	for index, definition := range definitions {
		wire.Definitions[index] = semanticAttributeDefinitionDigestWire{ID: definition.ID, Name: definition.Name, Type: definition.Type,
			Shape: definition.Shape, DefinitionVersion: definition.DefinitionVersion, OwnerKind: definition.Metadata.Owner.Kind,
			OwnerID: definition.Metadata.Owner.ID, DisplayName: definition.Metadata.DisplayName, Description: definition.Metadata.Description,
			DocumentationURL: definition.Metadata.DocumentationURL, LifecycleState: definition.LifecycleState}
	}
	return semanticAttributeDigestJSON(wire, "registry")
}

type semanticAttributeAssignmentDigestWire struct {
	ID, DefinitionID, SubjectKind, SubjectID string
	DefinitionVersion                        int64
	Type                                     semanticvalue.Type
	Shape                                    SemanticAttributeShape
	Values                                   []string
	ValueDigest                              string
	Version                                  int64
	TombstonedAtMicros                       int64
}

type semanticAttributeMappingDigestWire struct {
	ID, SourceKind, Provider, Issuer, Audience, Claim, DefinitionID string
	DefinitionVersion                                               int64
	Type                                                            semanticvalue.Type
	Shape                                                           SemanticAttributeShape
	Version                                                         int64
	TombstonedAtMicros                                              int64
}

type semanticAttributeControlDigestWire struct {
	Profile     string                                  `json:"profile"`
	Assignments []semanticAttributeAssignmentDigestWire `json:"assignments"`
	Mappings    []semanticAttributeMappingDigestWire    `json:"mappings"`
}

// SemanticAttributeControlDigest is the canonical FAI-637 assignment and
// trusted-mapping identity. Input order never affects the result.
func SemanticAttributeControlDigest(assignments []SemanticAttributeAssignment, mappings []TrustedClaimMapping) (string, error) {
	assignments = append([]SemanticAttributeAssignment(nil), assignments...)
	mappings = append([]TrustedClaimMapping(nil), mappings...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].ID < assignments[j].ID })
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].ID < mappings[j].ID })
	wire := semanticAttributeControlDigestWire{Profile: semanticvalue.Profile,
		Assignments: make([]semanticAttributeAssignmentDigestWire, len(assignments)), Mappings: make([]semanticAttributeMappingDigestWire, len(mappings))}
	for index, row := range assignments {
		tombstonedAt, err := semanticAttributeTimestampMicroseconds(row.TombstonedAt)
		if err != nil {
			return "", fmt.Errorf("assignment %s tombstoned timestamp: %w", row.ID, err)
		}
		wire.Assignments[index] = semanticAttributeAssignmentDigestWire{ID: row.ID, DefinitionID: row.DefinitionID,
			SubjectKind: string(row.Subject.Kind), SubjectID: row.Subject.ID, DefinitionVersion: row.DefinitionVersion,
			Type: row.Type, Shape: row.Shape, Values: append([]string(nil), row.CanonicalValues...), ValueDigest: row.ValueDigest,
			Version: row.AssignmentVersion, TombstonedAtMicros: tombstonedAt}
	}
	for index, row := range mappings {
		tombstonedAt, err := semanticAttributeTimestampMicroseconds(row.TombstonedAt)
		if err != nil {
			return "", fmt.Errorf("mapping %s tombstoned timestamp: %w", row.ID, err)
		}
		wire.Mappings[index] = semanticAttributeMappingDigestWire{ID: row.ID, SourceKind: string(row.SourceKind), Provider: row.Provider,
			Issuer: row.Issuer, Audience: row.Audience, Claim: row.Claim, DefinitionID: row.DefinitionID,
			DefinitionVersion: row.DefinitionVersion, Type: row.Type, Shape: row.Shape, Version: row.MappingVersion,
			TombstonedAtMicros: tombstonedAt}
	}
	return semanticAttributeDigestJSON(wire, "control")
}

func semanticAttributeTimestampMicroseconds(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if micros, err := strconv.ParseInt(value, 10, 64); err == nil {
		return micros, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp %q: %w", value, err)
	}
	return parsed.UTC().UnixMicro(), nil
}

func semanticAttributeDigestJSON(value any, label string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode semantic attribute %s digest: %w", label, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
