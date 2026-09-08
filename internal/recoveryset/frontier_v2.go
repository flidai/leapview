package recoveryset

import (
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/recoveryset/observation"
	"github.com/flidai/leapview/pkg/strictjson"
)

// EvidenceSchemaVersion is opt-in, off-host qualification evidence. It is not
// accepted by the v1 SQL publication or startup contract.
const EvidenceSchemaVersion int32 = 2

// ManagedEvidence binds an independently retained observation descriptor and
// the exact marker attestation. Structural validation is not proof of capture
// provenance or retention: the observation store must verify those before it
// accepts a frontier for immutable persistence.
type ManagedEvidence struct {
	ManifestDigest   string               `json:"manifest_digest"`
	BoundaryDigest   string               `json:"boundary_digest"`
	DescriptorDigest string               `json:"descriptor_digest"`
	Boundary         observation.Boundary `json:"boundary"`
}

func (s RecoverySet) validateEvidenceVersion() error {
	switch s.SchemaVersion {
	case SchemaVersion:
		if s.ManagedEvidence != nil {
			return fmt.Errorf("%w: v1 forbids managed evidence", ErrInvalid)
		}
		return nil
	case EvidenceSchemaVersion:
		if s.Status != StatusPrepared || s.PublishedValidationAttemptID != "" {
			return fmt.Errorf("%w: v2 is qualification-only; activation is unsupported", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalid, s.SchemaVersion)
	}
	binding := s.ManagedEvidence
	if binding == nil {
		return fmt.Errorf("%w: v2 requires managed evidence", ErrInvalid)
	}
	for _, value := range []string{binding.ManifestDigest, binding.BoundaryDigest, binding.DescriptorDigest} {
		if err := digest(value, "managed evidence digest"); err != nil {
			return err
		}
	}
	want, err := binding.Boundary.Digest()
	if err != nil || want != binding.BoundaryDigest {
		return fmt.Errorf("%w: boundary attestation digest mismatch", ErrInvalid)
	}
	for _, point := range s.ClusterPoints {
		if point.DatabaseRole != DatabaseControl {
			continue
		}
		if point.ClusterIdentity != "postgres:"+binding.Boundary.SystemIdentity ||
			point.DatabaseIdentity != binding.Boundary.DatabaseIdentity ||
			point.RecoveryIdentity != binding.Boundary.RecoveryIdentity() {
			return fmt.Errorf("%w: control frontier differs from captured boundary", ErrInvalid)
		}
		return nil
	}
	return fmt.Errorf("%w: missing control boundary", ErrInvalid)
}

// ParseRecoverySet rejects unknown versions, duplicate keys and unsupported
// evidence instead of interpreting a future version as a v1 frontier. Existing
// v1 canonical bytes are unchanged. This is not a restore/admission API.
func ParseRecoverySet(raw []byte) (RecoverySet, error) {
	var set RecoverySet
	if err := strictjson.DecodeWithOptions(raw, &set, strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 32}); err != nil {
		return RecoverySet{}, fmt.Errorf("%w: malformed recovery frontier", ErrInvalid)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return RecoverySet{}, ErrInvalid
	}
	if set.SchemaVersion == SchemaVersion {
		if _, exists := fields["managed_evidence"]; exists {
			return RecoverySet{}, fmt.Errorf("%w: v1 forbids managed evidence fields", ErrInvalid)
		}
	}
	// Require the exact field spelling and presence emitted by this version.
	// encoding/json alone accepts case aliases and missing zero-valued fields.
	encoded, err := json.Marshal(set)
	if err != nil {
		return RecoverySet{}, ErrInvalid
	}
	var supplied, expected any
	if json.Unmarshal(raw, &supplied) != nil || json.Unmarshal(encoded, &expected) != nil || !sameJSONShape(supplied, expected) {
		return RecoverySet{}, fmt.Errorf("%w: frontier fields do not match the exact versioned schema", ErrInvalid)
	}
	return set.Normalize()
}

func sameJSONShape(supplied, expected any) bool {
	switch value := expected.(type) {
	case map[string]any:
		got, ok := supplied.(map[string]any)
		if !ok || len(got) != len(value) {
			return false
		}
		for key, child := range value {
			actual, exists := got[key]
			if !exists || !sameJSONShape(actual, child) {
				return false
			}
		}
	case []any:
		got, ok := supplied.([]any)
		if !ok || len(got) != len(value) {
			return false
		}
		for index, child := range value {
			if !sameJSONShape(got[index], child) {
				return false
			}
		}
	default:
		if (expected == nil) != (supplied == nil) {
			return false
		}
	}
	return true
}
