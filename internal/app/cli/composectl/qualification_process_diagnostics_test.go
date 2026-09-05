package composectl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creachadair/jrpc2"
)

func TestQualificationBrowserImageMatchesInstalledPlaywright(t *testing.T) {
	manifestPath := filepath.Join(
		"..", "..", "..", "..", "deploy", "compose", "qualification", "package.json",
	)
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	version := manifest.Dependencies["playwright"]
	want := fmt.Sprintf("mcr.microsoft.com/playwright:v%s-noble", version)
	if version == "" || qualificationBrowserImage != want {
		t.Fatalf("qualification browser image = %q, want %q", qualificationBrowserImage, want)
	}
}

func TestQualificationJSONWorkerIncludesRedactedStderrOnProtocolFailure(t *testing.T) {
	transport := newQualificationTestRoundTrip("not-json\n")
	worker := &qualificationJSONWorker{
		stderr: &boundedQualificationBuffer{maxBytes: 1024},
	}
	_, _ = worker.stderr.Write([]byte("browser crashed token=secret-value"))
	worker.client = jrpc2.NewClient(
		newQualificationRPCChannel(transport, transport),
		&jrpc2.ClientOptions{
			OnNotify:   worker.handleNotification,
			OnCallback: worker.handleCallback,
		},
	)
	t.Cleanup(func() { _ = worker.client.Close() })

	err := worker.Call("inspect", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "browser crashed") {
		t.Fatalf("worker error = %v, want stderr diagnostics", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("worker error leaked a secret: %v", err)
	}
}
