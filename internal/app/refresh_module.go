package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
)

func configureRefreshModule(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, ctx context.Context, database *sql.DB, persistence persistenceInputs, workflow workflowInputs, storage storageInputs) error {
	if routes == nil || routes.refreshModule != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	refreshDeps := workspaceRefreshDependencies{
		access:                routes.accessModule,
		dashboards:            func() *dashboardmodule.Module { return routes.dashboardModule },
		refresh:               func() *refreshmodule.Module { return routes.refreshModule },
		workspaces:            func() *workspacemodule.Module { return routes.workspaceModule },
		broker:                runtime.broker,
		persistenceConfigured: runtime.persistenceConfigured, defaultEnvironment: policy.defaultEnvironment,
	}
	service, err := workspaceRefreshService(&refreshDeps, persistence, workflow)
	if err != nil && database != nil {
		return fmt.Errorf("configure refresh service: %w", err)
	}
	config := refreshmodule.Config{
		Database: database, Service: service,
		Analytics: runtime.analyticsModule.ProjectMaterializer(), ManagedData: workflow.managedDataResolver,
		Artifacts: appruntimefactory.NewRefreshArtifactLoader(),
		HTTP: refreshmodule.HTTPConfig{
			RunnerConfigured: func() bool { return runtime.metrics != nil },
			CurrentPrincipal: func(r *http.Request) (refreshmodule.HTTPPrincipal, bool) {
				principal, ok := routes.accessModule.CurrentPrincipal(r)
				return refreshmodule.HTTPPrincipal{ID: principal.ID}, ok
			},
			WorkspaceID: func(value string) string {
				return value
			},
			Environment: func(*http.Request) string {
				return string(defaultServingEnvironment(policy.defaultEnvironment))
			},
		},
		Authorization: refreshmodule.AuthorizationConfig{
			CurrentPrincipal: func(r *http.Request) (refreshmodule.AuthorizationPrincipal, bool) {
				principal, ok := routes.accessModule.CurrentPrincipal(r)
				return refreshmodule.AuthorizationPrincipal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
			},
			CurrentCredential: func(r *http.Request) (accessmodule.APICredential, bool) {
				return accessmodule.APICredentialFromContext(r.Context())
			},
			ResolvePipelineModel: refreshmodule.PipelineModelResolver(
				persistence.servingStateRepo,
				appruntimefactory.NewRefreshArtifactLoader(),
				defaultServingEnvironment(policy.defaultEnvironment),
			),
			AuthorizeObject: routes.accessModule.AuthorizeObject,
		},
		ApplyAccessSnapshot: accessmodule.ApplySnapshot,
		Admission:           workloadController(&runtime.workloads), LeaseTimeout: storage.jobLeaseTimeout,
		Environment: string(defaultServingEnvironment(policy.defaultEnvironment)), Clock: workflow.refreshPipelineClock,
		EnableDispatcher: database != nil && runtime.metrics != nil,
		EnableScheduler:  database != nil && persistence.servingStateRepo != nil,
		Logger:           platform.logger, Events: platform.asyncJobs, Workflow: platform.jobModule,
		WorkloadStats: func() refreshmodule.WorkloadStats {
			return workloadController(&runtime.workloads).Stats()
		},
		RunFinished: func(ctx context.Context, run refreshmodule.RunRecord) {
			if run.Status == refreshmodule.RunStatusSucceeded && runtime.storageRetention != nil {
				_ = runtime.storageRetention.Run(ctx, false)
			}
		},
	}
	module, err := refreshmodule.Build(ctx, config)
	if err != nil {
		return fmt.Errorf("build refresh module: %w", err)
	}
	routes.refreshModule = module
	return nil
}
