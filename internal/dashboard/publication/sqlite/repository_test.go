package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	publicationdb "github.com/flidai/leapview/internal/dashboard/internal/db"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestMapPublicationRejectsInvalidPersistedProjectID(t *testing.T) {
	_, err := mapPublication(publicationdb.DashboardPublication{
		ProjectID:              "invalid project",
		AllowedOriginsJson:     "[]",
		DependencyAssetIdsJson: "[]",
	})
	if err == nil {
		t.Fatal("mapPublication accepted an invalid persisted project ID")
	}
}

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
	if first.PublicID == "" || first.Status() != publication.StatusActive || first.Revision != 1 {
		t.Fatalf("first = %#v", first)
	}

	input.ServingStateID = "state_2"
	input.Publications["website"] = definition("digest-2")
	reconcile(t, ctx, db, input)
	second := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if second.PublicID != first.PublicID || second.ServingStateID != "state_2" || second.ConfigurationDigest != "digest-2" || second.Revision != 2 {
		t.Fatalf("second = %#v", second)
	}
	reconcile(t, ctx, db, input)
	unchanged := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if unchanged.Revision != second.Revision {
		t.Fatalf("unchanged reconciliation revision = %d, want %d", unchanged.Revision, second.Revision)
	}

	input.ServingStateID = "state_3"
	input.Publications = map[string]publication.Definition{}
	reconcile(t, ctx, db, input)
	disabled := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if disabled.Status() != publication.StatusUnconfigured || disabled.PublicID != first.PublicID || disabled.Revision != 3 {
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
	err = new(Repository).ReconcileTx(ctx, tx, publication.ReconcileInput{ServingStateID: "generation"}, func(context.Context, transaction.Transaction, projectgraph.ResourceID, string) error { return nil })
	_ = tx.Rollback()
	if err == nil {
		t.Fatal("ReconcileTx accepted missing project identity")
	}
}

func TestPublicationMutationCommitsAuditIntentInSameTransaction(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	seedProject(t, db)
	reconcile(t, ctx, db, publication.ReconcileInput{ProjectID: projectgraph.ResourceID("site"), ServingStateID: "state_1", ActorID: "owner", Publications: map[string]publication.Definition{"website": definition("digest-1")}})

	accessRepo := accesssqlite.NewRepository(db)
	var captured access.AuditIntent
	recorder := access.AuditIntentRecorderFunc(func(ctx context.Context, tx transaction.Transaction, intent access.AuditIntent) error {
		captured = intent
		return accessRepo.RecordAuditIntent(ctx, tx, intent)
	})
	repo := NewRepositoryWithAudit(db, recorder)
	intent := publicationAuditIntent("event-publication-commit")
	if _, err := repo.Suspend(publication.WithAuditIntent(ctx, intent), projectgraph.ResourceID("site"), "website", "principal-a", 1); err != nil {
		t.Fatal(err)
	}
	row := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if row.Status() != publication.StatusSuspended {
		t.Fatalf("publication status = %s, want suspended", row.Status())
	}
	if captured.EventID != intent.EventID || captured.AggregateSequence == 0 {
		t.Fatalf("captured audit intent = %#v", captured)
	}
	var state, aggregate string
	if err := db.QueryRowContext(ctx, `SELECT state, aggregate_key FROM audit_outbox WHERE event_id = ?`, captured.EventID).Scan(&state, &aggregate); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || aggregate != intent.AggregateKey {
		t.Fatalf("outbox state=%q aggregate=%q", state, aggregate)
	}

	// Replaying the exact payload is idempotent at the durable outbox boundary.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := accessRepo.RecordAuditIntent(ctx, tx, captured); err != nil {
		_ = tx.Rollback()
		t.Fatalf("idempotent replay: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationMutationRollsBackWhenAuditIntentFails(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	seedProject(t, db)
	reconcile(t, ctx, db, publication.ReconcileInput{ProjectID: projectgraph.ResourceID("site"), ServingStateID: "state_1", ActorID: "owner", Publications: map[string]publication.Definition{"website": definition("digest-1")}})
	repo := NewRepositoryWithAudit(db, access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error {
		return errors.New("audit unavailable")
	}))
	_, err = repo.Suspend(publication.WithAuditIntent(ctx, publicationAuditIntent("event-publication-rollback")), projectgraph.ResourceID("site"), "website", "principal-a", 1)
	if err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("suspend error = %v", err)
	}
	row := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if row.Status() != publication.StatusActive {
		t.Fatalf("publication status = %s, want active after rollback", row.Status())
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox WHERE event_id = ?`, "event-publication-rollback").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back audit intents = %d, want 0", count)
	}
}

func TestPublicationMutationRejectsSecretAuditMetadataAndRollsBack(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, t.TempDir()+"/platform.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.SQLDB()
	seedProject(t, db)
	reconcile(t, ctx, db, publication.ReconcileInput{ProjectID: projectgraph.ResourceID("site"), ServingStateID: "state_1", ActorID: "owner", Publications: map[string]publication.Definition{"website": definition("digest-1")}})
	repo := NewRepositoryWithAudit(db, accesssqlite.NewRepository(db))
	intent := publicationAuditIntent("event-publication-secret")
	intent.MetadataJSON = `{"secret":"must-not-persist"}`
	_, err = repo.Suspend(publication.WithAuditIntent(ctx, intent), projectgraph.ResourceID("site"), "website", "principal-a", 1)
	if err == nil || !strings.Contains(err.Error(), "metadata key") {
		t.Fatalf("secret metadata error = %v", err)
	}
	row := mustGet(t, repo, ctx, projectgraph.ResourceID("site"), "website")
	if row.Status() != publication.StatusActive {
		t.Fatalf("publication status = %s, want active after metadata rejection", row.Status())
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
	if err := new(Repository).ReconcileTx(ctx, tx, input, func(context.Context, transaction.Transaction, projectgraph.ResourceID, string) error { return nil }); err != nil {
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

func publicationAuditIntent(eventID string) access.AuditIntent {
	return access.AuditIntent{
		EventID: eventID, Source: "dashboard.publication", Operation: "suspendDashboardPublication",
		PrincipalID: "principal-a", Action: "dashboard_publication.suspended", ResourceKind: "project", ResourceID: "site",
		Capability: access.CapabilityResourcePublish, Outcome: "success", RequestID: "request-1", CorrelationID: "request-1",
		AggregateKey: "dashboard_publication:site:website", MetadataJSON: `{"operationId":"suspendDashboardPublication","owner":"DashboardPublications","surface":"api"}`,
	}
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
