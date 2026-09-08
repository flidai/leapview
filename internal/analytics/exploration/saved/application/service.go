// Package application owns the storage-independent saved-exploration
// application service. It binds durable lifecycle/revision storage to one
// active project-runtime lease and leaves principal policy decisions to the
// composing authorizer.
package application

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	"github.com/flidai/leapview/internal/analytics/exploration/lowering"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type Service struct {
	repository    saved.Repository
	authorizer    Authorizer
	runtime       projectruntime.Provider
	executor      LeaseBoundExecutor
	now           func() time.Time
	newRevisionID func() (saved.RevisionID, error)
}

var _ saved.Service = (*Service)(nil)

func NewService(options Options) (*Service, error) {
	if options.Repository == nil || options.Authorizer == nil || options.Runtime == nil {
		return nil, fmt.Errorf("saved exploration repository, authorizer, and runtime are required")
	}
	if options.Now == nil || options.NewRevisionID == nil {
		return nil, fmt.Errorf("saved exploration clock and revision ID generator are required")
	}
	return &Service{repository: options.Repository, authorizer: options.Authorizer, runtime: options.Runtime,
		executor: options.Executor, now: options.Now, newRevisionID: options.NewRevisionID}, nil
}

// AuthorizeMutationReplay resolves durable mutation evidence and rechecks the
// current lifecycle under the active serving lease. It never calls a mutation
// repository method and returns false when no matching ledger entry exists.
func (s *Service) AuthorizeMutationReplay(ctx context.Context, request MutationReplayAuthorizationRequest) (bool, error) {
	if s == nil {
		return false, saved.ErrUnavailable
	}
	if err := request.Validate(); err != nil {
		return false, err
	}
	lookup := saved.MutationLookupInput{ProjectID: request.ProjectID, ActorID: request.ActorID, Action: request.Action, IdempotencyKey: request.IdempotencyKey, Fingerprint: request.Fingerprint}
	if err := lookup.Validate(); err != nil {
		return false, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return false, err
	}
	defer lease.Release()
	replay, found, err := s.lookupMutation(ctx, lookup)
	if err != nil || !found {
		return false, err
	}
	if err := ensureUsableLifecycle(replay.Lifecycle, request.ProjectID, replay.Lifecycle.ID); err != nil {
		return false, publicMutationLookupError(err)
	}
	if request.Action != saved.MutationActionDuplicate && replay.Lifecycle.ID != request.TargetID {
		return false, saved.ErrConflict
	}

	// Reauthorize the current source/destination lifecycle using only durable
	// metadata. The browser's authored payload is used solely to derive the
	// lookup fingerprint and is never supplied to this policy decision.
	switch request.Action {
	case saved.MutationActionDuplicate:
		source, sourceErr := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.TargetID})
		if sourceErr != nil {
			return false, publicMutationLookupError(sourceErr)
		}
		if sourceErr := ensureUsableLifecycle(source, request.ProjectID, request.TargetID); sourceErr != nil {
			return false, publicMutationLookupError(sourceErr)
		}
		if sourceErr := s.authorize(ctx, lease, AuthorizationRequest{
			ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: source.ID,
			SourceID: source.ID, OwnerPrincipalID: source.OwnerPrincipalID, Title: source.Title,
			Visibility: source.Visibility, Status: source.Status, SemanticModelID: source.SemanticModelID,
			Action: AuthorizationActionView, Lifecycle: source,
		}); sourceErr != nil {
			return false, publicMutationLookupError(sourceErr)
		}
		if authErr := s.authorize(ctx, lease, AuthorizationRequest{
			ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: replay.Lifecycle.ID,
			SourceID: request.TargetID, OwnerPrincipalID: replay.Lifecycle.OwnerPrincipalID,
			Title: replay.Lifecycle.Title, Visibility: replay.Lifecycle.Visibility,
			Status: replay.Lifecycle.Status, SemanticModelID: replay.Lifecycle.SemanticModelID,
			Action: AuthorizationActionCreate, Lifecycle: replay.Lifecycle,
		}); authErr != nil {
			return false, publicMutationLookupError(authErr)
		}
	default:
		current, currentErr := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.TargetID})
		if currentErr != nil {
			return false, publicMutationLookupError(currentErr)
		}
		if currentErr := ensureUsableLifecycle(current, request.ProjectID, request.TargetID); currentErr != nil {
			return false, publicMutationLookupError(currentErr)
		}
		action := AuthorizationActionCreate
		if request.Action == saved.MutationActionUpdate {
			action = AuthorizationActionEdit
		} else if request.Action == saved.MutationActionArchive {
			action = AuthorizationActionArchive
		}
		if authErr := s.authorize(ctx, lease, AuthorizationRequest{
			ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: current.ID,
			OwnerPrincipalID: current.OwnerPrincipalID, Title: current.Title, Visibility: current.Visibility,
			Status: current.Status, SemanticModelID: current.SemanticModelID, Action: action, Lifecycle: current,
		}); authErr != nil {
			return false, publicMutationLookupError(authErr)
		}
	}
	return true, nil
}

// Create persists the first active revision after create authorization and
// model validation against the current leased model.
func (s *Service) Create(ctx context.Context, request saved.CreateRequest) (saved.MutationResult, error) {
	if s == nil {
		return saved.MutationResult{}, saved.ErrUnavailable
	}
	if err := request.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	if err := rejectRestricted(request.Visibility); err != nil {
		return saved.MutationResult{}, err
	}
	payload, err := request.ValidatedPayload()
	if err != nil {
		return saved.MutationResult{}, err
	}
	spec, err := payload.Spec()
	if err != nil {
		return saved.MutationResult{}, err
	}
	fingerprint, err := FingerprintCreate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := compareFingerprint(request.Evidence, fingerprint); err != nil {
		return saved.MutationResult{}, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer lease.Release()
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: request.ID, OwnerPrincipalID: request.ActorID, Title: request.Title, Visibility: request.Visibility, SemanticModelID: projectgraph.ResourceID(spec.ModelID), Action: AuthorizationActionCreate}); err != nil {
		return saved.MutationResult{}, err
	}
	if replay, found, err := s.lookup(ctx, request.ProjectID, request.ActorID, request.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if found {
		if err := ensureUsableLifecycle(replay.Lifecycle, request.ProjectID, request.ID); err != nil {
			return saved.MutationResult{}, publicMutationLookupError(err)
		}
		return s.hydrateReplay(ctx, replay)
	}
	if err := s.validateModel(lease, spec); err != nil {
		return saved.MutationResult{}, err
	}
	now, err := s.timestamp()
	if err != nil {
		return saved.MutationResult{}, err
	}
	revision, err := s.newRevision(1, request.ActorID, payload, lease.Identity(), now)
	if err != nil {
		return saved.MutationResult{}, err
	}
	input := saved.CreateInput{ProjectID: request.ProjectID, ID: request.ID, OwnerPrincipalID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, SemanticModelID: projectgraph.ResourceID(spec.ModelID), CreatedAt: now, Revision: revision, Evidence: request.Evidence}
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	result, err := s.repository.Create(saved.WithAuditIntent(ctx, auditIntent(request.ActorID, request.Evidence)), input)
	if err != nil {
		return saved.MutationResult{}, err
	}
	return validatedMutation(result)
}

// Read authorizes lifecycle metadata before reading the exact current token.
// Both missing and denied resources are returned as saved.ErrNotFound.
func (s *Service) Read(ctx context.Context, request saved.ReadRequest) (saved.OpenResult, error) {
	if err := request.Validate(); err != nil {
		return saved.OpenResult{}, err
	}
	opened, _, err := s.openCurrent(ctx, saved.ReadRequest{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID}, AuthorizationActionView)
	return opened, publicLookupError(err)
}

// Reopen is a defensive read-only working-copy operation. No repository
// mutation is performed and the decoded spec is independent of persisted
// payload bytes.
func (s *Service) Reopen(ctx context.Context, request saved.ReopenRequest) (saved.ReopenResult, error) {
	if err := request.Validate(); err != nil {
		return saved.ReopenResult{}, err
	}
	opened, _, err := s.openCurrent(ctx, saved.ReadRequest{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID}, AuthorizationActionView)
	if err != nil {
		return saved.ReopenResult{}, publicLookupError(err)
	}
	spec, err := opened.Revision.Payload.Spec()
	if err != nil {
		return saved.ReopenResult{}, err
	}
	return saved.ReopenResult{Lifecycle: opened.Lifecycle, Revision: opened.Revision.Metadata, Spec: spec}, nil
}

// List is the complete-list compatibility helper used by the browser. It is
// implemented through the same bounded keyset/authorization path as the API;
// a zero limit means the service continues until SQL exhaustion.
func (s *Service) List(ctx context.Context, request saved.ListRequest) ([]saved.Lifecycle, error) {
	request.Limit = 0
	request.PageToken = ""
	page, err := s.ListPage(ctx, request)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

// ListPage authorizes lifecycle rows in bounded repository batches and never
// loads a revision payload. It keeps scanning when denied rows consume a
// batch, and only emits a continuation token derived from an authorized row.
// This avoids exposing an unauthorized ID in a cursor while still returning
// exactly the requested number of visible rows.
func (s *Service) ListPage(ctx context.Context, request saved.ListRequest) (saved.ListPage, error) {
	if err := request.Validate(); err != nil {
		return saved.ListPage{}, err
	}
	start, err := saved.DecodeListCursor(request.PageToken, request.ProjectID, request.IncludeArchived)
	if err != nil {
		return saved.ListPage{}, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.ListPage{}, err
	}
	defer lease.Release()
	if start != "" {
		// Preserve fail-closed continuation semantics: a fabricated token, a
		// deleted row, a row removed by the archive filter, or a row whose
		// authorization was revoked is not treated as an arbitrary SQL seek.
		cursorLifecycle, err := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: start})
		if err != nil {
			if isInaccessibleListCursorError(err) {
				return saved.ListPage{}, fmt.Errorf("%w: list cursor is unavailable", saved.ErrInvalid)
			}
			return saved.ListPage{}, err
		}
		if cursorLifecycle.Visibility == saved.VisibilityRestricted || (!request.IncludeArchived && cursorLifecycle.Status == saved.StatusArchived) {
			return saved.ListPage{}, fmt.Errorf("%w: list cursor is unavailable", saved.ErrInvalid)
		}
		if err := ensureLifecycle(cursorLifecycle, request.ProjectID, start); err != nil {
			return saved.ListPage{}, fmt.Errorf("%w: list cursor is unavailable", saved.ErrInvalid)
		}
		if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: cursorLifecycle.ID, OwnerPrincipalID: cursorLifecycle.OwnerPrincipalID, Title: cursorLifecycle.Title, Visibility: cursorLifecycle.Visibility, Status: cursorLifecycle.Status, SemanticModelID: cursorLifecycle.SemanticModelID, Action: AuthorizationActionView, Lifecycle: cursorLifecycle}); err != nil {
			if isDenied(err) {
				return saved.ListPage{}, fmt.Errorf("%w: list cursor is unavailable", saved.ErrInvalid)
			}
			return saved.ListPage{}, err
		}
	}
	const repositoryBatch = saved.MaxListLimit
	visible := make([]saved.Lifecycle, 0, request.Limit)
	cursor := start.String()
	for {
		page, err := s.repository.ListPage(ctx, saved.ListInput{ProjectID: request.ProjectID, IncludeArchived: request.IncludeArchived, Cursor: cursor, Limit: repositoryBatch})
		if err != nil {
			return saved.ListPage{}, err
		}
		for _, lifecycle := range page.Items {
			// Keep restricted records indistinguishable from absent records,
			// including when their other metadata is malformed.
			if lifecycle.Visibility == saved.VisibilityRestricted {
				continue
			}
			if err := ensureLifecycle(lifecycle, request.ProjectID, lifecycle.ID); err != nil {
				return saved.ListPage{}, err
			}
			err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: lifecycle.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, Title: lifecycle.Title, Visibility: lifecycle.Visibility, Status: lifecycle.Status, SemanticModelID: lifecycle.SemanticModelID, Action: AuthorizationActionView, Lifecycle: lifecycle})
			if isDenied(err) {
				continue
			}
			if err != nil {
				return saved.ListPage{}, err
			}
			visible = append(visible, lifecycle)
			// Find one authorized row beyond the requested page before
			// returning. This makes NextCursor absent when only unauthorized
			// records remain after the last visible item.
			if request.Limit > 0 && len(visible) > request.Limit {
				next, err := saved.EncodeListCursor(request.ProjectID, request.IncludeArchived, visible[request.Limit-1].ID)
				if err != nil {
					return saved.ListPage{}, err
				}
				return saved.ListPage{Items: visible[:request.Limit], NextCursor: next}, nil
			}
		}
		if page.NextCursor == "" {
			if request.Limit > 0 && len(visible) > request.Limit {
				visible = visible[:request.Limit]
			}
			return saved.ListPage{Items: visible}, nil
		}
		cursor = page.NextCursor
	}
}

// UpdateVersion appends exactly one fresh revision under the request's CAS
// token. The current revision is read only after lifecycle authorization.
func (s *Service) UpdateVersion(ctx context.Context, request saved.UpdateVersionRequest) (saved.MutationResult, error) {
	if err := request.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	if err := rejectRestricted(request.Visibility); err != nil {
		return saved.MutationResult{}, err
	}
	payload, err := request.ValidatedPayload()
	if err != nil {
		return saved.MutationResult{}, err
	}
	spec, err := payload.Spec()
	if err != nil {
		return saved.MutationResult{}, err
	}
	fingerprint, err := FingerprintUpdate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := compareFingerprint(request.Evidence, fingerprint); err != nil {
		return saved.MutationResult{}, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer lease.Release()
	lifecycle, err := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.ID})
	if err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if err := ensureUsableLifecycle(lifecycle, request.ProjectID, request.ID); err != nil {
		return saved.MutationResult{}, err
	}
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: request.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, Title: request.Title, Visibility: request.Visibility, Status: lifecycle.Status, SemanticModelID: projectgraph.ResourceID(spec.ModelID), Action: AuthorizationActionEdit, Lifecycle: lifecycle}); err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if replay, found, err := s.lookup(ctx, request.ProjectID, request.ActorID, request.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if found {
		if err := ensureUsableLifecycle(replay.Lifecycle, request.ProjectID, request.ID); err != nil {
			return saved.MutationResult{}, publicMutationLookupError(err)
		}
		return s.hydrateReplay(ctx, replay, request.ExpectedRevision)
	}
	if lifecycle.Status == saved.StatusArchived {
		return saved.MutationResult{}, saved.ErrArchived
	}
	if lifecycle.CurrentRevision.Token() != request.ExpectedRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	if err := s.validateModel(lease, spec); err != nil {
		return saved.MutationResult{}, err
	}
	now, err := s.timestamp()
	if err != nil {
		return saved.MutationResult{}, err
	}
	// The prior authored payload is intentionally not loaded here. A saved
	// revision may become incompatible after a semantic-model change; an edit
	// with a valid CAS token is the repair path. The repository still enforces
	// the same exact token atomically before appending this fresh revision.
	revision, err := s.newRevision(request.ExpectedRevision.Number+1, request.ActorID, payload, lease.Identity(), now)
	if err != nil {
		return saved.MutationResult{}, err
	}
	input := saved.UpdateVersionInput{ProjectID: request.ProjectID, ID: request.ID, ExpectedRevision: request.ExpectedRevision, Revision: revision, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, SemanticModelID: projectgraph.ResourceID(spec.ModelID), UpdatedAt: now, Evidence: request.Evidence}
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	result, err := s.repository.UpdateVersion(saved.WithAuditIntent(ctx, auditIntent(request.ActorID, request.Evidence)), input)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if result.ConcurrencyRevision.IsZero() {
		// Non-SQLite repository implementations may not expose the transaction
		// observation. The lifecycle read above is still an independent
		// canonical source; SQLite replaces this fallback with its same-tx CAS
		// observation.
		result.ConcurrencyRevision = lifecycle.CurrentRevision.Token()
	}
	return validatedMutation(result)
}

// Duplicate reads and authorizes source lifecycle metadata, then reads its
// exact expected revision and copies its canonical payload bytes verbatim.
func (s *Service) Duplicate(ctx context.Context, request saved.DuplicateRequest) (saved.MutationResult, error) {
	if err := request.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	if err := rejectRestricted(request.Visibility); err != nil {
		return saved.MutationResult{}, err
	}
	fingerprint, err := FingerprintDuplicate(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := compareFingerprint(request.Evidence, fingerprint); err != nil {
		return saved.MutationResult{}, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer lease.Release()
	source, err := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.SourceID})
	if err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if err := ensureUsableLifecycle(source, request.ProjectID, request.SourceID); err != nil {
		return saved.MutationResult{}, err
	}
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: source.ID, SourceID: source.ID, OwnerPrincipalID: source.OwnerPrincipalID, Title: source.Title, Visibility: source.Visibility, Status: source.Status, SemanticModelID: source.SemanticModelID, Action: AuthorizationActionView, Lifecycle: source}); err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if replay, found, err := s.lookup(ctx, request.ProjectID, request.ActorID, request.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if found {
		if err := ensureUsableLifecycle(replay.Lifecycle, request.ProjectID, request.ID); err != nil {
			return saved.MutationResult{}, publicMutationLookupError(err)
		}
		if err := s.authorize(ctx, lease, AuthorizationRequest{
			ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: replay.Lifecycle.ID,
			SourceID: request.SourceID, OwnerPrincipalID: replay.Lifecycle.OwnerPrincipalID,
			Title: replay.Lifecycle.Title, Visibility: replay.Lifecycle.Visibility,
			Status: replay.Lifecycle.Status, SemanticModelID: replay.Lifecycle.SemanticModelID,
			Action: AuthorizationActionCreate, Lifecycle: replay.Lifecycle,
		}); err != nil {
			return saved.MutationResult{}, publicMutationLookupError(err)
		}
		return s.hydrateReplay(ctx, replay, request.ExpectedSourceRevision)
	}
	if source.CurrentRevision.Token() != request.ExpectedSourceRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	sourceRevision, err := s.repository.GetRevision(ctx, saved.RevisionReadInput{ProjectID: request.ProjectID, ID: request.SourceID, Revision: request.ExpectedSourceRevision})
	if err != nil {
		return saved.MutationResult{}, err
	}
	spec, err := currentSpec(sourceRevision)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: request.ID, SourceID: request.SourceID, OwnerPrincipalID: request.ActorID, Title: request.Title, Visibility: request.Visibility, Status: saved.StatusActive, SemanticModelID: projectgraph.ResourceID(spec.ModelID), Action: AuthorizationActionCreate}); err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if err := s.validateModel(lease, spec); err != nil {
		return saved.MutationResult{}, err
	}
	now, err := s.timestamp()
	if err != nil {
		return saved.MutationResult{}, err
	}
	payload := sourceRevision.Payload.Clone()
	revision, err := s.newRevision(1, request.ActorID, payload, lease.Identity(), now)
	if err != nil {
		return saved.MutationResult{}, err
	}
	destination := saved.CreateInput{ProjectID: request.ProjectID, ID: request.ID, OwnerPrincipalID: request.ActorID, Title: request.Title, Slug: request.Slug, Visibility: request.Visibility, SemanticModelID: projectgraph.ResourceID(spec.ModelID), CreatedAt: now, Revision: revision, Evidence: request.Evidence}
	input := saved.DuplicateInput{ProjectID: request.ProjectID, SourceID: request.SourceID, ExpectedSourceRevision: request.ExpectedSourceRevision, Destination: destination, Evidence: request.Evidence}
	if err := input.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	result, err := s.repository.Duplicate(saved.WithAuditIntent(ctx, auditIntent(request.ActorID, request.Evidence)), input)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if result.ConcurrencyRevision.IsZero() {
		result.ConcurrencyRevision = source.CurrentRevision.Token()
	}
	return validatedMutation(result)
}

// Archive transitions lifecycle state via exact CAS and deliberately never
// reads or appends an authored revision.
func (s *Service) Archive(ctx context.Context, request saved.ArchiveRequest) (saved.MutationResult, error) {
	if err := request.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	fingerprint, err := FingerprintArchive(request)
	if err != nil {
		return saved.MutationResult{}, err
	}
	if err := compareFingerprint(request.Evidence, fingerprint); err != nil {
		return saved.MutationResult{}, err
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.MutationResult{}, err
	}
	defer lease.Release()
	lifecycle, err := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.ID})
	if err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if err := ensureUsableLifecycle(lifecycle, request.ProjectID, request.ID); err != nil {
		return saved.MutationResult{}, err
	}
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: request.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, Title: lifecycle.Title, Visibility: lifecycle.Visibility, Status: lifecycle.Status, SemanticModelID: lifecycle.SemanticModelID, Action: AuthorizationActionArchive, Lifecycle: lifecycle}); err != nil {
		return saved.MutationResult{}, publicMutationLookupError(err)
	}
	if replay, found, err := s.lookup(ctx, request.ProjectID, request.ActorID, request.Evidence); err != nil {
		return saved.MutationResult{}, err
	} else if found {
		if err := ensureUsableLifecycle(replay.Lifecycle, request.ProjectID, request.ID); err != nil {
			return saved.MutationResult{}, publicMutationLookupError(err)
		}
		return s.hydrateReplay(ctx, replay, request.ExpectedRevision)
	}
	if lifecycle.Status == saved.StatusArchived {
		return saved.MutationResult{}, saved.ErrArchived
	}
	if lifecycle.CurrentRevision.Token() != request.ExpectedRevision {
		return saved.MutationResult{}, saved.ErrStaleRevision
	}
	archivedAt, err := s.timestamp()
	if err != nil {
		return saved.MutationResult{}, err
	}
	result, err := s.repository.Archive(saved.WithAuditIntent(ctx, auditIntent(request.ActorID, request.Evidence)), saved.ArchiveInput{ProjectID: request.ProjectID, ID: request.ID, ExpectedRevision: request.ExpectedRevision, ArchivedAt: archivedAt, Evidence: request.Evidence})
	if err != nil {
		return saved.MutationResult{}, err
	}
	if result.ConcurrencyRevision.IsZero() {
		result.ConcurrencyRevision = lifecycle.CurrentRevision.Token()
	}
	return validatedMutation(result)
}

// Execute authorizes and reads the exact current revision before lowering and
// sending it to the lease-bound governed executor.
func (s *Service) Execute(ctx context.Context, request saved.ExecuteRequest) (saved.ExecuteResult, error) {
	if err := request.Validate(); err != nil {
		return saved.ExecuteResult{}, err
	}
	if s.executor == nil {
		return saved.ExecuteResult{}, saved.ErrUnavailable
	}
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.ExecuteResult{}, err
	}
	defer lease.Release()
	opened, model, err := s.openCurrentWithLease(ctx, lease, saved.ReadRequest{ProjectID: request.ProjectID, ID: request.ID, ActorID: request.ActorID}, AuthorizationActionExecute)
	if err != nil {
		return saved.ExecuteResult{}, publicLookupError(err)
	}
	if model == nil {
		return saved.ExecuteResult{}, fmt.Errorf("%w: semantic model is unavailable", saved.ErrUnavailable)
	}
	spec, err := currentSpec(opened.Revision)
	if err != nil {
		return saved.ExecuteResult{}, err
	}
	query, err := lowering.QueryForModel(spec, model)
	if err != nil {
		return saved.ExecuteResult{}, fmt.Errorf("%w: lower saved exploration: %v", saved.ErrInvalidPayload, err)
	}
	query = query.WithMetadata(dataquery.Metadata{ProjectID: request.ProjectID, Surface: "saved_exploration", Operation: "saved_exploration_execute", PrincipalID: request.ActorID, RequestID: request.RequestID, CorrelationID: request.CorrelationID, ObjectType: "saved_exploration", ObjectID: request.ID.String()})
	result, err := s.executor.Execute(ctx, lease, request.ActorID, query)
	if err != nil {
		return saved.ExecuteResult{}, err
	}
	return saved.ExecuteResult{Lifecycle: opened.Lifecycle, Revision: opened.Revision, Query: query, Result: result, Evidence: saved.ExecutionEvidence{ActorID: request.ActorID, Revision: opened.Revision.Token(), ServingIdentity: lease.Identity()}}, nil
}
