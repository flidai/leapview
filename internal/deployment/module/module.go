package module

import (
	"context"
	"database/sql"
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
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
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
	deliveryMutations         DeliveryMutationPort
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
	Database *sql.DB
	// AuditIntentRecorder is the Access-owned transaction-scoped outbox port.
	// It is required whenever deployment SQLite persistence is configured.
	AuditIntentRecorder       access.AuditIntentRecorder
	States                    ServingStatePort
	Runtime                   deployment.Runtime
	ManagedData               deployment.ManagedDataResolver
	ActivationHooks           ActivationHooks
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
	// DeliveryReader is the durable, read-only plan/build/seal/operator port.
	// When Database is configured, Build installs the SQLite repository by
	// default; tests and alternate control stores may provide an implementation.
	DeliveryReader deployment.DeliveryReader
}

func Build(_ context.Context, config Config) (*Module, error) {
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
	if config.Database != nil {
		if config.AuditIntentRecorder == nil {
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
				database: config.Database,
				workflow: config.API.Workflow,
				audit:    config.AuditIntentRecorder,
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
		cancelJob, cancelJobOK := config.API.Jobs.(jobplatform.WorkflowJobCanceller)
		if config.API.Jobs != nil && !cancelJobOK {
			return nil, errors.New("deployment transactional job canceller is required")
		}
		repository, activation, candidateRepository, approvalRepository := newPersistence(
			config.Database,
			config.ActivationHooks,
			config.API.Releases,
			config.API.Workflow,
			cancelJob,
			config.AuditIntentRecorder,
		)
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
		logger:                   config.Logger, auditIntentConfigured: config.Database != nil && config.AuditIntentRecorder != nil,
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
		deliveryMutations: config.DeliveryMutations,
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
