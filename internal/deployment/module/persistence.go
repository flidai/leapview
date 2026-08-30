package module

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	deploymentsqlite "github.com/flidai/leapview/internal/deployment/sqlite"
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
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

	native  *deploymentpostgres.Repository
	backend persistenceBackend
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

// ErrUnsupportedCapability identifies a deliberately unavailable native HTTP
// surface.  Callers can use errors.As to distinguish this from malformed
// persistence configuration and avoid silently falling back to SQLite.
var ErrUnsupportedCapability = errors.New("deployment native capability is unsupported")

type UnsupportedCapabilityError struct{ Capability string }

func (e *UnsupportedCapabilityError) Error() string {
	if e == nil || e.Capability == "" {
		return ErrUnsupportedCapability.Error()
	}
	return fmt.Sprintf("%s: %s", ErrUnsupportedCapability, e.Capability)
}

func (e *UnsupportedCapabilityError) Unwrap() error { return ErrUnsupportedCapability }

// unsupportedCoordinator keeps the native module's HTTP shell safe while the
// legacy request/response coordinator is migrated to clean-slate value types.
// Every operation fails closed with the same typed capability error; there is
// no implicit SQLite fallback and no nil-interface panic in the handler.
type unsupportedCoordinator struct{}

func (unsupportedCoordinator) Create(context.Context, apiadapter.CreateRequest) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, &UnsupportedCapabilityError{Capability: "deployment HTTP create over native PostgreSQL delivery"}
}
func (unsupportedCoordinator) Get(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, &UnsupportedCapabilityError{Capability: "deployment HTTP read over native PostgreSQL delivery"}
}
func (unsupportedCoordinator) Activate(context.Context, apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, &UnsupportedCapabilityError{Capability: "deployment HTTP activation over native PostgreSQL delivery"}
}
func (unsupportedCoordinator) Cancel(context.Context, apiadapter.Scope) (apiadapter.Deployment, error) {
	return apiadapter.Deployment{}, &UnsupportedCapabilityError{Capability: "deployment HTTP cancellation over native PostgreSQL delivery"}
}

var _ deploymenthttp.Coordinator = unsupportedCoordinator{}
