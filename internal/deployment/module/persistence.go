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

// SQLitePersistenceConfig contains the complete explicit local/evaluation
// adapter construction inputs. Build never infers SQLite from a raw database
// handle; callers must construct this bundle deliberately.
type SQLitePersistenceConfig struct {
	Database  *sql.DB
	Releases  ReleasePort
	Workflow  jobplatform.WorkflowRecorder
	CancelJob jobplatform.WorkflowJobCanceller
	Audit     access.AuditIntentRecorder
}

// NewSQLiteBootstrapPersistence constructs the durable bootstrap policy and
// project claim ports owned by the deployment module. Callers receive
// contracts only; the SQLite adapter never crosses the module boundary.
func NewSQLiteBootstrapPersistence(database *sql.DB) (BootstrapPersistence, error) {
	if database == nil {
		return nil, errors.New("SQLite deployment database is required")
	}
	return deploymentsqlite.NewRepositoryWithHooks(database, deploymentsqlite.ActivationHooks{}), nil
}

// NewSQLitePersistence constructs the full local/evaluation deployment
// authority bundle. The concrete SQLite repositories remain private to this
// module; Build consumes the typed bundle rather than selecting an adapter
// from Config.Database/LegacySQLite flags.
func NewSQLitePersistence(config SQLitePersistenceConfig) (Persistence, error) {
	if config.Database == nil {
		return Persistence{}, errors.New("SQLite deployment database is required")
	}
	repository, activation, candidates, approvals := newPersistence(
		config.Database, config.Releases, config.Workflow,
		config.CancelJob, config.Audit,
	)
	return Persistence{
		Candidates:       nil,
		ProjectClaims:    nil,
		DeliveryReader:   nil,
		Activation:       nil,
		legacyRepository: repository,
		legacyActivation: activation,
		legacyCandidates: candidates,
		legacyApprovals:  approvals,
		legacyDatabase:   config.Database,
		legacyAudit:      config.Audit,
		backend:          backendSQLite,
	}, nil
}

func newPersistence(
	database *sql.DB,
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
	Approval       *deploymentpostgres.ApprovalAuthority

	native  *deploymentpostgres.Repository
	backend persistenceBackend

	legacyRepository deployment.Repository
	legacyActivation deployment.ActivationUnitOfWork
	legacyCandidates deployment.CandidateRepository
	legacyApprovals  deployment.ApprovalRepository
	legacyDatabase   *sql.DB
	legacyAudit      access.AuditIntentRecorder
}

// NativePersistenceCapabilities is the complete cross-capability dependency
// set required by production HTTP mutations. Each port receives the same
// caller-owned pgx transaction as the delivery repository.
type NativePersistenceCapabilities struct {
	Events     NativeDeliveryEventAppender
	Audit      NativeDeliveryAuditAppender
	Workflow   NativeDeliveryWorkflowRecorder
	Operations NativeOperationAuthority
	Approval   *deploymentpostgres.ApprovalAuthority
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
	Operation, Outcome, RequestDigest, CorrelationID, AggregateKey             string
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

// NativeOperationState is the durable state of one idempotent operation. It
// is kept separate from NativeOperationStatus because acquisition also has
// transient dispositions (busy/indeterminate) while the stored row has a
// stable state used for replay and recovery decisions.
type NativeOperationState string

const (
	NativeOperationStatePending       NativeOperationState = "pending"
	NativeOperationStateCompleted     NativeOperationState = "completed"
	NativeOperationStateFailed        NativeOperationState = "failed"
	NativeOperationStateIndeterminate NativeOperationState = "indeterminate"
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
	State                                                        NativeOperationState
	FencingGeneration                                            int64
	LeaseExpiresAt                                               time.Time
	AttemptID, AttemptIdentity                                   string
	AttemptEvidence, ResolutionEvidence                          json.RawMessage
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

// NativeOperationBeginAttemptInput binds one external attempt to an acquired
// operation. AttemptID may be empty to request an authority-generated
// UUIDv7; AttemptIdentity is the caller's stable external identity.
type NativeOperationBeginAttemptInput struct {
	Lease           NativeOperationLease
	AttemptID       string
	AttemptIdentity string
}

// NativeOperationAttempt is the value-only attempt projection returned by an
// operation authority after binding an external attempt.
type NativeOperationAttempt struct {
	AttemptID       string
	AttemptIdentity string
	Lease           NativeOperationLease
}

// NativeOperationSuccessorInput binds a fresh executable leaf to an
// indeterminate public operation. The predecessor identity is immutable and
// remains the public row's attempt; the leaf owns the new lease/fence.
type NativeOperationSuccessorInput struct {
	Predecessor         NativeOperationLease
	PredecessorID       string
	PredecessorIdentity string
	AttemptID           string
	AttemptIdentity     string
	OwnerID             string
	LeaseExpiresAt      time.Time
}

type NativeOperationSuccessor struct {
	AttemptID           string
	AttemptIdentity     string
	PredecessorID       string
	PredecessorIdentity string
	Lease               NativeOperationLease
	// State and evidence describe the current executable leaf. They are
	// projected so recovery can distinguish a still-pending external attempt
	// from one already marked indeterminate before deciding whether to resolve
	// or append another successor.
	State              NativeOperationState
	AttemptEvidence    json.RawMessage
	ResolutionEvidence json.RawMessage
}

// NativeBuildOperationSuccessorAuthority is optional on the broad operation
// interface so existing SQLite/evaluation doubles remain unchanged. Native
// PostgreSQL BuildPlan recovery requires this capability to execute a
// successor leaf without mutating the public indeterminate operation row.
type NativeBuildOperationSuccessorAuthority interface {
	AdmitSuccessorAttemptTx(context.Context, NativeOperationTx, NativeOperationSuccessorInput) (NativeOperationSuccessor, error)
	CurrentSuccessorAttempt(context.Context, string) (NativeOperationSuccessor, bool, error)
}

// NativeBuildOperationSuccessorLockAuthority extends successor recovery with
// an in-transaction public->leaf lock. Long-running completion and settlement
// must acquire the append-only leaf before touching delivery/DuckLake rows.
type NativeBuildOperationSuccessorLockAuthority interface {
	NativeBuildOperationSuccessorAuthority
	LockSuccessorAttemptTx(context.Context, NativeOperationTx, NativeOperationLease) (NativeOperationSuccessor, error)
}

// NativeOperationReconcileAttemptInput resolves an indeterminate operation
// using the exact external attempt identity and positive evidence. State must
// be completed or failed; outcome and evidence are canonical object JSON
// owned by the operation authority.
type NativeOperationReconcileAttemptInput struct {
	Scope           string
	IdempotencyKey  string
	AttemptID       string
	AttemptIdentity string
	State           NativeOperationState
	Outcome         json.RawMessage
	Evidence        json.RawMessage
}

// NativeOperationReconcileAttemptResult is the storage-neutral projection of
// the reconciled durable operation.
type NativeOperationReconcileAttemptResult struct {
	Operation NativeOperationRecord
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

// NativeBuildOperationAuthority extends the short operation surface used by
// publication/create mutations with the attempt and terminal transitions
// required by long-running physical builds. Keeping this separate avoids
// forcing unrelated native callers and test doubles to implement recovery
// methods they never invoke.
type NativeBuildOperationAuthority interface {
	NativeOperationAuthority
	Lookup(context.Context, NativeOperationAcquireInput) (NativeOperationRecord, bool, error)
	// LockOperationTx retains the exact operation row lock in the caller-owned
	// transaction without changing state. Multi-ledger build paths use it to
	// preserve operation -> target lease -> attempt lock ordering.
	LockOperationTx(context.Context, NativeOperationTx, NativeOperationAcquireInput) (NativeOperationRecord, bool, error)
	BeginAttemptTx(context.Context, NativeOperationTx, NativeOperationBeginAttemptInput) (NativeOperationAttempt, error)
	ReconcileAttemptTx(context.Context, NativeOperationTx, NativeOperationReconcileAttemptInput) (NativeOperationReconcileAttemptResult, error)
	RenewLeaseTx(context.Context, NativeOperationTx, NativeOperationLease, time.Duration) (NativeOperationLease, error)
	FailTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error
	MarkIndeterminateTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error
	// ExpireAttemptTx settles a bound external attempt after the operation
	// lease has expired. The authority must match every lease and attempt
	// identity field exactly before fencing the operation to indeterminate.
	ExpireAttemptTx(context.Context, NativeOperationTx, NativeOperationLease, json.RawMessage) error
	// ConfirmExpiredAttemptTx locks and projects the exact indeterminate row
	// produced by MarkIndeterminateTx or ExpireAttemptTx.
	// expectedFencingGeneration must be the predecessor lease fence plus one.
	ConfirmExpiredAttemptTx(context.Context, NativeOperationTx, NativeOperationLease, int64) (NativeOperationRecord, error)
}

type persistenceBackend uint8

const (
	backendUnknown persistenceBackend = iota
	backendSQLite
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
	// OperatorSnapshot is the bounded native operator projection. The
	// PostgreSQL delivery authority owns target identity and active pointers;
	// richer SQLite-only retention/lease projections intentionally do not cross
	// this port.
	OperatorSnapshot(context.Context, string) (deploymentpostgres.DeliveryOperatorSnapshot, error)
}

// IsNativeDeliveryMissing translates the concrete PostgreSQL reader's bounded
// absence/invalid-input vocabulary without exposing its adapter package to
// application routing.
func IsNativeDeliveryMissing(err error) bool {
	return errors.Is(err, deploymentpostgres.ErrNotFound) || errors.Is(err, deploymentpostgres.ErrInvalid)
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
	if capabilities.Events == nil || capabilities.Audit == nil || capabilities.Workflow == nil || capabilities.Operations == nil || capabilities.Approval == nil {
		return Persistence{}, errors.New("native PostgreSQL deployment consequence authorities are required")
	}
	persistence.Events, persistence.Audit, persistence.Workflow, persistence.Operations, persistence.Approval = capabilities.Events, capabilities.Audit, capabilities.Workflow, capabilities.Operations, capabilities.Approval
	return persistence, nil
}

func (p Persistence) isPostgres() bool {
	return p.backend == backendPostgres && p.native != nil && p.Repository == p.native
}

func (p Persistence) isSQLite() bool {
	return p.backend == backendSQLite && p.native == nil && p.legacyDatabase != nil && p.legacyRepository != nil
}

func (p Persistence) validate() error {
	if p.isSQLite() {
		if p.legacyActivation == nil || p.legacyCandidates == nil || p.legacyApprovals == nil {
			return errors.New("SQLite deployment persistence surfaces are required")
		}
		return nil
	}
	if !p.isPostgres() {
		return errors.New("deployment persistence backend is not configured as PostgreSQL")
	}
	if p.Repository == nil || !p.Repository.Configured() {
		return errors.New("PostgreSQL deployment repository is not configured")
	}
	if !p.Repository.TransactionCapable() {
		return errors.New("PostgreSQL deployment repository must support caller-owned transactions")
	}
	if p.Approval == nil {
		return errors.New("PostgreSQL deployment approval authority is required")
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
