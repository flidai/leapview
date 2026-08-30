package module

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type ActivationHooks struct{}

// NewBootstrapPersistence constructs the durable bootstrap policy and project
// claim ports owned by the deployment module. Callers receive contracts only;
// the SQLite adapter never crosses the module boundary.
func NewBootstrapPersistence(database *sql.DB) (BootstrapPersistence, error) {
	if database == nil {
		return nil, errors.New("deployment database is required")
	}
	return deploymentsqlite.NewRepositoryWithHooks(database, deploymentsqlite.ActivationHooks{}), nil
}

func newPersistence(
	database *sql.DB,
	hooks ActivationHooks,
	releases ReleasePort,
	workflow jobplatform.WorkflowRecorder,
	cancelJob jobplatform.WorkflowJobCanceller,
	audit access.AuditIntentRecorder,
) (
	deployment.Repository,
	deployment.ActivationUnitOfWork,
	deployment.CandidateRepository,
	deployment.ApprovalRepository,
) {
	sqliteHooks := deploymentsqlite.ActivationHooks{}
	sqliteHooks.Audit = audit
	if releases != nil {
		sqliteHooks.LinkRelease = func(ctx context.Context, tx transaction.Transaction, input deployment.CreateInput) error {
			return releases.LinkDeploymentTx(ctx, tx, input.ServingIdentity.ProjectID.String(), input.ID, input.ReleaseID, input.RollbackOf)
		}
	}
	sqliteHooks.RecordWorkflow = workflow
	sqliteHooks.CancelJob = cancelJob
	owned := deploymentsqlite.NewRepositoryWithHooks(database, sqliteHooks)
	return owned, owned, owned, owned
}

// Persistence is the deployment module's native authority bundle.  It is
// deliberately constructed only by NewPostgresPersistence: the unexported
// backend marker and repository identity prevent a database/sql repository (or
// a test double) from being labelled as the production PostgreSQL authority.
//
// The native delivery repository exposes the clean-slate candidate, project
// claim, delivery-reader, and activation surfaces directly.  These surfaces
// use PostgreSQL-owned value types and caller-owned pgx transactions; they are
// not coerced into the legacy deployment HTTP contracts below.
type Persistence struct {
	Repository     *deploymentpostgres.Repository
	Candidates     NativeCandidateRepository
	ProjectClaims  NativeProjectClaimRepository
	DeliveryReader NativeDeliveryReader
	Activation     NativeActivationRepository
	Events         NativeDeliveryEventAppender
	Audit          NativeDeliveryAuditAppender
	Workflow       NativeDeliveryWorkflowRecorder
	Operations     NativeOperationAuthority

	native  *deploymentpostgres.Repository
	backend persistenceBackend
}

// NativePersistenceCapabilities is the complete cross-capability dependency
// set required by production HTTP mutations. Each port receives the same
// caller-owned pgx transaction as the delivery repository.
type NativePersistenceCapabilities struct {
	Events     NativeDeliveryEventAppender
	Audit      NativeDeliveryAuditAppender
	Workflow   NativeDeliveryWorkflowRecorder
	Operations NativeOperationAuthority
}

// NativeDeliveryEventInput is the capability-neutral event contract used by
// native deployment mutations. Deployment supplies the preallocated UUIDv7
// retry identity; the event appender owns exact keyed replay.
type NativeDeliveryEventInput struct {
	EventID, ScopeID, AggregateType, AggregateID, EventType string
	SchemaVersion                                           int64
	CorrelationID                                           string
	Payload                                                 json.RawMessage
}

type NativeDeliveryEventAppender interface {
	AppendDeliveryEvent(context.Context, deploymentpostgres.Tx, NativeDeliveryEventInput) (deploymentpostgres.Event, error)
}

// NativeDeliveryAuditInput is the source-mutation projection consumed by the
// Access audit adapter. AuditID and DomainEventID are UUIDv7 identities for a
// fresh mutation and must be replayed exactly on a retry.
type NativeDeliveryAuditInput struct {
	AuditID, DomainEventID, ScopeID, ActorID, Action, ResourceKind, ResourceID string
	Outcome, RequestDigest, CorrelationID, AggregateKey                        string
	AggregateSequence                                                          int64
	Metadata                                                                   json.RawMessage
}

type NativeDeliveryAuditAppender interface {
	AppendMutationAudit(context.Context, deploymentpostgres.Tx, NativeDeliveryAuditInput) (deploymentpostgres.AuditEvent, error)
}

type NativeDeliveryWorkflowRecorder interface {
	RecordWorkflow(context.Context, deploymentpostgres.Tx, jobs.WorkflowIntent) error
}

// NativeOperationTx is the only operation-capability surface that crosses
// the module boundary. It is intentionally structural: the application
// adapter may back it with any transactional operation store without exposing
// that store's DTOs or package to deployment.
type NativeOperationTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type NativeOperationStatus string

const (
	NativeOperationAcquired      NativeOperationStatus = "acquired"
	NativeOperationReplay        NativeOperationStatus = "replay"
	NativeOperationBusy          NativeOperationStatus = "busy"
	NativeOperationIndeterminate NativeOperationStatus = "indeterminate"
)

// NativeOperationAcquireInput is the minimal idempotency request projection.
// Request bytes are deliberately absent: the coordinator supplies the
// canonical digest after validating the HTTP request.
type NativeOperationAcquireInput struct {
	Scope, OperationType, IdempotencyKey, RequestDigest, OwnerID string
}

type NativeOperationRecord struct {
	Scope, OperationType, IdempotencyKey, RequestDigest, OwnerID string
	OperationID                                                  string
	Outcome                                                      json.RawMessage
}

type NativeOperationLease struct {
	Scope, IdempotencyKey, OperationID, OwnerID string
	FencingGeneration                           int64
	LeaseExpiresAt                              time.Time
	AttemptID, AttemptIdentity                  string
}

type NativeOperationAcquireResult struct {
	Status    NativeOperationStatus
	Operation NativeOperationRecord
	Lease     NativeOperationLease
}

// These sentinels let adapters translate their own storage errors without
// leaking a concrete operation package into deployment.
var (
	ErrNativeOperationConflict        = errors.New("native operation conflict")
	ErrNativeOperationBusy            = errors.New("native operation busy")
	ErrNativeOperationStaleFence      = errors.New("native operation stale fence")
	ErrNativeOperationLeaseExpired    = errors.New("native operation lease expired")
	ErrNativeOperationAlreadyTerminal = errors.New("native operation already terminal")
	ErrNativeOperationInvalid         = errors.New("native operation invalid")
	ErrNativeOperationNotFound        = errors.New("native operation not found")
)

type NativeOperationAuthority interface {
	AcquireTx(context.Context, NativeOperationTx, NativeOperationAcquireInput) (NativeOperationAcquireResult, error)
	CompleteTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error
}

type persistenceBackend uint8

const (
	backendUnknown persistenceBackend = iota
	backendPostgres
)

// NativeProjectClaimRepository is the transactional project-claim surface
// owned by the PostgreSQL delivery authority.
type NativeProjectClaimRepository interface {
	deployment.ProjectClaimRepository
	ClaimProjectTx(context.Context, deploymentpostgres.Tx, deployment.ProjectClaimInput) (deployment.ProjectClaim, error)
}

// NativeCandidateRepository contains only the candidate operations currently
// implemented by the clean-slate delivery authority.  It intentionally does
// not implement deployment.CandidateRepository: the latter is the legacy
// SQLite candidate service and has different identity and lifecycle semantics.
type NativeCandidateRepository interface {
	CreateCandidate(context.Context, deploymentpostgres.CandidateInput) (deploymentpostgres.DeliveryCandidate, error)
	CreateCandidateTx(context.Context, deploymentpostgres.Tx, deploymentpostgres.CandidateInput) (deploymentpostgres.DeliveryCandidate, error)
	StartCandidateWithClaimTx(context.Context, deploymentpostgres.Tx, deployment.ProjectClaimInput, deploymentpostgres.CandidateInput) (deployment.ProjectClaim, deploymentpostgres.DeliveryCandidate, error)
	Candidate(context.Context, string) (deploymentpostgres.DeliveryCandidate, error)
	LoadCandidate(context.Context, string) (deploymentpostgres.DeliveryCandidate, error)
	QualifyCandidate(context.Context, string, string, string) (deploymentpostgres.DeliveryCandidate, error)
	QualifyCandidateTx(context.Context, deploymentpostgres.Tx, string, string, string) (deploymentpostgres.DeliveryCandidate, error)
}

// NativeDeliveryReader is the read-only clean-slate delivery surface.  The
// legacy deployment.DeliveryReader also includes an operator snapshot that is
// not yet persisted by the PostgreSQL authority, so no unsafe adapter is
// provided.
type NativeDeliveryReader interface {
	Plan(context.Context, string) (deploymentpostgres.DeliveryPlan, error)
	LoadPlan(context.Context, string) (deploymentpostgres.DeliveryPlan, error)
	BuildAttempt(context.Context, string) (deploymentpostgres.DeliveryBuildAttempt, error)
	LoadBuildAttempt(context.Context, string) (deploymentpostgres.DeliveryBuildAttempt, error)
	SnapshotSeal(context.Context, string) (deploymentpostgres.SnapshotSeal, error)
	LoadSnapshotSeal(context.Context, string) (deploymentpostgres.SnapshotSeal, error)
	Candidate(context.Context, string) (deploymentpostgres.DeliveryCandidate, error)
	LoadCandidate(context.Context, string) (deploymentpostgres.DeliveryCandidate, error)
	Generation(context.Context, string) (deploymentpostgres.DeliveryGeneration, error)
	LoadGeneration(context.Context, string) (deploymentpostgres.DeliveryGeneration, error)
	Publication(context.Context, string) (deploymentpostgres.DeliveryPublication, error)
	LoadPublication(context.Context, string) (deploymentpostgres.DeliveryPublication, error)
}

// NativeActivationRepository is the fence/CAS activation surface.  Both
// forms accept a caller-owned transaction where supplied; neither crosses
// into a DuckLake transaction.
type NativeActivationRepository interface {
	Activate(context.Context, deploymentpostgres.ActivationInput) (deploymentpostgres.ActivationResult, error)
	ActivateTx(context.Context, deploymentpostgres.Tx, deploymentpostgres.ActivationInput) (deploymentpostgres.ActivationResult, error)
}

// NewPostgresPersistence adapts the concrete PostgreSQL delivery authority.
// Requiring the concrete type is intentional: an arbitrary implementation of
// a legacy deployment interface must never opt into the native path.
func NewPostgresPersistence(repository *deploymentpostgres.Repository) (Persistence, error) {
	if repository == nil {
		return Persistence{}, errors.New("PostgreSQL deployment repository is required")
	}
	if !repository.Configured() {
		return Persistence{}, errors.New("PostgreSQL deployment repository is not configured")
	}
	if !repository.TransactionCapable() {
		return Persistence{}, errors.New("PostgreSQL deployment repository must support caller-owned transactions")
	}
	return Persistence{
		Repository:     repository,
		Candidates:     repository,
		ProjectClaims:  repository,
		DeliveryReader: repository,
		Activation:     repository,
		native:         repository,
		backend:        backendPostgres,
	}, nil
}

// NewPostgresPersistenceWithCapabilities constructs the production authority
// bundle and fails closed when any transactional consequence port is absent.
// The legacy constructor remains available for read/build-only tests; Build
// applies the same strict check whenever Production is enabled.
func NewPostgresPersistenceWithCapabilities(repository *deploymentpostgres.Repository, capabilities NativePersistenceCapabilities) (Persistence, error) {
	persistence, err := NewPostgresPersistence(repository)
	if err != nil {
		return Persistence{}, err
	}
	if capabilities.Events == nil || capabilities.Audit == nil || capabilities.Workflow == nil || capabilities.Operations == nil {
		return Persistence{}, errors.New("native PostgreSQL deployment consequence authorities are required")
	}
	persistence.Events, persistence.Audit, persistence.Workflow, persistence.Operations = capabilities.Events, capabilities.Audit, capabilities.Workflow, capabilities.Operations
	return persistence, nil
}

func (p Persistence) isPostgres() bool {
	return p.backend == backendPostgres && p.native != nil && p.Repository == p.native
}

func (p Persistence) validate() error {
	if !p.isPostgres() {
		return errors.New("deployment persistence backend is not configured as PostgreSQL")
	}
	if p.Repository == nil || !p.Repository.Configured() {
		return errors.New("PostgreSQL deployment repository is not configured")
	}
	if !p.Repository.TransactionCapable() {
		return errors.New("PostgreSQL deployment repository must support caller-owned transactions")
	}
	if p.Candidates == nil || p.ProjectClaims == nil || p.DeliveryReader == nil || p.Activation == nil {
		return errors.New("PostgreSQL deployment candidate, project-claim, delivery-reader, and activation surfaces are required")
	}
	if candidate, ok := p.Candidates.(*deploymentpostgres.Repository); !ok || candidate != p.native {
		return errors.New("deployment candidate surface is not the constructed PostgreSQL repository")
	}
	if claims, ok := p.ProjectClaims.(*deploymentpostgres.Repository); !ok || claims != p.native {
		return errors.New("deployment project-claim surface is not the constructed PostgreSQL repository")
	}
	if reader, ok := p.DeliveryReader.(*deploymentpostgres.Repository); !ok || reader != p.native {
		return errors.New("deployment delivery-reader surface is not the constructed PostgreSQL repository")
	}
	if activation, ok := p.Activation.(*deploymentpostgres.Repository); !ok || activation != p.native {
		return errors.New("deployment activation surface is not the constructed PostgreSQL repository")
	}
	if !p.Repository.AuditCapable() {
		return errors.New("PostgreSQL deployment activation audit capability is required")
	}
	return nil
}
