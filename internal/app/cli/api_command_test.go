package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

func TestAPICommandListsEveryGeneratedOperation(t *testing.T) {
	output := captureStdout(t, func() {
		cmd := apiCommand(context.Background(), &rootOptions{})
		cmd.SetArgs([]string{"list"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("api list: %v", err)
		}
	})

	for operationID := range apiaggregate.GetAPIGenOperationContracts() {
		if !strings.Contains(output, operationID) {
			t.Fatalf("api list missing %s:\n%s", operationID, output)
		}
	}
}

func TestGeneratedCommandHeadersFollowIdempotencyAndConcurrencyPolicy(t *testing.T) {
	create, ok := apiaggregate.GetAPIGenOperationContract("createAgentRun")
	if !ok || create.Command == nil {
		t.Fatal("createAgentRun command contract is missing")
	}
	key, ifMatch := generatedCommandHeaders(create, &apiCallOptions{idempotencyKey: "run-1", ifMatch: "ignored"})
	if key != "run-1" || ifMatch != "" {
		t.Fatalf("create command headers = %q/%q", key, ifMatch)
	}

	update, ok := apiaggregate.GetAPIGenOperationContract("updateRoleBinding")
	if !ok || update.Command == nil {
		t.Fatal("updateRoleBinding command contract is missing")
	}
	key, ifMatch = generatedCommandHeaders(update, &apiCallOptions{idempotencyKey: "ignored", ifMatch: `"revision-1"`})
	if key != "" || ifMatch != `"revision-1"` {
		t.Fatalf("concurrency command headers = %q/%q", key, ifMatch)
	}

	query, ok := apiaggregate.GetAPIGenOperationContract("getInstance")
	if !ok {
		t.Fatal("getInstance contract is missing")
	}
	key, ifMatch = generatedCommandHeaders(query, &apiCallOptions{idempotencyKey: "ignored", ifMatch: "ignored"})
	if key != "" || ifMatch != "" {
		t.Fatalf("query headers = %q/%q", key, ifMatch)
	}
}

func TestAPICommandCallUsesGeneratedContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/api/v1/agent/conversations/conv_1/runs" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("trace"); got != "1" {
			t.Fatalf("trace query = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-LeapView-Client"); got != "cli" {
			t.Fatalf("X-LeapView-Client = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "run-commit-a" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["input"] != "hello" {
			t.Fatalf("body = %#v", body)
		}
		writeCLIJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		cmd := apiCommand(context.Background(), &rootOptions{target: server.URL, token: "token", workspaceID: "test"})
		cmd.SetArgs([]string{
			"call", "createAgentRun",
			"--target", server.URL,
			"--token", "token",
			"--path", "conversation=conv_1",
			"--query", "trace=1",
			"--body-json", `{"input":"hello"}`,
			"--idempotency-key", "run-commit-a",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("api call: %v", err)
		}
	})
	if strings.TrimSpace(output) != `{"ok":true}` {
		t.Fatalf("output = %q", output)
	}
}

func TestAPICommandInvokesRoleBindingOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/projects/sales/role-bindings" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-LeapView-Client"); got != "cli" {
			t.Fatalf("X-LeapView-Client = %q", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "binding-commit-a" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["subjectId"] != "principal-viewer" || body["role"] != "viewer" {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		writeCLIJSON(t, w, map[string]any{"id": "binding-1"})
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		cmd := apiCommand(context.Background(), &rootOptions{target: server.URL, token: "token"})
		cmd.SetArgs([]string{
			"call", "createRoleBinding",
			"--target", server.URL,
			"--token", "token",
			"--path", "project=sales",
			"--body-json", `{"subjectType":"principal","subjectId":"principal-viewer","role":"viewer"}`,
			"--idempotency-key", "binding-commit-a",
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("api call createRoleBinding: %v", err)
		}
	})
	if strings.TrimSpace(output) != `{"id":"binding-1"}` {
		t.Fatalf("output = %q", output)
	}
}

func TestAPICommandCallDefaultsJSONBodyFileContentTypeFromGeneratedContract(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "turn.json")
	if err := os.WriteFile(bodyPath, []byte(`{"input":"hello"}`), 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		writeCLIJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	captureStdout(t, func() {
		cmd := apiCommand(context.Background(), &rootOptions{target: server.URL, token: "token", workspaceID: "test"})
		cmd.SetArgs([]string{
			"call", "createAgentRun",
			"--target", server.URL,
			"--token", "token",
			"--path", "conversation=conv_1",
			"--body-file", bodyPath,
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("api call: %v", err)
		}
	})
}

func TestAPICommandCallDefaultsBinaryBodyFileContentTypeFromGeneratedContract(t *testing.T) {
	bodyPath := filepath.Join(t.TempDir(), "artifact.tar.gz")
	if err := os.WriteFile(bodyPath, []byte("bundle"), 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.Path != "/api/v1/projects/project/releases/release_1/workspaces/test/artifact" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Fatalf("Content-Type = %q", got)
		}
		writeCLIJSON(t, w, map[string]any{"ok": true})
	}))
	defer server.Close()

	captureStdout(t, func() {
		cmd := apiCommand(context.Background(), &rootOptions{target: server.URL, token: "token", workspaceID: "test"})
		cmd.SetArgs([]string{
			"call", "uploadReleaseArtifact",
			"--target", server.URL,
			"--token", "token",
			"--path", "project=project",
			"--path", "release=release_1",
			"--body-file", bodyPath,
		})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("api call: %v", err)
		}
	})
}

func TestAPICommandRejectsMissingPathParameter(t *testing.T) {
	cmd := apiCommand(context.Background(), &rootOptions{target: "https://leapview.example", token: "token", workspaceID: "test"})
	cmd.SetArgs([]string{"call", "getAgentRun", "--target", "https://leapview.example", "--token", "token", "--path", "conversation=conv_1"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "run") {
		t.Fatalf("err = %v, want missing run path parameter", err)
	}
}
