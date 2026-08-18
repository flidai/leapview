package filter

import (
	"fmt"
	"sort"
	"sync"
)

// Shared-link codecs are code-owned. A dashboard document can request a
// parameter name, but cannot pin a wire codec or protocol version. This keeps
// link compatibility independently versioned from the YAML contract.
type SharedLinkCodecStatus string

const (
	SharedLinkCodecActive     SharedLinkCodecStatus = "active"
	SharedLinkCodecDeprecated SharedLinkCodecStatus = "deprecated"
	SharedLinkCodecRetired    SharedLinkCodecStatus = "retired"
)

const SharedLinkProtocolVersion = 1

type SharedLinkCodec struct {
	Encoding URLEncoding
	Status   SharedLinkCodecStatus
	Encode   func(Expression, ValueKind) (string, error)
	Decode   func(string, ValueKind) (Expression, error)
}

type SharedLinkRegistry struct {
	mu     sync.RWMutex
	codecs map[URLEncoding]SharedLinkCodec
}

func NewSharedLinkRegistry() *SharedLinkRegistry {
	registry := &SharedLinkRegistry{codecs: map[URLEncoding]SharedLinkCodec{}}
	_ = registry.Register(SharedLinkCodec{Encoding: URLEncodingTypedV1, Status: SharedLinkCodecActive, Encode: EncodeTypedV1, Decode: DecodeTypedV1})
	return registry
}

func (registry *SharedLinkRegistry) Register(codec SharedLinkCodec) error {
	if registry == nil {
		return fmt.Errorf("shared-link registry is nil")
	}
	if codec.Encoding == "" || codec.Encode == nil || codec.Decode == nil {
		return fmt.Errorf("shared-link codec requires encoding, encoder and decoder")
	}
	if codec.Status == "" {
		codec.Status = SharedLinkCodecActive
	}
	if codec.Status != SharedLinkCodecActive && codec.Status != SharedLinkCodecDeprecated && codec.Status != SharedLinkCodecRetired {
		return fmt.Errorf("unsupported shared-link codec status %q", codec.Status)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.codecs[codec.Encoding]; exists {
		return fmt.Errorf("shared-link codec %q is already registered", codec.Encoding)
	}
	registry.codecs[codec.Encoding] = codec
	return nil
}

func (registry *SharedLinkRegistry) SetStatus(encoding URLEncoding, status SharedLinkCodecStatus) error {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	codec, ok := registry.codecs[encoding]
	if !ok {
		return fmt.Errorf("unknown shared-link codec %q", encoding)
	}
	if status != SharedLinkCodecActive && status != SharedLinkCodecDeprecated && status != SharedLinkCodecRetired {
		return fmt.Errorf("unsupported shared-link codec status %q", status)
	}
	codec.Status = status
	registry.codecs[encoding] = codec
	return nil
}

func (registry *SharedLinkRegistry) SupportedCodecs() []URLEncoding {
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make([]URLEncoding, 0, len(registry.codecs))
	for encoding, codec := range registry.codecs {
		if codec.Status != SharedLinkCodecRetired {
			result = append(result, encoding)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (registry *SharedLinkRegistry) Encode(encoding URLEncoding, expression Expression, kind ValueKind) (string, error) {
	codec, err := registry.codec(encoding, false)
	if err != nil {
		return "", err
	}
	return codec.Encode(expression, kind)
}

func (registry *SharedLinkRegistry) Decode(encoding URLEncoding, value string, kind ValueKind) (Expression, error) {
	codec, err := registry.codec(encoding, true)
	if err != nil {
		return Expression{}, err
	}
	return codec.Decode(value, kind)
}

// EncodeDefault and DecodeDefault are the protocol-owned lifecycle entry
// points.  Dashboard documents carry only a friendly parameter name; codec
// selection is deliberately resolved by this registry so a protocol rollout
// never requires rewriting or redeploying authored YAML.
func (registry *SharedLinkRegistry) EncodeDefault(expression Expression, kind ValueKind) (string, error) {
	if registry == nil {
		return "", fmt.Errorf("shared-link registry is nil")
	}
	for _, encoding := range registry.SupportedCodecs() {
		if codec, err := registry.codec(encoding, false); err == nil {
			return codec.Encode(expression, kind)
		}
	}
	return "", fmt.Errorf("shared-link registry has no active codec")
}

func (registry *SharedLinkRegistry) DecodeDefault(value string, kind ValueKind) (Expression, error) {
	if registry == nil {
		return Expression{}, fmt.Errorf("shared-link registry is nil")
	}
	// Existing links carry a protocol prefix; probe non-retired codecs in
	// deterministic registry order. Deprecated codecs remain readable for the
	// declared lifecycle while retired codecs are rejected.
	for _, encoding := range registry.SupportedCodecs() {
		if expression, err := registry.Decode(encoding, value, kind); err == nil {
			return expression, nil
		}
	}
	return Expression{}, fmt.Errorf("shared-link value is not decodable by the active protocol")
}

func (registry *SharedLinkRegistry) codec(encoding URLEncoding, decode bool) (SharedLinkCodec, error) {
	if registry == nil {
		return SharedLinkCodec{}, fmt.Errorf("shared-link registry is nil")
	}
	registry.mu.RLock()
	codec, ok := registry.codecs[encoding]
	registry.mu.RUnlock()
	if !ok {
		return SharedLinkCodec{}, fmt.Errorf("unsupported shared-link codec %q", encoding)
	}
	if codec.Status == SharedLinkCodecRetired || (!decode && codec.Status == SharedLinkCodecDeprecated) {
		return SharedLinkCodec{}, fmt.Errorf("shared-link codec %q is not supported for %s", encoding, map[bool]string{true: "decoding", false: "encoding"}[decode])
	}
	return codec, nil
}

var defaultSharedLinkRegistry = NewSharedLinkRegistry()

func DefaultSharedLinkRegistry() *SharedLinkRegistry { return defaultSharedLinkRegistry }

func EncodeSharedLink(expression Expression, kind ValueKind) (string, error) {
	return defaultSharedLinkRegistry.Encode(URLEncodingTypedV1, expression, kind)
}

func DecodeSharedLink(encoded string, kind ValueKind) (Expression, error) {
	return defaultSharedLinkRegistry.Decode(URLEncodingTypedV1, encoded, kind)
}
