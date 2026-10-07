package application

import (
	"context"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

// AuthorizationAction is deliberately smaller than the access capability
// vocabulary. The adapter maps these resource actions to its private-owner,
// admin, and organization-reader policy without requiring the service to
// know how principals or grants are stored.
type AuthorizationAction string

const (
	AuthorizationActionCreate  AuthorizationAction = "create"
	AuthorizationActionView    AuthorizationAction = "view"
	AuthorizationActionEdit    AuthorizationAction = "edit"
	AuthorizationActionArchive AuthorizationAction = "archive"
	AuthorizationActionExecute AuthorizationAction = "execute"
)

// AuthorizationRequest contains lifecycle metadata only. In particular, the
// authored spec is never available to the authorizer before its decision.
// SourceID is set for duplicate operations; ExplorationID is always the
// resource whose policy is being requested.
type AuthorizationRequest struct {
	ActorID          string
	ProjectID        projectgraph.ResourceID
	ExplorationID    saved.ExplorationID
	SourceID         saved.ExplorationID
	OwnerPrincipalID string
	Title            string
	Visibility       saved.Visibility
	Status           saved.Status
	SemanticModelID  projectgraph.ResourceID
	Action           AuthorizationAction
	Lifecycle        saved.Lifecycle
}

type MutationReplayAuthorizationRequest = saved.MutationReplayAuthorizationRequest

// Authorizer is called only after the service has acquired and validated the
// exact serving lease. Implementations must use that lease/snapshot and must
// not reacquire a runtime internally.
type Authorizer interface {
	Authorize(context.Context, projectruntime.Lease, AuthorizationRequest) error
}

type AuthorizerFunc func(context.Context, projectruntime.Lease, AuthorizationRequest) error

func (f AuthorizerFunc) Authorize(ctx context.Context, lease projectruntime.Lease, request AuthorizationRequest) error {
	return f(ctx, lease, request)
}

// LeaseBoundExecutor is the execution adapter boundary. Implementations are
// responsible for governed admission/RLS/masking using this exact lease;
// they must not acquire another active runtime or fall back to a creator's
// credentials.
type LeaseBoundExecutor interface {
	Execute(context.Context, projectruntime.Lease, string, dataquery.Query) (dataquery.Result, error)
}

type LeaseBoundExecutorFunc func(context.Context, projectruntime.Lease, string, dataquery.Query) (dataquery.Result, error)

func (f LeaseBoundExecutorFunc) Execute(ctx context.Context, lease projectruntime.Lease, actorID string, query dataquery.Query) (dataquery.Result, error) {
	return f(ctx, lease, actorID, query)
}

// SemanticModelProjection is the sole model capability required from a
// leased runtime. The implementation must return the model projection bound
// to that lease's serving generation.
type SemanticModelProjection interface {
	SemanticModelProjection(projectgraph.ResourceID) (*semanticmodel.Model, bool)
}

// Options wires all application dependencies. IDs and time are injected so
// mutations remain deterministic and do not derive identity from authored
// titles, slugs, or stale revision provenance.
type Options struct {
	Repository    saved.Repository
	Authorizer    Authorizer
	Runtime       projectruntime.Provider
	Executor      LeaseBoundExecutor
	Now           func() time.Time
	NewRevisionID func() (saved.RevisionID, error)
}

// Fingerprint helpers expose the exact durable request identity expected in
// MutationEvidence. They intentionally omit generated revision IDs/timestamps
// and evidence itself, so a retry compares caller intent rather than server
// allocation details.
func FingerprintCreate(request saved.CreateRequest) (string, error) {
	return saved.FingerprintCreate(request)
}
func FingerprintUpdate(request saved.UpdateVersionRequest) (string, error) {
	return saved.FingerprintUpdate(request)
}
func FingerprintDuplicate(request saved.DuplicateRequest) (string, error) {
	return saved.FingerprintDuplicate(request)
}
func FingerprintArchive(request saved.ArchiveRequest) (string, error) {
	return saved.FingerprintArchive(request)
}
