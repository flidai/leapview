package developmentinput

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
	if err := compiler.AddResource("development-inputs.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("development-inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]any{
		"version": float64(1),
		"inputs": map[string]any{"sample": map[string]any{
			"connection": "sample", "from": "data/sample",
			"provenance": map[string]any{"kind": "synthetic", "generator": "leapview-init/v1", "rows": float64(12), "bounded": true},
			"files":      map[string]any{"sales.csv": map[string]any{"sha256": "b09b718b0967e3d1bed215440e3d46258b0748363ea7ca6120a483cb5464af05", "sizeBytes": float64(545)}},
		}},
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("valid editor document rejected: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unknown root": func(value map[string]any) { value["target"] = "prod" },
		"bad version":  func(value map[string]any) { value["version"] = float64(2) },
		"path escape": func(value map[string]any) {
			value["inputs"].(map[string]any)["sample"].(map[string]any)["from"] = "../outside"
		},
		"unbounded": func(value map[string]any) {
			value["inputs"].(map[string]any)["sample"].(map[string]any)["provenance"].(map[string]any)["bounded"] = false
		},
		"uppercase digest": func(value map[string]any) {
			value["inputs"].(map[string]any)["sample"].(map[string]any)["files"].(map[string]any)["sales.csv"].(map[string]any)["sha256"] = "B09B718B0967E3D1BED215440E3D46258B0748363EA7CA6120A483CB5464AF05"
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(valid)
			if err != nil {
				t.Fatal(err)
			}
			var clone map[string]any
			if err := json.Unmarshal(encoded, &clone); err != nil {
				t.Fatal(err)
			}
			mutate(clone)
			if err := schema.Validate(clone); err == nil {
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
		t.Fatal("generated development-input schema is not deterministic")
	}
}
