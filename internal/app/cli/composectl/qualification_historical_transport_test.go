package composectl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yacobolo/toolbelt/apigen/runtime/chi"
	chimux "github.com/go-chi/chi/v5"
)

// TestQualificationHistoricalPythonTLSRelay exercises the exact Python
// urllib -> CONNECT relay -> Go TLS -> upstream path used by the isolated
// historical generation-status client, without starting Docker services.
func TestQualificationHistoricalPythonTLSRelay(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	router := chimux.NewRouter()
	router.Get("/api/v1/projects/{project}/delivery/generations/{generation}", func(w http.ResponseWriter, r *http.Request) {
		var projectID string
		if r.Method != http.MethodGet || chi.BindPathParameter("project", chi.URLParam(r, "project"), true, &projectID) != nil ||
			projectID != "project:transport-test" || chi.URLParam(r, "generation") != "generation-test" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"generation-test","projectId":"`+projectID+`","candidateId":"candidate-test","targetId":"target-test","environment":"prod","status":"active"}`)
	})
	upstream := httptest.NewServer(router)
	defer upstream.Close()
	transport := startQualificationHistoricalTransport(t, context.Background(), repoRoot, strings.TrimPrefix(upstream.URL, "http://"))

	environment := environmentMap(os.Environ())
	environment["DEMO_CLONE_ONLY"] = ""
	environment["DEMO_CLONE_PROXY"] = ""
	environment["DEMO_GENERATION_TOKEN"] = "synthetic-transport-token"
	environment["DEMO_GENERATION_CA_CERT"] = transport.CACert
	environment["DEMO_GENERATION_PROXY"] = transport.ProxyURL
	command := exec.CommandContext(t.Context(), "python3", filepath.Join(repoRoot, "scripts", "demo_client_contract.py"),
		"--wait-generation", "--target", qualificationHistoricalOrigin,
		"--project", "project:transport-test", "--generation", "generation-test", "--candidate", "candidate-test",
		"--target-id", "target-test", "--environment", "prod", "--timeout", "10", "--poll-interval", "1")
	command.Dir = repoRoot
	command.Env = sortedEnvironment(environment)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python generation-status client failed through the pinned TLS relay: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	for _, expected := range []string{`"candidateId": "candidate-test"`, `"id": "generation-test"`, `"status": "active"`, `"targetId": "target-test"`} {
		if !strings.Contains(string(output), expected) {
			t.Errorf("Python generation-status response omitted %s: %s", expected, strings.TrimSpace(string(output)))
		}
	}
}
