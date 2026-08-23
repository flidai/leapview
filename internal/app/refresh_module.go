package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	"github.com/flidai/leapview/internal/servingstate"
)

func configureRefreshModule(routes *capabilityRoutes, runtime *runtimeServices, platform *platformServices, policy *httpPolicy, ctx context.Context, database *sql.DB, persistence persistenceInputs, workflow workflowInputs, storage storageInputs) error {
	if routes == nil || routes.refreshModule != nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	service, err := projectRefreshService(persistence, workflow, func() *dashboardmodule.Module { return routes.dashboardModule })
	if err != nil && database != nil {
		return fmt.Errorf("configure refresh service: %w", err)
	}
	if workflow.refreshMaterializer != nil {
		service.Materializer = workflow.refreshMaterializer
	}
	service.CanonicalExecutor = workflow.canonicalRefreshExecutor
	service.ResolveActive = func(ctx context.Context, identity projectgraph.ServingIdentity) (refreshrun.ServingState, error) {
		if runtime.runtimeHostModule == nil {
			return refreshrun.ServingState{}, fmt.Errorf("active project runtime is unavailable")
		}
		lease, err := runtime.runtimeHostModule.Acquire(ctx)
		if err != nil {
			return refreshrun.ServingState{}, err
		}
		defer lease.Release()
		if lease.Identity() != identity {
			return refreshrun.ServingState{}, fmt.Errorf("refresh base serving identity changed")
		}
		state, err := service.ServingStates.ByID(ctx, servingstate.ID(identity.GenerationID))
		if err != nil {
			return refreshrun.ServingState{}, err
		}
		artifact, err := service.ServingStates.ArtifactByServingState(ctx, state.ID)
		if err != nil {
			return refreshrun.ServingState{}, err
		}
		return refreshrun.ServingState{State: state, Artifact: artifact}, nil
	}
	resolveRefreshIdentity := func(ctx context.Context) (projectgraph.ServingIdentity, error) {
		if runtime.runtimeHostModule == nil {
			return projectgraph.ServingIdentity{}, fmt.Errorf("active project runtime is unavailable")
		}
		lease, err := runtime.runtimeHostModule.Acquire(ctx)
		if err != nil {
			return projectgraph.ServingIdentity{}, err
		}
		defer lease.Release()
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return projectgraph.ServingIdentity{}, fmt.Errorf("active runtime serving identity is invalid: %w", err)
		}
		return identity, nil
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
			ServingIdentity: func(r *http.Request) (projectgraph.ServingIdentity, error) {
				if runtime.runtimeHostModule == nil {
					return projectgraph.ServingIdentity{}, fmt.Errorf("active project runtime is unavailable")
				}
				lease, err := runtime.runtimeHostModule.Acquire(r.Context())
				if err != nil {
					return projectgraph.ServingIdentity{}, err
				}
				defer lease.Release()
				return lease.Identity(), nil
			},
		},
		Authorization: refreshmodule.AuthorizationConfig{
			CurrentPrincipal: func(r *http.Request) (refreshmodule.AuthorizationPrincipal, bool) {
				principal, ok := routes.accessModule.CurrentPrincipal(r)
				return refreshmodule.AuthorizationPrincipal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
			},
			CurrentCredential: func(r *http.Request) (access.APICredential, bool) {
				return accessmodule.APICredentialFromContext(r.Context())
			},
			AuthorizeObject: func(ctx context.Context, principalID string, capability access.Capability, resource access.ResourceRef) (bool, error) {
				projectID, err := runtime.resolveProjectID(ctx)
				if err != nil {
					return false, err
				}
				return authorizeProjectResources(ctx, routes.accessModule, runtime.runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
			},
		},
		Admission: workloadController(&runtime.workloads), LeaseTimeout: storage.jobLeaseTimeout,
		Clock: workflow.refreshPipelineClock, ResolveIdentity: resolveRefreshIdentity,
		EnableDispatcher: workflow.enableRefreshDispatcher,
		EnableScheduler:  false,
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
	if database != nil {
		config.AuditIntentRecorder = accessmodule.NewAuditStore(database)
	}
	module, err := refreshmodule.Build(ctx, config)
	if err != nil {
		return fmt.Errorf("build refresh module: %w", err)
	}
	routes.refreshModule = module
	return nil
}
