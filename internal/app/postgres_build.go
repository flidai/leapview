package app

// Native PostgreSQL process composition.  This file intentionally keeps the
// graph assembly separate from the local SQLite buildLocalSQLiteRuntime path: a
// production process must never open database/sql or infer a fallback store.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/gates"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	appaccesspostgres "github.com/flidai/leapview/internal/app/accesspostgres"
	"github.com/flidai/leapview/internal/app/config"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	appobjectstore "github.com/flidai/leapview/internal/app/objectstore"
	postgresauthority "github.com/flidai/leapview/internal/app/postgresauthority"
	projectsource "github.com/flidai/leapview/internal/app/projectsource"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	appruntimefactory "github.com/flidai/leapview/internal/app/runtimefactory"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddatamodule "github.com/flidai/leapview/internal/manageddata/module"
	"github.com/flidai/leapview/internal/platform/buildinfo"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	apihttpmiddleware "github.com/flidai/leapview/internal/platform/http/middleware"
	jobsmodule "github.com/flidai/leapview/internal/platform/jobs/module"
	platformobjectstore "github.com/flidai/leapview/internal/platform/objectstore"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workloadmodule "github.com/flidai/leapview/internal/workload/module"
)

// nativeProjectSourceComposition keeps the native source reader's authority
// set visible to composition tests. The synchronizer remains the sole
// capability injected into release.
type nativeProjectSourceComposition struct {
	Objects               platformobjectstore.ImmutableStore
	StorageSecurityDomain string
	Sources               projectsource.NativeSourceRepository
	CandidateSourceReader *projectsource.NativeCandidateSourceSynchronizer
}

type candidateApprovalServingStateReader interface {
	ByID(context.Context, servingstate.ID) (servingstate.State, error)
	ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
}

// candidateApprovalCapabilities compiles the immutable authorization policy
// attached to the exact not-yet-active generation. This is the reviewer
// authority for a first publication: it does not depend on a preview runtime
// having been opened and never consults mutable source files.
func candidateApprovalCapabilities(
	ctx context.Context,
	states candidateApprovalServingStateReader,
	objects projectbundle.ArtifactObjectReader,
	subjects func(context.Context, string) ([]access.SubjectRef, error),
	generationID, principalID string,
) (string, string, []access.Capability, error) {
	if states == nil || objects == nil || subjects == nil || strings.TrimSpace(generationID) == "" || strings.TrimSpace(principalID) == "" {
		return "", "", nil, errors.New("candidate approval authorization dependencies are unavailable")
	}
	id := servingstate.ID(strings.TrimSpace(generationID))
	state, err := states.ByID(ctx, id)
	if err != nil {
		return "", "", nil, fmt.Errorf("read candidate approval generation: %w", err)
	}
	if state.ID != id || state.ProjectID.Validate() != nil || strings.TrimSpace(string(state.Environment)) == "" || state.Status != servingstate.StatusValidated {
		return "", "", nil, errors.New("candidate approval generation identity is invalid")
	}
	artifact, err := states.ArtifactByServingState(ctx, id)
	if err != nil {
		return "", "", nil, fmt.Errorf("read candidate approval artifact: %w", err)
	}
	if artifact.ServingStateID != id || artifact.Digest != state.Digest {
		return "", "", nil, errors.New("candidate approval artifact differs from its generation")
	}
	compiled, err := (projectbundle.ServingArtifactLoader{Objects: objects}).LoadCompiled(ctx, artifact, "")
	if err != nil {
		return "", "", nil, fmt.Errorf("load candidate approval artifact: %w", err)
	}
	if compiled.ProjectID != state.ProjectID || compiled.ProjectDigest != state.ProjectDigest {
		return "", "", nil, errors.New("candidate approval compiled project differs from its generation")
	}
	identity, err := projectgraph.NewServingIdentity(state.ProjectID, string(state.Environment), string(state.ID))
	if err != nil {
		return "", "", nil, fmt.Errorf("bind candidate approval identity: %w", err)
	}
	snapshot, err := projectmodule.CompileAuthorizationSnapshotJSON(identity, compiled.Graph, state.AccessPolicyJSON)
	if err != nil {
		return "", "", nil, fmt.Errorf("compile candidate approval policy: %w", err)
	}
	// Subject membership remains durable and current, exactly like the active
	// authorization path. The generation freezes policy; disabling a principal
	// or removing a group membership must revoke approval immediately.
	resolvedSubjects, err := subjects(ctx, principalID)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve candidate approval subjects: %w", err)
	}
	capabilities, err := snapshot.EffectiveCapabilities(resolvedSubjects)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve candidate approval capabilities: %w", err)
	}
	return state.ProjectID.String(), string(state.Environment), capabilities, nil
}

// resolvePostgresSealedActiveState resolves the sealed runtime's authoritative
// active generation through the public deployment target port. A clean
// PostgreSQL installation has no delivery_target row until the first plan is
// admitted; that absence is the normal unbound/no-active state and must map to
// servingstate.ErrNotFound so runtimehost can remain administrable without
// fabricating an active delivery. Other authority failures stay fail-closed.
func resolvePostgresSealedActiveState(ctx context.Context, delivery deployment.DeliveryTargetResolver, targetID string) (servingstate.ID, error) {
	if delivery == nil || strings.TrimSpace(targetID) == "" {
		return "", errors.New("PostgreSQL sealed active-state target authority is unavailable")
	}
	target, err := delivery.ResolveDeliveryTarget(ctx, targetID)
	if err != nil {
		if errors.Is(err, deployment.ErrNotFound) {
			return "", servingstate.ErrNotFound
		}
		return "", err
	}
	if strings.TrimSpace(target.ActiveGenerationID) == "" {
		return "", servingstate.ErrNotFound
	}
	return servingstate.ID(target.ActiveGenerationID), nil
}

// composeNativeProjectSource wires the process-bound immutable object store
// to the PostgreSQL project authority. The caller owns transaction lifecycle
// through begin; this helper never opens a second database or derives a
// filesystem-backed project repository.
func composeNativeProjectSource(
	ctx context.Context,
	cfg config.Config,
	instanceID string,
	environment string,
	begin projectsource.BeginFunc,
	sources projectsource.NativeSourceRepository,
) (nativeProjectSourceComposition, error) {
	if begin == nil || sources == nil {
		return nativeProjectSourceComposition{}, errors.New("native project source composition requires PostgreSQL begin and project authorities")
	}
	objects, storageDomain, err := appobjectstore.New(ctx, cfg, instanceID, environment)
	if err != nil {
		return nativeProjectSourceComposition{}, fmt.Errorf("construct native project object store: %w", err)
	}
	compiler := projectsource.Compiler{}
	reader, err := projectsource.NewNativeCandidateSourceSynchronizer(projectsource.NativeCandidateSourceConfig{
		Begin:                 begin,
		Sources:               sources,
		Objects:               objects,
		Compiler:              compiler,
		StorageSecurityDomain: storageDomain,
	})
	if err != nil {
		return nativeProjectSourceComposition{}, fmt.Errorf("construct native candidate source synchronizer: %w", err)
	}
	return nativeProjectSourceComposition{
		Objects: objects, StorageSecurityDomain: storageDomain,
		Sources: sources, CandidateSourceReader: reader,
	}, nil
}

// buildPostgresProductionTarget assembles the native graph and HTTP surface.
// The retained PostgreSQL lifecycle is an application component, so pool
// shutdown follows worker/runtime-host shutdown on the reverse component path.
func buildPostgresProductionTarget(ctx context.Context, cfg config.Config) (*Application, error) {
	cfg.Production = true
	bootstrap, err := openPostgresControlPlane(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var runtimeHost *runtimehostmodule.Module
	var analytics *analyticsmodule.Module
	var workloads workloadControl
	var resourcesOnce sync.Once
	var resourcesErr error
	closeResources := func() error {
		resourcesOnce.Do(func() {
			if analytics != nil {
				resourcesErr = errors.Join(resourcesErr, analytics.Close())
			}
			if workloads != nil {
				workloads.Close()
			}
		})
		return resourcesErr
	}
	var runtimeHostOnce sync.Once
	var runtimeHostErr error
	closeRuntimeHost := func() error {
		runtimeHostOnce.Do(func() {
			if runtimeHost != nil {
				runtimeHostErr = runtimeHost.Close()
			}
		})
		return runtimeHostErr
	}
	fail := func(cause error) (*Application, error) {
		// Resource owners must close before the PostgreSQL pools they may still
		// reference. The application lifecycle enforces this order on success;
		// construction failures perform the same ordering explicitly.
		runtimeErr := closeRuntimeHost()
		resourceErr := closeResources()
		poolErr := bootstrap.Stop(context.Background())
		return nil, errors.Join(cause, runtimeErr, resourceErr, poolErr)
	}

	environment := servingstatemodule.NormalizeEnvironment(servingstatemodule.Environment(cfg.Environment))
	if strings.TrimSpace(cfg.Environment) == "" {
		environment = servingstatemodule.Environment("prod")
	}
	if err := servingstate.ValidateEnvironment(environment); err != nil {
		return fail(err)
	}
	for _, dir := range []string{cfg.HomeDir, cfg.ArtifactDir(), cfg.DuckDBDirPath(), cfg.RuntimeDir(), cfg.DuckLakeDataDir()} {
		if err := securefs.EnsurePrivateDir(dir); err != nil {
			return fail(err)
		}
	}
	assets := applicationAssets(cfg, true)
	dashboardAssets, err := dashboardmodule.BuildAssets(ctx, cfg.MapAssetDir)
	if err != nil {
		return fail(err)
	}
	cookieSecure, err := cfg.CookieSecure()
	if err != nil {
		return fail(err)
	}
	allowedHosts, err := cfg.ProductionAllowedHosts()
	if err != nil {
		return fail(err)
	}
	publicURL := firstConfigured(cfg.PublicURL, configuredListenURL(cfg.ListenAddr()))
	extensionSupply, err := loadExtensionSupply(ctx, cfg)
	if err != nil {
		return fail(err)
	}

	// The instance identity is durable bootstrap state, not a configuration
	// guess.  It is required before constructing any target-bound authority.
	instanceID, err := postgresauthority.ResolveInstanceIdentity(ctx, bootstrap.RuntimePool(), string(environment))
	if err != nil {
		return fail(fmt.Errorf("read PostgreSQL instance identity: %w", err))
	}
	fingerprintKey := []byte(strings.TrimSpace(cfg.TokenHashKey))
	if len(fingerprintKey) < 32 {
		fingerprintKey = []byte(strings.TrimSpace(cfg.CSRFKey))
	}
	if len(fingerprintKey) < 32 {
		return fail(errors.New("PostgreSQL access fingerprint key is required"))
	}
	graph, err := postgresauthority.NewPostgresAuthorityGraph(bootstrap.RuntimePool(), bootstrap.MaintenancePool(), postgresauthority.PostgresAuthorityGraphOptions{TargetID: instanceID, FingerprintKey: fingerprintKey})
	if err != nil {
		return fail(fmt.Errorf("build PostgreSQL authority graph: %w", err))
	}
	nativeProjectSource, err := composeNativeProjectSource(ctx, cfg, instanceID, string(environment), func(beginCtx context.Context) (projectsource.Tx, error) {
		return bootstrap.RuntimePool().Begin(beginCtx)
	}, graph.Project)
	if err != nil {
		return fail(fmt.Errorf("build native project source reader: %w", err))
	}
	readClaim := readClaimedProject(graph.DeploymentRepository, environment)
	authoringProject := postgresAuthoringProjectIDResolver(graph.DeploymentRepository, graph.ServingState, instanceID, environment)
	claimedProject, found, err := readClaim(ctx)
	if err != nil {
		return fail(err)
	}
	projectID := projectgraph.ResourceID("")
	if found {
		projectID = claimedProject
	}

	// Access is built before runtimehost; all current-project callbacks remain
	// late-bound and therefore work on a fresh target with no claim.
	currentProject := func(ctx context.Context) (projectgraph.ResourceID, error) {
		if runtimeHost == nil {
			return "", errors.New("runtime host is unavailable")
		}
		lease, err := runtimeHost.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		return lease.Identity().ProjectID, nil
	}
	avatarBlobs, err := profileImageBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	var internalOAuth *appaccesspostgres.InternalOAuthConfig
	if strings.TrimSpace(cfg.MCPOAuthIssuerURL) == "" {
		secret := sha256.Sum256([]byte("leapview:mcp-oauth:" + cfg.CSRFKey))
		internalOAuth = &appaccesspostgres.InternalOAuthConfig{IssuerURL: publicURL, ResourceURL: strings.TrimSuffix(publicURL, "/") + "/mcp", Secret: secret[:]}
	}
	accessPersistence, err := appaccesspostgres.NewPersistence(graph.Access, internalOAuth)
	if err != nil {
		return fail(err)
	}
	accessBundle, err := buildAccessCapability(ctx, accessCapabilityConfig{Persistence: &accessPersistence, Production: true, Auth: accessAuthConfig(cfg, true, cookieSecure), Assets: assets, AvatarBlobs: avatarBlobs, PublicURL: publicURL, InstanceID: instanceID, MCPIssuerURL: cfg.MCPOAuthIssuerURL, CurrentProject: currentProject, AuthoringProject: authoringProject})
	if err != nil {
		return fail(err)
	}

	jobsPersistence, err := jobsmodule.NewPostgresPersistence(graph.Jobs)
	if err != nil {
		return fail(err)
	}
	workloadBundle, err := buildWorkloadCapability(ctx, workloadCapabilityConfig{Persistence: &jobsPersistence, Production: true, LeaseTimeout: cfg.RefreshJobLeaseTimeout, Logger: slog.Default(), Workload: workloadmodule.Config{Policy: cfg.WorkloadConfig()}})
	if err != nil {
		return fail(err)
	}
	workloads = workloadBundle.Controller

	analyticsBundle, err := buildAnalyticsCapability(ctx, analyticsCapabilityConfig{ConnectionBindings: graph.ConnectionBinding, QueryAuditStore: graph.QueryAudit, Production: true, CredentialMode: analyticsmodule.CredentialModeNonSecret, CredentialTarget: instanceID, Environment: string(environment), RootDir: cfg.DuckDBDirPath(), DataPath: cfg.DuckLakeDataDir(), ExtensionSupply: extensionSupply, MaxConnections: cfg.WorkloadConfig().MaxRunning, MemoryMaxBytes: cfg.DuckDBNodeMemoryMaxBytes, TempMaxBytes: cfg.DuckDBNodeTempMaxBytes, MaxThreads: cfg.DuckDBNodeMaxThreads, TempDir: cfg.DuckDBTempDirPath(), DisableProcessEnv: true, RuntimeCacheItems: cfg.QueryCacheRuntimeMaxEntries, RuntimeCacheBytes: cfg.QueryCacheRuntimeMaxBytes, NodeCacheItems: cfg.QueryCacheNodeMaxEntries, NodeCacheBytes: cfg.QueryCacheNodeMaxBytes})
	if err != nil {
		return fail(err)
	}
	analytics = analyticsBundle.Module

	managedPersistence := graph.ManagedDataPersistence
	managedData, err := manageddatamodule.Build(ctx, manageddatamodule.Config{Persistence: managedPersistence, Production: true, CleanupAcker: graph.ManagedDataMaintenance, Product: managedDataProductConfig(cfg), ServingStates: graph.ServingState, Environment: string(environment), CurrentPrincipal: func(r *http.Request) (manageddatamodule.Principal, bool) {
		p, ok := accessBundle.Module.CurrentPrincipal(r)
		return manageddatamodule.Principal{ID: p.ID, DevBypass: p.DevBypass}, ok
	}, Jobs: workloadBundle.Jobs, Workflow: workloadBundle.Jobs, Worker: manageddatamodule.MaintenanceWorkerConfig{Interval: cfg.ManagedDataGCInterval, Acquire: func(ctx context.Context) (manageddatamodule.MaintenanceLease, error) {
		return workloadBundle.Controller.Acquire(ctx, workloadmodule.MaintenanceRequest("managed_data.collect"))
	}, Logger: slog.Default()}})
	if err != nil {
		return fail(fmt.Errorf("build managed-data module: %w", err))
	}
	managedResolver := appruntimefactory.NewManagedDataResolver(managedData.RuntimeResolution())

	identityResolver, err := apprefreshpostgres.NewPostgresPublicationIdentityResolverAdapter(graph.DeploymentRepository, instanceID)
	if err != nil {
		return fail(err)
	}
	canonicalVerifier, err := apprefreshpostgres.NewPostgresCanonicalVerifierAdapter(graph.DeploymentRepository, instanceID)
	if err != nil {
		return fail(err)
	}
	nativeRefreshFinalizer, err := apprefreshpostgres.NewPostgresNativeRefreshFinalizer(graph.Refresh, graph.DeploymentRepository, instanceID)
	if err != nil {
		return fail(err)
	}
	refreshOperations, err := apprefreshpostgres.NewPostgresOperationAuthorityAdapter(graph.Operation)
	if err != nil {
		return fail(err)
	}
	refreshPersistence, err := refreshmodule.NewPostgresPersistence(graph.Refresh, refreshmodule.PostgresPersistenceConfig{SchedulerOwner: instanceID, PublicationIdentityResolver: identityResolver, Jobs: graph.RefreshJobs, CanonicalVerifier: canonicalVerifier, NativeFinalizer: nativeRefreshFinalizer, Operations: refreshOperations, CancelAuditWriter: graph.RefreshCancelAudit, CreateAuditWriter: graph.RefreshCancelAudit})
	if err != nil {
		return fail(fmt.Errorf("build refresh persistence: %w", err))
	}

	productBlobs, err := productLogoBlobStore(ctx, cfg)
	if err != nil {
		return fail(err)
	}
	product, err := adminmodule.NewProductServiceWithStorage(graph.Product, productBlobs)
	if err != nil {
		return fail(err)
	}
	releasePersistence, err := releasemodule.NewPostgresPersistence(graph.Release)
	if err != nil {
		return fail(fmt.Errorf("build release persistence: %w", err))
	}
	release, err := releasemodule.Build(ctx, releasemodule.Config{Persistence: &releasePersistence, Catalog: graph.ReleaseCatalog, Production: true, States: graph.ServingState, ManagedDataPins: managedData.BindingValidation(), ManagedDataHook: managedData.BindingValidation(), ExtensionPreparation: extensionSupply, ArtifactDirectory: cfg.ArtifactDir(), Environment: environment, CandidateSourceReader: nativeProjectSource.CandidateSourceReader, CandidateArtifactStore: nativeProjectSource.Objects, StorageSecurityDomain: nativeProjectSource.StorageSecurityDomain, API: releasemodule.APIConfig{Jobs: workloadBundle.Jobs, Workflow: workloadBundle.Jobs}})
	if err != nil {
		return fail(fmt.Errorf("build release module: %w", err))
	}

	// Runtime factory resolution is entirely root-driven. No catalog database,
	// pool ID, UUID, or snapshot is synthesized from configuration.
	targetReader := appdeploymentpostgres.NewTargetReader(graph.DeploymentRepository)
	// Refresh admission carries the target-owned fence and source identity
	// forward from PostgreSQL. Neither value is inferred from the serving-state
	// row or process configuration; both are checked against the exact active
	// generation selected by the target pointer. The active plan may itself
	// have been built from a predecessor generation; only its own plan ID and
	// digest are authoritative here.
	resolveRefreshTargetRevision := func(resolveCtx context.Context, identity projectgraph.ServingIdentity) (int64, error) {
		target, err := targetReader.DeliveryTargetRevision(resolveCtx, instanceID)
		if err != nil {
			return 0, err
		}
		if target.ProjectID != identity.ProjectID.String() || target.Environment != identity.Environment || target.ActiveGenerationID != identity.GenerationID {
			return 0, fmt.Errorf("%w: refresh target fence does not match active serving identity", deployment.ErrDeliveryConflict)
		}
		if target.TargetRevision <= 0 {
			return 0, fmt.Errorf("%w: refresh target revision is not positive", deployment.ErrDeliveryConflict)
		}
		return target.TargetRevision, nil
	}
	resolveRefreshSourceDigest := func(resolveCtx context.Context, identity projectgraph.ServingIdentity) (string, error) {
		target, err := targetReader.DeliveryTargetRevision(resolveCtx, instanceID)
		if err != nil {
			return "", err
		}
		if target.ProjectID != identity.ProjectID.String() || target.Environment != identity.Environment || target.ActiveGenerationID != identity.GenerationID {
			return "", fmt.Errorf("%w: refresh source target does not match active serving identity", deployment.ErrDeliveryConflict)
		}
		generation, err := graph.DeploymentRepository.Generation(resolveCtx, target.ActiveGenerationID)
		if err != nil {
			return "", err
		}
		if generation.TargetID != instanceID || generation.GenerationID != identity.GenerationID || strings.TrimSpace(generation.PlanID) == "" {
			return "", fmt.Errorf("%w: refresh active generation identity is incomplete", deployment.ErrDeliveryConflict)
		}
		storedPlan, err := graph.DeploymentRepository.Plan(resolveCtx, generation.PlanID)
		if err != nil {
			return "", err
		}
		rich, err := storedPlan.RichPlan()
		if err != nil {
			return "", err
		}
		if rich.ID != generation.PlanID || rich.ProjectID != identity.ProjectID || rich.TargetID != instanceID || rich.Environment != identity.Environment || rich.Digest != generation.PlanDigest {
			return "", fmt.Errorf("%w: refresh source plan identity does not match active generation", deployment.ErrDeliveryConflict)
		}
		if err := platformdigest.ValidateSHA256Identity(rich.SourceDigest); err != nil {
			return "", fmt.Errorf("%w: refresh source digest is invalid: %v", deployment.ErrDeliveryConflict, err)
		}
		return rich.SourceDigest, nil
	}
	// Runtime attach eligibility is control-plane evidence, not DuckLake
	// catalog metadata.  The checker must therefore read the canonical
	// DuckLake control ledger in leapview_control; the separately authenticated
	// DuckLake pool remains reserved for the actual catalog ATTACH.
	attachChecker := graph.DuckLakeControlLedger
	// BuildRuntime uses the analytics module's factory against the immutable
	// DuckLake environment opened by the sealed runtime factory. The dashboard
	// runtime implementation remains behind the app/runtimefactory seam.
	postgresFactory := appruntimefactory.NewPostgresSealedFactory(appruntimefactory.PostgresSealedFactoryConfig{
		Base:             appruntimefactory.FactoryConfig{DuckDBDir: cfg.DuckDBDirPath(), RuntimeDir: cfg.RuntimeDir(), SealedLeaseHolder: instanceID},
		ServingArtifacts: nativeProjectSource.Objects,
		Resolve:          appruntimefactory.NewPostgresSealedRootResolver(instanceID, graph.DeploymentRepository, graph.PhysicalPool), SnapshotLeases: graph.ServingState, RuntimeAttachChecker: attachChecker,
		LeaseHolder: instanceID, DuckLakeSecret: postgresDuckLakeSecret, PostgresSecret: postgresConnectionSecret, ExtensionAdmission: extensionSupply,
		CredentialBootstrapFactory: newPostgresDuckLakeCredentialBootstrapFactory(cfg, extensionSupply),
		Authorize: func(ctx context.Context, input appruntimefactory.PostgresServingAuthorizationInput) error {
			_, ok, err := readClaim(ctx)
			if err != nil {
				return err
			}
			if !ok || input.Root.GenerationID == "" {
				return errors.New("sealed serving authorization evidence is unavailable")
			}
			return nil
		},
		BuildRuntime: appruntimefactory.NewPostgresDashboardRuntimeBuilder(appruntimefactory.PostgresDashboardRuntimeConfig{Projects: analytics.ProjectRuntimeFactoryForEnvironment, MaxRows: cfg.QueryResultMaxRows, MaxBytes: cfg.QueryResultMaxBytes}),
	})
	runtimeHost, err = runtimehostmodule.Build(ctx, runtimehostmodule.Config{States: graph.ServingState, ProjectID: projectID, Environment: environment, ReadClaimedProject: readClaim, ManagedData: managedResolver, Authorization: accessBundle.AuthorizationInstaller, Factory: postgresFactory, RequireSealedCatalog: true, ResolveSealedActiveState: func(ctx context.Context) (servingstate.ID, error) {
		return resolvePostgresSealedActiveState(ctx, targetReader, instanceID)
	}})
	if err != nil {
		return fail(fmt.Errorf("build runtime host: %w", err))
	}
	// Approval authorization is deliberately late-bound to the active runtime
	// snapshot. The graph is constructed before the runtime host and therefore
	// starts fail-closed; install both identity and capability resolvers only
	// after the host has proved its serving generation.
	graph.ApprovalAuthorizer.SetResolvers(func(resolveCtx context.Context) (string, error) {
		project, err := currentProject(resolveCtx)
		if err != nil {
			return "", err
		}
		return project.String(), nil
	}, func(resolveCtx context.Context, principalID string) ([]access.Capability, error) {
		subjects, err := accessBundle.Module.AuthorizationSubjects(resolveCtx, principalID)
		if err != nil {
			return nil, err
		}
		lease, err := runtimeHost.Acquire(resolveCtx)
		if err != nil {
			return nil, err
		}
		defer lease.Release()
		authorizedLease, ok := lease.(interface {
			AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
		})
		if !ok {
			return nil, errors.New("active runtime lease does not expose authorization snapshot")
		}
		snapshot := authorizedLease.AuthorizationSnapshot()
		if err := snapshot.ValidateBound(); err != nil {
			return nil, err
		}
		return snapshot.EffectiveCapabilities(subjects)
	})
	graph.ApprovalAuthorizer.SetCandidateResolver(func(resolveCtx context.Context, generationID, principalID string) (string, string, []access.Capability, error) {
		return candidateApprovalCapabilities(resolveCtx, graph.ServingState, nativeProjectSource.Objects, accessBundle.Module.AuthorizationSubjects, generationID, principalID)
	})
	projectCatalogService, err := projectcatalog.NewService(projectCatalogLeaseProvider{provider: runtimeHost.Provider()}, projectCatalogSubjectResolver{resolve: accessBundle.Module.AuthorizationSubjects})
	if err != nil {
		return fail(err)
	}
	release.SetProjectSearchCatalog(projectCatalogService)
	if err := analytics.ConfigureActiveRuntimeBindings(activeConnectionEvidenceSource{releases: release, targetID: instanceID, environment: string(environment)}); err != nil {
		return fail(err)
	}
	authoring, err := dashboardmodule.BuildAuthoring(dashboardmodule.AuthoringConfig{Persistence: graph.DashboardPersistence, AuthorizeResource: func(ctx context.Context, principal string, project projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error) {
		return authorizeProjectResources(ctx, accessBundle.Module, runtimeHost, principal, project, []access.ResourceRef{resource}, capability)
	}, AuthorizeProjectCapability: func(ctx context.Context, principal string, project projectgraph.ResourceID, capability access.Capability) (bool, error) {
		return authorizeProjectRole(ctx, accessBundle.Module, runtimeHost, principal, project, capability)
	}, AcquireRuntime: runtimeHost.Acquire})
	if err != nil {
		return fail(err)
	}
	reconciler, err := NewNativeDashboardPublicationReconciler(NativeDashboardPublicationActivationConfig{Begin: bootstrap.RuntimePool(), Publications: graph.DashboardPublication, Project: graph.Project, Access: accessBundle.Module, GenerationFence: graph.DashboardGenerationFence})
	if err != nil {
		return fail(err)
	}
	deliveryStartup, err := newPostgresDeliveryStartupCheck(postgresDeliveryStartupCheckConfig{
		TargetID:    instanceID,
		Environment: environment,
		ReadClaim:   readClaim,
		Delivery:    appdeploymentpostgres.NewStartupReader(graph.DeploymentRepository),
		Serving:     graph.ServingState,
		Physical:    graph.PhysicalPool,
	})
	if err != nil {
		return fail(fmt.Errorf("build PostgreSQL delivery startup checker: %w", err))
	}

	// Candidate planning inspects durable, non-secret binding evidence. Physical
	// build opens validated credential leases only around materialization and
	// releases them before qualification or generation admission.
	candidateBindings, err := analytics.NewRuntimeBindingLeaser(analyticsmodule.RuntimeBindingLeaserConfig{
		Authorize: func(ctx context.Context, principalID string, binding analyticsmodule.ConnectionTargetBinding) error {
			resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
			if err != nil {
				return err
			}
			allowed, err := authorizeProjectResources(ctx, accessBundle.Module, runtimeHost, principalID, binding.Scope.ProjectID, []access.ResourceRef{resource}, access.CapabilityResourceUse)
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
			record: accessAuditRecorder(accessBundle.Module),
		},
	})
	if err != nil {
		return fail(fmt.Errorf("build native candidate binding authority: %w", err))
	}
	candidateConnections := candidateConnectionLeaser{leaser: candidateBindings, module: analytics}

	physicalPoolID := strings.TrimSpace(cfg.DeliveryPhysicalPoolID)
	compatibilityDigest := strings.TrimSpace(cfg.DeliveryPhysicalPoolCompatibilityDigest)
	contractAuthority, err := appdeploymentpostgres.NewNativeBuildContractAuthority(appdeploymentpostgres.NativeBuildContractAuthorityConfig{
		PhysicalPool: graph.PhysicalPool,
		Catalog:      graph.DuckLakeControlLedger,
		Runtime:      graph.DuckLakeControlLedger,
	})
	if err != nil {
		return fail(fmt.Errorf("build native delivery contract authority: %w", err))
	}
	resolveDuckLakeConfig := func(resolveCtx context.Context) (ducklake.Config, string, error) {
		contract, err := contractAuthority.Resolve(resolveCtx, appdeploymentpostgres.NativeBuildContractRequest{
			PhysicalPoolID: physicalPoolID, CompatibilityDigest: compatibilityDigest,
		})
		if err != nil {
			return ducklake.Config{}, "", err
		}
		credentialBootstrap, err := newPostgresDuckLakeCredentialBootstrap(cfg, contract.PoolContract, extensionSupply)
		if err != nil {
			return ducklake.Config{}, "", err
		}
		return ducklake.Config{
			RootDir:             cfg.RuntimeDir(),
			PhysicalPoolID:      contract.PhysicalPoolID,
			SharedPool:          true,
			Compatibility:       contract.PoolContract.Tuple,
			PoolContract:        contract.PoolContract,
			CredentialBootstrap: credentialBootstrap,
			ExtensionAdmission:  extensionSupply,
			MaxConnections:      cfg.WorkloadConfig().MaxRunning,
			MemoryMaxBytes:      cfg.DuckDBNodeMemoryMaxBytes,
			TempMaxBytes:        cfg.DuckDBNodeTempMaxBytes,
			MaxThreads:          cfg.DuckDBNodeMaxThreads,
			TempDir:             cfg.DuckDBTempDirPath(),
			PostgresCatalog: &ducklake.PostgresCatalogConfig{
				PhysicalPoolID: contract.PhysicalPoolID,
				DuckLakeSecret: postgresDuckLakeSecret,
				PostgresSecret: postgresConnectionSecret,
				MetadataSchema: ducklake.MetadataSchemaForPool(contract.PhysicalPoolID),
				Mode:           ducklake.PostgresCatalogWriter,
			},
		}, contract.Catalog.CatalogID, nil
	}
	buildOperations, ok := graph.DeploymentPersistence.Operations.(deploymentmodule.NativeBuildOperationAuthority)
	if !ok {
		return fail(errors.New("native delivery build operation authority is unavailable"))
	}
	attemptAdmission, err := appdeploymentpostgres.NewCandidateBuildAttemptAdmission(graph.DeploymentRepository, graph.DuckLakeControlLedger)
	if err != nil {
		return fail(err)
	}
	attemptTermination, err := appdeploymentpostgres.NewAttemptTermination(graph.DeploymentRepository, graph.DuckLakeControlLedger)
	if err != nil {
		return fail(err)
	}
	managedDataAdmission, err := appdeploymentpostgres.NewNativeManagedDataBindingAdmission(graph.ManagedDataRepository)
	if err != nil {
		return fail(err)
	}
	generationAdmission, err := appdeploymentpostgres.NewGenerationAdmission(graph.DeploymentRepository, graph.ServingState, graph.DuckLakeControlLedger, managedDataAdmission)
	if err != nil {
		return fail(err)
	}
	heartbeat, err := appdeploymentpostgres.NewNativeBuildHeartbeat(graph.DeploymentRepository, graph.DuckLakeControlLedger, buildOperations)
	if err != nil {
		return fail(err)
	}
	identity := buildinfo.Current()
	runtimeVersion := identity.Version + ":" + identity.Revision
	planCoordinator, err := appdeploymentpostgres.NewNativeCreatePlanCoordinator(appdeploymentpostgres.NativeCreatePlanConfig{
		Repository:      graph.DeploymentRepository,
		Sources:         nativeProjectSource.CandidateSourceReader,
		Artifacts:       release,
		BindingEvidence: candidateConnections,
		RuntimeVersion:  runtimeVersion,
		PolicyResolver: func(operation deployment.DeliveryOperationKind) (appruntimefactory.CandidateDeliveryPolicy, error) {
			return appruntimefactory.CandidateDeliveryPolicy{
				RequiresApproval:       requiresDeliveryApproval(true, cfg.EvaluationMode, operation),
				ApprovalPolicyRevision: appruntimefactory.CurrentApprovalPolicyRevision,
				RollbackClass:          deployment.DeliveryServingSafe,
				RetentionWindow:        cfg.DeliveryRollbackRetention().String(),
			}, nil
		},
		Events:     graph.DeploymentPersistence.Events,
		Audit:      graph.DeploymentPersistence.Audit,
		Workflow:   graph.DeploymentPersistence.Workflow,
		Operations: graph.DeploymentPersistence.Operations,
	})
	if err != nil {
		return fail(fmt.Errorf("build native delivery plan coordinator: %w", err))
	}
	physicalFactory := appdeploymentpostgres.NativePhysicalBuildEnvironmentFactoryFunc(func(openCtx context.Context, marker analyticsmodule.CommitMarker) (appdeploymentpostgres.NativePhysicalBuildEnvironment, error) {
		duckLakeConfig, catalogID, err := resolveDuckLakeConfig(openCtx)
		if err != nil {
			return nil, err
		}
		return appdeploymentpostgres.DuckLakePhysicalBuildEnvironmentFactory{
			Config: duckLakeConfig, CatalogID: catalogID,
			MaterializerFactory: func(environment *ducklake.Environment) (analyticsmodule.MaterializationExecutor, error) {
				return analytics.ProjectMaterializerForEnvironment(environment)
			},
		}.Open(openCtx, marker)
	})
	qualificationFactory := appdeploymentpostgres.NativeQualificationEnvironmentFactoryFunc(func(openCtx context.Context, request appdeploymentpostgres.NativeQualificationOpenRequest) (appdeploymentpostgres.NativeQualificationEnvironment, error) {
		duckLakeConfig, catalogID, err := resolveDuckLakeConfig(openCtx)
		if err != nil {
			return nil, err
		}
		return appdeploymentpostgres.DuckLakeNativeQualificationEnvironmentFactory{
			Config: duckLakeConfig, CatalogID: catalogID, CompatibilityAuthority: graph.DuckLakeControlLedger,
		}.Open(openCtx, request)
	})
	markerFactory := appdeploymentpostgres.NativePhysicalMarkerResolverFactoryFunc(func(openCtx context.Context) (appdeploymentpostgres.NativePhysicalMarkerResolver, error) {
		duckLakeConfig, _, err := resolveDuckLakeConfig(openCtx)
		if err != nil {
			return nil, err
		}
		return appdeploymentpostgres.DuckLakePhysicalMarkerResolverFactory{Config: duckLakeConfig}.OpenReadOnly(openCtx)
	})
	buildCoordinator, err := appdeploymentpostgres.NewNativeBuildCoordinator(appdeploymentpostgres.NativeBuildConfig{
		Repository:            graph.DeploymentRepository,
		Sources:               nativeProjectSource.CandidateSourceReader,
		Artifacts:             release,
		ArtifactRecovery:      release,
		BindingEvidence:       candidateConnections,
		Connections:           candidateConnections,
		ManagedData:           managedData.RuntimeResolution(),
		ContractAuthority:     contractAuthority,
		PhysicalPoolID:        physicalPoolID,
		CompatibilityDigest:   compatibilityDigest,
		Operations:            buildOperations,
		Heartbeat:             heartbeat,
		AttemptAdmission:      attemptAdmission,
		AttemptTermination:    attemptTermination,
		GenerationAdmission:   generationAdmission,
		PhysicalFactory:       physicalFactory,
		ObservationWriter:     graph.DuckLakeControlLedger,
		MarkerResolverFactory: markerFactory,
		ObservationReader:     graph.DuckLakeControlLedger,
		SnapshotFactory:       appdeploymentpostgres.NativeQualificationSnapshotInspectorFactory{QualificationFactory: qualificationFactory},
		QualificationFactory:  qualificationFactory,
		RuntimeVersion:        runtimeVersion,
		Bounds:                gates.Bounds{MaxRows: 10000, MaxQueries: 128, MaxMillis: 5000},
		Events:                graph.DeploymentPersistence.Events,
		Audit:                 graph.DeploymentPersistence.Audit,
		Workflow:              graph.DeploymentPersistence.Workflow,
	})
	if err != nil {
		return fail(fmt.Errorf("build native delivery build coordinator: %w", err))
	}
	nativeDelivery, err := appdeploymentpostgres.NewNativeDeliveryCoordinator(planCoordinator, buildCoordinator)
	if err != nil {
		return fail(fmt.Errorf("build native delivery coordinator: %w", err))
	}
	nativeDeliveryReader := appdeploymentpostgres.NewNativeReader(graph.DeploymentRepository)
	nativeRefreshExecutor, err := apprefreshpostgres.NewPostgresNativeRefreshExecutor(nativeDelivery, nativeDeliveryReader, instanceID)
	if err != nil {
		return fail(fmt.Errorf("build native refresh executor: %w", err))
	}

	// The PostgreSQL module receives only the clean-slate native mutation port.
	// Legacy delivery projection and candidate-builder paths remain absent.
	// Publication/activation remain owned by refresh persistence's native
	// finalizer; the executor stops after the sealed native build evidence.
	deploymentConfig := deploymentmodule.Config{
		Persistence: graph.DeploymentPersistence, Production: true, Protected: true,
		InstanceID: instanceID, InstanceEnvironment: string(environment),
		CanonicalOrigin:             publicURL,
		CandidateConnections:        candidateConnections,
		CandidateRuntime:            runtimeHost,
		CandidateArtifactRecovery:   release,
		CandidateRuntimeLifecycle:   runtimeHost,
		CandidateAdmission:          candidatePreparationAdmitter(workloadBundle.Controller, workloadmodule.ControlRequest("candidate.prepare")),
		NativeMetadataSchemaForPool: ducklake.MetadataSchemaForPool,
		RuntimeVersion:              runtimeVersion,
		NativeDeliveryMutations:     nativeDelivery,
		NativeDeliveryReader:        nativeDeliveryReader,
		ProjectClaims:               graph.DeploymentRepository,
		CandidateSources:            nativeProjectSource.CandidateSourceReader,
		BindClaimedProject:          bindClaimedProject(runtimeHost, environment),
		CurrentApprovalActor: func(r *http.Request) (deploymentmodule.ApprovalActor, bool) {
			evidence, ok := accessBundle.Module.CurrentCredentialEvidence(r)
			if !ok {
				return deploymentmodule.ApprovalActor{}, false
			}
			return deploymentmodule.ApprovalActor{PrincipalID: evidence.PrincipalID, CredentialClass: deploymentmodule.CredentialClass(evidence.Class), CredentialID: evidence.ID, CredentialExpiresAt: evidence.ExpiresAt}, true
		},
	}
	routes, runtimeServices, platform, policy, err := buildApplicationSurfaces(ctx, dashboardmodule.NewRuntimeMetrics(dashboardmodule.RuntimeMetricsOptions{Provider: runtimeHost.Provider(), ProjectID: projectID, PublishedCompilationReader: authoring.PublishedCompilationReader()}), dataAssemblyInputs{PlatformHealth: bootstrap.RuntimePool(), ServingStateRepo: graph.ServingState, AccessRepo: accessBundle.Repository, APIIdempotency: graph.Idempotency, CursorSigning: graph.CursorSigning, BypassDurableIdempotency: map[string]struct{}{refreshmodule.CreateRefreshRunOperationID: {}}, DashboardPublicationReconciler: reconciler, DashboardPersistence: graph.DashboardPersistence, RefreshPersistence: &refreshPersistence, RequireNativeDashboard: true, RequireExplicitAPIProtocol: true}, capabilityAssemblyInputs{ReleaseModule: release, JobModule: workloadBundle.Jobs, AgentPersistence: graph.AgentPersistence, AccessModule: accessBundle.Module, ManagedDataModule: managedData, AnalyticsModule: analytics, Authoring: authoring, DashboardAssets: dashboardAssets, Product: product, ProductStatus: productAdministrationStatus(cfg, instanceID, publicURL, string(environment), buildinfo.Current()), ProjectCatalog: projectCatalogService, ProjectGraph: projectmodule.NewActiveServingStateGraphReader(runtimeHost.Provider(), graph.ServingState)}, workflowAssemblyInputs{AgentSettings: graph.Settings, AgentConfig: agentmodule.ModelConfig{APIKey: cfg.AgentAPIKey, BaseURL: cfg.AgentBaseURL, Model: cfg.AgentModel}, Auth: accessBundle.Module.Auth(), Reloader: runtimeHost, Workload: workloadBundle.Controller, ManagedDataValidation: managedData.BindingValidation(), ManagedDataResolver: managedResolver, DeploymentConfig: deploymentConfig, ServingArtifacts: nativeProjectSource.Objects, RefreshPipelineClock: refreshmodule.NewRealClock(), EnableRefreshDispatcher: true, RefreshTargetRevision: resolveRefreshTargetRevision, RefreshSourceDigest: resolveRefreshSourceDigest, CanonicalRefreshExecutor: nativeRefreshExecutor.Execute, PublishedVersion: appdeploymentpostgres.NewNativePublishedDataVersionResolver(nativeDeliveryReader, instanceID)}, runtimeAssemblyInputs{RuntimeHost: runtimeHost, Production: true, DeliveryTargetReader: targetReader, ProjectID: projectID, ProjectIDResolver: currentProject, ServingSnapshotResolver: func(ctx context.Context) (string, error) {
		lease, err := runtimeHost.Acquire(ctx)
		if err != nil {
			return "", err
		}
		defer lease.Release()
		return lease.Identity().GenerationID, nil
	}, DuckLakeCatalogPath: "", DuckLakeDataPath: "", DefaultEnvironment: string(environment), Assets: assets, InstanceID: instanceID, AllowedHosts: allowedHosts, RequireActiveDeployment: false, RequireQueryAuthorization: true, SealedServing: true, DeliveryStartup: deliveryStartup}, httpAssemblyInputs{PublicURL: publicURL, RateLimits: apihttpmiddleware.ProductionRateLimitConfig(), SecurityHeaders: apihttpmiddleware.SecurityHeaders(true), RequestLogging: cfg.RequestLoggingEnabled(), Logger: slog.Default(), JobLeaseTimeout: cfg.RefreshJobLeaseTimeout, ManagedDataTus: managedData.TusHandler()})
	if err != nil {
		return fail(err)
	}
	handler := Routes(routes, runtimeServices, platform, policy)

	// Start/stop ordering is explicit: the bootstrap wrapper starts first and
	// owns startup-failure cleanup; resource closure follows runtimehost and
	// workers, and the pools close last.
	runtimeLifecycle := newRuntimeLifecycle(platform.workers, analytics, workloadBundle.Controller)
	resourceLifecycle := newPostgresResourceLifecycle(analytics, workloadBundle.Controller)
	runtimeHostLifecycle := newRuntimeHostLifecycle(runtimeHost)
	bootstrapLifecycle := newPostgresBootstrapLifecycle(bootstrap)
	bootstrapLifecycle.onStartFailure = func() error {
		return errors.Join(closeRuntimeHost(), closeResources())
	}
	components := []Lifecycle{bootstrapLifecycle, resourceLifecycle, runtimeHostLifecycle, runtimeLifecycle}
	return newApplication(handler, components), nil
}

// postgresResourceLifecycle owns capability resources that are safe to start
// during composition but must close after workers and runtimehost have
// drained. Start is intentionally a no-op because construction already
// completed all resource initialization.
type postgresResourceLifecycle struct {
	analytics postgresAnalyticsCloser
	workloads workloadControl
	stop      sync.Once
	err       error
}

type postgresAnalyticsCloser interface {
	Close() error
}

func newPostgresResourceLifecycle(analytics postgresAnalyticsCloser, workloads workloadControl) *postgresResourceLifecycle {
	return &postgresResourceLifecycle{analytics: analytics, workloads: workloads}
}

func (l *postgresResourceLifecycle) Start(context.Context) error { return nil }

func (l *postgresResourceLifecycle) Stop(_ context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.workloads != nil {
			l.workloads.Close()
		}
		if l.analytics != nil {
			l.err = l.analytics.Close()
		}
	})
	return l.err
}

// postgresBootstrapLifecycle wraps the pool owner so a failed startup ping
// cannot leak serving pools: Application only marks a component started after
// Start succeeds and therefore would otherwise skip its Stop method.
type postgresBootstrapLifecycle struct {
	owner          Lifecycle
	onStartFailure func() error
	stop           sync.Once
	stopErr        error
}

func newPostgresBootstrapLifecycle(owner Lifecycle) *postgresBootstrapLifecycle {
	return &postgresBootstrapLifecycle{owner: owner}
}

func (l *postgresBootstrapLifecycle) Start(ctx context.Context) error {
	if l == nil || l.owner == nil {
		return errors.New("PostgreSQL bootstrap lifecycle is not initialized")
	}
	if err := l.owner.Start(ctx); err != nil {
		cleanupErr := error(nil)
		if l.onStartFailure != nil {
			cleanupErr = l.onStartFailure()
		}
		return errors.Join(err, cleanupErr, l.Stop(context.Background()))
	}
	return nil
}

func (l *postgresBootstrapLifecycle) Stop(ctx context.Context) error {
	if l == nil || l.owner == nil {
		return nil
	}
	l.stop.Do(func() {
		l.stopErr = l.owner.Stop(ctx)
	})
	return l.stopErr
}
