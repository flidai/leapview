package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/exploration"
	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func (s *Service) openCurrent(ctx context.Context, request saved.ReadRequest, action AuthorizationAction) (saved.OpenResult, *semanticmodel.Model, error) {
	lease, err := s.acquire(ctx, request.ProjectID)
	if err != nil {
		return saved.OpenResult{}, nil, err
	}
	defer lease.Release()
	return s.openCurrentWithLease(ctx, lease, request, action)
}

func (s *Service) openCurrentWithLease(ctx context.Context, lease projectruntime.Lease, request saved.ReadRequest, action AuthorizationAction) (saved.OpenResult, *semanticmodel.Model, error) {
	lifecycle, err := s.repository.GetLifecycle(ctx, saved.ReadInput{ProjectID: request.ProjectID, ID: request.ID})
	if err != nil {
		return saved.OpenResult{}, nil, err
	}
	if err := ensureUsableLifecycle(lifecycle, request.ProjectID, request.ID); err != nil {
		return saved.OpenResult{}, nil, err
	}
	if err := s.authorize(ctx, lease, AuthorizationRequest{ActorID: request.ActorID, ProjectID: request.ProjectID, ExplorationID: request.ID, OwnerPrincipalID: lifecycle.OwnerPrincipalID, Title: lifecycle.Title, Visibility: lifecycle.Visibility, Status: lifecycle.Status, SemanticModelID: lifecycle.SemanticModelID, Action: action, Lifecycle: lifecycle}); err != nil {
		return saved.OpenResult{}, nil, err
	}
	if action == AuthorizationActionExecute && lifecycle.Status == saved.StatusArchived {
		return saved.OpenResult{}, nil, saved.ErrArchived
	}
	revision, err := s.repository.GetRevision(ctx, saved.RevisionReadInput{ProjectID: request.ProjectID, ID: request.ID, Revision: lifecycle.CurrentRevision.Token()})
	if err != nil {
		return saved.OpenResult{}, nil, err
	}
	if _, err := saved.Open(lifecycle, revision); err != nil {
		return saved.OpenResult{}, nil, err
	}
	spec, err := currentSpec(revision)
	if err != nil {
		return saved.OpenResult{}, nil, err
	}
	// A view/reopen is also the repair path for authored revisions whose
	// selected fields disappeared from the active semantic model. The payload
	// has already passed its own integrity checks above (including the
	// lifecycle/model identity binding), so only execution needs the active
	// model projection and compatibility validation.
	var model *semanticmodel.Model
	if action == AuthorizationActionExecute {
		model, err = s.modelForLease(lease, lifecycle.SemanticModelID)
		if err != nil {
			return saved.OpenResult{}, nil, err
		}
		if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
			return saved.OpenResult{}, nil, fmt.Errorf("%w: current revision is incompatible with active semantic model: %v", saved.ErrInvalidPayload, err)
		}
	}
	opened, err := saved.Open(lifecycle, revision)
	if err != nil {
		return saved.OpenResult{}, nil, err
	}
	return opened, model, nil
}

func (s *Service) acquire(ctx context.Context, projectID projectgraph.ResourceID) (projectruntime.Lease, error) {
	if s == nil || s.runtime == nil {
		return nil, saved.ErrUnavailable
	}
	lease, err := s.runtime.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, fmt.Errorf("%w: runtime lease is nil", saved.ErrUnavailable)
	}
	identity := lease.Identity()
	if err := identity.Validate(); err != nil {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime serving identity: %v", saved.ErrUnavailable, err)
	}
	if identity.ProjectID != projectID {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime project %q does not match request %q", saved.ErrInvalid, identity.ProjectID, projectID)
	}
	runtime := lease.Runtime()
	if runtime == nil {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime capability is unavailable", saved.ErrUnavailable)
	}
	identityRuntime, ok := runtime.(interface {
		Identity() projectgraph.ServingIdentity
	})
	if !ok {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime does not expose its serving identity", saved.ErrUnavailable)
	}
	runtimeIdentity := identityRuntime.Identity()
	if err := runtimeIdentity.Validate(); err != nil {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime serving identity: %v", saved.ErrUnavailable, err)
	}
	if runtimeIdentity != identity {
		lease.Release()
		return nil, fmt.Errorf("%w: runtime serving identity does not match lease", saved.ErrUnavailable)
	}
	return lease, nil
}

func (s *Service) authorize(ctx context.Context, lease projectruntime.Lease, request AuthorizationRequest) error {
	if lease == nil || s == nil || s.authorizer == nil {
		return fmt.Errorf("%w: saved exploration authorizer is unavailable", saved.ErrUnavailable)
	}
	if err := s.authorizer.Authorize(ctx, lease, request); err != nil {
		return err
	}
	return nil
}

func (s *Service) modelForLease(lease projectruntime.Lease, modelID projectgraph.ResourceID) (*semanticmodel.Model, error) {
	if lease == nil || lease.Runtime() == nil {
		return nil, fmt.Errorf("%w: runtime model capability is unavailable", saved.ErrUnavailable)
	}
	runtime := lease.Runtime()
	if reader, ok := runtime.(SemanticModelProjection); ok {
		if model, found := reader.SemanticModelProjection(modelID); found && model != nil {
			return model, nil
		}
	}
	return nil, fmt.Errorf("%w: semantic model %q is unavailable from active runtime", saved.ErrUnavailable, modelID)
}

func (s *Service) validateModel(lease projectruntime.Lease, spec exploration.ExplorationSpec) error {
	model, err := s.modelForLease(lease, projectgraph.ResourceID(spec.ModelID))
	if err != nil {
		return err
	}
	if err := exploration.ValidateAgainstModel(model, &spec); err != nil {
		return fmt.Errorf("%w: exploration does not match active semantic model: %v", saved.ErrInvalidPayload, err)
	}
	return nil
}

func (s *Service) lookup(ctx context.Context, projectID projectgraph.ResourceID, actor string, evidence saved.MutationEvidence) (saved.MutationReplayMetadata, bool, error) {
	return s.lookupMutation(ctx, saved.MutationLookupInput{ProjectID: projectID, ActorID: actor, Action: evidence.Action, IdempotencyKey: evidence.IdempotencyKey, Fingerprint: evidence.Fingerprint})
}

func (s *Service) lookupMutation(ctx context.Context, input saved.MutationLookupInput) (saved.MutationReplayMetadata, bool, error) {
	metadata, found, err := s.repository.LookupMutation(ctx, input)
	if err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	if !found {
		return saved.MutationReplayMetadata{}, false, nil
	}
	if err := metadata.Validate(); err != nil {
		return saved.MutationReplayMetadata{}, false, err
	}
	if metadata.Evidence.ActorID != input.ActorID || metadata.Evidence.Action != input.Action || metadata.Evidence.IdempotencyKey != input.IdempotencyKey || metadata.Evidence.Fingerprint != input.Fingerprint {
		return saved.MutationReplayMetadata{}, false, fmt.Errorf("%w: replay metadata does not match lookup identity", saved.ErrConflict)
	}
	return metadata.Clone(), true, nil
}

// hydrateReplay loads the exact immutable revision only after the application
// service has authorized the replay lifecycle. Archive metadata is complete
// without a payload and deliberately remains revisionless.
func (s *Service) hydrateReplay(ctx context.Context, metadata saved.MutationReplayMetadata, concurrencyRevision ...saved.RevisionToken) (saved.MutationResult, error) {
	if err := metadata.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	result := saved.MutationResult{Lifecycle: metadata.Lifecycle, AppliedRevision: metadata.AppliedRevision, Evidence: metadata.Evidence, Replayed: true}
	if len(concurrencyRevision) > 0 {
		result.ConcurrencyRevision = concurrencyRevision[0]
	}
	if metadata.Evidence.Action != saved.MutationActionArchive {
		revision, err := s.repository.GetRevision(ctx, saved.RevisionReadInput{ProjectID: metadata.Lifecycle.ProjectID, ID: metadata.Lifecycle.ID, Revision: metadata.AppliedRevision})
		if err != nil {
			return saved.MutationResult{}, err
		}
		result.Revision = &revision
	}
	return validatedMutation(result)
}

func (s *Service) timestamp() (time.Time, error) {
	now := s.now()
	if now.Location() != time.UTC {
		now = now.UTC()
	}
	if now.IsZero() {
		return time.Time{}, fmt.Errorf("%w: mutation clock returned zero", saved.ErrInvalid)
	}
	return now, nil
}

func (s *Service) newRevision(number uint64, actor string, payload saved.ExplorationSpecPayload, identity projectgraph.ServingIdentity, now time.Time) (saved.Revision, error) {
	id, err := s.newRevisionID()
	if err != nil {
		return saved.Revision{}, err
	}
	return saved.NewRevision(id, number, now, actor, payload, identity)
}

func currentSpec(revision saved.Revision) (exploration.ExplorationSpec, error) {
	if err := revision.Validate(); err != nil {
		return exploration.ExplorationSpec{}, err
	}
	return revision.Payload.Spec()
}

func ensureLifecycle(lifecycle saved.Lifecycle, projectID projectgraph.ResourceID, id saved.ExplorationID) error {
	if err := lifecycle.Validate(); err != nil {
		return err
	}
	if lifecycle.ProjectID != projectID || lifecycle.ID != id {
		return fmt.Errorf("%w: lifecycle identity does not match request", saved.ErrInvalid)
	}
	return nil
}

func ensureUsableLifecycle(lifecycle saved.Lifecycle, projectID projectgraph.ResourceID, id saved.ExplorationID) error {
	// Restricted is a reserved state until its policy adapter is introduced.
	// Check it before validating the remaining row so malformed restricted rows
	// cannot be used to enumerate their existence.
	if lifecycle.Visibility == saved.VisibilityRestricted {
		return saved.ErrNotFound
	}
	if err := ensureLifecycle(lifecycle, projectID, id); err != nil {
		return err
	}
	return nil
}

func rejectRestricted(visibility saved.Visibility) error {
	if visibility == saved.VisibilityRestricted {
		return fmt.Errorf("%w: restricted visibility is not supported", saved.ErrInvalid)
	}
	return nil
}

func compareFingerprint(evidence saved.MutationEvidence, expected string) error {
	if evidence.Fingerprint != expected {
		return fmt.Errorf("%w: mutation request fingerprint does not match evidence", saved.ErrConflict)
	}
	return nil
}

func publicLookupError(err error) error {
	if isDenied(err) {
		return saved.ErrNotFound
	}
	return saved.PublicNotFound(err)
}

func publicMutationLookupError(err error) error {
	if isDenied(err) {
		return saved.ErrNotFound
	}
	return err
}

func isDenied(err error) bool {
	return errors.Is(err, saved.ErrUnauthorized) || errors.Is(err, saved.ErrNotFound) || errors.Is(err, access.ErrForbidden)
}

func isInaccessibleListCursorError(err error) bool {
	return errors.Is(err, saved.ErrNotFound) || errors.Is(err, saved.ErrUnauthorized) || errors.Is(err, access.ErrForbidden)
}

func validatedMutation(result saved.MutationResult) (saved.MutationResult, error) {
	if err := result.Validate(); err != nil {
		return saved.MutationResult{}, err
	}
	return result.Clone(), nil
}

func auditIntent(actor string, evidence saved.MutationEvidence) access.AuditIntent {
	return access.AuditIntent{PrincipalID: actor, RequestID: evidence.RequestID, CorrelationID: evidence.CorrelationID}
}
