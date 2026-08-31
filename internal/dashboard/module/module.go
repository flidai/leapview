package module

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/dashboard/api"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
	dashboardauthoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	dashboardauthoringpostgres "github.com/flidai/leapview/internal/dashboard/authoring/postgres"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationpostgres "github.com/flidai/leapview/internal/dashboard/publication/postgres"
	publicationsqlite "github.com/flidai/leapview/internal/dashboard/publication/sqlite"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	sessionpostgres "github.com/flidai/leapview/internal/dashboard/session/postgres"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	dashboardui "github.com/flidai/leapview/internal/dashboard/ui"
	dashboardsignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/dashboard/usage"
	usagepostgres "github.com/flidai/leapview/internal/dashboard/usage/postgres"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/pagestream"
)

type Module struct {
	handler                    dashboardhttp.Handler
	authoring                  *dashboardauthoringapplication.Application
	semantic                   semanticapi.Handler
	snapshot                   func(context.Context) (string, error)
	publications               PublicationRepository
	publicationService         *publication.Service
	publicURL                  string
	currentActor               func(*http.Request) string
	publicationAuditConfigured bool
	streams                    publication.StreamRegistry
	publicBroker               dashboardhttp.SignalBroker
	publicTelemetry            PublicTelemetry
	dashboardTelemetry         DashboardTelemetry
	logger                     *slog.Logger
	runtimeMetrics             queryruntime.Metrics
	coordinators               *dashboardstream.Registry
	usageReader                usage.Reader
	appearanceStore            dashboardappearance.Store
	usageNow                   func() time.Time
	lifecycleMu                sync.Mutex
	lifecycleCancel            context.CancelFunc
	lifecycleWG                sync.WaitGroup
}

type Config struct {
	Database *sql.DB
	// NativePersistence is the only accepted source of dashboard persistence
	// when RequireNativePersistence is enabled. Its constructor checks that all
	// authorities are the concrete PostgreSQL implementations, so callers cannot
	// accidentally label a memory or SQLite store as production native state.
	NativePersistence *NativePersistence
	// SessionStore, UsageRecorder/UsageReader, and AppearanceStore are the
	// product-owned persistence seams. Native production composition supplies
	// PostgreSQL implementations. Sessions use the concurrency-safe in-process
	// MemoryStore when no native store is supplied; appearance and publication
	// retain explicit SQLite fallbacks for legacy tests.
	SessionStore             dashboardsession.Store
	AppearanceStore          dashboardappearance.Store
	LegacySQLite             bool
	RequireNativePersistence bool
	RequireAuthoring         bool
	RequirePublication       bool
	// Authoring is supplied by production composition. It remains optional at
	// this module boundary so focused dashboard-module tests can exercise the
	// read/render surface without constructing runtime-backed authoring ports.
	Authoring       *dashboardauthoringapplication.Application
	HTTP            HTTPConfig
	Semantic        SemanticConfig
	ServingSnapshot func(context.Context) (string, error)
	PublicTelemetry PublicTelemetry
	Logger          *slog.Logger
	PublicURL       string
	CurrentActor    func(*http.Request) string
	// AuditIntentRecorder is the narrow Access-owned port used by publication
	// SQLite transactions. It must be supplied whenever Database is configured.
	AuditIntentRecorder access.AuditIntentRecorder
	UsageRecorder       usage.Recorder
	UsageReader         usage.Reader
	UsageNow            func() time.Time
	RuntimeMetrics      queryruntime.Metrics
}

// NativePersistence is an opaque, validated bundle of dashboard PostgreSQL
// authorities. Fields remain private so a partial or forged bundle cannot be
// supplied to production module composition.
type NativePersistence struct {
	session     *sessionpostgres.Store
	usage       *usagepostgres.Repository
	appearance  *appearancepostgres.Repository
	authoring   *dashboardauthoringpostgres.Repository
	publication *publicationpostgres.Repository
	streams     publication.StreamRegistry
	broker      SignalBroker
}

// NativePersistenceOptions is the typed constructor input for the complete
// dashboard PostgreSQL authority bundle. Keeping every authority as a named
// field prevents order-dependent variadic wiring and rejects partial bundles.
type NativePersistenceOptions struct {
	Session     *sessionpostgres.Store
	Usage       *usagepostgres.Repository
	Appearance  *appearancepostgres.Repository
	Authoring   *dashboardauthoringpostgres.Repository
	Publication *publicationpostgres.Repository
	Streams     *publicationpostgres.StreamRegistry
	Broker      *publicationpostgres.Broker
}

func (p *NativePersistence) valid() bool {
	if p == nil {
		return false
	}
	nativeStreams, streamsOK := p.streams.(*publicationpostgres.StreamRegistry)
	registryOK := streamsOK && nativeStreams != nil && nativeStreams.IsNative()
	nativeBroker, brokerOK := p.broker.(*publicationpostgres.Broker)
	return p.session != nil && p.session.IsNative() && p.usage != nil && p.usage.IsNative() && p.appearance != nil && p.appearance.IsNative() && p.authoring != nil && p.authoring.IsNative() && p.publication != nil && p.publication.IsNative() && registryOK && brokerOK && nativeBroker != nil && nativeBroker.IsNative() && nativeBroker.Configured()
}

// Matches reports whether options contains the exact authorities owned by p.
// The identity check lets application composition verify that a bundle was
// built from the same stores it intends to install without exposing any of
// the bundle's opaque fields.
func (p *NativePersistence) Matches(options NativePersistenceOptions) bool {
	if !p.valid() {
		return false
	}
	nativeStreams, streamsOK := p.streams.(*publicationpostgres.StreamRegistry)
	nativeBroker, brokerOK := p.broker.(*publicationpostgres.Broker)
	return streamsOK && nativeStreams != nil && nativeBroker != nil && brokerOK &&
		p.session == options.Session &&
		p.usage == options.Usage &&
		p.appearance == options.Appearance &&
		p.authoring == options.Authoring &&
		p.publication == options.Publication &&
		nativeStreams == options.Streams &&
		nativeBroker == options.Broker
}

// MatchesAuthoringRepository reports whether p owns the exact native
// authoring repository supplied by application composition. The repository
// remains opaque; callers can validate identity without reading p's private
// authority fields.
func (p *NativePersistence) MatchesAuthoringRepository(repository *dashboardauthoringpostgres.Repository) bool {
	return p.valid() && repository != nil && p.authoring == repository
}

// MatchesAuthoringApplication reports whether the supplied transport-facing
// authoring application was composed with p's exact native repository.
func (p *NativePersistence) MatchesAuthoringApplication(application *AuthoringApplication) bool {
	return p.valid() && application != nil && application.MatchesRepository(p.authoring)
}

// NewNativePersistence validates the complete dashboard persistence bundle.
// Production composition should construct this only from native PostgreSQL
// repositories; legacy SQLite and memory stores are intentionally rejected.
func NewNativePersistence(options NativePersistenceOptions) (*NativePersistence, error) {
	if options.Session == nil || !options.Session.IsNative() {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL session store")
	}
	if options.Usage == nil || !options.Usage.IsNative() {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL usage repository")
	}
	if options.Appearance == nil || !options.Appearance.IsNative() {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL appearance repository")
	}
	if options.Authoring == nil || !options.Authoring.IsNative() {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL authoring repository")
	}
	if options.Publication == nil || !options.Publication.IsNative() {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL publication repository")
	}
	if options.Streams == nil || options.Streams.IsNative() == false {
		return nil, fmt.Errorf("dashboard native persistence requires a constructed PostgreSQL stream registry")
	}
	if options.Broker == nil || !options.Broker.IsNative() || !options.Broker.Configured() {
		return nil, fmt.Errorf("dashboard native persistence requires a configured scoped PostgreSQL publication broker")
	}
	bundle := &NativePersistence{session: options.Session, usage: options.Usage, appearance: options.Appearance, authoring: options.Authoring, publication: options.Publication, streams: options.Streams, broker: options.Broker}
	return bundle, nil
}

type HTTPConfig struct {
	Metrics                    queryruntime.Metrics
	ProjectID                  projectgraph.ResourceID
	ResolveProjectID           func(context.Context) (projectgraph.ResourceID, error)
	ResolveDashboardAppearance func(context.Context, projectgraph.ResourceID, projectgraph.ResourceID) (dashboardappearance.Value, error)
	Admission                  workload.Admitter
	Broker                     SignalBroker
	Logger                     *slog.Logger
	Telemetry                  DashboardTelemetry
	CurrentPrincipalID         func(*http.Request) string
	CurrentUsagePrincipal      func(*http.Request) (string, bool)
	AuthorizeListResource      func(context.Context, string, access.ResourceRef, access.Capability) (bool, error)
	CSRFToken                  func(*http.Request) string
	Layout                     func(*http.Request) webpage.Provider
	Environment                func(*http.Request) string
	DataRefreshedAt            func(context.Context, string, string, string) string
	QueryFreshness             func(context.Context, string, string, string) (api.QueryFreshness, bool)
	AgentBootstrap             func(*http.Request, string) dashboardui.AgentBootstrap
	AgentCommands              dashboardui.AgentCommandBindings
	Presentation               dashboardui.Presentation
	Assets                     staticasset.Resolver
}

type SemanticConfig struct {
	Metrics               queryruntime.Metrics
	ResolveProjectID      func(context.Context) (projectgraph.ResourceID, error)
	CurrentPrincipalID    func(*http.Request) string
	AuthorizeListResource func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error)
	QueryFreshness        func(context.Context, string, string, string) (api.QueryFreshness, bool)
}

type SignalBroker interface {
	Subscribe(string) (<-chan pagestream.SignalPatch, func())
	PublishEnvelope(string, dashboardstream.Envelope)
}

type DeliveryBroker = dashboardstream.DeliveryBroker

// PublicationRepository is the capability-neutral dashboard publication
// authority. SQLite and native PostgreSQL adapters both satisfy this port;
// module code never depends on a concrete storage package.
type PublicationRepository interface {
	publication.ServiceRepository
	Get(context.Context, projectgraph.ResourceID, string) (publication.Publication, error)
	GetByPublicID(context.Context, string) (publication.Publication, error)
	List(context.Context, projectgraph.ResourceID) ([]publication.Publication, error)
	ListAll(context.Context) ([]publication.Publication, error)
	ListEvents(context.Context, string) ([]publication.Event, error)
}

var _ PublicationRepository = (*publicationpostgres.Repository)(nil)

func newSQLitePublicationRepository(db *sql.DB, audit access.AuditIntentRecorder) PublicationRepository {
	return publicationsqlite.NewRepositoryWithAudit(db, audit)
}
func newSQLitePublicationStreams(db *sql.DB) publication.StreamRegistry {
	return publicationsqlite.NewStreamRegistry(db)
}

func NewDeliveryBroker() *DeliveryBroker {
	return dashboardstream.NewDeliveryBroker()
}

type Presentation = dashboardui.Presentation
type HTTP = dashboardhttp.Handler
type QueryFreshness = api.QueryFreshness
type AgentBootstrap = dashboardui.AgentBootstrap
type AgentCommandBindings = dashboardui.AgentCommandBindings
type ChatSignal = dashboardsignals.ChatSignal
type ChatConversationSummary = dashboardsignals.ChatConversationSummary
type ChatTranscriptItemSignal = dashboardsignals.ChatTranscriptItemSignal
type ChatArtifactSignal = dashboardsignals.ChatArtifactSignal
type AgentReferenceSignal = dashboardsignals.AgentReferenceSignal
type AgentReferenceLocationSignal = dashboardsignals.AgentReferenceLocationSignal
type AgentReferenceKeySignal = dashboardsignals.AgentReferenceKeySignal
type ChatStatus = dashboardsignals.ChatStatus
type ComposerSignal = dashboardsignals.ComposerSignal

type DashboardTelemetry interface {
	DashboardRefreshStarted(string)
	DashboardRefreshFinished(string, string, int, map[string]float64)
	DashboardRefreshEventObserved(string, string)
	VisualizationFrameObserved(kind string, rows, cardinality, encodedBytes int)
	DashboardCacheObserved(string)
	DashboardCacheObservationObserved(dataquery.CacheObservation)
	SpatialTileObserved(outcome, cache, precision string, queryMS, encodingMS int64, encodedBytes, features int, fallback bool)
}

type Telemetry interface {
	DashboardTelemetry
	PublicDocumentObserved(presentation, outcome string)
	PublicStreamStarted(presentation string) func()
	PublicCommandObserved(command, outcome string)
	PublicRateLimitObserved(family string)
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.RequireNativePersistence {
		if config.Database != nil {
			return nil, fmt.Errorf("dashboard native persistence rejects the legacy SQLite database handle")
		}
		if config.LegacySQLite {
			return nil, fmt.Errorf("dashboard native persistence rejects LegacySQLite mode")
		}
		if config.NativePersistence == nil {
			return nil, fmt.Errorf("dashboard native persistence bundle is required")
		}
		if !config.NativePersistence.valid() {
			return nil, fmt.Errorf("dashboard native persistence bundle is incomplete")
		}
		if config.SessionStore != nil || config.UsageRecorder != nil || config.UsageReader != nil || config.AppearanceStore != nil || config.AuditIntentRecorder != nil {
			return nil, fmt.Errorf("dashboard native persistence rejects partial or legacy authority injection")
		}
	} else if config.NativePersistence != nil {
		return nil, fmt.Errorf("dashboard native persistence bundle requires RequireNativePersistence")
	} else if config.Database != nil && !config.LegacySQLite {
		return nil, fmt.Errorf("dashboard database handle requires explicit LegacySQLite mode")
	}
	if config.RequireAuthoring && config.Authoring == nil {
		return nil, fmt.Errorf("dashboard authoring authority is required")
	}
	if config.RequirePublication {
		if config.NativePersistence == nil || config.NativePersistence.publication == nil || !config.NativePersistence.publication.IsNative() {
			return nil, fmt.Errorf("dashboard native publication authority is required")
		}
	}
	publicationAuditConfigured := false
	if config.RequireNativePersistence {
		// Native repositories own their transaction-scoped audit port. Keep the
		// same generated-command contract validation as the SQLite path, but do
		// not require (or accept) the legacy process-local recorder.
		if err := validatePublicationCommandAuditContracts(); err != nil {
			return nil, err
		}
		publicationAuditConfigured = true
	} else if config.Database != nil {
		if config.AuditIntentRecorder == nil {
			return nil, errPublicationCommandAuditUnavailable
		}
		if err := validatePublicationCommandAuditContracts(); err != nil {
			return nil, err
		}
		publicationAuditConfigured = true
	}
	if config.RequireNativePersistence && config.RequireAuthoring && (config.NativePersistence.authoring == nil || !config.NativePersistence.authoring.IsNative()) {
		return nil, fmt.Errorf("dashboard native authoring authority is required")
	}
	// Keep one process-local broker for the legacy/memory path when callers do
	// not supply an application-owned broker. The same instance is installed on
	// the handler and module so SSE and command refreshes share delivery state.
	if config.HTTP.Broker == nil {
		config.HTTP.Broker = dashboardstream.NewDeliveryBroker()
	}
	coordinators := dashboardstream.NewRegistry()
	optionCursorSecret := make([]byte, 32)
	if _, err := rand.Read(optionCursorSecret); err != nil {
		return nil, fmt.Errorf("generate dashboard option cursor secret: %w", err)
	}
	var sessionStore dashboardsession.Store = config.SessionStore
	usageRecorder, usageReader := config.UsageRecorder, config.UsageReader
	var appearanceStore dashboardappearance.Store = config.AppearanceStore
	if config.RequireNativePersistence {
		sessionStore = config.NativePersistence.session
		usageRecorder, usageReader = config.NativePersistence.usage, config.NativePersistence.usage
		appearanceStore = config.NativePersistence.appearance
	}
	if sessionStore == nil {
		sessionStore = dashboardsession.NewMemoryStore()
	}
	usageNow := config.UsageNow
	if usageNow == nil {
		usageNow = time.Now
	}
	telemetry := config.HTTP.Telemetry
	handler := dashboardhttp.Handler{
		Metrics:                    config.HTTP.Metrics,
		ProjectID:                  config.HTTP.ProjectID,
		ResolveProjectID:           config.HTTP.ResolveProjectID,
		ResolveDashboardAppearance: config.HTTP.ResolveDashboardAppearance,
		Authoring:                  config.Authoring,
		AnalyticalContext: func(ctx context.Context) context.Context {
			return workload.WithAdmitter(ctx, config.HTTP.Admission)
		},
		Broker: config.HTTP.Broker, Coordinators: coordinators, Logger: config.HTTP.Logger,
		RefreshStarted: func(refresh dashboardstream.Refresh) {
			if telemetry != nil {
				telemetry.DashboardRefreshStarted(refresh.Command)
			}
		},
		RefreshFinished: func(summary dashboardstream.RefreshSummary) {
			if telemetry != nil {
				telemetry.DashboardRefreshFinished(summary.Command, summary.Outcome, summary.CancellationCount, summary.StageTimingsMs)
			}
		},
		RefreshEventObserved: func(event dashboardstream.RefreshEvent) {
			if telemetry != nil {
				telemetry.DashboardRefreshEventObserved(string(event.Type), event.Target)
				observeVisualizationFrame(telemetry, event)
			}
		},
		CacheObserved: func(outcome string) {
			if telemetry != nil {
				telemetry.DashboardCacheObserved(outcome)
			}
		},
		CacheObservationObserved: func(observation dataquery.CacheObservation) {
			if telemetry != nil {
				telemetry.DashboardCacheObservationObserved(observation)
			}
		},
		SessionStore:       sessionStore,
		OptionCursorSecret: optionCursorSecret,
		OptionCache:        dashboardfilter.NewOptionCache(4096),
		CurrentPrincipalID: config.HTTP.CurrentPrincipalID, AuthorizeListResource: config.HTTP.AuthorizeListResource,
		CurrentUsagePrincipal: config.HTTP.CurrentUsagePrincipal,
		CSRFToken:             config.HTTP.CSRFToken, Layout: config.HTTP.Layout,
		Presentation: config.HTTP.Presentation,
		Assets:       config.HTTP.Assets,
		Environment:  config.HTTP.Environment, DataRefreshedAt: config.HTTP.DataRefreshedAt,
		QueryFreshness: config.HTTP.QueryFreshness,
		AgentBootstrap: config.HTTP.AgentBootstrap,
		AgentCommands:  config.HTTP.AgentCommands,
		SpatialTileStreamClosed: func(metrics dashboardhttp.Metrics, streamID string) {
			if expirer, ok := metrics.(interface{ ExpireVisualizationTileStream(string) }); ok {
				expirer.ExpireVisualizationTileStream(streamID)
			}
		},
	}
	if usageRecorder != nil {
		handler.RecordDashboardView = usageRecorder.RecordView
	}
	handler.SessionKey = func(r *http.Request, definition dashboarddefinition.Definition, clientID, streamInstanceID string) (dashboardsession.Key, error) {
		dashboardID, err := projectgraph.NewResourceID(definition.ID)
		if err != nil {
			return dashboardsession.Key{}, err
		}
		servingStateID := definition.DefaultFilterState().DefaultsRevision
		if config.ServingSnapshot != nil {
			active, err := config.ServingSnapshot(r.Context())
			if err != nil {
				return dashboardsession.Key{}, err
			}
			if active != "" {
				servingStateID = active
			}
		}
		projectID := config.HTTP.ProjectID
		if config.HTTP.ResolveProjectID != nil {
			resolved, err := config.HTTP.ResolveProjectID(r.Context())
			if err != nil {
				return dashboardsession.Key{}, err
			}
			projectID = resolved
		}
		if err := projectID.Validate(); err != nil {
			return dashboardsession.Key{}, err
		}
		principalOrClient := clientID
		if config.HTTP.CurrentPrincipalID != nil {
			if principalID := config.HTTP.CurrentPrincipalID(r); principalID != "" {
				principalOrClient = principalID + ":" + clientID
			}
		}
		return dashboardsession.Key{
			ProjectID:         projectID,
			PrincipalOrClient: principalOrClient,
			DashboardID:       dashboardID,
			ServingStateID:    servingStateID,
			StreamInstanceID:  streamInstanceID,
		}, nil
	}
	module := &Module{
		handler:   handler,
		authoring: config.Authoring,
		semantic: semanticapi.Handler{
			Metrics: config.Semantic.Metrics, ResolveProjectID: config.Semantic.ResolveProjectID,
			CurrentPrincipalID:    config.Semantic.CurrentPrincipalID,
			AuthorizeListResource: config.Semantic.AuthorizeListResource,
			QueryFreshness:        config.Semantic.QueryFreshness,
		},
		snapshot:  config.ServingSnapshot,
		publicURL: config.PublicURL, currentActor: config.CurrentActor,
		publicationAuditConfigured: publicationAuditConfigured,
		streams:                    publication.NewMemoryStreamRegistry(), publicBroker: config.HTTP.Broker,
		publicTelemetry: config.PublicTelemetry, dashboardTelemetry: config.HTTP.Telemetry, logger: config.Logger,
		runtimeMetrics: config.RuntimeMetrics,
		coordinators:   coordinators,
		usageReader:    usageReader, usageNow: usageNow,
		appearanceStore: appearanceStore,
	}
	if config.RequireNativePersistence {
		if config.NativePersistence.streams != nil {
			module.streams = config.NativePersistence.streams
		}
		if config.NativePersistence.broker != nil {
			module.publicBroker = config.NativePersistence.broker
		}
		// Streams and broker must be installed before constructing the service so
		// revocation closes the durable native registry, never the memory default.
		module.publications = config.NativePersistence.publication
		module.publicationService = publication.NewService(module.publications, module.streams.ClosePublication)
	} else if config.Database != nil {
		// Legacy SQLite remains explicit and test/development-only.
		module.publications = newSQLitePublicationRepository(config.Database, config.AuditIntentRecorder)
		module.streams = newSQLitePublicationStreams(config.Database)
		module.publicationService = publication.NewService(module.publications, module.streams.ClosePublication)
	}
	return module, nil
}

func observeVisualizationFrame(telemetry DashboardTelemetry, event dashboardstream.RefreshEvent) {
	if event.Type != dashboardstream.RefreshEventVisual && event.Type != dashboardstream.RefreshEventVisualMetadata {
		return
	}
	envelope, ok := event.Value.(visualizationir.VisualizationEnvelope)
	if !ok {
		return
	}
	rows, cardinality, kind := 0, 0, ""
	switch state := envelope.DataState.Value.(type) {
	case *visualizationir.InlineVisualizationDataState:
		kind = "inline"
		for _, dataset := range state.Datasets {
			rows += len(dataset.Rows)
		}
		cardinality = rows
	case *visualizationir.WindowedVisualizationDataState:
		kind = "windowed"
		for _, block := range state.Blocks {
			rows += len(block.Rows)
		}
		cardinality = int(state.AvailableRows)
		if state.Cardinality.Count != nil {
			cardinality = int(*state.Cardinality.Count)
		}
	default:
		return
	}
	encoded, _ := json.Marshal(envelope)
	telemetry.VisualizationFrameObserved(kind, rows, cardinality, len(encoded))
}

func (m *Module) HTTP() dashboardhttp.Handler      { return m.handler }
func (m *Module) SemanticAPI() semanticapi.Handler { return m.semantic }
func (m *Module) AppearanceStore() dashboardappearance.Store {
	if m == nil {
		return nil
	}
	return m.appearanceStore
}
func (m *Module) Authoring() *dashboardauthoringapplication.Application {
	if m == nil {
		return nil
	}
	return m.authoring
}

type PopularityLevel string

const (
	PopularityLow    PopularityLevel = "low"
	PopularityMedium PopularityLevel = "medium"
	PopularityHigh   PopularityLevel = "high"
)

// Popularity ranks dashboard usage across the instance for a configured
// dashboard population. Persistence and ranking stay owned by this capability;
// catalog consumers receive only the resulting module contract.
func (m *Module) Popularity(ctx context.Context, dashboardCount int) (map[string]PopularityLevel, error) {
	if m == nil || m.usageReader == nil {
		return nil, nil
	}
	summaries, err := m.usageReader.ListSummaries(ctx, m.usageNow().UTC().Add(-usage.PopularityWindow))
	if err != nil {
		return nil, err
	}
	levels := make(map[string]PopularityLevel)
	for _, ranked := range usage.RankPopularity(summaries, dashboardCount) {
		levels[ranked.CatalogID()] = PopularityLevel(ranked.Level)
	}
	return levels, nil
}
