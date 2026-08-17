package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/agent"
	agentsqlite "github.com/flidai/leapview/internal/agent/sqlite"
	"github.com/flidai/leapview/internal/platform"
	jobssqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/servingstate"
)

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
	return agentsqlite.NewRepositoryWithEvents(store.SQLDB(), jobssqlite.NewRepository(store.SQLDB()))
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
