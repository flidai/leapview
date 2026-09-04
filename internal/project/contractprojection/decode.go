package contractprojection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeSourcePublication strictly decodes canonical Source publication bytes
// into a generated read view. The result intentionally is not a Projection;
// only ProjectSource can produce a value accepted by canonicalization.
func DecodeSourcePublication(data []byte) (SourceView, error) {
	var value SourceView
	if err := decodeCanonicalPublication(data, "Source", &value); err != nil {
		return SourceView{}, err
	}
	return value, nil
}

// DecodeModelPublication strictly decodes canonical Model publication bytes
// into a generated read view.
func DecodeModelPublication(data []byte) (ModelView, error) {
	var value ModelView
	if err := decodeCanonicalPublication(data, "Model", &value); err != nil {
		return ModelView{}, err
	}
	return value, nil
}

// DecodeSemanticModelPublication strictly decodes canonical SemanticModel
// publication bytes into a generated read view.
func DecodeSemanticModelPublication(data []byte) (SemanticModelView, error) {
	var value SemanticModelView
	if err := decodeCanonicalPublication(data, "SemanticModel", &value); err != nil {
		return SemanticModelView{}, err
	}
	return value, nil
}

// DigestSourcePublication validates canonical Source publication bytes and
// returns the SHA-256 identity of those exact bytes.
func DigestSourcePublication(data []byte) (string, error) {
	if _, err := DecodeSourcePublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

// DigestModelPublication validates canonical Model publication bytes and
// returns the SHA-256 identity of those exact bytes.
func DigestModelPublication(data []byte) (string, error) {
	if _, err := DecodeModelPublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

// DigestSemanticModelPublication validates canonical SemanticModel publication
// bytes and returns the SHA-256 identity of those exact bytes.
func DigestSemanticModelPublication(data []byte) (string, error) {
	if _, err := DecodeSemanticModelPublication(data); err != nil {
		return "", err
	}
	return digestCanonicalPublication(data), nil
}

func digestCanonicalPublication(data []byte) string {
	return digestCanonicalBytes(data)
}

func decodeCanonicalPublication(data []byte, expectedKind string, output any) error {
	if len(data) == 0 {
		return errors.New("decode contract publication: empty bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode %s publication: %w", expectedKind, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode %s publication: trailing JSON value", expectedKind)
		}
		return fmt.Errorf("decode %s publication: %w", expectedKind, err)
	}

	// Re-encode the generated view and canonicalize that representation. Some
	// generated union decoders own their UnmarshalJSON implementation, so an
	// unknown nested property can be accepted and then omitted by MarshalJSON.
	// Comparing the canonical DTO bytes to the input closes that publication
	// boundary: accepted bytes must be exactly the bytes represented by output.
	encoded, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("decode %s publication: encode generated view: %w", expectedKind, err)
	}
	canonical, err := canonicalPublicationBytes(encoded, expectedKind)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonical, data) {
		return fmt.Errorf("decode %s publication: decoded view differs from canonical bytes", expectedKind)
	}
	return nil
}

func canonicalPublicationBytes(data []byte, expectedKind string) ([]byte, error) {
	var envelope struct {
		Profile    string `json:"profile"`
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Contract struct {
				Version       string `json:"version"`
				Compatibility string `json:"compatibility"`
			} `json:"contract"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s publication envelope: %w", expectedKind, err)
	}
	if envelope.Profile != Profile || envelope.APIVersion != "leapview.dev/v1" || envelope.Kind != expectedKind {
		return nil, fmt.Errorf("decode %s publication: invalid envelope", expectedKind)
	}
	if envelope.Metadata.ID == "" || envelope.Metadata.Name == "" || envelope.Metadata.Contract.Version == "" || envelope.Metadata.Contract.Compatibility == "" {
		return nil, fmt.Errorf("decode %s publication: required envelope field is missing", expectedKind)
	}
	normalized, err := normalizeJSONStrings(data)
	if err != nil {
		return nil, fmt.Errorf("decode %s publication: normalize JSON: %w", expectedKind, err)
	}
	canonical, err := canonicalizeRFC8785(normalized)
	if err != nil {
		return nil, fmt.Errorf("decode %s publication: RFC 8785 canonicalize: %w", expectedKind, err)
	}
	return canonical, nil
}
