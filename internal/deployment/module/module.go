package module

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	projectClaims             *deployment.ProjectClaimService
	approvals                 *deployment.ApprovalService
	candidateRuntimes         CandidateRuntimePreparer
	candidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	candidateSources          deployment.CandidateSourceSynchronizer
	candidateSourceAudit      func(context.Context, CandidateSourceAuditEvent) error
	candidateSourceBlobAudit  func(context.Context, CandidateSourceAuditEvent) error
	candidateArtifacts        release.CandidateArtifactPreparer
	candidateArtifactRecovery release.CandidateArtifactRecovery
	candidateAdmission        CandidatePreparationAdmitter
	nativeMetadataSchema      func(string) string
	deliveryCandidateBuilder  func(context.Context, deployment.DeliveryCandidateBuildInput) (deployment.Candidate, error)
	logger                    *slog.Logger
	jobs                      JobConfig
	api                       APIConfig
	instanceID                string
	canonicalOrigin           string
	instanceEnvironment       servingstate.Environment
	bindClaimedProject        func(context.Context, projectgraph.ResourceID, servingstate.Environment) error
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
// this module; composition may provide a store without depending on its
// concrete repository.
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

// ActivationPreCommitHook is the module-owned qualification seam invoked
// immediately before native activation commits its durable target CAS. The
// module exposes only the cancellation context; publication details remain an
// implementation concern of the PostgreSQL deployment adapter.
type ActivationPreCommitHook func(context.Context) error

type Config struct {
	// Persistence is the native PostgreSQL delivery authority. Production
	// callers construct it with NewPostgresPersistence; the module never infers
	// an adapter from a raw database handle.
	Persistence               *Persistence
	Production                bool
	Logger                    *slog.Logger
	InstanceID                string
	CanonicalOrigin           string
	InstanceEnvironment       string
	CandidateSourceAudit      func(context.Context, CandidateSourceAuditEvent) error
	CandidateSourceBlobAudit  func(context.Context, CandidateSourceAuditEvent) error
	CandidateConnections      deployment.CandidateConnectionLeaser
	CandidateRuntime          deployment.CandidateRuntimeHost
	CandidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	CandidateSources          deployment.CandidateSourceSynchronizer
	CandidateArtifacts        release.CandidateArtifactPreparer
	// CandidateArtifactRecovery is the value-only native serving-bundle
	// recovery authority used to replay candidate runtime preparation after a
	// process restart. It is deliberately separate from source/artifact
	// preparation so preview cannot recompile mutable authoring state.
	CandidateArtifactRecovery release.CandidateArtifactRecovery
	CandidateAdmission        CandidatePreparationAdmitter
	// NativeMetadataSchemaForPool derives Analytics' deterministic DuckLake
	// metadata namespace without coupling Deployment to an Analytics adapter.
	// Native candidate preview preparation fails closed when it is absent.
	NativeMetadataSchemaForPool func(string) string
	// BindClaimedProject binds the process runtime to the durable instance
	// claim after candidate start commits.
	BindClaimedProject   func(context.Context, projectgraph.ResourceID, servingstate.Environment) error
	RuntimeVersion       string
	CurrentPrincipal     func(*http.Request) (Principal, bool)
	CurrentApprovalActor func(*http.Request) (deployment.ApprovalActor, bool)
	AuthorizeApproval    func(context.Context, deployment.ApprovalActor, string, string) error
	AuthorizeActivation  func(context.Context, deployment.ApprovalActor, string, string) error
	// BeforeNativeActivationCommit is an optional application-owned release
	// qualification seam. The native PostgreSQL repository invokes it after
	// validating every activation proof and immediately before its target CAS.
	BeforeNativeActivationCommit ActivationPreCommitHook
	// BootstrapPolicies is the durable one-shot first-activation policy store.
	// It is intentionally separate from approvals and active-generation
	// snapshots; composition supplies the access-role/credential and
	// no-active-generation revalidator.
	BootstrapPolicies  BootstrapPolicyStore
	AuthorizeBootstrap func(context.Context, deployment.BootstrapActivationPolicy) error
	// ProjectClaims is the read-only durable instance/project binding used by
	// pre-activation API authorization. It is deliberately independent from
	// the activation-policy store so native PostgreSQL delivery does not need a
	// legacy bootstrap-policy adapter merely to expose its canonical claim.
	ProjectClaims            ProjectClaimReader
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
	// DeliveryReader is retained as a topology-neutral read contract for
	// non-native callers; production uses NativeDeliveryReader.
	DeliveryReader deployment.DeliveryReader
	// NativeDeliveryReader is the clean-slate PostgreSQL read port. It is
	// deliberately distinct from DeliveryReader so production handlers cannot
	// silently fall back to projections with weaker identity guarantees.
	NativeDeliveryReader NativeDeliveryReader
	// Native source-mutation capabilities are strict transaction-bound ports.
	// Production PostgreSQL composition must provide all four; no
	// cross-connection fallback is permitted.
	NativeDeliveryEvents     NativeDeliveryEventAppender
	NativeDeliveryAudit      NativeDeliveryAuditAppender
	NativeDeliveryWorkflow   NativeDeliveryWorkflowRecorder
	NativeOperationAuthority NativeOperationAuthority
}

func Build(_ context.Context, config Config) (*Module, error) {
	if config.Persistence == nil {
		return nil, errors.New("deployment persistence is required")
	}
	if err := config.Persistence.validate(); err != nil {
		return nil, err
	}
	if !config.Production {
		return nil, errors.New("native PostgreSQL persistence requires production deployment mode")
	}
	if config.Persistence.isPostgres() {
		// Native PostgreSQL delivery is the only production mutation authority.
		// Requiring this port here prevents a partially composed module from
		// falling through to the legacy DeliveryMutationPort at request time.
		if config.NativeDeliveryMutations == nil {
			return nil, errors.New("production native deployment requires delivery mutation authority")
		}
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
		// Keep the native module from retaining optional compatibility seams
		// supplied by broad application composition. Native handlers fail closed
		// when their native ports are absent rather than falling back to them.
		config.DeliveryMutations = nil
		config.DeliveryReader = nil
		config.API.Releases = nil
		config.SealedCoordinator = nil
		config.SealedPublishRequest = nil
		config.SealedRollbackRequest = nil
		config.SealedActivationMarker = nil
		config.SealedReconcile = nil
		config.SealedRollbackFence = nil
		config.RequireSealedCoordinator = false
	}
	if config.RequireSealedCoordinator && (config.SealedCoordinator == nil || config.SealedPublishRequest == nil || config.SealedRollbackRequest == nil || config.SealedReconcile == nil || config.SealedRollbackFence == nil) {
		return nil, fmt.Errorf("sealed publication coordinator and durable request resolvers are required")
	}
	executions, err := loadDeploymentExecutionContracts()
	if err != nil {
		return nil, err
	}
	options := deploymenthttp.Options{}
	options.CurrentPrincipal = func(r *http.Request) (deploymenthttp.Principal, bool) {
		if config.CurrentPrincipal == nil {
			return deploymenthttp.Principal{}, false
		}
		principal, ok := config.CurrentPrincipal(r)
		return deploymenthttp.Principal{ID: principal.ID}, ok
	}
	var coordinator deploymenthttp.Coordinator
	var projectClaims *deployment.ProjectClaimService
	var candidateRuntimes *deployment.CandidateRuntimeService
	// Native production HTTP requests are coordinated directly against the
	// canonical PostgreSQL delivery authority. No legacy service or adapter is
	// introduced on this path.
	var coordinatorErr error
	coordinator, coordinatorErr = newNativeCoordinator(config.Persistence.Repository, config.InstanceID, config.InstanceEnvironment, nativeCoordinatorCapabilities{
		events: config.NativeDeliveryEvents, audit: config.NativeDeliveryAudit, workflow: config.NativeDeliveryWorkflow, operations: config.NativeOperationAuthority,
		beforeActivationCommit: config.BeforeNativeActivationCommit,
	})
	if coordinatorErr != nil {
		return nil, coordinatorErr
	}
	// The native coordinator owns pending publish/rollback creation as a
	// separate authority from plan/build. Keep an explicit caller override
	// possible, while ensuring production PostgreSQL composition cannot
	// accidentally fall back to a non-native mutation port.
	if config.NativeDeliveryPublication == nil {
		if publicationPort, ok := coordinator.(NativeDeliveryPublicationPort); ok {
			config.NativeDeliveryPublication = publicationPort
		}
	}
	projectClaims, coordinatorErr = deployment.NewProjectClaimService(config.Persistence.ProjectClaims)
	if coordinatorErr != nil {
		return nil, coordinatorErr
	}
	// Candidate runtime preparation is replayed lazily from durable delivery
	// evidence.
	if config.CandidateConnections != nil || config.CandidateRuntime != nil {
		if config.CandidateAdmission == nil {
			return nil, errors.New("candidate runtime preparation workload admission is required")
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
	options.InstanceEnvironment = config.InstanceEnvironment
	jobs := config.Jobs
	if jobs.Coordinator == nil {
		jobs.Coordinator = coordinator
	}
	m := &Module{
		handler:           deploymenthttp.NewHandler(options),
		projectClaims:     projectClaims,
		candidateRuntimes: candidateRuntimes, candidateRuntimeLifecycle: config.CandidateRuntimeLifecycle, candidateSources: config.CandidateSources,
		candidateArtifacts: config.CandidateArtifacts, candidateArtifactRecovery: config.CandidateArtifactRecovery,
		candidateAdmission:       config.CandidateAdmission,
		nativeMetadataSchema:     config.NativeMetadataSchemaForPool,
		candidateSourceAudit:     config.CandidateSourceAudit,
		candidateSourceBlobAudit: config.CandidateSourceBlobAudit,
		logger:                   config.Logger,
		jobs:                     jobs, api: config.API, protected: config.Protected,
		instanceID: config.InstanceID, canonicalOrigin: config.CanonicalOrigin, instanceEnvironment: servingstate.Environment(config.InstanceEnvironment),
		bindClaimedProject: config.BindClaimedProject, executions: executions,
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
		persistence:            config.Persistence,
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
// native delivery composition.
func (m *Module) NativePersistence() *Persistence {
	if m == nil {
		return nil
	}
	return m.persistence
}

// SealedApprovalVerifier returns the module's durable approval check for the
// sealed publication boundary. Composition installs it on the coordinator
// after Build, once the durable approval service exists.
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
