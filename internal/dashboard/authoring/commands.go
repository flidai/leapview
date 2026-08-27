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

	// maxPlacementUpdates bounds one atomic browser reflow command and keeps
	// validation and revision hashing work proportional to the page size.
	maxPlacementUpdates = 1024
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

// PlacementUpdate identifies one page component and its final canonical grid
// placement. A placement command carries every component touched by a drag or
// resize transaction so GridStack-style reflow is persisted atomically.
type PlacementUpdate struct {
	ComponentID string                      `json:"componentId"`
	Placement   document.DashboardPlacement `json:"placement"`
}

// SetPlacementsPayload atomically replaces the placements of one page's
// components. Components not listed retain their existing placement.
// Placement coordinates are canonical 1-based column/row values.
type SetPlacementsPayload struct {
	PageID     string            `json:"pageId"`
	Placements []PlacementUpdate `json:"placements"`
}

func (SetPlacementsPayload) authoringPayload() {}
func (SetPlacementsPayload) RequiredAction() (AuthorizationAction, error) {
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

// SetVisualTypePayload changes the renderer type of one placed visual. VisualID
// is the page component identity (rather than the shared definition key), so
// duplicated definitions remain independently editable.
type SetVisualTypePayload struct {
	PageID   string                       `json:"pageId"`
	VisualID string                       `json:"visualId"`
	Type     document.DashboardVisualType `json:"type"`
}

func (SetVisualTypePayload) authoringPayload() {}
func (SetVisualTypePayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// RenameVisualPayload updates the authored title of one placed visual.
type RenameVisualPayload struct {
	PageID   string `json:"pageId"`
	VisualID string `json:"visualId"`
	Title    string `json:"title"`
}

func (RenameVisualPayload) authoringPayload() {}
func (RenameVisualPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// DuplicateVisualPayload clones a placed visual definition and component. IDs
// are optional; the reducer allocates deterministic collision-free IDs.
type DuplicateVisualPayload struct {
	PageID         string `json:"pageId"`
	VisualID       string `json:"visualId"`
	NewVisualID    string `json:"newVisualId,omitempty"`
	NewComponentID string `json:"newComponentId,omitempty"`
	Title          string `json:"title,omitempty"`
}

func (DuplicateVisualPayload) authoringPayload() {}
func (DuplicateVisualPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// UpdateVisualFormatPayload changes only explicit, renderer-neutral visual
// formatting controls. Omitted pointers preserve existing values.
type UpdateVisualFormatPayload struct {
	PageID            string  `json:"pageId"`
	VisualID          string  `json:"visualId"`
	Title             *string `json:"title,omitempty"`
	TitleVisible      *bool   `json:"titleVisible,omitempty"`
	LegendVisible     *bool   `json:"legendVisible,omitempty"`
	AxisVisible       *bool   `json:"axisVisible,omitempty"`
	DataLabelsVisible *bool   `json:"dataLabelsVisible,omitempty"`
}

func (UpdateVisualFormatPayload) authoringPayload() {}
func (UpdateVisualFormatPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// RemoveFieldPayload removes one governed query selection from a placed
// visual. Scalar histogram/distribution bindings cannot be removed without
// making an invalid canonical query and are rejected by the reducer.
type RemoveFieldPayload struct {
	PageID   string    `json:"pageId"`
	VisualID string    `json:"visualId"`
	FieldID  string    `json:"fieldId"`
	Role     FieldRole `json:"role"`
}

func (RemoveFieldPayload) authoringPayload() {}
func (RemoveFieldPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

// MoveFieldPayload reorders a selection within its governed semantic role.
// Cross-role moves are rejected unless a future governed conversion contract
// proves the source field's kind. Index is zero-based; when omitted, direction
// is one of up/down and moves within the current role.
type MoveFieldPayload struct {
	PageID     string    `json:"pageId"`
	VisualID   string    `json:"visualId"`
	FieldID    string    `json:"fieldId"`
	Role       FieldRole `json:"role"`
	TargetRole FieldRole `json:"targetRole,omitempty"`
	Direction  string    `json:"direction,omitempty"`
	Index      *int      `json:"index,omitempty"`
}

func (MoveFieldPayload) authoringPayload() {}
func (MoveFieldPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
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
	PageID   string `json:"pageId,omitempty"`
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

	Metadata           *MetadataPatch             `json:"metadata,omitempty"`
	SetVisibility      *SetVisibilityPayload      `json:"setVisibility,omitempty"`
	AddPage            *AddPagePayload            `json:"addPage,omitempty"`
	AddVisual          *AddVisualPayload          `json:"addVisual,omitempty"`
	SetPlacements      *SetPlacementsPayload      `json:"setPlacements,omitempty"`
	AssignField        *AssignFieldPayload        `json:"assignField,omitempty"`
	SetVisualType      *SetVisualTypePayload      `json:"setVisualType,omitempty"`
	RenameVisual       *RenameVisualPayload       `json:"renameVisual,omitempty"`
	DuplicateVisual    *DuplicateVisualPayload    `json:"duplicateVisual,omitempty"`
	UpdateVisualFormat *UpdateVisualFormatPayload `json:"updateVisualFormat,omitempty"`
	RemoveField        *RemoveFieldPayload        `json:"removeField,omitempty"`
	MoveField          *MoveFieldPayload          `json:"moveField,omitempty"`
	UpsertPage         *UpsertPagePayload         `json:"upsertPage,omitempty"`
	RemovePage         *RemovePagePayload         `json:"removePage,omitempty"`
	UpsertVisual       *UpsertVisualPayload       `json:"upsertVisual,omitempty"`
	RemoveVisual       *RemoveVisualPayload       `json:"removeVisual,omitempty"`
	SetLayout          *SetLayoutPayload          `json:"setLayout,omitempty"`
	SetFilters         *SetFiltersPayload         `json:"setFilters,omitempty"`
	SetInteraction     *SetInteractionPayload     `json:"setInteraction,omitempty"`
	Publish            *PublishPayload            `json:"publish,omitempty"`
	Archive            *ArchivePayload            `json:"archive,omitempty"`
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
	if c.SetPlacements != nil {
		payloads = append(payloads, c.SetPlacements)
	}
	if c.AssignField != nil {
		payloads = append(payloads, c.AssignField)
	}
	if c.SetVisualType != nil {
		payloads = append(payloads, c.SetVisualType)
	}
	if c.RenameVisual != nil {
		payloads = append(payloads, c.RenameVisual)
	}
	if c.DuplicateVisual != nil {
		payloads = append(payloads, c.DuplicateVisual)
	}
	if c.UpdateVisualFormat != nil {
		payloads = append(payloads, c.UpdateVisualFormat)
	}
	if c.RemoveField != nil {
		payloads = append(payloads, c.RemoveField)
	}
	if c.MoveField != nil {
		payloads = append(payloads, c.MoveField)
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
	case *SetVisibilityPayload, *AddPagePayload, *AddVisualPayload, *SetPlacementsPayload, *AssignFieldPayload,
		*SetVisualTypePayload, *RenameVisualPayload, *DuplicateVisualPayload, *UpdateVisualFormatPayload,
		*RemoveFieldPayload, *MoveFieldPayload, *RemoveVisualPayload:
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
	case *SetPlacementsPayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: set placement requires page id", ErrInvalidPayload)
		}
		if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
			return err
		}
		if len(value.Placements) == 0 {
			return fmt.Errorf("%w: set placement requires at least one component placement", ErrInvalidPayload)
		}
		if len(value.Placements) > maxPlacementUpdates {
			return fmt.Errorf("%w: set placement exceeds bounded component limit", ErrInvalidPayload)
		}
		seen := make(map[string]struct{}, len(value.Placements))
		for index, update := range value.Placements {
			componentID := strings.TrimSpace(update.ComponentID)
			if componentID == "" {
				return fmt.Errorf("%w: set placement component %d requires component id", ErrInvalidPayload, index)
			}
			if err := validateCanonicalObjectID("component id", componentID); err != nil {
				return err
			}
			if _, exists := seen[componentID]; exists {
				return fmt.Errorf("%w: set placement contains duplicate component %q", ErrInvalidPayload, componentID)
			}
			seen[componentID] = struct{}{}
			if err := validatePlacementCoordinates(update.Placement); err != nil {
				return fmt.Errorf("%w: set placement component %q: %v", ErrInvalidPayload, componentID, err)
			}
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
	case *SetVisualTypePayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "set visual type"); err != nil {
			return err
		}
		if !canonicalVisualTypeSupported(value.Type) {
			return fmt.Errorf("%w: unsupported visual type %q", ErrInvalidPayload, value.Type)
		}
	case *RenameVisualPayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "rename visual"); err != nil {
			return err
		}
		if strings.TrimSpace(value.Title) == "" {
			return fmt.Errorf("%w: visual title cannot be blank", ErrInvalidPayload)
		}
	case *DuplicateVisualPayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "duplicate visual"); err != nil {
			return err
		}
		for kind, id := range map[string]string{"new visual id": value.NewVisualID, "new component id": value.NewComponentID} {
			if strings.TrimSpace(id) != "" {
				if err := validateCanonicalObjectID(kind, id); err != nil {
					return err
				}
			}
		}
		if value.Title != "" && strings.TrimSpace(value.Title) == "" {
			return fmt.Errorf("%w: duplicate visual title cannot be blank", ErrInvalidPayload)
		}
	case *UpdateVisualFormatPayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "update visual format"); err != nil {
			return err
		}
		if value.Title == nil && value.TitleVisible == nil && value.LegendVisible == nil && value.AxisVisible == nil && value.DataLabelsVisible == nil {
			return fmt.Errorf("%w: visual format has no edits", ErrInvalidPayload)
		}
		if value.Title != nil && strings.TrimSpace(*value.Title) == "" {
			return fmt.Errorf("%w: visual title cannot be blank", ErrInvalidPayload)
		}
	case *RemoveFieldPayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "remove field"); err != nil {
			return err
		}
		if !ValidGovernedFieldID(value.FieldID) {
			return fmt.Errorf("%w: invalid governed field id %q", ErrInvalidPayload, value.FieldID)
		}
		if !value.Role.Valid() {
			return fmt.Errorf("%w: unsupported field role %q", ErrInvalidPayload, value.Role)
		}
	case *MoveFieldPayload:
		if err := validateVisualTargetFields(value.PageID, value.VisualID, "move field"); err != nil {
			return err
		}
		if !ValidGovernedFieldID(value.FieldID) {
			return fmt.Errorf("%w: invalid governed field id %q", ErrInvalidPayload, value.FieldID)
		}
		if !value.Role.Valid() {
			return fmt.Errorf("%w: unsupported field role %q", ErrInvalidPayload, value.Role)
		}
		if value.TargetRole != "" && !value.TargetRole.Valid() {
			return fmt.Errorf("%w: unsupported target field role %q", ErrInvalidPayload, value.TargetRole)
		}
		if value.Index != nil && *value.Index < 0 {
			return fmt.Errorf("%w: field index must be non-negative", ErrInvalidPayload)
		}
		direction := strings.TrimSpace(value.Direction)
		if value.Index == nil && direction != "up" && direction != "down" {
			return fmt.Errorf("%w: move field requires index or up/down direction", ErrInvalidPayload)
		}
		if value.Index != nil && direction != "" && direction != "before" && direction != "after" {
			return fmt.Errorf("%w: unsupported field move direction %q", ErrInvalidPayload, value.Direction)
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
		if value.PageID != "" {
			if err := validateCanonicalObjectID("page id", value.PageID); err != nil {
				return err
			}
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
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || value != trimmed || !canonicalObjectIDPattern.MatchString(trimmed) {
		return fmt.Errorf("%w: invalid canonical %s %q", ErrInvalidPayload, kind, value)
	}
	return nil
}

func validateVisualTargetFields(pageID, visualID, operation string) error {
	if strings.TrimSpace(pageID) == "" || strings.TrimSpace(visualID) == "" {
		return fmt.Errorf("%w: %s requires page id and visual id", ErrInvalidPayload, operation)
	}
	if err := validateCanonicalObjectID("page id", pageID); err != nil {
		return err
	}
	return validateCanonicalObjectID("visual id", visualID)
}

func validatePlacementCoordinates(value document.DashboardPlacement) error {
	if value.Column <= 0 || value.Row <= 0 {
		return fmt.Errorf("placement column and row must be greater than zero")
	}
	if value.ColumnSpan <= 0 || value.RowSpan <= 0 {
		return fmt.Errorf("placement spans must be greater than zero")
	}
	return nil
}

// ValidGovernedFieldID is the canonical closed validator for semantic field
// references accepted by builder intents and projections. A field is either a
// model member (name) or a qualified physical dimension (table.field); no
// expression, renderer alias, or raw SQL syntax is accepted.
func ValidGovernedFieldID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || value != trimmed {
		return false
	}
	parts := strings.Split(trimmed, ".")
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
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && value == trimmed && validSemanticPart(trimmed)
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
