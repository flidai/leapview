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

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/api"
	dashboardauthoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	dashboardhttp "github.com/flidai/leapview/internal/dashboard/http"
	"github.com/flidai/leapview/internal/dashboard/publication"
	publicationsqlite "github.com/flidai/leapview/internal/dashboard/publication/sqlite"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	dashboardsession "github.com/flidai/leapview/internal/dashboard/session"
	dashboardsessionsqlite "github.com/flidai/leapview/internal/dashboard/session/sqlite"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	dashboardui "github.com/flidai/leapview/internal/dashboard/ui"
	dashboardsignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/internal/dashboard/usage"
	dashboardusagesqlite "github.com/flidai/leapview/internal/dashboard/usage/sqlite"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
)

type Module struct {
	handler                    dashboardhttp.Handler
	authoring                  *dashboardauthoringapplication.Application
	semantic                   semanticapi.Handler
	snapshot                   func(context.Context) (string, error)
	publications               *publicationsqlite.Repository
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
	usageNow                   func() time.Time
	lifecycleMu                sync.Mutex
	lifecycleCancel            context.CancelFunc
	lifecycleWG                sync.WaitGroup
}

type Config struct {
	Database *sql.DB
	// Authoring is supplied by production composition. It remains optional at
	// this module boundary so focused dashboard-module tests can exercise the
	// read/render surface without constructing runtime-backed authoring ports.
	Authoring       *dashboardauthoringapplication.Application
	HTTP            HTTPConfig
	Semantic        SemanticConfig
	ServingSnapshot func(context.Context) (string, error)
	PublicTelemetry PublicTelemetry
	Logger          *slog.Logger
	Trace           *pagestream.TraceStore
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

type HTTPConfig struct {
	Metrics               queryruntime.Metrics
	ProjectID             projectgraph.ResourceID
	ResolveProjectID      func(context.Context) (projectgraph.ResourceID, error)
	Admission             workload.Admitter
	Broker                SignalBroker
	Logger                *slog.Logger
	Telemetry             DashboardTelemetry
	CurrentPrincipalID    func(*http.Request) string
	CurrentUsagePrincipal func(*http.Request) (string, bool)
	AuthorizeListResource func(context.Context, string, access.ResourceRef, access.Capability) (bool, error)
	CSRFToken             func(*http.Request) string
	Layout                func(*http.Request) webpage.Provider
	Environment           func(*http.Request) string
	DataRefreshedAt       func(context.Context, string, string, string) string
	QueryFreshness        func(context.Context, string, string, string) (api.QueryFreshness, bool)
	AgentBootstrap        func(*http.Request, string) dashboardui.AgentBootstrap
	AgentCommands         dashboardui.AgentCommandBindings
	Presentation          dashboardui.Presentation
	Assets                staticasset.Resolver
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
	PublishEnvelope(string, pagestream.Envelope)
	TraceStore() *pagestream.TraceStore
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
	publicationAuditConfigured := false
	if config.Database != nil {
		if config.AuditIntentRecorder == nil {
			return nil, errPublicationCommandAuditUnavailable
		}
		if err := validatePublicationCommandAuditContracts(); err != nil {
			return nil, err
		}
		publicationAuditConfigured = true
	}
	coordinators := dashboardstream.NewRegistry()
	optionCursorSecret := make([]byte, 32)
	if _, err := rand.Read(optionCursorSecret); err != nil {
		return nil, fmt.Errorf("generate dashboard option cursor secret: %w", err)
	}
	var sessionStore dashboardsession.Store = dashboardsession.NewMemoryStore()
	if config.Database != nil {
		sessionStore = dashboardsessionsqlite.NewStore(config.Database)
	}
	usageRecorder, usageReader := config.UsageRecorder, config.UsageReader
	if config.Database != nil && (usageRecorder == nil || usageReader == nil) {
		repository := dashboardusagesqlite.NewRepository(config.Database)
		if usageRecorder == nil {
			usageRecorder = repository
		}
		if usageReader == nil {
			usageReader = repository
		}
	}
	usageNow := config.UsageNow
	if usageNow == nil {
		usageNow = time.Now
	}
	telemetry := config.HTTP.Telemetry
	handler := dashboardhttp.Handler{
		Metrics:          config.HTTP.Metrics,
		ProjectID:        config.HTTP.ProjectID,
		ResolveProjectID: config.HTTP.ResolveProjectID,
		Authoring:        config.Authoring,
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
	}
	if config.Database != nil {
		module.publications = publicationsqlite.NewRepositoryWithAudit(config.Database, config.AuditIntentRecorder)
		module.streams = publicationsqlite.NewStreamRegistry(config.Database)
		module.publicBroker = publicationsqlite.NewBroker(config.Database, config.Trace, config.Logger)
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
