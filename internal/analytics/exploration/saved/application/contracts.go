package application

import (
	"context"
	"fmt"
	"strings"
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

// MutationReplayAuthorizationRequest identifies a previously committed
// mutation without carrying a writable mutation input. It is intentionally
// limited to the durable retry identity and the request's explicit target so
// replay authorization cannot accidentally dispatch a fresh mutation.
type MutationReplayAuthorizationRequest struct {
	ProjectID      projectgraph.ResourceID
	ActorID        string
	Action         saved.MutationAction
	IdempotencyKey string
	Fingerprint    string
	TargetID       saved.ExplorationID
}

func (request MutationReplayAuthorizationRequest) Validate() error {
	if err := request.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: replay project id: %v", saved.ErrInvalid, err)
	}
	if strings.TrimSpace(request.ActorID) == "" {
		return fmt.Errorf("%w: replay actor id is required", saved.ErrInvalid)
	}
	if !request.Action.Valid() {
		return fmt.Errorf("%w: replay mutation action is invalid", saved.ErrInvalid)
	}
	if err := saved.ExplorationID(request.TargetID).Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" || strings.TrimSpace(request.Fingerprint) == "" {
		return fmt.Errorf("%w: replay identity is incomplete", saved.ErrInvalid)
	}
	return nil
}

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
	payload, err := request.ValidatedPayload()
	if err != nil {
		return "", err
	}
	return saved.CanonicalFingerprint(createFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, Payload: payload.Canonical()})
}

func FingerprintUpdate(request saved.UpdateVersionRequest) (string, error) {
	payload, err := request.ValidatedPayload()
	if err != nil {
		return "", err
	}
	return saved.CanonicalFingerprint(updateFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, ExpectedRevision: request.ExpectedRevision, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, Payload: payload.Canonical()})
}

func FingerprintDuplicate(request saved.DuplicateRequest) (string, error) {
	return saved.CanonicalFingerprint(duplicateFingerprint{ProjectID: request.ProjectID, SourceID: request.SourceID, ExpectedSourceRevision: request.ExpectedSourceRevision, ID: request.ID, ActorID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility})
}

func FingerprintArchive(request saved.ArchiveRequest) (string, error) {
	return saved.CanonicalFingerprint(archiveFingerprint{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID, ExpectedRevision: request.ExpectedRevision})
}

type createFingerprint struct {
	ProjectID  projectgraph.ResourceID
	ID         saved.ExplorationID
	ActorID    string
	Title      string
	Slug       string
	Visibility saved.Visibility
	Payload    []byte
}

type updateFingerprint struct {
	ProjectID        projectgraph.ResourceID
	ID               saved.ExplorationID
	ActorID          string
	ExpectedRevision saved.RevisionToken
	Title            string
	Slug             string
	Visibility       saved.Visibility
	Payload          []byte
}

type duplicateFingerprint struct {
	ProjectID              projectgraph.ResourceID
	SourceID               saved.ExplorationID
	ExpectedSourceRevision saved.RevisionToken
	ID                     saved.ExplorationID
	ActorID                string
	Title                  string
	Slug                   string
	Visibility             saved.Visibility
}

type archiveFingerprint struct {
	ProjectID        projectgraph.ResourceID
	ID               saved.ExplorationID
	ActorID          string
	ExpectedRevision saved.RevisionToken
}
