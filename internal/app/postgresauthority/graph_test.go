package postgresauthority

import (
	"context"
	"errors"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	productpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	"github.com/flidai/leapview/internal/app/connectionbindingaudit"
	"github.com/flidai/leapview/internal/app/dashboardappearanceaudit"
	"github.com/flidai/leapview/internal/app/dashboardappearanceevents"
	"github.com/flidai/leapview/internal/app/dashboardauthoringaudit"
	"github.com/flidai/leapview/internal/app/dashboardauthoringevents"
	"github.com/flidai/leapview/internal/app/dashboardgenerationfence"
	"github.com/flidai/leapview/internal/app/dashboardpublicationaudit"
	"github.com/flidai/leapview/internal/app/dashboardpublicationevents"
	manageddataaudit "github.com/flidai/leapview/internal/app/manageddataaudit"
	"github.com/flidai/leapview/internal/app/productaudit"
	refreshcomposition "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/app/releaseaudit"
	"github.com/flidai/leapview/internal/app/releaseevents"
	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardauthoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/release"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type graphDBStub struct{}

func (graphDBStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (graphDBStub) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (graphDBStub) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (graphDBStub) Begin(context.Context) (pgx.Tx, error)                   { return nil, nil }

type graphDBNoBeginStub struct{}

func (graphDBNoBeginStub) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (graphDBNoBeginStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}
func (graphDBNoBeginStub) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

type releaseListFake struct {
	rows []release.Release
	err  error
}

func (f releaseListFake) List(context.Context, projectgraph.ResourceID) ([]release.Release, error) {
	return f.rows, f.err
}

type deploymentTargetFake struct {
	target deploymentpostgres.DeliveryTarget
	err    error
}

func (f deploymentTargetFake) Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error) {
	return f.target, f.err
}

func TestPostgresAuthorityGraphValidateRejectsNilAndPartialGraphs(t *testing.T) {
	var nilGraph *PostgresAuthorityGraph
	if err := nilGraph.Validate(); err == nil || !strings.Contains(err.Error(), "graph is nil") {
		t.Fatalf("nil graph error = %v, want nil-graph rejection", err)
	}

	partial := &PostgresAuthorityGraph{}
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "platform bootstrap authority") {
		t.Fatalf("empty graph error = %v, want bootstrap rejection", err)
	}

	partial.Bootstrap = &platformbootstrappostgres.Repository{}
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "platform settings authority") {
		t.Fatalf("bootstrap-only graph error = %v, want settings rejection", err)
	}
	partial.Settings = partial.Bootstrap
	if err := partial.Validate(); err == nil || !strings.Contains(err.Error(), "operation authority") {
		t.Fatalf("bootstrap/settings graph error = %v, want operation rejection", err)
	}
}

func TestValidateConfiguredAuthorityRequiresNativeMarkerAndConfiguration(t *testing.T) {
	if err := validateConfiguredAuthority("query-audit authority", graphUnmarkedAuthority{}); err == nil || !strings.Contains(err.Error(), "not marked as native PostgreSQL") {
		t.Fatalf("unmarked authority error = %v, want marker rejection", err)
	}
	if err := validateConfiguredAuthority("query-audit authority", graphMarkedAuthority{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured authority error = %v, want configured rejection", err)
	}
	if err := validateConfiguredAuthority("query-audit authority", queryauditpostgres.New(graphDBStub{})); err != nil {
		t.Fatalf("configured query-audit repository rejected: %v", err)
	}
	product, err := productpostgres.NewWithOptions(graphDBStub{}, productpostgres.Options{Audit: graphProductAuditStub{}})
	if err != nil {
		t.Fatalf("construct product repository: %v", err)
	}
	if err := validateConfiguredAuthority("product authority", product); err != nil {
		t.Fatalf("configured product repository rejected: %v", err)
	}
}

type graphUnmarkedAuthority struct{}

type graphMarkedAuthority struct{}

func (graphMarkedAuthority) PostgreSQLAuthority() {}
func (graphMarkedAuthority) Configured() bool     { return false }

type graphProductAuditStub struct{}

func (graphProductAuditStub) RecordAuditEvent(context.Context, pgx.Tx, productpostgres.AuditInput) error {
	return nil
}

func TestNewPostgresAuthorityGraphRejectsMissingPoolsAndOptions(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	if _, err := NewPostgresAuthorityGraph(nil, nil, PostgresAuthorityGraphOptions{TargetID: "target", FingerprintKey: key}); err == nil || !strings.Contains(err.Error(), "initialized runtime and maintenance pools") {
		t.Fatalf("nil pools error = %v, want pool rejection", err)
	}
	if _, err := NewPostgresAuthorityGraph(&platformpostgres.Pool{}, nil, PostgresAuthorityGraphOptions{TargetID: "target", FingerprintKey: key}); err == nil || !strings.Contains(err.Error(), "initialized runtime and maintenance pools") {
		t.Fatalf("partial lifecycle error = %v, want pool rejection", err)
	}
}

func TestPostgresAuthorityGraphReadersPreserveNativeIdentity(t *testing.T) {
	latest, err := latestReleaseID(t.Context(), releaseListFake{rows: []release.Release{{ID: "release-new"}, {ID: "release-old"}}}, "project:sales")
	if err != nil || latest != "release-new" {
		t.Fatalf("latest release = %q, error = %v; want first (DESC) row", latest, err)
	}
	if _, err := latestReleaseID(t.Context(), releaseListFake{err: errors.New("list failed")}, "project:sales"); err == nil {
		t.Fatal("latest release reader swallowed repository error")
	}

	active, err := activeDeploymentID(t.Context(), deploymentTargetFake{target: deploymentpostgres.DeliveryTarget{ActiveGenerationID: "generation-1", ActivePublicationID: "publication-1"}}, "target-prod")
	if err != nil || active != "publication-1" {
		t.Fatalf("active deployment = %q, error = %v; want publication identity", active, err)
	}
	if _, err := activeDeploymentID(t.Context(), deploymentTargetFake{err: errors.New("target failed")}, "target-prod"); err == nil {
		t.Fatal("active deployment reader swallowed repository error")
	}
}

func TestPostgresAuthorityGraphDeploymentPersistenceUsesSameRepository(t *testing.T) {
	repository := deploymentpostgres.New(nil)
	persistence := &deploymentmodule.Persistence{Repository: repository}
	if !deploymentPersistenceMatches(repository, persistence) {
		t.Fatal("matching deployment repository/persistence pair was rejected")
	}
	other := deploymentpostgres.New(nil)
	if deploymentPersistenceMatches(other, persistence) {
		t.Fatal("different deployment repository identity was accepted")
	}
}

func TestPostgresAuthorityGraphRefreshAdaptersPreserveRepositoryIdentity(t *testing.T) {
	jobs := jobspostgres.New(nil)
	refresh := refreshpostgres.New(nil)
	audit := accesspostgres.New()
	jobsAdapter := refreshmodule.NewPostgresJobsAdapter(jobs, refresh)
	auditAdapter, err := refreshcomposition.NewPostgresCancelAuditWriterAdapter(audit)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshJobsMatches(jobs, refresh, jobsAdapter) {
		t.Fatal("refresh jobs adapter did not retain exact Jobs and Refresh repository identities")
	}
	if !refreshCancelAuditMatches(audit, auditAdapter) {
		t.Fatal("refresh cancellation audit adapter did not retain exact Access audit identity")
	}
	if _, err := refreshcomposition.NewPostgresCancelAuditWriterAdapter(nil); err == nil {
		t.Fatal("refresh cancellation audit adapter accepted a nil Access audit authority")
	}
	if refreshJobsMatches(jobspostgres.New(nil), refresh, jobsAdapter) {
		t.Fatal("refresh jobs adapter accepted a different Jobs repository")
	}
	if refreshJobsMatches(jobs, refreshpostgres.New(nil), jobsAdapter) {
		t.Fatal("refresh jobs adapter accepted a different Refresh repository")
	}
	if refreshJobsMatches(jobs, refresh, nil) || refreshCancelAuditMatches(audit, nil) {
		t.Fatal("nil refresh adapter was accepted")
	}
}

func TestPostgresAuthorityGraphDashboardPersistencePreservesSiblingIdentity(t *testing.T) {
	db := graphDBStub{}
	audit := accesspostgres.New()
	events := eventspostgres.New()

	appearanceAudit := dashboardappearanceaudit.NewWithRepository(audit)
	appearanceEvents := dashboardappearanceevents.NewWithRepository(events)
	appearance, err := dashboardappearancepostgres.New(db, dashboardappearancepostgres.Options{Audit: appearanceAudit, Events: appearanceEvents})
	if err != nil {
		t.Fatal(err)
	}
	deployment := deploymentpostgres.New(db)
	fence, err := dashboardgenerationfence.New(deployment, "target-prod")
	if err != nil {
		t.Fatal(err)
	}
	authoringAudit := dashboardauthoringaudit.NewWithRepository(audit)
	authoringEvents := dashboardauthoringevents.NewWithRepository(events)
	authoring, err := dashboardauthoringpostgres.New(db, authoringAudit, authoringEvents, fence)
	if err != nil {
		t.Fatal(err)
	}
	publicationAudit := dashboardpublicationaudit.NewWithRepository(audit)
	publicationEvents := dashboardpublicationevents.NewWithRepository(events)
	publication, err := dashboardpublicationpostgres.New(db, publicationAudit, publicationEvents)
	if err != nil {
		t.Fatal(err)
	}
	session, err := dashboardsessionpostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := dashboardusagepostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	streams := dashboardpublicationpostgres.NewStreamRegistry(db)
	broker := dashboardpublicationpostgres.NewBroker(nil)
	options := dashboardmodule.NativePersistenceOptions{
		Session: session, Usage: usage, Appearance: appearance, Authoring: authoring,
		Publication: publication, Streams: streams, Broker: broker,
	}
	bundle, err := dashboardmodule.NewNativePersistence(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Matches(options) {
		t.Fatal("dashboard native persistence bundle rejected its exact authorities")
	}
	if !appearanceAudit.Matches(audit) || !appearanceEvents.Matches(events) ||
		!authoringAudit.Matches(audit) || !authoringEvents.Matches(events) ||
		!publicationAudit.Matches(audit) || !publicationEvents.Matches(events) ||
		!fence.Matches(deployment, "target-prod") {
		t.Fatal("dashboard adapters did not preserve exact sibling authority identities")
	}
	if appearanceAudit.Matches(accesspostgres.New()) || authoringEvents.Matches(eventspostgres.New()) {
		t.Fatal("dashboard adapters accepted distinct stateless authority allocations")
	}
	if bundle.Matches(dashboardmodule.NativePersistenceOptions{
		Session: session, Usage: usage, Appearance: appearance, Authoring: authoring,
		Publication: publication, Streams: dashboardpublicationpostgres.NewStreamRegistry(db), Broker: broker,
	}) {
		t.Fatal("dashboard native persistence accepted a different stream authority")
	}
}

func TestNewPostgresAuthorityGraphConstructsAndValidatesDashboardAuthorities(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "postgres_authority_graph")
	config := platformpostgres.Config{
		URL: database.AdminURL(), ExpectedMajor: platformpostgres.DefaultExpectedMajor,
		RuntimeRole: "postgres", Intent: platformpostgres.IntentReadWrite,
		MinConns: 0, MaxConns: 4,
	}
	runtime, err := platformpostgres.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	config.MaxConns = 1
	maintenance, err := platformpostgres.Open(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(maintenance.Close)

	graph, err := NewPostgresAuthorityGraph(
		runtime, maintenance,
		PostgresAuthorityGraphOptions{TargetID: "target-prod", FingerprintKey: []byte(strings.Repeat("k", 32))},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatalf("validate constructed graph: %v", err)
	}
	if graph.DashboardTargetID != "target-prod" || !graph.DashboardPersistence.Matches(dashboardmodule.NativePersistenceOptions{
		Session: graph.DashboardSession, Usage: graph.DashboardUsage, Appearance: graph.DashboardAppearance,
		Authoring: graph.DashboardAuthoring, Publication: graph.DashboardPublication,
		Streams: graph.DashboardStreams, Broker: graph.DashboardBroker,
	}) {
		t.Fatal("constructed graph did not preserve dashboard target and persistence identities")
	}
	if graph.DuckLakeControlLedger == nil || !graph.DuckLakeControlLedger.Configured() || !graph.DuckLakeControlLedger.TransactionCapable() {
		t.Fatal("constructed graph did not expose a transaction-capable DuckLake control ledger")
	}
	if graph.DuckLakeControlLedger.DB() != runtime {
		t.Fatal("DuckLake control ledger is not configured from the control runtime pool")
	}
	if graph.DuckLakeControlLedger.DB() == maintenance {
		t.Fatal("DuckLake control ledger is incorrectly configured from the maintenance pool")
	}
	if graph.DuckLakeControlLedger.DB() != graph.DeploymentRepository.DB() {
		t.Fatal("DuckLake control ledger is not transaction-compatible with the delivery control pool")
	}
	if !graph.ProductAudit.Matches(graph.AccessAudit) ||
		!graph.ConnectionBindingAudit.Matches(graph.AccessAudit) ||
		!graph.ManagedDataAudit.Matches(graph.AccessAudit) ||
		!graph.ReleaseAudit.Matches(graph.AccessAudit) ||
		!graph.ReleaseEvents.Matches(graph.Events) {
		t.Fatal("constructed graph did not preserve product, connection-binding, managed-data, or release sibling identities")
	}

	originalEvents := graph.DashboardPublicationEvents
	graph.DashboardPublicationEvents = dashboardpublicationevents.NewWithRepository(eventspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "publication adapters") {
		t.Fatalf("mismatched publication event authority error = %v", err)
	}
	graph.DashboardPublicationEvents = originalEvents
	originalProductAudit := graph.ProductAudit
	graph.ProductAudit = productaudit.NewWithRepository(accesspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "product audit adapter") {
		t.Fatalf("mismatched product audit authority error = %v", err)
	}
	graph.ProductAudit = originalProductAudit
	originalConnectionBindingAudit := graph.ConnectionBindingAudit
	graph.ConnectionBindingAudit = connectionbindingaudit.NewWithRepository(accesspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "connection-binding audit adapter") {
		t.Fatalf("mismatched connection-binding audit authority error = %v", err)
	}
	graph.ConnectionBindingAudit = originalConnectionBindingAudit
	originalManagedDataAudit := graph.ManagedDataAudit
	graph.ManagedDataAudit = manageddataaudit.NewWithRepository(accesspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "managed-data audit adapter") {
		t.Fatalf("mismatched managed-data audit authority error = %v", err)
	}
	graph.ManagedDataAudit = originalManagedDataAudit
	originalReleaseAudit, originalReleaseEvents := graph.ReleaseAudit, graph.ReleaseEvents
	graph.ReleaseAudit = releaseaudit.NewWithRepository(accesspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "release adapters") {
		t.Fatalf("mismatched release audit authority error = %v", err)
	}
	graph.ReleaseAudit, graph.ReleaseEvents = originalReleaseAudit, originalReleaseEvents
	graph.ReleaseEvents = releaseevents.NewWithRepository(eventspostgres.New())
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "release adapters") {
		t.Fatalf("mismatched release event authority error = %v", err)
	}
	graph.ReleaseEvents = originalReleaseEvents
	graph.DashboardTargetID = "target-other"
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "generation fence") {
		t.Fatalf("mismatched dashboard target error = %v", err)
	}
	graph.DashboardTargetID = "target-prod"
	originalLedger := graph.DuckLakeControlLedger
	graph.DuckLakeControlLedger = ducklakepostgres.New(maintenance)
	if err := graph.Validate(); err == nil || !strings.Contains(err.Error(), "control runtime pool") {
		t.Fatalf("DuckLake ledger pool mismatch error = %v", err)
	}
	if ducklakepostgres.New(graphDBNoBeginStub{}).TransactionCapable() {
		t.Fatal("DuckLake ledger reported transaction capability for a DBTX without Begin")
	}
	graph.DuckLakeControlLedger = originalLedger
}
