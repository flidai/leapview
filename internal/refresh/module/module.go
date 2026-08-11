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
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
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
	Analytics           analyticsmaterialization.WorkspaceExecutor
	Artifacts           refreshrun.ArtifactLoader
	ManagedData         runtimehost.ManagedDataResolver
	Admission           workload.Admitter
	LeaseTimeout        time.Duration
	Environment         string
	WorkloadStats       func() workload.Stats
	RunFinished         func(context.Context, refreshrun.RunRecord)
	Events              EventStore
	Workflow            jobs.WorkflowRecorder
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
	WorkspaceID      func(string) string
	Environment      func(*http.Request) string
}

type HTTPPrincipal struct {
	ID string
}

type AuthorizationPrincipal struct {
	ID        string
	DevBypass bool
}

type AuthorizationConfig struct {
	CurrentPrincipal     func(*http.Request) (AuthorizationPrincipal, bool)
	CurrentCredential    func(*http.Request) (access.APICredential, bool)
	ResolvePipelineModel func(context.Context, string, string) (string, bool, error)
	AuthorizeObject      func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error)
}

type Module struct {
	handler            materializehttp.Handler
	runs               *refreshsqlite.SQLRunRepository
	schedules          *refreshsqlite.Repository
	service            refreshrun.Service
	environment        string
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
			WorkspaceID:      config.HTTP.WorkspaceID, Environment: config.HTTP.Environment,
		},
		dispatcher: config.Dispatcher, scheduler: config.Scheduler,
		environment: config.Environment, refreshClock: config.Clock,
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
	m.handler.AuthorizePipelineView = func(r *http.Request, workspaceID, pipelineID string) (bool, error) {
		return authorizePipeline(r, workspaceID, pipelineID, access.PrivilegeViewItem, config.Authorization)
	}
	m.handler.AuthorizePipelineRun = func(r *http.Request, workspaceID, pipelineID string) (bool, error) {
		return authorizePipeline(r, workspaceID, pipelineID, access.PrivilegeRefreshData, config.Authorization)
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
		m.service.Materializer = refreshanalytics.WorkspaceRefreshMaterializer{
			Executor: config.Analytics, ManagedData: config.ManagedData,
		}
	}
	m.service.Runs = m.runs
	m.service.DataVersions = m.schedules
	m.service.Publication = refreshsqlite.NewPublicationUnitOfWork(config.Database, config.ApplyAccessSnapshot)
	if m.dispatcher == nil && config.EnableDispatcher {
		m.dispatcher = refreshrun.Dispatcher{
			Runs: m.runs, Service: m.service, Admitter: config.Admission,
			LeaseTimeout: config.LeaseTimeout, Logger: logger, Environment: config.Environment,
			WorkloadStats: config.WorkloadStats, RunFinished: m.runFinished(config.RunFinished),
		}
	}
	if m.scheduler == nil && config.EnableScheduler {
		m.scheduler = refreshschedule.Scheduler{
			Repository: m.schedules, Clock: config.Clock, Environment: config.Environment,
			Trigger: func(ctx context.Context, occurrence refreshschedule.Occurrence) (string, error) {
				result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
					WorkspaceID: occurrence.WorkspaceID, Environment: servingstate.Environment(occurrence.Environment),
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
	m.handler.QueuePipeline = func(ctx context.Context, workspaceID, environment, pipelineID, principalID, retryOf string) (refreshrun.RunRecord, error) {
		trigger := refreshrun.TriggerManual
		if retryOf != "" {
			trigger = refreshrun.TriggerRetry
		}
		result, err := m.service.QueuePipelineRefresh(ctx, refreshrun.QueuePipelineInput{
			WorkspaceID: workspaceID, Environment: servingstate.Environment(environment), PrincipalID: principalID,
			PipelineID: pipelineID, TriggerType: trigger, RetryOf: retryOf,
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

func (m *Module) GetRun(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	if m == nil || m.runs == nil {
		return refreshrun.RunRecord{}, errors.New("refresh persistence is not configured")
	}
	return m.runs.GetRun(ctx, workspaceID, runID)
}

func (m *Module) ListRuns(ctx context.Context, workspaceID string, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if m == nil || m.runs == nil {
		return nil, errors.New("refresh persistence is not configured")
	}
	return m.runs.ListRuns(ctx, workspaceID, page)
}

func (m *Module) ListTargetRuns(ctx context.Context, workspaceID, targetType, targetID string, page refreshrun.RunPage) ([]refreshrun.RunRecord, error) {
	if m == nil || m.runs == nil {
		return nil, errors.New("refresh persistence is not configured")
	}
	return m.runs.ListTargetRuns(ctx, workspaceID, targetType, targetID, page)
}

func (m *Module) LatestSuccessfulTargetRun(ctx context.Context, workspaceID, environment, targetType, targetID string) (refreshrun.RunRecord, bool, error) {
	if m == nil || m.runs == nil {
		return refreshrun.RunRecord{}, false, errors.New("refresh persistence is not configured")
	}
	return m.runs.LatestSuccessfulTargetRun(ctx, workspaceID, environment, targetType, targetID)
}

func (m *Module) CancelRun(ctx context.Context, workspaceID, runID string) (refreshrun.RunRecord, error) {
	if m == nil || m.runs == nil {
		return refreshrun.RunRecord{}, errors.New("refresh persistence is not configured")
	}
	return m.runs.CancelRun(ctx, workspaceID, runID)
}

func (m *Module) NextRun(ctx context.Context, workspaceID, environment, pipelineID string) (time.Time, bool, error) {
	if m == nil || m.schedules == nil {
		return time.Time{}, false, errors.New("refresh persistence is not configured")
	}
	return m.schedules.NextRun(ctx, workspaceID, environment, pipelineID)
}

func (m *Module) DataVersion(ctx context.Context, workspaceID, environment, modelID string) (refreshschedule.DataVersion, bool, error) {
	if m == nil || m.schedules == nil {
		return refreshschedule.DataVersion{}, false, errors.New("refresh persistence is not configured")
	}
	return m.schedules.DataVersion(ctx, workspaceID, environment, modelID)
}

type activeServingStates interface {
	ListActiveScopes(context.Context) ([]servingstate.ActiveScope, error)
	ActiveArtifact(context.Context, servingstate.WorkspaceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error)
}

type semanticModelVersionPublisher interface {
	PublishSemanticModelVersion(context.Context, string, string, string)
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
	clock := m.clock()
	var reconcileErrors []error
	for _, scope := range activeScopes(scopes, servingstate.Environment(m.environment)) {
		workspaceID := string(scope.WorkspaceID)
		state, artifact, err := states.ActiveArtifact(ctx, scope.WorkspaceID, scope.Environment)
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
			reconcileErrors = append(reconcileErrors, fmt.Errorf("workspace %q has no compiled definition", workspaceID))
			continue
		}
		pipelines := make([]refreshschedule.Definition, 0, len(loaded.Definition.Pipelines))
		for _, pipeline := range loaded.Definition.Pipelines {
			pipelines = append(pipelines, pipeline)
		}
		sort.Slice(pipelines, func(i, j int) bool { return pipelines[i].ID < pipelines[j].ID })
		if err := m.schedules.Reconcile(ctx, refreshschedule.ReconcileInput{
			WorkspaceID: workspaceID, Environment: string(scope.Environment), ArtifactDigest: artifact.Digest,
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
			current, found, err := m.schedules.DataVersion(ctx, workspaceID, string(scope.Environment), modelID)
			if err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
			if found && current.ServingStateID == string(state.ID) {
				continue
			}
			if err := m.schedules.SaveDataVersion(ctx, refreshschedule.DataVersion{
				WorkspaceID: workspaceID, Environment: string(scope.Environment), SemanticModel: modelID,
				SnapshotID: state.DuckLakeSnapshotID, ServingStateID: string(state.ID), RefreshedAt: refreshedAt,
				Source: refreshschedule.DataVersionSourcePublish,
			}); err != nil {
				reconcileErrors = append(reconcileErrors, err)
				continue
			}
			if publisher, ok := m.service.Publisher.(semanticModelVersionPublisher); ok {
				publisher.PublishSemanticModelVersion(ctx, workspaceID, string(scope.Environment), modelID)
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

func activeScopes(scopes []servingstate.ActiveScope, environment servingstate.Environment) []servingstate.ActiveScope {
	environment = servingstate.NormalizeEnvironment(environment)
	out := make([]servingstate.ActiveScope, 0, len(scopes))
	for _, scope := range scopes {
		if servingstate.NormalizeEnvironment(scope.Environment) == environment {
			out = append(out, scope)
		}
	}
	return out
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
