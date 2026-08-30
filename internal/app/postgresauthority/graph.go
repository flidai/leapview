package postgresauthority

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	adminproductpostgres "github.com/flidai/leapview/internal/admin/product/postgres"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	cachepostgres "github.com/flidai/leapview/internal/analytics/cache/postgres"
	connectionbindingpostgres "github.com/flidai/leapview/internal/analytics/connectionbinding/postgres"
	physicalpoolpostgres "github.com/flidai/leapview/internal/analytics/physicalpool/postgres"
	queryauditpostgres "github.com/flidai/leapview/internal/analytics/queryaudit/postgres"
	agentcomposition "github.com/flidai/leapview/internal/app/agentpostgres"
	"github.com/flidai/leapview/internal/app/connectionbindingaudit"
	"github.com/flidai/leapview/internal/app/dashboardappearanceaudit"
	"github.com/flidai/leapview/internal/app/dashboardappearanceevents"
	"github.com/flidai/leapview/internal/app/dashboardauthoringaudit"
	"github.com/flidai/leapview/internal/app/dashboardauthoringevents"
	"github.com/flidai/leapview/internal/app/dashboardgenerationfence"
	"github.com/flidai/leapview/internal/app/dashboardpublicationaudit"
	"github.com/flidai/leapview/internal/app/dashboardpublicationevents"
	deploymentcomposition "github.com/flidai/leapview/internal/app/deploymentpostgres"
	manageddataaudit "github.com/flidai/leapview/internal/app/manageddataaudit"
	manageddataworkflow "github.com/flidai/leapview/internal/app/manageddataworkflow"
	postgresmaintenance "github.com/flidai/leapview/internal/app/postgresmaintenance"
	"github.com/flidai/leapview/internal/app/productaudit"
	refreshcomposition "github.com/flidai/leapview/internal/app/refreshpostgres"
	"github.com/flidai/leapview/internal/app/releaseaudit"
	"github.com/flidai/leapview/internal/app/releasecatalog"
	"github.com/flidai/leapview/internal/app/releaseevents"
	"github.com/flidai/leapview/internal/app/releasejobs"
	dashboardappearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardauthoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	dashboardpublicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	dashboardsessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardusagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	"github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	lineagepostgres "github.com/flidai/leapview/internal/lineage/postgres"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	manageddatapostgres "github.com/flidai/leapview/internal/manageddata/postgres"
	platformbootstrappostgres "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	cursorsigningpostgres "github.com/flidai/leapview/internal/platform/http/cursorsigning/postgres"
	idempotencypostgres "github.com/flidai/leapview/internal/platform/http/idempotency/postgres"
	jobspostgres "github.com/flidai/leapview/internal/platform/jobs/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	releasepostgres "github.com/flidai/leapview/internal/release/postgres"
	servingstatepostgres "github.com/flidai/leapview/internal/servingstate/postgres"
)

// PostgresAuthorityGraph is the application-owned native control-plane graph.
// Each field is a capability authority over the already-retained PostgreSQL
// pool; no field opens a pool, migrates a schema, or owns lifecycle shutdown.
// The graph is intentionally separate from BuildProduction while the HTTP
// surface is being migrated, so construction and validation can be tested in
// isolation.
type PostgresAuthorityGraph struct {
	Bootstrap *platformbootstrappostgres.Repository
	// Settings is an explicit alias for the platform bootstrap/settings
	// authority. Keeping it named makes the settings dependency visible to
	// future composition without introducing a second repository or pool.
	Settings *platformbootstrappostgres.Repository

	Operation            *operationpostgres.Repository
	OperationMaintenance *operationpostgres.Maintenance
	Jobs                 *jobspostgres.Repository
	JobsMaintenance      *jobspostgres.Maintenance
	Events               *eventspostgres.Repository

	Project      *projectpostgres.Repository
	Access       *accesspostgres.Repository
	AccessAudit  *accesspostgres.AuditRepository
	Product      *adminproductpostgres.Repository
	ProductAudit *productaudit.Adapter

	Idempotency              *idempotencypostgres.Store
	CursorSigning            *cursorsigningpostgres.Repository
	CursorSigningMaintenance *cursorsigningpostgres.Maintenance

	ConnectionBinding      *connectionbindingpostgres.Repository
	ConnectionBindingAudit *connectionbindingaudit.Adapter
	QueryAudit             *queryauditpostgres.Repository
	QueryAuditMaintenance  *queryauditpostgres.Maintenance
	Cache                  *cachepostgres.Repository
	CacheMaintenance       *cachepostgres.Maintenance
	Lineage                *lineagepostgres.Repository

	// PhysicalPool is the control-database identity/admission authority for a
	// DuckLake physical namespace. DuckLake remains authoritative for table and
	// object membership; this repository stores only stable identity and
	// immutable conformance evidence.
	PhysicalPool *physicalpoolpostgres.Repository
	// ServingState stores immutable serving-generation evidence and reader
	// leases. Delivery remains the sole mutable activation authority.
	ServingState *servingstatepostgres.Repository
	// Refresh stores durable schedules, runs, attempts, publications and data
	// versions. Queue admission is delegated to the canonical Jobs authority.
	Refresh *refreshpostgres.Repository
	// RefreshJobs and RefreshCancelAudit are composition-owned bridges. They
	// retain the exact Refresh, Jobs and Access audit repository identities so
	// refresh transactions cannot silently split across sibling authorities.
	RefreshJobs        *refreshmodule.PostgresJobsAdapter
	RefreshCancelAudit *refreshcomposition.PostgresCancelAuditWriterAdapter

	Release        *releasepostgres.Repository
	ReleaseAudit   *releaseaudit.Adapter
	ReleaseEvents  *releaseevents.Adapter
	ReleaseCatalog *releasemodule.PostgresCatalog

	DeploymentRepository   *deploymentpostgres.Repository
	DeploymentPersistence  *module.Persistence
	AgentRepository        *agentpostgres.Repository
	AgentMaintenance       *agentpostgres.Maintenance
	AgentPersistence       *agentmodule.Persistence
	ManagedDataRepository  *manageddatapostgres.Repository
	ManagedDataMaintenance *manageddatapostgres.Maintenance
	ManagedDataAudit       *manageddataaudit.Adapter
	ManagedDataPersistence *manageddatamodule.Persistence

	// Dashboard authorities are all backed by the retained runtime pool. The
	// audit and event adapters below deliberately retain the graph's canonical
	// Access and platform-event repository identities, while authoring's fence
	// retains the graph's deployment repository and process-bound target.
	DashboardSession            *dashboardsessionpostgres.Store
	DashboardSessionMaintenance *dashboardsessionpostgres.Maintenance
	DashboardUsage              *dashboardusagepostgres.Repository
	DashboardUsageMaintenance   *dashboardusagepostgres.Maintenance
	DashboardAppearance         *dashboardappearancepostgres.Repository
	DashboardAppearanceAudit    *dashboardappearanceaudit.Adapter
	DashboardAppearanceEvents   *dashboardappearanceevents.Adapter
	DashboardAuthoring          *dashboardauthoringpostgres.Repository
	DashboardAuthoringAudit     *dashboardauthoringaudit.Adapter
	DashboardAuthoringEvents    *dashboardauthoringevents.Adapter
	DashboardGenerationFence    *dashboardgenerationfence.Fence
	DashboardTargetID           string
	DashboardPublication        *dashboardpublicationpostgres.Repository
	DashboardPublicationAudit   *dashboardpublicationaudit.Adapter
	DashboardPublicationEvents  *dashboardpublicationevents.Adapter
	DashboardStreams            *dashboardpublicationpostgres.StreamRegistry
	DashboardStreamsMaintenance *dashboardpublicationpostgres.Maintenance
	DashboardBroker             *dashboardpublicationpostgres.Broker
	DashboardPersistence        *dashboardmodule.NativePersistence

	AccessAuditMaintenance     *accesspostgres.Maintenance
	AccessAuthStateMaintenance *accesspostgres.Maintenance
	Retention                  *postgresmaintenance.Coordinator
}

// PostgresAuthorityGraphOptions supplies values that are not persisted in the
// pool lifecycle itself. FingerprintKey must be the explicit API-token HMAC
// key; TargetID is the process-bound delivery target used by the release
// catalog and is never read from a browser request.
type PostgresAuthorityGraphOptions struct {
	TargetID       string
	FingerprintKey []byte
}

// NewPostgresAuthorityGraph composes the native authorities over the pools
// retained by postgresControlPlaneLifecycle. It performs no network I/O and
// does not change lifecycle ownership. Callers should invoke Validate before
// exposing any handlers.
func NewPostgresAuthorityGraph(runtime, maintenance *platformpostgres.Pool, options PostgresAuthorityGraphOptions) (*PostgresAuthorityGraph, error) {
	if runtime == nil || maintenance == nil {
		return nil, errors.New("PostgreSQL authority graph requires initialized runtime and maintenance pools")
	}
	if strings.TrimSpace(options.TargetID) == "" {
		return nil, errors.New("PostgreSQL authority graph target id is required")
	}
	if len(options.FingerprintKey) < 32 {
		return nil, errors.New("PostgreSQL authority graph fingerprint key must be at least 32 bytes")
	}
	bootstrap := platformbootstrappostgres.New(runtime)
	operations := operationpostgres.New(runtime)
	operationMaintenance := operationpostgres.NewMaintenance(maintenance)
	cursorSigningMaintenance := cursorsigningpostgres.NewMaintenance(maintenance)
	jobs := jobspostgres.New(runtime)
	events := eventspostgres.New()
	project := projectpostgres.New(runtime)
	audit := accesspostgres.New()
	accessRepository, err := accesspostgres.NewAccess(runtime, accesspostgres.FingerprintConfig{Key: options.FingerprintKey})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL access authority: %w", err)
	}
	productAudit := productaudit.NewWithRepository(audit)
	product, err := adminproductpostgres.NewWithOptions(runtime, adminproductpostgres.Options{Audit: productAudit})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL product authority: %w", err)
	}
	connectionBindingAudit := connectionbindingaudit.NewWithRepository(audit)
	binding, err := connectionbindingpostgres.NewProduction(runtime, connectionBindingAudit)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL connection-binding authority: %w", err)
	}

	// These capability schemas are all part of leapview_control. In
	// particular, the DuckLake package's broader repository is intentionally
	// not constructed here: its attempt/retention/lease methods duplicate the
	// canonical delivery and serving-state authorities. A future migration-only
	// facade may be added once that package exposes a narrow port.
	physicalPool := physicalpoolpostgres.New(runtime)
	servingState := servingstatepostgres.New(runtime)
	refresh := refreshpostgres.New(runtime)
	refreshJobs := refreshmodule.NewPostgresJobsAdapter(jobs, refresh)
	refreshCancelAudit, err := refreshcomposition.NewPostgresCancelAuditWriterAdapter(audit)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL refresh cancellation audit authority: %w", err)
	}

	deploymentPersistence, err := deploymentcomposition.NewPersistence(runtime, deploymentcomposition.Authorities{
		Access: audit, Events: events, Jobs: jobs, Operations: operations,
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL deployment authority: %w", err)
	}
	// Reuse the exact repository allocated by the persistence helper. Keeping
	// one pointer for the module bundle and release catalog avoids split
	// identity even though both surfaces are otherwise capability-equivalent.
	deploymentRepository := deploymentPersistence.Repository

	dashboardSession, err := dashboardsessionpostgres.New(runtime)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard session authority: %w", err)
	}
	dashboardUsage, err := dashboardusagepostgres.New(runtime)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard usage authority: %w", err)
	}
	dashboardAppearanceAudit := dashboardappearanceaudit.NewWithRepository(audit)
	dashboardAppearanceEvents := dashboardappearanceevents.NewWithRepository(events)
	dashboardAppearance, err := dashboardappearancepostgres.New(runtime, dashboardappearancepostgres.Options{
		Audit: dashboardAppearanceAudit, Events: dashboardAppearanceEvents,
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard appearance authority: %w", err)
	}
	dashboardFence, err := dashboardgenerationfence.New(deploymentRepository, options.TargetID)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard generation fence: %w", err)
	}
	dashboardAuthoringAudit := dashboardauthoringaudit.NewWithRepository(audit)
	dashboardAuthoringEvents := dashboardauthoringevents.NewWithRepository(events)
	dashboardAuthoring, err := dashboardauthoringpostgres.New(
		runtime,
		dashboardAuthoringAudit,
		dashboardAuthoringEvents,
		dashboardFence,
	)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard authoring authority: %w", err)
	}
	dashboardPublicationAudit := dashboardpublicationaudit.NewWithRepository(audit)
	dashboardPublicationEvents := dashboardpublicationevents.NewWithRepository(events)
	dashboardPublication, err := dashboardpublicationpostgres.New(
		runtime,
		dashboardPublicationAudit,
		dashboardPublicationEvents,
	)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard publication authority: %w", err)
	}
	dashboardStreams := dashboardpublicationpostgres.NewStreamRegistry(runtime)
	dashboardBroker := dashboardpublicationpostgres.NewBroker(nil)
	dashboardPersistence, err := dashboardmodule.NewNativePersistence(dashboardmodule.NativePersistenceOptions{
		Session: dashboardSession, Usage: dashboardUsage, Appearance: dashboardAppearance,
		Authoring: dashboardAuthoring, Publication: dashboardPublication,
		Streams: dashboardStreams, Broker: dashboardBroker,
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL dashboard persistence: %w", err)
	}
	agentPersistence, err := agentcomposition.NewPersistence(runtime, agentcomposition.Authorities{
		Access: audit, Events: events, Jobs: jobs,
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL agent persistence: %w", err)
	}
	agentRepository, ok := agentPersistence.Repository.(*agentpostgres.Repository)
	if !ok || agentRepository == nil {
		return nil, errors.New("construct PostgreSQL agent persistence: native repository is unavailable")
	}
	managedDataAudit := manageddataaudit.NewWithRepository(audit)
	managedDataRepository := manageddatapostgres.NewWithOptions(runtime, manageddatapostgres.Options{
		Workflow: manageddataworkflow.New(jobs), Audit: managedDataAudit,
	})
	managedDataPersistence, err := manageddatamodule.NewPostgresPersistence(managedDataRepository)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL managed-data persistence: %w", err)
	}

	eventTransactions, err := postgresmaintenance.NewPgxEventTxRunner(maintenance.Begin)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL event maintenance transaction runner: %w", err)
	}
	jobsMaintenance := jobspostgres.NewMaintenance(maintenance)
	queryAuditMaintenance := queryauditpostgres.NewMaintenance(maintenance)
	cacheMaintenance := cachepostgres.NewMaintenance(maintenance)
	agentMaintenance := agentpostgres.NewMaintenance(maintenance)
	managedDataMaintenance := manageddatapostgres.NewMaintenance(maintenance)
	dashboardSessionMaintenance := dashboardsessionpostgres.NewMaintenance(maintenance)
	dashboardUsageMaintenance := dashboardusagepostgres.NewMaintenance(maintenance)
	dashboardStreamsMaintenance := dashboardpublicationpostgres.NewMaintenance(maintenance)
	accessAuditMaintenance := accesspostgres.NewMaintenance(maintenance)
	accessAuthStateMaintenance := accesspostgres.NewMaintenance(maintenance)
	retention, err := postgresmaintenance.New(postgresmaintenance.Options{
		Operations: operationMaintenance, CursorSigning: cursorSigningMaintenance,
		Jobs: jobsMaintenance, Events: events, EventTransactions: eventTransactions,
		Cache: cacheMaintenance, DashboardSession: dashboardSessionMaintenance,
		DashboardUsage: dashboardUsageMaintenance, DashboardStreams: dashboardStreamsMaintenance,
		ManagedData: managedDataMaintenance, AccessAudit: accessAuditMaintenance, AccessAuthState: accessAuthStateMaintenance,
		QueryAudit: queryAuditMaintenance, AgentHistory: agentMaintenance,
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL retention coordinator: %w", err)
	}

	releaseAudit := releaseaudit.NewWithRepository(audit)
	releaseEvents := releaseevents.NewWithRepository(events)
	releaseRepository := releasepostgres.NewWithOptions(runtime, releasepostgres.Options{
		Audit: releaseAudit, Events: releaseEvents, Workflow: releasejobs.New(jobs),
	})
	projectCatalog, err := releasecatalog.NewProjectAuthority(project)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL release project catalog: %w", err)
	}
	bindingCatalog, err := releasecatalog.NewConnectionBindingAuthority(binding)
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL release binding catalog: %w", err)
	}
	releaseCatalog, err := releasemodule.NewPostgresCatalog(releasemodule.PostgresCatalogConfig{
		Projects: projectCatalog, Bindings: bindingCatalog, TargetID: options.TargetID,
		LatestReleaseID: func(ctx context.Context, projectID string) (string, error) {
			return latestReleaseID(ctx, releaseRepository, projectID)
		},
		ActiveDeploymentID: func(ctx context.Context, _ string) (string, error) {
			return activeDeploymentID(ctx, deploymentRepository, options.TargetID)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("construct PostgreSQL release catalog: %w", err)
	}

	graph := &PostgresAuthorityGraph{
		Bootstrap: bootstrap, Settings: bootstrap,
		Operation: operations, OperationMaintenance: operationMaintenance, Jobs: jobs, JobsMaintenance: jobsMaintenance, Events: events,
		Project: project, Access: accessRepository, AccessAudit: audit, Product: product, ProductAudit: productAudit,
		Idempotency:   idempotencypostgres.NewStore(runtime),
		CursorSigning: cursorsigningpostgres.NewRepository(runtime), CursorSigningMaintenance: cursorSigningMaintenance,
		ConnectionBinding: binding, ConnectionBindingAudit: connectionBindingAudit, QueryAudit: queryauditpostgres.New(runtime), QueryAuditMaintenance: queryAuditMaintenance, Cache: cachepostgres.New(runtime), CacheMaintenance: cacheMaintenance, Lineage: lineagepostgres.New(runtime),
		PhysicalPool: physicalPool, ServingState: servingState, Refresh: refresh,
		RefreshJobs: refreshJobs, RefreshCancelAudit: refreshCancelAudit,
		Release: releaseRepository, ReleaseAudit: releaseAudit, ReleaseEvents: releaseEvents, ReleaseCatalog: releaseCatalog,
		DeploymentRepository: deploymentRepository, DeploymentPersistence: &deploymentPersistence,
		AgentRepository: agentRepository, AgentMaintenance: agentMaintenance, AgentPersistence: &agentPersistence,
		ManagedDataRepository: managedDataRepository, ManagedDataMaintenance: managedDataMaintenance, ManagedDataAudit: managedDataAudit, ManagedDataPersistence: &managedDataPersistence,
		DashboardSession: dashboardSession, DashboardSessionMaintenance: dashboardSessionMaintenance, DashboardUsage: dashboardUsage, DashboardUsageMaintenance: dashboardUsageMaintenance,
		DashboardAppearance: dashboardAppearance, DashboardAppearanceAudit: dashboardAppearanceAudit, DashboardAppearanceEvents: dashboardAppearanceEvents,
		DashboardAuthoring: dashboardAuthoring, DashboardAuthoringAudit: dashboardAuthoringAudit, DashboardAuthoringEvents: dashboardAuthoringEvents,
		DashboardGenerationFence: dashboardFence, DashboardTargetID: options.TargetID,
		DashboardPublication: dashboardPublication, DashboardPublicationAudit: dashboardPublicationAudit, DashboardPublicationEvents: dashboardPublicationEvents,
		DashboardStreams: dashboardStreams, DashboardStreamsMaintenance: dashboardStreamsMaintenance, DashboardBroker: dashboardBroker, DashboardPersistence: dashboardPersistence,
		AccessAuditMaintenance: accessAuditMaintenance, AccessAuthStateMaintenance: accessAuthStateMaintenance, Retention: retention,
	}
	if err := graph.Validate(); err != nil {
		return nil, err
	}
	return graph, nil
}

func projectIDValue(value string) (projectgraph.ResourceID, error) {
	id, err := projectgraph.NewResourceID(value)
	if err != nil {
		return "", fmt.Errorf("invalid project id %q: %w", value, err)
	}
	return id, nil
}

type releaseLister interface {
	List(context.Context, projectgraph.ResourceID) ([]release.Release, error)
}

type deploymentTargetReader interface {
	Target(context.Context, string) (deploymentpostgres.DeliveryTarget, error)
}

// latestReleaseID preserves the native release query's ordering contract:
// ListReleases orders by created_at DESC, release_id DESC, so the first row is
// the latest release. Keeping this in a small function makes the ordering
// invariant testable without a database.
func latestReleaseID(ctx context.Context, releases releaseLister, projectID string) (string, error) {
	if releases == nil {
		return "", errors.New("release authority is required")
	}
	id, err := projectIDValue(projectID)
	if err != nil {
		return "", err
	}
	rows, err := releases.List(ctx, id)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].ID, nil
}

// activeDeploymentID maps the native delivery pointer's publication identity
// to the release catalog's deployment identity. sealedcontrol.Coordinator
// deliberately sets DeploymentID from Publication.ID, so ActivePublicationID
// is the canonical value (not ActiveGenerationID).
func activeDeploymentID(ctx context.Context, targets deploymentTargetReader, targetID string) (string, error) {
	if targets == nil {
		return "", errors.New("deployment authority is required")
	}
	target, err := targets.Target(ctx, targetID)
	if err != nil {
		return "", err
	}
	return target.ActivePublicationID, nil
}

// Validate rejects a nil or partial graph. The checks are intentionally
// structural and side-effect free: pool readiness and schema revision remain
// owned by postgresControlPlaneLifecycle.Start.
func (g *PostgresAuthorityGraph) Validate() error {
	if g == nil {
		return errors.New("PostgreSQL authority graph is nil")
	}
	required := []struct {
		name  string
		value any
	}{
		{"platform bootstrap authority", g.Bootstrap}, {"platform settings authority", g.Settings},
		{"operation authority", g.Operation}, {"operation maintenance authority", g.OperationMaintenance},
		{"jobs authority", g.Jobs}, {"jobs maintenance authority", g.JobsMaintenance}, {"event authority", g.Events}, {"project authority", g.Project},
		{"access authority", g.Access}, {"access audit authority", g.AccessAudit}, {"product authority", g.Product}, {"product audit authority", g.ProductAudit},
		{"idempotency authority", g.Idempotency}, {"cursor-signing authority", g.CursorSigning}, {"cursor-signing maintenance authority", g.CursorSigningMaintenance},
		{"connection-binding authority", g.ConnectionBinding}, {"connection-binding audit authority", g.ConnectionBindingAudit}, {"query-audit authority", g.QueryAudit}, {"query-audit maintenance authority", g.QueryAuditMaintenance}, {"cache authority", g.Cache}, {"cache maintenance authority", g.CacheMaintenance}, {"lineage authority", g.Lineage},
		{"physical-pool authority", g.PhysicalPool}, {"serving-state authority", g.ServingState},
		{"refresh authority", g.Refresh}, {"refresh jobs authority", g.RefreshJobs}, {"refresh cancellation audit authority", g.RefreshCancelAudit},
		{"release authority", g.Release}, {"release audit authority", g.ReleaseAudit}, {"release event authority", g.ReleaseEvents}, {"release catalog authority", g.ReleaseCatalog},
		{"deployment repository", g.DeploymentRepository}, {"deployment persistence", g.DeploymentPersistence},
		{"agent repository", g.AgentRepository}, {"agent maintenance authority", g.AgentMaintenance}, {"agent persistence", g.AgentPersistence},
		{"managed-data repository", g.ManagedDataRepository}, {"managed-data maintenance authority", g.ManagedDataMaintenance}, {"managed-data audit authority", g.ManagedDataAudit}, {"managed-data persistence", g.ManagedDataPersistence},
		{"dashboard session authority", g.DashboardSession}, {"dashboard session maintenance authority", g.DashboardSessionMaintenance}, {"dashboard usage authority", g.DashboardUsage}, {"dashboard usage maintenance authority", g.DashboardUsageMaintenance},
		{"dashboard appearance authority", g.DashboardAppearance}, {"dashboard appearance audit authority", g.DashboardAppearanceAudit},
		{"dashboard appearance event authority", g.DashboardAppearanceEvents},
		{"dashboard authoring authority", g.DashboardAuthoring}, {"dashboard authoring audit authority", g.DashboardAuthoringAudit},
		{"dashboard authoring event authority", g.DashboardAuthoringEvents}, {"dashboard generation fence", g.DashboardGenerationFence},
		{"dashboard publication authority", g.DashboardPublication}, {"dashboard publication audit authority", g.DashboardPublicationAudit},
		{"dashboard publication event authority", g.DashboardPublicationEvents}, {"dashboard streams authority", g.DashboardStreams}, {"dashboard streams maintenance authority", g.DashboardStreamsMaintenance},
		{"dashboard broker authority", g.DashboardBroker}, {"dashboard persistence", g.DashboardPersistence},
		{"access-audit maintenance authority", g.AccessAuditMaintenance}, {"access-auth-state maintenance authority", g.AccessAuthStateMaintenance}, {"retention coordinator", g.Retention},
	}
	for _, item := range required {
		if isNilAuthority(item.value) {
			return fmt.Errorf("PostgreSQL authority graph missing %s", item.name)
		}
	}
	if strings.TrimSpace(g.DashboardTargetID) == "" || g.DashboardTargetID != strings.TrimSpace(g.DashboardTargetID) {
		return errors.New("PostgreSQL authority graph dashboard target id is not configured")
	}
	if g.Bootstrap != g.Settings {
		return errors.New("PostgreSQL authority graph platform bootstrap and settings authorities must share identity")
	}
	if g.Access.DB() == nil {
		return errors.New("PostgreSQL authority graph access authority is not configured")
	}
	if !g.ProductAudit.Matches(g.AccessAudit) {
		return errors.New("PostgreSQL authority graph product audit adapter does not preserve access audit identity")
	}
	if !g.Project.Configured() {
		return errors.New("PostgreSQL authority graph project authority is not configured")
	}
	if !g.ConnectionBinding.Configured() || !g.ConnectionBinding.AuditCapable() {
		return errors.New("PostgreSQL authority graph connection-binding authority is not audit-capable")
	}
	if !g.ConnectionBindingAudit.Matches(g.AccessAudit) {
		return errors.New("PostgreSQL authority graph connection-binding audit adapter does not preserve access audit identity")
	}
	if !g.ServingState.Configured() {
		return errors.New("PostgreSQL authority graph serving-state authority is not configured")
	}
	if !refreshJobsMatches(g.Jobs, g.Refresh, g.RefreshJobs) {
		return errors.New("PostgreSQL authority graph refresh jobs adapter does not preserve sibling repository identity")
	}
	if !refreshCancelAuditMatches(g.AccessAudit, g.RefreshCancelAudit) {
		return errors.New("PostgreSQL authority graph refresh cancellation audit adapter does not preserve access audit identity")
	}
	if !g.Release.Configured() || !g.Release.AuditCapable() || !g.Release.EventCapable() || !g.Release.WorkflowCapable() {
		return errors.New("PostgreSQL authority graph release authority is not fully configured")
	}
	if !g.ReleaseAudit.Matches(g.AccessAudit) || !g.ReleaseEvents.Matches(g.Events) {
		return errors.New("PostgreSQL authority graph release adapters do not preserve sibling repository identity")
	}
	if !g.ReleaseCatalog.Configured() {
		return errors.New("PostgreSQL authority graph release catalog is not configured")
	}
	if !g.DeploymentRepository.Configured() || !g.DeploymentRepository.TransactionCapable() || !g.DeploymentRepository.AuditCapable() {
		return errors.New("PostgreSQL authority graph deployment authority is not fully configured")
	}
	if !g.AgentRepository.Configured() || !g.AgentRepository.TransactionCapable() || !g.AgentRepository.WorkflowCapable() || !g.AgentRepository.JobsCapable() || !g.AgentRepository.AuditCapable() || !g.AgentRepository.DomainEventCapable() {
		return errors.New("PostgreSQL authority graph agent authority is not fully configured")
	}
	if !g.ManagedDataRepository.TransitionCapabilitiesConfigured() {
		return errors.New("PostgreSQL authority graph managed-data authority is not fully configured")
	}
	if !g.ManagedDataAudit.Matches(g.AccessAudit) {
		return errors.New("PostgreSQL authority graph managed-data audit adapter does not preserve access audit identity")
	}
	if !deploymentPersistenceMatches(g.DeploymentRepository, g.DeploymentPersistence) || !agentPersistenceMatches(g.AgentRepository, g.AgentPersistence) {
		return errors.New("PostgreSQL authority graph persistence identity mismatch")
	}
	if !g.DashboardSession.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard session authority is not configured")
	}
	if !g.DashboardUsage.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard usage authority is not configured")
	}
	if !g.DashboardAppearance.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard appearance authority is not configured")
	}
	if !g.DashboardAuthoring.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard authoring authority is not configured")
	}
	if !g.DashboardPublication.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard publication authority is not configured")
	}
	if !g.DashboardStreams.IsNative() {
		return errors.New("PostgreSQL authority graph dashboard streams authority is not configured")
	}
	if !g.DashboardBroker.IsNative() || !g.DashboardBroker.Configured() {
		return errors.New("PostgreSQL authority graph dashboard broker authority is not configured")
	}
	if !g.DashboardAppearanceAudit.Matches(g.AccessAudit) || !g.DashboardAppearanceEvents.Matches(g.Events) {
		return errors.New("PostgreSQL authority graph dashboard appearance adapters do not preserve sibling repository identity")
	}
	if !g.DashboardAuthoringAudit.Matches(g.AccessAudit) || !g.DashboardAuthoringEvents.Matches(g.Events) {
		return errors.New("PostgreSQL authority graph dashboard authoring adapters do not preserve sibling repository identity")
	}
	if !g.DashboardGenerationFence.Matches(g.DeploymentRepository, g.DashboardTargetID) {
		return errors.New("PostgreSQL authority graph dashboard generation fence does not preserve deployment identity")
	}
	if !g.DashboardPublicationAudit.Matches(g.AccessAudit) || !g.DashboardPublicationEvents.Matches(g.Events) {
		return errors.New("PostgreSQL authority graph dashboard publication adapters do not preserve sibling repository identity")
	}
	if !g.DashboardPersistence.Matches(dashboardmodule.NativePersistenceOptions{
		Session: g.DashboardSession, Usage: g.DashboardUsage, Appearance: g.DashboardAppearance,
		Authoring: g.DashboardAuthoring, Publication: g.DashboardPublication,
		Streams: g.DashboardStreams, Broker: g.DashboardBroker,
	}) {
		return errors.New("PostgreSQL authority graph dashboard persistence identity mismatch")
	}
	return nil
}

func deploymentPersistenceMatches(repository *deploymentpostgres.Repository, persistence *module.Persistence) bool {
	return repository != nil && persistence != nil && persistence.Repository == repository
}

func agentPersistenceMatches(repository *agentpostgres.Repository, persistence *agentmodule.Persistence) bool {
	return repository != nil && persistence != nil && persistence.Repository == repository
}

func refreshJobsMatches(jobs *jobspostgres.Repository, refresh *refreshpostgres.Repository, adapter *refreshmodule.PostgresJobsAdapter) bool {
	return jobs != nil && refresh != nil && adapter != nil && adapter.Jobs == jobs && adapter.Refresh == refresh
}

func refreshCancelAuditMatches(audit *accesspostgres.AuditRepository, adapter *refreshcomposition.PostgresCancelAuditWriterAdapter) bool {
	return audit != nil && adapter != nil && adapter.Audit == audit
}

func isNilAuthority(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
