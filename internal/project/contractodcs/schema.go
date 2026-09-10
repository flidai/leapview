package contractodcs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schema/odcs-json-schema-v3.1.0.json
var officialSchema []byte

var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func OfficialSchema() []byte { return append([]byte(nil), officialSchema...) }

func compiledSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(officialSchema))
		if err != nil {
			schemaErr = fmt.Errorf("ODCS official schema: %w", err)
			return
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource(OfficialSchemaURL, value); err != nil {
			schemaErr = fmt.Errorf("ODCS official schema resource: %w", err)
			return
		}
		schema, schemaErr = compiler.Compile(OfficialSchemaURL)
	})
	return schema, schemaErr
}

// Validate applies both the pinned upstream schema and LeapView's sealed
// extension rules. The upstream customProperties value is intentionally open,
// so schema validation alone cannot govern the LeapView namespace.
func Validate(document []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrInvalidODCSDocument, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON value", ErrInvalidODCSDocument)
	}
	compiled, err := compiledSchema()
	if err != nil {
		return err
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("%w: official schema: %v", ErrInvalidODCSDocument, err)
	}
	if err := validateExtension(document); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidODCSDocument, err)
	}
	return nil
}

func validateExtension(document []byte) error {
	var envelope struct {
		CustomProperties []struct {
			Property string          `json:"property"`
			Value    json.RawMessage `json:"value"`
		} `json:"customProperties"`
	}
	if err := json.Unmarshal(document, &envelope); err != nil {
		return err
	}
	if len(envelope.CustomProperties) != 1 || envelope.CustomProperties[0].Property != ExtensionProperty {
		return fmt.Errorf("exactly one governed %q extension is required", ExtensionProperty)
	}
	var extension ExtensionEvidence
	decoder := json.NewDecoder(bytes.NewReader(envelope.CustomProperties[0].Value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&extension); err != nil {
		return fmt.Errorf("decode %s extension: %w", ExtensionProperty, err)
	}
	if extension.Namespace != ExtensionNamespace || extension.ProjectionProfile != "leapview.contract/v1" {
		return fmt.Errorf("unsupported LeapView extension namespace or projection profile")
	}
	if extension.ResourceKind != "Source" && extension.ResourceKind != "Model" {
		return fmt.Errorf("unsupported LeapView extension resource kind %q", extension.ResourceKind)
	}
	if err := platformdigest.ValidateSHA256Identity(extension.ProjectionDigest); err != nil {
		return fmt.Errorf("invalid LeapView extension projection digest: %w", err)
	}
	return nil
}
