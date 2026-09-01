package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type Module struct {
	handler                   *deploymenthttp.Handler
	candidates                *deployment.CandidateService
	approvals                 *deployment.ApprovalService
	candidateRuntimes         CandidateRuntimePreparer
	candidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	candidateSources          deployment.CandidateSourceSynchronizer
	candidateSourceAudit      func(context.Context, CandidateSourceAuditEvent) error
	candidateSourceBlobAudit  func(context.Context, CandidateSourceAuditEvent) error
	candidateArtifacts        release.CandidateArtifactPreparer
	candidateAdmission        CandidatePreparationAdmitter
	deliveryCandidateBuilder  func(context.Context, deployment.DeliveryCandidateBuildInput) (deployment.Candidate, error)
	logger                    *slog.Logger
	jobs                      JobConfig
	api                       APIConfig
	instanceID                string
	executions                map[string]apigencommand.AsyncExecutionContract
	protected                 bool
	auditIntentConfigured     bool
	currentApprovalActor      func(*http.Request) (deployment.ApprovalActor, bool)
	authorizeApproval         func(context.Context, deployment.ApprovalActor, string, string) error
	authorizeActivation       func(context.Context, deployment.ApprovalActor, string, string) error
	bootstrapPolicies         BootstrapPolicyStore
	authorizeBootstrap        func(context.Context, deployment.BootstrapActivationPolicy) error
	sealedCoordinator         SealedCoordinator
	sealedPublishRequest      SealedPublishRequestResolver
	sealedRollbackRequest     SealedRollbackRequestResolver
	sealedActivationMarker    SealedActivationMarker
	sealedReconcile           func(context.Context, string) error
	sealedRollbackFence       func(context.Context, string) (string, int64, error)
	requireSealedCoordinator  bool
	deliveryReader            deployment.DeliveryReader
	nativeDeliveryReader      NativeDeliveryReader
	deliveryMutations         DeliveryMutationPort
	nativeDeliveryMutations   NativeDeliveryMutationPort
	nativeDeliveryPublication NativeDeliveryPublicationPort
	nativeDeliveryApproval    NativeDeliveryApprovalPort
	persistence               *Persistence
}

type Principal struct {
	ID string
}

type Candidate = deployment.Candidate
type CandidateStatus = deployment.CandidateStatus
type CandidateEvent = deployment.CandidateEvent
type CandidateConnectionRequest = deployment.CandidateConnectionRequest
type CandidateConnectionEvidence = deployment.CandidateConnectionEvidence
type CandidateConnectionLeases = deployment.CandidateConnectionLeases
type CandidateConnectionEvidenceResolver = deployment.CandidateConnectionEvidenceResolver
type CandidateRuntimeRequest = deployment.CandidateRuntimeRequest
type CandidateGenerationRuntime = deployment.CandidateGenerationRuntime
type CandidateConnectionRequirement = deployment.CandidateConnectionRequirement
type CandidateAuthoredConnection = deployment.CandidateAuthoredConnection
type CandidateRestriction = deployment.CandidateRestriction
type CandidateDataMode = deployment.CandidateDataMode

type CandidateRuntimePreparer interface {
	Prepare(
		context.Context,
		deployment.CandidateRuntimeRequest,
	) (deployment.CandidateRuntimeReceipt, error)
}

type CandidatePreparationLease interface {
	Context() context.Context
	Release()
}

type CandidatePreparationAdmitter interface {
	AcquireCandidatePreparation(context.Context) (CandidatePreparationLease, error)
}

type CandidatePreparationAdmitterFunc func(
	context.Context,
) (CandidatePreparationLease, error)

func (admit CandidatePreparationAdmitterFunc) AcquireCandidatePreparation(
	ctx context.Context,
) (CandidatePreparationLease, error) {
	return admit(ctx)
}

// BootstrapPolicyStore is the module-owned contract for the durable,
// one-shot first-activation policy. The persistence adapter remains private to
// this module; composition may provide a store without depending on SQLite.
type BootstrapPolicyStore interface {
	ArmBootstrapActivation(context.Context, deployment.BootstrapActivationPolicy) (deployment.BootstrapActivationPolicy, error)
	BootstrapActivationPolicy(context.Context, string) (deployment.BootstrapActivationPolicy, error)
}

// ProjectClaimReader is the read-only module contract used to bind process
// startup and bootstrap authorization to the durable instance claim.
type ProjectClaimReader interface {
	GetProjectClaim(context.Context) (deployment.ProjectClaim, error)
}

// BootstrapPersistence combines the bootstrap policy and project-claim ports
// exposed by the module-owned persistence factory.
type BootstrapPersistence interface {
	BootstrapPolicyStore
	ProjectClaimReader
}

// CandidateSourceAuditEvent is the transport-neutral audit record emitted
// after an immutable candidate source or source blob has been accepted.
// Action and Capability are copied from the generated command contract by the
// module. SourceAttestationDigest is populated for retained source snapshots.
type CandidateSourceAuditEvent struct {
	PrincipalID             string
	ProjectID               projectgraph.ResourceID
	Digest                  string
	SourceAttestationDigest string
	Action                  string
	Capability              access.Capability
	Status                  string
	RequestID               string
	CorrelationID           string
	MetadataJSON            string
}

const (
	CandidatePreparing          = deployment.CandidatePreparing
	CandidateReady              = deployment.CandidateReady
	CandidateFailed             = deployment.CandidateFailed
	CandidateCancelled          = deployment.CandidateCancelled
	CandidateExpired            = deployment.CandidateExpired
	CandidateDataReuseBase      = deployment.CandidateDataReuseBase
	CandidateDataRefreshSources = deployment.CandidateDataRefreshSources
)

var (
	ErrCandidateNotFound    = deployment.ErrCandidateNotFound
	ErrCandidateUnavailable = deployment.ErrCandidateUnavailable
)

type ServingStatePort interface {
	deployment.ServingStateRepository
}

// SealedCoordinator contains only durable publication and rollback operations.
// Catalog/seal lookup remains in the resolver callbacks so this HTTP module
// never receives object-store credentials or paths.
type SealedCoordinator interface {
	Publish(context.Context, sealedcontrol.PublishRequest) (deployment.PublicationIntent, error)
	Rollback(context.Context, sealedcontrol.RollbackRequest) (deployment.RollbackResult, error)
}

type SealedPublishRequestResolver func(context.Context, apiadapter.Deployment, string, deployment.ApprovalActor, bool) (sealedcontrol.PublishRequest, error)
type SealedRollbackRequestResolver func(context.Context, apiadapter.Deployment, string, deployment.ApprovalActor, string, int64) (sealedcontrol.RollbackRequest, error)
type SealedActivationMarker func(context.Context, deployment.ActivationInput) (deployment.Deployment, error)

type Config struct {
	// Persistence is the native clean-slate PostgreSQL delivery authority.
	// Production callers construct it with NewPostgresPersistence; local and
	// evaluation callers use NewSQLitePersistence. The module never infers an
	// adapter from a raw database handle.
	Persistence *Persistence
	Production  bool
	// AuditIntentRecorder is the Access-owned transaction-scoped outbox port.
	// It is required whenever deployment SQLite persistence is configured.
	AuditIntentRecorder       access.AuditIntentRecorder
	States                    ServingStatePort
	Runtime                   deployment.Runtime
	ManagedData               deployment.ManagedDataResolver
	MaxJSONBodyBytes          int64
	Logger                    *slog.Logger
	InstanceID                string
	CanonicalOrigin           string
	InstanceEnvironment       string
	CandidateLifetime         time.Duration
	ApprovalLifetime          time.Duration
	MaxCandidatesPerOwner     int
	CandidateAudit            func(context.Context, deployment.CandidateEvent) error
	CandidateSourceAudit      func(context.Context, CandidateSourceAuditEvent) error
	CandidateSourceBlobAudit  func(context.Context, CandidateSourceAuditEvent) error
	CandidateConnections      deployment.CandidateConnectionLeaser
	CandidateRuntime          deployment.CandidateRuntimeHost
	CandidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	CandidateSources          deployment.CandidateSourceSynchronizer
	CandidateArtifacts        release.CandidateArtifactPreparer
	CandidateAdmission        CandidatePreparationAdmitter
	// DeliveryCandidateBuilder is the canonical plan -> build -> seal adapter.
	// When configured, candidate synchronization delegates to it after the
	// immutable source snapshot is committed. Production composition sets
	// RequireCanonicalDelivery so an omitted adapter fails closed.
	DeliveryCandidateBuilder func(context.Context, deployment.DeliveryCandidateBuildInput) (deployment.Candidate, error)
	CanonicalDeliveryAdapter *CanonicalDeliveryAdapter
	// RequireCanonicalDelivery makes production composition fail closed when
	// the plan-driven adapter is missing. Development compatibility can leave
	// this false until its target-owned adapter is wired.
	RequireCanonicalDelivery bool
	// BindClaimedProject binds the process runtime to the durable instance
	// claim after candidate start commits.
	BindClaimedProject   func(context.Context, projectgraph.ResourceID, servingstate.Environment) error
	RuntimeVersion       string
	CurrentPrincipal     func(*http.Request) (Principal, bool)
	CurrentApprovalActor func(*http.Request) (deployment.ApprovalActor, bool)
	AuthorizeApproval    func(context.Context, deployment.ApprovalActor, string, string) error
	AuthorizeActivation  func(context.Context, deployment.ApprovalActor, string, string) error
	// BootstrapPolicies is the durable one-shot first-activation policy store.
	// It is intentionally separate from approvals and active-generation
	// snapshots; composition supplies the access-role/credential and
	// no-active-generation revalidator.
	BootstrapPolicies  BootstrapPolicyStore
	AuthorizeBootstrap func(context.Context, deployment.BootstrapActivationPolicy) error
	// AfterActivated runs after runtime publication and durable activation.
	// It is observational and cannot influence activation.
	AfterActivated           func(context.Context, deployment.Deployment)
	Protected                bool
	Jobs                     JobConfig
	API                      APIConfig
	PublicationAuthorization PublicationAuthorizationConfig
	SealedCoordinator        SealedCoordinator
	SealedPublishRequest     SealedPublishRequestResolver
	SealedRollbackRequest    SealedRollbackRequestResolver
	SealedActivationMarker   SealedActivationMarker
	SealedReconcile          func(context.Context, string) error
	SealedRollbackFence      func(context.Context, string) (string, int64, error)
	RequireSealedCoordinator bool
	// DeliveryMutations owns the canonical plan -> build -> publish/rollback
	// use cases. It is deliberately a narrow callback port so HTTP/CLI cannot
	// bypass target admission, sealing, or the authoritative CAS fence.
	DeliveryMutations DeliveryMutationPort
	// NativeDeliveryMutations is the clean-slate PostgreSQL plan/build port.
	// Native production composition must inject this port; it is deliberately
	// separate from DeliveryMutations so the HTTP plan/build handlers cannot
	// fall back to the legacy DeliveryLifecycle or text-ID contracts.
	NativeDeliveryMutations NativeDeliveryMutationPort
	// NativeDeliveryPublication is the clean-slate PostgreSQL publication and
	// rollback request port. It is intentionally separate from plan/build so
	// those authorities cannot activate or publish inline.
	NativeDeliveryPublication NativeDeliveryPublicationPort
	// NativeDeliveryApproval owns publication-scoped request and decision
	// transitions. Production must provide it whenever native persistence is
	// enabled; no candidate-wide approval fallback is permitted.
	NativeDeliveryApproval NativeDeliveryApprovalPort
	// DeliveryReader is the durable, read-only plan/build/seal/operator port for
	// explicit local/evaluation persistence. Production uses NativeDeliveryReader.
	DeliveryReader deployment.DeliveryReader
	// NativeDeliveryReader is the clean-slate PostgreSQL read port. It is
	// deliberately distinct from DeliveryReader so production handlers cannot
	// silently fall back to SQLite-shaped projections.
	NativeDeliveryReader NativeDeliveryReader
	// Native source-mutation capabilities are strict transaction-bound ports.
	// Production PostgreSQL composition must provide all four; no SQLite or
	// cross-connection fallback is permitted.
	NativeDeliveryEvents     NativeDeliveryEventAppender
	NativeDeliveryAudit      NativeDeliveryAuditAppender
	NativeDeliveryWorkflow   NativeDeliveryWorkflowRecorder
	NativeOperationAuthority NativeOperationAuthority
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("deployment persistence is required; choose an explicit PostgreSQL or SQLite persistence bundle")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	if config.Production {
		if !config.Persistence.isPostgres() {
			return nil, errors.New("production deployment module requires native PostgreSQL persistence")
		}
	}
	if config.Persistence.isPostgres() && !config.Production {
		return nil, errors.New("native PostgreSQL persistence requires production deployment mode")
	}
	if config.Production && config.Persistence.isPostgres() {
		if config.NativeDeliveryEvents == nil {
			config.NativeDeliveryEvents = config.Persistence.Events
		}
		if config.NativeDeliveryAudit == nil {
			config.NativeDeliveryAudit = config.Persistence.Audit
		}
		if config.NativeDeliveryWorkflow == nil {
			config.NativeDeliveryWorkflow = config.Persistence.Workflow
		}
		if config.NativeOperationAuthority == nil {
			config.NativeOperationAuthority = config.Persistence.Operations
		}
		if config.NativeDeliveryApproval == nil && config.Persistence.Approval != nil {
			var approvalPort NativeDeliveryApprovalPort
			var approvalErr error
			approvalPort, approvalErr = newNativeApprovalCoordinator(config.Persistence.Repository, config.Persistence.Approval, config.InstanceID, config.InstanceEnvironment)
			if approvalErr != nil {
				return nil, approvalErr
			}
			config.NativeDeliveryApproval = approvalPort
		}
		if config.NativeDeliveryEvents == nil || config.NativeDeliveryAudit == nil || config.NativeDeliveryWorkflow == nil || config.NativeOperationAuthority == nil {
			return nil, errors.New("production native deployment requires delivery event, audit, workflow, and operation authorities")
		}
		if config.NativeDeliveryApproval == nil {
			return nil, errors.New("production native deployment requires publication approval authority")
		}
		if config.NativeDeliveryReader == nil {
			// The validated persistence bundle owns the native reader. Keep this
			// fallback for callers that pass the complete bundle directly while
			// still rejecting an incomplete bundle below.
			config.NativeDeliveryReader = config.Persistence.DeliveryReader
		}
		if config.NativeDeliveryReader == nil {
			return nil, errors.New("production native deployment requires a delivery reader")
		}
	}
	if config.DeliveryCandidateBuilder == nil && config.CanonicalDeliveryAdapter != nil {
		config.DeliveryCandidateBuilder = config.CanonicalDeliveryAdapter.CandidateDeliveryBuilder()
	}
	if config.RequireCanonicalDelivery && config.DeliveryCandidateBuilder == nil {
		return nil, fmt.Errorf("canonical delivery lifecycle is required")
	}
	if config.RequireSealedCoordinator && (config.SealedCoordinator == nil || config.SealedPublishRequest == nil || config.SealedRollbackRequest == nil || config.SealedReconcile == nil || config.SealedRollbackFence == nil) {
		return nil, fmt.Errorf("sealed publication coordinator and durable request resolvers are required")
	}
	executions, err := loadDeploymentExecutionContracts()
	if err != nil {
		return nil, err
	}
	options := deploymenthttp.Options{MaxJSONBodyBytes: config.MaxJSONBodyBytes}
	options.CurrentPrincipal = func(r *http.Request) (deploymenthttp.Principal, bool) {
		if config.CurrentPrincipal == nil {
			return deploymenthttp.Principal{}, false
		}
		principal, ok := config.CurrentPrincipal(r)
		return deploymenthttp.Principal{ID: principal.ID}, ok
	}
	var coordinator deploymenthttp.Coordinator
	var candidates *deployment.CandidateService
	var approvals *deployment.ApprovalService
	var candidateRuntimes *deployment.CandidateRuntimeService
	var durableBootstrapPolicies BootstrapPolicyStore
	if config.Persistence.isPostgres() {
		// Native production HTTP requests are coordinated directly against the
		// canonical PostgreSQL delivery authority. No legacy service or SQLite
		// adapter is introduced on this path.
		var coordinatorErr error
		coordinator, coordinatorErr = newNativeCoordinator(config.Persistence.Repository, config.InstanceID, config.InstanceEnvironment, nativeCoordinatorCapabilities{
			events: config.NativeDeliveryEvents, audit: config.NativeDeliveryAudit, workflow: config.NativeDeliveryWorkflow, operations: config.NativeOperationAuthority,
		})
		if coordinatorErr != nil {
			return nil, coordinatorErr
		}
		// The native coordinator owns pending publish/rollback creation as a
		// separate authority from plan/build. Keep an explicit caller override
		// possible, while ensuring production PostgreSQL composition cannot
		// accidentally fall back to the legacy mutation port.
		if config.NativeDeliveryPublication == nil {
			if publicationPort, ok := coordinator.(NativeDeliveryPublicationPort); ok {
				config.NativeDeliveryPublication = publicationPort
			}
		}
	} else {
		database := config.Persistence.legacyDatabase
		if !config.Persistence.isSQLite() || database == nil {
			return nil, errors.New("deployment persistence backend is unavailable")
		}
		audit := config.AuditIntentRecorder
		if audit == nil {
			audit = config.Persistence.legacyAudit
		}
		if audit == nil {
			return nil, errors.New("deployment audit intent recorder is required")
		}
		if config.States == nil || config.Runtime == nil || config.ManagedData == nil {
			return nil, errors.New("deployment states, runtime, and managed data are required")
		}
		if config.BindClaimedProject == nil {
			return nil, errors.New("candidate project claim binder is required")
		}
		if config.API.Workflow != nil {
			ownedCommitter := workflowAuditCommitter{
				database: database,
				workflow: config.API.Workflow,
				audit:    audit,
			}
			config.API.Committer = ownedCommitter
			config.API.AuditedCommitter = ownedCommitter
		} else if config.API.AuditedCommitter == nil {
			if audited, ok := config.API.Committer.(AuditedWorkflowCommitter); ok {
				config.API.AuditedCommitter = audited
			} else {
				return nil, errors.New("deployment audited workflow committer is required")
			}
		}
		if config.API.Committer == nil {
			config.API.Committer = config.API.AuditedCommitter
		}
		repository := config.Persistence.legacyRepository
		activation := config.Persistence.legacyActivation
		candidateRepository := config.Persistence.legacyCandidates
		approvalRepository := config.Persistence.legacyApprovals
		if config.DeliveryReader == nil {
			if reader, ok := repository.(deployment.DeliveryReader); ok {
				config.DeliveryReader = reader
			}
		}
		if config.SealedActivationMarker == nil {
			if marker, ok := repository.(interface {
				ActivateSealedDeployment(context.Context, deployment.ActivationInput) (deployment.Deployment, error)
			}); ok {
				config.SealedActivationMarker = marker.ActivateSealedDeployment
			}
		}
		if policyStore, ok := repository.(BootstrapPolicyStore); ok {
			durableBootstrapPolicies = policyStore
		}
		service, err := deployment.New(repository, activation, config.States, config.Runtime, config.ManagedData)
		if err != nil {
			return nil, err
		}
		service.SetAfterActivated(config.AfterActivated)
		if config.CandidateConnections != nil || config.CandidateRuntime != nil {
			if config.CandidateAdmission == nil {
				return nil, errors.New(
					"candidate runtime preparation workload admission is required",
				)
			}
			candidateRuntimes, err = deployment.NewCandidateRuntimeService(
				deployment.CandidateRuntimeServiceConfig{
					Connections:    config.CandidateConnections,
					Runtime:        config.CandidateRuntime,
					RuntimeVersion: config.RuntimeVersion,
				},
			)
			if err != nil {
				return nil, err
			}
		}
		coordinator, err = apiadapter.New(service)
		if err != nil {
			return nil, err
		}
		if err := requireCandidateAuditSink(config.CandidateAudit); err != nil {
			return nil, err
		}
		candidates, err = deployment.NewCandidateService(candidateRepository, deployment.CandidateServiceConfig{
			TargetID: config.InstanceID, CanonicalOrigin: config.CanonicalOrigin,
			Environment: config.InstanceEnvironment, Lifetime: config.CandidateLifetime,
			MaxActivePerOwner: config.MaxCandidatesPerOwner, Audit: config.CandidateAudit,
			Logger:           config.Logger,
			RuntimeLifecycle: config.CandidateRuntimeLifecycle,
			BindProject:      config.BindClaimedProject,
		})
		if err != nil {
			return nil, err
		}
		if config.DeliveryMutations == nil && config.CanonicalDeliveryAdapter != nil && config.CandidateSources != nil {
			config.DeliveryMutations = &CanonicalDeliveryMutations{
				Lifecycle: config.CanonicalDeliveryAdapter.Lifecycle,
				Sources:   config.CandidateSources, Artifacts: config.CandidateArtifacts,
				Admission: config.CandidateAdmission,
				Plan:      config.CanonicalDeliveryAdapter.Plan, PlanPreview: config.CanonicalDeliveryAdapter.PlanPreview, BuildRequest: config.CanonicalDeliveryAdapter.BuildRequest,
				Adapter: config.CanonicalDeliveryAdapter, Publish: config.CanonicalDeliveryAdapter.Publish, Rollback: config.CanonicalDeliveryAdapter.Rollback,
			}
		}
		approvals, err = deployment.NewApprovalService(
			approvalRepository,
			deployment.ApprovalServiceConfig{
				Lifetime: config.ApprovalLifetime,
			},
		)
		if err != nil {
			return nil, err
		}
	}
	options.Coordinator = coordinator
	options.Logger = config.Logger
	options.InstanceEnvironment = config.InstanceEnvironment
	jobs := config.Jobs
	if jobs.Coordinator == nil {
		jobs.Coordinator = coordinator
	}
	m := &Module{
		handler: deploymenthttp.NewHandler(options), candidates: candidates,
		approvals:         approvals,
		candidateRuntimes: candidateRuntimes, candidateRuntimeLifecycle: config.CandidateRuntimeLifecycle, candidateSources: config.CandidateSources,
		candidateArtifacts:       config.CandidateArtifacts,
		candidateAdmission:       config.CandidateAdmission,
		deliveryCandidateBuilder: config.DeliveryCandidateBuilder,
		candidateSourceAudit:     config.CandidateSourceAudit,
		candidateSourceBlobAudit: config.CandidateSourceBlobAudit,
		logger:                   config.Logger, auditIntentConfigured: config.Persistence.isSQLite() && func() bool {
			if config.AuditIntentRecorder != nil {
				return true
			}
			return config.Persistence.legacyAudit != nil
		}(),
		jobs: jobs, api: config.API, protected: config.Protected,
		instanceID: config.InstanceID, executions: executions,
		currentApprovalActor: config.CurrentApprovalActor,
		authorizeApproval:    config.AuthorizeApproval,
		authorizeActivation:  config.AuthorizeActivation,
		bootstrapPolicies:    config.BootstrapPolicies, authorizeBootstrap: config.AuthorizeBootstrap,
		sealedCoordinator: config.SealedCoordinator, sealedPublishRequest: config.SealedPublishRequest,
		sealedRollbackRequest: config.SealedRollbackRequest, sealedActivationMarker: config.SealedActivationMarker,
		sealedReconcile: config.SealedReconcile, sealedRollbackFence: config.SealedRollbackFence,
		requireSealedCoordinator: config.RequireSealedCoordinator, deliveryReader: config.DeliveryReader,
		nativeDeliveryReader: config.NativeDeliveryReader,
		deliveryMutations:    config.DeliveryMutations, nativeDeliveryMutations: config.NativeDeliveryMutations, nativeDeliveryPublication: config.NativeDeliveryPublication,
		nativeDeliveryApproval: config.NativeDeliveryApproval,
		persistence: func() *Persistence {
			if config.Persistence.isPostgres() {
				return config.Persistence
			}
			return nil
		}(),
	}
	if m.bootstrapPolicies == nil {
		m.bootstrapPolicies = durableBootstrapPolicies
	}
	if m.logger == nil {
		m.logger = slog.Default()
	}
	if m.jobs.Authorize == nil {
		m.jobs.Authorize = m.publicationAuthorizer(config.PublicationAuthorization)
	}
	if err := validateDeploymentJobHandlers(executions, m.JobHandlers()); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Module) HTTP() *deploymenthttp.Handler { return m.handler }

// NativePersistence exposes the validated clean-slate authority bundle to
// native delivery composition. It returns nil for the explicit legacy SQLite
// module path.
func (m *Module) NativePersistence() *Persistence {
	if m == nil {
		return nil
	}
	return m.persistence
}

// SealedApprovalVerifier returns the module's durable approval check for the
// sealed publication boundary. Composition installs it on the coordinator
// after Build, once the SQLite-backed approval service exists.
func (m *Module) SealedApprovalVerifier() sealedcontrol.ApprovalVerifier {
	if m == nil {
		return sealedcontrol.DurableApprovalVerifier(nil)
	}
	return sealedcontrol.DurableApprovalVerifier(m.approvals)
}

func (m *Module) PrepareCandidateRuntime(
	ctx context.Context,
	request deployment.CandidateRuntimeRequest,
) (deployment.CandidateRuntimeReceipt, error) {
	if m == nil || m.candidateRuntimes == nil {
		return deployment.CandidateRuntimeReceipt{}, deployment.ErrCandidateUnavailable
	}
	return m.candidateRuntimes.Prepare(ctx, request)
}
