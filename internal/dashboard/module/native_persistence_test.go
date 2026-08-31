package module_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	dashboardpublicationaudit "github.com/flidai/leapview/internal/app/dashboardpublicationaudit"
	dashboardpublicationevents "github.com/flidai/leapview/internal/app/dashboardpublicationevents"
	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardauthoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/dashboard/publication"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type nativeDBStub struct{}

func (nativeDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (nativeDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (nativeDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (nativeDBStub) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }

type nativeAuditStub struct{}

func (nativeAuditStub) RecordAuditEvent(context.Context, dashboardappearancepostgres.Tx, dashboardappearancepostgres.AuditInput) error {
	return nil
}

type nativeEventStub struct{}

func (nativeEventStub) AppendEvent(_ context.Context, _ dashboardappearancepostgres.Tx, input dashboardappearancepostgres.EventInput) (dashboardappearancepostgres.Event, error) {
	return dashboardappearancepostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, DashboardID: input.DashboardID, ActorID: input.ActorID, Revision: input.Revision, Patch: input.Patch, AggregateVersion: input.Revision}, nil
}

type nativeAuthoringAuditStub struct{}

func (nativeAuthoringAuditStub) RecordAuditIntent(context.Context, dashboardauthoringpostgres.Tx, access.AuditIntent) error {
	return nil
}

type nativeAuthoringEventStub struct{}

func (nativeAuthoringEventStub) AppendEvent(_ context.Context, _ dashboardauthoringpostgres.Tx, input dashboardauthoringpostgres.EventInput) (dashboardauthoringpostgres.Event, error) {
	return dashboardauthoringpostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, DashboardID: input.DashboardID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Revision: input.Revision, AggregateVersion: input.Revision, Type: input.Type, Payload: input.Payload}, nil
}

type nativeFenceStub struct{}

func (nativeFenceStub) ValidateActiveGeneration(context.Context, dashboardauthoringpostgres.Tx, projectgraph.ServingIdentity) error {
	return nil
}

type nativePublicationAuditStub struct{}

func (nativePublicationAuditStub) RecordAuditIntent(context.Context, dashboardpublicationpostgres.Tx, access.AuditIntent) error {
	return nil
}

type capturingNativePublicationAudit struct {
	count    int
	delegate dashboardpublicationpostgres.AuditPort
}

func (a *capturingNativePublicationAudit) RecordAuditIntent(ctx context.Context, tx dashboardpublicationpostgres.Tx, intent access.AuditIntent) error {
	if a.delegate != nil {
		if err := a.delegate.RecordAuditIntent(ctx, tx, intent); err != nil {
			return err
		}
	}
	a.count++
	return nil
}

type nativePublicationEventStub struct{}

func (nativePublicationEventStub) AppendEvent(_ context.Context, _ dashboardpublicationpostgres.Tx, input dashboardpublicationpostgres.EventInput) (dashboardpublicationpostgres.Event, error) {
	return dashboardpublicationpostgres.Event{EventID: input.EventID, ProjectID: input.ProjectID, PublicationID: input.PublicationID, ActorID: input.ActorID, CorrelationID: input.CorrelationID, Revision: input.Revision, AggregateVersion: input.Revision, Type: input.Type, ServingStateID: input.ServingStateID, Payload: input.Payload}, nil
}

func validNativePersistenceOptions(t *testing.T) dashboardmodule.NativePersistenceOptions {
	t.Helper()
	db := nativeDBStub{}
	sessions, err := dashboardsessionpostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := dashboardusagepostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	appearance, err := dashboardappearancepostgres.New(db, dashboardappearancepostgres.Options{Audit: nativeAuditStub{}, Events: nativeEventStub{}})
	if err != nil {
		t.Fatal(err)
	}
	authoring, err := dashboardauthoringpostgres.New(db, nativeAuthoringAuditStub{}, nativeAuthoringEventStub{}, nativeFenceStub{})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := dashboardpublicationpostgres.New(db, nativePublicationAuditStub{}, nativePublicationEventStub{})
	if err != nil {
		t.Fatal(err)
	}
	return dashboardmodule.NativePersistenceOptions{
		Session: sessions, Usage: usage, Appearance: appearance, Authoring: authoring, Publication: pub,
		Streams: dashboardpublicationpostgres.NewStreamRegistry(db), Broker: dashboardpublicationpostgres.NewBroker(nil),
	}
}

func validNativePersistence(t *testing.T) *dashboardmodule.NativePersistence {
	t.Helper()
	bundle, err := dashboardmodule.NewNativePersistence(validNativePersistenceOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestNativePersistenceMatchesExactAuthorities(t *testing.T) {
	options := validNativePersistenceOptions(t)
	bundle, err := dashboardmodule.NewNativePersistence(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Matches(options) {
		t.Fatal("native persistence did not match the exact constructor authorities")
	}

	tests := []struct {
		name   string
		mutate func(*dashboardmodule.NativePersistenceOptions, dashboardmodule.NativePersistenceOptions)
	}{
		{name: "session", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Session = other.Session
		}},
		{name: "usage", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Usage = other.Usage
		}},
		{name: "appearance", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Appearance = other.Appearance
		}},
		{name: "authoring", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Authoring = other.Authoring
		}},
		{name: "publication", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Publication = other.Publication
		}},
		{name: "streams", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Streams = other.Streams
		}},
		{name: "broker", mutate: func(options *dashboardmodule.NativePersistenceOptions, other dashboardmodule.NativePersistenceOptions) {
			options.Broker = other.Broker
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mismatched := options
			test.mutate(&mismatched, validNativePersistenceOptions(t))
			if bundle.Matches(mismatched) {
				t.Fatalf("native persistence matched a different %s authority", test.name)
			}
		})
	}
}

func TestNativePersistenceMatchesOnlyItsAuthoringRepository(t *testing.T) {
	options := validNativePersistenceOptions(t)
	bundle, err := dashboardmodule.NewNativePersistence(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.MatchesAuthoringRepository(options.Authoring) {
		t.Fatal("native persistence did not match its exact authoring repository")
	}
	other := validNativePersistenceOptions(t)
	if bundle.MatchesAuthoringRepository(other.Authoring) || bundle.MatchesAuthoringRepository(nil) {
		t.Fatal("native persistence matched a substituted or nil authoring repository")
	}
}

func TestBuildRequiresExplicitNativeDashboardAuthorities(t *testing.T) {
	_, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{RequireNativePersistence: true})
	if err == nil {
		t.Fatal("native dashboard build accepted missing persistence authorities")
	}
}

func TestBuildNativeDashboardUsesValidatedBundleWithoutSQLite(t *testing.T) {
	module, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{RequireNativePersistence: true, NativePersistence: validNativePersistence(t)})
	if err != nil {
		t.Fatal(err)
	}
	if module.AppearanceStore() == nil {
		t.Fatal("native dashboard build did not expose appearance authority")
	}
	if !module.PublicationsConfigured() {
		t.Fatal("native dashboard build did not mark publication audit/mutation authority ready")
	}
}

func TestBuildLegacyDatabaseUsesMemoryDashboardSessions(t *testing.T) {
	audit := access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error {
		return nil
	})
	module, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{
		Database: &sql.DB{}, LegacySQLite: true, AuditIntentRecorder: audit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := module.HTTP().SessionStore.(*dashboardsession.MemoryStore); !ok {
		t.Fatalf("legacy dashboard database selected session store %T, want *session.MemoryStore", module.HTTP().SessionStore)
	}
}

// TestBuildNativeDashboardMutationUsesNativeAudit exercises the composed
// module through its transport-neutral mutation adapter. A real PostgreSQL
// authority is required here because a native repository must execute its
// source transaction and invoke the transaction-scoped audit port; SQLite or
// nil database stand-ins would only prove the readiness flag.
func TestBuildNativeDashboardMutationUsesNativeAudit(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "dashboard_native_module")
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
	if err := dashboardappearancepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := dashboardauthoringpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := dashboardpublicationpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := dashboardsessionpostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := dashboardusagepostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}

	sessions, err := dashboardsessionpostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := dashboardusagepostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	appearance, err := dashboardappearancepostgres.New(db, dashboardappearancepostgres.Options{Audit: nativeAuditStub{}, Events: nativeEventStub{}})
	if err != nil {
		t.Fatal(err)
	}
	authoring, err := dashboardauthoringpostgres.New(db, nativeAuthoringAuditStub{}, nativeAuthoringEventStub{}, nativeFenceStub{})
	if err != nil {
		t.Fatal(err)
	}
	events := eventspostgres.New()
	audit := &capturingNativePublicationAudit{delegate: dashboardpublicationaudit.NewWithRepository(accesspostgres.New())}
	publications, err := dashboardpublicationpostgres.New(db, audit, dashboardpublicationevents.NewWithRepository(events))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := dashboardmodule.NewNativePersistence(dashboardmodule.NativePersistenceOptions{
		Session: sessions, Usage: usage, Appearance: appearance, Authoring: authoring, Publication: publications,
		Streams: dashboardpublicationpostgres.NewStreamRegistry(db), Broker: dashboardpublicationpostgres.NewBroker(nil),
	})
	if err != nil {
		t.Fatal(err)
	}

	projectID := projectgraph.ResourceID("project_native")
	principalID := uuid.MustParse("018f4f2e-0000-7000-8000-000000000730").String()
	tx, err = db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := publications.ReconcileTx(t.Context(), tx, publication.ReconcileInput{
		ProjectID: projectID, ServingStateID: "generation_native", ActorID: principalID,
		Publications: map[string]publication.Definition{
			"website": {Name: "website", Dashboard: "dashboard:website", DefaultPage: "overview", ConfigurationDigest: "sha256:" + "a" + strings.Repeat("0", 63)},
		},
	}, func(context.Context, dashboardpublicationpostgres.Tx, projectgraph.ResourceID, string) error {
		return nil
	}); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	row, err := publications.Get(t.Context(), projectID, "website")
	if err != nil {
		t.Fatal(err)
	}
	if audit.count == 0 {
		t.Fatal("native publication reconciliation did not record an audit intent")
	}
	module, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{RequireNativePersistence: true, NativePersistence: bundle})
	if err != nil {
		t.Fatal(err)
	}
	requestID := uuid.MustParse("018f4f2e-0000-7000-8000-000000000731").String()
	_, err = module.MutatePublicationWithInvocation(t.Context(), projectID.String(), "website", principalID, publication.ActionSuspend, publication.CommandInvocation{
		Surface: "api", OperationID: "suspendDashboardPublication", RequestID: requestID, CorrelationID: requestID,
		IdempotencyKey: uuid.MustParse("018f4f2e-0000-7000-8000-000000000732").String(), ExpectedRevision: row.Revision,
	})
	if err != nil {
		if errors.Is(err, publication.ErrNotFound) {
			t.Fatalf("native mutation incorrectly returned ErrNotFound: %v", err)
		}
		t.Fatalf("native publication mutation failed (audit unavailable would indicate bad composition): %v", err)
	}
	if audit.count < 2 {
		t.Fatalf("native publication mutation recorded %d audit intents, want reconciliation plus mutation", audit.count)
	}
}

func TestBuildNativeDashboardRejectsLegacyOrForgedPersistence(t *testing.T) {
	bundle := validNativePersistence(t)
	cases := []dashboardmodule.Config{
		{RequireNativePersistence: true, NativePersistence: bundle, Database: &sql.DB{}},
		{RequireNativePersistence: true, NativePersistence: bundle, LegacySQLite: true},
		{RequireNativePersistence: true, NativePersistence: &dashboardmodule.NativePersistence{}},
		{RequireNativePersistence: true, NativePersistence: bundle, SessionStore: dashboardsession.NewMemoryStore()},
		{RequireNativePersistence: true, NativePersistence: bundle, AuditIntentRecorder: access.AuditIntentRecorderFunc(func(context.Context, transaction.Transaction, access.AuditIntent) error { return nil })},
	}
	for index, config := range cases {
		if _, err := dashboardmodule.Build(t.Context(), config); err == nil {
			t.Fatalf("forged native config %d was accepted", index)
		}
	}
}

func TestBuildRejectsUnmarkedDatabaseOutsideLegacyMode(t *testing.T) {
	if _, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{Database: &sql.DB{}}); err == nil {
		t.Fatal("dashboard build accepted a database without explicit LegacySQLite mode")
	}
}

func TestBuildRejectsMissingAuthoringOrPublicationWhenRequired(t *testing.T) {
	if _, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{RequireAuthoring: true}); err == nil {
		t.Fatal("build accepted missing authoring authority")
	}
	if _, err := dashboardmodule.Build(t.Context(), dashboardmodule.Config{RequirePublication: true}); err == nil {
		t.Fatal("build accepted unavailable native publication authority")
	}
}
