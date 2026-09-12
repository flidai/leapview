package developmentprofile

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestJSONSchemaMatchesRuntimeShape(t *testing.T) {
	encoded, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("profile.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("profile.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"version": float64(1),
		"profiles": map[string]any{"local": map[string]any{"connections": map[string]any{
			"warehouse": map[string]any{
				"endpoint":    map[string]any{"host": "analytics.internal", "port": float64(5432)},
				"credentials": map[string]any{"env": "LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
			},
		}}},
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("valid editor document rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown root":  func(value map[string]any) { value["target"] = "prod" },
		"bad version":   func(value map[string]any) { value["version"] = float64(2) },
		"float version": func(value map[string]any) { value["version"] = 1.5 },
		"bad env": func(value map[string]any) {
			profileSchemaConnection(value)["credentials"] = map[string]any{"env": "DATABASE_URL"}
		},
		"two modes": func(value map[string]any) {
			profileSchemaConnection(value)["credentials"] = map[string]any{"env": "LEAPVIEW_DEV_CONNECTION_WAREHOUSE", "none": true}
		},
		"unknown endpoint": func(value map[string]any) {
			profileSchemaConnection(value)["endpoint"].(map[string]any)["password"] = "secret"
		},
		"unknown option": func(value map[string]any) {
			profileSchemaConnection(value)["endpoint"].(map[string]any)["options"] = map[string]any{"arbitrary": "value"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cloned := cloneSchemaDocument(t, valid)
			mutate(cloned)
			if err := schema.Validate(cloned); err == nil {
				t.Fatal("editor schema accepted invalid document")
			}
		})
	}
}

func TestJSONSchemaIsDeterministic(t *testing.T) {
	first, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	second, err := JSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generated profile schema is not deterministic")
	}
}

func TestJSONSchemaForConnectionsMatchesGraphAndConnectorContracts(t *testing.T) {
	encoded, err := JSONSchemaForConnections(map[string]LogicalConnection{
		"warehouse":    {ID: "connection_warehouse", ConnectorKind: "postgres"},
		"catalog":      {ID: "connection_catalog", ConnectorKind: "ducklake"},
		"public_files": {ID: "connection_public", ConnectorKind: "s3", Access: "public"},
		"fixture":      {ID: "connection_fixture", ConnectorKind: "managed", Access: "public"},
	})
	if err != nil {
		t.Fatal(err)
	}
	schema := compileSchema(t, encoded)
	base := func() map[string]any {
		return map[string]any{
			"version": float64(1),
			"profiles": map[string]any{"local": map[string]any{"connections": map[string]any{
				"warehouse": map[string]any{
					"endpoint":    map[string]any{"host": "db.internal"},
					"credentials": map[string]any{"env": "LEAPVIEW_DEV_CONNECTION_WAREHOUSE"},
				},
				"catalog": map[string]any{
					"endpoint":    map[string]any{"options": map[string]any{"data_path": "/data"}},
					"credentials": map[string]any{"env": "LEAPVIEW_DEV_CONNECTION_CATALOG"},
				},
				"public_files": map[string]any{
					"endpoint":    map[string]any{"objectScope": "s3://bucket/"},
					"credentials": map[string]any{"none": true},
				},
			}}},
		}
	}
	if err := schema.Validate(base()); err != nil {
		t.Fatalf("graph-specific schema rejected valid profile: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"missing required connection": func(value map[string]any) {
			delete(profileSchemaConnections(value), "catalog")
		},
		"unknown name": func(value map[string]any) {
			profileSchemaConnections(value)["Warehouse"] = profileSchemaConnections(value)["warehouse"]
			delete(profileSchemaConnections(value), "warehouse")
		},
		"managed name": func(value map[string]any) {
			profileSchemaConnections(value)["fixture"] = map[string]any{"endpoint": map[string]any{}, "credentials": map[string]any{"none": true}}
		},
		"postgres foreign option": func(value map[string]any) {
			profileSchemaConnection(value)["endpoint"].(map[string]any)["options"] = map[string]any{"data_path": "/data"}
		},
		"private none": func(value map[string]any) {
			profileSchemaConnection(value)["credentials"] = map[string]any{"none": true}
		},
		"public env": func(value map[string]any) {
			profileSchemaConnections(value)["public_files"].(map[string]any)["credentials"] = map[string]any{"env": "LEAPVIEW_DEV_CONNECTION_PUBLIC"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			document := cloneSchemaDocument(t, base())
			mutate(document)
			if err := schema.Validate(document); err == nil {
				t.Fatal("graph-specific schema accepted invalid profile")
			}
		})
	}
}

func compileSchema(t *testing.T, encoded []byte) *jsonschema.Schema {
	t.Helper()
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("profile.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("profile.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func profileSchemaConnection(document map[string]any) map[string]any {
	return profileSchemaConnections(document)["warehouse"].(map[string]any)
}

func profileSchemaConnections(document map[string]any) map[string]any {
	return document["profiles"].(map[string]any)["local"].(map[string]any)["connections"].(map[string]any)
}

func cloneSchemaDocument(t *testing.T, input map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return output
}
