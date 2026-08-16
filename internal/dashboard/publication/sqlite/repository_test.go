package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestReconcilePreservesPublicIDAcrossProjectCutover(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	seedProject(t, db)
	repo := NewRepository(db)
	input := publication.ReconcileInput{ProjectID: projectgraph.ResourceID("site"), ServingStateID: "state_1", ActorID: "owner", Publications: map[string]publication.Definition{"website": definition("digest-1")}}
	reconcile(t, ctx, db, input)
	first := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if first.PublicID == "" || first.Status() != publication.StatusActive {
		t.Fatalf("first = %#v", first)
	}

	input.ServingStateID = "state_2"
	input.Publications["website"] = definition("digest-2")
	reconcile(t, ctx, db, input)
	second := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if second.PublicID != first.PublicID || second.ServingStateID != "state_2" || second.ConfigurationDigest != "digest-2" {
		t.Fatalf("second = %#v", second)
	}

	input.ServingStateID = "state_3"
	input.Publications = map[string]publication.Definition{}
	reconcile(t, ctx, db, input)
	disabled := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if disabled.Status() != publication.StatusUnconfigured || disabled.PublicID != first.PublicID {
		t.Fatalf("disabled = %#v", disabled)
	}
}

func TestReconcileRejectsMissingProjectIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tx, err := store.SQLDB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = ReconcileTx(ctx, tx, publication.ReconcileInput{ServingStateID: "generation"}, func(context.Context, transaction.Transaction, string, string) error { return nil })
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("ReconcileTx accepted missing project identity")
	}
}

func seedProject(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	for _, query := range []string{
		`INSERT INTO projects (id, title) VALUES ('site', 'Site')`,
		`INSERT INTO serving_states (id, project_id, environment, status, source) VALUES ('state_1', 'site', 'prod', 'validated', 'publish')`,
		`INSERT INTO serving_states (id, project_id, environment, status, source) VALUES ('state_2', 'site', 'prod', 'validated', 'publish')`,
		`INSERT INTO serving_states (id, project_id, environment, status, source) VALUES ('state_3', 'site', 'prod', 'validated', 'publish')`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
}

func reconcile(t *testing.T, ctx context.Context, db *sql.DB, input publication.ReconcileInput) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileTx(ctx, tx, input, func(context.Context, transaction.Transaction, string, string) error { return nil }); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func definition(digest string) publication.Definition {
	return publication.Definition{Name: "website", Dashboard: "showcase", DefaultPage: "overview", ConfigurationDigest: digest, DependencyAssetIDs: []string{"dashboard:showcase"}}
}

func mustGet(t *testing.T, repo *Repository, ctx context.Context, projectID projectgraph.ResourceID, name string) publication.Publication {
	t.Helper()
	row, err := repo.Get(ctx, projectID, name)
	if err != nil && !errors.Is(err, publication.ErrNotFound) {
		t.Fatal(err)
	}
	if errors.Is(err, publication.ErrNotFound) {
		t.Fatal("publication not found")
	}
	return row
}
