package authoring

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/dashboard/document"
)

// AuthorizationAction is the domain-level action required to execute an
// authoring command. It deliberately does not depend on the access package;
// adapters can map these actions to their own privileges at the boundary.
type AuthorizationAction string

const (
	AuthorizationActionView    AuthorizationAction = "view"
	AuthorizationActionEdit    AuthorizationAction = "edit"
	AuthorizationActionPublish AuthorizationAction = "publish"
	AuthorizationActionArchive AuthorizationAction = "archive"
)

func (a AuthorizationAction) Valid() bool {
	return a == AuthorizationActionView || a == AuthorizationActionEdit || a == AuthorizationActionPublish || a == AuthorizationActionArchive
}

func (a AuthorizationAction) Validate() error {
	if !a.Valid() {
		return fmt.Errorf("%w: unsupported authorization action %q", ErrInvalidAuthoring, a)
	}
	return nil
}

// CommandEvidence is the complete immutable audit/idempotency record for one
// authoring mutation. It is persisted in the same transaction as the state it
// authorizes, so an accepted edit, publication, or archive always has a
// durable action, provenance, and UTC timestamp.
type CommandEvidence struct {
	ID          CommandID           `json:"id"`
	Fingerprint string              `json:"fingerprint"`
	Action      AuthorizationAction `json:"action"`
	Provenance  Provenance          `json:"provenance"`
	OccurredAt  time.Time           `json:"occurredAt"`
}

func (e CommandEvidence) Validate() error {
	if err := e.ID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.Fingerprint) == "" {
		return fmt.Errorf("%w: command fingerprint is required", ErrInvalidAuthoring)
	}
	if e.Fingerprint != strings.TrimSpace(e.Fingerprint) {
		return fmt.Errorf("%w: command fingerprint cannot have surrounding whitespace", ErrInvalidAuthoring)
	}
	if err := e.Action.Validate(); err != nil {
		return err
	}
	if err := e.Provenance.Validate(); err != nil {
		return err
	}
	if e.OccurredAt.IsZero() || e.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("%w: command occurred_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	return nil
}

// authoringPayload is closed. Command exposes named typed pointers for a
// JSON-capable union while this private method blocks arbitrary patch values.
// Every payload declares its required authorization action.
type authoringPayload interface {
	authoringPayload()
	RequiredAction() (AuthorizationAction, error)
}

// MetadataPatch uses pointers so omitted fields and explicit clears remain
// distinct. Title and semantic model are required values when supplied.
type MetadataPatch struct {
	Title         *string                       `json:"title,omitempty"`
	Description   *string                       `json:"description,omitempty"`
	Slug          *string                       `json:"slug,omitempty"`
	SemanticModel *string                       `json:"semanticModel,omitempty"`
	Visibility    *Visibility                   `json:"visibility,omitempty"`
	Appearance    *document.DashboardAppearance `json:"appearance,omitempty"`
}

// SetVisibilityPayload changes only the dashboard lifecycle visibility. It is
// intentionally separate from MetadataPatch so builder intent handling cannot
// accidentally rewrite authored document metadata while sharing the same
// optimistic, transactional command path.
type SetVisibilityPayload struct {
	Visibility Visibility `json:"visibility"`
}

func (SetVisibilityPayload) authoringPayload() {}
func (SetVisibilityPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// AddPagePayload is a bounded builder intent. Empty ID/title values are
// server-resolved by the reducer to deterministic safe values derived from the
// current document; callers may provide an explicit safe value when restoring
// a prepared intent.
type AddPagePayload struct {
	PageID string `json:"pageId,omitempty"`
	Title  string `json:"title,omitempty"`
}

func (AddPagePayload) authoringPayload() {}
func (AddPagePayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// AddVisualPayload creates a definition and its page component in one reducer
// transaction. The payload contains only closed visual-builder fields; it does
// not accept a caller-supplied authored document or raw query expression.
type AddVisualPayload struct {
	PageID      string `json:"pageId"`
	VisualID    string `json:"visualId,omitempty"`
	ComponentID string `json:"componentId,omitempty"`
	Type        string `json:"type"`
	Title       string `json:"title,omitempty"`
}

func (AddVisualPayload) authoringPayload() {}
func (AddVisualPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type FieldRole string

const (
	FieldRoleMetric    FieldRole = "metric"
	FieldRoleDimension FieldRole = "dimension"
	FieldRoleDetail    FieldRole = "detail"
)

func (r FieldRole) Valid() bool {
	return r == FieldRoleMetric || r == FieldRoleDimension || r == FieldRoleDetail
}

// AssignFieldPayload binds one governed semantic field to one exact placed
// visual component. VisualID is the component identity, not a definition ID,
// so two placements of one definition remain unambiguous.
type AssignFieldPayload struct {
	PageID   string    `json:"pageId"`
	VisualID string    `json:"visualId"`
	FieldID  string    `json:"fieldId"`
	Role     FieldRole `json:"role"`

	// ResolvedTable is populated only by the governed application boundary
	// after it validates FieldID against the active semantic model. It is not a
	// transport field and therefore cannot be supplied by a builder client.
	ResolvedTable string `json:"-"`
}

func (AssignFieldPayload) authoringPayload() {}
func (AssignFieldPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

func (MetadataPatch) authoringPayload() {}
func (MetadataPatch) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type UpsertPagePayload struct {
	Page document.DashboardPage `json:"page"`
}

func (UpsertPagePayload) authoringPayload() {}
func (UpsertPagePayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type RemovePagePayload struct {
	PageID string `json:"pageId"`
}

func (RemovePagePayload) authoringPayload() {}
func (RemovePagePayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type UpsertVisualPayload struct {
	VisualID string                   `json:"visualId"`
	Visual   document.DashboardVisual `json:"visual"`
}

func (UpsertVisualPayload) authoringPayload() {}
func (UpsertVisualPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type RemoveVisualPayload struct {
	VisualID string `json:"visualId"`
}

func (RemoveVisualPayload) authoringPayload() {}
func (RemoveVisualPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type SetLayoutPayload struct {
	PageID string                            `json:"pageId"`
	Layout *document.DashboardLayoutOverride `json:"layout,omitempty"`
}

func (SetLayoutPayload) authoringPayload() {}
func (SetLayoutPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type SetFiltersPayload struct {
	Filters []document.DashboardFilter `json:"filters,omitempty"`
	Clear   bool                       `json:"clear,omitempty"`
}

func (SetFiltersPayload) authoringPayload() {}
func (SetFiltersPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type SetInteractionPayload struct {
	PageID      string                         `json:"pageId,omitempty"`
	VisualID    string                         `json:"visualId,omitempty"`
	Interaction *document.DashboardInteraction `json:"interaction,omitempty"`
	Clear       bool                           `json:"clear,omitempty"`
}

func (SetInteractionPayload) authoringPayload() {}
func (SetInteractionPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// Publish and Archive affect authoring lifecycle only; they do not deploy or
// activate serving state.
type PublishPayload struct{}

func (PublishPayload) authoringPayload() {}
func (PublishPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionPublish, nil
}

type ArchivePayload struct{}

func (ArchivePayload) authoringPayload() {}
func (ArchivePayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionArchive, nil
}

// CommandID is the idempotency key. Fingerprint intentionally excludes it so
// a store can detect reuse of one key with changed request inputs.
type Command struct {
	ID               CommandID     `json:"id"`
	DashboardID      DashboardID   `json:"dashboardId"`
	DraftID          DraftID       `json:"draftId"`
	ExpectedRevision RevisionToken `json:"expectedRevision"`
	ContentHash      string        `json:"contentHash,omitempty"`
	Provenance       Provenance    `json:"provenance"`

	Metadata       *MetadataPatch         `json:"metadata,omitempty"`
	SetVisibility  *SetVisibilityPayload  `json:"setVisibility,omitempty"`
	AddPage        *AddPagePayload        `json:"addPage,omitempty"`
	AddVisual      *AddVisualPayload      `json:"addVisual,omitempty"`
	AssignField    *AssignFieldPayload    `json:"assignField,omitempty"`
	UpsertPage     *UpsertPagePayload     `json:"upsertPage,omitempty"`
	RemovePage     *RemovePagePayload     `json:"removePage,omitempty"`
	UpsertVisual   *UpsertVisualPayload   `json:"upsertVisual,omitempty"`
	RemoveVisual   *RemoveVisualPayload   `json:"removeVisual,omitempty"`
	SetLayout      *SetLayoutPayload      `json:"setLayout,omitempty"`
	SetFilters     *SetFiltersPayload     `json:"setFilters,omitempty"`
	SetInteraction *SetInteractionPayload `json:"setInteraction,omitempty"`
	Publish        *PublishPayload        `json:"publish,omitempty"`
	Archive        *ArchivePayload        `json:"archive,omitempty"`
}

func (c Command) payloads() []authoringPayload {
	var payloads []authoringPayload
	if c.Metadata != nil {
		payloads = append(payloads, c.Metadata)
	}
	if c.SetVisibility != nil {
		payloads = append(payloads, c.SetVisibility)
	}
	if c.AddPage != nil {
		payloads = append(payloads, c.AddPage)
	}
	if c.AddVisual != nil {
		payloads = append(payloads, c.AddVisual)
	}
	if c.AssignField != nil {
		payloads = append(payloads, c.AssignField)
	}
	if c.UpsertPage != nil {
		payloads = append(payloads, c.UpsertPage)
	}
	if c.RemovePage != nil {
		payloads = append(payloads, c.RemovePage)
	}
	if c.UpsertVisual != nil {
		payloads = append(payloads, c.UpsertVisual)
	}
	if c.RemoveVisual != nil {
		payloads = append(payloads, c.RemoveVisual)
	}
	if c.SetLayout != nil {
		payloads = append(payloads, c.SetLayout)
	}
	if c.SetFilters != nil {
		payloads = append(payloads, c.SetFilters)
	}
	if c.SetInteraction != nil {
		payloads = append(payloads, c.SetInteraction)
	}
	if c.Publish != nil {
		payloads = append(payloads, c.Publish)
	}
	if c.Archive != nil {
		payloads = append(payloads, c.Archive)
	}
	return payloads
}

func (c Command) payloadValue() (authoringPayload, error) {
	payloads := c.payloads()
	if len(payloads) != 1 {
		return nil, fmt.Errorf("%w: exactly one payload is required (got %d)", ErrInvalidPayload, len(payloads))
	}
	return payloads[0], nil
}

// RequiredAction returns the authorization action declared by this command's
// single typed payload, or an error when the payload union is invalid.
func (c Command) RequiredAction() (AuthorizationAction, error) {
	payload, err := c.payloadValue()
	if err != nil {
		return "", err
	}
	action, err := payload.RequiredAction()
	if err != nil {
		return "", err
	}
	if err := action.Validate(); err != nil {
		return "", err
	}
	return action, nil
}

// IsBuilderIntent identifies the deliberately narrow application dispatcher
// used by interactive builder transports. Generic authoring transports may
// still use Execute for the broader union, but ExecuteIntent must not become
// an alternate document-patch endpoint.
func (c Command) IsBuilderIntent() bool {
	payload, err := c.payloadValue()
	if err != nil {
		return false
	}
	switch payload.(type) {
	case *SetVisibilityPayload, *AddPagePayload, *AddVisualPayload, *AssignFieldPayload:
		return true
	default:
		return false
	}
}

func (c Command) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := validateDashboardID(c.DashboardID); err != nil {
		return err
	}
	if err := c.ExpectedRevision.ValidateComplete(); err != nil {
		return err
	}
	if c.ContentHash != "" && !validSHA256(c.ContentHash) {
		return fmt.Errorf("%w: invalid command content hash", ErrInvalidAuthoring)
	}
	if err := c.Provenance.Validate(); err != nil {
		return err
	}
	payload, err := c.payloadValue()
	if err != nil {
		return err
	}
	if action, err := payload.RequiredAction(); err != nil {
		return err
	} else if err := action.Validate(); err != nil {
		return err
	}
	if c.DraftID == "" {
		if _, archive := payload.(*ArchivePayload); !archive {
			return fmt.Errorf("%w: draft id is required for this command", ErrInvalidIdentifier)
		}
	} else if err := c.DraftID.Validate(); err != nil {
		return err
	}
	return validatePayload(payload)
}

func validatePayload(payload authoringPayload) error {
	switch value := payload.(type) {
	case *MetadataPatch:
		if value.Title == nil && value.Description == nil && value.Slug == nil && value.SemanticModel == nil && value.Visibility == nil && value.Appearance == nil {
			return fmt.Errorf("%w: metadata patch has no edits", ErrInvalidPayload)
		}
		if value.Title != nil && strings.TrimSpace(*value.Title) == "" {
			return fmt.Errorf("%w: title cannot be cleared", ErrInvalidPayload)
		}
		if value.SemanticModel != nil && strings.TrimSpace(*value.SemanticModel) == "" {
			return fmt.Errorf("%w: semantic model cannot be cleared", ErrInvalidPayload)
		}
		if value.Slug != nil && !slugPattern.MatchString(*value.Slug) {
			return fmt.Errorf("%w: invalid dashboard slug %q", ErrInvalidPayload, *value.Slug)
		}
		if value.Visibility != nil && !value.Visibility.Valid() {
			return fmt.Errorf("%w: unsupported visibility %q", ErrInvalidPayload, *value.Visibility)
		}
		if value.Appearance != nil {
			if value.Appearance.Icon != nil && strings.TrimSpace(*value.Appearance.Icon) == "" {
				return fmt.Errorf("%w: appearance icon cannot be blank", ErrInvalidPayload)
			}
		}
	case *SetVisibilityPayload:
		if !value.Visibility.Valid() {
			return fmt.Errorf("%w: unsupported visibility %q", ErrInvalidPayload, value.Visibility)
		}
	case *AddPagePayload:
		if value.PageID != "" {
			if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
				return err
			}
		}
		if value.Title != "" && strings.TrimSpace(value.Title) == "" {
			return fmt.Errorf("%w: page title cannot be blank", ErrInvalidPayload)
		}
	case *AddVisualPayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: add visual requires page id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
			return err
		}
		if strings.TrimSpace(value.VisualID) != "" {
			if err := validateCanonicalObjectID("visual id", value.VisualID); err != nil {
				return err
			}
		}
		if strings.TrimSpace(value.ComponentID) != "" {
			if err := validateCanonicalObjectID("component id", value.ComponentID); err != nil {
				return err
			}
		}
		if !canonicalVisualTypeSupported(document.DashboardVisualType(strings.TrimSpace(value.Type))) {
			return fmt.Errorf("%w: unsupported visual type %q", ErrInvalidPayload, value.Type)
		}
	case *AssignFieldPayload:
		for kind, id := range map[string]string{"page id": value.PageID, "visual id": value.VisualID, "field id": value.FieldID} {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("%w: assign field requires %s", ErrInvalidPayload, kind)
			}
		}
		if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
			return err
		}
		if err := validateCanonicalObjectID("visual id", value.VisualID); err != nil {
			return err
		}
		if !ValidGovernedFieldID(value.FieldID) {
			return fmt.Errorf("%w: invalid governed field id %q", ErrInvalidPayload, value.FieldID)
		}
		if !value.Role.Valid() {
			return fmt.Errorf("%w: unsupported field role %q", ErrInvalidPayload, value.Role)
		}
	case *UpsertPagePayload:
		if value.Page.ID == "" {
			return fmt.Errorf("%w: upsert page requires page id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("page id", value.Page.ID); err != nil {
			return err
		}
	case *RemovePagePayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: remove page requires page id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
			return err
		}
	case *UpsertVisualPayload:
		if strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: upsert visual requires visual id", ErrInvalidPayload)
		}
		if value.Visual.Type == "" {
			return fmt.Errorf("%w: upsert visual requires type", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("visual id", value.VisualID); err != nil {
			return err
		}
	case *RemoveVisualPayload:
		if strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: remove visual requires visual id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("visual id", value.VisualID); err != nil {
			return err
		}
	case *SetLayoutPayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: set layout requires page id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
			return err
		}
		if value.Layout == nil {
			return fmt.Errorf("%w: set layout has no edits", ErrInvalidPayload)
		}
	case *SetFiltersPayload:
		if value.Clear && len(value.Filters) != 0 {
			return fmt.Errorf("%w: clear filters cannot include replacement values", ErrInvalidPayload)
		}
		if !value.Clear && value.Filters == nil {
			return fmt.Errorf("%w: set filters has no edits", ErrInvalidPayload)
		}
	case *SetInteractionPayload:
		if strings.TrimSpace(value.PageID) == "" && strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: set interaction requires a page or visual id", ErrInvalidPayload)
		}
		if value.PageID != "" {
			if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
				return err
			}
		}
		if value.VisualID != "" {
			if err := validateCanonicalObjectID("visual id", value.VisualID); err != nil {
				return err
			}
		}
		if value.Clear && value.Interaction != nil {
			return fmt.Errorf("%w: clear interaction cannot include replacement values", ErrInvalidPayload)
		}
		if !value.Clear && value.Interaction == nil {
			return fmt.Errorf("%w: set interaction requires interaction or clear", ErrInvalidPayload)
		}
	case *PublishPayload, *ArchivePayload:
	default:
		return fmt.Errorf("%w: unsupported payload %T", ErrInvalidPayload, payload)
	}
	return nil
}

func validateCanonicalObjectID(kind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value != strings.TrimSpace(value) || !canonicalObjectIDPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid canonical %s %q", ErrInvalidPayload, kind, value)
	}
	return nil
}

// ValidGovernedFieldID is the canonical closed validator for semantic field
// references accepted by builder intents and projections. A field is either a
// model member (name) or a qualified physical dimension (table.field); no
// expression, renderer alias, or raw SQL syntax is accepted.
func ValidGovernedFieldID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value != strings.TrimSpace(value) {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !validSemanticPart(part) {
			return false
		}
	}
	return true
}

// ValidSemanticMemberID accepts the unqualified semantic member identifiers
// used by aggregate, pivot, histogram, and distribution query selections.
// Physical table-qualified fields are reserved for records queries.
func ValidSemanticMemberID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value == strings.TrimSpace(value) && validSemanticPart(value)
}

func validSemanticPart(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (index > 0 && char >= '0' && char <= '9') || char == '_' {
			continue
		}
		return false
	}
	return true
}

type fingerprintInput struct {
	DashboardID      DashboardID      `json:"dashboardId"`
	DraftID          DraftID          `json:"draftId"`
	ExpectedRevision RevisionToken    `json:"expectedRevision"`
	ContentHash      string           `json:"contentHash,omitempty"`
	ProvenanceDigest string           `json:"provenanceDigest"`
	Payload          authoringPayload `json:"payload"`
}

func (c Command) Fingerprint() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	payload, _ := c.payloadValue()
	// Marshal here as an explicit check: the named payload union must remain
	// JSON-capable for command transport even though the interface is private.
	input := fingerprintInput{DashboardID: c.DashboardID, DraftID: c.DraftID, ExpectedRevision: c.ExpectedRevision, ContentHash: c.ContentHash, ProvenanceDigest: c.Provenance.Digest(), Payload: payload}
	if _, err := json.Marshal(input); err != nil {
		return "", fmt.Errorf("%w: fingerprint payload: %v", ErrInvalidPayload, err)
	}
	return digestValue(input), nil
}
