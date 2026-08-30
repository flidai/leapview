package postgres

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/dashboard/document"
	eventpostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testDBTX struct{ *pgxpool.Pool }

func TestCheckedInt64RejectsPostgreSQLBigintOverflow(t *testing.T) {
	if got, err := checkedInt64(uint64(math.MaxInt64), "revision"); err != nil || got != math.MaxInt64 {
		t.Fatalf("checked maximum = %d, %v", got, err)
	}
	if _, err := checkedInt64(uint64(math.MaxInt64)+1, "revision"); err == nil || !strings.Contains(err.Error(), "exceeds PostgreSQL bigint range") {
		t.Fatalf("overflow error = %v", err)
	}
}

func authoringDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type) VALUES ($1::uuid,'user') ON CONFLICT (id) DO NOTHING`, "018f4f2e-0000-7000-0000-000000000601"); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertAuthoringEvidence(t *testing.T, db *pgxpool.Pool, eventID, projectID, dashboardID string) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"kind":"schema_test"}`)
	const eventType = "schema_test"
	if _, err := eventpostgres.New().AppendEvent(t.Context(), tx, eventpostgres.EventInput{
		EventID: eventID, ScopeID: projectID, AggregateType: "dashboard_authoring", AggregateID: dashboardID,
		EventType: eventType, SchemaVersion: 1, CorrelationID: eventID, Payload: payload,
	}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := accesspostgres.New().RecordAuditEvent(t.Context(), tx, access.AuditIntent{
		EventID: eventID, DomainEventID: eventID, ScopeID: projectID, ActorID: "schema-test-actor",
		Source: "dashboard.authoring", Operation: "schemaTest", Action: eventType,
		ResourceKind: "dashboard", ResourceID: dashboardID, Capability: access.CapabilityResourceEdit,
		Outcome: "success", RequestID: eventID, CorrelationID: eventID,
		AggregateKey: "dashboard_authoring:" + projectID + ":" + dashboardID, AggregateSequence: 1,
		MetadataJSON: string(payload),
	}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPostgreSQL18NativeOpaqueIDsAndFence(t *testing.T) {
	db := authoringDB(t)
	var revisionType, draftType string
	if err := db.QueryRow(t.Context(), `SELECT (SELECT data_type FROM information_schema.columns WHERE table_schema='dashboard' AND table_name='authoring_revisions' AND column_name='revision_id'), (SELECT data_type FROM information_schema.columns WHERE table_schema='dashboard' AND table_name='authoring_drafts' AND column_name='draft_id')`).Scan(&revisionType, &draftType); err != nil {
		t.Fatal(err)
	}
	if revisionType != "uuid" || draftType != "uuid" {
		t.Fatalf("opaque authoring ID types = %q/%q, want uuid", revisionType, draftType)
	}
	if _, err := New(testDBTX{db}, nil, nil, nil); err == nil {
		t.Fatal("authoring PostgreSQL constructor accepted missing audit/event/fence ports")
	}
}

func TestNativeAuthoringUUIDBoundaryRejectsOpaqueAndNonV7IDs(t *testing.T) {
	repo, err := New(testDBTX{}, &authoringAudit{}, authoringEvents{}, &authoringFence{})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []authoring.RevisionID{"revision:not-a-uuid", "550e8400-e29b-41d4-a716-446655440000"} {
		if _, err := repo.GetRevision(t.Context(), projectgraph.ResourceID("project:test"), authoring.DashboardID("dashboard:test"), value); err == nil {
			t.Fatalf("GetRevision accepted non-native revision id %q", value)
		}
	}
}

func TestAuthoringSchemaRejectsInvalidRows(t *testing.T) {
	db := authoringDB(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_dashboards(project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status) VALUES ('project:invalid','dashboard:bad',$1::uuid,'Bad Slug','Title','model','private','draft')`, "018f4f2e-0000-7000-0000-000000000601"); err == nil {
		t.Fatal("invalid dashboard slug was accepted")
	}
	validEventID := "018f4f2e-0000-7000-0000-000000000621"
	insertAuthoringEvidence(t, db, validEventID, "project:invalid", "dashboard:valid")
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_dashboards(project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status,last_event_id) VALUES ('project:invalid','dashboard:valid',$1::uuid,'valid','Title','model','private','draft',$2::uuid)`, "018f4f2e-0000-7000-0000-000000000601", validEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_revisions(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at) VALUES ('project:invalid','dashboard:valid',$1::uuid,1,'{}'::jsonb,'not-a-digest','{}'::jsonb,clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000591"); err == nil {
		t.Fatal("invalid revision digest was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_revisions(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at) VALUES ('project:invalid','dashboard:valid',$1::uuid,1,'{}'::jsonb,$2,'{}'::jsonb,clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000594", "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_commands(project_id,dashboard_id,command_id,request_fingerprint,action,provenance_json,occurred_at) VALUES ('project:invalid','dashboard:valid',$1::uuid,'fingerprint','unknown','{}'::jsonb,clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000592"); err == nil {
		t.Fatal("invalid command action was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_revalidation_attempts(project_id,dashboard_id,generation_id,attempt_id,generation_identity_json,graph_digest,dependency_ids_json,authored_revision_id,authored_revision_number,authored_content_hash,prior_compiled_identity_json,status,error_code,error_message,attempted_at) VALUES ('project:invalid','dashboard:valid','generation',$1::uuid,'{}'::jsonb,$2,'[]'::jsonb,$3::uuid,1,$2,'{}'::jsonb,'failed','   ','failure',clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000595", "sha256:"+strings.Repeat("a", 64), "018f4f2e-0000-7000-0000-000000000594"); err == nil {
		t.Fatal("blank revalidation error code was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_revisions(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at) VALUES ('project:missing','dashboard:missing',$1::uuid,1,'{}'::jsonb,$2,'{}'::jsonb,clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000593", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("revision for missing dashboard was accepted")
	}
}

func TestAuthoringSchemaIdentityAndTransitionGuards(t *testing.T) {
	db := authoringDB(t)
	ctx := t.Context()
	owner := "018f4f2e-0000-7000-0000-000000000601"
	guardEventID := "018f4f2e-0000-7000-0000-000000000622"
	insertAuthoringEvidence(t, db, guardEventID, "project:guards", "dashboard:guards")
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_dashboards(project_id,dashboard_id,owner_principal_id,slug,title,semantic_model,visibility,status,last_event_id) VALUES ('project:guards','dashboard:guards',$1::uuid,'guards','Guards','model','private','draft',$2::uuid)`, owner, guardEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.authoring_dashboards SET owner_principal_id=$1::uuid WHERE project_id='project:guards' AND dashboard_id='dashboard:guards'`, "018f4f2e-0000-7000-0000-000000000602"); err == nil {
		t.Fatal("authoring dashboard owner identity was mutable")
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.authoring_dashboards SET status='published',updated_at=clock_timestamp() WHERE project_id='project:guards' AND dashboard_id='dashboard:guards'`); err != nil {
		t.Fatalf("draft to published transition failed: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.authoring_dashboards SET status='draft',updated_at=clock_timestamp() WHERE project_id='project:guards' AND dashboard_id='dashboard:guards'`); err == nil {
		t.Fatal("published dashboard transitioned back to draft")
	}
	revisionID := "018f4f2e-0000-7000-8000-000000000611"
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_revisions(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at) VALUES ('project:guards','dashboard:guards',$1::uuid,1,'{}'::jsonb,$2,'{}'::jsonb,clock_timestamp())`, revisionID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	draftID := "018f4f2e-0000-7000-8000-000000000612"
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_drafts(project_id,dashboard_id,draft_id,revision_id,revision_number,content_hash,provenance_json) VALUES ('project:guards','dashboard:guards',$1::uuid,$2::uuid,1,$3,'{}'::jsonb)`, draftID, revisionID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.authoring_drafts SET revision_number=3 WHERE project_id='project:guards' AND dashboard_id='dashboard:guards'`); err == nil {
		t.Fatal("authoring draft revision skipped a transition")
	}
	compiledID := revisionID
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_compiled_revisions(project_id,dashboard_id,revision_id,revision_number,content_hash,definition_json,definition_hash,semantic_model_id,semantic_identity_json,compiled_at) VALUES ('project:guards','dashboard:guards',$1::uuid,1,$2,'{}'::jsonb,$3,'model','{}'::jsonb,clock_timestamp())`, compiledID, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.authoring_published(project_id,dashboard_id,revision_id,revision_number,content_hash,compiled_revision_id,compiled_revision_number,compiled_content_hash,compiled_definition_hash,compiled_semantic_model_id,compiled_semantic_identity_json,provenance_json,published_at) VALUES ('project:guards','dashboard:guards',$1::uuid,1,$2,$3::uuid,1,$2,$4,'model','{}'::jsonb,'{}'::jsonb,clock_timestamp())`, revisionID, "sha256:"+strings.Repeat("a", 64), compiledID, "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.authoring_published SET project_id='project:other' WHERE project_id='project:guards' AND dashboard_id='dashboard:guards'`); err == nil {
		t.Fatal("authoring published identity was mutable")
	}
}

var _ DBTX = testDBTX{}

// authoringAudit and authoringEvents are transaction-scoped capability fakes:
// they intentionally never commit or open a second transaction. The tests
// therefore exercise the repository's source transaction and rollback paths.
type authoringAudit struct {
	mu  sync.Mutex
	err error
}

func (a *authoringAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	// The fixture's opaque actor intentionally is not a typed access principal.
	// Preserve it in actor_id while leaving principal_id NULL for the canonical
	// PostgreSQL audit adapter.
	intent.PrincipalID = ""
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

type authoringEvents struct{}

func (authoringEvents) AppendEvent(ctx context.Context, tx Tx, input EventInput) (Event, error) {
	stored, err := eventpostgres.New().AppendEvent(ctx, tx, eventpostgres.EventInput{
		EventID: input.EventID, ScopeID: input.ProjectID, AggregateType: "dashboard_authoring", AggregateID: input.DashboardID,
		EventType: input.Type, SchemaVersion: 1, CorrelationID: input.CorrelationID, Payload: input.Payload,
	})
	if err != nil {
		return Event{}, err
	}
	return Event{EventID: stored.EventID, ProjectID: stored.ScopeID, DashboardID: stored.AggregateID, ActorID: input.ActorID, CorrelationID: stored.CorrelationID, Revision: input.Revision, AggregateVersion: stored.AggregateVersion, Type: stored.EventType, Payload: stored.Payload}, nil
}

type countingAuthoringEvents struct {
	mu    sync.Mutex
	count int
}

func (e *countingAuthoringEvents) AppendEvent(ctx context.Context, tx Tx, input EventInput) (Event, error) {
	e.mu.Lock()
	e.count++
	e.mu.Unlock()
	return authoringEvents{}.AppendEvent(ctx, tx, input)
}

type countingAuthoringAudit struct {
	mu    sync.Mutex
	count int
}

// postgresAuthoringAudit and postgresAuthoringEvents are the production
// capability adapters used by the runtime-role conformance test below. They
// deliberately retain the caller-owned transaction and never commit it.
type postgresAuthoringAudit struct{}

func (postgresAuthoringAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	intent.PrincipalID = ""
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

type postgresAuthoringEvents struct{}

func (postgresAuthoringEvents) AppendEvent(ctx context.Context, tx Tx, input EventInput) (Event, error) {
	return authoringEvents{}.AppendEvent(ctx, tx, input)
}

func (a *countingAuthoringAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	a.mu.Lock()
	a.count++
	a.mu.Unlock()
	intent.PrincipalID = ""
	_, err := accesspostgres.New().RecordAuditEvent(ctx, tx, intent)
	return err
}

type authoringFence struct {
	mu  sync.Mutex
	err error
}

func (f *authoringFence) ValidateActiveGeneration(context.Context, Tx, projectgraph.ServingIdentity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

type authoringFixture struct {
	db         *pgxpool.Pool
	repo       *Repository
	audit      *authoringAudit
	fence      *authoringFence
	project    projectgraph.ResourceID
	dashboard  authoring.DashboardID
	document   document.DashboardDocument
	provenance authoring.Provenance
	revision   authoring.Revision
	lifecycle  authoring.DashboardLifecycle
}

func newAuthoringFixture(t *testing.T) authoringFixture {
	t.Helper()
	db := authoringDB(t)
	audit, fence := &authoringAudit{}, &authoringFence{}
	repo, err := New(testDBTX{db}, audit, authoringEvents{}, fence)
	if err != nil {
		t.Fatal(err)
	}
	project := projectgraph.ResourceID("project:sales")
	dashboard := authoring.DashboardID("dashboard:sales")
	const sourceYAML = `apiVersion: leapview.dev/v1
kind: Dashboard
metadata:
  id: dashboard:sales
  name: sales
  displayName: Sales
spec:
  semanticModel: sales
  filters: []
  visuals:
    revenue:
      type: bar
      title: Revenue
      query:
        type: aggregate
        dimensions: [month]
        metrics: [revenue]
      presentation:
        type: cartesian
  pages:
    - id: overview
      title: Overview
      components:
        - id: revenue-component
          type: visual
          visual: revenue
          placement:
            column: 1
            row: 1
            columnSpan: 12
            rowSpan: 6
`
	var doc document.DashboardDocument
	if err := decodeDashboardFixture(sourceYAML, &doc); err != nil {
		t.Fatal(err)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor", ConversationID: "conversation", ToolCallID: "tool"}
	revision, err := authoring.NewRevision("018f4f2e-0000-7000-8000-000000001001", dashboard, 1, time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: project, ID: dashboard, OwnerPrincipalID: "018f4f2e-0000-7000-0000-000000000601", Slug: "sales", Title: "Sales", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "018f4f2e-0000-7000-8000-000000001002", DashboardID: dashboard, Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	return authoringFixture{db: db, repo: repo, audit: audit, fence: fence, project: project, dashboard: dashboard, document: doc, provenance: provenance, revision: revision, lifecycle: lifecycle}
}

func decodeDashboardFixture(content string, destination *document.DashboardDocument) error {
	// DecodeResource is the same generated schema boundary used by production
	// authoring; this keeps the fixture representative without duplicating union
	// marshaling details in the test.
	return configschema.DecodeResource(configschema.KindDashboard, "authoring-postgres-test.yaml", []byte(content), destination)
}

func auditContext(eventID, action string) context.Context {
	metadata := `{"schemaVersion":1,"retention":"standard","payloadSchema":"dashboard.authoring.command.audit.v1","payload":{"operationId":"operation","projectId":"project:sales","dashboardId":"pending-dashboard","draftId":"pending-draft","origin":"ui"}}`
	return authoring.WithAuditIntent(context.Background(), access.AuditIntent{EventID: eventID, Source: "dashboard.authoring", Operation: "executeDashboardAuthoringCommand", PrincipalID: "actor", Action: action, Capability: access.CapabilityResourceEdit, Outcome: "success", RequestID: eventID, CorrelationID: eventID, MetadataJSON: metadata})
}

func commandEvidence(id, action string, provenance authoring.Provenance, at time.Time) authoring.CommandEvidence {
	return authoring.CommandEvidence{ID: authoring.CommandID(id), Fingerprint: "sha256:" + strings.Repeat("a", 64), Action: authoring.AuthorizationAction(action), Provenance: provenance, OccurredAt: at}
}

func uuidv7(id string) string { return uuid.MustParse(id).String() }

func TestRepositoryPostgreSQL18AuthoringLifecycleCASReplayRollbackAndFence(t *testing.T) {
	f := newAuthoringFixture(t)
	createOperation := authoring.CreateOperation{ProjectID: f.project, ActorID: "actor", Kind: "create", IdempotencyKey: "create-op", ConversationID: "conversation", ToolCallID: "tool", Fingerprint: "sha256:" + strings.Repeat("b", 64)}
	created, err := f.repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001003"), "dashboard_authoring.draft_created"), authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision, Operation: createOperation})
	if err != nil {
		t.Fatal(err)
	}
	if created.Draft == nil || created.Draft.Revision != f.revision.Token() {
		t.Fatalf("created lifecycle = %#v", created)
	}
	// Durable create replay returns the original identity, not a duplicate.
	replayed, err := f.repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001003"), "dashboard_authoring.draft_created"), authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision, Operation: createOperation})
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("create replay = %#v (%v)", replayed, err)
	}

	updatedDoc, err := f.document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	title := "Sales Updated"
	updatedDoc.Metadata.DisplayName = &title
	revision2, err := authoring.NewRevision("018f4f2e-0000-7000-8000-000000001004", f.dashboard, 2, time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC), updatedDoc, f.provenance)
	if err != nil {
		t.Fatal(err)
	}
	next := created
	next.Title = title
	next.Draft = &authoring.Draft{ID: created.Draft.ID, DashboardID: f.dashboard, Revision: revision2.Token(), Provenance: f.provenance}
	appendEvidence := commandEvidence("018f4f2e-0000-7000-8000-000000001005", "edit", f.provenance, revision2.CreatedAt)
	appended, err := f.repo.AppendDraft(auditContext("018f4f2e-0000-7000-8000-000000001005", "dashboard_authoring.draft_edited"), authoring.AppendDraftInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: f.revision.Token(), Revision: revision2, Next: next, Evidence: appendEvidence})
	if err != nil || appended.Token() != revision2.Token() {
		t.Fatalf("append = %#v (%v)", appended, err)
	}
	// A stale expected token loses the CAS and leaves no third revision behind.
	staleDoc, _ := updatedDoc.Clone()
	revision3, _ := authoring.NewRevision("018f4f2e-0000-7000-8000-000000001006", f.dashboard, 3, time.Date(2026, 8, 30, 12, 2, 0, 0, time.UTC), staleDoc, f.provenance)
	staleNext := next
	staleNext.Draft = &authoring.Draft{ID: created.Draft.ID, DashboardID: f.dashboard, Revision: revision3.Token(), Provenance: f.provenance}
	_, err = f.repo.AppendDraft(auditContext("018f4f2e-0000-7000-8000-000000001006", "dashboard_authoring.draft_edited"), authoring.AppendDraftInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: f.revision.Token(), Revision: revision3, Next: staleNext, Evidence: commandEvidence("018f4f2e-0000-7000-8000-000000001007", "edit", f.provenance, revision3.CreatedAt)})
	if !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale append error = %v", err)
	}
	var revisions int
	if err := f.db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.authoring_revisions`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("revision count = %d (%v)", revisions, err)
	}

	// Reusing the successful command ID returns its durable result without a
	// second revision or audit event.
	replayedRevision, err := f.repo.AppendDraft(auditContext("018f4f2e-0000-7000-8000-000000001005", "dashboard_authoring.draft_edited"), authoring.AppendDraftInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: f.revision.Token(), Revision: revision2, Next: next, Evidence: appendEvidence})
	if err != nil || replayedRevision.Token() != revision2.Token() {
		t.Fatalf("append replay = %#v (%v)", replayedRevision, err)
	}

	identity1, _ := projectgraph.NewServingIdentity(f.project, "test", "generation-1")
	definition := dashboarddefinition.Definition{ID: f.dashboard.String(), Title: title, SemanticModel: "sales"}
	compiled, err := authoring.NewCompiledRevision(f.project, f.dashboard, revision2.Token(), definition, identity1, time.Date(2026, 8, 30, 12, 3, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	publishEvidence := commandEvidence("018f4f2e-0000-7000-8000-000000001008", "publish", f.provenance, compiled.CompiledAt)
	published, err := f.repo.Publish(auditContext("018f4f2e-0000-7000-8000-000000001008", "dashboard_authoring.published"), authoring.PublishInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: revision2.Token(), Published: authoring.Published{Revision: revision2.Token(), Compilation: compiled.Token(), PublishedAt: compiled.CompiledAt, Provenance: f.provenance}, Compilation: compiled, Evidence: publishEvidence})
	if err != nil || published.Status != authoring.LifecycleStatusPublished || published.Published == nil {
		t.Fatalf("publish = %#v (%v)", published, err)
	}

	// Audit failure rolls back the immutable revision and pointer mutation.
	f.audit.mu.Lock()
	f.audit.err = errors.New("audit unavailable")
	f.audit.mu.Unlock()
	rollbackNext := published
	rollbackNext.Draft = &authoring.Draft{ID: published.Draft.ID, DashboardID: f.dashboard, Revision: revision3.Token(), Provenance: f.provenance}
	_, err = f.repo.AppendDraft(auditContext("018f4f2e-0000-7000-8000-000000001009", "dashboard_authoring.draft_edited"), authoring.AppendDraftInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: revision2.Token(), Revision: revision3, Next: rollbackNext, Evidence: commandEvidence("018f4f2e-0000-7000-8000-000000001009", "edit", f.provenance, revision3.CreatedAt)})
	if err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("audit rollback error = %v", err)
	}
	f.audit.mu.Lock()
	f.audit.err = nil
	f.audit.mu.Unlock()
	if got, err := f.repo.Get(t.Context(), f.project, f.dashboard); err != nil || got.Draft.Revision != revision2.Token() {
		t.Fatalf("rollback lifecycle = %#v (%v)", got, err)
	}

	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: f.project, Kind: projectgraph.KindProject, Name: "sales"}, {ID: f.dashboard, Kind: projectgraph.KindDashboard, Name: "sales-dashboard"}, {ID: "sales", Kind: projectgraph.KindSemanticModel, Name: "sales-model"}}, []projectgraph.Edge{{From: f.dashboard, To: "sales"}})
	if err != nil {
		t.Fatal(err)
	}
	identity2, _ := projectgraph.NewServingIdentity(f.project, "test", "generation-2")
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity2, graphValue, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.repo.Get(t.Context(), f.project, f.dashboard)
	if err != nil {
		t.Fatal(err)
	}
	compiled2, err := authoring.NewCompiledRevision(f.project, f.dashboard, revision2.Token(), definition, identity2, time.Date(2026, 8, 30, 12, 4, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	generation := authoring.RevalidationGeneration{Identity: identity2, Graph: graphValue, Authorization: authorization, ChangedIDs: []projectgraph.ResourceID{"sales"}}
	commit := authoring.RevalidationCommit{AttemptID: "018f4f2e-0000-7000-8000-000000000011", Generation: generation, Dashboard: current, AuthoredRevision: revision2, PriorCompilation: current.Published.Compilation, Compilation: compiled2, DependencyIDs: []projectgraph.ResourceID{"sales"}, AttemptedAt: compiled2.CompiledAt}
	f.fence.mu.Lock()
	f.fence.err = errors.New("generation superseded")
	f.fence.mu.Unlock()
	if err := f.repo.CommitRevalidation(t.Context(), commit); err == nil || !strings.Contains(err.Error(), "generation superseded") {
		t.Fatalf("fence error = %v", err)
	}
	var attempts int
	if err := f.db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.authoring_revalidation_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("fenced attempt count = %d (%v)", attempts, err)
	}
	f.fence.mu.Lock()
	f.fence.err = nil
	f.fence.mu.Unlock()
	failureInput := authoring.RevalidationFailureInput{
		AttemptID: "018f4f2e-0000-7000-8000-000000000014", Generation: generation, Dashboard: current,
		AuthoredRevision: revision2, PriorCompilation: current.Published.Compilation,
		DependencyIDs: []projectgraph.ResourceID{"sales"},
		Failure:       authoring.RevalidationFailure{Identity: identity2, DependencyIDs: []projectgraph.ResourceID{"sales"}, Code: "REVALIDATION_FAILED", Message: "generation changed", FailedAt: time.Date(2026, 8, 30, 12, 5, 0, 0, time.UTC)},
	}
	f.fence.mu.Lock()
	f.fence.err = authoring.ErrGenerationSuperseded
	f.fence.mu.Unlock()
	if err := f.repo.RecordRevalidationFailure(t.Context(), failureInput); !errors.Is(err, authoring.ErrGenerationSuperseded) {
		t.Fatalf("fenced failure error = %v", err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.authoring_revalidation_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("fenced failure attempt count = %d (%v)", attempts, err)
	}
	f.fence.mu.Lock()
	f.fence.err = nil
	f.fence.mu.Unlock()
	commits := []authoring.RevalidationCommit{commit, commit}
	// ProjectGraph.Validate lazily materializes internal indexes. Give each
	// concurrent transaction its own immutable graph copy so the test mirrors
	// separate activation snapshots without racing on validation caches.
	graphCopy, err := projectgraph.NewProjectGraph(graphValue.Resources(), graphValue.Edges())
	if err != nil {
		t.Fatal(err)
	}
	authorizationCopy, err := accesssnapshot.NewAuthorizationSnapshot(identity2, graphCopy, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	commits[1].Generation = authoring.RevalidationGeneration{Identity: identity2, Graph: graphCopy, Authorization: authorizationCopy, ChangedIDs: []projectgraph.ResourceID{"sales"}}
	commits[1].AttemptID = "018f4f2e-0000-7000-8000-000000000012"
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range commits {
		group.Add(1)
		go func(input authoring.RevalidationCommit) {
			defer group.Done()
			results <- f.repo.CommitRevalidation(t.Context(), input)
		}(candidate)
	}
	group.Wait()
	close(results)
	success, conflicts := 0, 0
	for result := range results {
		t.Logf("revalidation result: %v", result)
		if result == nil {
			success++
		} else if errors.Is(result, authoring.ErrRevalidationConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent revalidation error = %v", result)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("concurrent revalidation success=%d conflicts=%d", success, conflicts)
	}
}

func TestRepositoryPostgreSQL18ConcurrentCreateOperationReplay(t *testing.T) {
	f := newAuthoringFixture(t)
	op := authoring.CreateOperation{ProjectID: f.project, ActorID: "actor", Kind: "create", IdempotencyKey: "concurrent-create", ConversationID: "conversation", ToolCallID: "tool", Fingerprint: "sha256:" + strings.Repeat("c", 64)}
	input := authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision, Operation: op}
	start := make(chan struct{})
	results := make(chan struct {
		lifecycle authoring.DashboardLifecycle
		err       error
	}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- func() (result struct {
				lifecycle authoring.DashboardLifecycle
				err       error
			}) {
				result.lifecycle, result.err = f.repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001007"), "dashboard_authoring.draft_created"), input)
				return result
			}()
		}()
	}
	close(start)
	var first authoring.DashboardLifecycle
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if i == 0 {
			first = result.lifecycle
		} else if result.lifecycle.ID != first.ID || result.lifecycle.Draft == nil || first.Draft == nil || result.lifecycle.Draft.Revision != first.Draft.Revision {
			t.Fatalf("concurrent create results differ: first=%#v current=%#v", first, result.lifecycle)
		}
	}
	conflict := input
	conflict.Operation.Fingerprint = "sha256:" + strings.Repeat("d", 64)
	if _, err := f.repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001009"), "dashboard_authoring.draft_created"), conflict); !errors.Is(err, authoring.ErrCommandReuse) {
		t.Fatalf("different-fingerprint create error = %v", err)
	}
}

func TestRepositoryPostgreSQL18ConcurrentAppendCommandReplay(t *testing.T) {
	f := newAuthoringFixture(t)
	events, audits := &countingAuthoringEvents{}, &countingAuthoringAudit{}
	repo, err := New(testDBTX{f.db}, audits, events, f.fence)
	if err != nil {
		t.Fatal(err)
	}
	create := authoring.CreateInput{ProjectID: f.project, Lifecycle: f.lifecycle, Revision: f.revision, Operation: authoring.CreateOperation{ProjectID: f.project, ActorID: "actor", Kind: "create", IdempotencyKey: "append-seed", ConversationID: "conversation", ToolCallID: "tool", Fingerprint: "sha256:" + strings.Repeat("e", 64)}}
	created, err := repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001011"), "dashboard_authoring.draft_created"), create)
	if err != nil {
		t.Fatal(err)
	}
	events.mu.Lock()
	events.count = 0
	events.mu.Unlock()
	audits.mu.Lock()
	audits.count = 0
	audits.mu.Unlock()
	updatedDoc, err := f.document.Clone()
	if err != nil {
		t.Fatal(err)
	}
	title := "Sales Concurrent"
	updatedDoc.Metadata.DisplayName = &title
	revision2, err := authoring.NewRevision("018f4f2e-0000-7000-8000-000000001012", f.dashboard, 2, time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC), updatedDoc, f.provenance)
	if err != nil {
		t.Fatal(err)
	}
	next := created
	next.Title = title
	next.Draft = &authoring.Draft{ID: created.Draft.ID, DashboardID: f.dashboard, Revision: revision2.Token(), Provenance: f.provenance}
	evidence := commandEvidence("018f4f2e-0000-7000-8000-000000001013", "edit", f.provenance, revision2.CreatedAt)
	input := authoring.AppendDraftInput{ProjectID: f.project, DashboardID: f.dashboard, ExpectedDraftRevision: f.revision.Token(), Revision: revision2, Next: next, Evidence: evidence}
	start := make(chan struct{})
	results := make(chan struct {
		revision authoring.Revision
		err      error
	}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			var result struct {
				revision authoring.Revision
				err      error
			}
			result.revision, result.err = repo.AppendDraft(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001013"), "dashboard_authoring.draft_edited"), input)
			results <- result
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.revision.Token() != revision2.Token() {
			t.Fatalf("concurrent append result = %#v", result.revision)
		}
	}
	var revisions, commands int
	if err := f.db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.authoring_revisions WHERE project_id=$1 AND dashboard_id=$2`, f.project.String(), f.dashboard.String()).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.authoring_commands WHERE project_id=$1 AND dashboard_id=$2 AND command_id=$3::uuid`, f.project.String(), f.dashboard.String(), evidence.ID.String()).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 || commands != 1 {
		t.Fatalf("concurrent append rows revisions=%d commands=%d, want 2/1", revisions, commands)
	}
	events.mu.Lock()
	eventCount := events.count
	events.mu.Unlock()
	audits.mu.Lock()
	auditCount := audits.count
	audits.mu.Unlock()
	if eventCount != 1 || auditCount != 1 {
		t.Fatalf("concurrent append evidence events=%d audits=%d, want 1/1", eventCount, auditCount)
	}
}

func TestRepositoryPostgreSQL18RuntimeRoleUsesGuardedAuthoringMutation(t *testing.T) {
	// Reuse the canonical fixture values, but execute the mutation against a
	// separately provisioned database through the non-owner runtime role.
	seed := newAuthoringFixture(t)
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-authoring-secret", Login: true})
	database := h.NewDatabase(t, "")
	admin, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	tx, err := admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), eventpostgres.SchemaSQL()); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	// The standalone event capability intentionally leaves role policy to the
	// control-plane baseline. Mirror that narrow runtime event append surface
	// here so the real event adapter can participate in the same transaction.
	if _, err := tx.Exec(t.Context(), `GRANT USAGE ON SCHEMA event TO leapview_control_runtime;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA event TO leapview_control_runtime;
REVOKE UPDATE, DELETE ON event.event_log FROM leapview_control_runtime`); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type) VALUES ($1::uuid,'user')`, seed.lifecycle.OwnerPrincipalID); err != nil {
		t.Fatal(err)
	}
	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	repo, err := New(runtime, postgresAuthoringAudit{}, postgresAuthoringEvents{}, &authoringFence{})
	if err != nil {
		t.Fatal(err)
	}
	create := authoring.CreateInput{
		ProjectID: seed.project, Lifecycle: seed.lifecycle, Revision: seed.revision,
		Operation: authoring.CreateOperation{ProjectID: seed.project, ActorID: "actor", Kind: "create", IdempotencyKey: "runtime-create", ConversationID: "conversation", ToolCallID: "tool", Fingerprint: "sha256:" + strings.Repeat("c", 64)},
	}
	created, err := repo.Create(auditContext(uuidv7("018f4f2e-0000-7000-8000-000000001020"), "dashboard_authoring.draft_created"), create)
	if err != nil {
		t.Fatalf("runtime guarded create failed: %v", err)
	}
	if created.ID != seed.dashboard {
		t.Fatalf("runtime guarded create dashboard = %q, want %q", created.ID, seed.dashboard)
	}
	if _, err := runtime.Exec(t.Context(), `INSERT INTO dashboard.authoring_revisions(project_id,dashboard_id,revision_id,revision_number,document_json,content_hash,provenance_json,created_at) VALUES ('project:runtime','dashboard:runtime',$1::uuid,2,'{}'::jsonb,$2,'{}'::jsonb,clock_timestamp())`, "018f4f2e-0000-7000-8000-000000001021", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("runtime role unexpectedly inserted an authoring revision directly")
	}
	if _, err := runtime.Exec(t.Context(), `UPDATE dashboard.authoring_dashboards SET title='bypass' WHERE project_id=$1 AND dashboard_id=$2`, seed.project.String(), seed.dashboard.String()); err == nil {
		t.Fatal("runtime role unexpectedly updated an authoring dashboard directly")
	}
	var count int
	if err := runtime.QueryRow(t.Context(), `SELECT count(*) FROM dashboard.authoring_dashboards WHERE project_id=$1 AND dashboard_id=$2`, seed.project.String(), seed.dashboard.String()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runtime dashboard visibility count = %d, want 1", count)
	}
}
