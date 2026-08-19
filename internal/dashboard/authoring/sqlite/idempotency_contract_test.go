package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/project/graph"
)

func contractCreateOperation(project, actor, kind, key, fingerprint string) authoring.CreateOperation {
	return authoring.CreateOperation{ProjectID: graph.ResourceID(project), ActorID: actor, Kind: kind, IdempotencyKey: key, ConversationID: "conversation", ToolCallID: "tool", Fingerprint: fingerprint}
}

func contractCreateInput(t *testing.T, project, id, revisionID string, operation authoring.CreateOperation) authoring.CreateInput {
	t.Helper()
	input, _ := canonicalSQLiteInput(t, project, id, revisionID, operation)
	input.Lifecycle.Slug = strings.ToLower(strings.ReplaceAll(id, ":", "-"))
	return input
}

func TestCanonicalCreateOperationDurableReplayAndConflictAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "authoring.db")
	store, err := platform.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	operation := contractCreateOperation("project:sales", "actor", "create", "retry-1", "sha256:"+strings.Repeat("a", 64))
	input, revision := canonicalSQLiteInput(t, "project:sales", "dashboard:sales", "revision-1", operation)
	first, err := repository.Create(ctx, input)
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
	result, found, err := repository.LookupCreateOperation(ctx, operation)
	if err != nil || !found || result.DashboardID != first.ID || result.Revision != revision.Token() {
		t.Fatalf("durable operation=%#v found=%v err=%v", result, found, err)
	}
	replayed, err := repository.Create(ctx, contractCreateInput(t, "project:sales", "dashboard:other", "revision-2", operation))
	if err != nil || replayed.ID != first.ID || replayed.Draft.Revision != first.Draft.Revision {
		t.Fatalf("replayed lifecycle=%#v err=%v", replayed, err)
	}
	changed := operation
	changed.Fingerprint = "sha256:" + strings.Repeat("b", 64)
	if _, err := repository.Create(ctx, contractCreateInput(t, "project:sales", "dashboard:other", "revision-2", changed)); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("changed payload error=%v", err)
	}
}

func TestCanonicalCreateOperationConcurrentSameKeyIsSingleLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	operation := contractCreateOperation("project:sales", "actor", "create", "retry-concurrent", "sha256:"+strings.Repeat("c", 64))
	inputs := []authoring.CreateInput{contractCreateInput(t, "project:sales", "dashboard:a", "revision-a", operation), contractCreateInput(t, "project:sales", "dashboard:b", "revision-b", operation)}
	results := make([]authoring.DashboardLifecycle, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for index := range inputs {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = repository.Create(ctx, inputs[index])
		}(index)
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("create %d error=%v", index, err)
		}
	}
	if results[0].ID == "" || results[0].ID != results[1].ID {
		t.Fatalf("concurrent results=%#v", results)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_dashboards`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("dashboard count=%d err=%v", count, err)
	}
}

func TestCanonicalCreateOperationKeyScopesByProjectActorAndKind(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	repository := authoringsqlite.NewRepository(store.SQLDB())
	cases := []struct{ project, actor, kind, dashboard string }{{"project:sales", "actor", "create", "dashboard:a"}, {"project:sales", "actor", "create", "dashboard:b"}, {"project:sales", "actor-2", "create", "dashboard:c"}, {"project:other", "actor", "create", "dashboard:d"}, {"project:sales", "actor", "fork", "dashboard:e"}}
	for index, item := range cases {
		key := "same-key"
		if index == 1 {
			key = "different-key"
		}
		operation := contractCreateOperation(item.project, item.actor, item.kind, key, "sha256:"+strings.Repeat(string(rune('a'+index)), 64))
		input := contractCreateInput(t, item.project, item.dashboard, "revision-"+item.dashboard, operation)
		if _, err := repository.Create(ctx, input); err != nil {
			t.Fatalf("case %d error=%v", index, err)
		}
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_dashboards`).Scan(&count); err != nil || count != len(cases) {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
