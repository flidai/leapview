package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/access/http/mcpoauth"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/pkg/jobs"
)

// testMCPResource is a bounded profile-only resource verifier. It exercises
// MCP transport authentication and challenge/metadata wiring without creating
// durable MCP OAuth state in the SQLite application fixture. Internal OAuth
// protocol behavior is covered by the PostgreSQL mcpoauth integration suite.
type testMCPResource struct {
	repo     access.Repository
	resource string
}

func newTestMCPResource(repo access.Repository, publicURL string) *testMCPResource {
	publicURL = strings.TrimSuffix(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		publicURL = "http://localhost:8080"
	}
	return &testMCPResource{repo: repo, resource: publicURL + "/mcp"}
}

func (r *testMCPResource) Authenticate(ctx context.Context, token string) (mcpoauth.Credential, error) {
	if r == nil || r.repo == nil || !strings.HasPrefix(token, "test-mcp-") {
		return mcpoauth.Credential{}, fmt.Errorf("invalid test MCP token")
	}
	principalID := strings.TrimPrefix(token, "test-mcp-")
	principal, err := r.repo.PrincipalByID(ctx, principalID)
	if err != nil || principal.AccessDisabled() {
		return mcpoauth.Credential{}, fmt.Errorf("test MCP principal is unavailable")
	}
	return mcpoauth.Credential{Principal: principal, Resource: r.resource, Scopes: []string{mcpoauth.ScopeMCPUse}}, nil
}

func (r *testMCPResource) ProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeTestMCPJSON(w, http.StatusOK, map[string]any{
		"resource":                 r.resource,
		"authorization_servers":    []string{strings.TrimSuffix(r.resource, "/mcp")},
		"scopes_supported":         []string{mcpoauth.ScopeMCPUse},
		"bearer_methods_supported": []string{"header"},
	})
}

func (r *testMCPResource) Challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, strings.TrimSuffix(r.resource, "/mcp")+"/.well-known/oauth-protected-resource/mcp", mcpoauth.ScopeMCPUse))
	writeTestMCPJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token", "error_description": "A valid MCP OAuth access token is required"})
}

func (r *testMCPResource) IssueToken(principalID string) string {
	return "test-mcp-" + strings.TrimSpace(principalID)
}

func writeTestMCPJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// testStore opens the canonical platform database. Project and resource
// authorization is supplied by the request fixture; no workspace registry is
// created as an implicit test dependency.
func testStore(t *testing.T) *platform.Store {
	t.Helper()
	store, err := platform.Open(context.Background(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(context.Background(), `INSERT INTO projects (id, title) VALUES ('project:test', 'Test Project')`); err != nil {
		t.Fatalf("seed test project: %v", err)
	}
	t.Cleanup(func() {
		closeTestRuntimeHost(store.SQLDB())
		_ = store.Close()
	})
	return store
}

func testAccessRepository(store *platform.Store) access.Repository {
	return accesssqlite.NewRepository(store.SQLDB())
}

func testAgentRepository(store *platform.Store) agent.Repository {
	return agentsqlite.NewRepositoryWithEvents(store.SQLDB(), &testJobEvents{})
}

type testJobEvents struct {
	mu   sync.Mutex
	rows []jobs.Event
}

func (*testJobEvents) Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error) {
	return jobs.Job{}, errors.New("test queue execution is unavailable")
}
func (*testJobEvents) Get(context.Context, string) (jobs.Job, error) {
	return jobs.Job{}, jobs.ErrNotFound
}
func (*testJobEvents) Cancel(context.Context, string) error { return jobs.ErrNotFound }
func (r *testJobEvents) AppendEvent(_ context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := jobs.Event{ID: int64(len(r.rows) + 1), ResourceKind: kind, ResourceID: id, EventType: event, Data: append([]byte(nil), data...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.rows = append(r.rows, row)
	return row, nil
}
func (r *testJobEvents) ListEvents(_ context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]jobs.Event, 0, limit)
	for _, row := range r.rows {
		if row.ResourceKind == kind && row.ResourceID == id && row.ID > after && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
}

// testPrincipal creates a project-scoped identity. Role assignment belongs to
// the serving authorization snapshot and is intentionally not hidden in this
// fixture helper; callers that exercise authorization seed explicit grants.
func testPrincipal(t *testing.T, ctx context.Context, store *platform.Store, email, displayName string) access.Principal {
	t.Helper()
	principal, err := testAccessRepository(store).UpsertPrincipal(ctx, access.PrincipalInput{
		Kind: access.PrincipalKindUser, Email: email, DisplayName: displayName,
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	return principal
}

func testPlatformPrincipal(t *testing.T, ctx context.Context, store *platform.Store, email, displayName string) access.Principal {
	t.Helper()
	principal, err := testAccessRepository(store).SetPlatformRole(ctx, access.PlatformRoleInput{
		Email: email, DisplayName: displayName, Role: access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatalf("bind platform role: %v", err)
	}
	return principal
}

func testAPIToken(t *testing.T, ctx context.Context, store *platform.Store, principalID, name string) string {
	t.Helper()
	secret, err := testAccessRepository(store).CreateAPIToken(ctx, principalID, name)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	return secret
}

func assertAPIError(t *testing.T, rec *httptest.ResponseRecorder, wantCode int, messageContains string) {
	t.Helper()
	if rec.Code != wantCode {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, wantCode, rec.Body.String())
	}
	if messageContains != "" && !strings.Contains(rec.Body.String(), messageContains) {
		t.Fatalf("body = %q, want %q", rec.Body.String(), messageContains)
	}
}

func zeroArtifact(servingStateID servingstate.ID, _ string) servingstate.Artifact {
	return servingstate.Artifact{ID: "artifact_" + string(servingStateID), ServingStateID: servingStateID, Digest: "digest", Format: "tar.gz", Path: "artifact.tar.gz", ManifestJSON: "{}"}
}

func completeTestValidation(_ string, validation servingstate.Validation) servingstate.Validation {
	validation.ProjectDigest = "sha256:" + strings.Repeat("a", 64)
	if validation.ProjectID == "" {
		validation.ProjectID = projectgraph.ResourceID("project:test")
	}
	return validation
}
