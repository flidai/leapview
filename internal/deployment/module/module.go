package module

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
)

type Module struct {
	handler                   *deploymenthttp.Handler
	candidates                *deployment.CandidateService
	approvals                 *deployment.ApprovalService
	candidateRuntimes         CandidateRuntimePreparer
	candidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	candidateSources          deployment.CandidateSourceSynchronizer
	candidateSourceBlobAudit  func(context.Context, CandidateSourceBlobAuditEvent) error
	candidateArtifacts        release.CandidateArtifactPreparer
	candidateAdmission        CandidatePreparationAdmitter
	logger                    *slog.Logger
	jobs                      JobConfig
	api                       APIConfig
	instanceID                string
	executions                map[string]apigencommand.AsyncExecutionContract
	protected                 bool
	currentApprovalActor      func(*http.Request) (deployment.ApprovalActor, bool)
	authorizeApproval         func(context.Context, deployment.ApprovalActor, string, string) error
	authorizeActivation       func(context.Context, deployment.ApprovalActor, string, string) error
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

// CandidateSourceBlobAuditEvent is the transport-neutral audit record emitted
// after an immutable candidate source blob has been accepted. Action and
// Capability is copied from the generated command contract by the module.
type CandidateSourceBlobAuditEvent struct {
	PrincipalID   string
	ProjectID     projectgraph.ResourceID
	Digest        string
	Action        string
	Capability    access.Capability
	Status        string
	RequestID     string
	CorrelationID string
	MetadataJSON  string
}

const (
	CandidatePreparing          = deployment.CandidatePreparing
	CandidateReady              = deployment.CandidateReady
	CandidateFailed             = deployment.CandidateFailed
	CandidateCancelled          = deployment.CandidateCancelled
	CandidateExpired            = deployment.CandidateExpired
	CandidateDataReuseSnapshot  = deployment.CandidateDataReuseSnapshot
	CandidateDataRefreshSources = deployment.CandidateDataRefreshSources
)

var (
	ErrCandidateNotFound    = deployment.ErrCandidateNotFound
	ErrCandidateUnavailable = deployment.ErrCandidateUnavailable
)

type ServingStatePort interface {
	deployment.ServingStateRepository
}

type Config struct {
	Database                  *sql.DB
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
	CandidateSourceBlobAudit  func(context.Context, CandidateSourceBlobAuditEvent) error
	CandidateConnections      deployment.CandidateConnectionLeaser
	CandidateRuntime          deployment.CandidateRuntimeHost
	CandidateRuntimeLifecycle deployment.CandidateRuntimeLifecycle
	CandidateSources          deployment.CandidateSourceSynchronizer
	CandidateArtifacts        release.CandidateArtifactPreparer
	CandidateAdmission        CandidatePreparationAdmitter
	RuntimeVersion            string
	CurrentPrincipal          func(*http.Request) (Principal, bool)
	CurrentApprovalActor      func(*http.Request) (deployment.ApprovalActor, bool)
	AuthorizeApproval         func(context.Context, deployment.ApprovalActor, string, string) error
	AuthorizeActivation       func(context.Context, deployment.ApprovalActor, string, string) error
	// AfterActivated runs after runtime publication and durable activation.
	// It is observational and cannot influence activation.
	AfterActivated           func(context.Context, deployment.Deployment)
	Protected                bool
	Jobs                     JobConfig
	API                      APIConfig
	PublicationAuthorization PublicationAuthorizationConfig
}

func Build(_ context.Context, config Config) (*Module, error) {
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
	if config.Database != nil {
		if config.States == nil || config.Runtime == nil || config.ManagedData == nil {
			return nil, errors.New("deployment states, runtime, and managed data are required")
		}
		repository, activation, candidateRepository, approvalRepository := newPersistence(
			config.Database,
			config.ActivationHooks,
			config.API.Releases,
			config.API.Workflow,
		)
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
		})
		if err != nil {
			return nil, err
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
		candidateSourceBlobAudit: config.CandidateSourceBlobAudit,
		logger:                   config.Logger,
		jobs:                     jobs, api: config.API, protected: config.Protected,
		instanceID: config.InstanceID, executions: executions,
		currentApprovalActor: config.CurrentApprovalActor,
		authorizeApproval:    config.AuthorizeApproval,
		authorizeActivation:  config.AuthorizeActivation,
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

func (m *Module) PrepareCandidateRuntime(
	ctx context.Context,
	request deployment.CandidateRuntimeRequest,
) (deployment.CandidateRuntimeReceipt, error) {
	if m == nil || m.candidateRuntimes == nil {
		return deployment.CandidateRuntimeReceipt{}, deployment.ErrCandidateUnavailable
	}
	return m.candidateRuntimes.Prepare(ctx, request)
}
