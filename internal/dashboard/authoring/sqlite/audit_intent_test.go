package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	authoringsqlite "github.com/flidai/leapview/internal/dashboard/authoring/sqlite"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/transaction"
)

func authoringAuditIntent(operation, action string) access.AuditIntent {
	return access.AuditIntent{
		EventID: "dashboard-authoring:pending", Source: "dashboard.authoring", Operation: operation,
		PrincipalID: "actor", Action: action, Capability: access.CapabilityResourceEdit, Outcome: "success",
		RequestID: "request-1", CorrelationID: "request-1",
		MetadataJSON: `{"operationId":"` + operation + `","projectId":"project:sales","dashboardId":"pending-dashboard","draftId":"pending-draft","origin":"agent"}`,
	}
}

type failingAuthoringAuditRecorder struct{ err error }

func (r failingAuthoringAuditRecorder) RecordAuditIntent(context.Context, transaction.Transaction, access.AuditIntent) error {
	return r.err
}

func openAuthoringAuditStore(t *testing.T) (*platform.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "authoring-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('owner', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func TestAuthoringCreateAuditIntentRollsBackAndReplaysIdempotently(t *testing.T) {
	store, ctx := openAuthoringAuditStore(t)
	input, _ := canonicalSQLiteInput(t, "project:sales", "dashboard:audit", "revision-1", authoring.CreateOperation{
		ProjectID: "project:sales", ActorID: "actor", Kind: "create", IdempotencyKey: "create-audit", Fingerprint: "fingerprint-audit",
	})
	intent := authoringAuditIntent("createDashboardAuthoringDraft", "dashboard_authoring.draft_created")
	boom := errors.New("audit unavailable")
	failing := authoringsqlite.NewRepositoryWithAudit(store.SQLDB(), failingAuthoringAuditRecorder{err: boom})
	if _, err := failing.Create(authoring.WithAuditIntent(ctx, intent), input); !errors.Is(err, boom) {
		t.Fatalf("create error = %v, want audit failure", err)
	}
	var dashboards, outbox int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_dashboards`).Scan(&dashboards); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if dashboards != 0 || outbox != 0 {
		t.Fatalf("rolled back state dashboards=%d outbox=%d", dashboards, outbox)
	}

	repository := authoringsqlite.NewRepositoryWithAudit(store.SQLDB(), accesssqlite.NewRepository(store.SQLDB()))
	created, err := repository.Create(authoring.WithAuditIntent(ctx, intent), input)
	if err != nil {
		t.Fatal(err)
	}
	var firstEvent, aggregate, metadata string
	var firstSequence int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT event_id, aggregate_key, aggregate_sequence, metadata_json FROM audit_outbox`).Scan(&firstEvent, &aggregate, &firstSequence, &metadata); err != nil {
		t.Fatal(err)
	}
	if aggregate != "dashboard_authoring:project:sales:dashboard:audit" || firstSequence != 1 {
		t.Fatalf("aggregate = %q sequence=%d", aggregate, firstSequence)
	}
	if firstEvent == "" {
		t.Fatal("audit event id is empty")
	}
	if strings.Contains(metadata, "pending-dashboard") || strings.Contains(metadata, "pending-draft") || !strings.Contains(metadata, `"dashboardId":"dashboard:audit"`) {
		t.Fatalf("audit metadata did not bind revision identity: %s", metadata)
	}
	replayed, err := repository.Create(authoring.WithAuditIntent(ctx, intent), input)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replay = %#v err=%v", replayed, err)
	}
	var outboxAfter int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_outbox`).Scan(&outboxAfter); err != nil {
		t.Fatal(err)
	}
	if outboxAfter != 1 {
		t.Fatalf("outbox rows after replay=%d, want 1", outboxAfter)
	}
}

func TestAuthoringAppendAuditIntentRollsBackRevisionAndDraftPointer(t *testing.T) {
	store, ctx := openAuthoringAuditStore(t)
	createInput, first := canonicalSQLiteInput(t, "project:sales", "dashboard:append-audit", "revision-1", authoring.CreateOperation{
		ProjectID: "project:sales", ActorID: "actor", Kind: "create", IdempotencyKey: "append-create", Fingerprint: "append-create-fingerprint",
	})
	createIntent := authoringAuditIntent("createDashboardAuthoringDraft", "dashboard_authoring.draft_created")
	repository := authoringsqlite.NewRepositoryWithAudit(store.SQLDB(), accesssqlite.NewRepository(store.SQLDB()))
	created, err := repository.Create(authoring.WithAuditIntent(ctx, createIntent), createInput)
	if err != nil {
		t.Fatal(err)
	}
	documentValue := canonicalSQLiteDocument(created.ID.String())
	documentValue.Metadata.DisplayName = stringPointer("Sales append")
	second, err := authoring.NewRevision("revision-2", created.ID, 2, time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC), documentValue, first.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	next := created
	next.Title = "Sales append"
	next.Draft = &authoring.Draft{ID: created.Draft.ID, DashboardID: created.ID, Revision: second.Token(), Provenance: second.Provenance}
	evidence := authoring.CommandEvidence{ID: "append-edit", Fingerprint: "append-fingerprint", Action: authoring.AuthorizationActionEdit, Provenance: first.Provenance, OccurredAt: second.CreatedAt}
	failing := authoringsqlite.NewRepositoryWithAudit(store.SQLDB(), failingAuthoringAuditRecorder{err: errors.New("audit write failed")})
	appendIntent := authoringAuditIntent("executeDashboardAuthoringCommand", "dashboard_authoring.command_executed")
	if _, err := failing.AppendDraft(authoring.WithAuditIntent(ctx, appendIntent), authoring.AppendDraftInput{ProjectID: "project:sales", DashboardID: created.ID, ExpectedDraftRevision: first.Token(), Revision: second, Next: next, Evidence: evidence}); err == nil {
		t.Fatal("append unexpectedly succeeded with failing audit recorder")
	}
	var revisionRows, commandRows int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_revisions WHERE dashboard_id = ?`, created.ID.String()).Scan(&revisionRows); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_authoring_commands WHERE dashboard_id = ?`, created.ID.String()).Scan(&commandRows); err != nil {
		t.Fatal(err)
	}
	if revisionRows != 1 || commandRows != 0 {
		t.Fatalf("rolled back append revisions=%d commands=%d", revisionRows, commandRows)
	}
	current, err := repository.Get(ctx, "project:sales", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Draft == nil || current.Draft.Revision != first.Token() {
		t.Fatalf("draft pointer after rollback=%#v", current.Draft)
	}
}

func TestAuthoringArchiveAuditIntentSupportsPublishedOnlyLifecycle(t *testing.T) {
	store, ctx := openAuthoringAuditStore(t)
	createInput, _ := canonicalSQLiteInput(t, "project:sales", "dashboard:published-only", "revision-published-only", authoring.CreateOperation{})
	repository := authoringsqlite.NewRepositoryWithAudit(store.SQLDB(), accesssqlite.NewRepository(store.SQLDB()))
	created, err := repository.Create(ctx, createInput)
	if err != nil {
		t.Fatal(err)
	}
	identity := servingIdentity(t, created.ProjectID.String(), "test", "generation-published-only")
	definition := dashboarddefinition.Definition{ID: created.ID.String(), Title: created.Title, SemanticModel: created.SemanticModel.String()}
	compiled, err := authoring.NewCompiledRevision(created.ProjectID, created.ID, created.Draft.Revision, definition, identity, time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	provenance := created.Draft.Provenance
	if _, err := repository.Publish(ctx, authoring.PublishInput{
		ProjectID: created.ProjectID, DashboardID: created.ID, ExpectedDraftRevision: created.Draft.Revision,
		Published:   authoring.Published{Revision: created.Draft.Revision, Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: provenance},
		Compilation: compiled,
		Evidence:    authoring.CommandEvidence{ID: "publish-published-only", Fingerprint: "publish-published-only", Action: authoring.AuthorizationActionPublish, Provenance: provenance, OccurredAt: compiled.CompiledAt},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `DELETE FROM dashboard_authoring_drafts WHERE project_id = ? AND dashboard_id = ?`, created.ProjectID.String(), created.ID.String()); err != nil {
		t.Fatal(err)
	}
	publishedOnly, err := repository.Get(ctx, created.ProjectID, created.ID)
	if err != nil || publishedOnly.Draft != nil || publishedOnly.Published == nil {
		t.Fatalf("published-only lifecycle = %#v (%v)", publishedOnly, err)
	}
	intent := authoringAuditIntent("archiveDashboardAuthoring", "dashboard_authoring.archived")
	intent.MetadataJSON = `{"operationId":"archiveDashboardAuthoring"}`
	archived, err := repository.Archive(authoring.WithAuditIntent(ctx, intent), authoring.ArchiveInput{
		ProjectID: created.ProjectID, DashboardID: created.ID, ExpectedCurrentRevision: publishedOnly.Published.Revision,
		Evidence: authoring.CommandEvidence{ID: "archive-published-only", Fingerprint: "archive-published-only", Action: authoring.AuthorizationActionArchive, Provenance: provenance, OccurredAt: time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Status != authoring.LifecycleStatusArchived {
		t.Fatalf("archived status = %q, want archived", archived.Status)
	}
	var metadata string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT metadata_json FROM audit_outbox WHERE aggregate_key = ?`, "dashboard_authoring:project:sales:dashboard:published-only").Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metadata, `"origin":"agent"`) || strings.Contains(metadata, `"draftId"`) {
		t.Fatalf("published-only archive metadata = %s", metadata)
	}
}
