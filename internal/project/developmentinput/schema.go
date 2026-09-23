package developmentinput

import "encoding/json"

const SchemaFilename = "analytics-development-inputs.schema.json"

// JSONSchema publishes the editor-visible shape used by runtime validation.
// Filesystem identity, exact inventory, aggregate size, and digest checks remain
// runtime responsibilities because JSON Schema cannot inspect local files.
func JSONSchema() ([]byte, error) {
	path := map[string]any{"type": "string", "pattern": portablePathPattern.String()}
	document := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     "https://leapview.dev/schemas/analytics-development-inputs.schema.json",
		"title":   "LeapView analytics development inputs",
		"type":    "object", "additionalProperties": false,
		"required": []string{"version", "inputs"},
		"properties": map[string]any{
			"version": map[string]any{"type": "integer", "const": Version},
			"inputs": map[string]any{
				"type": "object", "minProperties": 1,
				"propertyNames": map[string]any{"pattern": namePattern.String()},
				"additionalProperties": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"connection", "from", "provenance", "files"},
					"properties": map[string]any{
						"connection": map[string]any{"type": "string", "pattern": namePattern.String()},
						"from":       path,
						"provenance": map[string]any{
							"type": "object", "additionalProperties": false,
							"required": []string{"kind", "generator", "rows", "bounded"},
							"properties": map[string]any{
								"kind":      map[string]any{"const": "synthetic"},
								"generator": map[string]any{"type": "string", "pattern": generatorPattern.String()},
								"rows":      map[string]any{"type": "integer", "minimum": 1, "maximum": MaxRows},
								"bounded":   map[string]any{"const": true},
							},
						},
						"files": map[string]any{
							"type": "object", "minProperties": 1, "maxProperties": MaxFiles,
							"propertyNames": path,
							"additionalProperties": map[string]any{
								"type": "object", "additionalProperties": false,
								"required": []string{"sha256", "sizeBytes"},
								"properties": map[string]any{
									"sha256":    map[string]any{"type": "string", "pattern": digestPattern.String()},
									"sizeBytes": map[string]any{"type": "integer", "minimum": 1, "maximum": MaxTotalBytes},
								},
							},
						},
					},
				},
			},
		},
		"x-leapview-runtime-validation": []string{"canonical-project-root", "exact-inventory", "aggregate-size", "regular-files", "sha256"},
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
