package access

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/semanticvalue"
)

// SemanticDecisionAuditAction identifies the retained, semantic-access
// decision evidence envelope. The envelope is deliberately owned by Access:
// query execution supplies only already-authorized, immutable observations.
const SemanticDecisionAuditAction = "semantic_access.evaluated"

var (
	// ErrInvalidSemanticDecisionEvidence identifies malformed or unsafe
	// retained semantic evidence at an Access validation boundary.
	ErrInvalidSemanticDecisionEvidence = errors.New("invalid semantic decision audit evidence")
	errInvalidSemanticDecisionEvidence = ErrInvalidSemanticDecisionEvidence
)

// SemanticDecisionEvidence is the redacted semantic authorization decision
// retained alongside a CanonicalAuditEvent. It contains identities and
// digests, never effective attribute values or SQL predicates.
type SemanticDecisionEvidence struct {
	Version             int                       `json:"version"`
	InstanceID          string                    `json:"instanceId"`
	ActorPrincipalID    string                    `json:"actorPrincipalId"`
	SemanticModelDigest string                    `json:"semanticModelDigest"`
	Registry            SemanticAuditRevision     `json:"registry"`
	Control             SemanticAuditRevision     `json:"control"`
	Attributes          []SemanticAuditAttribute  `json:"attributes"`
	Target              SemanticAuditTarget       `json:"target"`
	Grants              []SemanticAuditGrant      `json:"grants"`
	Filters             []SemanticAuditFilter     `json:"filters"`
	Allowed             bool                      `json:"allowed"`
	Reason              string                    `json:"reason"`
}

type SemanticAuditRevision struct {
	Profile  string `json:"profile"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type SemanticAuditAttribute struct {
	DefinitionID      string `json:"definitionId"`
	DefinitionName    string `json:"definitionName"`
	DefinitionVersion int64  `json:"definitionVersion"`
	Type              string `json:"type"`
	Shape             string `json:"shape"`
	Source            string `json:"source"`
	ValueDigest       string `json:"valueDigest"`
}

type SemanticAuditTarget struct {
	Dataset   string `json:"dataset"`
	Dimension string `json:"dimension"`
	Metric    string `json:"metric"`
}

type SemanticAuditGrant struct {
	Grant                      string `json:"grant"`
	UserAttribute              string `json:"userAttribute"`
	AttributeDefinitionID      string `json:"attributeDefinitionId"`
	AttributeDefinitionVersion int64  `json:"attributeDefinitionVersion"`
	Satisfied                  bool   `json:"satisfied"`
}

type SemanticAuditFilter struct {
	Dataset                    string `json:"dataset"`
	Dimension                  string `json:"dimension"`
	UserAttribute              string `json:"userAttribute"`
	Identity                   string `json:"identity"`
	AttributeDefinitionID      string `json:"attributeDefinitionId"`
	AttributeDefinitionVersion int64  `json:"attributeDefinitionVersion"`
	Applied                    bool   `json:"applied"`
}

// ValidateBinding validates the immutable serving/registry portion before a
// consumer has its first decision. Target and decision fields are checked by
// Validate when an observation is appended.
func (e SemanticDecisionEvidence) ValidateBinding() error {
	return e.validate(false)
}

// Validate validates the complete evidence envelope, including the closed
// reason vocabulary and deterministic array order.
func (e SemanticDecisionEvidence) Validate() error {
	return e.validate(true)
}

func (e SemanticDecisionEvidence) validate(includeDecision bool) error {
	if e.Version != 1 {
		return fmt.Errorf("%w: version must be 1", errInvalidSemanticDecisionEvidence)
	}
	if err := validateSemanticAuditText("instanceId", e.InstanceID); err != nil {
		return err
	}
	if err := validateSemanticAuditText("actorPrincipalId", e.ActorPrincipalID); err != nil {
		return err
	}
	if err := validateSemanticAuditDigest("semanticModelDigest", e.SemanticModelDigest); err != nil {
		return err
	}
	if err := e.Registry.validate("registry"); err != nil {
		return err
	}
	if err := e.Control.validate("control"); err != nil {
		return err
	}
	for index, attribute := range e.Attributes {
		if index > 0 && e.Attributes[index-1].DefinitionID >= attribute.DefinitionID {
			return fmt.Errorf("%w: attributes must be strictly ordered by definitionId", errInvalidSemanticDecisionEvidence)
		}
		if err := attribute.validate(); err != nil {
			return fmt.Errorf("%w: attribute %d: %w", errInvalidSemanticDecisionEvidence, index, err)
		}
	}
	for index, grant := range e.Grants {
		if index > 0 && e.Grants[index-1].Grant >= grant.Grant {
			return fmt.Errorf("%w: grants must be strictly ordered by grant", errInvalidSemanticDecisionEvidence)
		}
		if err := grant.validate(); err != nil {
			return fmt.Errorf("%w: grant %d: %w", errInvalidSemanticDecisionEvidence, index, err)
		}
	}
	for index, filter := range e.Filters {
		if index > 0 && e.Filters[index-1].Identity >= filter.Identity {
			return fmt.Errorf("%w: filters must be strictly ordered by identity", errInvalidSemanticDecisionEvidence)
		}
		if err := filter.validate(); err != nil {
			return fmt.Errorf("%w: filter %d: %w", errInvalidSemanticDecisionEvidence, index, err)
		}
	}
	if !includeDecision {
		return nil
	}
	if err := e.Target.validate(); err != nil {
		return err
	}
	if e.Allowed {
		if e.Reason != "" {
			return fmt.Errorf("%w: allowed decision must have an empty reason", errInvalidSemanticDecisionEvidence)
		}
	} else if !semanticDecisionReason(e.Reason) {
		return fmt.Errorf("%w: reason %q is not an allowed decision reason", errInvalidSemanticDecisionEvidence, e.Reason)
	}
	return nil
}

func (revision SemanticAuditRevision) validate(name string) error {
	if revision.Profile != semanticvalue.Profile {
		return fmt.Errorf("%w: %s profile is invalid", errInvalidSemanticDecisionEvidence, name)
	}
	if revision.Revision <= 0 {
		return fmt.Errorf("%w: %s revision is invalid", errInvalidSemanticDecisionEvidence, name)
	}
	return validateSemanticAuditDigest(name+".digest", revision.Digest)
}

func (attribute SemanticAuditAttribute) validate() error {
	if err := validateSemanticAuditText("definitionId", attribute.DefinitionID); err != nil {
		return err
	}
	if err := semanticvalue.ValidateAttributeName(attribute.DefinitionName); err != nil {
		return fmt.Errorf("definitionName: %w", err)
	}
	if attribute.DefinitionVersion <= 0 {
		return errors.New("definitionVersion must be positive")
	}
	switch semanticvalue.Type(attribute.Type) {
	case semanticvalue.TypeString, semanticvalue.TypeBoolean, semanticvalue.TypeInteger,
		semanticvalue.TypeDecimal, semanticvalue.TypeDate, semanticvalue.TypeTimestamp:
	default:
		return fmt.Errorf("type %q is invalid", attribute.Type)
	}
	if attribute.Shape != "scalar" && attribute.Shape != "list" {
		return fmt.Errorf("shape %q is invalid", attribute.Shape)
	}
	if attribute.Source != "direct" && attribute.Source != "trusted_claim" && attribute.Source != "direct+trusted_claim" {
		return fmt.Errorf("source %q is invalid", attribute.Source)
	}
	return validateSemanticAuditDigest("valueDigest", attribute.ValueDigest)
}

func (target SemanticAuditTarget) validate() error {
	if target.Metric != "" {
		if target.Dataset != "" || target.Dimension != "" {
			return fmt.Errorf("%w: metric target cannot include dataset or dimension", errInvalidSemanticDecisionEvidence)
		}
		return validateSemanticAuditText("metric", target.Metric)
	}
	if target.Dataset == "" {
		return fmt.Errorf("%w: dataset target is required", errInvalidSemanticDecisionEvidence)
	}
	if err := validateSemanticAuditText("dataset", target.Dataset); err != nil {
		return err
	}
	if target.Dimension != "" {
		return validateSemanticAuditText("dimension", target.Dimension)
	}
	return nil
}

func (grant SemanticAuditGrant) validate() error {
	for name, value := range map[string]string{"grant": grant.Grant, "userAttribute": grant.UserAttribute, "attributeDefinitionId": grant.AttributeDefinitionID} {
		if err := validateSemanticAuditText(name, value); err != nil {
			return err
		}
	}
	if grant.AttributeDefinitionVersion <= 0 {
		return errors.New("attributeDefinitionVersion must be positive")
	}
	return nil
}

func (filter SemanticAuditFilter) validate() error {
	for name, value := range map[string]string{
		"dataset": filter.Dataset, "dimension": filter.Dimension, "userAttribute": filter.UserAttribute,
		"identity": filter.Identity, "attributeDefinitionId": filter.AttributeDefinitionID,
	} {
		if err := validateSemanticAuditText(name, value); err != nil {
			return err
		}
	}
	if filter.AttributeDefinitionVersion <= 0 {
		return errors.New("attributeDefinitionVersion must be positive")
	}
	return nil
}

func validateSemanticAuditText(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is required and must be trimmed", errInvalidSemanticDecisionEvidence, name)
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return fmt.Errorf("%w: %s contains whitespace or control characters", errInvalidSemanticDecisionEvidence, name)
		}
	}
	return nil
}

func validateSemanticAuditDigest(name, value string) error {
	if err := platformdigest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("%w: %s: %v", errInvalidSemanticDecisionEvidence, name, err)
	}
	return nil
}

// semanticDecisionReason is intentionally closed. These values are stable,
// non-sensitive evaluator classifications; evaluator errors use the final
// value rather than copying implementation details into retained evidence.
func semanticDecisionReason(reason string) bool {
	switch reason {
	case "required access grant is not satisfied",
		"required access filter attribute is unavailable",
		"required access filter attribute is invalid",
		"semantic attribute registry state does not match compiled policy",
		"semantic attribute control state is unavailable",
		"semantic attribute control state is invalid",
		"effective semantic attribute is unknown",
		"effective semantic attribute identity is invalid",
		"effective semantic attribute definition is stale",
		"effective semantic attribute type or lifecycle is invalid",
		"effective semantic attribute source is untrusted",
		"effective semantic attribute values are invalid",
		"effective semantic attribute digest is invalid",
		"effective semantic attribute source is conflicting",
		"semantic access evaluation failed":
		return true
	default:
		return false
	}
}

// MetadataJSON returns the only representation accepted by the canonical
// audit event. Struct marshaling emits every field; the generic canonicalizer
// then fixes object-key ordering without introducing another digest.
func (e SemanticDecisionEvidence) MetadataJSON() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	if e.Attributes == nil {
		e.Attributes = []SemanticAuditAttribute{}
	}
	if e.Grants == nil {
		e.Grants = []SemanticAuditGrant{}
	}
	if e.Filters == nil {
		e.Filters = []SemanticAuditFilter{}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("%w: encode: %v", errInvalidSemanticDecisionEvidence, err)
	}
	return canonicalSemanticDecisionJSON(raw)
}

// DecodeSemanticDecisionEvidence strictly decodes a retained metadata object.
// Unknown, missing, duplicate, null, and trailing fields are rejected before
// validation so replay cannot silently reinterpret a newer schema.
func DecodeSemanticDecisionEvidence(raw string) (SemanticDecisionEvidence, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return SemanticDecisionEvidence{}, fmt.Errorf("%w: metadata is empty", errInvalidSemanticDecisionEvidence)
	}
	if err := rejectDuplicateCanonicalJSONKeys([]byte(trimmed)); err != nil {
		return SemanticDecisionEvidence{}, fmt.Errorf("%w: %v", errInvalidSemanticDecisionEvidence, err)
	}
	if err := validateSemanticDecisionWireSchema([]byte(trimmed)); err != nil {
		return SemanticDecisionEvidence{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	var evidence SemanticDecisionEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return SemanticDecisionEvidence{}, fmt.Errorf("%w: decode: %v", errInvalidSemanticDecisionEvidence, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return SemanticDecisionEvidence{}, fmt.Errorf("%w: trailing JSON value", errInvalidSemanticDecisionEvidence)
	}
	if err := evidence.Validate(); err != nil {
		return SemanticDecisionEvidence{}, err
	}
	return evidence, nil
}

var semanticDecisionEvidenceFields = []string{"version", "instanceId", "actorPrincipalId", "semanticModelDigest", "registry", "control", "attributes", "target", "grants", "filters", "allowed", "reason"}

var (
	semanticDecisionRevisionFields = []string{"profile", "revision", "digest"}
	semanticDecisionAttributeFields = []string{"definitionId", "definitionName", "definitionVersion", "type", "shape", "source", "valueDigest"}
	semanticDecisionTargetFields = []string{"dataset", "dimension", "metric"}
	semanticDecisionGrantFields = []string{"grant", "userAttribute", "attributeDefinitionId", "attributeDefinitionVersion", "satisfied"}
	semanticDecisionFilterFields = []string{"dataset", "dimension", "userAttribute", "identity", "attributeDefinitionId", "attributeDefinitionVersion", "applied"}
)

func canonicalSemanticDecisionJSON(raw []byte) (string, error) {
	// CanonicalAuditEvent is the sole canonical metadata authority. Keep this
	// DTO-specific schema validation layered above it rather than introducing a
	// second JSON canonicalization implementation (and therefore a second hash
	// domain by accident).
	return (CanonicalAuditEvent{MetadataJSON: string(raw)}).CanonicalMetadataJSON()
}

func requireSemanticDecisionObject(raw []byte, fields []string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return fmt.Errorf("%w: metadata must be an object", errInvalidSemanticDecisionEvidence)
	}
	if len(object) != len(fields) {
		return fmt.Errorf("%w: metadata schema fields are incomplete or unknown", errInvalidSemanticDecisionEvidence)
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok {
			return fmt.Errorf("%w: metadata field %q is required", errInvalidSemanticDecisionEvidence, field)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: metadata field %q cannot be null", errInvalidSemanticDecisionEvidence, field)
		}
	}
	return nil
}

func validateSemanticDecisionWireSchema(raw []byte) error {
	if err := requireSemanticDecisionObject(raw, semanticDecisionEvidenceFields); err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("%w: metadata object is invalid", errInvalidSemanticDecisionEvidence)
	}
	for _, field := range []string{"registry", "control", "target"} {
		if err := requireSemanticDecisionObject(root[field], map[string][]string{
			"registry": semanticDecisionRevisionFields,
			"control":  semanticDecisionRevisionFields,
			"target":   semanticDecisionTargetFields,
		}[field]); err != nil {
			return err
		}
	}
	for _, field := range []string{"attributes", "grants", "filters"} {
		var values []json.RawMessage
		if err := json.Unmarshal(root[field], &values); err != nil || values == nil {
			return fmt.Errorf("%w: metadata field %q must be an array", errInvalidSemanticDecisionEvidence, field)
		}
		var fields []string
		switch field {
		case "attributes":
			fields = semanticDecisionAttributeFields
		case "grants":
			fields = semanticDecisionGrantFields
		case "filters":
			fields = semanticDecisionFilterFields
		}
		for _, value := range values {
			if err := requireSemanticDecisionObject(value, fields); err != nil {
				return err
			}
		}
	}
	return nil
}
