// Package module owns refresh transport and worker lifecycle composition.
package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	uicommand "github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshanalytics "github.com/flidai/leapview/internal/refresh/analyticsruntime"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	materializehttp "github.com/flidai/leapview/internal/refresh/http"
	refreshpostgres "github.com/flidai/leapview/internal/refresh/postgres"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
)

type Dispatcher interface {
	Run(context.Context)
}

type Scheduler interface {
	DispatchDue(context.Context) error
}

func classifyQueueAdmissionError(err error) error {
	if errors.Is(err, refreshpostgres.ErrConflict) {
		return refreshrun.ErrInvocationConflict
	}
	return err
}

// PublishedDataVersion is the durable data snapshot established by the
// canonical publication that activated one serving generation. It provides
// the baseline freshness before that generation has completed a later
// semantic-model refresh.
type PublishedDataVersion struct {
	SnapshotID  int64
	RefreshedAt time.Time
}

type PublishedDataVersionResolver func(context.Context, projectgraph.ServingIdentity) (PublishedDataVersion, bool, error)

type Config struct {
	// Persistence is the capability-owned refresh store.  Production callers
	// inject a complete bundle. SQLite callers must construct
	// NewSQLitePersistence explicitly.
	Persistence *Persistence
	// Production requires the typed PostgreSQL persistence bundle. The module
	// never infers a backend from the repository interfaces in Persistence.
	Production          bool
	HTTP                HTTPConfig
	Authorization       AuthorizationConfig
	Service             refreshrun.Service
	Analytics           analyticsmaterialization.Executor
	Artifacts           refreshrun.ArtifactLoader
	ManagedData         runtimehost.ManagedDataResolver
	Admission           workload.Admitter
	LeaseTimeout        time.Duration
	ResolveIdentity     func(context.Context) (projectgraph.ServingIdentity, error)
	PublishedVersion    PublishedDataVersionResolver
	WorkloadStats       func() workload.Stats
	RunFinished         func(context.Context, refreshrun.RunRecord)
	Events              EventStore
	AuditIntentRecorder access.AuditIntentRecorder
	Clock               refreshschedule.Clock
	EnableDispatcher    bool
	EnableScheduler     bool
	Dispatcher          Dispatcher
	Scheduler           Scheduler
	ReconcileSchedules  func(context.Context) error
	ScheduleInterval    time.Duration
	Logger              *slog.Logger
	RecoveryLifecycle   *RecoveryLifecycle
	RecoveryInterval    time.Duration
	// RecoveryEnvironment is the exact configured serving environment used by
	// startup reconciliation. It is never inferred from a mutable active
	// serving pointer.
	RecoveryEnvironment string
}

type HTTPConfig struct {
	RunnerConfigured func() bool
	CurrentPrincipal func(*http.Request) (HTTPPrincipal, bool)
	ServingIdentity  func(*http.Request) (projectgraph.ServingIdentity, error)
}

type HTTPPrincipal struct {
	ID string
}

type AuthorizationPrincipal struct {
	ID        string
	DevBypass bool
}

type AuthorizationConfig struct {
	CurrentPrincipal  func(*http.Request) (AuthorizationPrincipal, bool)
	CurrentCredential func(*http.Request) (access.APICredential, bool)
	AuthorizeObject   func(context.Context, string, access.Capability, access.ResourceRef) (bool, error)
}

type Module struct {
	handler             materializehttp.Handler
	runs                RunPersistence
	schedules           refreshschedule.Repository
	service             refreshrun.Service
	refreshClock        refreshschedule.Clock
	dispatcher          Dispatcher
	scheduler           Scheduler
	reconcileSchedules  func(context.Context) error
	scheduleInterval    time.Duration
	leaseTimeout        time.Duration
	logger              *slog.Logger
	events              EventStore
	durableAudit        bool
	refreshExecution    apigencommand.AsyncExecutionContract
	resolveIdentity     func(context.Context) (projectgraph.ServingIdentity, error)
	publishedVersion    PublishedDataVersionResolver
	recoveryLifecycle   *RecoveryLifecycle
	recoveryInterval    time.Duration
	terminalRecovery    TerminalRunRecovery
	recoveryEnvironment string

	mu          sync.Mutex
	background  context.Context
	cancel      context.CancelFunc
	started     bool
	stopping    bool
	stopped     bool
	dispatching bool
	wg          sync.WaitGroup
}

func durableRefreshAudit(config Config) bool {
	// Build validates a PostgreSQL backend's concrete native repository before
	// this value can reach a live module. Inspecting the backend marker here
	// keeps the audit guarantee independent of repository implementation detail.
	return config.AuditIntentRecorder != nil || (config.Persistence != nil && config.Persistence.backend == backendPostgres)
}

func Build(ctx context.Context, config Config) (*Module, error) {
	if config.Production {
		if config.Persistence == nil || !config.Persistence.isPostgres() {
			return nil, errors.New("production refresh module requires PostgreSQL persistence")
		}
	} else if config.Persistence != nil && config.Persistence.isPostgres() {
		return nil, errors.New("native PostgreSQL refresh persistence requires production refresh mode")
	}
	if config.RecoveryLifecycle != nil {
		if err := config.RecoveryLifecycle.Validate(); err != nil {
			return nil, fmt.Errorf("configure scheduled recovery qualification: %w", err)
		}
	}
	if config.Authorization.AuthorizeObject == nil {
		return nil, errors.New("refresh object authorizer is required")
	}
	refreshExecution, err := loadRefreshExecutionContract()
	if err != nil {
		return nil, err
	}
	interval := config.ScheduleInterval
	if interval <= 0 {
		interval = time.Minute
	}
	recoveryInterval := config.RecoveryInterval
	if recoveryInterval <= 0 {
		recoveryInterval = time.Minute
	}
	leaseTimeout := config.LeaseTimeout
	if leaseTimeout <= 0 {
		leaseTimeout = 2 * time.Minute
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}
	m := &Module{
		handler: materializehttp.Handler{
			RunnerConfigured: config.HTTP.RunnerConfigured,
			ServingIdentity:  config.HTTP.ServingIdentity,
		},
		dispatcher: config.Dispatcher, scheduler: config.Scheduler,
		refreshClock:       config.Clock,
		reconcileSchedules: config.ReconcileSchedules, scheduleInterval: interval,
		leaseTimeout: leaseTimeout, logger: logger,
		events: config.Events,
		// Native PostgreSQL admission records the generated create audit intent
		// inside the same transaction as the operation, run tree, queue job, and
		// initial lifecycle event. It therefore satisfies the transactional
		// command guarantee without the legacy post-commit audit recorder.
		durableAudit:      durableRefreshAudit(config),
		refreshExecution:  refreshExecution,
		resolveIdentity:   config.ResolveIdentity,
		publishedVersion:  config.PublishedVersion,
		recoveryLifecycle: config.RecoveryLifecycle, recoveryInterval: recoveryInterval,
	}
	m.handler.CurrentPrincipal = func(r *http.Request) (materializehttp.Principal, bool) {
		if config.HTTP.CurrentPrincipal == nil {
			return materializehttp.Principal{}, false
		}
		principal, ok := config.HTTP.CurrentPrincipal(r)
		return materializehttp.Principal{ID: principal.ID}, ok
	}
	m.handler.AuthorizePipelineView = func(r *http.Request, identity projectgraph.ServingIdentity, pipelineID string) (bool, error) {
		return authorizePipeline(r, identity, pipelineID, access.CapabilityResourceRead, config.Authorization)
	}
	m.handler.AuthorizePipelineRun = func(r *http.Request, identity projectgraph.ServingIdentity, pipelineID string) (bool, error) {
		return authorizePipeline(r, identity, pipelineID, access.CapabilityResourceUse, config.Authorization)
	}
	m.handler.RunCreated = m.verifyRunCreated
	// Native PostgreSQL admission records generated create audit intents
	// through its transaction-aware persistence writer; it does not expose the
	// database/sql recorder used by the legacy SQLite outbox.
	if config.AuditIntentRecorder != nil || (config.Persistence != nil && config.Persistence.isPostgres()) {
		m.handler.BuildAuditIntent = buildRefreshAuditIntent
	}
	if config.Persistence == nil {
		if config.EnableDispatcher || config.EnableScheduler || config.Service.Runs != nil || config.RecoveryLifecycle != nil {
			return nil, errors.New("refresh persistence is required")
		}
		return m, nil
	}
	persistence := *config.Persistence
	if err := persistence.Validate(); err != nil {
		return nil, err
	}
	if config.Production {
		publication, ok := persistence.Publication.(*postgresPublicationPersistence)
		if !ok || publication == nil || isNilPostgresCapability(publication.nativeFinalizer) {
			return nil, errors.New("production refresh module requires a native finalizer")
		}
	}
	if config.RecoveryLifecycle != nil && persistence.Recovery == nil {
		return nil, errors.New("refresh recovery persistence is required when recovery lifecycle is configured")
	}
	m.runs = persistence.Runs
	m.schedules = persistence.Schedules
	m.service = config.Service
	if m.service.Artifacts == nil {
		m.service.Artifacts = config.Artifacts
	}
	if m.service.Materializer == nil {
		m.service.Materializer = refreshanalytics.RefreshMaterializer{
			Executor: config.Analytics, ManagedData: config.ManagedData,
		}
	}
	m.service.Runs = m.runs
	m.service.DataVersions = m.schedules
	m.service.Publication = persistence.Publication
	m.terminalRecovery = persistence.TerminalRecovery
	m.recoveryEnvironment = strings.TrimSpace(config.RecoveryEnvironment)
	if config.RecoveryLifecycle != nil && persistence.Recovery != nil {
		config.RecoveryLifecycle.Repository = persistence.Recovery
	}
	if m.dispatcher == nil && config.EnableDispatcher {
		if config.ResolveIdentity == nil {
			return nil, errors.New("refresh dispatcher identity resolver is required")
		}
		m.dispatcher = refreshrun.Dispatcher{
			Runs: m.runs, Service: m.service, Admitter: config.Admission,
			LeaseTimeout: config.LeaseTimeout, Logger: logger, ResolveIdentity: config.ResolveIdentity,
			WorkloadStats: config.WorkloadStats, RunFinished: m.runFinished(config.RunFinished),
		}
	}
	if m.scheduler == nil && config.EnableScheduler {
		if config.ResolveIdentity == nil {
			return nil, errors.New("refresh scheduler identity resolver is required")
		}
		m.scheduler = refreshschedule.Scheduler{
			Repository: m.schedules, Clock: config.Clock, ResolveIdentity: config.ResolveIdentity,
			Trigger: func(ctx context.Context, occurrence refreshschedule.Occurrence) error {
				result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
					Identity: occurrence.Identity, PrincipalID: "scheduler", EstimatedMemoryBytes: 1,
					PipelineID: occurrence.PipelineID, TriggerType: refreshrun.TriggerSchedule,
					ArtifactDigest: occurrence.ArtifactDigest, Occurrence: &occurrence,
				})
				if err == nil {
					if result.Run.Status == refreshrun.RunStatusSkipped {
						return refreshschedule.ErrOccurrenceSkipped
					}
					m.Dispatch(ctx)
				}
				return err
			},
		}
	}
	if m.reconcileSchedules == nil && m.schedules != nil {
		m.reconcileSchedules = m.Reconcile
	}
	m.handler.Repository = func() (refreshrun.RunRepository, error) { return m.runs, nil }
	m.handler.DispatchQueued = func() { m.Dispatch(context.Background()) }
	m.handler.QueuePipeline = func(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID, principalID, idempotencyKey string) (refreshrun.RunRecord, error) {
		pipelineIDValue, parseErr := projectgraph.NewResourceID(pipelineID)
		if parseErr != nil {
			return refreshrun.RunRecord{}, parseErr
		}
		var intent *access.AuditIntent
		if fromContext, ok := refreshrun.AuditIntentFromContext(ctx); ok {
			intent = &fromContext
		}
		result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
			Identity: identity, PrincipalID: principalID, EstimatedMemoryBytes: 1,
			PipelineID: pipelineIDValue, TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual,
			AuditIntent: intent, IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			err = classifyQueueAdmissionError(err)
			m.logger.ErrorContext(ctx, "queue refresh pipeline failed",
				slog.String("project_id", identity.ProjectID.String()),
				slog.String("serving_state_id", identity.GenerationID),
				slog.String("pipeline_id", pipelineID),
				slog.String("error", err.Error()),
			)
		}
		return result.Run, err
	}
	return m, nil
}

func (m *Module) QueuePipelineRefresh(ctx context.Context, input refreshrun.QueuePipelineInput) (refreshrun.QueueAssetResult, error) {
	if m == nil || m.runs == nil {
		return refreshrun.QueueAssetResult{}, errors.New("refresh persistence is not configured")
	}
	return m.service.QueuePipelineRefresh(ctx, input)
}

// UICommandBindings exposes the generated browser command identities for
// monitor surfaces that do not yet have an asset-specific refresh snapshot.
func (*Module) UICommandBindings() (uicommand.Binding, uicommand.Binding) {
	return refreshgen.GenUIActionCreateRefreshRun(), refreshgen.GenUIActionCancelRefreshRun()
}

type PipelineUICommandInvocation struct {
	Action         string
	Project        string
	IdempotencyKey string
	RequestID      string
	CorrelationID  string
}

func (*Module) BeginPipelineUICommand(ctx context.Context, invocation PipelineUICommandInvocation) (context.Context, error) {
	if invocation.Action == "cancel" {
		started, _, err := refreshgen.BeginGenCancelRefreshRunCommand(ctx, refreshgen.GenCancelRefreshRunCommandInvocation{Surface: apigencommand.SurfaceUI, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
		return started, err
	}
	if invocation.Action != "run" && invocation.Action != "retry" {
		return ctx, errors.New("unsupported pipeline UI command")
	}
	started, _, err := refreshgen.BeginGenCreateRefreshRunCommand(ctx, refreshgen.GenCreateRefreshRunCommandInvocation{Surface: apigencommand.SurfaceUI, Project: invocation.Project, IdempotencyKey: invocation.IdempotencyKey, RequestID: invocation.RequestID, CorrelationID: invocation.CorrelationID})
	return started, err
}

// ActiveServingIdentity resolves the exact generation used by refresh writes.
// Browser commands use this fence instead of accepting a project or generation
// selector from the client.
func (m *Module) ActiveServingIdentity(ctx context.Context) (projectgraph.ServingIdentity, error) {
	if m == nil {
		return projectgraph.ServingIdentity{}, errors.New("refresh service is unavailable")
	}
	if m.resolveIdentity != nil {
		return m.resolveIdentity(ctx)
	}
	return projectgraph.ServingIdentity{}, errors.New("refresh serving identity resolver is unavailable")
}

// QueuePipelineRefreshForUI queues a browser-originated run after the caller
// has authorized the pipeline. It retains the same generated audit and
// dispatch guarantees as the API command path.
func (m *Module) QueuePipelineRefreshForUI(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID, principalID, retryOf string) error {
	if m == nil || m.service.Runs == nil {
		return errors.New("refresh service is unavailable")
	}
	pipeline, err := projectgraph.NewResourceID(pipelineID)
	if err != nil {
		return err
	}
	if retryOf != "" && m.runs != nil {
		scope, scopeErr := refreshrun.ReadScopeForIdentity(identity)
		if scopeErr != nil {
			return scopeErr
		}
		runs, readErr := m.readRuns()
		if readErr != nil {
			return readErr
		}
		prior, getErr := runs.GetRun(ctx, scope, retryOf)
		if getErr != nil || !scope.Matches(prior.Identity) || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.PipelineID != pipeline || prior.Status == refreshrun.RunStatusQueued || prior.Status == refreshrun.RunStatusRunning {
			return errors.New("refresh retry is invalid")
		}
	}
	// ADR-0014 models a retry as a fresh manual invocation. The prior run is
	// validated above for UI safety, but it is not retained as mutable execution
	// state on the new immutable pipeline occurrence.
	result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
		Identity: identity, PipelineID: pipeline, PrincipalID: principalID,
		EstimatedMemoryBytes: 1, TriggerType: refreshrun.TriggerManual, InvocationSource: refreshrun.TriggerManual,
	})
	if err != nil {
		return err
	}
	if err := m.verifyRunCreated(ctx, result.Run); err != nil {
		return err
	}
	m.Dispatch(ctx)
	return nil
}

// CancelPipelineRefreshForUI cancels a queued root pipeline run and verifies
// its generated lifecycle audit before acknowledging the browser command.
func (m *Module) CancelPipelineRefreshForUI(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID, runID, principalID string) error {
	if m == nil || m.runs == nil {
		return errors.New("refresh service is unavailable")
	}
	if strings.TrimSpace(principalID) == "" {
		return errors.New("refresh principal is unavailable")
	}
	pipeline, err := projectgraph.NewResourceID(pipelineID)
	if err != nil {
		return errors.New("refresh run not found")
	}
	scope, err := refreshrun.ReadScopeForIdentity(identity)
	if err != nil {
		return err
	}
	runs, readErr := m.readRuns()
	if readErr != nil {
		return readErr
	}
	prior, err := runs.GetRun(ctx, scope, runID)
	if err != nil || !scope.Matches(prior.Identity) || prior.TargetType != refreshrun.TargetRefreshPipeline || prior.ParentRunID != "" || prior.PipelineID != pipeline || prior.TargetID != pipeline {
		return errors.New("refresh run not found")
	}
	cancel, cancelErr := m.cancelRuns()
	if cancelErr != nil {
		return cancelErr
	}
	row, err := cancel.CancelRun(ctx, prior.Identity, runID)
	if err != nil {
		return err
	}
	return m.verifyRunCancelled(ctx, row)
}

// DataVersion returns the latest persisted semantic-model version for the
// active serving generation in the requested project/environment scope.
func (m *Module) DataVersion(ctx context.Context, projectID, environment, modelID string) (AssetDataVersion, bool, error) {
	if m == nil || m.schedules == nil {
		return AssetDataVersion{}, false, nil
	}
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	var identity projectgraph.ServingIdentity
	if m.resolveIdentity != nil {
		identity, err = m.resolveIdentity(ctx)
		if err != nil {
			return AssetDataVersion{}, false, err
		}
		if identity.ProjectID != project || identity.Environment != environment {
			return AssetDataVersion{}, false, fmt.Errorf("active refresh identity does not match requested scope")
		}
	} else {
		if m.service.ServingStates == nil {
			return AssetDataVersion{}, false, nil
		}
		state, _, activeErr := m.service.ServingStates.ActiveArtifact(ctx, project, servingstate.Environment(environment))
		if activeErr != nil {
			return AssetDataVersion{}, false, activeErr
		}
		identity, err = projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
		if err != nil {
			return AssetDataVersion{}, false, err
		}
	}
	model, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	version, found, err := m.dataVersionForIdentity(ctx, identity, model)
	if err != nil || !found {
		return AssetDataVersion{}, found, err
	}
	return AssetDataVersion{SnapshotID: version.SnapshotID, ServingStateID: version.Identity.GenerationID, RefreshedAt: version.RefreshedAt, Source: version.Source}, true, nil
}

// AssetRefreshState returns the durable presentation state for a refresh
// pipeline in the requested project/environment scope. Run reads use the
// project/environment scope so history remains visible after generation
// replacement; schedule and data-version reads remain pinned to the active
// serving generation.
func (m *Module) AssetRefreshState(ctx context.Context, projectID projectgraph.ResourceID, environment string, pipelineID, modelID projectgraph.ResourceID) (AssetRefreshState, error) {
	state := AssetRefreshState{
		RunCommand:    refreshgen.GenUIActionCreateRefreshRun(),
		CancelCommand: refreshgen.GenUIActionCancelRefreshRun(),
	}
	if err := projectID.Validate(); err != nil {
		return state, err
	}
	if err := pipelineID.Validate(); err != nil {
		return state, err
	}
	environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(environment)))
	if m == nil || m.runs == nil {
		state.Unavailable = true
		return state, nil
	}
	runStore, readErr := m.readRuns()
	if readErr != nil {
		state.Unavailable = true
		return state, nil
	}
	scope := refreshrun.ReadScope{ProjectID: projectID, Environment: environment}
	if err := scope.Validate(); err != nil {
		return state, err
	}
	runs, err := runStore.ListTargetRuns(ctx, scope, refreshrun.TargetRefreshPipeline, pipelineID, refreshrun.RunPage{Limit: 50})
	if err != nil {
		return state, err
	}
	state.Runs = make([]AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		state.Runs = append(state.Runs, assetRefreshRun(run))
	}
	if len(state.Runs) > 0 {
		state.Latest = state.Runs[0]
	}
	latest, ok, err := runStore.LatestSuccessfulTargetRun(ctx, scope, refreshrun.TargetRefreshPipeline, pipelineID)
	if err != nil {
		return state, err
	}
	if ok {
		state.LatestSuccessful = assetRefreshRun(latest)
	}
	if m.schedules == nil {
		return state, nil
	}
	identity, err := m.activeServingIdentity(ctx, projectID, environment)
	if err != nil {
		return state, err
	}
	if next, ok, err := m.schedules.NextRun(ctx, identity, pipelineID); err != nil {
		return state, err
	} else if ok {
		state.NextRun = next
	}
	if modelID == "" {
		return state, nil
	}
	if err := modelID.Validate(); err != nil {
		return state, err
	}
	if version, ok, err := m.dataVersionForIdentity(ctx, identity, modelID); err != nil {
		return state, err
	} else if ok {
		state.DataVersion = AssetDataVersion{SnapshotID: version.SnapshotID, ServingStateID: version.Identity.GenerationID, RefreshedAt: version.RefreshedAt, Source: version.Source}
	}
	return state, nil
}

// ModelRefreshState returns the durable child-run history for one model table.
// Model runs are targeted explicitly by the refresh service, so the history
// remains correct when multiple pipelines materialize the same model.
func (m *Module) ModelRefreshState(ctx context.Context, projectID projectgraph.ResourceID, environment string, modelID projectgraph.ResourceID) (AssetRefreshState, error) {
	state := AssetRefreshState{}
	if err := projectID.Validate(); err != nil {
		return state, err
	}
	if err := modelID.Validate(); err != nil {
		return state, err
	}
	environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(environment)))
	if m == nil || m.runs == nil {
		state.Unavailable = true
		return state, nil
	}
	runStore, readErr := m.readRuns()
	if readErr != nil {
		state.Unavailable = true
		return state, nil
	}
	scope := refreshrun.ReadScope{ProjectID: projectID, Environment: environment}
	if err := scope.Validate(); err != nil {
		return state, err
	}
	runs, err := runStore.ListTargetRuns(ctx, scope, refreshrun.TargetModelTable, modelID, refreshrun.RunPage{Limit: 50})
	if err != nil {
		return state, err
	}
	state.Runs = make([]AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		state.Runs = append(state.Runs, assetRefreshRun(run))
	}
	if len(state.Runs) > 0 {
		state.Latest = state.Runs[0]
	}
	latest, ok, err := runStore.LatestSuccessfulTargetRun(ctx, scope, refreshrun.TargetModelTable, modelID)
	if err != nil {
		return state, err
	}
	if ok {
		state.LatestSuccessful = assetRefreshRun(latest)
	}
	return state, nil
}

// SemanticModelRefreshState returns root pipeline runs that materialize the
// requested semantic model. A semantic model may be refreshed by multiple
// pipelines, so the projection is keyed by semantic-model identity rather
// than by one pipeline target.
func (m *Module) SemanticModelRefreshState(ctx context.Context, projectID projectgraph.ResourceID, environment string, semanticModelID projectgraph.ResourceID) (AssetRefreshState, error) {
	state := AssetRefreshState{}
	if err := projectID.Validate(); err != nil {
		return state, err
	}
	if err := semanticModelID.Validate(); err != nil {
		return state, err
	}
	environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(environment)))
	if m == nil || m.runs == nil {
		state.Unavailable = true
		return state, nil
	}
	runStore, readErr := m.readRuns()
	if readErr != nil {
		state.Unavailable = true
		return state, nil
	}
	scope := refreshrun.ReadScope{ProjectID: projectID, Environment: environment}
	if err := scope.Validate(); err != nil {
		return state, err
	}
	runs, err := runStore.ListSemanticModelRuns(ctx, scope, semanticModelID, refreshrun.RunPage{Limit: 50})
	if err != nil {
		return state, err
	}
	state.Runs = make([]AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		state.Runs = append(state.Runs, assetRefreshRun(run))
	}
	if len(state.Runs) > 0 {
		state.Latest = state.Runs[0]
	}
	latest, ok, err := runStore.LatestSuccessfulSemanticModelRun(ctx, scope, semanticModelID)
	if err != nil {
		return state, err
	}
	if ok {
		state.LatestSuccessful = assetRefreshRun(latest)
	}
	if m.schedules == nil {
		return state, nil
	}
	identity, err := m.activeServingIdentity(ctx, projectID, environment)
	if err != nil {
		return state, err
	}
	if version, found, err := m.dataVersionForIdentity(ctx, identity, semanticModelID); err != nil {
		return state, err
	} else if found {
		state.DataVersion = AssetDataVersion{SnapshotID: version.SnapshotID, ServingStateID: version.Identity.GenerationID, RefreshedAt: version.RefreshedAt, Source: version.Source}
	}
	return state, nil
}

func (m *Module) activeServingIdentity(ctx context.Context, projectID projectgraph.ResourceID, environment string) (projectgraph.ServingIdentity, error) {
	if m.resolveIdentity != nil {
		identity, err := m.resolveIdentity(ctx)
		if err != nil {
			return projectgraph.ServingIdentity{}, err
		}
		if identity.ProjectID != projectID || identity.Environment != environment {
			return projectgraph.ServingIdentity{}, fmt.Errorf("active refresh identity does not match requested scope")
		}
		return identity, nil
	}
	if m.service.ServingStates == nil {
		return projectgraph.ServingIdentity{}, errors.New("active serving state is unavailable")
	}
	state, _, err := m.service.ServingStates.ActiveArtifact(ctx, projectID, servingstate.Environment(environment))
	if err != nil {
		return projectgraph.ServingIdentity{}, err
	}
	return projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
}

func (m *Module) dataVersionForIdentity(ctx context.Context, identity projectgraph.ServingIdentity, modelID projectgraph.ResourceID) (refreshschedule.DataVersion, bool, error) {
	if m == nil || m.schedules == nil {
		return refreshschedule.DataVersion{}, false, nil
	}
	version, found, err := m.schedules.DataVersion(ctx, identity, modelID)
	if err != nil || found || m.publishedVersion == nil {
		return version, found, err
	}
	published, found, err := m.publishedVersion(ctx, identity)
	if err != nil || !found {
		return refreshschedule.DataVersion{}, found, err
	}
	if published.SnapshotID <= 0 || published.RefreshedAt.IsZero() {
		return refreshschedule.DataVersion{}, false, errors.New("canonical publication data version is incomplete")
	}
	return refreshschedule.DataVersion{
		Identity: identity, SemanticModelID: modelID,
		SnapshotID: published.SnapshotID, RefreshedAt: published.RefreshedAt.UTC(),
		Source: refreshschedule.DataVersionSourcePublish,
	}, true, nil
}

func assetRefreshRun(run refreshrun.RunRecord) AssetRefreshRun {
	return AssetRefreshRun{
		ID: run.ID, Environment: run.Identity.Environment, ModelID: run.SemanticModelID.String(),
		ServingStateID: run.Identity.GenerationID, PrincipalID: run.PrincipalID,
		PrincipalDisplayName: run.PrincipalDisplayName, TriggerType: run.TriggerType,
		ParentRunID: run.ParentRunID, TargetGeneration: run.TargetRevision,
		Status: run.Status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
	}
}

type activeServingStates interface {
	ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error)
	ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
}

type semanticModelVersionPublisher interface {
	PublishSemanticModelVersion(context.Context, projectgraph.ServingIdentity, projectgraph.ResourceID)
}

func (m *Module) Reconcile(ctx context.Context) error {
	if m == nil || m.schedules == nil || m.service.ServingStates == nil || m.service.Artifacts == nil {
		return errors.New("refresh schedule reconciliation is not configured")
	}
	states, ok := m.service.ServingStates.(activeServingStates)
	if !ok {
		return errors.New("serving-state port does not support active scope discovery")
	}
	scopes, err := states.ListActiveScopes(ctx)
	if err != nil {
		return err
	}
	if len(scopes) == 0 {
		return nil
	}
	if len(scopes) > 1 {
		return fmt.Errorf("refresh schedule reconciliation requires exactly one active serving scope, found %d", len(scopes))
	}
	clock := m.clock()
	var reconcileErrors []error
	for _, scope := range scopes {
		projectID := scope.ProjectID
		state, artifact, err := states.ActiveArtifact(ctx, scope.ProjectID, scope.Environment)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		loaded, err := m.service.Artifacts.Load(ctx, artifact)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if loaded.Definition == nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("project %q has no compiled definition", projectID))
			continue
		}
		pipelines := make([]refreshschedule.Definition, 0, len(loaded.Definition.Pipelines))
		for _, pipeline := range loaded.Definition.Pipelines {
			pipelines = append(pipelines, pipeline)
		}
		sort.Slice(pipelines, func(i, j int) bool { return pipelines[i].ID < pipelines[j].ID })
		identity, identityErr := projectgraph.NewServingIdentity(state.ProjectID, string(scope.Environment), string(state.ID))
		if identityErr != nil {
			reconcileErrors = append(reconcileErrors, identityErr)
			continue
		}
		if err := m.schedules.Reconcile(ctx, refreshschedule.ReconcileInput{
			Identity: identity, ArtifactDigest: artifact.Digest,
			Pipelines: pipelines, Now: clock.Now(),
		}); err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		if state.Source != servingstate.SourcePublish || state.DuckLakeSnapshotID <= 0 {
			continue
		}
		if m.publishedVersion != nil {
			published, found, publishedErr := m.publishedVersion(ctx, identity)
			if publishedErr != nil {
				reconcileErrors = append(reconcileErrors, publishedErr)
				continue
			}
			if !found {
				reconcileErrors = append(reconcileErrors, errors.New("canonical publication data version is unavailable"))
				continue
			}
			if published.SnapshotID <= 0 || published.RefreshedAt.IsZero() {
				reconcileErrors = append(reconcileErrors, errors.New("canonical publication data version is incomplete"))
				continue
			}
			if published.SnapshotID != state.DuckLakeSnapshotID {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("canonical publication snapshot %d differs from serving-state snapshot %d", published.SnapshotID, state.DuckLakeSnapshotID))
				continue
			}
			// Deployment publication owns this baseline. The resolver has already
			// proved the exact committed publication, so refresh must not create a
			// duplicate refresh.data_version row or publish a second invalidation.
			continue
		}
		refreshedAt, err := parseServingStateTime(state.ActivatedAt)
		if err != nil {
			reconcileErrors = append(reconcileErrors, err)
			continue
		}
		for modelID := range loaded.Definition.Models {
			modelResource, modelErr := projectgraph.NewResourceID(modelID)
			if modelErr != nil {
				reconcileErrors = append(reconcileErrors, modelErr)
				continue
			}
			current, found, err := m.schedules.DataVersion(ctx, identity, modelResource)
			if err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
			if found && current.Identity == identity {
				continue
			}
			if err := m.schedules.SaveDataVersion(ctx, refreshschedule.DataVersion{
				Identity: identity, SemanticModelID: modelResource,
				SnapshotID: state.DuckLakeSnapshotID, RefreshedAt: refreshedAt,
				Source: refreshschedule.DataVersionSourcePublish,
			}); err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
			if publisher, ok := m.service.Publisher.(semanticModelVersionPublisher); ok {
				publisher.PublishSemanticModelVersion(ctx, identity, modelResource)
			}
		}
	}
	return errors.Join(reconcileErrors...)
}

func (m *Module) clock() refreshschedule.Clock {
	if m.refreshClock != nil {
		return m.refreshClock
	}
	return refreshschedule.RealClock{}
}

func parseServingStateTime(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid serving-state activation time %q", value)
}

func (m *Module) HTTP() materializehttp.Handler {
	if m == nil {
		return materializehttp.Handler{}
	}
	return m.handler
}

func (m *Module) Start(ctx context.Context) error {
	if m == nil {
		return errors.New("refresh module is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errors.New("refresh module has stopped")
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	if m.terminalRecovery != nil {
		if err := RecoverWithPersistence(ctx, m.terminalRecovery, m.recoveryEnvironment); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("refresh startup recovery: %w", err)
		}
	}
	m.background, m.cancel = context.WithCancel(ctx)
	m.started = true
	background := m.background
	if m.scheduler != nil {
		m.wg.Add(1)
		go m.runScheduler(background)
	}
	if m.dispatcher != nil {
		m.wg.Add(1)
		go m.runDispatcherRecovery(background)
	}
	if m.recoveryLifecycle != nil {
		m.wg.Add(1)
		go m.runRecoveryLifecycle(background)
	}
	m.mu.Unlock()
	m.Dispatch(background)
	return nil
}

func (m *Module) Dispatch(ctx context.Context) {
	if m == nil || m.dispatcher == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.stopping || m.stopped || m.dispatching {
		m.mu.Unlock()
		return
	}
	if m.background != nil {
		ctx = m.background
	}
	m.dispatching = true
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			m.dispatching = false
			m.mu.Unlock()
			m.wg.Done()
		}()
		m.dispatcher.Run(ctx)
	}()
}

func (m *Module) runScheduler(ctx context.Context) {
	defer m.wg.Done()
	if m.reconcileSchedules != nil {
		if err := m.reconcileSchedules(ctx); err != nil {
			m.logger.WarnContext(ctx, "reconcile refresh pipeline schedules failed", "error", err)
		}
	}
	dispatch := func() {
		if err := m.scheduler.DispatchDue(ctx); err != nil {
			m.logger.WarnContext(ctx, "dispatch scheduled refresh pipelines failed", "error", err)
		}
	}
	dispatch()
	ticker := time.NewTicker(m.scheduleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatch()
		}
	}
}

func (m *Module) runDispatcherRecovery(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.leaseTimeout)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Dispatch(ctx)
		}
	}
}

func (m *Module) runRecoveryLifecycle(ctx context.Context) {
	defer m.wg.Done()
	run := func() {
		if err := m.recoveryLifecycle.RunOnce(ctx); err != nil && ctx.Err() == nil {
			m.logger.WarnContext(ctx, "run scheduled recovery qualification lifecycle failed", "error", RedactFailure(err))
		}
	}
	run()
	ticker := time.NewTicker(m.recoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (m *Module) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		m.mu.Lock()
		m.stopped = true
		m.stopping = false
		m.background = nil
		m.cancel = nil
		m.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
