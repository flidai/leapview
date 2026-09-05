package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/command"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationdb "github.com/flidai/leapview/internal/dashboard/publication/postgres/internal/db"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationAudit struct{}

func (integrationAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit.audit_event(audit_id,event_id,scope_id,actor_id,source,operation,action,resource_kind,resource_id,capability,outcome,correlation_id,aggregate_key,aggregate_sequence,metadata)
VALUES ($1::uuid,NULLIF($2,'')::uuid,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,NULLIF($12,'')::uuid,$13,$14,$15::jsonb)
ON CONFLICT (audit_id) DO NOTHING`, intent.EventID, intent.DomainEventID, intent.ScopeID, intent.ActorID, intent.Source, intent.Operation, intent.Action, intent.ResourceKind, intent.ResourceID, intent.Capability.String(), intent.Outcome, intent.CorrelationID, intent.AggregateKey, intent.AggregateSequence, coalesceAuditMetadata(intent.MetadataJSON))
	if err == nil {
		var count int
		_ = tx.QueryRow(ctx, `SELECT count(*) FROM audit.audit_event WHERE event_id=$1::uuid AND scope_id=$2 AND actor_id=$3 AND action=$4 AND resource_kind='publication' AND resource_id=$5 AND outcome='success' AND aggregate_key=$6 AND aggregate_sequence=$7`, intent.DomainEventID, intent.ScopeID, intent.ActorID, intent.Action, intent.ResourceID, intent.AggregateKey, intent.AggregateSequence).Scan(&count)
		if count != 1 {
			return fmt.Errorf("publication audit evidence was not persisted")
		}
	}
	return err
}

func coalesceAuditMetadata(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

type failingPublicationAudit struct{ err error }

func (r failingPublicationAudit) RecordAuditIntent(context.Context, Tx, access.AuditIntent) error {
	return r.err
}

type mismatchingPublicationAudit struct{}

func (mismatchingPublicationAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	intent.MetadataJSON = `{"tampered":true}`
	return persistPublicationAudit(ctx, tx, intent)
}

type capturingPublicationAudit struct {
	mu      sync.Mutex
	intents []access.AuditIntent
}

func persistPublicationAudit(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit.audit_event(audit_id,event_id,scope_id,actor_id,source,operation,action,resource_kind,resource_id,capability,outcome,correlation_id,aggregate_key,aggregate_sequence,metadata)
VALUES ($1::uuid,NULLIF($2,'')::uuid,NULLIF($3,''),NULLIF($4,''),$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,NULLIF($12,'')::uuid,$13,$14,$15::jsonb)
ON CONFLICT (audit_id) DO NOTHING`, intent.EventID, intent.DomainEventID, intent.ScopeID, intent.ActorID, intent.Source, intent.Operation, intent.Action, intent.ResourceKind, intent.ResourceID, intent.Capability.String(), intent.Outcome, intent.CorrelationID, intent.AggregateKey, intent.AggregateSequence, coalesceAuditMetadata(intent.MetadataJSON))
	return err
}

func (r *capturingPublicationAudit) RecordAuditIntent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	if err := persistPublicationAudit(ctx, tx, intent); err != nil {
		return err
	}
	r.mu.Lock()
	r.intents = append(r.intents, intent)
	r.mu.Unlock()
	return nil
}

type integrationEvents struct{}

func (integrationEvents) AppendEvent(ctx context.Context, tx Tx, input EventInput) (Event, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO event.event_aggregate(scope_id,aggregate_type,aggregate_id,next_version) VALUES ($1,'dashboard_publication',$2,$3) ON CONFLICT DO NOTHING`, input.ProjectID, input.PublicationID, input.Revision+1); err != nil {
		return Event{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO event.event_log(event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,correlation_id,payload) VALUES ($1::uuid,$2,'dashboard_publication',$3,$4,$5,1,NULLIF($6,'')::uuid,$7::jsonb) ON CONFLICT (event_id) DO NOTHING`, input.EventID, input.ProjectID, input.PublicationID, input.Revision, input.Type, input.CorrelationID, input.Payload); err != nil {
		return Event{}, err
	}
	return Event{EventID: input.EventID, ProjectID: input.ProjectID, PublicationID: input.PublicationID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Type: input.Type, ServingStateID: input.ServingStateID, Revision: input.Revision, AggregateVersion: input.Revision, Payload: input.Payload}, nil
}

func publicationDB(t *testing.T) *pgxpool.Pool {
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
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
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
	return db
}

func reconciledPublication(t *testing.T) (*pgxpool.Pool, *Repository, publication.Publication) {
	t.Helper()
	db := publicationDB(t)
	repo, err := New(db, integrationAudit{}, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	projectID := projectgraph.ResourceID("project:streams")
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := publication.ReconcileInput{ProjectID: projectID, ServingStateID: "generation-streams", ActorID: "principal:owner", Publications: map[string]publication.Definition{
		"website": {Name: "website", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:" + "b" + strings.Repeat("0", 63)},
	}}
	if err := repo.ReconcileTx(t.Context(), tx, input, func(context.Context, Tx, projectgraph.ResourceID, string) error { return nil }); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(t.Context(), projectID, "website")
	if err != nil {
		t.Fatal(err)
	}
	return db, repo, row
}

func TestRepositoryPostgreSQL18StreamsReconnectCASRegistrationFenceAndMaintenance(t *testing.T) {
	db, _, publicationRow := reconciledPublication(t)
	version := publication.StreamVersion{PublicID: publicationRow.PublicID, ServingStateID: publicationRow.ServingStateID}
	registryA := NewStreamRegistry(db)
	streamCtx, closeA, err := registryA.Register(t.Context(), publicationRow.ID, "stream-1", version)
	if err != nil {
		t.Fatal(err)
	}
	if !registryA.Active(publicationRow.ID, "stream-1", version) {
		t.Fatal("new stream registration is not active")
	}
	prepared, generation, err := registryA.PrepareCommand(t.Context(), publicationRow.ID, "stream-1", version, func(filters dashboard.Filters) (command.PreparedRefresh, error) {
		return command.PreparedRefresh{Filters: filters.WithDefaults()}, nil
	})
	if err != nil || generation != 2 || prepared.Filters.Selections == nil {
		t.Fatalf("prepare command = %#v generation=%d err=%v", prepared, generation, err)
	}
	// A second registration fences the first. The old cleanup cannot expire the
	// newer durable registration because deletion is registration-id scoped.
	streamCtxB, closeB, err := registryA.Register(t.Context(), publicationRow.ID, "stream-1", version)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("superseded stream registration was not canceled")
	}
	if !registryA.Active(publicationRow.ID, "stream-1", version) {
		t.Fatal("new stream registration is inactive")
	}
	closeA()
	if !registryA.Active(publicationRow.ID, "stream-1", version) {
		t.Fatal("old registration cleanup expired the replacement")
	}

	// A second process observes the durable registration fence and removes its
	// stale local stream during reconciliation.
	registryB := NewStreamRegistry(db)
	_, closeB2, err := registryB.Register(t.Context(), publicationRow.ID, "stream-1", version)
	if err != nil {
		t.Fatal(err)
	}
	registryA.Reconcile(t.Context(), map[string]publication.StreamVersion{publicationRow.ID: version})
	select {
	case <-streamCtxB.Done():
	case <-time.After(time.Second):
		t.Fatal("durable registration fence did not cancel stale local stream")
	}
	closeB()
	closeB2()

	brokerA := NewBroker(nil)
	first, stopFirst := brokerA.SubscribeForPublication(publicationRow.ID, "stream-2")
	defer stopFirst()
	envelope1 := dashboardstream.Envelope{Signals: pagestream.SignalPatch{"status": "first"}, Delivery: dashboardstream.DeliveryMetadata{Generation: 1, Boundary: true}}
	brokerA.PublishEnvelopeForPublication(publicationRow.ID, "stream-2", envelope1)
	select {
	case patch := <-first:
		if patch["status"] != "first" {
			t.Fatalf("first broker patch = %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("first broker subscriber did not receive envelope")
	}
	second := dashboardstream.Envelope{Signals: pagestream.SignalPatch{"status": "second"}, Delivery: dashboardstream.DeliveryMetadata{Generation: 2, Boundary: true}}
	brokerA.PublishEnvelopeForPublication(publicationRow.ID, "stream-2", second)

	// Maintenance deletion is explicitly bounded; one invocation removes at
	// most one expired stream registration.
	if _, err := db.Exec(t.Context(), `UPDATE dashboard.publication_streams SET expires_at=clock_timestamp()-interval '1 hour' WHERE publication_id=$1::uuid`, publicationRow.ID); err != nil {
		t.Fatal(err)
	}
	if err := NewMaintenance(db).PruneExpired(t.Context(), time.Now().UTC(), time.Now().UTC(), 1); err != nil {
		t.Fatal(err)
	}
	var streamCount int
	if err := db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.publication_streams WHERE publication_id=$1::uuid`, publicationRow.ID).Scan(&streamCount); err != nil {
		t.Fatal(err)
	}
	if streamCount > 0 {
		t.Fatalf("bounded maintenance left stream rows=%d", streamCount)
	}
}

func TestRepositoryPostgreSQL18ReconcileMutationAndCAS(t *testing.T) {
	db := publicationDB(t)
	audit := &capturingPublicationAudit{}
	repo, err := New(db, audit, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileTx(t.Context(), nil, publication.ReconcileInput{ProjectID: projectgraph.ResourceID("project:publication"), ServingStateID: "generation-1", ActorID: "principal:owner"}, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil reconciliation transaction error = %v, want ErrUnavailable", err)
	}
	projectID := projectgraph.ResourceID("project:publication")
	input := publication.ReconcileInput{ProjectID: projectID, ServingStateID: "generation-1", ActorID: "principal:owner", Publications: map[string]publication.Definition{
		"website": {Name: "website", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileTx(t.Context(), tx, input, func(context.Context, Tx, projectgraph.ResourceID, string) error { return nil }); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(t.Context(), projectID, "website")
	if err != nil || row.Revision != 1 {
		t.Fatalf("reconciled row = %#v (%v)", row, err)
	}
	intent := access.AuditIntent{EventID: "018f4f2e-0000-7000-8000-000000000201", ActorID: "principal:owner", CorrelationID: "018f4f2e-0000-7000-8000-000000000202", Action: "dashboard_publication.suspended", AggregateKey: "dashboard_publication:" + projectID.String() + ":website"}
	mutated, err := repo.Suspend(publication.WithAuditIntent(t.Context(), intent), projectID, "website", "principal:owner", row.Revision)
	if err != nil || mutated.Revision != 2 || mutated.Status() != publication.StatusSuspended {
		t.Fatalf("suspend = %#v (%v)", mutated, err)
	}
	if _, err := repo.Resume(publication.WithAuditIntent(t.Context(), intent), projectID, "website", "principal:owner", row.Revision); !errors.Is(err, publication.ErrConflict) {
		t.Fatalf("stale resume error = %v", err)
	} else if kind, ok := apigenfailure.KindOf(err); !ok || kind != "precondition" {
		t.Fatalf("stale resume failure kind = %q (ok=%v), want precondition", kind, ok)
	}
	mismatch := access.AuditIntent{EventID: "018f4f2e-0000-7000-0000-000000000203", ActorID: "principal:owner", CorrelationID: "018f4f2e-0000-7000-0000-000000000204", Action: "dashboard_publication.rotated"}
	if _, err := repo.Resume(publication.WithAuditIntent(t.Context(), mismatch), projectID, "website", "principal:owner", mutated.Revision); err == nil || !strings.Contains(err.Error(), "does not match mutation event") {
		t.Fatalf("mismatched resume audit action error = %v", err)
	}
	retained, err := repo.Get(t.Context(), projectID, "website")
	if err != nil || retained.Revision != mutated.Revision || retained.Status() != publication.StatusSuspended {
		t.Fatalf("mismatched resume changed projection = %#v err=%v", retained, err)
	}
	rotateIntent := access.AuditIntent{EventID: "018f4f2e-0000-7000-8000-000000000205", ActorID: "principal:owner", CorrelationID: "018f4f2e-0000-7000-8000-000000000206", Action: "dashboard_publication.rotated", AggregateKey: "dashboard_publication:" + projectID.String() + ":website"}
	rotated, err := repo.Rotate(publication.WithAuditIntent(t.Context(), rotateIntent), projectID, "website", "principal:owner", mutated.Revision)
	if err != nil || rotated.Revision != mutated.Revision+1 || rotated.PublicID == mutated.PublicID || rotated.RotatedAt == "" {
		t.Fatalf("rotate = %#v (%v), want committed public-id rotation", rotated, err)
	}
	if events, err := repo.ListEvents(t.Context(), rotated.ID); err != nil || len(events) != 3 || events[0].Type != "dashboard_publication.rotated" {
		t.Fatalf("rotate events = %#v (%v), want latest rotated", events, err)
	}
	audit.mu.Lock()
	intents := append([]access.AuditIntent(nil), audit.intents...)
	audit.mu.Unlock()
	if len(intents) != 3 || intents[2].Action != "dashboard_publication.rotated" || intents[2].AggregateSequence != rotated.Revision {
		t.Fatalf("rotate audit intents = %#v, want committed rotated intent", intents)
	}
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ReconcileTx(t.Context(), tx, publication.ReconcileInput{ProjectID: projectID, ServingStateID: "generation-2", ActorID: "principal:owner", Publications: map[string]publication.Definition{}}, nil); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.Get(t.Context(), projectID, "website")
	if err != nil || disabled.Status() != publication.StatusUnconfigured {
		t.Fatalf("disabled publication = %#v (%v)", disabled, err)
	}
	resumeIntent := access.AuditIntent{EventID: "018f4f2e-0000-7000-8000-000000000207", ActorID: "principal:owner", CorrelationID: "018f4f2e-0000-7000-8000-000000000208", Action: "dashboard_publication.resumed"}
	if _, err := repo.Resume(publication.WithAuditIntent(t.Context(), resumeIntent), projectID, "website", "principal:owner", disabled.Revision); !errors.Is(err, publication.ErrConflict) {
		t.Fatalf("unconfigured resume error = %v, want publication conflict", err)
	} else if kind, ok := apigenfailure.KindOf(err); !ok || kind != "conflict" {
		t.Fatalf("unconfigured resume failure kind = %q (ok=%v), want conflict", kind, ok)
	}
}

func TestRepositoryPostgreSQL18ReconcileServingStateChange(t *testing.T) {
	db := publicationDB(t)
	audit := &capturingPublicationAudit{}
	repo, err := New(db, audit, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	projectID := projectgraph.ResourceID("project:serving-state")
	definition := publication.Definition{Name: "website", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:" + strings.Repeat("a", 64)}
	for generation, state := range []string{"generation-1", "generation-2"} {
		tx, txErr := db.Begin(t.Context())
		if txErr != nil {
			t.Fatal(txErr)
		}
		if txErr = repo.ReconcileTx(t.Context(), tx, publication.ReconcileInput{ProjectID: projectID, ServingStateID: state, ActorID: "principal:owner", Publications: map[string]publication.Definition{"website": definition}}, func(context.Context, Tx, projectgraph.ResourceID, string) error { return nil }); txErr != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(txErr)
		}
		if txErr = tx.Commit(t.Context()); txErr != nil {
			t.Fatal(txErr)
		}
		if generation == 0 {
			continue
		}
	}
	row, err := repo.Get(t.Context(), projectID, "website")
	if err != nil {
		t.Fatal(err)
	}
	if row.Revision != 2 || row.ServingStateID != "generation-2" {
		t.Fatalf("same-digest serving-state reconcile row=%#v, want revision 2 generation-2", row)
	}
	events, err := repo.ListEvents(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "dashboard_publication.serving_state_changed" {
		t.Fatalf("serving-state reconcile events=%#v, want latest serving_state_changed", events)
	}
	audit.mu.Lock()
	intents := append([]access.AuditIntent(nil), audit.intents...)
	audit.mu.Unlock()
	if len(intents) != 2 || intents[1].Action != "dashboard_publication.serving_state_changed" || intents[1].AggregateSequence != 2 {
		t.Fatalf("serving-state reconcile audits=%#v, want second serving_state_changed sequence 2", intents)
	}
}

func TestRepositoryPostgreSQL18PublicationMutationAuditRollback(t *testing.T) {
	db, _, row := reconciledPublication(t)
	repo, err := New(db, failingPublicationAudit{err: errors.New("audit unavailable")}, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{EventID: "018f4f2e-0000-7000-8000-000000000301", ActorID: "principal:owner", CorrelationID: "018f4f2e-0000-7000-8000-000000000302", Action: "dashboard_publication.suspended"}
	if _, err := repo.Suspend(publication.WithAuditIntent(t.Context(), intent), row.ProjectID, row.Name, "principal:owner", row.Revision); err == nil || !strings.Contains(err.Error(), "audit unavailable") {
		t.Fatalf("audit rollback error=%v", err)
	}
	retained, err := repo.Get(t.Context(), row.ProjectID, row.Name)
	if err != nil || retained.Revision != row.Revision || retained.Status() != publication.StatusActive {
		t.Fatalf("publication after rollback=%#v err=%v", retained, err)
	}
	var events int
	if err := db.QueryRow(t.Context(), `SELECT COUNT(*) FROM dashboard.publication_events WHERE publication_id=$1::uuid`, row.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("publication event count after rollback=%d, want 1", events)
	}
}

func TestRepositoryPostgreSQL18PublicationMutationAuditMetadataMismatchRollsBack(t *testing.T) {
	db, _, row := reconciledPublication(t)
	repo, err := New(db, mismatchingPublicationAudit{}, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	intent := access.AuditIntent{
		EventID: "018f4f2e-0000-7000-8000-000000000311", ActorID: "principal:owner",
		CorrelationID: "018f4f2e-0000-7000-8000-000000000312", Action: "dashboard_publication.suspended",
		MetadataJSON: `{"operationId":"suspend-dashboard-publication","owner":"dashboard","surface":"api"}`,
	}
	if _, err := repo.Suspend(publication.WithAuditIntent(t.Context(), intent), row.ProjectID, row.Name, "principal:owner", row.Revision); err == nil || !strings.Contains(err.Error(), "audit evidence") {
		t.Fatalf("metadata mismatch error=%v, want audit evidence failure", err)
	}
	retained, err := repo.Get(t.Context(), row.ProjectID, row.Name)
	if err != nil {
		t.Fatal(err)
	}
	if retained.Revision != row.Revision || retained.Status() != publication.StatusActive {
		t.Fatalf("publication after metadata rollback=%#v, want unchanged active row", retained)
	}
	var projectionEvents, auditEvents int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM dashboard.publication_events WHERE publication_id=$1::uuid`, row.ID).Scan(&projectionEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key=$1`, "dashboard_publication:"+row.ProjectID.String()+":"+row.Name).Scan(&auditEvents); err != nil {
		t.Fatal(err)
	}
	if projectionEvents != 1 || auditEvents != 1 {
		t.Fatalf("metadata mismatch left projection events=%d audit events=%d, want 1/1", projectionEvents, auditEvents)
	}
}

func TestRepositoryPostgreSQL18ProjectionReplayRejectsTamper(t *testing.T) {
	db := publicationDB(t)
	_, err := New(db, integrationAudit{}, integrationEvents{})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	row := publication.Publication{ID: "018f4f2e-0000-7000-8000-000000000211", ProjectID: "project:projection", Name: "website", Revision: 1, Configured: true, ServingStateID: "generation-1", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if _, err := tx.Exec(t.Context(), `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,revision,configured,active_serving_state_id,configured_at) VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,1,true,$8,clock_timestamp())`, row.ID, row.ProjectID.String(), row.Name, "public-id", row.Dashboard, row.DefaultPage, row.ConfigurationDigest, row.ServingStateID); err != nil {
		t.Fatal(err)
	}
	payload, err := publicationEventPayload(row, "dashboard_publication.suspended")
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "018f4f2e-0000-7000-8000-000000000212", PublicationID: row.ID, Revision: 1, AggregateVersion: 1, Type: "dashboard_publication.suspended", ActorID: "principal:owner", CorrelationID: "corr", ServingStateID: row.ServingStateID, Payload: payload}
	if err := recordProjectionEvent(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE dashboard.publication_events SET actor_id='tampered' WHERE domain_event_id=$1::uuid`, event.EventID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SAVEPOINT tampered_replay`); err != nil {
		t.Fatal(err)
	}
	if err := recordProjectionEvent(t.Context(), tx, event); err == nil || !strings.Contains(err.Error(), "publication event replay differs") {
		t.Fatalf("tampered replay error = %v", err)
	}
	if _, err := tx.Exec(t.Context(), `ROLLBACK TO SAVEPOINT tampered_replay`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE dashboard.publication_events SET actor_id='principal:owner' WHERE domain_event_id=$1::uuid`, event.EventID); err != nil {
		t.Fatal(err)
	}
	differentSequence := event
	differentSequence.AggregateVersion = 2
	if _, err := tx.Exec(t.Context(), `SAVEPOINT sequence_replay`); err != nil {
		t.Fatal(err)
	}
	if err := recordProjectionEvent(t.Context(), tx, differentSequence); err == nil || !strings.Contains(err.Error(), "publication event replay differs") {
		t.Fatalf("same event UUID with different aggregate sequence error = %v", err)
	}
	if _, err := tx.Exec(t.Context(), `ROLLBACK TO SAVEPOINT sequence_replay`); err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback(t.Context())
}

func TestRepositoryPostgreSQL18RuntimeMaintenanceRoleGrants(t *testing.T) {
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "runtime-secret", Login: true})
	maintenanceRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_maintenance", Password: "maintenance-secret", Login: true})
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
	if _, err := tx.Exec(t.Context(), eventspostgres.SchemaSQL()); err != nil {
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
	runtime, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	maintenance, err := pgxpool.New(t.Context(), database.URL(maintenanceRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)
	publicationID := "018f4f2e-0000-7000-8000-000000000401"
	registrationID := "018f4f2e-0000-7000-8000-000000000402"
	// Seed canonical evidence as an administrator. Runtime retains append rights
	// on the platform event/audit authorities, while dashboard projection DML
	// remains owner-function-bound.
	mutationEventID := "018f4f2e-0000-7000-8000-000000000403"
	mutationCorrelationID := "018f4f2e-0000-7000-8000-000000000404"
	if _, err := admin.Exec(t.Context(), `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at) VALUES ($1::uuid,'project:roles','website','public-roles','dashboard:website','overview','sha256:`+strings.Repeat("a", 64)+`','generation-roles',clock_timestamp())`, publicationID); err != nil {
		t.Fatal(err)
	}
	row := publication.Publication{ID: publicationID, ProjectID: projectgraph.ResourceID("project:roles"), Name: "website", PublicID: "public-roles", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:" + strings.Repeat("a", 64), Revision: 2, Configured: true, ServingStateID: "generation-roles", AllowedOrigins: []string{}, DependencyAssetIDs: []string{}, SuspendedBy: "principal:runtime"}
	payload, err := publicationEventPayload(row, "dashboard_publication.suspended")
	if err != nil {
		t.Fatal(err)
	}
	auditMetadata := []byte(`{"operationId":"runtime-test","owner":"runtime","surface":"test"}`)
	if _, err := admin.Exec(t.Context(), `INSERT INTO event.event_log(event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,correlation_id,payload) VALUES ($1::uuid,$2,'dashboard_publication',$3::text,1,$4,1,$5::uuid,$6::jsonb)`, mutationEventID, row.ProjectID.String(), publicationID, "dashboard_publication.suspended", mutationCorrelationID, payload); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO audit.audit_event(audit_id,event_id,scope_id,actor_id,source,operation,action,resource_kind,resource_id,capability,outcome,correlation_id,aggregate_key,aggregate_sequence,metadata) VALUES ($1::uuid,$1::uuid,$2,$3,'dashboard.publication','mutation','dashboard_publication.suspended','publication',$4,'RESOURCE_PUBLISH','success',$5::uuid,$6,1,$7::jsonb)`, mutationEventID, row.ProjectID.String(), "principal:runtime", publicationID, mutationCorrelationID, "dashboard_publication:"+row.ProjectID.String()+":"+row.Name, auditMetadata); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Exec(t.Context(), `SELECT dashboard.suspend_publication($1,'website','principal:runtime',1,$2::uuid,1,$3,$4::jsonb,'mutation','publication',$5,$6::jsonb)`, row.ProjectID.String(), mutationEventID, mutationCorrelationID, payload, publicationID, auditMetadata); err != nil {
		t.Fatalf("runtime guarded publication mutation failed: %v", err)
	}
	var revision int64
	if err := admin.QueryRow(t.Context(), `SELECT revision FROM dashboard.publications WHERE id=$1::uuid`, publicationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("runtime guarded mutation revision=%d, want 2", revision)
	}
	if _, err := runtime.Exec(t.Context(), `SELECT dashboard.suspend_publication($1,'website','principal:runtime',NULL,$2::uuid,1,$3,$4::jsonb,'mutation','publication',$5,$6::jsonb)`, row.ProjectID.String(), mutationEventID, mutationCorrelationID, payload, publicationID, auditMetadata); err == nil || !strings.Contains(err.Error(), "expected revision must be positive") {
		t.Fatalf("runtime NULL expected revision error=%v, want guarded rejection", err)
	}
	// A mismatched audit envelope must roll back the row update and private
	// projection event, even when the runtime can call the wrapper.
	mismatchPublicationID := "018f4f2e-0000-7000-8000-000000000407"
	mismatchEventID := "018f4f2e-0000-7000-8000-000000000408"
	mismatchCorrelationID := "018f4f2e-0000-7000-8000-000000000409"
	if _, err := admin.Exec(t.Context(), `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at) VALUES ($1::uuid,'project:roles','mismatch','public-mismatch','dashboard:mismatch','overview','sha256:`+strings.Repeat("c", 64)+`','generation-roles',clock_timestamp())`, mismatchPublicationID); err != nil {
		t.Fatal(err)
	}
	mismatchRow := publication.Publication{ID: mismatchPublicationID, ProjectID: row.ProjectID, Name: "mismatch", PublicID: "public-mismatch", Dashboard: "dashboard:mismatch", DefaultPage: "overview", ConfigurationDigest: "sha256:" + strings.Repeat("c", 64), Revision: 2, Configured: true, ServingStateID: "generation-roles", AllowedOrigins: []string{}, DependencyAssetIDs: []string{}, SuspendedBy: "principal:runtime"}
	mismatchPayload, err := publicationEventPayload(mismatchRow, "dashboard_publication.suspended")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO event.event_log(event_id,scope_id,aggregate_type,aggregate_id,aggregate_version,event_type,schema_version,correlation_id,payload) VALUES ($1::uuid,$2,'dashboard_publication',$3::text,1,$4,1,$5::uuid,$6::jsonb)`, mismatchEventID, mismatchRow.ProjectID.String(), mismatchPublicationID, "dashboard_publication.suspended", mismatchCorrelationID, mismatchPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(t.Context(), `INSERT INTO audit.audit_event(audit_id,event_id,scope_id,actor_id,source,operation,action,resource_kind,resource_id,capability,outcome,correlation_id,aggregate_key,aggregate_sequence,metadata) VALUES ($1::uuid,$1::uuid,$2,$3,'dashboard.publication','suspendDashboardPublication','dashboard_publication.suspended','project',$4,'RESOURCE_PUBLISH','success',$5::uuid,$6,1,'{}'::jsonb)`, mismatchEventID, mismatchRow.ProjectID.String(), "principal:runtime", mismatchRow.ProjectID.String(), mismatchCorrelationID, "dashboard_publication:"+mismatchRow.ProjectID.String()+":"+mismatchRow.Name); err != nil {
		t.Fatal(err)
	}
	mismatchAuditMetadata := []byte(`{"operationId":"suspendDashboardPublication","owner":"dashboard","surface":"api"}`)
	if _, err := runtime.Exec(t.Context(), `SELECT dashboard.suspend_publication($1,'mismatch','principal:runtime',1,$2::uuid,1,$3,$4::jsonb,'suspendDashboardPublication','project',$5,$6::jsonb)`, mismatchRow.ProjectID.String(), mismatchEventID, mismatchCorrelationID, mismatchPayload, mismatchRow.ProjectID.String(), mismatchAuditMetadata); err == nil || !strings.Contains(err.Error(), "audit evidence") {
		t.Fatalf("runtime mismatched audit metadata error=%v, want audit evidence failure", err)
	}
	if err := admin.QueryRow(t.Context(), `SELECT revision FROM dashboard.publications WHERE id=$1::uuid`, mismatchPublicationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("runtime mismatched audit metadata revision=%d, want rollback to 1", revision)
	}
	var mismatchEvents int
	if err := admin.QueryRow(t.Context(), `SELECT count(*) FROM dashboard.publication_events WHERE publication_id=$1::uuid`, mismatchPublicationID).Scan(&mismatchEvents); err != nil {
		t.Fatal(err)
	}
	if mismatchEvents != 0 {
		t.Fatalf("runtime mismatched audit metadata projection events=%d, want 0", mismatchEvents)
	}
	if _, err := runtime.Exec(t.Context(), `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest) VALUES ($1::uuid,'project:roles','forbidden','public-forbidden','dashboard:forbidden','overview','sha256:`+strings.Repeat("b", 64)+`')`, "018f4f2e-0000-7000-8000-000000000405"); err == nil {
		t.Fatal("runtime role unexpectedly has publication INSERT")
	}
	if _, err := runtime.Exec(t.Context(), `UPDATE dashboard.publications SET dashboard='dashboard:forbidden' WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("runtime role unexpectedly has publication UPDATE")
	}
	if _, err := runtime.Exec(t.Context(), `DELETE FROM dashboard.publications WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("runtime role unexpectedly has publication DELETE")
	}
	if _, err := runtime.Exec(t.Context(), `INSERT INTO dashboard.publication_events(publication_id,domain_event_id,aggregate_version,revision,event_type,payload_json) VALUES ($1::uuid,$2::uuid,9,9,'dashboard_publication.suspended','{}'::jsonb)`, publicationID, "018f4f2e-0000-7000-8000-000000000406"); err == nil {
		t.Fatal("runtime role unexpectedly has publication event INSERT")
	}
	if _, err := runtime.Exec(t.Context(), `SELECT dashboard.upsert_publication_stream($1::uuid,'role-stream','public-roles','generation-roles',$2::uuid,'{}'::jsonb,clock_timestamp()+interval '1 hour')`, publicationID, registrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Exec(t.Context(), `DELETE FROM dashboard.publication_streams WHERE publication_id=$1::uuid`, publicationID); err == nil {
		t.Fatal("runtime role unexpectedly has DELETE on stream registrations")
	}
	if _, err := maintenance.Exec(t.Context(), `SELECT dashboard.prune_expired_publication_streams(clock_timestamp()+interval '1 hour',1000)`); err != nil {
		t.Fatalf("maintenance role cannot prune stream registration: %v", err)
	}
	if _, err := maintenance.Exec(t.Context(), `SELECT 1 FROM dashboard.publications LIMIT 1`); err == nil {
		t.Fatal("maintenance role unexpectedly has publication projection read access")
	}
}

func TestPublicationSchemaRejectsInvalidRows(t *testing.T) {
	db := publicationDB(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest) VALUES ($1::uuid,'project:invalid','bad','public-bad','dashboard:bad','overview','not-a-digest')`, "018f4f2e-0000-7000-0000-000000000491"); err == nil {
		t.Fatal("invalid publication digest was accepted")
	}
	publicationID := "018f4f2e-0000-7000-0000-000000000492"
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at) VALUES ($1::uuid,'project:invalid','website','public-valid','dashboard:website','overview',$2,'generation',clock_timestamp())`, publicationID, "sha256:"+strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,configured,active_serving_state_id,configured_at,disabled_at) VALUES ($1::uuid,'project:invalid','bad-state','public-bad-state','dashboard:bad','overview',$2,false,'generation',clock_timestamp(),clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000495", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("inconsistent configured state tuple was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at,suspended_at) VALUES ($1::uuid,'project:invalid','bad-suspension','public-bad-suspension','dashboard:bad','overview',$2,'generation',clock_timestamp(),clock_timestamp())`, "018f4f2e-0000-7000-0000-000000000496", "sha256:"+strings.Repeat("a", 64)); err == nil {
		t.Fatal("inconsistent suspension tuple was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publication_events(publication_id,domain_event_id,aggregate_version,revision,event_type,payload_json) VALUES ($1::uuid,$2::uuid,1,1,'dashboard_publication.unknown','{}'::jsonb)`, publicationID, "018f4f2e-0000-7000-0000-000000000493"); err == nil {
		t.Fatal("invalid publication event type was accepted")
	}
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at) VALUES ($1::uuid,'invalid','public-valid','generation',$2::uuid,'[]'::jsonb,clock_timestamp())`, publicationID, "018f4f2e-0000-7000-0000-000000000494"); err == nil {
		t.Fatal("non-object stream filters were accepted")
	}
}

func TestPublicationSchemaIdentityRevisionRotationAndStreamGuards(t *testing.T) {
	db := publicationDB(t)
	ctx := t.Context()
	publicationID := "018f4f2e-0000-7000-0000-000000000497"
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at) VALUES ($1::uuid,'project:guards','website','public-guards','dashboard:website','overview',$2,'generation',clock_timestamp())`, publicationID, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publications SET dashboard='dashboard:other' WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("publication update without a revision advance was accepted")
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publications SET revision=revision+2 WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("publication revision advanced by more than one")
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publications SET revision=revision+1,public_id='public-rotated' WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("public_id changed without rotated_at")
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publications SET revision=revision+1,rotated_at=clock_timestamp() WHERE id=$1::uuid`, publicationID); err == nil {
		t.Fatal("rotated_at changed without public_id")
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publications SET revision=revision+1,public_id='public-rotated',rotated_at=clock_timestamp() WHERE id=$1::uuid`, publicationID); err != nil {
		t.Fatalf("valid publication rotation failed: %v", err)
	}
	registrationID := "018f4f2e-0000-7000-0000-000000000498"
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at) VALUES ($1::uuid,'stream-guards','public-rotated','generation',$2::uuid,'{}'::jsonb,clock_timestamp()+interval '1 hour')`, publicationID, registrationID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE dashboard.publication_streams SET stream_id='stream-other' WHERE publication_id=$1::uuid AND stream_id='stream-guards'`, publicationID); err == nil {
		t.Fatal("publication stream primary key was mutable")
	}
	var indexes int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE schemaname='dashboard' AND indexname='publication_events_publication_revision_idx'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("publication event revision index count = %d (%v)", indexes, err)
	}
}

func TestPublicationStreamExpiredHeartbeatCASStops(t *testing.T) {
	db := publicationDB(t)
	ctx := t.Context()
	publicationID := "018f4f2e-0000-7000-0000-000000000499"
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publications(id,project_id,name,public_id,dashboard,default_page,configuration_digest,active_serving_state_id,configured_at) VALUES ($1::uuid,'project:heartbeat','website','public-heartbeat','dashboard:website','overview',$2,'generation',clock_timestamp())`, publicationID, digest); err != nil {
		t.Fatal(err)
	}
	registrationID := "018f4f2e-0000-7000-0000-000000000500"
	if _, err := db.Exec(ctx, `INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at) VALUES ($1::uuid,'stream-heartbeat','public-heartbeat','generation',$2::uuid,'{}'::jsonb,clock_timestamp()-interval '1 minute')`, publicationID, registrationID); err != nil {
		t.Fatal(err)
	}
	q := publicationdb.New(db)
	pubUUID := uuid.MustParse(publicationID)
	regUUID := uuid.MustParse(registrationID)
	rows, err := q.ExtendStream(ctx, publicationdb.ExtendStreamParams{
		ExpiresAt: time.Now().UTC().Add(time.Hour), PublicationID: pgtype.UUID{Bytes: pubUUID, Valid: true},
		StreamID: "stream-heartbeat", PublicID: "public-heartbeat", ServingStateID: "generation",
		RegistrationID: pgtype.UUID{Bytes: regUUID, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("expired heartbeat affected %d rows, want zero", rows)
	}
}

func TestMaintenancePruneExpiredConcurrentClaimsAreBounded(t *testing.T) {
	db, _, row := reconciledPublication(t)
	ctx := t.Context()
	for i := 0; i < 4; i++ {
		streamID := fmt.Sprintf("expired-%d", i)
		registrationID := uuid.New()
		if _, err := db.Exec(ctx, `INSERT INTO dashboard.publication_streams(publication_id,stream_id,public_id,serving_state_id,registration_id,filters_json,expires_at) VALUES ($1::uuid,$2,$3,$4,$5,'{}'::jsonb,clock_timestamp()-interval '1 hour')`, row.ID, streamID, row.PublicID, row.ServingStateID, registrationID); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewMaintenance(db).PruneExpired(ctx, time.Now().UTC(), time.Now().UTC(), 1001); err == nil {
		t.Fatal("maintenance accepted a batch larger than the bounded maximum")
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			errs <- NewMaintenance(db).PruneExpired(ctx, time.Now().UTC(), time.Now().UTC(), 2)
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var streams int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM dashboard.publication_streams WHERE publication_id=$1::uuid AND stream_id LIKE 'expired-%'`, row.ID).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if streams != 0 {
		t.Fatalf("concurrent maintenance left streams=%d", streams)
	}
}

// recordProjectionEvent exercises the owner-only projection event boundary
// directly; production mutations reach it through guarded SQL functions.
func recordProjectionEvent(ctx context.Context, tx Tx, event Event) error {
	publicationID, err := nativeUUID(event.PublicationID)
	if err != nil {
		return err
	}
	domainEventID, err := nativeUUID(event.EventID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT dashboard.record_publication_event($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9::jsonb)`,
		publicationID, domainEventID, event.AggregateVersion, event.Revision, event.Type,
		event.ActorID, event.CorrelationID, event.ServingStateID, event.Payload)
	return err
}
