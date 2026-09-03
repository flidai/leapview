package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsgates "github.com/flidai/leapview/internal/analytics/gates"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	physicalpoolsqlite "github.com/flidai/leapview/internal/analytics/physicalpool/sqlite"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/app/config"
	"github.com/flidai/leapview/internal/app/desktopdiscovery"
	"github.com/flidai/leapview/internal/app/gcadapter"
	localruntimefactory "github.com/flidai/leapview/internal/app/localruntimefactory"
	"github.com/flidai/leapview/internal/app/poolcompatibility"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapiadapter "github.com/flidai/leapview/internal/deployment/apiadapter"
	"github.com/flidai/leapview/internal/deployment/gcstore"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	"github.com/flidai/leapview/internal/extension"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	"github.com/flidai/leapview/internal/platform/filesystem"
	cursorsigningsqlite "github.com/flidai/leapview/internal/platform/http/cursorsigning/sqlite"
	idempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	"github.com/flidai/leapview/internal/release"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/flidai/leapview/internal/runtimehost"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// Local SQLite application composition. This file intentionally owns the complete
// development/evaluation process graph while shared PostgreSQL composition remains
// in composition.go.

// assembleLocalSQLite constructs the explicitly local development/evaluation
// process graph exactly once. CLI and other process entrypoints provide
// configuration but never construct capability adapters. Non-evaluation
// production must enter buildPostgresProductionTarget instead.
func assembleLocalSQLite(ctx context.Context, cfg config.Config) (http.Handler, Lifecycle, cleanupFunc, error) {
	if err := guardSQLiteAuthorityComposition(cfg.Production, cfg.EvaluationMode); err != nil {
		return nil, nil, nil, err
	}
	production := cfg.Production
	environment := servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(cfg.Environment))
	if strings.TrimSpace(cfg.Environment) == "" {
		if production {
			environment = servingstatemodule.Environment("prod")
		} else {
			environment = servingstatemodule.DefaultEnvironment
		}
	}
	return buildLocalSQLiteRuntime(ctx, cfg, production, environment)
}

func buildLocalSQLiteRuntime(ctx context.Context, cfg config.Config, production bool, environment servingstatemodule.Environment) (http.Handler, Lifecycle, cleanupFunc, error) {
	// Keep the guard on the lower-level builder as well: this function is the
	// local graph's concrete assembly boundary and must remain safe if a future
	// caller bypasses assembleLocalSQLite.
	if err := guardSQLiteAuthorityComposition(production || cfg.Production, cfg.EvaluationMode); err != nil {
		return nil, nil, nil, err
	}
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
	// Local/evaluation composition owns its SQLite protocol authorities. The
	// protocol builder receives these typed ports rather than inferring them
	// from the platform database at a later routing seam.
	apiIdempotency := idempotencysqlite.NewStore(store.SQLDB())
	apiCursorSigning := cursorsigningsqlite.NewInitializer(store.SQLDB())
	fail := func(err error) (http.Handler, Lifecycle, cleanupFunc, error) {
		cleanupErr := cleanup.Close(context.WithoutCancel(ctx))
		return nil, nil, nil, errors.Join(err, cleanupErr)
	}
	auditRuntime, err := newAuditRuntime(store.SQLDB())
	if err != nil {
		return fail(fmt.Errorf("build access audit runtime: %w", err))
	}
	if err := store.BindInstanceEnvironment(ctx, string(environment)); err != nil {
		return fail(err)
	}
	extensionSupply, err := loadExtensionSupply(ctx, cfg)
	if err != nil {
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
	servingStatePersistence, err := servingstatemodule.NewSQLitePersistence(store.SQLDB())
	if err != nil {
		return fail(err)
	}
	servingStateRepo, err := servingstatemodule.Build(ctx, servingstatemodule.Config{Persistence: &servingStatePersistence})
	if err != nil {
		return fail(err)
	}
	projectClaimRepository, err := deploymentmodule.NewSQLiteBootstrapPersistence(store.SQLDB())
	if err != nil {
		return fail(err)
	}
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
	analyticsBindings, err := analyticsmodule.NewSQLiteConnectionBindings(store.SQLDB(), auditRuntime.recorder)
	if err != nil {
		return fail(err)
	}
	analyticsBundle, err := buildAnalyticsCapability(ctx, analyticsCapabilityConfig{
		ConnectionBindings: analyticsBindings, CredentialMode: credentialMode,
		CredentialTarget: instanceID, CredentialProject: projectID, Environment: string(environment),
		TargetCredentials: analyticsmodule.TargetCredentialConfig{
			InfisicalBaseURL: cfg.InfisicalBaseURL, InfisicalUniversalClientID: cfg.InfisicalUniversalClientID,
			InfisicalUniversalClientSecret: cfg.InfisicalUniversalClientSecret, InfisicalAllowedScopes: cfg.InfisicalAllowedScopes,
		},
		RootDir: cfg.DuckDBDirPath(), ExtensionSupply: extensionSupply,
		CatalogPath: duckLakeCatalogPath, DataPath: cfg.DuckLakeDataDir(),
		MaxConnections: workloadConfig.MaxRunning, MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes,
		TempMaxBytes: cfg.DuckDBNodeTempMaxBytes, MaxThreads: cfg.DuckDBNodeMaxThreads, TempDir: cfg.DuckDBTempDirPath(),
		DisableProcessEnv: production,
		RuntimeCacheItems: cfg.QueryCacheRuntimeMaxEntries, RuntimeCacheBytes: cfg.QueryCacheRuntimeMaxBytes,
		NodeCacheItems: cfg.QueryCacheNodeMaxEntries, NodeCacheBytes: cfg.QueryCacheNodeMaxBytes,
	})
	if err != nil {
		return fail(err)
	}
	analyticsModule := analyticsBundle.Module
	cleanup.Push("analytics", func(context.Context) error { return analyticsModule.Close() })
	avatarBlobs, err := profileImageBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productLogoBlobs, err := productLogoBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	productService, err := adminmodule.NewLegacySQLiteProductService(store.SQLDB(), productLogoBlobs)
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
	accessPersistence, err := accessmodule.NewSQLitePersistence(ctx, accessmodule.SQLitePersistenceConfig{Database: store.SQLDB()})
	if err != nil {
		return fail(err)
	}
	accessBundle, err := buildAccessCapability(ctx, accessCapabilityConfig{
		Persistence: &accessPersistence, Auth: accessAuthConfig(cfg, production, cookieSecure), Assets: assets, AvatarBlobs: avatarBlobs,
		PublicURL: publicURL, InstanceID: instanceID, MCPIssuerURL: cfg.MCPOAuthIssuerURL, CurrentProject: currentProjectID,
	})
	if err != nil {
		return fail(err)
	}
	accessModule := accessBundle.Module
	accessRepo := accessBundle.Repository
	authorizationInstaller := accessBundle.AuthorizationInstaller
	if !production {
		if err := accessModule.SeedLocalDeveloperPlatformAdmin(ctx); err != nil {
			return fail(err)
		}
	}
	jobsPersistence, err := jobsmodule.NewSQLitePersistence(jobsmodule.SQLitePersistenceConfig{Database: store.SQLDB()})
	if err != nil {
		return fail(err)
	}
	workloadBundle, err := buildWorkloadCapability(ctx, workloadCapabilityConfig{Workload: workloadmodule.Config{Policy: workloadConfig}, Persistence: &jobsPersistence, LeaseTimeout: cfg.RefreshJobLeaseTimeout, Logger: slog.Default()})
	if err != nil {
		return fail(err)
	}
	workloadController := workloadBundle.Controller
	cleanup.Push("workload", func(context.Context) error {
		workloadController.Close()
		return nil
	})
	jobModule := workloadBundle.Jobs
	agentPersistence, err := agentmodule.NewSQLitePersistence(agentmodule.SQLitePersistenceConfig{Database: store.SQLDB(), Workflow: jobModule, AuditIntentRecorder: auditRuntime.recorder})
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
	sealedDelivery := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
	resolveCurrentProjectID := func(ctx context.Context) (projectgraph.ResourceID, error) {
		claimed, found, err := readClaim(ctx)
		if err != nil {
			return "", fmt.Errorf("read live project claim: %w", err)
		}
		if found {
			return claimed, nil
		}
		return projectID, nil
	}
	snapshotAuthorizeConnection := accessmodule.ConnectionAuthorizerFromSnapshot(authorizationSnapshot, accessModule.AuthorizationSubjects)
	authorizeConnection := bootstrapAwareConnectionAuthorization(snapshotAuthorizeConnection, func(ctx context.Context) (bool, error) {
		currentProjectID, err := resolveCurrentProjectID(ctx)
		if err != nil {
			return false, err
		}
		return hasActiveBootstrapServingState(ctx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, currentProjectID.String())
	})
	managedDataPersistence, err := manageddatamodule.NewSQLitePersistence(manageddatamodule.SQLitePersistenceConfig{Database: store.SQLDB(), Workflow: jobModule, AuditIntentRecorder: auditRuntime.recorder})
	if err != nil {
		return fail(err)
	}
	managedDataModule, err := manageddatamodule.Build(ctx, manageddatamodule.Config{
		Persistence: &managedDataPersistence, Product: managedDataProductConfig(cfg), ServingStates: servingStateRepo,
		Environment: string(environment),
		CurrentPrincipal: func(r *http.Request) (manageddatamodule.Principal, bool) {
			auth := accessModule.Auth()
			if auth == nil {
				return manageddatamodule.Principal{}, false
			}
			principal, ok := auth.Principal(r)
			return manageddatamodule.Principal{ID: principal.ID, DevBypass: principal.DevBypass}, ok
		},
		AuthorizeConnection: manageddatamodule.ConnectionAuthorizer(authorizeConnection),
		Jobs:                jobModule, Workflow: jobModule,
		AuditIntentRecorder: auditRuntime.recorder,
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
	releasePersistence, err := releasemodule.NewSQLitePersistence(releasemodule.SQLitePersistenceConfig{
		Database: store.SQLDB(), Workflow: jobModule, AuditIntentRecorder: auditRuntime.recorder,
	})
	if err != nil {
		return fail(err)
	}
	releaseModule, err := releasemodule.Build(ctx, releasemodule.Config{
		Persistence: &releasePersistence, AuditIntentRecorder: auditRuntime.recorder,
		States:          servingStateRepo,
		ManagedDataPins: managedDataModule.BindingValidation(), ManagedDataHook: managedDataModule.BindingValidation(),
		ExtensionPreparation: extensionSupply,
		ArtifactDirectory:    cfg.ArtifactDir(), Environment: environment,
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
			AuthorizeConnection: snapshotAuthorizeConnection,
			Jobs:                jobModule, Workflow: jobModule,
		},
	})
	if err != nil {
		return fail(err)
	}
	activeRuntimeEvidence := activeConnectionEvidenceSource{
		releases: releaseModule, targetID: instanceID, environment: string(environment),
	}
	if err := analyticsModule.ConfigureActiveRuntimeBindings(activeRuntimeEvidence); err != nil {
		return fail(err)
	}
	managedDataResolution := managedDataModule.RuntimeResolution()
	if managedDataResolution == nil {
		return fail(errors.New("managed-data runtime resolver is required"))
	}
	managedDataResolver := appruntimefactory.NewManagedDataResolver(managedDataResolution)
	var sealedCoordinator *sealedcontrol.Coordinator
	var sealedPublishRequest deploymentmodule.SealedPublishRequestResolver
	var sealedRollbackRequest deploymentmodule.SealedRollbackRequestResolver
	var sealedRollbackFence func(context.Context, string) (string, int64, error)
	var sealedActiveState func(context.Context) (servingstate.ID, error)
	var deliveryStartupCheck func(context.Context) error
	{
		var beforePublicationCommit func(context.Context, deployment.PublicationIntent) error
		if string(environment) == "evaluation" {
			beforePublicationCommit = func(ctx context.Context, publication deployment.PublicationIntent) error {
				return sealedcontrol.QualificationActivationBarrier(ctx, publication.Environment)
			}
		}
		sealedCoordinator = &sealedcontrol.Coordinator{
			Publications: sealedDelivery, Rollbacks: sealedDelivery,
			BeforePublicationCommit: beforePublicationCommit,
			Authorize: func(ctx context.Context, binding sealedcontrol.SealBinding) error {
				if binding.Operation == "publish" {
					marker, marked := accessmodule.BootstrapAuthorizationFromContext(ctx)
					handled, decisionErr := sealedPublicationBootstrapDecision(ctx, binding, marker, marked, func(activeCtx context.Context) (bool, error) {
						return hasActiveBootstrapServingState(activeCtx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, binding.ProjectID)
					})
					if decisionErr != nil {
						return decisionErr
					}
					if handled {
						return nil
					}
				}
				return authorizeSealedPublication(ctx, binding, instanceID, sealedDelivery, accessModule, runtimeHostModule)
			},
			VerifySeal: func(ctx context.Context, binding sealedcontrol.SealBinding) error {
				slog.Default().InfoContext(ctx, "sealed publication seal verification started", "deployment", binding.DeploymentID, "candidate", binding.CandidateID, "bootstrap", binding.Bootstrap)
				candidate, err := sealedDelivery.DeliveryCandidateByID(ctx, binding.CandidateID)
				if err != nil {
					return err
				}
				seal, err := sealedDelivery.DeliveryCatalogSealByID(ctx, binding.Seal.SealID)
				if err != nil {
					return err
				}
				if candidate.Status != deployment.DeliveryCandidateReady || seal.Status != deployment.CatalogSealVerified || candidate.SealID != seal.ID || candidate.CatalogDigest != binding.Seal.CatalogDigest || candidate.CatalogObjectKey != binding.Seal.CatalogObjectKey || candidate.PhysicalPoolID != binding.Seal.PhysicalPoolID || candidate.CompatibilityDigest != binding.Seal.CompatibilityDigest || candidate.ServingArtifactID != binding.Seal.ServingArtifactID || candidate.ServingArtifactDigest != binding.Seal.ServingArtifactDigest || seal.CatalogDigest != binding.Seal.CatalogDigest || seal.CompatibilityDigest != binding.Seal.CompatibilityDigest || seal.ObjectKey != binding.Seal.CatalogObjectKey || seal.PhysicalPoolID != binding.Seal.PhysicalPoolID || seal.ServingArtifactID != binding.Seal.ServingArtifactID || seal.ServingArtifactDigest != binding.Seal.ServingArtifactDigest {
					return fmt.Errorf("sealed publication evidence is not one durable candidate/seal tuple")
				}
				pools := physicalpoolsqlite.NewRepository(store.SQLDB())
				admission, err := pools.LoadAdmissionContractByCompatibilityDigest(ctx, physicalpool.PoolID(seal.PhysicalPoolID), seal.CompatibilityDigest)
				if err != nil {
					return fmt.Errorf("load sealed pool admission: %w", err)
				}
				contract := &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
				objectStore, err := gcadapter.NewPoolStore(ctx, contract, gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle})
				if err != nil {
					return err
				}
				object, err := objectStore.Open(ctx, binding.Seal.CatalogObjectKey)
				if err != nil || object.Body == nil {
					return fmt.Errorf("open sealed catalog object: %w", err)
				}
				defer object.Body.Close()
				slog.Default().InfoContext(ctx, "sealed publication catalog object opened", "deployment", binding.DeploymentID, "objectKey", binding.Seal.CatalogObjectKey, "size", binding.Seal.ObjectSize)
				hash := sha256.New()
				n, err := io.Copy(hash, object.Body)
				if err != nil {
					return err
				}
				if n != binding.Seal.ObjectSize || object.Size != binding.Seal.ObjectSize || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != binding.Seal.CatalogDigest {
					return fmt.Errorf("sealed catalog object bytes or metadata do not match verified seal")
				}
				slog.Default().InfoContext(ctx, "sealed publication seal verification completed", "deployment", binding.DeploymentID, "candidate", binding.CandidateID)
				return nil
			},
		}
		releases := releaseModule.DeploymentLinkage()
		sealedPublishRequest = func(ctx context.Context, pending deploymentapiadapter.Deployment, releaseID string, actor deployment.ApprovalActor, bootstrap bool) (sealedcontrol.PublishRequest, error) {
			request, err := buildSealedPublishRequest(ctx, sealedDelivery, releases, pending, releaseID, instanceID)
			request.ActorID = actor.PrincipalID
			request.Bootstrap = bootstrap
			return request, err
		}
		sealedRollbackRequest = func(ctx context.Context, pending deploymentapiadapter.Deployment, releaseID string, actor deployment.ApprovalActor, expectedBaseGenerationID string, expectedTargetRevision int64) (sealedcontrol.RollbackRequest, error) {
			request, err := buildSealedRollbackRequest(ctx, sealedDelivery, releases, pending, releaseID, instanceID, expectedBaseGenerationID, expectedTargetRevision)
			request.ActorID = actor.PrincipalID
			return request, err
		}
		sealedRollbackFence = func(ctx context.Context, targetID string) (string, int64, error) {
			target, err := sealedDelivery.DeliveryTargetRevision(ctx, targetID)
			if err != nil {
				return "", 0, err
			}
			if strings.TrimSpace(target.ActiveGenerationID) == "" {
				return "", 0, fmt.Errorf("rollback requires an active delivery generation")
			}
			return target.ActiveGenerationID, target.TargetRevision, nil
		}
		sealedActiveState = func(ctx context.Context) (servingstate.ID, error) {
			currentProjectID, err := resolveCurrentProjectID(ctx)
			if err != nil {
				return "", err
			}
			target, targetErr := sealedDelivery.DeliveryTargetRevision(ctx, instanceID)
			if targetErr != nil {
				if errors.Is(targetErr, sql.ErrNoRows) || errors.Is(targetErr, deployment.ErrNotFound) {
					return "", servingstate.ErrNotFound
				}
				return "", targetErr
			}
			if target.ProjectID != currentProjectID.String() || target.Environment != string(environment) {
				return "", fmt.Errorf("%w: target scope or active generation changed", deployment.ErrDeliveryConflict)
			}
			// A target revision is created before its first publication. An empty
			// pointer is therefore the expected fresh-target state, not a scope
			// conflict; leave the sealed runtime unbound until publication commits.
			if strings.TrimSpace(target.ActiveGenerationID) == "" {
				return "", servingstate.ErrNotFound
			}
			active, err := sealedDelivery.ActiveDeliveryGenerationForTarget(ctx, instanceID, currentProjectID.String(), string(environment))
			if err != nil {
				// A fresh production target has no delivery pointer yet. Keep the
				// administration surface bootable and let readiness/serving report
				// the absence explicitly instead of failing process construction.
				if errors.Is(err, deployment.ErrNotFound) {
					return "", servingstate.ErrNotFound
				}
				return "", err
			}
			stateID := active.ServingStateID
			if strings.TrimSpace(stateID) == "" {
				return "", fmt.Errorf("active delivery generation has no persisted serving state")
			}
			return servingstate.ID(stateID), nil
		}
	}
	if err := refreshmodule.RecoverSQLite(ctx, store.SQLDB(), string(environment)); err != nil {
		return fail(err)
	}
	// Production candidate synchronization is canonical-only. The concrete
	// target-owned adapter is wired after candidate connection bindings and
	// runtime-host construction below; administration remains available when
	// physical-pool admission is absent.
	var canonicalDelivery *deploymentmodule.CanonicalDeliveryAdapter
	var canonicalDeliveryMutations *deploymentmodule.CanonicalDeliveryMutations
	candidatePreparationAdmission := candidatePreparationAdmitter(
		workloadController,
		workloadmodule.ControlRequest("candidate.prepare"),
	)
	canonicalDeliveryRequired := true
	// Production serving resolves only the durable delivery pointer and exact
	// sealed catalog object. The legacy process-wide catalog remains available
	// to evaluation/tests, but is not opened in production.
	var servingFactory runtimehost.RuntimeFactory
	var gcMaintenance *gcadapter.Maintenance
	{
		var gcErr error
		gcMaintenance, gcErr = gcadapter.NewMaintenance(func(gcCtx context.Context) error {
			gcProjectID, err := resolveCurrentProjectID(gcCtx)
			if err != nil {
				return fmt.Errorf("resolve physical-pool GC project: %w", err)
			}
			return localruntimefactory.RunSQLiteGC(gcCtx, localruntimefactory.SQLiteGCRunConfig{
				Database: store.SQLDB(), TargetID: instanceID, ProjectID: gcProjectID.String(), Environment: string(environment), OwnerID: instanceID, HolderID: instanceID,
				StagingRoot:   filepath.Join(cfg.RuntimeDir(), "gc"),
				PoolS3:        gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply},
				LeaseDuration: 15 * time.Minute, BuildGrace: time.Hour, OrphanGrace: time.Hour, ReaderGrace: 30 * time.Minute,
			})
		}, cfg.ManagedDataGCInterval, slog.Default(), nil)
		if gcErr != nil {
			return fail(gcErr)
		}
	}
	{
		var factoryErr error
		servingFactory, factoryErr = localruntimefactory.NewSQLiteSealedFactory(localruntimefactory.SQLiteSealedFactoryConfig{
			Database: store.SQLDB(), TargetID: instanceID, CatalogObjectRoot: cfg.ArtifactDir(),
			DuckDBDir: cfg.DuckDBDirPath(), RuntimeDir: cfg.RuntimeDir(), LeaseHolder: instanceID,
			ProjectRuntimeFactory: analyticsModule.ProjectRuntimeFactoryForEnvironment,
			DashboardMaxRows:      cfg.QueryResultMaxRows, DashboardMaxBytes: cfg.QueryResultMaxBytes,
			PoolS3:             gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply},
			ActivationEvidence: activeRuntimeEvidence,
			Authorize: func(ctx context.Context, evidence localruntimefactory.SealedAuthorizationInput) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				claimed, found, err := readClaim(ctx)
				if err != nil {
					return fmt.Errorf("read live serving claim: %w", err)
				}
				if !found || claimed.String() != evidence.ProjectID || evidence.TargetID != instanceID || evidence.Environment != string(environment) || evidence.ProjectID == "" || evidence.GenerationID == "" && evidence.CandidateID == "" || evidence.SealID == "" {
					return fmt.Errorf("sealed serving live authorization evidence is incomplete")
				}
				return nil
			},
		})
		if factoryErr != nil {
			return fail(factoryErr)
		}
	}
	err = withRuntimeHostStartupAdmission(ctx, workloadController, func(startupCtx context.Context) error {
		var buildErr error
		runtimeHostModule, buildErr = runtimehostmodule.Build(startupCtx, runtimehostmodule.Config{
			States:                   servingStateRepo,
			ProjectID:                projectID,
			Environment:              environment,
			ReadClaimedProject:       readClaim,
			ManagedData:              managedDataResolver,
			Authorization:            authorizationInstaller,
			Factory:                  servingFactory,
			RequireSealedCatalog:     true,
			ResolveSealedActiveState: sealedActiveState,
		})
		return buildErr
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
	authoringPersistence, err := dashboardmodule.NewSQLiteAuthoringPersistence(store.SQLDB(), auditRuntime.recorder)
	if err != nil {
		return fail(err)
	}
	dashboardPersistence, err := dashboardmodule.NewSQLitePersistence(store.SQLDB(), auditRuntime.recorder)
	if err != nil {
		return fail(err)
	}
	authoringApplication, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{
		SQLitePersistence: authoringPersistence,
		AuthorizeResource: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
			if authoringDevelopmentBypass(ctx, principalID) {
				return true, nil
			}
			return authorizeProjectResources(ctx, accessModule, runtimeHostModule, principalID, projectID, []access.ResourceRef{resource}, capability)
		},
		AuthorizeProjectCapability: func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, capability access.Capability) (bool, error) {
			if authoringDevelopmentBypass(ctx, principalID) {
				return true, nil
			}
			return authorizeProjectRole(ctx, accessModule, runtimeHostModule, principalID, projectID, capability)
		},
		AcquireRuntime: authoringAcquireRuntime,
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
	refreshPersistence, err := refreshmodule.NewSQLitePersistence(refreshmodule.SQLitePersistenceConfig{
		Database: store.SQLDB(), Workflow: jobModule, Audit: auditRuntime.recorder,
	})
	if err != nil {
		return fail(err)
	}
	{
		// The canonical adapter is assembled only after the runtime binding
		// leaser exists. Missing or unadmitted pool configuration leaves the
		// process available for administration but makes candidate sync fail
		// closed with ErrCandidateUnavailable.
		deliveryRepository := deploymentsqlite.NewRepositoryWithHooks(store.SQLDB(), deploymentsqlite.ActivationHooks{})
		deliveryLifecycle, lifecycleErr := deployment.NewDeliveryLifecycle(localruntimefactory.BootstrapTargetResolver{
			Resolver: deliveryRepository, TargetID: instanceID, ProjectID: projectID.String(), Environment: string(environment),
			ProjectIDResolver: func(resolveCtx context.Context) (string, error) {
				claimed, found, err := readClaim(resolveCtx)
				if err != nil {
					return "", err
				}
				if !found {
					return "", nil
				}
				return claimed.String(), nil
			},
		}, deliveryRepository)
		if lifecycleErr != nil {
			return fail(lifecycleErr)
		}
		var poolContract *ducklake.PoolContract
		var poolStore catalogseal.ObjectStore
		var poolCredentialBootstrap ducklake.CredentialBootstrap
		var poolErr error
		deliveryPhysicalPoolID := strings.TrimSpace(cfg.DeliveryPhysicalPoolID)
		deliveryCompatibilityDigest := strings.TrimSpace(cfg.DeliveryPhysicalPoolCompatibilityDigest)
		// The disposable loopback-only evaluation profile deliberately uses the
		// production serving runtime, but it retains the development contract of
		// owning and qualifying an isolated local pool. Ordinary production
		// targets must still use reviewed offline admission evidence.
		if allowsLocalEvaluationRuntime(production, cfg.EvaluationMode) && deliveryPhysicalPoolID == "" {
			tuple, compatibilityErr := poolcompatibility.LocalPool(ctx, extensionSupply)
			if compatibilityErr != nil {
				poolErr = fmt.Errorf("resolve local physical-pool compatibility: %w", compatibilityErr)
			} else if deliveryStorageLocation, storageLocationErr := filepath.Abs(cfg.DuckLakeDataDir()); storageLocationErr != nil {
				poolErr = fmt.Errorf("resolve local physical-pool storage location: %w", storageLocationErr)
			} else {
				pool, newPoolErr := physicalpool.NewPhysicalPool(physicalpool.PoolIdentity{StorageLocation: deliveryStorageLocation, StorageNamespace: "delivery", EncryptionDomain: "local", IsolationBoundary: instanceID, RetentionAuthority: instanceID, RetentionPolicy: physicalpool.RetentionPolicy{ReaderGracePeriodSeconds: 1800, OrphanGracePeriodSeconds: 3600, BuildGracePeriodSeconds: 3600}, Compatibility: tuple})
				if newPoolErr != nil {
					poolErr = newPoolErr
				} else {
					pools := physicalpoolsqlite.NewRepository(store.SQLDB())
					admission, loadErr := pools.LoadAdmissionContract(ctx, pool.ID)
					if loadErr == nil && admission.Pool.Compatibility == tuple {
						deliveryPhysicalPoolID = admission.Pool.ID.String()
						deliveryCompatibilityDigest = admission.Admission.CompatibilityDigest
					} else if loadErr != nil && !errors.Is(loadErr, sql.ErrNoRows) && !errors.Is(loadErr, deployment.ErrNotFound) && !errors.Is(loadErr, physicalpool.ErrPoolNotAdmitted) {
						poolErr = fmt.Errorf("load local physical-pool admission: %w", loadErr)
					} else {
						evidence, evidenceErr := ducklake.RunLocalPoolConformance(ctx, filepath.Join(cfg.RuntimeDir(), "delivery-conformance"), tuple, extensionSupply)
						if evidenceErr != nil {
							poolErr = fmt.Errorf("run local physical-pool conformance: %w", evidenceErr)
						} else if dataPath, dataPathErr := pool.DataPath(); dataPathErr != nil {
							poolErr = dataPathErr
						} else if err := securefs.EnsurePrivateDir(dataPath); err != nil {
							poolErr = err
						} else if marker, markerErr := gcstore.NewLocal(dataPath); markerErr != nil {
							poolErr = markerErr
						} else if admitted, admission, admitErr := pools.CreateAndAdmitWithOwnership(ctx, pool, evidence, instanceID, marker); admitErr != nil {
							poolErr = fmt.Errorf("admit local physical pool: %w", admitErr)
						} else {
							deliveryPhysicalPoolID = admitted.ID.String()
							deliveryCompatibilityDigest = admission.CompatibilityDigest
						}
					}
				}
			}
		}
		if deliveryPhysicalPoolID != "" {
			pools := physicalpoolsqlite.NewRepository(store.SQLDB())
			poolID := physicalpool.PoolID(deliveryPhysicalPoolID)
			var admission physicalpoolsqlite.AdmissionContract
			var err error
			if deliveryCompatibilityDigest != "" {
				admission, err = pools.LoadAdmissionContractByCompatibilityDigest(ctx, poolID, deliveryCompatibilityDigest)
			} else {
				// A pool with append-only upgrades is ambiguous without the exact
				// tuple. The repository intentionally rejects that case; never
				// select the newest admission by timestamp.
				admission, err = pools.LoadAdmissionContract(ctx, poolID)
			}
			if err != nil {
				poolErr = fmt.Errorf("load configured delivery physical-pool admission: %w", err)
			} else {
				poolContract = &ducklake.PoolContract{Pool: admission.Pool, Tuple: admission.Pool.Compatibility, Admission: admission.Admission, Evidence: admission.Evidence}
				poolS3 := gcadapter.S3Config{Region: cfg.ManagedDataS3Region, AccessKeyID: cfg.ManagedDataS3AccessKeyID, SecretAccessKey: cfg.ManagedDataS3SecretAccessKey, SessionToken: cfg.ManagedDataS3SessionToken, Endpoint: cfg.ManagedDataS3Endpoint, PathStyle: cfg.ManagedDataS3PathStyle, ExtensionAdmission: extensionSupply}
				poolStore, poolErr = localruntimefactory.NewCatalogObjectStore(ctx, poolContract, poolS3)
				if poolErr == nil {
					poolCredentialBootstrap, poolErr = gcadapter.NewPoolCredentialBootstrap(poolContract, poolS3)
				}
			}
		}
		// A production process may still serve the administration surface on a
		// fresh target, but readiness must expose missing pool admission,
		// missing target revision, or legacy rows without serving identity. The
		// callback reads durable state each time; configuration never synthesizes
		// an admission or serving pointer.
		deliveryStartupCheck = func(ctx context.Context) error {
			startupProjectID, err := resolveDeliveryStartupProjectID(ctx, projectID, readClaim)
			if err != nil {
				return fmt.Errorf("delivery startup project claim: %w", err)
			}
			state := deployment.DeliveryStartupState{
				Production:               production,
				TargetID:                 instanceID,
				ProjectID:                startupProjectID.String(),
				Environment:              string(environment),
				ConfiguredPhysicalPoolID: deliveryPhysicalPoolID,
				PhysicalPoolExists:       poolContract != nil,
				PhysicalPoolAdmitted:     poolContract != nil && poolErr == nil,
				LegacyServingPathEnabled: false,
			}
			if indeterminate, err := deliveryRepository.HasIndeterminateDeliveryPublication(ctx, instanceID); err != nil {
				return fmt.Errorf("delivery startup publication reconciliation: %w", err)
			} else {
				state.IndeterminatePublication = indeterminate
			}
			target, targetErr := deliveryRepository.DeliveryTargetRevision(ctx, instanceID)
			if targetErr == nil {
				state.TargetRevisionExists = true
				if target.ProjectID != startupProjectID.String() || target.Environment != string(environment) {
					return fmt.Errorf("delivery startup target scope changed")
				}
			} else if !errors.Is(targetErr, sql.ErrNoRows) && !errors.Is(targetErr, deployment.ErrNotFound) {
				return fmt.Errorf("delivery startup target revision: %w", targetErr)
			}
			if targetErr == nil && strings.TrimSpace(target.ActiveGenerationID) != "" {
				active, err := deliveryRepository.ActiveDeliveryGenerationForTarget(ctx, instanceID, startupProjectID.String(), string(environment))
				if err == nil {
					state.ActiveServingGeneration = true
					state.ActiveServingStateIdentity = active.ServingStateID
					if strings.TrimSpace(state.ActiveServingStateIdentity) == "" {
						state.MigratedRowsWithoutServingID = 1
					}
				} else if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, deployment.ErrNotFound) {
					return fmt.Errorf("delivery startup active generation: %w", err)
				}
			}
			return deployment.ValidateDeliveryStartup(state)
		}
		canonicalRuntime, runtimeErr := deployment.NewCandidateRuntimeService(deployment.CandidateRuntimeServiceConfig{Connections: candidateConnectionLeaser{leaser: candidateBindings, module: analyticsModule}, Runtime: runtimeHostModule, RuntimeVersion: identity.Version + ":" + identity.Revision})
		if runtimeErr != nil {
			return fail(runtimeErr)
		}
		materialize := func(matCtx context.Context, working *candidatecatalog.WorkingCatalog, buildInput deployment.DeliveryBuildInput, artifacts release.CandidateArtifactSet, candidateID string) ([]analyticsgates.SourceInput, error) {
			observationBounds := analyticsgates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000}
			matCtx = analyticsmaterialize.WithObservationBudget(matCtx, analyticsmaterialize.ObservationBudget{MaxQueries: observationBounds.MaxQueries, MaxMillis: observationBounds.MaxMillis})
			models := artifacts.Compiler.Artifact.Models()
			if len(models) == 0 {
				return nil, fmt.Errorf("compiled project contains no semantic models")
			}
			// Candidate artifacts intentionally keep managed-data locations out of
			// the portable project artifact. Resolve the exact pins that were
			// installed by ValidateWithManagedDataRevisions and bind their leased
			// runtime roots to this detached model copy for physical refresh.
			managedResolution, resolveErr := managedDataResolver.ResolveManagedDataForIdentity(matCtx, artifacts.Generation.Identity)
			if resolveErr != nil {
				return nil, fmt.Errorf("resolve candidate managed-data roots: %w", resolveErr)
			}
			if managedResolution.Lifetime != nil {
				defer managedResolution.Lifetime.Release()
			}
			if err := analyticsmodule.BindCandidateManagedDataRoots(models, artifacts.Compiler.Artifact.Manifest().NameIndex.Connections, managedResolution.Roots); err != nil {
				return nil, err
			}
			pipelineScoped := buildInput.Plan.PipelinePlan != nil && buildInput.Plan.Operation == deployment.DeliveryOperationRestatement
			if artifacts.Generation.DataMode == release.GenerationDataReuseBase && !pipelineScoped {
				actual, err := working.VisibleTables(matCtx)
				if err != nil {
					return nil, err
				}
				expected := appruntimefactory.ExpectedRelations(artifacts)
				if err := appruntimefactory.VerifyExpectedRelations(actual, expected); err != nil {
					return nil, err
				}
				return sourceInputsFromManifest(artifacts, nil), nil
			}
			var observations []analyticsgates.SourceInput
			err := working.WithEnvironment(matCtx, func(environment *ducklake.Environment) error {
				baseRetained := buildInput.Attempt.BaseCatalogDigest != ""
				changedByModel, removed, refreshAll := deliveryMaterializationDelta(artifacts, buildInput.Plan)
				if baseRetained {
					for _, table := range removed {
						if err := environment.Exec(matCtx, `DROP TABLE IF EXISTS "model"."`+strings.ReplaceAll(table, `"`, `""`)+`"`); err != nil {
							return fmt.Errorf("remove retired model relation %q: %w", table, err)
						}
					}
				}
				factory := analyticsModule.ProjectRuntimeFactoryForEnvironment(environment)
				runtime, err := factory.OpenProject(matCtx, analyticsruntime.ProjectRequest{Models: models, ServingStateID: artifacts.Generation.Identity.GenerationID, TargetID: buildInput.Plan.TargetID, ProjectID: artifacts.Generation.Identity.ProjectID, Environment: artifacts.Generation.Identity.Environment, SemanticDigest: artifacts.Artifact.ProjectDigest, ArtifactDigest: artifacts.Generation.ArtifactDigest, SourceDataDigest: artifacts.Generation.DataRevision, CandidateID: candidateID, AuthorizationFingerprint: artifacts.AuthorizationFingerprint, BindingFingerprint: buildInput.Plan.Execution.BindingDigest, SkipInitialRefresh: baseRetained && !refreshAll})
				if err != nil {
					return err
				}
				defer runtime.Close()
				if baseRetained && !refreshAll {
					for modelID, tables := range changedByModel {
						if len(tables) == 0 {
							continue
						}
						if err := runtime.RefreshModelTables(matCtx, modelID, tables); err != nil {
							return fmt.Errorf("refresh impacted model %q: %w", modelID, err)
						}
					}
					observations = sourceInputsFromManifest(artifacts, runtime)
					return nil
				}
				if err := runtime.Refresh(matCtx); err != nil {
					return err
				}
				observations = sourceInputsFromManifest(artifacts, runtime)
				return nil
			})
			if err != nil {
				return nil, err
			}
			return observations, nil
		}
		var baseResolver func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error)
		if poolContract != nil && poolStore != nil {
			baseResolver = func(baseCtx context.Context, buildInput deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				if buildInput.Plan.BaseGenerationID == "" {
					return nil, nil
				}
				generation, err := deliveryRepository.DeliveryGenerationByID(baseCtx, buildInput.Plan.BaseGenerationID)
				if err != nil {
					return nil, err
				}
				baseCandidate, err := deliveryRepository.DeliveryCandidateByID(baseCtx, generation.CandidateID)
				if err != nil {
					return nil, err
				}
				seal, err := deliveryRepository.DeliveryCatalogSealByID(baseCtx, baseCandidate.SealID)
				if err != nil {
					return nil, err
				}
				if generation.CatalogDigest != seal.CatalogDigest || generation.PhysicalPoolID != poolContract.Pool.ID.String() || generation.CompatibilityDigest != seal.CompatibilityDigest || baseCandidate.Status != deployment.DeliveryCandidateReady || baseCandidate.ServingStateID != generation.ServingStateID || baseCandidate.ServingArtifactID != generation.ServingArtifactID || baseCandidate.ServingArtifactDigest != generation.ServingArtifactDigest {
					return nil, fmt.Errorf("active sealed base does not match configured physical pool")
				}
				return &candidatecatalog.SealedArtifact{ObjectKey: seal.ObjectKey, Digest: seal.CatalogDigest, SizeBytes: seal.ObjectSize, PhysicalPoolID: seal.PhysicalPoolID, Compatibility: poolContract.Tuple, Reader: candidatecatalog.ObjectReader{Store: localruntimefactory.CandidateObjectStore{Store: poolStore}, Key: seal.ObjectKey}}, nil
			}
		}
		var verifyLease candidatecatalog.LeaseVerifier
		if poolErr == nil && poolContract != nil {
			verifyLease = localruntimefactory.SQLiteWriterLeaseVerifier(deliveryRepository)
		}
		buildFactory := localruntimefactory.BuildRequestFactory(localruntimefactory.CandidateCatalogRunnerConfig{PoolContract: poolContract, StagingRoot: cfg.DeliveryStagingDir, ExtensionAdmission: extensionSupply, CredentialBootstrap: poolCredentialBootstrap, Base: baseResolver, Materialize: materialize, Connections: candidateConnectionLeaser{leaser: candidateBindings, module: analyticsModule}, QualificationFactory: appruntimefactory.QualificationRequestForCandidate, ObjectStore: poolStore, SealRepository: deliveryRepository, RemoteVerifier: localruntimefactory.ReadOnlyCatalogVerifier{PoolContract: poolContract, StagingRoot: cfg.DeliveryStagingDir, ObjectStore: poolStore, ExtensionAdmission: extensionSupply, CredentialBootstrap: poolCredentialBootstrap}, VerifyLease: verifyLease, RuntimeVersion: identity.Version + ":" + identity.Revision})
		planCandidate := func(planCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) (deployment.DeliveryPlan, error) {
			var reuse *deployment.DeliveryReuseInput
			if input.Candidate.Scope.BaseGenerationID != "" {
				generation, generationErr := deliveryRepository.DeliveryGenerationByID(planCtx, input.Candidate.Scope.BaseGenerationID)
				if generationErr != nil {
					return deployment.DeliveryPlan{}, generationErr
				}
				baseCandidate, candidateErr := deliveryRepository.DeliveryCandidateByID(planCtx, generation.CandidateID)
				if candidateErr != nil {
					return deployment.DeliveryPlan{}, candidateErr
				}
				basePlan, planErr := deliveryRepository.PlanByID(planCtx, baseCandidate.PlanID)
				if planErr != nil {
					return deployment.DeliveryPlan{}, planErr
				}
				baseContextDigest, contextErr := basePlan.Execution.ContextDigest()
				if contextErr != nil {
					return deployment.DeliveryPlan{}, contextErr
				}
				reuse = &deployment.DeliveryReuseInput{
					BaseExecutionDigest: baseCandidate.ExecutionDigest, CatalogDigest: generation.CatalogDigest, BaseCatalogDigest: generation.CatalogDigest,
					PhysicalPoolID: generation.PhysicalPoolID, BasePhysicalPoolID: generation.PhysicalPoolID, BaseContextDigest: baseContextDigest,
					CompatibilityDigest: generation.CompatibilityDigest, BaseCompatibilityDigest: generation.CompatibilityDigest, Deterministic: artifacts.Generation.Deterministic,
				}
			}
			return appruntimefactory.PreviewCandidatePlanWithPolicyAndReuse(planCtx, deliveryLifecycle, input, artifacts, identity.Version+":"+identity.Revision, appruntimefactory.CandidateDeliveryPolicy{RequiresApproval: requiresDeliveryApproval(production, cfg.EvaluationMode, input.Operation), ApprovalPolicyRevision: appruntimefactory.CurrentApprovalPolicyRevision, RollbackClass: deployment.DeliveryServingSafe, RetentionWindow: cfg.DeliveryRollbackRetention().String()}, reuse)
		}
		publishCanonicalCandidate := func(publishCtx context.Context, project, candidate, actor string, refreshFence *deployment.RefreshPublicationFence) (deployment.DeliveryPublication, error) {
			candidateRecord, candidateErr := sealedDelivery.DeliveryCandidateByID(publishCtx, candidate)
			if candidateErr != nil {
				return deployment.DeliveryPublication{}, candidateErr
			}
			if candidateRecord.ProjectID.String() != project {
				return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication project scope changed", deployment.ErrDeliveryConflict)
			}
			request, err := buildCanonicalPublishRequest(publishCtx, sealedDelivery, candidate, instanceID)
			if err != nil {
				return deployment.DeliveryPublication{}, err
			}
			if refreshFence != nil {
				if err := refreshFence.Validate(); err != nil {
					return deployment.DeliveryPublication{}, err
				}
				request.Publication.RefreshRunID = refreshFence.RunID
				request.Publication.RefreshLeaseOwner = refreshFence.LeaseOwner
				request.Publication.RefreshLeaseRevision = refreshFence.LeaseRevision
				request.Publication.RefreshTargetRevision = refreshFence.TargetRevision
			}
			request.ActorID = actor
			if _, err := sealedCoordinator.PublishWithActivation(publishCtx, request, func(activationCtx context.Context, commit func() error) error {
				commitAndVerify := func() error {
					if err := commit(); err != nil {
						return err
					}
					return verifyCanonicalDeliveryTarget(activationCtx, sealedDelivery, instanceID, request.Publication.ProjectID.String(), request.Publication.Environment, request.Generation.ServingStateID, request.Publication.ExpectedTargetRevision+1)
				}
				return activateCanonicalServingState(activationCtx, runtimeHostModule, request.Generation.ServingStateID, commitAndVerify)
			}); err != nil {
				if pending, readErr := sealedDelivery.DeliveryPublicationByID(publishCtx, request.Publication.ID); readErr == nil {
					return pending, err
				}
				return deployment.DeliveryPublication{}, err
			}
			activated := deployment.Deployment{
				ServingIdentity: projectgraph.ServingIdentity{
					ProjectID: request.Publication.ProjectID, Environment: request.Publication.Environment,
					GenerationID: request.Generation.ServingStateID,
				},
				ActivationPrincipal: actor,
			}
			if err := reconcileSQLiteActivatedDashboardPublications(publishCtx, store.SQLDB(), servingStateRepo, activated); err != nil {
				logDashboardPublicationReconciliationFailure(slog.Default(), err, request.Generation.ServingStateID)
			}
			return sealedDelivery.DeliveryPublicationByID(publishCtx, request.Publication.ID)
		}
		canonicalDelivery = appruntimefactory.NewCanonicalDeliveryAdapter(appruntimefactory.CanonicalDeliveryConfig{Lifecycle: deliveryLifecycle, Artifacts: releaseModule, Publish: func(publishCtx context.Context, project, candidate, actor, _ string) (deployment.DeliveryPublication, error) {
			return publishCanonicalCandidate(publishCtx, project, candidate, actor, nil)
		}, Rollback: func(rollbackCtx context.Context, project, generation, actor, key string) (deployment.DeliveryPublication, error) {
			generationRecord, generationErr := sealedDelivery.DeliveryGenerationByID(rollbackCtx, generation)
			if generationErr != nil {
				return deployment.DeliveryPublication{}, generationErr
			}
			if generationRecord.ProjectID.String() != project {
				return deployment.DeliveryPublication{}, fmt.Errorf("%w: rollback project scope changed", deployment.ErrDeliveryConflict)
			}
			request, err := buildCanonicalRollbackRequest(rollbackCtx, sealedDelivery, generation, key, instanceID)
			if err != nil {
				return deployment.DeliveryPublication{}, err
			}
			request.ActorID = actor
			if _, err := sealedCoordinator.RollbackWithActivation(rollbackCtx, request, func(activationCtx context.Context, commit func() error) error {
				commitAndVerify := func() error {
					if err := commit(); err != nil {
						return err
					}
					return verifyCanonicalDeliveryTarget(activationCtx, sealedDelivery, instanceID, request.Request.ProjectID.String(), request.Request.Environment, request.Request.GenerationID, request.Request.ExpectedTargetRevision+1)
				}
				return activateCanonicalServingState(activationCtx, runtimeHostModule, request.Request.GenerationID, commitAndVerify)
			}); err != nil {
				return deployment.DeliveryPublication{}, err
			}
			activated := deployment.Deployment{
				ServingIdentity: projectgraph.ServingIdentity{
					ProjectID: request.Request.ProjectID, Environment: request.Request.Environment,
					GenerationID: request.Request.GenerationID,
				},
				ActivationPrincipal: actor,
			}
			if err := reconcileSQLiteActivatedDashboardPublications(rollbackCtx, store.SQLDB(), servingStateRepo, activated); err != nil {
				logDashboardPublicationReconciliationFailure(slog.Default(), err, request.Request.GenerationID)
			}
			return sealedDelivery.DeliveryPublicationByID(rollbackCtx, request.Request.ID)
		}, Plan: planCandidate, PlanPreview: planCandidate, BuildRequest: func(buildCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error) {
			if poolErr != nil || poolContract == nil {
				return deployment.DeliveryBuildRequest{}, fmt.Errorf("%w: candidate physical-pool admission unavailable", deployment.ErrCandidateUnavailable)
			}
			request, err := buildFactory(buildCtx, input, artifacts)
			if err != nil {
				return deployment.DeliveryBuildRequest{}, err
			}
			return request, nil
		}, ReadyCandidate: func(readyCtx context.Context, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet, build deployment.DeliveryBuildResult) (deployment.Candidate, error) {
			if build.GateEvidence == nil {
				return deployment.Candidate{}, fmt.Errorf("%w: candidate gate evidence is required", deployment.ErrCandidateInvalid)
			}
			bindingFingerprint := ""
			if build.GateEvidence != nil {
				bindingFingerprint = build.GateEvidence.BindingGeneration
			}
			receipt, err := canonicalRuntime.Prepare(readyCtx, deployment.CandidateRuntimeRequest{Candidate: input.Candidate, AuthorizationFingerprint: artifacts.AuthorizationFingerprint, Generation: deployment.CandidateGenerationRuntime{Identity: artifacts.Generation.Identity, ArtifactDigest: artifacts.Generation.ArtifactDigest, DataRevision: artifacts.Generation.DataRevision, DataMode: deployment.CandidateDataMode(artifacts.Generation.DataMode), Connections: candidateConnectionRequirements(artifacts.Generation.Connections), AuthoredConnections: candidateReleaseAuthoredConnections(artifacts.Generation.AuthoredConnections), ManagedDataConnections: candidateManagedDataConnections(artifacts.Generation.ManagedDataPins), Extensions: append([]extension.Evidence(nil), artifacts.Extensions...), Restrictions: candidateRuntimeRestrictions(artifacts.Generation.Restrictions), BindingFingerprint: bindingFingerprint, GateEvidence: build.GateEvidence}})
			if err != nil {
				return deployment.Candidate{}, err
			}
			provenance, err := deploymentmodule.CandidateProvenance(input.Candidate, artifacts, receipt, input.Source.SourceRevision)
			if err != nil {
				return deployment.Candidate{}, err
			}
			retained, err := releaseModule.RetainCandidateProvenance(readyCtx, input.ProjectID, provenance)
			if err != nil {
				return deployment.Candidate{}, err
			}
			if retained.Digest != provenance.Digest {
				return deployment.Candidate{}, fmt.Errorf("retained candidate provenance changed")
			}
			input.Candidate.Status = deployment.CandidateReady
			input.Candidate.ProvenanceDigest = retained.Digest
			return input.Candidate, nil
		}})
		canonicalDeliveryMutations = &deploymentmodule.CanonicalDeliveryMutations{
			Lifecycle:    canonicalDelivery.Lifecycle,
			Sources:      candidateSources,
			Artifacts:    releaseModule,
			Admission:    candidatePreparationAdmission,
			Plan:         canonicalDelivery.Plan,
			PlanPreview:  canonicalDelivery.PlanPreview,
			BuildRequest: canonicalDelivery.BuildRequest,
			Adapter:      canonicalDelivery,
			Publish:      canonicalDelivery.Publish,
			PublishFenced: func(publishCtx context.Context, project, candidate, actor, _ string, fence deployment.RefreshPublicationFence) (deployment.DeliveryPublication, error) {
				return publishCanonicalCandidate(publishCtx, project, candidate, actor, &fence)
			},
			Rollback: canonicalDelivery.Rollback,
		}
	}
	deploymentPersistence, err := deploymentmodule.NewSQLitePersistence(deploymentmodule.SQLitePersistenceConfig{
		Database: store.SQLDB(), Releases: releaseModule.DeploymentLinkage(), Workflow: jobModule, CancelJob: jobModule,
		Audit: auditRuntime.recorder,
	})
	if err != nil {
		return fail(err)
	}
	deploymentConfig := deploymentmodule.Config{
		Persistence: &deploymentPersistence, AuditIntentRecorder: auditRuntime.recorder, States: servingStateRepo, Runtime: deploymentRuntime,
		DeliveryReader:     sealedDelivery,
		ManagedData:        managedDataResolver,
		BootstrapPolicies:  projectClaimRepository,
		ProjectClaims:      projectClaimRepository,
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
			// The process may have started before candidate synchronization
			// established the durable project claim, so the startup projectID
			// can legitimately be empty here. The bootstrap policy has already
			// been validated against the fresh claim above; use that canonical
			// request scope for the active-target check instead of the stale
			// startup snapshot.
			active, activeErr := hasActiveBootstrapServingState(ctx, runtimeHostModule, servingStateRepo, string(environment), sealedDelivery, instanceID, policy.ProjectID.String())
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
		CandidateAdmission:        candidatePreparationAdmission,
		CandidateSources:          candidateSources,
		CandidateArtifacts:        releaseModule,
		CanonicalDeliveryAdapter:  canonicalDelivery,
		DeliveryMutations:         canonicalDeliveryMutations,
		RequireCanonicalDelivery:  canonicalDeliveryRequired,
		CandidateSourceAudit:      candidateSourceAuditRecorder(accessModule),
		CandidateSourceBlobAudit:  candidateSourceAuditRecorder(accessModule),
		RuntimeVersion:            identity.Version + ":" + identity.Revision,
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
		SealedCoordinator: sealedCoordinator, SealedPublishRequest: sealedPublishRequest,
		SealedRollbackRequest: sealedRollbackRequest, SealedRollbackFence: sealedRollbackFence, RequireSealedCoordinator: true,
		SealedReconcile: func(ctx context.Context, generationID string) error {
			return runtimeHostModule.ReconcileSealed(ctx, servingstatemodule.ID(generationID))
		},
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
			AuditRuntime: auditRuntime, PlatformHealth: store,
			RecoveryMetrics:  refreshmodule.NewSQLiteRecoveryMetricsCollector(store.SQLDB(), nil),
			ServingStateRepo: servingStateRepo, RefreshServingStateMutations: servingStateRepo,
			AccessRepo: accessRepo, RefreshPersistence: &refreshPersistence, DashboardSQLite: dashboardPersistence,
			DashboardPublicationReconciler: newSQLiteDashboardPublicationReconciler(store.SQLDB()),
			APIIdempotency:                 apiIdempotency, CursorSigning: apiCursorSigning,
		},
		capabilityAssemblyInputs{
			AnalyticsModule: analyticsModule, DashboardAssets: dashboardAssets,
			ReleaseModule: releaseModule, JobModule: jobModule,
			AccessModule: accessModule, ManagedDataModule: managedDataModule, AgentPersistence: &agentPersistence,
			ProjectCatalog: projectCatalog,
			// Browser graph reads are pinned to the exact active runtime lease;
			// canonical sealed publication no longer updates the legacy active
			// serving-state scope pointer.
			ProjectGraph: projectmodule.NewActiveServingStateGraphReader(runtimeHostModule.Provider(), servingStateRepo),
			Authoring:    authoringApplication,
			Product:      productService, ProductStatus: productAdministrationStatus(cfg, instanceID, publicURL, string(environment), identity),
		},
		workflowAssemblyInputs{
			AgentSettings: store,
			AgentConfig:   agentmodule.ModelConfig{APIKey: cfg.AgentAPIKey, BaseURL: cfg.AgentBaseURL, Model: cfg.AgentModel},
			Auth:          auth, Reloader: runtimeHostModule, Workload: workloadController,
			ManagedDataValidation: managedDataModule.BindingValidation(),
			ManagedDataResolver:   managedDataResolver,
			RefreshSourceDigest:   canonicalRefreshSourceDigest(sealedDelivery, instanceID),
			CanonicalRefreshExecutor: canonicalRefreshExecutor(
				canonicalDeliveryMutations, sealedDelivery, instanceID, auth != nil && auth.DevBypass(),
			),
			PublishedVersion:        canonicalPublishedDataVersion(sealedDelivery, instanceID),
			EnableRefreshDispatcher: true,
			RecoveryInterval:        time.Minute,
			DeploymentConfig:        deploymentConfig,
		},
		runtimeAssemblyInputs{
			Production:           production && !cfg.EvaluationMode,
			RuntimeHost:          runtimeHostModule,
			DeliveryTargetReader: sealedDelivery,
			ProjectID:            projectID, ProjectIDResolver: projectIDResolver, ServingSnapshotResolver: servingSnapshotResolver,
			DuckLakeCatalogPath: duckLakeCatalogPath, DuckLakeDataPath: cfg.DuckLakeDataDir(),
			DefaultEnvironment: string(environment), SCIMBearerToken: cfg.SCIMBearerToken,
			MetricsBearerToken: cfg.MetricsBearerToken, AllowedHosts: allowedHosts, Assets: assets,
			InstanceID: instanceID, RequireActiveDeployment: cfg.EvaluationMode, SealedServing: true,
			RequireQueryAuthorization: production, AllowDevAuthBypass: !production,
			DeliveryStartup: deliveryStartupCheck,
		},
		httpAssemblyInputs{
			PublicURL: publicURL,
			DesktopDiscovery: desktopdiscovery.Config{
				CanonicalOrigin:   publicURL,
				InstanceID:        instanceID,
				DisplayName:       "LeapView",
				ServerVersion:     assets.Version(),
				AllowLoopbackHTTP: allowsLocalEvaluationRuntime(production, cfg.EvaluationMode),
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
	if sealedCoordinator != nil && routes.deploymentModule != nil {
		durableApproval := routes.deploymentModule.SealedApprovalVerifier()
		sealedCoordinator.ApprovalVerifier = func(approvalCtx context.Context, binding sealedcontrol.SealBinding, publication deployment.PublicationIntent) error {
			slog.Default().InfoContext(approvalCtx, "sealed publication approval verification started", "deployment", binding.DeploymentID, "bootstrap", binding.Bootstrap)
			if binding.Bootstrap {
				// The activation worker has already revalidated the durable
				// one-shot bootstrap policy. Recheck the active-generation fence
				// here because Authorize and ApprovalVerifier are separate control
				// boundaries; if a generation appeared in between, fall through to
				// ordinary durable approval rather than bypassing it.
				active, activeErr := hasActiveBootstrapServingState(
					approvalCtx,
					runtimeHostModule,
					servingStateRepo,
					string(environment),
					sealedDelivery,
					instanceID,
					binding.ProjectID,
				)
				if activeErr != nil {
					return activeErr
				}
				slog.Default().InfoContext(approvalCtx, "sealed publication bootstrap fence checked", "deployment", binding.DeploymentID, "active", active)
				if !active {
					return nil
				}
			}
			plan, planErr := sealedDelivery.PlanByID(approvalCtx, publication.PlanID)
			if planErr != nil {
				return planErr
			}
			if !plan.Governance.RequiresApproval {
				slog.Default().InfoContext(approvalCtx, "sealed publication approval not required", "deployment", binding.DeploymentID)
				return nil
			}
			err := durableApproval(approvalCtx, binding, publication)
			if err != nil {
				slog.Default().ErrorContext(approvalCtx, "sealed publication approval verification failed", "deployment", binding.DeploymentID, "error", err)
			}
			return err
		}
	}
	runtime.runtimeHostModule = runtimeHostModule
	handler := Routes(routes, runtime, platformServices, policy)
	lifecycle := newRuntimeLifecycle(platformServices.workers, runtime.analyticsModule, runtime.workloads, gcMaintenance)
	return handler, lifecycle, cleanup.Close, nil
}
