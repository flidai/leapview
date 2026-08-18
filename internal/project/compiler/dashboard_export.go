package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/document"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"gopkg.in/yaml.v3"
)

// ExportDashboard emits one deterministic, schema-validated canonical
// Dashboard resource. The generated DashboardDocument is the only accepted
// source: compiled definitions and legacy authoring structs are not
// decompiled or translated at this boundary.
func ExportDashboard(value document.DashboardDocument) ([]byte, error) {
	// Generated DTOs own the JSON contract, including all closed unions. Going
	// through JSON before YAML preserves those public camelCase names and
	// discriminator envelopes without reintroducing handwritten YAML unions.
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal dashboard document: %w", err)
	}
	var node yaml.Node
	if err := yaml.Unmarshal(jsonBytes, &node); err != nil {
		return nil, fmt.Errorf("convert dashboard document to YAML: %w", err)
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	if err := encoder.Encode(&node); err != nil {
		_ = encoder.Close()
		return nil, fmt.Errorf("marshal canonical dashboard: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close canonical dashboard encoder: %w", err)
	}
	content := output.Bytes()
	if err := configschema.ValidateBytes(configschema.KindDashboard, "dashboard.yaml", content); err != nil {
		return nil, fmt.Errorf("validate canonical dashboard: %w", err)
	}
	return content, nil
}
