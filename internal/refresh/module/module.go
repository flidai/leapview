// Package module owns refresh transport and worker lifecycle composition.
package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	analyticsmaterialization "github.com/flidai/leapview/internal/analytics/materialization"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshanalytics "github.com/flidai/leapview/internal/refresh/analyticsruntime"
	materializehttp "github.com/flidai/leapview/internal/refresh/http"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	refreshsqlite "github.com/flidai/leapview/internal/refresh/sqlite"
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

type Config struct {
	Database            *sql.DB
	ApplyAccessSnapshot func(context.Context, transaction.Transaction, string) error
	HTTP                HTTPConfig
	Authorization       AuthorizationConfig
	Service             refreshrun.Service
	Analytics           analyticsmaterialization.Executor
	Artifacts           refreshrun.ArtifactLoader
	ManagedData         runtimehost.ManagedDataResolver
	Admission           workload.Admitter
	LeaseTimeout        time.Duration
	ResolveIdentity     func(context.Context) (projectgraph.ServingIdentity, error)
	WorkloadStats       func() workload.Stats
	RunFinished         func(context.Context, refreshrun.RunRecord)
	Events              EventStore
	Workflow            jobplatform.WorkflowRecorder
	Clock               refreshschedule.Clock
	EnableDispatcher    bool
	EnableScheduler     bool
	Dispatcher          Dispatcher
	Scheduler           Scheduler
	ReconcileSchedules  func(context.Context) error
	ScheduleInterval    time.Duration
	Logger              *slog.Logger
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
	handler            materializehttp.Handler
	runs               *refreshsqlite.SQLRunRepository
	schedules          *refreshsqlite.Repository
	service            refreshrun.Service
	refreshClock       refreshschedule.Clock
	dispatcher         Dispatcher
	scheduler          Scheduler
	reconcileSchedules func(context.Context) error
	scheduleInterval   time.Duration
	leaseTimeout       time.Duration
	logger             *slog.Logger
	events             EventStore
	refreshExecution   apigencommand.AsyncExecutionContract

	mu          sync.Mutex
	background  context.Context
	cancel      context.CancelFunc
	started     bool
	stopping    bool
	stopped     bool
	dispatching bool
	wg          sync.WaitGroup
}

func Build(ctx context.Context, config Config) (*Module, error) {
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
		events:           config.Events,
		refreshExecution: refreshExecution,
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
	if config.Database == nil {
		return m, nil
	}
	if config.Workflow == nil {
		return nil, errors.New("refresh workflow recorder is required")
	}
	m.runs = refreshsqlite.NewSQLRunRepositoryWithWorkflow(config.Database, config.Workflow, refreshsqlite.RunWorkflowConfig{
		ResourceKind: refreshExecution.ResourceKind,
		InitialEvent: refreshExecution.InitialEvent,
		InitialState: refreshExecution.InitialState,
	})
	m.schedules = refreshsqlite.NewRepository(config.Database)
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
	m.service.Publication = refreshsqlite.NewPublicationUnitOfWork(config.Database, config.ApplyAccessSnapshot)
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
			Trigger: func(ctx context.Context, occurrence refreshschedule.Occurrence) (string, error) {
				result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
					Identity: occurrence.Identity, PrincipalID: "scheduler", EstimatedMemoryBytes: 1,
					PipelineID: occurrence.PipelineID, TriggerType: refreshrun.TriggerSchedule,
					ArtifactDigest: occurrence.ArtifactDigest, Occurrence: &occurrence,
				})
				if err == nil {
					m.Dispatch(ctx)
				}
				return result.Run.ID, err
			},
		}
	}
	if m.reconcileSchedules == nil && m.schedules != nil {
		m.reconcileSchedules = m.Reconcile
	}
	m.handler.Repository = func() (refreshrun.RunRepository, error) { return m.runs, nil }
	m.handler.DispatchQueued = func() { m.Dispatch(context.Background()) }
	m.handler.QueuePipeline = func(ctx context.Context, identity projectgraph.ServingIdentity, pipelineID, principalID, retryOf string) (refreshrun.RunRecord, error) {
		trigger := refreshrun.TriggerManual
		if retryOf != "" {
			trigger = refreshrun.TriggerRetry
		}
		pipelineIDValue, parseErr := projectgraph.NewResourceID(pipelineID)
		if parseErr != nil {
			return refreshrun.RunRecord{}, parseErr
		}
		result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
			Identity: identity, PrincipalID: principalID, EstimatedMemoryBytes: 1,
			PipelineID: pipelineIDValue, TriggerType: trigger, RetryOf: retryOf,
		})
		return result.Run, err
	}
	return m, nil
}

func Recover(ctx context.Context, database *sql.DB, environment string) error {
	if database == nil || environment == "" {
		return nil
	}
	return refreshsqlite.NewSQLRunRepository(database).FailRunsForTerminalServingStates(ctx, environment, "refresh did not complete")
}

func (m *Module) QueuePipelineRefresh(ctx context.Context, input refreshrun.QueuePipelineInput) (refreshrun.QueueAssetResult, error) {
	if m == nil || m.runs == nil {
		return refreshrun.QueueAssetResult{}, errors.New("refresh persistence is not configured")
	}
	return m.service.QueuePipelineRefresh(ctx, input)
}

// DataVersion returns the latest persisted semantic-model version for the
// active serving generation in the requested project/environment scope.
func (m *Module) DataVersion(ctx context.Context, projectID, environment, modelID string) (AssetDataVersion, bool, error) {
	if m == nil || m.schedules == nil || m.service.ServingStates == nil {
		return AssetDataVersion{}, false, nil
	}
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	state, _, err := m.service.ServingStates.ActiveArtifact(ctx, project, servingstate.Environment(environment))
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	identity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	model, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return AssetDataVersion{}, false, err
	}
	version, found, err := m.schedules.DataVersion(ctx, identity, model)
	if err != nil || !found {
		return AssetDataVersion{}, found, err
	}
	return AssetDataVersion{SnapshotID: version.SnapshotID, ServingStateID: version.Identity.GenerationID, RefreshedAt: version.RefreshedAt, Source: version.Source}, true, nil
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
