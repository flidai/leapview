package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/manageddata"
	manageddatacli "github.com/flidai/leapview/internal/manageddata/cli"
	"github.com/flidai/leapview/internal/manageddata/localplan"
)

type developmentInputTestTransport func(*http.Request) (*http.Response, error)

func (transport developmentInputTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestDevelopmentInputUploadTransportConcurrentRequests(t *testing.T) {
	const endpoint = "http://127.0.0.1/api/v1/projects/local/connections/sample/upload-sessions"
	transport := &developmentInputUploadTransport{endpoint: endpoint, base: developmentInputTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody}, nil
	})}
	first, _ := http.NewRequest(http.MethodPost, endpoint, nil)
	if _, err := transport.RoundTrip(first); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 100 {
				request, _ := http.NewRequest(http.MethodPatch, "http://127.0.0.1/upload/part", nil)
				if _, err := transport.RoundTrip(request); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
}

func TestDeclaredDevelopmentInputReusesOnlyExactServerRevision(t *testing.T) {
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "sales.csv", Size: 5, SHA256: strings.Repeat("a", 64)}}}
	for _, scenario := range []string{"exact", "plain denial", "missing", "forbidden", "wrong revision", "wrong manifest", "wrong size", "wrong count", "missing session", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			reads, uploads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer local-token" {
					t.Error("retained revision request omitted native credentials")
				}
				base := "/api/v1/projects/project:local/connections/connection:sample/"
				if r.Method == http.MethodPost && r.URL.Path == base+"upload-sessions" {
					uploads++
					if scenario == "plain denial" {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
					w.Header().Set("Content-Type", "application/problem+json")
					w.WriteHeader(http.StatusForbidden)
					_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": "Forbidden", "status": 403, "code": "FORBIDDEN", "detail": "Access denied"})
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != base+"revisions/"+manifest.RevisionID() {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				reads++
				if scenario == "missing" || scenario == "forbidden" {
					status := http.StatusNotFound
					if scenario == "forbidden" {
						status = http.StatusForbidden
					}
					http.Error(w, http.StatusText(status), status)
					return
				}
				body := map[string]any{"id": manifest.RevisionID(), "status": "available", "manifest": manifest, "fileCount": 1, "size": 5, "uploadSessionId": "session-retained", "createdAt": "2026-10-07T00:00:00Z"}
				switch scenario {
				case "wrong revision":
					body["id"] = "sha256:" + strings.Repeat("b", 64)
				case "wrong manifest":
					body["manifest"] = manageddata.Manifest{Files: []manageddata.File{{Path: "other.csv", Size: 5, SHA256: strings.Repeat("a", 64)}}}
				case "wrong size":
					body["size"] = 6
				case "wrong count":
					body["fileCount"] = 2
				case "missing session":
					body["uploadSessionId"] = ""
				case "unavailable":
					body["status"] = "pending"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			err := syncDeclaredDevelopmentInput(t.Context(), manageddatacli.SyncRequest{
				ProjectID: "project:local", Connection: "sample", ConnectionID: "connection:sample", Root: t.TempDir(),
				Target: server.URL, Token: "local-token", HTTPClient: server.Client(), Out: io.Discard,
				Plan: localplan.Result{Connection: "connection:sample", ConnectionName: "sample", Manifest: manifest},
			})
			if (err == nil) != (scenario == "exact" || scenario == "plain denial") {
				t.Fatalf("error=%v, want success only for exact retained revision", err)
			}
			if reads != 1 || uploads != 1 {
				t.Fatalf("requests uploads=%d reads=%d, want one of each", uploads, reads)
			}
		})
	}
}

func TestDeclaredDevelopmentInputDoesNotRecoverAfterUploadAdmission(t *testing.T) {
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "sales.csv", Size: 5, SHA256: strings.Repeat("a", 64)}}}
	for _, scenario := range []string{"completed", "later forbidden", "invalid upload identity"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/revisions/") {
					reads++
					t.Error("admitted upload attempted revision fallback")
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodGet {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				body := map[string]any{"id": "upload-test", "project": "project:local", "connection": "connection:sample", "revisionId": manifest.RevisionID(), "manifest": manifest, "status": "completed", "files": []any{map[string]any{"file": manifest.Files[0], "status": "verified"}}, "createdAt": "2026-10-07T00:00:00Z", "expiresAt": "2026-10-08T00:00:00Z"}
				if scenario == "later forbidden" {
					body["status"] = "open"
				}
				if scenario == "invalid upload identity" {
					body["connection"] = "connection:other"
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer server.Close()
			err := syncDeclaredDevelopmentInput(t.Context(), manageddatacli.SyncRequest{ProjectID: "project:local", Connection: "sample", ConnectionID: "connection:sample", Root: t.TempDir(), Target: server.URL, Token: "local-token", HTTPClient: server.Client(), Out: io.Discard, Plan: localplan.Result{Connection: "connection:sample", ConnectionName: "sample", Manifest: manifest}})
			if (err == nil) != (scenario == "completed") || reads != 0 {
				t.Fatalf("err=%v reads=%d", err, reads)
			}
		})
	}
}

func TestDeclaredDevelopmentInputPreservesOtherSyncFailures(t *testing.T) {
	manifest := manageddata.Manifest{Files: []manageddata.File{{Path: "sales.csv", Size: 5, SHA256: strings.Repeat("a", 64)}}}
	for _, status := range []int{400, 401, 409, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			reads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					reads++
					t.Error("non-authorization failure attempted retained revision reuse")
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "code": "TEST_FAILURE", "detail": "must preserve failure"})
			}))
			defer server.Close()
			err := syncDeclaredDevelopmentInput(t.Context(), manageddatacli.SyncRequest{ProjectID: "project:local", Connection: "sample", ConnectionID: "connection:sample", Root: t.TempDir(), Target: server.URL, Token: "local-token", HTTPClient: server.Client(), Out: io.Discard, Plan: localplan.Result{Connection: "connection:sample", ConnectionName: "sample", Manifest: manifest}})
			if err == nil || reads != 0 {
				t.Fatalf("err=%v reads=%d", err, reads)
			}
		})
	}
}
