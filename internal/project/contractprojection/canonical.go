package contractprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

// CanonicalBytes serializes only sealed leapview.contract/v1 projections. It
// is intentionally not a generic canonical JSON helper.
func CanonicalBytes(value Projection) ([]byte, error) {
	if value == nil || reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil() {
		return nil, errors.New("canonicalize contract projection: nil projection")
	}
	if err := validateProjectionEnvelope(value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal contract projection: %w", err)
	}
	normalized, err := normalizeJSONStrings(encoded)
	if err != nil {
		return nil, fmt.Errorf("normalize contract projection: %w", err)
	}
	canonical, err := canonicalizeRFC8785(normalized)
	if err != nil {
		return nil, fmt.Errorf("RFC 8785 canonicalize contract projection: %w", err)
	}
	return canonical, nil
}

func validateProjectionEnvelope(value Projection) error {
	var profile, apiVersion, kind, expectedKind string
	switch typed := value.(type) {
	case Source:
		profile, apiVersion, kind = typed.Profile, typed.APIVersion, typed.Kind
		expectedKind = "Source"
	case Model:
		profile, apiVersion, kind = typed.Profile, typed.APIVersion, typed.Kind
		expectedKind = "Model"
	case SemanticModel:
		profile, apiVersion, kind = typed.Profile, typed.APIVersion, typed.Kind
		expectedKind = "SemanticModel"
	default:
		return fmt.Errorf("canonicalize contract projection: unsupported projection %T", value)
	}
	if profile != Profile || apiVersion != "leapview.dev/v1" {
		return fmt.Errorf("canonicalize contract projection: invalid envelope profile=%q apiVersion=%q", profile, apiVersion)
	}
	if kind != expectedKind {
		return fmt.Errorf("canonicalize contract projection: invalid kind %q for %T", kind, value)
	}
	return nil
}

func canonicalizeRFC8785(encoded []byte) ([]byte, error) {
	return jsoncanonicalizer.Transform(encoded)
}

// Digest identifies the exact RFC 8785 bytes of a contract projection. This
// identity is separate from all existing graph, artifact, and release digests.
func Digest(value Projection) (string, error) {
	canonical, err := CanonicalBytes(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeJSONStrings(encoded []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func normalizeJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case string:
		return canonicalText(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			canonicalKey, err := canonicalText(key)
			if err != nil {
				return nil, err
			}
			if _, exists := result[canonicalKey]; exists {
				return nil, fmt.Errorf("map keys %q collide after Unicode NFC normalization", canonicalKey)
			}
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[canonicalKey] = normalized
		}
		return result, nil
	case json.Number:
		text := string(typed)
		if strings.ContainsAny(text, ".eE") {
			return nil, fmt.Errorf("approximate JSON number %q is not permitted in contract projections", text)
		}
		integer, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid contract integer %q", text)
		}
		const maxSafeInteger = int64(1<<53 - 1)
		if integer > maxSafeInteger || integer < -maxSafeInteger {
			return nil, fmt.Errorf("contract integer %q is outside the RFC 8785 exact range; use a generated type-tagged value", text)
		}
		return typed, nil
	default:
		return value, nil
	}
}
