package developmentprofile

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
)

const SchemaFilename = "analytics-development-profile.schema.json"

// JSONSchema publishes the portable editor shape from the same connector
// registry and field vocabulary used by runtime profile validation. Editors
// with a compiled graph should prefer JSONSchemaForConnections. The schema is
// an editor aid; Load remains authoritative for canonical-path, YAML-feature,
// template, inline-secret, and secret-bearing URL validation.
func JSONSchema() ([]byte, error) {
	return encodeSchema(schemaDocument(nil))
}

// JSONSchemaForConnections publishes an exact editor schema for one compiled
// graph. Only graph connection names that use target bindings are accepted,
// and each name receives its connector-specific options and credential mode.
func JSONSchemaForConnections(catalog map[string]LogicalConnection) ([]byte, error) {
	connections := map[string]any{}
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		logical := catalog[name]
		spec, ok := connectors.LookupConnection(logical.ConnectorKind)
		if !ok {
			return nil, fmt.Errorf("connection %q has unsupported connector kind %q", name, logical.ConnectorKind)
		}
		if spec.ActivationMode != connectors.TargetBindingActivation {
			continue
		}
		connections[name] = connectionSchema(
			spec.AllowedOptions,
			credentialSchema(spec.AllowPublicAccess && logical.Access == semanticmodel.ConnectionAccessPublic),
		)
	}
	return encodeSchema(schemaDocument(connections))
}

// schemaDocument accepts nil for the generic published schema and an exact
// connection-property map for a graph-specific schema.
func schemaDocument(exactConnections map[string]any) map[string]any {
	connections := map[string]any{"type": "object"}
	if exactConnections == nil {
		connections["additionalProperties"] = connectionSchema(allEndpointOptions(), credentialSchemaGeneric())
	} else {
		connections["additionalProperties"] = false
		connections["properties"] = exactConnections
	}
	profile := map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"connections"},
		"properties": map[string]any{"connections": connections},
	}
	return map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id":     "https://leapview.dev/schemas/analytics-development-profile.schema.json",
		"title":   "LeapView local analytics connection profiles",
		"type":    "object", "additionalProperties": false, "required": []string{"version", "profiles"},
		"properties": map[string]any{
			"version": map[string]any{"type": "integer", "const": Version},
			"profiles": map[string]any{
				"type": "object", "minProperties": 1,
				"propertyNames":        map[string]any{"pattern": profileNamePattern.String()},
				"additionalProperties": profile,
			},
		},
		"x-leapview-endpoint-options": allEndpointOptions(),
		"x-leapview-runtime-validation": []string{
			"canonical-path", "yaml-features", "templates", "inline-secrets", "secret-bearing-urls",
		},
	}
}

func connectionSchema(options []string, credentials map[string]any) map[string]any {
	optionProperties := map[string]any{}
	for _, name := range options {
		optionProperties[name] = map[string]any{"type": "string"}
	}
	stringProperty := func() map[string]any { return map[string]any{"type": "string"} }
	endpointProperties := map[string]any{
		"host": stringProperty(), "port": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
		"database": stringProperty(), "objectScope": stringProperty(), "sourceIdentity": stringProperty(),
		"tlsMode": stringProperty(),
		"options": map[string]any{"type": "object", "additionalProperties": false, "properties": optionProperties},
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"endpoint", "credentials"},
		"properties": map[string]any{
			"endpoint":    map[string]any{"type": "object", "additionalProperties": false, "properties": endpointProperties},
			"credentials": credentials,
		},
	}
}

func credentialSchema(public bool) map[string]any {
	if public {
		return noneCredentialSchema()
	}
	return environmentCredentialSchema()
}

func credentialSchemaGeneric() map[string]any {
	return map[string]any{"oneOf": []any{environmentCredentialSchema(), noneCredentialSchema()}}
}

func environmentCredentialSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"env"},
		"properties": map[string]any{"env": map[string]any{"type": "string", "maxLength": 256, "pattern": environmentPattern.String()}},
	}
}

func noneCredentialSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"none"},
		"properties": map[string]any{"none": map[string]any{"const": true}},
	}
}

func allEndpointOptions() []string {
	set := map[string]struct{}{}
	for _, kind := range connectors.ConnectionKinds() {
		spec, ok := connectors.LookupConnection(kind)
		if !ok {
			continue
		}
		for _, name := range spec.AllowedOptions {
			set[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func encodeSchema(schema map[string]any) ([]byte, error) {
	encoded, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
