package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/filesystem"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// projectCatalogLeaseProvider narrows the runtime-host provider to the
// catalog lease contract while preserving the exact active lease object. No
// graph or authorization snapshot is cached here.
type projectCatalogLeaseProvider struct {
	provider runtimehostmodule.Provider
}

type projectCatalogSubjectResolver struct {
	resolve func(context.Context, string) ([]access.SubjectRef, error)
}

func (r projectCatalogSubjectResolver) AuthorizationSubjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	if r.resolve == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	return r.resolve(ctx, principalID)
}

func (p projectCatalogLeaseProvider) Acquire(ctx context.Context) (projectcatalog.Lease, error) {
	if p.provider == nil {
		return nil, projectcatalog.ErrUnavailable
	}
	lease, err := p.provider.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	catalogLease, ok := lease.(projectcatalog.Lease)
	if !ok {
		lease.Release()
		return nil, fmt.Errorf("runtime lease does not expose catalog authorization snapshot")
	}
	return catalogLease, nil
}

// assemble constructs the complete process exactly once. CLI and other process
// entrypoints provide configuration but never construct capability adapters.
func assemble(ctx context.Context, cfg config.Config) (http.Handler, Lifecycle, cleanupFunc, error) {
	production := cfg.Production
	environment := servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(cfg.Environment))
	if strings.TrimSpace(cfg.Environment) == "" {
		if production {
			environment = servingstatemodule.Environment("prod")
		} else {
			environment = servingstatemodule.DefaultEnvironment
		}
	}
	return buildRuntime(ctx, cfg, production, environment)
}

func buildRuntime(ctx context.Context, cfg config.Config, production bool, environment servingstatemodule.Environment) (http.Handler, Lifecycle, cleanupFunc, error) {
	assets := applicationAssets(cfg, production)
	dashboardAssets, err := dashboardmodule.BuildAssets(ctx, cfg.MapAssetDir)
	if err != nil {
		return nil, nil, nil, err
	}
	cookieSecure, err := cfg.CookieSecure()
	if err != nil {
		return nil, nil, nil, err
	}
	var allowedHosts []string
	if production {
		allowedHosts, err = cfg.ProductionAllowedHosts()
	} else {
		allowedHosts, err = cfg.AllowedHostList()
	}
	if err != nil {
		return nil, nil, nil, err
	}
	duckLakeCatalogPath := cfg.DuckLakeCatalogPath()
	for _, dir := range []string{cfg.HomeDir, cfg.ArtifactDir(), cfg.DuckDBDirPath(), cfg.RuntimeDir(), cfg.DuckLakeDataDir(), filepath.Dir(duckLakeCatalogPath)} {
		if err := securefs.EnsurePrivateDir(dir); err != nil {
			return nil, nil, nil, err
		}
	}
	store, err := platform.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := &cleanupStack{}
	cleanup.Push("sqlite", func(context.Context) error { return store.Close() })
	fail := func(err error) (http.Handler, Lifecycle, cleanupFunc, error) {
		cleanupErr := cleanup.Close(context.WithoutCancel(ctx))
		return nil, nil, nil, errors.Join(err, cleanupErr)
	}
	if err := store.BindInstanceEnvironment(ctx, string(environment)); err != nil {
		return fail(err)
	}
	candidateSources, err := projectmodule.NewCandidateSourceSynchronizer(
		filepath.Join(cfg.ArtifactDir(), "candidate-sources"),
	)
	if err != nil {
		return fail(err)
	}
	instanceID, err := store.InstanceID(ctx)
	if err != nil {
		return fail(err)
	}
	servingStateRepo, err := servingstatemodule.Build(ctx, servingstatemodule.Config{Database: store.SQLDB()})
	if err != nil {
		return fail(err)
	}
	projectClaimRepository := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
	// Every authorization callback reads the claim afresh. The runtime host
	// uses this same reader during startup, while bootstrap decisions must not
	// rely on a stale memoized claim after a concurrent first operation.
	readClaim := readClaimedProject(projectClaimRepository, environment)
	claimedProjectID, claimFound, err := readClaim(ctx)
	if err != nil {
		return fail(err)
	}
	activeScopes, err := servingStateRepo.ListActiveScopes(ctx)
	if err != nil {
		return fail(err)
	}
	projectID, err := resolveClaimedProjectID(activeScopes, environment, claimedProjectID, claimFound)
	if err != nil {
		return fail(err)
	}
	publicURL := firstConfigured(cfg.PublicURL, configuredListenURL(cfg.ListenAddr()))
	workloadConfig := cfg.WorkloadConfig()
	credentialMode := analyticsmodule.CredentialModeNonSecret
	if !production {
		credentialMode = analyticsmodule.CredentialModeDevelopmentEnvironment
	}
	analyticsModule, err := analyticsmodule.Build(ctx, analyticsmodule.Config{
		Database: store.SQLDB(), CredentialMode: credentialMode,
		CredentialTargetID: instanceID, CredentialProjectID: projectID, CredentialEnvironment: string(environment),
		TargetCredentials: analyticsmodule.TargetCredentialConfig{
			InfisicalBaseURL:               cfg.InfisicalBaseURL,
			InfisicalUniversalClientID:     cfg.InfisicalUniversalClientID,
			InfisicalUniversalClientSecret: cfg.InfisicalUniversalClientSecret,
			InfisicalAllowedScopes:         cfg.InfisicalAllowedScopes,
		},
		RootDir:     cfg.DuckDBDirPath(),
		CatalogPath: duckLakeCatalogPath, DataPath: cfg.DuckLakeDataDir(),
		MaxConnections: workloadConfig.MaxRunning, MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes,
		TempMaxBytes: cfg.DuckDBNodeTempMaxBytes, MaxThreads: cfg.DuckDBNodeMaxThreads,
		TempDir:             cfg.DuckDBTempDirPath(),
		RuntimeCacheEntries: cfg.QueryCacheRuntimeMaxEntries, RuntimeCacheBytes: cfg.QueryCacheRuntimeMaxBytes,
		NodeCacheEntries: cfg.QueryCacheNodeMaxEntries, NodeCacheBytes: cfg.QueryCacheNodeMaxBytes,
	})
	if err != nil {
		return fail(err)
	}
	cleanup.Push("analytics", func(context.Context) error { return analyticsModule.Close() })
	analyticsProjectFactory := analyticsModule.ProjectRuntimeFactory()
	avatarBlobs, err := profileImageBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productLogoBlobs, err := productLogoBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productService, err := adminmodule.NewProductService(store.SQLDB(), productLogoBlobs)
	if err != nil {
		return fail(err)
	}
	var runtimeHostModule *runtimehostmodule.Module
	currentProjectID := func(ctx context.Context) (projectgraph.ResourceID, error) {
		if runtimeHostModule == nil {
			return "", fmt.Errorf("runtime host is unavailable")
		}
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		projectID := lease.Identity().ProjectID
		if err := projectID.Validate(); err != nil {
			return "", fmt.Errorf("active runtime project identity is invalid: %w", err)
		}
		return projectID, nil
	}
	accessModule, err := accessmodule.Build(ctx, accessmodule.Config{
		Database: store.SQLDB(), Auth: accessAuthConfig(cfg, production, cookieSecure),
		Assets:      assets,
		AvatarBlobs: avatarBlobs,
		PublicURL:   publicURL, InstanceID: instanceID, MCPIssuerURL: cfg.MCPOAuthIssuerURL,
		CurrentProjectID: currentProjectID,
	})
	if err != nil {
		return fail(err)
	}
	accessRepo, err := accessRepository(accessModule)
	if err != nil {
		return fail(err)
	}
	if !production {
		if err := accessModule.SeedLocalDeveloperPlatformAdmin(ctx); err != nil {
			return fail(err)
		}
	}
	workloadController, err := workloadmodule.Build(ctx, workloadmodule.Config{Policy: workloadConfig})
	if err != nil {
		return fail(err)
	}
	cleanup.Push("workload", func(context.Context) error {
		workloadController.Close()
		return nil
	})
	jobModule, err := jobsmodule.Build(ctx, jobsmodule.Config{
		Database: store.SQLDB(), Admission: workloadmodule.JobAdmitter(workloadController),
		LeaseTimeout: cfg.RefreshJobLeaseTimeout, Logger: slog.Default(),
	})
	if err != nil {
		return fail(err)
	}
	authorizationSnapshot := func(ctx context.Context) (accesssnapshot.AuthorizationSnapshot, error) {
		if runtimeHostModule == nil {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("runtime host is unavailable")
		}
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, err
		}
		defer lease.Release()
		authorizedLease, ok := lease.(interface {
			AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
		})
		if !ok {
			return accesssnapshot.AuthorizationSnapshot{}, fmt.Errorf("active runtime lease does not expose authorization snapshot")
		}
		snapshot := authorizedLease.AuthorizationSnapshot()
		if err := snapshot.ValidateBound(); err != nil {
			return accesssnapshot.AuthorizationSnapshot{}, err
		}
		return snapshot, nil
	}
	authorizeConnection := accessmodule.ConnectionAuthorizerFromSnapshot(authorizationSnapshot, accessModule.AuthorizationSubjects)
	managedDataModule, err := manageddatamodule.Build(ctx, manageddatamodule.Config{
		Database: store.SQLDB(), Product: managedDataProductConfig(cfg), ServingStates: servingStateRepo,
		Environment: string(environment),
		CurrentPrincipal: func(r *http.Request) (manageddatamodule.Principal, bool) {
			auth := accessModule.Auth()
			if auth == nil {
				return manageddatamodule.Principal{}, false
			}
			principal, ok := auth.Principal(r)
			return manageddatamodule.Principal{ID: principal.ID}, ok
		},
		AuthorizeConnection: authorizeConnection,
		Jobs:                jobModule, Workflow: jobModule,
		RecordAudit: managedDataCommandAuditRecorder(accessModule),
		Worker: manageddatamodule.MaintenanceWorkerConfig{
			Interval: cfg.ManagedDataGCInterval,
			Acquire: func(ctx context.Context) (manageddatamodule.MaintenanceLease, error) {
				return workloadController.Acquire(ctx, workloadmodule.MaintenanceRequest("managed_data.collect"))
			},
			Logger: slog.Default(),
		},
	})
	if err != nil {
		return fail(err)
	}
	releaseModule, err := releasemodule.Build(ctx, releasemodule.Config{
		Database:        store.SQLDB(),
		States:          servingStateRepo,
		ManagedDataPins: managedDataModule.BindingValidation(), ManagedDataHook: managedDataModule.BindingValidation(),
		ArtifactDirectory: cfg.ArtifactDir(), Environment: environment,
		API: releasemodule.APIConfig{
			CurrentPrincipal: func(r *http.Request) (releasemodule.Principal, bool) {
				auth := accessModule.Auth()
				if auth == nil {
					principal := accessmodule.LocalDeveloperPrincipal()
					return releasemodule.Principal{ID: principal.ID}, true
				}
				principal, ok := auth.Principal(r)
				return releasemodule.Principal{ID: principal.ID}, ok
			},
			AuthorizeConnection: authorizeConnection,
			Jobs:                jobModule, Workflow: jobModule,
		},
	})
	if err != nil {
		return fail(err)
	}
	if err := analyticsModule.ConfigureActiveRuntimeBindings(activeConnectionEvidenceSource{
		releases: releaseModule, targetID: instanceID, environment: string(environment),
	}); err != nil {
		return fail(err)
	}
	managedDataResolution := managedDataModule.RuntimeResolution()
	if managedDataResolution == nil {
		return fail(errors.New("managed-data runtime resolver is required"))
	}
	managedDataResolver := appruntimefactory.NewManagedDataResolver(managedDataResolution)
	if err := refreshmodule.Recover(ctx, store.SQLDB(), string(environment)); err != nil {
		return fail(err)
	}
	retention := servingstatemodule.NewRetention(servingstatemodule.RetentionConfig{
		States: servingStateRepo, Snapshots: analyticsModule.RetentionSnapshots(),
		Admission: workloadController, Environment: string(environment),
		CatalogPath: duckLakeCatalogPath, DataPath: cfg.DuckLakeDataDir(),
	})
	if err := retention.Run(ctx, false); err != nil {
		return fail(err)
	}
	runtimeHostModule, err = runtimehostmodule.Build(ctx, runtimehostmodule.Config{
		States:             servingStateRepo,
		ProjectID:          projectID,
		Environment:        environment,
		ReadClaimedProject: readClaim,
		ManagedData:        managedDataResolver,
		OnDrained: func(_ servingstatemodule.ID, _ int64) {
			go func() {
				if err := retention.Run(context.Background(), false); err != nil {
					slog.Default().Warn("storage retention cleanup failed after runtime drain", "error", err)
				}
			}()
		},
		Factory: appruntimefactory.NewFactory(appruntimefactory.FactoryConfig{
			DuckDBDir: cfg.DuckDBDirPath(), RuntimeDir: cfg.RuntimeDir(),
			DashboardRuntime: dashboardmodule.NewRuntimeFactory(dashboardmodule.RuntimeFactoryConfig{
				Projects: analyticsProjectFactory,
				MaxRows:  cfg.QueryResultMaxRows, MaxBytes: cfg.QueryResultMaxBytes,
			}),
		}),
	})
	if err != nil {
		return fail(err)
	}
	projectCatalog, err := projectcatalog.NewService(
		projectCatalogLeaseProvider{provider: runtimeHostModule.Provider()},
		projectCatalogSubjectResolver{resolve: accessModule.AuthorizationSubjects},
	)
	if err != nil {
		return fail(fmt.Errorf("build project catalog: %w", err))
	}
	releaseModule.SetProjectSearchCatalog(projectCatalog)
	accessModule.SetCurrentEffectiveCapabilities(func(ctx context.Context, principalID string) ([]access.Capability, error) {
		subjects, err := accessModule.AuthorizationSubjects(ctx, principalID)
		if err != nil {
			return nil, err
		}
		snapshot, err := authorizationSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		return snapshot.EffectiveCapabilities(subjects)
	})
	projectIDResolver := currentProjectID
	accessModule.SetCurrentProjectID(projectIDResolver)
	servingSnapshotResolver := func(ctx context.Context) (string, error) {
		lease, err := runtimeHostModule.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return "", fmt.Errorf("active runtime serving identity is invalid: %w", err)
		}
		return identity.GenerationID, nil
	}
	cleanup.Push("runtime-host", func(context.Context) error { return runtimeHostModule.Close() })
	authoringAcquireRuntime := func(ctx context.Context) (runtimehostmodule.Lease, error) {
		return runtimeHostModule.Acquire(ctx)
	}
	authoringApplication, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{
		Database: store.SQLDB(),
		AuthorizeResource: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			return authorizeProjectResources(ctx, accessModule, runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
		},
		AcquireRuntime:  authoringAcquireRuntime,
		ExportDashboard: projectmodule.ExportDashboard,
	})
	if err != nil {
		return fail(fmt.Errorf("build dashboard authoring module: %w", err))
	}
	generationRevalidator, err := authoringApplication.NewGenerationRevalidator(time.Now)
	if err != nil {
		return fail(fmt.Errorf("build dashboard generation revalidator: %w", err))
	}
	deploymentRuntime, err := deploymentmodule.NewRuntime(runtimeHostModule)
	if err != nil {
		return fail(err)
	}
	candidateBindings, err := analyticsModule.NewRuntimeBindingLeaser(
		analyticsmodule.RuntimeBindingLeaserConfig{
			Authorize: func(
				ctx context.Context,
				principalID string,
				binding analyticsmodule.ConnectionTargetBinding,
			) error {
				resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
				if err != nil {
					return err
				}
				allowed, err := authorizeProjectResources(
					ctx, accessModule, runtimeHostModule, principalID,
					binding.Scope.ProjectID, []access.ResourceRef{resource}, access.CapabilityResourceUse,
				)
				if err != nil {
					return err
				}
				if !allowed {
					return analyticsmodule.ErrConnectionBindingUnauthorized
				}
				return nil
			},
			Now: time.Now,
			Audit: connectionRotationAuditRecorder{
				record: accessAuditRecorder(accessModule),
			},
		},
	)
	if err != nil {
		return fail(err)
	}
	identity := buildinfo.Current()
	deploymentConfig := deploymentmodule.Config{
		Database: store.SQLDB(), States: servingStateRepo, Runtime: deploymentRuntime,
		ManagedData:        managedDataResolver,
		BootstrapPolicies:  projectClaimRepository,
		BindClaimedProject: bindClaimedProject(runtimeHostModule, environment),
		Protected: protectedPublishingTarget(
			production,
			cfg.EvaluationMode,
		),
		CurrentApprovalActor: func(r *http.Request) (deploymentmodule.ApprovalActor, bool) {
			evidence, ok := accessModule.CurrentCredentialEvidence(r)
			if !ok {
				return deploymentmodule.ApprovalActor{}, false
			}
			return deploymentmodule.ApprovalActor{
				PrincipalID:         evidence.PrincipalID,
				CredentialClass:     deploymentmodule.CredentialClass(evidence.Class),
				CredentialID:        evidence.ID,
				CredentialExpiresAt: evidence.ExpiresAt,
			}, true
		},
		AuthorizeApproval: func(
			ctx context.Context,
			actor deploymentmodule.ApprovalActor,
			projectID string,
			environment string,
		) error {
			requestedProject, err := projectgraph.NewResourceID(projectID)
			if err != nil {
				return err
			}
			if requestedProject.String() != projectID {
				return deploymentmodule.ErrApprovalForbidden
			}
			project, err := access.NewResourceRef(requestedProject, projectgraph.KindProject)
			if err != nil {
				return err
			}
			allowed, err := authorizeProjectResources(ctx, accessModule, runtimeHostModule, actor.PrincipalID, requestedProject, []access.ResourceRef{project}, access.CapabilityProjectAdmin)
			if err != nil {
				return err
			}
			if !allowed {
				return deploymentmodule.ErrApprovalForbidden
			}
			return nil
		},
		AuthorizeActivation: func(
			ctx context.Context,
			actor deploymentmodule.ApprovalActor,
			projectID string,
			environment string,
		) error {
			requestedProject, err := projectgraph.NewResourceID(projectID)
			if err != nil {
				return err
			}
			if requestedProject.String() != projectID {
				return deploymentmodule.ErrActivationForbidden
			}
			project, err := access.NewResourceRef(requestedProject, projectgraph.KindProject)
			if err != nil {
				return err
			}
			allowed, err := authorizeProjectResources(ctx, accessModule, runtimeHostModule, actor.PrincipalID, requestedProject, []access.ResourceRef{project}, access.CapabilityProjectAdmin)
			if err != nil {
				return err
			}
			if !allowed {
				return deploymentmodule.ErrActivationForbidden
			}
			return nil
		},
		AuthorizeBootstrap: func(ctx context.Context, policy deployment.BootstrapActivationPolicy) error {
			if err := policy.Validate(); err != nil {
				return err
			}
			claimedProject, found, err := readClaim(ctx)
			if err != nil {
				return fmt.Errorf("bootstrap claim authorization: %w", err)
			}
			if !found || claimedProject != policy.ProjectID || policy.Environment != environment {
				return deployment.ErrBootstrapPolicyConflict
			}
			admin, err := accessModule.IsPlatformAdmin(ctx, policy.ActorID)
			if err != nil {
				return fmt.Errorf("bootstrap platform role authorization: %w", err)
			}
			if !admin {
				return deployment.ErrBootstrapPolicyConflict
			}
			if err := accessModule.AuthorizeBootstrapCredential(ctx, policy.ActorID, policy.CredentialID, policy.CredentialExpiresAt, time.Now().UTC()); err != nil {
				return fmt.Errorf("bootstrap credential authorization: %w", err)
			}
			active, activeErr := hasActiveBootstrapServingState(ctx, runtimeHostModule, servingStateRepo, string(environment))
			if activeErr != nil {
				return fmt.Errorf("%w: %v", deployment.ErrBootstrapPolicyConflict, activeErr)
			}
			if active {
				return deployment.ErrBootstrapPolicyConflict
			}
			return nil
		},
		CandidateConnections: candidateConnectionLeaser{
			leaser: candidateBindings, module: analyticsModule,
		},
		CandidateRuntime:          runtimeHostModule,
		CandidateRuntimeLifecycle: runtimeHostModule,
		CandidateAdmission: deploymentmodule.CandidatePreparationAdmitterFunc(
			func(ctx context.Context) (deploymentmodule.CandidatePreparationLease, error) {
				return workloadController.Acquire(
					ctx,
					workloadmodule.ControlRequest("candidate.prepare"),
				)
			},
		),
		CandidateSources:         candidateSources,
		CandidateArtifacts:       releaseModule,
		CandidateSourceBlobAudit: candidateSourceBlobAuditRecorder(accessModule),
		RuntimeVersion:           identity.Version + ":" + identity.Revision,
		AfterActivated: func(ctx context.Context, activated deployment.Deployment) {
			generation, generationErr := activatedRevalidationGeneration(
				ctx, servingStateRepo, runtimeHostModule, activated.ServingIdentity, activated.PriorGenerationID,
			)
			if generationErr != nil {
				slog.Default().Warn("dashboard generation revalidation could not load activated generation", "project", activated.ServingIdentity.ProjectID, "generation", activated.ServingIdentity.GenerationID, "error", generationErr)
				return
			}
			results, revalidationErr := generationRevalidator.GenerationActivated(ctx, generation)
			for _, result := range results {
				if result.Err != nil {
					slog.Default().Warn("dashboard generation revalidation failed", "project", generation.Identity.ProjectID, "generation", generation.Identity.GenerationID, "dashboard", result.DashboardID, "error", result.Err)
				}
			}
			if revalidationErr != nil {
				slog.Default().Warn("dashboard generation revalidation failed", "project", generation.Identity.ProjectID, "generation", generation.Identity.GenerationID, "error", revalidationErr)
			}
		},
		ActivationHooks: deploymentmodule.ActivationHooks{},
	}
	runtimeMetrics := dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{
		Provider: runtimeHostModule.Provider(), ProjectID: projectID,
		PublishedCompilationReader: authoringApplication.PublishedCompilationReader(),
	})
	auth := accessModule.Auth()
	rateLimits := apihttpmiddleware.ProductionRateLimitConfig()
	rateLimits.Enabled = production && cfg.RateLimitingEnabled()
	rateLimits.UseRealIP = cfg.RateLimitingUsesRealIP()
	routes, runtime, platformServices, policy, err := buildApplicationSurfaces(ctx, runtimeMetrics,
		dataAssemblyInputs{
			Database: store.SQLDB(), PlatformHealth: store, AdminDatabase: store.SQLDB(),
			ServingStateRepo: servingStateRepo, StorageRetention: retention,
			AccessRepo: accessRepo,
		},
		capabilityAssemblyInputs{
			AnalyticsModule: analyticsModule, DashboardAssets: dashboardAssets,
			ReleaseModule: releaseModule, JobModule: jobModule,
			AccessModule: accessModule, ManagedDataModule: managedDataModule,
			ProjectCatalog: projectCatalog,
			ProjectGraph:   servingStateRepo,
			Authoring:      authoringApplication,
			Product:        productService, ProductStatus: productAdministrationStatus(cfg, instanceID, publicURL, string(environment), identity),
		},
		workflowAssemblyInputs{
			AgentSettings: store,
			AgentConfig:   agentmodule.ModelConfig{APIKey: cfg.AgentAPIKey, BaseURL: cfg.AgentBaseURL, Model: cfg.AgentModel},
			Auth:          auth, Reloader: runtimeHostModule, Workload: workloadController,
			ManagedDataValidation: managedDataModule.BindingValidation(),
			ManagedDataResolver:   managedDataResolver,
			DeploymentConfig:      deploymentConfig,
		},
		runtimeAssemblyInputs{
			RuntimeHost: runtimeHostModule,
			ProjectID:   projectID, ProjectIDResolver: projectIDResolver, ServingSnapshotResolver: servingSnapshotResolver,
			DuckLakeCatalogPath: duckLakeCatalogPath, DuckLakeDataPath: cfg.DuckLakeDataDir(),
			DefaultEnvironment: string(environment), SCIMBearerToken: cfg.SCIMBearerToken,
			MetricsBearerToken: cfg.MetricsBearerToken, AllowedHosts: allowedHosts, Assets: assets,
			InstanceID: instanceID, RequireActiveDeployment: cfg.EvaluationMode,
		},
		httpAssemblyInputs{
			PublicURL: publicURL,
			DesktopDiscovery: desktopdiscovery.Config{
				CanonicalOrigin:   publicURL,
				InstanceID:        instanceID,
				DisplayName:       "LeapView",
				ServerVersion:     assets.Version(),
				AllowLoopbackHTTP: !production,
			},
			RateLimits:      rateLimits,
			SecurityHeaders: apihttpmiddleware.SecurityHeaders(production && cfg.HSTSEnabled(cookieSecure)),
			RequestLogging:  production && cfg.RequestLoggingEnabled(), Logger: slog.Default(),
			JobLeaseTimeout: cfg.RefreshJobLeaseTimeout, ManagedDataTus: managedDataModule.TusHandler(),
		},
	)
	if err != nil {
		return fail(err)
	}
	runtime.runtimeHostModule = runtimeHostModule
	handler := Routes(routes, runtime, platformServices, policy)
	lifecycle := newRuntimeLifecycle(platformServices.workers, runtime.analyticsModule, runtime.workloads)
	return handler, lifecycle, cleanup.Close, nil
}

func protectedPublishingTarget(production, evaluation bool) bool {
	return production && !evaluation
}

func readClaimedProject(repository deployment.ProjectClaimRepository, environment servingstatemodule.Environment) func(context.Context) (projectgraph.ResourceID, bool, error) {
	return func(ctx context.Context) (projectgraph.ResourceID, bool, error) {
		if repository == nil {
			return "", false, errors.New("project claim repository is required")
		}
		claim, err := repository.GetProjectClaim(ctx)
		if errors.Is(err, deployment.ErrProjectClaimNotFound) {
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("read claimed project: %w", err)
		}
		if claim.Environment != environment {
			return "", false, fmt.Errorf("claimed project environment %q does not match configured environment %q", claim.Environment, environment)
		}
		return claim.ProjectID, true, nil
	}
}

type claimedProjectBinder interface {
	BindClaimedProject(projectgraph.ResourceID, servingstatemodule.Environment) error
}

func bindClaimedProject(runtimeHost claimedProjectBinder, environment servingstatemodule.Environment) func(context.Context, projectgraph.ResourceID, servingstatemodule.Environment) error {
	return func(_ context.Context, projectID projectgraph.ResourceID, claimedEnvironment servingstatemodule.Environment) error {
		if claimedEnvironment != environment {
			return fmt.Errorf("claimed project environment %q does not match configured environment %q", claimedEnvironment, environment)
		}
		if runtimeHost == nil {
			return errors.New("runtime host is unavailable")
		}
		return runtimeHost.BindClaimedProject(projectID, claimedEnvironment)
	}
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func singletonProjectID(scopes []servingstatemodule.ActiveScope, environment servingstatemodule.Environment) (projectgraph.ResourceID, error) {
	var projectID projectgraph.ResourceID
	for _, scope := range scopes {
		if scope.Environment != environment {
			continue
		}
		if err := scope.ProjectID.Validate(); err != nil {
			return "", fmt.Errorf("active serving project identity is invalid: %w", err)
		}
		if projectID == "" {
			projectID = scope.ProjectID
			continue
		}
		if scope.ProjectID != projectID {
			return "", fmt.Errorf("active serving scopes span multiple projects: %q and %q", projectID, scope.ProjectID)
		}
	}
	return projectID, nil
}

func resolveClaimedProjectID(scopes []servingstatemodule.ActiveScope, environment servingstatemodule.Environment, claimedProjectID projectgraph.ResourceID, claimFound bool) (projectgraph.ResourceID, error) {
	projectID, err := singletonProjectID(scopes, environment)
	if err != nil {
		return "", err
	}
	if !claimFound {
		if projectID != "" {
			return "", errors.New("active serving scopes require a durable project claim")
		}
		return "", nil
	}
	if projectID != "" && projectID != claimedProjectID {
		return "", fmt.Errorf("active serving project %q does not match durable project claim %q", projectID, claimedProjectID)
	}
	return claimedProjectID, nil
}

func configuredListenURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func accessAuthConfig(cfg config.Config, production, cookieSecure bool) accessmodule.AuthConfig {
	if !production {
		return accessmodule.AuthConfig{DevBypass: true, DevAPIToken: cfg.DevAPIToken, CSRFKey: cfg.CSRFKey}
	}
	providers := []accessmodule.OIDCProviderConfig{}
	if cfg.OIDCConfigured() {
		providers = append(providers, accessmodule.OIDCProviderConfig{
			ID: cfg.OIDCProviderID, IssuerURL: cfg.OIDCIssuerURL, ClientID: cfg.OIDCClientID,
			ClientSecret: cfg.OIDCSecret, RedirectURL: cfg.OIDCCallbackURL, Scopes: cfg.OIDCScopesList(),
		})
	}
	return accessmodule.AuthConfig{
		DevBypass: cfg.DevAuthBypass, DevAPIToken: cfg.DevAPIToken, APITokenOnly: cfg.APITokenOnlyAuth,
		LocalAuth: cfg.LocalAuth, AzureClientID: cfg.AzureClientID, AzureSecret: cfg.AzureSecret,
		AzureCallback: cfg.AzureCallbackURL, AzureTenant: cfg.AzureTenant, CSRFKey: cfg.CSRFKey,
		CookieSecure: cookieSecure, BootstrapTenant: cfg.AzureTenant, OIDCProviders: providers,
	}
}
