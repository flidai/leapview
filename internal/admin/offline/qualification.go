package offline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

// QualificationPoolArtifactsSchemaVersion identifies the transport envelope
// emitted by the hidden qualification command. The pool identity and evidence
// artifact each retain their own canonical validation and schema contracts.
const QualificationPoolArtifactsSchemaVersion = 1

// QualificationPoolArtifacts contains the non-secret pool contract and the
// complete physicalpool conformance artifact produced by local qualification.
// It is a transport value only: no admission or control-plane state is stored
// by this artifact export.
type QualificationPoolArtifacts struct {
	SchemaVersion int                           `json:"schema_version"`
	Pool          physicalpool.PoolIdentity     `json:"pool"`
	Evidence      physicalpool.EvidenceArtifact `json:"evidence"`
}

// MarshalQualificationPoolArtifacts validates and encodes one qualification
// transport envelope. Validation is deliberately repeated at this boundary so
// callers cannot accidentally emit fabricated or mismatched evidence.
func MarshalQualificationPoolArtifacts(artifacts QualificationPoolArtifacts) ([]byte, error) {
	if artifacts.SchemaVersion != QualificationPoolArtifactsSchemaVersion {
		return nil, fmt.Errorf("unsupported qualification artifact schema version %d", artifacts.SchemaVersion)
	}
	if err := artifacts.Pool.Validate(); err != nil {
		return nil, fmt.Errorf("qualification pool identity: %w", err)
	}
	if err := validateQualificationPoolDeliveryIdentity(artifacts.Pool); err != nil {
		return nil, err
	}
	if artifacts.Evidence.SchemaVersion != physicalpool.EvidenceArtifactSchemaVersion {
		return nil, fmt.Errorf("unsupported physical-pool evidence schema version %d", artifacts.Evidence.SchemaVersion)
	}
	if err := artifacts.Evidence.Evidence.Verify(); err != nil {
		return nil, fmt.Errorf("qualification conformance evidence: %w", err)
	}
	if !artifacts.Pool.Compatibility.Equal(artifacts.Evidence.Evidence.Compatibility) {
		return nil, fmt.Errorf("qualification pool identity and evidence compatibility differ")
	}
	return json.Marshal(artifacts)
}

// UnmarshalQualificationPoolArtifacts strictly decodes and validates one
// qualification transport envelope. Trailing JSON values and unknown fields
// are rejected before the owner writes separate mounted artifacts.
func UnmarshalQualificationPoolArtifacts(encoded []byte) (QualificationPoolArtifacts, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var artifacts QualificationPoolArtifacts
	if err := decoder.Decode(&artifacts); err != nil {
		return QualificationPoolArtifacts{}, fmt.Errorf("decode qualification artifacts: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return QualificationPoolArtifacts{}, fmt.Errorf("decode qualification artifacts: trailing JSON value")
		}
		return QualificationPoolArtifacts{}, fmt.Errorf("decode qualification artifacts: %w", err)
	}
	if artifacts.SchemaVersion != QualificationPoolArtifactsSchemaVersion {
		return QualificationPoolArtifacts{}, fmt.Errorf("unsupported qualification artifact schema version %d", artifacts.SchemaVersion)
	}
	if err := artifacts.Pool.Validate(); err != nil {
		return QualificationPoolArtifacts{}, fmt.Errorf("qualification pool identity: %w", err)
	}
	if err := validateQualificationPoolDeliveryIdentity(artifacts.Pool); err != nil {
		return QualificationPoolArtifacts{}, err
	}
	if artifacts.Evidence.SchemaVersion != physicalpool.EvidenceArtifactSchemaVersion {
		return QualificationPoolArtifacts{}, fmt.Errorf("unsupported physical-pool evidence schema version %d", artifacts.Evidence.SchemaVersion)
	}
	if err := artifacts.Evidence.Evidence.Verify(); err != nil {
		return QualificationPoolArtifacts{}, fmt.Errorf("qualification conformance evidence: %w", err)
	}
	if !artifacts.Pool.Compatibility.Equal(artifacts.Evidence.Evidence.Compatibility) {
		return QualificationPoolArtifacts{}, fmt.Errorf("qualification pool identity and evidence compatibility differ")
	}
	return artifacts, nil
}

// A qualification envelope is consumed by native delivery, whose immutable
// snapshot seals require explicit target ownership and locality. PoolIdentity
// permits these fields to be absent for non-delivery consumers, so enforce the
// stronger contract at this transport boundary before admission can persist an
// unusable target-owned pool.
func validateQualificationPoolDeliveryIdentity(identity physicalpool.PoolIdentity) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "tenant", value: identity.Tenant},
		{name: "region", value: identity.Region},
	} {
		if field.value == "" || field.value != strings.TrimSpace(field.value) {
			return fmt.Errorf("qualification pool identity: %s is required and must be canonical", field.name)
		}
	}
	return nil
}
