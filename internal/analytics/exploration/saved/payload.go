package saved

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/pkg/strictjson"
)

const (
	// ExplorationSpecVersion is the durable envelope version. It is separate
	// from ExplorationSpec.SchemaVersion so the saved-resource boundary can
	// evolve without changing the renderer-neutral authored contract.
	ExplorationSpecVersion uint32 = 1

	// MaxSpecPayloadBytes bounds the complete canonical payload retained by a
	// saved exploration. Query results, compiled plans, and UI state are not
	// valid payloads and therefore cannot consume this budget.
	MaxSpecPayloadBytes = 256 << 10

	specDigestDomain = "flid.savedexploration.spec.v1"
)

// ExplorationSpecPayload is an immutable, versioned canonical ExplorationSpec
// boundary. Its representation is intentionally opaque: persistence adapters
// can store Canonical, while callers cannot replace the bytes without going
// through strict decoding and validation again.
type ExplorationSpecPayload struct {
	version   uint32
	canonical []byte
	digest    string
}

// SpecPayload is the shorter name used by repository contracts.
type SpecPayload = ExplorationSpecPayload

// NewExplorationSpecPayload validates and canonically serializes one authored
// exploration specification. The input is copied by JSON serialization.
func NewExplorationSpecPayload(spec canonical.ExplorationSpec) (ExplorationSpecPayload, error) {
	if err := canonical.ValidateShape(&spec); err != nil {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: encode exploration spec: %v", ErrInvalidPayload, err)
	}
	return newPayload(ExplorationSpecVersion, specJSON)
}

// NewCanonicalExplorationSpecPayload validates a raw authored specification
// and stores its canonical form. Whitespace and equivalent JSON formatting are
// normalized; unknown fields, trailing values, and invalid discriminated
// variants are rejected.
func NewCanonicalExplorationSpecPayload(version uint32, specJSON []byte) (ExplorationSpecPayload, error) {
	if version != ExplorationSpecVersion {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: exploration spec payload version %d", ErrUnsupportedVersion, version)
	}
	spec, err := decodeSpec(specJSON)
	if err != nil {
		return ExplorationSpecPayload{}, err
	}
	canonicalJSON, err := json.Marshal(spec)
	if err != nil {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: encode exploration spec: %v", ErrInvalidPayload, err)
	}
	return newPayload(version, canonicalJSON)
}

// ParseExplorationSpecPayload is an alias for the raw-spec constructor useful
// at persistence and transport boundaries.
func ParseExplorationSpecPayload(version uint32, specJSON []byte) (ExplorationSpecPayload, error) {
	return NewCanonicalExplorationSpecPayload(version, specJSON)
}

// DecodeExplorationSpecPayload decodes the complete versioned canonical
// envelope emitted by MarshalJSON or Canonical.
func DecodeExplorationSpecPayload(data []byte) (ExplorationSpecPayload, error) {
	if len(data) > MaxSpecPayloadBytes {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(data), MaxSpecPayloadBytes)
	}
	var wire payloadWire
	if err := decodeStrict(data, &wire); err != nil {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: decode envelope: %v", ErrInvalidPayload, err)
	}
	if wire.Version != ExplorationSpecVersion {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: exploration spec payload version %d", ErrUnsupportedVersion, wire.Version)
	}
	if len(wire.Spec) == 0 || bytes.Equal(bytes.TrimSpace(wire.Spec), []byte("null")) {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: spec is required", ErrInvalidPayload)
	}
	return NewCanonicalExplorationSpecPayload(wire.Version, wire.Spec)
}

// Version returns the envelope version, or zero for an unavailable payload.
func (p ExplorationSpecPayload) Version() uint32 {
	if !p.Available() {
		return 0
	}
	return p.version
}

// Available reports whether the payload contains a complete validated value.
func (p ExplorationSpecPayload) Available() bool {
	return p.version == ExplorationSpecVersion && len(p.canonical) > 0 && p.digest != ""
}

// Canonical returns a defensive copy of the complete versioned envelope.
func (p ExplorationSpecPayload) Canonical() []byte {
	return append([]byte(nil), p.canonical...)
}

// CanonicalJSON is a descriptive alias for Canonical.
func (p ExplorationSpecPayload) CanonicalJSON() []byte { return p.Canonical() }

// ContentHash returns the domain-separated SHA-256 identity of the complete
// versioned envelope, or an empty string for an unavailable value.
func (p ExplorationSpecPayload) ContentHash() string { return p.digest }

// Digest is an alias for ContentHash used by revision-token consumers.
func (p ExplorationSpecPayload) Digest() string { return p.ContentHash() }

// Spec decodes a defensive authored ExplorationSpec copy from the payload.
func (p ExplorationSpecPayload) Spec() (canonical.ExplorationSpec, error) {
	if !p.Available() {
		return canonical.ExplorationSpec{}, fmt.Errorf("%w: exploration spec payload is unavailable", ErrInvalidPayload)
	}
	var wire payloadWire
	if err := decodeStrict(p.canonical, &wire); err != nil {
		return canonical.ExplorationSpec{}, fmt.Errorf("%w: decode envelope: %v", ErrInvalidPayload, err)
	}
	spec, err := decodeSpec(wire.Spec)
	if err != nil {
		return canonical.ExplorationSpec{}, err
	}
	return spec, nil
}

// Validate verifies the opaque value's canonical envelope and digest. It is
// used by aggregate and repository boundaries before a value is persisted.
func (p ExplorationSpecPayload) Validate() error {
	if !p.Available() {
		return fmt.Errorf("%w: exploration spec payload is unavailable", ErrInvalidPayload)
	}
	if len(p.canonical) > MaxSpecPayloadBytes {
		return fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(p.canonical), MaxSpecPayloadBytes)
	}
	decoded, err := DecodeExplorationSpecPayload(p.canonical)
	if err != nil {
		return err
	}
	if !bytes.Equal(decoded.canonical, p.canonical) || decoded.digest != p.digest {
		return fmt.Errorf("%w: payload identity is not canonical", ErrInvalidPayload)
	}
	return nil
}

// Clone returns an independent immutable value. Accessors already protect the
// internal bytes; Clone is useful when copying enclosing aggregates.
func (p ExplorationSpecPayload) Clone() ExplorationSpecPayload {
	return ExplorationSpecPayload{version: p.version, canonical: p.Canonical(), digest: p.digest}
}

func (p ExplorationSpecPayload) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p.Canonical(), nil
}

func (p *ExplorationSpecPayload) UnmarshalJSON(data []byte) error {
	if p == nil {
		return fmt.Errorf("%w: nil exploration spec payload", ErrInvalidPayload)
	}
	decoded, err := DecodeExplorationSpecPayload(data)
	if err != nil {
		return err
	}
	*p = decoded
	return nil
}

type payloadWire struct {
	Version uint32          `json:"version"`
	Spec    json.RawMessage `json:"spec"`
}

func newPayload(version uint32, specJSON []byte) (ExplorationSpecPayload, error) {
	if version != ExplorationSpecVersion {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: exploration spec payload version %d", ErrUnsupportedVersion, version)
	}
	wire := payloadWire{Version: version, Spec: append(json.RawMessage(nil), specJSON...)}
	canonicalJSON, err := json.Marshal(wire)
	if err != nil {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: encode envelope: %v", ErrInvalidPayload, err)
	}
	if len(canonicalJSON) > MaxSpecPayloadBytes {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(canonicalJSON), MaxSpecPayloadBytes)
	}
	preimage := make([]byte, 0, len(specDigestDomain)+1+len(canonicalJSON))
	preimage = append(preimage, specDigestDomain...)
	preimage = append(preimage, 0)
	preimage = append(preimage, canonicalJSON...)
	digest := sha256.Sum256(preimage)
	return ExplorationSpecPayload{version: version, canonical: canonicalJSON, digest: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func decodeSpec(data []byte) (canonical.ExplorationSpec, error) {
	if len(data) > MaxSpecPayloadBytes {
		return canonical.ExplorationSpec{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(data), MaxSpecPayloadBytes)
	}
	var spec canonical.ExplorationSpec
	if err := decodeStrict(data, &spec); err != nil {
		return canonical.ExplorationSpec{}, fmt.Errorf("%w: decode exploration spec: %v", ErrInvalidPayload, err)
	}
	if err := canonical.ValidateShape(&spec); err != nil {
		return canonical.ExplorationSpec{}, fmt.Errorf("%w: %v", ErrInvalidPayload, err)
	}
	return spec, nil
}

func decodeStrict(data []byte, target any) error {
	return strictjson.DecodeWithOptions(data, target, strictjson.Options{
		MaxBytes: MaxSpecPayloadBytes,
		MaxDepth: 32,
	})
}
