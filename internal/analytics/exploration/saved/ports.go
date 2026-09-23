package saved

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CreateInput is the complete first version handed to persistence. A
// repository must atomically reserve (ProjectID, ID) and (ProjectID, Slug),
// then retain the immutable first revision with the identity row.
type CreateInput struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	OwnerPrincipalID string
	Title            string
	Slug             string
	Visibility       Visibility
	SemanticModelID  projectgraph.ResourceID
	CreatedAt        time.Time
	Revision         Revision
	Evidence         MutationEvidence
}

func (input CreateInput) Validate() error {
	if err := validateMutationEvidence(input.Evidence, MutationActionCreate); err != nil {
		return err
	}
	if input.OwnerPrincipalID != input.Evidence.ActorID {
		return fmt.Errorf("%w: create owner principal must equal mutation actor", ErrInvalid)
	}
	if input.Revision.Metadata.CreatedBy != input.Evidence.ActorID {
		return fmt.Errorf("%w: initial revision creator must equal mutation actor", ErrInvalid)
	}
	_, err := NewSavedExploration(NewInput{
		ProjectID: input.ProjectID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID,
		Title: input.Title, Slug: input.Slug, Visibility: input.Visibility,
		SemanticModelID: input.SemanticModelID, CreatedAt: input.CreatedAt, Revision: input.Revision,
	})
	return err
}

// ReadInput identifies one project-bound resource. ProjectID is mandatory on
// every operation; an ID from another project is never a valid lookup.
type ReadInput struct {
	ProjectID projectgraph.ResourceID
	ID        ExplorationID
}

func (input ReadInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	return input.ID.Validate()
}

// RevisionReadInput reads one exact immutable version after its lifecycle
// metadata has been authorized. It intentionally requires the complete token.
type RevisionReadInput struct {
	ProjectID projectgraph.ResourceID
	ID        ExplorationID
	Revision  RevisionToken
}

func (input RevisionReadInput) Validate() error {
	if err := (ReadInput{ProjectID: input.ProjectID, ID: input.ID}).Validate(); err != nil {
		return err
	}
	return input.Revision.ValidateComplete()
}

// UpdateVersionInput appends exactly one immutable version under an atomic
// compare-and-swap against ExpectedRevision. The next metadata is explicit so
// title, slug, visibility, and model changes cannot be silently discarded.
type UpdateVersionInput struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ExpectedRevision RevisionToken
	Revision         Revision
	Title            string
	Slug             string
	Visibility       Visibility
	SemanticModelID  projectgraph.ResourceID
	UpdatedAt        time.Time
	Evidence         MutationEvidence
}

func (input UpdateVersionInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := input.ID.Validate(); err != nil {
		return err
	}
	if err := input.ExpectedRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := input.Revision.Validate(); err != nil {
		return err
	}
	if input.Revision.Metadata.Number != input.ExpectedRevision.Number+1 {
		return fmt.Errorf("%w: next revision number must be expected + 1", ErrInvalidRevision)
	}
	if input.Revision.Metadata.ID == input.ExpectedRevision.RevisionID {
		return fmt.Errorf("%w: revision id must be new", ErrInvalidRevision)
	}
	if input.Revision.Metadata.ServingIdentity.ProjectID != input.ProjectID {
		return fmt.Errorf("%w: revision serving identity project does not match update project", ErrInvalid)
	}
	if err := validateUTCTimestamp(input.UpdatedAt, "updatedAt"); err != nil {
		return err
	}
	if !input.Revision.Metadata.CreatedAt.Equal(input.UpdatedAt) {
		return fmt.Errorf("%w: revision timestamp must equal updatedAt", ErrInvalidRevision)
	}
	if err := validateTitle(input.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.Slug) || len(input.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, input.Slug)
	}
	if err := input.Visibility.Validate(); err != nil {
		return err
	}
	if err := input.SemanticModelID.Validate(); err != nil {
		return fmt.Errorf("%w: semantic model id: %v", ErrInvalid, err)
	}
	spec, err := input.Revision.Payload.Spec()
	if err != nil {
		return err
	}
	if projectgraph.ResourceID(spec.ModelID) != input.SemanticModelID {
		return fmt.Errorf("%w: semantic model id does not match revision spec", ErrInvalid)
	}
	if err := validateMutationEvidence(input.Evidence, MutationActionUpdate); err != nil {
		return err
	}
	if input.Revision.Metadata.CreatedBy != input.Evidence.ActorID {
		return fmt.Errorf("%w: revision creator must equal mutation actor", ErrInvalid)
	}
	return nil
}

// DuplicateInput asks a repository to copy the exact source version into a
// new project-bound identity. ExpectedSourceRevision is a source CAS token;
// duplicate must fail if the source changed between read and copy.
type DuplicateInput struct {
	ProjectID              projectgraph.ResourceID
	SourceID               ExplorationID
	ExpectedSourceRevision RevisionToken
	Destination            CreateInput
	Evidence               MutationEvidence
}

func (input DuplicateInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := input.SourceID.Validate(); err != nil {
		return err
	}
	if err := input.ExpectedSourceRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := validateMutationEvidence(input.Evidence, MutationActionDuplicate); err != nil {
		return err
	}
	if input.Destination.OwnerPrincipalID != input.Evidence.ActorID {
		return fmt.Errorf("%w: duplicate owner principal must equal mutation actor", ErrInvalid)
	}
	if input.Destination.Revision.Metadata.CreatedBy != input.Evidence.ActorID {
		return fmt.Errorf("%w: duplicate revision creator must equal mutation actor", ErrInvalid)
	}
	if err := input.Destination.validateWithoutEvidence(); err != nil {
		return err
	}
	if !input.Destination.Evidence.IsZero() && input.Destination.Evidence != input.Evidence {
		return fmt.Errorf("%w: duplicate destination evidence does not match operation evidence", ErrInvalid)
	}
	if input.Destination.ProjectID != input.ProjectID {
		return fmt.Errorf("%w: duplicate destination project does not match source project", ErrInvalid)
	}
	if input.Destination.ID == input.SourceID {
		return fmt.Errorf("%w: duplicate destination ID must be new", ErrConflict)
	}
	if input.Destination.Revision.Metadata.ContentHash != input.ExpectedSourceRevision.ContentHash {
		return fmt.Errorf("%w: duplicate destination payload differs from expected source revision", ErrConflict)
	}
	return nil
}

// ListInput scopes enumeration to one project. Archived records are excluded
// unless explicitly requested by the caller. The result is Lifecycle-only and
// therefore does not hydrate authored specs before authorization.
type ListInput struct {
	ProjectID       projectgraph.ResourceID
	IncludeArchived bool
	// Cursor is the last exploration identity returned by the repository's
	// keyset query. It is an internal SQL key, never an API token.
	Cursor string
	// Limit bounds one SQL batch. The repository reads one extra row to set
	// NextCursor without loading the complete project list.
	Limit int
}

func (input ListInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if input.Cursor != "" {
		if err := (ExplorationID(input.Cursor)).Validate(); err != nil {
			return fmt.Errorf("%w: list cursor: %v", ErrInvalid, err)
		}
	}
	if input.Limit < 0 || input.Limit > MaxListLimit {
		return fmt.Errorf("%w: list limit must be between 0 and %d", ErrInvalid, MaxListLimit)
	}
	return nil
}

// ListPage is a bounded lifecycle projection. Repository NextCursor values
// are internal SQL keys; application-service NextCursor values are scoped,
// opaque API tokens produced only from authorized items.
type ListPage struct {
	Items      []Lifecycle `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

// ArchiveInput atomically transitions one active identity to archived under a
// complete expected revision token. Archiving does not append a query or
// compiled result and retains the latest authored revision for audit.
type ArchiveInput struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ExpectedRevision RevisionToken
	ArchivedAt       time.Time
	Evidence         MutationEvidence
}

func (input ArchiveInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := input.ID.Validate(); err != nil {
		return err
	}
	if err := input.ExpectedRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := validateUTCTimestamp(input.ArchivedAt, "archivedAt"); err != nil {
		return err
	}
	return validateMutationEvidence(input.Evidence, MutationActionArchive)
}

// CreateRequest is the application-facing request boundary. ActorID is the
// authenticated principal and is deliberately the only owner authority; the
// mutation evidence must carry that same principal. The authored model ID is
// derived from the validated payload rather than accepted as a second client
// field.
func (input CreateRequest) Validate() error {
	if err := validateMutationRequestIdentity(input.ProjectID, input.ID, input.ActorID, input.Evidence, MutationActionCreate); err != nil {
		return err
	}
	if err := validateTitle(input.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.Slug) || len(input.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, input.Slug)
	}
	if err := input.Visibility.Validate(); err != nil {
		return err
	}
	_, err := input.ValidatedPayload()
	return err
}

// ValidatedPayload returns the one canonical authored payload for create. A
// caller may provide either Payload or Spec for convenience; providing both is
// allowed only when they serialize to the exact same canonical payload. The
// returned value is defensive and safe to pass to CreateInput.
func (input CreateRequest) ValidatedPayload() (ExplorationSpecPayload, error) {
	return validatedRequestPayload(input.Payload, input.Spec)
}

// UpdateVersionRequest is the application-facing version-append boundary.
// Implementations derive the next revision metadata, semantic model, and
// serving identity from the authorized current state and active graph rather
// than accepting those values as client-owned fields.
func (input UpdateVersionRequest) Validate() error {
	if err := validateMutationRequestIdentity(input.ProjectID, input.ID, input.ActorID, input.Evidence, MutationActionUpdate); err != nil {
		return err
	}
	if err := input.ExpectedRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := validateTitle(input.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.Slug) || len(input.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, input.Slug)
	}
	if err := input.Visibility.Validate(); err != nil {
		return err
	}
	_, err := input.ValidatedPayload()
	return err
}

// ValidatedPayload returns the one canonical authored payload for update. See
// CreateRequest.ValidatedPayload for the dual-source equality rule.
func (input UpdateVersionRequest) ValidatedPayload() (ExplorationSpecPayload, error) {
	return validatedRequestPayload(input.Payload, input.Spec)
}

// DuplicateRequest is the application-facing copy boundary. The destination
// payload and semantic model are intentionally absent: the service must load
// the exact authorized source revision and derive both values from it.
func (input DuplicateRequest) Validate() error {
	if err := validateMutationRequestIdentity(input.ProjectID, input.SourceID, input.ActorID, input.Evidence, MutationActionDuplicate); err != nil {
		return err
	}
	if err := input.ExpectedSourceRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := input.ID.Validate(); err != nil {
		return err
	}
	if input.ID == input.SourceID {
		return fmt.Errorf("%w: duplicate destination ID must be new", ErrConflict)
	}
	if err := validateTitle(input.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(input.Slug) || len(input.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, input.Slug)
	}
	return input.Visibility.Validate()
}

func (input ReadRequest) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := input.ID.Validate(); err != nil {
		return err
	}
	return validateSubjectID(input.ActorID, maxOwnerLength, "actor id")
}

func (input ListRequest) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := validateSubjectID(input.ActorID, maxOwnerLength, "actor id"); err != nil {
		return err
	}
	if input.Limit < 0 || input.Limit > MaxListLimit {
		return fmt.Errorf("%w: list limit must be between 0 and %d", ErrInvalid, MaxListLimit)
	}
	if len(input.PageToken) > maxListCursorLen {
		return fmt.Errorf("%w: list page token is too long", ErrInvalid)
	}
	return nil
}

func (input ArchiveRequest) Validate() error {
	if err := validateMutationRequestIdentity(input.ProjectID, input.ID, input.ActorID, input.Evidence, MutationActionArchive); err != nil {
		return err
	}
	return input.ExpectedRevision.ValidateComplete()
}

func validateMutationRequestIdentity(projectID projectgraph.ResourceID, id ExplorationID, actorID string, evidence MutationEvidence, action MutationAction) error {
	if err := projectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := id.Validate(); err != nil {
		return err
	}
	if err := validateSubjectID(actorID, maxOwnerLength, "actor id"); err != nil {
		return err
	}
	if err := validateMutationEvidence(evidence, action); err != nil {
		return err
	}
	if actorID != evidence.ActorID {
		return fmt.Errorf("%w: request actor must equal mutation evidence actor", ErrInvalid)
	}
	return nil
}

func validatedRequestPayload(payload ExplorationSpecPayload, spec canonical.ExplorationSpec) (ExplorationSpecPayload, error) {
	payloadPresent := payload.version != 0 || payload.canonical != nil || payload.digest != ""
	specPresent := authoredSpecPresent(spec)
	if !payloadPresent && !specPresent {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: authored payload or spec is required", ErrInvalidPayload)
	}

	if payloadPresent {
		if err := payload.Validate(); err != nil {
			return ExplorationSpecPayload{}, err
		}
	}
	if !specPresent {
		return payload.Clone(), nil
	}
	fromSpec, err := NewExplorationSpecPayload(spec)
	if err != nil {
		return ExplorationSpecPayload{}, err
	}
	if !payloadPresent {
		return fromSpec, nil
	}
	if !bytes.Equal(payload.Canonical(), fromSpec.Canonical()) || payload.ContentHash() != fromSpec.ContentHash() {
		return ExplorationSpecPayload{}, fmt.Errorf("%w: payload and spec are not canonically equal", ErrConflict)
	}
	return payload.Clone(), nil
}

func authoredSpecPresent(spec canonical.ExplorationSpec) bool {
	return spec.SchemaVersion != 0 || spec.ModelID != "" || spec.DatasetID != nil ||
		spec.Dimensions != nil || spec.Metrics != nil || spec.Filters != nil ||
		spec.Time != nil || spec.Sort != nil || spec.Limit != 0 ||
		spec.Pivot != nil || spec.Table != nil || spec.Visualization != nil
}

func (input CreateInput) validateWithoutEvidence() error {
	_, err := NewSavedExploration(NewInput{
		ProjectID: input.ProjectID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID,
		Title: input.Title, Slug: input.Slug, Visibility: input.Visibility,
		SemanticModelID: input.SemanticModelID, CreatedAt: input.CreatedAt, Revision: input.Revision,
	})
	return err
}

// Repository is the storage-independent persistence port for saved
// explorations. GetLifecycle and List never hydrate a spec; authorization can
// inspect owner, visibility, model, and current token before GetRevision.
// Mutation methods own transactionality, project/slug uniqueness, historical
// revision retention, and CAS enforcement.
type Repository interface {
	Create(context.Context, CreateInput) (MutationResult, error)
	LookupMutation(context.Context, MutationLookupInput) (MutationReplayMetadata, bool, error)
	GetLifecycle(context.Context, ReadInput) (Lifecycle, error)
	GetRevision(context.Context, RevisionReadInput) (Revision, error)
	ListPage(context.Context, ListInput) (ListPage, error)
	UpdateVersion(context.Context, UpdateVersionInput) (MutationResult, error)
	Duplicate(context.Context, DuplicateInput) (MutationResult, error)
	List(context.Context, ListInput) ([]Lifecycle, error)
	Archive(context.Context, ArchiveInput) (MutationResult, error)
}

// LifecycleRepository is the metadata-only portion useful to authorization
// services that intentionally do not gain revision payload access.
type LifecycleRepository interface {
	GetLifecycle(context.Context, ReadInput) (Lifecycle, error)
	ListPage(context.Context, ListInput) (ListPage, error)
	List(context.Context, ListInput) ([]Lifecycle, error)
}

// RevisionRepository is the exact-version payload portion. Callers should
// invoke it only after authorizing the corresponding Lifecycle.
type RevisionRepository interface {
	GetRevision(context.Context, RevisionReadInput) (Revision, error)
}

// MutationLookupInput addresses one actor-scoped retry identity. Fingerprint
// must match the original request; a mismatch is a command-reuse conflict.
type MutationLookupInput struct {
	ProjectID      projectgraph.ResourceID
	ActorID        string
	Action         MutationAction
	IdempotencyKey string
	Fingerprint    string
}

func (input MutationLookupInput) Validate() error {
	if err := input.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := validateSubjectID(input.ActorID, maxOwnerLength, "mutation actor id"); err != nil {
		return err
	}
	if err := validateBoundedText(input.IdempotencyKey, MaxIdempotencyKeyLength, "mutation idempotency key"); err != nil {
		return err
	}
	if !input.Action.Valid() || !sha256Pattern.MatchString(input.Fingerprint) {
		return fmt.Errorf("%w: mutation lookup identity is invalid", ErrInvalid)
	}
	return nil
}

// MutationReplayMetadata is the operation-ledger projection used to decide
// whether a mutation request is an idempotent replay. It intentionally has no
// authored revision or payload: callers must authorize this lifecycle first,
// then load AppliedRevision through RevisionRepository when a revision is
// required by the operation.
type MutationReplayMetadata struct {
	Lifecycle       Lifecycle        `json:"lifecycle"`
	AppliedRevision RevisionToken    `json:"appliedRevision"`
	Evidence        MutationEvidence `json:"evidence"`
}

func (metadata MutationReplayMetadata) Validate() error {
	if err := metadata.Lifecycle.Validate(); err != nil {
		return err
	}
	if err := metadata.AppliedRevision.ValidateComplete(); err != nil {
		return err
	}
	if err := metadata.Evidence.Validate(); err != nil {
		return err
	}
	if !sameRevisionToken(metadata.Lifecycle.CurrentRevision.Token(), metadata.AppliedRevision) {
		return fmt.Errorf("%w: replay metadata lifecycle does not describe applied revision", ErrConflict)
	}
	switch metadata.Evidence.Action {
	case MutationActionArchive:
		if metadata.Lifecycle.Status != StatusArchived {
			return fmt.Errorf("%w: archive replay metadata must be archived", ErrInvalid)
		}
	case MutationActionCreate, MutationActionUpdate, MutationActionDuplicate:
		if metadata.Lifecycle.Status != StatusActive {
			return fmt.Errorf("%w: non-archive replay metadata must be active", ErrInvalid)
		}
		if (metadata.Evidence.Action == MutationActionCreate || metadata.Evidence.Action == MutationActionDuplicate) && metadata.Lifecycle.OwnerPrincipalID != metadata.Evidence.ActorID {
			return fmt.Errorf("%w: create or duplicate replay owner must equal mutation actor", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported replay mutation action %q", ErrInvalid, metadata.Evidence.Action)
	}
	return nil
}

func (metadata MutationReplayMetadata) Clone() MutationReplayMetadata {
	clone := metadata
	if metadata.Lifecycle.ArchivedAt != nil {
		archivedAt := *metadata.Lifecycle.ArchivedAt
		clone.Lifecycle.ArchivedAt = &archivedAt
	}
	return clone
}

// MutationResult returns the exact immutable result of a mutation. On replay,
// Lifecycle and Revision describe the original mutation rather than whatever
// later state happens to be current; Replayed identifies that no new version
// was appended. Revision is nil only for archive, which records its exact
// AppliedRevision token in the result. ConcurrencyRevision is the canonical
// revision that the repository matched for the mutation's CAS. It is kept
// separate from AppliedRevision because an update appends a new revision after
// matching the caller's precondition. For a durable replay, the application
// service supplies the original validated precondition because no new CAS is
// performed. Evidence is the original validated actor-scoped retry/audit
// identity, so a replay cannot be misrepresented as a later read of current
// state.
type MutationResult struct {
	Lifecycle           Lifecycle        `json:"lifecycle"`
	Revision            *Revision        `json:"revision,omitempty"`
	AppliedRevision     RevisionToken    `json:"appliedRevision"`
	ConcurrencyRevision RevisionToken    `json:"concurrencyRevision"`
	Evidence            MutationEvidence `json:"evidence"`
	Replayed            bool             `json:"replayed"`
}

func (result MutationResult) Validate() error {
	if err := (MutationReplayMetadata{Lifecycle: result.Lifecycle, AppliedRevision: result.AppliedRevision, Evidence: result.Evidence}).Validate(); err != nil {
		return err
	}
	switch result.Evidence.Action {
	case MutationActionArchive:
		if err := result.ConcurrencyRevision.ValidateComplete(); err != nil {
			return fmt.Errorf("%w: archive mutation result concurrency revision: %v", ErrInvalidRevision, err)
		}
		if result.Revision != nil {
			return fmt.Errorf("%w: archive mutation result must not append a revision", ErrInvalidRevision)
		}
	case MutationActionCreate, MutationActionUpdate, MutationActionDuplicate:
		if result.Evidence.Action == MutationActionCreate {
			if !result.ConcurrencyRevision.IsZero() {
				return fmt.Errorf("%w: create mutation result must not have a concurrency revision", ErrInvalidRevision)
			}
		} else if err := result.ConcurrencyRevision.ValidateComplete(); err != nil {
			return fmt.Errorf("%w: mutation result concurrency revision: %v", ErrInvalidRevision, err)
		}
		if result.Lifecycle.Status != StatusActive {
			return fmt.Errorf("%w: non-archive mutation result must be active", ErrInvalid)
		}
		if result.Revision == nil {
			return fmt.Errorf("%w: non-archive mutation result requires a revision", ErrInvalidRevision)
		}
		if _, err := Open(result.Lifecycle, *result.Revision); err != nil {
			return err
		}
		if result.Revision.Metadata.CreatedBy != result.Evidence.ActorID {
			return fmt.Errorf("%w: mutation result revision creator must equal mutation actor", ErrInvalid)
		}
		if (result.Evidence.Action == MutationActionCreate || result.Evidence.Action == MutationActionDuplicate) &&
			result.Lifecycle.OwnerPrincipalID != result.Evidence.ActorID {
			return fmt.Errorf("%w: create or duplicate result owner must equal mutation actor", ErrInvalid)
		}
	}
	return nil
}

func (result MutationResult) Clone() MutationResult {
	clone := result
	if result.Revision != nil {
		revision := result.Revision.Clone()
		clone.Revision = &revision
	}
	return clone
}

// CreateRequest is the application-facing create port. The authenticated
// ActorID is the sole owner authority; adapters must not accept an owner
// override. Payload is preferred; Spec is provided as a convenience for a
// caller that has not crossed the canonical payload boundary yet. The model ID
// is derived from that validated spec rather than accepted independently.
type CreateRequest struct {
	ProjectID  projectgraph.ResourceID
	ID         ExplorationID
	ActorID    string
	Title      string
	Slug       string
	Visibility Visibility
	Payload    ExplorationSpecPayload
	Spec       canonical.ExplorationSpec
	Evidence   MutationEvidence
}

// ReadRequest carries caller context separately from the project-bound key.
// Authorization belongs to the composing application; this package only
// defines the stable service shape and non-enumerating error contract.
type ReadRequest struct {
	ProjectID projectgraph.ResourceID
	ID        ExplorationID
	ActorID   string
}

// UpdateVersionRequest appends one version using an exact revision token and
// explicit next identity metadata.
type UpdateVersionRequest struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ActorID          string
	ExpectedRevision RevisionToken
	Title            string
	Slug             string
	Visibility       Visibility
	Payload          ExplorationSpecPayload
	Spec             canonical.ExplorationSpec
	Evidence         MutationEvidence
}

// DuplicateRequest copies a source's current authored payload into a new
// stable project-bound identity. The destination defaults are intentionally
// explicit so adapters can apply their visibility policy before persistence.
type DuplicateRequest struct {
	ProjectID              projectgraph.ResourceID
	SourceID               ExplorationID
	ExpectedSourceRevision RevisionToken
	ID                     ExplorationID
	ActorID                string
	Title                  string
	Slug                   string
	Visibility             Visibility
	Evidence               MutationEvidence
}

type ListRequest struct {
	ProjectID       projectgraph.ResourceID
	ActorID         string
	IncludeArchived bool
	// Limit is the number of authorized rows requested. Zero asks for all
	// visible rows (used by the browser bootstrap); HTTP handlers provide the
	// public default explicitly.
	Limit     int
	PageToken string
}

type ArchiveRequest struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	ActorID          string
	ExpectedRevision RevisionToken
	Evidence         MutationEvidence
}

// ReopenRequest asks the service to reopen the current authored revision as a
// detached working copy. Reopening is intentionally read-only: callers may
// edit the returned Spec and must submit a separate update to persist it.
type ReopenRequest struct {
	ProjectID projectgraph.ResourceID
	ID        ExplorationID
	ActorID   string
}

func (input ReopenRequest) Validate() error {
	return (ReadRequest{ProjectID: input.ProjectID, ID: input.ID, ActorID: input.ActorID}).Validate()
}

// ReopenResult is the editable, detached view returned by Reopen. Revision
// retains the exact immutable source metadata while Spec is a value copy.
type ReopenResult struct {
	Lifecycle Lifecycle                 `json:"lifecycle"`
	Revision  RevisionMetadata          `json:"revision"`
	Spec      canonical.ExplorationSpec `json:"spec"`
}

// ExecuteRequest identifies the current revision to execute for one actor.
// Request and correlation IDs are copied into the governed query metadata by
// the application service; they are not part of the authored payload.
type ExecuteRequest struct {
	ProjectID     projectgraph.ResourceID
	ID            ExplorationID
	ActorID       string
	RequestID     string
	CorrelationID string
}

func (input ExecuteRequest) Validate() error {
	if err := (ReadRequest{ProjectID: input.ProjectID, ID: input.ID, ActorID: input.ActorID}).Validate(); err != nil {
		return err
	}
	if input.RequestID != "" {
		if err := validateBoundedText(input.RequestID, MaxRequestIDLength, "execute request id"); err != nil {
			return err
		}
	}
	if input.CorrelationID != "" {
		if err := validateBoundedText(input.CorrelationID, MaxCorrelationIDLength, "execute correlation id"); err != nil {
			return err
		}
	}
	return nil
}

// ExecutionEvidence binds a result to the exact active serving lease and
// immutable authored revision that produced it. It deliberately carries no
// creator credential or frozen result data.
type ExecutionEvidence struct {
	ActorID         string                       `json:"actorId"`
	Revision        RevisionToken                `json:"revision"`
	ServingIdentity projectgraph.ServingIdentity `json:"servingIdentity"`
}

// ExecuteResult contains a governed result and its immutable execution
// evidence. Query is included for adapters that need to inspect the lowered
// request, while Result remains the executor-owned response.
type ExecuteResult struct {
	Lifecycle Lifecycle         `json:"lifecycle"`
	Revision  Revision          `json:"revision"`
	Query     dataquery.Query   `json:"query"`
	Result    dataquery.Result  `json:"result"`
	Evidence  ExecutionEvidence `json:"evidence"`
}

// Service is the storage-independent application port. Implementations may
// perform authorization between GetLifecycle and GetRevision; callers must
// map both absent and denied IDs to ErrNotFound at their external boundary.
type Service interface {
	Create(context.Context, CreateRequest) (MutationResult, error)
	Read(context.Context, ReadRequest) (OpenResult, error)
	UpdateVersion(context.Context, UpdateVersionRequest) (MutationResult, error)
	Duplicate(context.Context, DuplicateRequest) (MutationResult, error)
	List(context.Context, ListRequest) ([]Lifecycle, error)
	ListPage(context.Context, ListRequest) (ListPage, error)
	Archive(context.Context, ArchiveRequest) (MutationResult, error)
	Reopen(context.Context, ReopenRequest) (ReopenResult, error)
	Execute(context.Context, ExecuteRequest) (ExecuteResult, error)
}
