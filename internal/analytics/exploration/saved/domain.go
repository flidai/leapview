package saved

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalid            = errors.New("invalid saved exploration")
	ErrInvalidIdentifier  = errors.New("invalid saved exploration identifier")
	ErrInvalidPayload     = errors.New("invalid saved exploration payload")
	ErrUnsupportedVersion = errors.New("unsupported saved exploration version")
	ErrPayloadTooLarge    = errors.New("saved exploration payload too large")
	ErrNotFound           = errors.New("saved exploration not found")
	ErrUnauthorized       = errors.New("saved exploration unauthorized")
	ErrConflict           = errors.New("saved exploration conflict")
	ErrAlreadyExists      = errors.New("saved exploration already exists")
	ErrStaleRevision      = errors.New("saved exploration revision is stale")
	ErrInvalidRevision    = errors.New("invalid saved exploration revision")
	ErrArchived           = errors.New("saved exploration is archived")
	ErrUnavailable        = errors.New("saved exploration repository unavailable")
)

const (
	MaxIdentifierLength = 128
	MaxOwnerLength      = 256
	MaxSlugLength       = 128
	MaxTitleLength      = 200

	maxOwnerLength = MaxOwnerLength
	maxSlugLength  = MaxSlugLength
	maxTitleLength = MaxTitleLength
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	slugPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)
	sha256Pattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// ExplorationID is stable within a project. It must not be derived from a
// title or slug: those are user-editable lookup metadata, while this identity
// remains fixed for the entire saved-exploration lifecycle.
type ExplorationID string

// SavedExplorationID documents the aggregate this ID belongs to.
type SavedExplorationID = ExplorationID

func (id ExplorationID) String() string { return string(id) }
func (id ExplorationID) Validate() error {
	if !identifierPattern.MatchString(string(id)) {
		return fmt.Errorf("%w: exploration id %q", ErrInvalidIdentifier, id)
	}
	return nil
}

// RevisionID identifies one immutable version and is not reused across
// updates, even when the payload bytes happen to be unchanged.
type RevisionID string

func (id RevisionID) String() string { return string(id) }
func (id RevisionID) Validate() error {
	if !identifierPattern.MatchString(string(id)) {
		return fmt.Errorf("%w: revision id %q", ErrInvalidIdentifier, id)
	}
	return nil
}

type Visibility string

const (
	VisibilityPrivate      Visibility = "private"
	VisibilityRestricted   Visibility = "restricted"
	VisibilityOrganization Visibility = "organization"
)

func (v Visibility) Valid() bool {
	return v == VisibilityPrivate || v == VisibilityRestricted || v == VisibilityOrganization
}
func (v Visibility) Validate() error {
	if !v.Valid() {
		return fmt.Errorf("%w: unsupported visibility %q", ErrInvalid, v)
	}
	return nil
}

// Status describes the lifecycle pointer, not the immutable revision. An
// archived resource retains its latest revision for audit and duplication
// policy decisions, but cannot receive a new version.
type Status string

const (
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

// LifecycleStatus is a descriptive alias for callers that use lifecycle
// terminology elsewhere in the application.
type LifecycleStatus = Status

func (s Status) Valid() bool { return s == StatusActive || s == StatusArchived }
func (s Status) Validate() error {
	if !s.Valid() {
		return fmt.Errorf("%w: unsupported status %q", ErrInvalid, s)
	}
	return nil
}

// RevisionToken is the complete optimistic-concurrency token. All three
// members are required for a non-zero token; adapters must pass the complete
// value to CAS repository methods rather than selecting the latest revision.
type RevisionToken struct {
	RevisionID  RevisionID `json:"revisionId"`
	Number      uint64     `json:"number"`
	ContentHash string     `json:"contentHash"`
}

func (t RevisionToken) IsZero() bool {
	return t.RevisionID == "" && t.Number == 0 && t.ContentHash == ""
}

func (t RevisionToken) Validate() error {
	if t.IsZero() {
		return nil
	}
	if err := t.RevisionID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if t.Number == 0 {
		return fmt.Errorf("%w: revision number is required", ErrInvalidRevision)
	}
	if !sha256Pattern.MatchString(t.ContentHash) {
		return fmt.Errorf("%w: content hash is invalid", ErrInvalidRevision)
	}
	return nil
}

func (t RevisionToken) ValidateComplete() error {
	if t.IsZero() {
		return fmt.Errorf("%w: revision token is required", ErrInvalidRevision)
	}
	return t.Validate()
}

// RevisionMetadata is immutable metadata retained alongside one payload. It
// deliberately contains no query result, SQL, compiled envelope, or UI state.
type RevisionMetadata struct {
	ID              RevisionID                   `json:"id"`
	Number          uint64                       `json:"number"`
	ContentHash     string                       `json:"contentHash"`
	CreatedAt       time.Time                    `json:"createdAt"`
	CreatedBy       string                       `json:"createdBy"`
	ServingIdentity projectgraph.ServingIdentity `json:"servingIdentity"`
}

func (m RevisionMetadata) Token() RevisionToken {
	return RevisionToken{RevisionID: m.ID, Number: m.Number, ContentHash: m.ContentHash}
}

func (m RevisionMetadata) Validate() error {
	if err := m.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if m.Number == 0 {
		return fmt.Errorf("%w: revision number is required", ErrInvalidRevision)
	}
	if !sha256Pattern.MatchString(m.ContentHash) {
		return fmt.Errorf("%w: content hash is invalid", ErrInvalidRevision)
	}
	if err := validateUTCTimestamp(m.CreatedAt, "revision createdAt"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if err := validateSubjectID(m.CreatedBy, maxOwnerLength, "revision createdBy"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRevision, err)
	}
	if err := m.ServingIdentity.Validate(); err != nil {
		return fmt.Errorf("%w: serving identity: %v", ErrInvalidRevision, err)
	}
	return nil
}

// Revision couples immutable metadata with one immutable canonical payload.
type Revision struct {
	Metadata RevisionMetadata       `json:"metadata"`
	Payload  ExplorationSpecPayload `json:"payload"`
}

// NewRevision constructs a complete immutable version from a validated
// canonical payload. Revision metadata is copied into the returned value.
func NewRevision(id RevisionID, number uint64, createdAt time.Time, createdBy string, payload ExplorationSpecPayload, serving projectgraph.ServingIdentity) (Revision, error) {
	revision := Revision{
		Metadata: RevisionMetadata{ID: id, Number: number, ContentHash: payload.ContentHash(), CreatedAt: createdAt, CreatedBy: createdBy, ServingIdentity: serving},
		Payload:  payload.Clone(),
	}
	if err := revision.Validate(); err != nil {
		return Revision{}, err
	}
	return revision, nil
}

func (r Revision) Token() RevisionToken { return r.Metadata.Token() }
func (r Revision) Validate() error {
	if err := r.Metadata.Validate(); err != nil {
		return err
	}
	if err := r.Payload.Validate(); err != nil {
		return err
	}
	if r.Metadata.ContentHash != r.Payload.ContentHash() {
		return fmt.Errorf("%w: revision hash does not match payload", ErrInvalidRevision)
	}
	return nil
}

// NewInput is the complete identity and first-revision input for a new
// aggregate. New records always begin active and cannot be born archived.
type NewInput struct {
	ProjectID        projectgraph.ResourceID
	ID               ExplorationID
	OwnerPrincipalID string
	Title            string
	Slug             string
	Visibility       Visibility
	SemanticModelID  projectgraph.ResourceID
	CreatedAt        time.Time
	Revision         Revision
}

// NewSavedExploration validates a complete aggregate before it crosses a
// repository boundary. The first revision timestamp must equal CreatedAt.
func NewSavedExploration(input NewInput) (SavedExploration, error) {
	if err := validateTitle(input.Title); err != nil {
		return SavedExploration{}, err
	}
	record := SavedExploration{
		ProjectID: input.ProjectID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID,
		Title: input.Title, Slug: input.Slug, Visibility: input.Visibility, SemanticModelID: input.SemanticModelID, Status: StatusActive,
		CreatedAt: input.CreatedAt, UpdatedAt: input.CreatedAt, Revision: input.Revision.Clone(),
	}
	if !record.Revision.Metadata.CreatedAt.Equal(input.CreatedAt) {
		return SavedExploration{}, fmt.Errorf("%w: first revision timestamp must equal createdAt", ErrInvalid)
	}
	if record.Revision.Metadata.Number != 1 {
		return SavedExploration{}, fmt.Errorf("%w: initial revision number must be 1", ErrInvalidRevision)
	}
	if err := record.Validate(); err != nil {
		return SavedExploration{}, err
	}
	return record, nil
}

// Lifecycle is the metadata-only read model used for authorization and list
// operations. It includes the current semantic model and complete revision
// token, but deliberately does not include the ExplorationSpec payload.
type Lifecycle struct {
	ProjectID        projectgraph.ResourceID `json:"projectId"`
	ID               ExplorationID           `json:"id"`
	OwnerPrincipalID string                  `json:"ownerPrincipalId"`
	Title            string                  `json:"title"`
	Slug             string                  `json:"slug"`
	Visibility       Visibility              `json:"visibility"`
	SemanticModelID  projectgraph.ResourceID `json:"semanticModelId"`
	Status           Status                  `json:"status"`
	CreatedAt        time.Time               `json:"createdAt"`
	UpdatedAt        time.Time               `json:"updatedAt"`
	ArchivedAt       *time.Time              `json:"archivedAt,omitempty"`
	CurrentRevision  RevisionMetadata        `json:"currentRevision"`
}

func (l Lifecycle) Validate() error {
	if err := l.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := l.ID.Validate(); err != nil {
		return err
	}
	if err := validateSubjectID(l.OwnerPrincipalID, maxOwnerLength, "owner principal id"); err != nil {
		return err
	}
	if err := validateTitle(l.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(l.Slug) || len(l.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, l.Slug)
	}
	if err := l.Visibility.Validate(); err != nil {
		return err
	}
	if err := l.SemanticModelID.Validate(); err != nil {
		return fmt.Errorf("%w: semantic model id: %v", ErrInvalid, err)
	}
	if err := l.Status.Validate(); err != nil {
		return err
	}
	if err := validateUTCTimestamp(l.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if err := validateUTCTimestamp(l.UpdatedAt, "updatedAt"); err != nil {
		return err
	}
	if l.UpdatedAt.Before(l.CreatedAt) {
		return fmt.Errorf("%w: updatedAt precedes createdAt", ErrInvalid)
	}
	if l.ArchivedAt != nil {
		if err := validateUTCTimestamp(*l.ArchivedAt, "archivedAt"); err != nil {
			return err
		}
		if l.Status != StatusArchived || l.ArchivedAt.Before(l.UpdatedAt) {
			return fmt.Errorf("%w: archivedAt and archived status are inconsistent", ErrInvalid)
		}
	} else if l.Status == StatusArchived {
		return fmt.Errorf("%w: archived status requires archivedAt", ErrInvalid)
	}
	if err := l.CurrentRevision.Validate(); err != nil {
		return err
	}
	if l.CurrentRevision.ServingIdentity.ProjectID != l.ProjectID {
		return fmt.Errorf("%w: revision serving identity project does not match lifecycle project", ErrInvalid)
	}
	if l.CurrentRevision.CreatedAt.Before(l.CreatedAt) || l.CurrentRevision.CreatedAt.After(l.UpdatedAt) {
		return fmt.Errorf("%w: revision timestamp is outside lifecycle timestamps", ErrInvalid)
	}
	return nil
}

// Lifecycle returns the metadata-only projection of an opened aggregate.
func (e SavedExploration) Lifecycle() Lifecycle {
	return Lifecycle{
		ProjectID: e.ProjectID, ID: e.ID, OwnerPrincipalID: e.OwnerPrincipalID,
		Title: e.Title, Slug: e.Slug, Visibility: e.Visibility, SemanticModelID: e.SemanticModelID,
		Status: e.Status, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		ArchivedAt: cloneTime(e.ArchivedAt), CurrentRevision: e.Revision.Metadata,
	}
}

// OpenResult pairs an authorized metadata read with an exact revision read.
// Keeping these distinct lets adapters authorize against Lifecycle before
// decoding a potentially incompatible or malformed payload.
type OpenResult struct {
	Lifecycle Lifecycle `json:"lifecycle"`
	Revision  Revision  `json:"revision"`
}

func Open(lifecycle Lifecycle, revision Revision) (OpenResult, error) {
	if err := lifecycle.Validate(); err != nil {
		return OpenResult{}, err
	}
	if err := revision.Validate(); err != nil {
		return OpenResult{}, err
	}
	if !sameRevisionToken(lifecycle.CurrentRevision.Token(), revision.Token()) {
		return OpenResult{}, ErrStaleRevision
	}
	if !sameRevisionMetadata(lifecycle.CurrentRevision, revision.Metadata) {
		return OpenResult{}, ErrStaleRevision
	}
	if revision.Metadata.ServingIdentity.ProjectID != lifecycle.ProjectID {
		return OpenResult{}, fmt.Errorf("%w: revision serving identity project does not match lifecycle project", ErrInvalid)
	}
	spec, err := revision.Payload.Spec()
	if err != nil {
		return OpenResult{}, err
	}
	if projectgraph.ResourceID(spec.ModelID) != lifecycle.SemanticModelID {
		return OpenResult{}, fmt.Errorf("%w: semantic model id does not match revision spec", ErrInvalid)
	}
	return OpenResult{Lifecycle: lifecycle, Revision: revision.Clone()}, nil
}

func sameRevisionMetadata(left, right RevisionMetadata) bool {
	return left.ID == right.ID && left.Number == right.Number && left.ContentHash == right.ContentHash &&
		left.CreatedAt.Equal(right.CreatedAt) && left.CreatedBy == right.CreatedBy && left.ServingIdentity == right.ServingIdentity
}

func (o OpenResult) Saved() (SavedExploration, error) {
	opened, err := Open(o.Lifecycle, o.Revision)
	if err != nil {
		return SavedExploration{}, err
	}
	return SavedExploration{
		ProjectID: opened.Lifecycle.ProjectID, ID: opened.Lifecycle.ID,
		OwnerPrincipalID: opened.Lifecycle.OwnerPrincipalID, Title: opened.Lifecycle.Title,
		Slug: opened.Lifecycle.Slug, Visibility: opened.Lifecycle.Visibility,
		SemanticModelID: opened.Lifecycle.SemanticModelID, Status: opened.Lifecycle.Status,
		CreatedAt: opened.Lifecycle.CreatedAt, UpdatedAt: opened.Lifecycle.UpdatedAt,
		ArchivedAt: cloneTime(opened.Lifecycle.ArchivedAt), Revision: opened.Revision.Clone(),
	}, nil
}

// SavedExploration is the mutable identity/lifecycle envelope around an
// immutable current revision. Historical revisions remain a repository concern
// and are addressed by their exact RevisionToken.
type SavedExploration struct {
	ProjectID        projectgraph.ResourceID `json:"projectId"`
	ID               ExplorationID           `json:"id"`
	OwnerPrincipalID string                  `json:"ownerPrincipalId"`
	Title            string                  `json:"title"`
	Slug             string                  `json:"slug"`
	Visibility       Visibility              `json:"visibility"`
	SemanticModelID  projectgraph.ResourceID `json:"semanticModelId"`
	Status           Status                  `json:"status"`
	CreatedAt        time.Time               `json:"createdAt"`
	UpdatedAt        time.Time               `json:"updatedAt"`
	ArchivedAt       *time.Time              `json:"archivedAt,omitempty"`
	Revision         Revision                `json:"revision"`
}

func (e SavedExploration) Validate() error {
	if err := e.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalid, err)
	}
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if err := validateSubjectID(e.OwnerPrincipalID, maxOwnerLength, "owner principal id"); err != nil {
		return err
	}
	if err := validateTitle(e.Title); err != nil {
		return err
	}
	if !slugPattern.MatchString(e.Slug) || len(e.Slug) > maxSlugLength {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalid, e.Slug)
	}
	if err := e.Visibility.Validate(); err != nil {
		return err
	}
	if err := e.Status.Validate(); err != nil {
		return err
	}
	if err := validateUTCTimestamp(e.CreatedAt, "createdAt"); err != nil {
		return err
	}
	if err := validateUTCTimestamp(e.UpdatedAt, "updatedAt"); err != nil {
		return err
	}
	if e.UpdatedAt.Before(e.CreatedAt) {
		return fmt.Errorf("%w: updatedAt precedes createdAt", ErrInvalid)
	}
	if e.ArchivedAt != nil {
		if err := validateUTCTimestamp(*e.ArchivedAt, "archivedAt"); err != nil {
			return err
		}
		if e.Status != StatusArchived {
			return fmt.Errorf("%w: archivedAt requires archived status", ErrInvalid)
		}
		if e.ArchivedAt.Before(e.UpdatedAt) {
			return fmt.Errorf("%w: archivedAt precedes updatedAt", ErrInvalid)
		}
	} else if e.Status == StatusArchived {
		return fmt.Errorf("%w: archived status requires archivedAt", ErrInvalid)
	}
	if err := e.Revision.Validate(); err != nil {
		return err
	}
	if e.Revision.Metadata.ServingIdentity.ProjectID != e.ProjectID {
		return fmt.Errorf("%w: revision serving identity project does not match exploration project", ErrInvalid)
	}
	if err := e.SemanticModelID.Validate(); err != nil {
		return fmt.Errorf("%w: semantic model id: %v", ErrInvalid, err)
	}
	spec, err := e.Revision.Payload.Spec()
	if err != nil {
		return err
	}
	if projectgraph.ResourceID(spec.ModelID) != e.SemanticModelID {
		return fmt.Errorf("%w: semantic model id %q does not match revision spec model %q", ErrInvalid, e.SemanticModelID, spec.ModelID)
	}
	if e.Revision.Metadata.CreatedAt.Before(e.CreatedAt) || e.Revision.Metadata.CreatedAt.After(e.UpdatedAt) {
		return fmt.Errorf("%w: revision timestamp is outside lifecycle timestamps", ErrInvalid)
	}
	return nil
}

// IsArchived reports the lifecycle state without exposing mutable internals.
func (e SavedExploration) IsArchived() bool { return e.Status == StatusArchived || e.ArchivedAt != nil }

// OwnerID is a short alias for callers whose principal model uses ownerID
// terminology rather than ownerPrincipalID.
func (e SavedExploration) OwnerID() string { return e.OwnerPrincipalID }

// Clone returns an aggregate copy safe for a repository or service boundary.
func (e SavedExploration) Clone() SavedExploration {
	clone := e
	clone.Revision.Payload = e.Revision.Payload.Clone()
	if e.ArchivedAt != nil {
		archived := *e.ArchivedAt
		clone.ArchivedAt = &archived
	}
	return clone
}

func (r Revision) Clone() Revision {
	r.Payload = r.Payload.Clone()
	return r
}

func sameRevisionToken(left, right RevisionToken) bool {
	return left.RevisionID == right.RevisionID && left.Number == right.Number && left.ContentHash == right.ContentHash
}

// AppendVersion applies one exact-token version transition to an aggregate.
// Repository implementations should perform this same check atomically in
// their UpdateVersion method; this helper keeps the domain invariants explicit
// for adapters and deterministic tests.
func AppendVersion(current SavedExploration, expected RevisionToken, next Revision, updatedAt time.Time) (SavedExploration, error) {
	return AppendVersionWithMetadata(current, expected, next, updatedAt, current.Title, current.Slug, current.Visibility, current.SemanticModelID)
}

// AppendVersionWithMetadata applies an exact-token version transition and an
// explicit next identity presentation. Title, slug, and visibility are
// mutable metadata on the identity envelope, not fields silently inferred from
// the new authored spec.
func AppendVersionWithMetadata(current SavedExploration, expected RevisionToken, next Revision, updatedAt time.Time, title, slug string, visibility Visibility, semanticModelID projectgraph.ResourceID) (SavedExploration, error) {
	return appendVersionWithMetadata(current, expected, next, updatedAt, title, slug, visibility, semanticModelID)
}

func appendVersionWithMetadata(current SavedExploration, expected RevisionToken, next Revision, updatedAt time.Time, title, slug string, visibility Visibility, semanticModelID projectgraph.ResourceID) (SavedExploration, error) {
	if err := current.Validate(); err != nil {
		return SavedExploration{}, err
	}
	if current.IsArchived() {
		return SavedExploration{}, ErrArchived
	}
	if err := expected.ValidateComplete(); err != nil {
		return SavedExploration{}, err
	}
	if !sameRevisionToken(expected, current.Revision.Token()) {
		return SavedExploration{}, ErrStaleRevision
	}
	if updatedAt.Before(current.UpdatedAt) {
		return SavedExploration{}, fmt.Errorf("%w: updatedAt precedes current updatedAt", ErrInvalid)
	}
	if err := next.Validate(); err != nil {
		return SavedExploration{}, err
	}
	if next.Metadata.Number != current.Revision.Metadata.Number+1 {
		return SavedExploration{}, fmt.Errorf("%w: next revision number must be current + 1", ErrInvalidRevision)
	}
	if next.Metadata.ID == current.Revision.Metadata.ID {
		return SavedExploration{}, fmt.Errorf("%w: revision id must be new", ErrInvalidRevision)
	}
	if !next.Metadata.CreatedAt.Equal(updatedAt) {
		return SavedExploration{}, fmt.Errorf("%w: revision timestamp must equal updatedAt", ErrInvalidRevision)
	}
	if err := validateUTCTimestamp(updatedAt, "updatedAt"); err != nil {
		return SavedExploration{}, err
	}
	updated := current.Clone()
	if err := validateTitle(title); err != nil {
		return SavedExploration{}, err
	}
	if !slugPattern.MatchString(slug) || len(slug) > maxSlugLength {
		return SavedExploration{}, fmt.Errorf("%w: invalid slug %q", ErrInvalid, slug)
	}
	if err := visibility.Validate(); err != nil {
		return SavedExploration{}, err
	}
	if err := semanticModelID.Validate(); err != nil {
		return SavedExploration{}, fmt.Errorf("%w: semantic model id: %v", ErrInvalid, err)
	}
	updated.Title = title
	updated.Slug = slug
	updated.Visibility = visibility
	updated.SemanticModelID = semanticModelID
	updated.UpdatedAt = updatedAt
	updated.Revision = next.Clone()
	if err := updated.Validate(); err != nil {
		return SavedExploration{}, err
	}
	return updated, nil
}

// Archive applies the exact-token archive transition. It does not create a
// new authored revision and never changes the immutable payload metadata.
func Archive(current SavedExploration, expected RevisionToken, archivedAt time.Time) (SavedExploration, error) {
	if err := current.Validate(); err != nil {
		return SavedExploration{}, err
	}
	if current.IsArchived() {
		return SavedExploration{}, ErrArchived
	}
	if err := expected.ValidateComplete(); err != nil {
		return SavedExploration{}, err
	}
	if !sameRevisionToken(expected, current.Revision.Token()) {
		return SavedExploration{}, ErrStaleRevision
	}
	if err := validateUTCTimestamp(archivedAt, "archivedAt"); err != nil {
		return SavedExploration{}, err
	}
	if archivedAt.Before(current.UpdatedAt) {
		return SavedExploration{}, fmt.Errorf("%w: archivedAt precedes updatedAt", ErrInvalid)
	}
	updated := current.Clone()
	updated.Status = StatusArchived
	updated.UpdatedAt = archivedAt
	updated.ArchivedAt = timePtr(archivedAt)
	if err := updated.Validate(); err != nil {
		return SavedExploration{}, err
	}
	return updated, nil
}

func timePtr(value time.Time) *time.Time { return &value }

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateOpaque(value string, max int, kind string) error {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > max || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalid, kind, value)
	}
	return nil
}

func validateTitle(value string) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > maxTitleLength || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%w: title must be 1-%d characters without surrounding whitespace or controls", ErrInvalid, maxTitleLength)
	}
	return nil
}

func validateSubjectID(value string, max int, kind string) error {
	if value == "" || !utf8.ValidString(value) || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "\x00\r\n\t") {
		return fmt.Errorf("%w: invalid %s", ErrInvalid, kind)
	}
	return nil
}

func validateUTCTimestamp(value time.Time, kind string) error {
	if value.IsZero() || value.Location() != time.UTC {
		return fmt.Errorf("%w: %s must be a non-zero UTC timestamp", ErrInvalid, kind)
	}
	return nil
}

// PublicNotFound maps both absent and denied records to one public sentinel.
// Adapters should call this before returning a resource lookup result so an
// unauthorized ID cannot be distinguished from an unknown ID.
func PublicNotFound(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized) {
		return ErrNotFound
	}
	return err
}

// IsNotFoundOrUnauthorized is useful for adapters that need to classify a
// repository result before applying PublicNotFound.
func IsNotFoundOrUnauthorized(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnauthorized)
}
