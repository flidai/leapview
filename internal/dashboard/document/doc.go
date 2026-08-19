// Package document contains the generated canonical Dashboard resource DTOs.
//
// Configschema decoding, project authoring, builder and agent projections,
// revision storage, export, and the canonical compiler all use this generated
// shape. Structural constraints are generated here; contextual compatibility
// and governed semantic resolution are enforced by the canonical compiler.
// This package intentionally contains no YAML union unmarshallers, aliases,
// translators, or runtime/compiler resolution fields.
package document

import (
	"bytes"
	"encoding/json"
	"fmt"

	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

// Clone returns a lossless deep copy of the generated canonical DTO. JSON is
// the generated contract's own tagged-union representation, so this copy path
// cannot reintroduce the legacy authoring model or silently drop variants.
func (value DashboardDocument) Clone() (DashboardDocument, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return DashboardDocument{}, fmt.Errorf("clone dashboard document: %w", err)
	}
	var clone DashboardDocument
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return DashboardDocument{}, fmt.Errorf("decode cloned dashboard document: %w", err)
	}
	return clone, nil
}

// EncodeYAML emits the canonical dashboard document exactly as represented by
// the generated DTO.  Authoring callers use this at export boundaries instead
// of converting through a legacy dashboard object or renderer-facing
// definition.  The generated JSON representation remains the tagged-union
// authority; YAML is only a presentation encoding of that value.
func EncodeYAML(value DashboardDocument) ([]byte, error) {
	if value.APIVersion == "" {
		return nil, fmt.Errorf("dashboard apiVersion is required")
	}
	if value.Kind == "" {
		return nil, fmt.Errorf("dashboard kind is required")
	}
	if value.Metadata.ID == "" || value.Metadata.Name == "" {
		return nil, fmt.Errorf("dashboard metadata.id and metadata.name are required")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal dashboard document: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(encoded, &node); err != nil {
		return nil, fmt.Errorf("normalize dashboard document: %w", err)
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	if err := encoder.Encode(&node); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("encode dashboard document: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close dashboard document: %w", err)
	}
	return output.Bytes(), nil
}

// ValidateSchema validates the complete generated Dashboard DTO at the
// canonical resource-schema boundary. Fragment expansion and all other
// callers must use this seam so a value that decodes into the DTO cannot skip
// final structural/schema validation before compilation or export.
func ValidateSchema(value DashboardDocument, filename string) error {
	content, err := EncodeYAML(value)
	if err != nil {
		return err
	}
	if filename == "" {
		filename = "dashboard.yaml"
	}
	if err := configschema.ValidateBytes(configschema.KindDashboard, filename, content); err != nil {
		return fmt.Errorf("validate canonical dashboard: %w", err)
	}
	return nil
}
