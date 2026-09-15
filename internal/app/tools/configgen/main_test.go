package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestGeneratedArtifactsAreCurrent(t *testing.T) {
	outputs, err := generatedOutputs()
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range outputs {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s is stale; run task config:generate", path)
		}
	}
}

func TestGeneratedEnvironmentSchemaCompiles(t *testing.T) {
	_ = compileEnvironmentSchema(t)
}

func TestGeneratedDocumentationUsesNavigationTitleAsHeading(t *testing.T) {
	docs := string(generateDocs())
	if !strings.Contains(docs, "\n# Environment variable reference\n") {
		t.Fatalf("generated documentation heading does not match its catalog title:\n%s", docs)
	}
}

func TestGeneratedEnvironmentSchemaEnforcesProductionRelationships(t *testing.T) {
	schema := compileEnvironmentSchema(t)
	valid := map[string]any{
		"LEAPVIEW_PRODUCTION":           "1",
		"LEAPVIEW_LOCAL_AUTH":           "true",
		"LEAPVIEW_CSRF_KEY":             "0123456789abcdef0123456789abcdef",
		"LEAPVIEW_METRICS_BEARER_TOKEN": "0123456789abcdef0123456789abcdef",
		"LEAPVIEW_ALLOWED_HOSTS":        "leapview.example.com",
		"LEAPVIEW_PUBLIC_URL":           "https://leapview.example.com",
		"LEAPVIEW_COOKIE_SECURE":        "true",
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("valid production environment rejected: %v", err)
	}
	if err := schema.Validate(map[string]any{"LEAPVIEW_PRODUCTION": "1"}); err == nil {
		t.Fatal("schema accepted production environment without authentication and secrets")
	}
}

func TestGeneratedEnvironmentSchemaEnforcesUnconditionalRules(t *testing.T) {
	schema := compileEnvironmentSchema(t)
	if err := schema.Validate(map[string]any{}); err == nil {
		t.Fatal("schema accepted an environment without a CSRF key")
	}
	if err := schema.Validate(map[string]any{"LEAPVIEW_CSRF_KEY": "short"}); err == nil {
		t.Fatal("schema accepted a short CSRF key outside production")
	}
	if err := schema.Validate(map[string]any{"LEAPVIEW_CSRF_KEY": "0123456789abcdef0123456789abcdef"}); err != nil {
		t.Fatalf("schema rejected a valid CSRF key outside production: %v", err)
	}
}

func compileEnvironmentSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	outputs, err := generatedOutputs()
	if err != nil {
		t.Fatal(err)
	}
	path := repositoryRoot() + "/schemas/config/environment.schema.json"
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(outputs[path]))
	if err != nil {
		t.Fatalf("unmarshal generated environment schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	const location = "https://leapview.dev/schemas/config/environment.schema.json"
	if err := compiler.AddResource(location, document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatalf("compile generated environment schema: %v", err)
	}
	return schema
}
