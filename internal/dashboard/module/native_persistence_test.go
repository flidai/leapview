package module_test

import (
	"context"
	"database/sql"
	"testing"

	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

func validNativePersistence(t *testing.T) *dashboardmodule.NativePersistence {
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
	bundle, err := dashboardmodule.NewNativePersistence(sessions, usage, appearance)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
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
}

func TestBuildNativeDashboardRejectsLegacyOrForgedPersistence(t *testing.T) {
	bundle := validNativePersistence(t)
	cases := []dashboardmodule.Config{
		{RequireNativePersistence: true, NativePersistence: bundle, Database: &sql.DB{}},
		{RequireNativePersistence: true, NativePersistence: bundle, LegacySQLite: true},
		{RequireNativePersistence: true, NativePersistence: &dashboardmodule.NativePersistence{}},
		{RequireNativePersistence: true, NativePersistence: bundle, SessionStore: dashboardsession.NewMemoryStore()},
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
