package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func TestCreateOperationDurableReplayAndConflictAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authoring.db")
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "workspace", Title: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	operation := testCreateOperation("workspace", "actor", "create", "retry-1", "sha256:"+repeatHex('a'))
	firstInput := testCreateInput(operation, "dash-1", "revision-1")
	first, err := repository.Create(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	repository = authoringsqlite.NewRepository(store.SQLDB())
	second, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found || second.DashboardID != first.ID || second.Revision.RevisionID != first.Draft.Revision.RevisionID {
		t.Fatalf("durable operation = %#v found=%v err=%v first=%#v", second, found, err, first)
	}
	replayed, err := repository.Create(ctx, testCreateInput(operation, "dash-2", "revision-2"))
	if err != nil || replayed.ID != first.ID || replayed.Draft.Revision != first.Draft.Revision {
		t.Fatalf("replayed lifecycle = %#v err=%v first=%#v", replayed, err, first)
	}
	changed := operation
	changed.Fingerprint = "sha256:" + repeatHex('b')
	if _, err := repository.Create(ctx, testCreateInput(changed, "dash-3", "revision-3")); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed payload error = %v", err)
	}
}

func TestCreateOperationConcurrentSameKeyIsSingleLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "workspace", Title: "Workspace"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	operation := testCreateOperation("workspace", "actor", "create", "retry-concurrent", "sha256:"+repeatHex('c'))
	inputs := []authoring.CreateInput{testCreateInput(operation, "dash-a", "revision-a"), testCreateInput(operation, "dash-b", "revision-b")}
	results := make([]authoring.DashboardLifecycle, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range inputs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = repository.Create(ctx, inputs[index])
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent create %d error = %v", i, err)
		}
	}
	if results[0].ID == "" || results[0].ID != results[1].ID {
		t.Fatalf("concurrent results = %#v", results)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_dashboards`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dashboard count = %d err=%v", count, err)
	}
}

func TestCreateOperationKeyScopesByWorkspaceActorAndKind(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	for _, id := range []string{"workspace", "workspace-2"} {
		if err := workspaceRepo.Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(id), Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES (?, ?, ?)`, "owner", "owner@example.test", "Owner"); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	cases := []struct {
		workspace, actor, kind, dashboard string
	}{
		{"workspace", "actor", "create", "dash-a"},
		{"workspace", "actor", "create", "dash-b"}, // different key below
		{"workspace", "actor-2", "create", "dash-c"},
		{"workspace-2", "actor", "create", "dash-d"},
		{"workspace", "actor", "fork", "dash-e"},
	}
	for i, item := range cases {
		key := "same-key"
		if i == 1 {
			key = "different-key"
		}
		operation := testCreateOperation(item.workspace, item.actor, item.kind, key, "sha256:"+repeatHex(byte('a'+i)))
		if _, err := repository.Create(ctx, testCreateInput(operation, item.dashboard, "revision-"+item.dashboard)); err != nil {
			t.Fatalf("case %d create error = %v", i, err)
		}
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_dashboards`).Scan(&count); err != nil || count != len(cases) {
		t.Fatalf("scoped dashboard count = %d err=%v", count, err)
	}
}

func testCreateOperation(workspaceID, actorID, kind, key, fingerprint string) authoring.CreateOperation {
	return authoring.CreateOperation{WorkspaceID: workspaceID, ActorID: actorID, Kind: kind, IdempotencyKey: key, ConversationID: "conversation", ToolCallID: "tool", Fingerprint: fingerprint}
}

func testCreateInput(operation authoring.CreateOperation, dashboardID, revisionID string) authoring.CreateInput {
	document := authoring.Dashboard{ID: dashboardID, Title: dashboardID, SemanticModel: "sales", Visuals: map[string]authoring.AuthoringVisualization{}, Pages: []dashboardmodel.Page{{ID: "overview", Title: "Overview"}}}
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: operation.ActorID, ConversationID: operation.ConversationID, ToolCallID: operation.ToolCallID}
	revision, _ := authoring.NewRevision(authoring.RevisionID(revisionID), authoring.DashboardID(dashboardID), 1, time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC), document, provenance)
	draft := &authoring.Draft{ID: authoring.DraftID("draft-" + dashboardID), DashboardID: authoring.DashboardID(dashboardID), Revision: revision.Token(), Provenance: provenance}
	lifecycle, _ := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{WorkspaceID: operation.WorkspaceID, ID: authoring.DashboardID(dashboardID), OwnerPrincipalID: "owner", Slug: dashboardID, Title: dashboardID, SemanticModel: "sales", Visibility: authoring.VisibilityPrivate, Draft: draft})
	return authoring.CreateInput{WorkspaceID: operation.WorkspaceID, Lifecycle: lifecycle, Revision: revision, Operation: operation}
}

func repeatHex(char byte) string {
	result := make([]byte, 64)
	for i := range result {
		result[i] = char
	}
	return string(result)
}
