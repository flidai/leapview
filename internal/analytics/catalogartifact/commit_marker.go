package catalogartifact

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/pkg/strictjson"
)

const (
	// CommitMarkerSchemaVersion is the version of the persistent commit
	// identity marker written alongside a catalog snapshot.
	CommitMarkerSchemaVersion = 1

	// MaxCommitMarkerBytes bounds the encoded marker document. Markers are
	// identity evidence, not a general metadata channel.
	MaxCommitMarkerBytes = 4096
	// MaxCommitMarkerFieldBytes bounds each scalar identity in a marker.
	MaxCommitMarkerFieldBytes = 512
)

// CommitMarker is the durable identity written to catalog commit metadata.
// Fields are deliberately relational-style scalar identities rather than an
// unbounded metadata map.
type CommitMarker struct {
	SchemaVersion  int    `json:"schema_version"`
	DeliveryID     string `json:"delivery_id"`
	GenerationID   string `json:"generation_id"`
	AttemptID      string `json:"attempt_id"`
	LeaseEpoch     int64  `json:"lease_epoch"`
	FencingToken   string `json:"fencing_token,omitempty"`
	RequestDigest  string `json:"request_digest"`
	PlanDigest     string `json:"plan_digest"`
	Project        string `json:"project"`
	Environment    string `json:"environment"`
	PhysicalPoolID string `json:"physical_pool_id"`
}

// Normalize validates a marker without mutating the receiver.
func (m CommitMarker) Normalize() (CommitMarker, error) {
	for name, value := range map[string]string{
		"delivery_id": m.DeliveryID, "generation_id": m.GenerationID,
		"attempt_id": m.AttemptID, "request_digest": m.RequestDigest,
		"plan_digest": m.PlanDigest, "project": m.Project,
		"environment": m.Environment, "physical_pool_id": m.PhysicalPoolID,
	} {
		if err := validateMarkerValue(name, value); err != nil {
			return CommitMarker{}, err
		}
	}
	if m.LeaseEpoch <= 0 {
		return CommitMarker{}, errors.New("commit marker lease_epoch must be positive")
	}
	if err := validateDigest(m.PlanDigest); err != nil {
		return CommitMarker{}, err
	}
	if err := validateDigest(m.RequestDigest); err != nil {
		return CommitMarker{}, fmt.Errorf("commit marker request_digest: %w", err)
	}
	if strings.TrimSpace(m.FencingToken) != "" {
		if err := validateMarkerValue("fencing_token", m.FencingToken); err != nil {
			return CommitMarker{}, err
		}
	}
	if m.SchemaVersion != CommitMarkerSchemaVersion {
		return CommitMarker{}, fmt.Errorf("unsupported commit marker schema version %d", m.SchemaVersion)
	}
	return m, nil
}

func validateDigest(value string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return errors.New("commit marker plan_digest must be a sha256 digest")
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(value, prefix)); err != nil {
		return fmt.Errorf("commit marker plan_digest is not valid sha256: %w", err)
	}
	return nil
}

func validateMarkerValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("commit marker %s is required", name)
	}
	if value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("commit marker %s is not normalized", name)
	}
	if len(value) > MaxCommitMarkerFieldBytes {
		return fmt.Errorf("commit marker %s exceeds %d bytes", name, MaxCommitMarkerFieldBytes)
	}
	return nil
}

// CanonicalJSON returns the stable JSON representation used in catalog
// commit metadata. Struct field order is intentional and stable.
func (m CommitMarker) CanonicalJSON() (string, error) {
	normalized, err := m.Normalize()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal commit marker: %w", err)
	}
	if len(encoded) > MaxCommitMarkerBytes {
		return "", fmt.Errorf("commit marker is %d bytes, maximum is %d", len(encoded), MaxCommitMarkerBytes)
	}
	return string(encoded), nil
}

// ParseCommitMarker validates and normalizes a canonical marker read from
// catalog commit metadata. Unknown fields and trailing data are rejected.
func ParseCommitMarker(raw string) (CommitMarker, error) {
	if strings.TrimSpace(raw) == "" {
		return CommitMarker{}, errors.New("commit marker is empty")
	}
	marker, err := DecodeCommitMarker([]byte(raw))
	if err != nil {
		return CommitMarker{}, err
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return CommitMarker{}, err
	}
	if canonical != raw {
		return CommitMarker{}, errors.New("commit marker is not canonical JSON")
	}
	return marker, nil
}

// DecodeCommitMarker strictly decodes and validates one marker document. It
// accepts non-canonical key ordering so PostgreSQL jsonb values can be
// compared by normalized identity; callers requiring canonical bytes should
// compare the result of CanonicalJSON with the input.
func DecodeCommitMarker(raw []byte) (CommitMarker, error) {
	if len(raw) == 0 {
		return CommitMarker{}, errors.New("commit marker is empty")
	}
	if len(raw) > MaxCommitMarkerBytes {
		return CommitMarker{}, fmt.Errorf("commit marker exceeds %d bytes", MaxCommitMarkerBytes)
	}
	var marker CommitMarker
	if err := strictjson.DecodeWithOptions(raw, &marker, strictjson.Options{MaxBytes: MaxCommitMarkerBytes}); err != nil {
		return CommitMarker{}, fmt.Errorf("decode commit marker: %w", err)
	}
	return marker.Normalize()
}
