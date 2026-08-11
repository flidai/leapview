package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func TestEveryCommandFailureVocabularyGeneratesTypedClientContracts(t *testing.T) {
	root := projectRoot(t)
	documentBytes, err := os.ReadFile(filepath.Join(root, "api", "gen", "json-ir.json"))
	if err != nil {
		t.Fatalf("read APIGen IR: %v", err)
	}
	var document struct {
		Endpoints []struct {
			OperationID string `json:"operation_id"`
			Namespace   string `json:"namespace"`
			Command     *struct {
				Failures []json.RawMessage `json:"failures"`
			} `json:"command"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(documentBytes, &document); err != nil {
		t.Fatalf("decode APIGen IR: %v", err)
	}

	clientPaths := map[string]string{
		"LeapViewAPI.Access":      "internal/access/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Agent":       "internal/agent/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Analytics":   "internal/analytics/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Dashboard":   "internal/dashboard/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Deployment":  "internal/deployment/api/gen/client.apigen.gen.go",
		"LeapViewAPI.ManagedData": "internal/manageddata/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Refresh":     "internal/refresh/api/gen/client.apigen.gen.go",
		"LeapViewAPI.Release":     "internal/release/api/gen/client.apigen.gen.go",
	}
	clients := make(map[string]string, len(clientPaths))
	for namespace, path := range clientPaths {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read generated client for %s: %v", namespace, err)
		}
		clients[namespace] = string(content)
	}
	typeScriptBytes, err := os.ReadFile(filepath.Join(root, "web", "generated", "api", "failures.ts"))
	if err != nil {
		t.Fatalf("read generated TypeScript failures: %v", err)
	}
	typeScript := string(typeScriptBytes)

	checked := 0
	for _, endpoint := range document.Endpoints {
		if endpoint.Command == nil || len(endpoint.Command.Failures) == 0 {
			continue
		}
		client, ok := clients[endpoint.Namespace]
		if !ok {
			t.Errorf("command %s has no generated-client namespace mapping for %q", endpoint.OperationID, endpoint.Namespace)
			continue
		}
		name := exportedOperationName(endpoint.OperationID)
		for _, declaration := range []string{
			"type Gen" + name + "Failure interface {",
			"func MatchGen" + name + "Failure[T any]",
			"err = gen" + name + "FailureFromError(err)",
		} {
			if !strings.Contains(client, declaration) {
				t.Errorf("generated Go client for %s is missing %q", endpoint.OperationID, declaration)
			}
		}
		for _, declaration := range []string{
			"export type " + name + "Failure =",
			"export function match" + name + "Failure<T>",
		} {
			if !strings.Contains(typeScript, declaration) {
				t.Errorf("generated TypeScript failures for %s are missing %q", endpoint.OperationID, declaration)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("APIGen IR contains no command failure vocabularies")
	}
}

func exportedOperationName(operationID string) string {
	parts := strings.FieldsFunc(operationID, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})
	if len(parts) == 0 {
		return ""
	}
	for index, part := range parts {
		runes := []rune(part)
		runes[0] = unicode.ToUpper(runes[0])
		parts[index] = string(runes)
	}
	return strings.Join(parts, "")
}
