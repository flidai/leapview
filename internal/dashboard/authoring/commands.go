package authoring

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	dashboardmodel "github.com/flidai/leapview/internal/dashboard"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
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
	Title         *string                    `json:"title,omitempty"`
	Description   *string                    `json:"description,omitempty"`
	Slug          *string                    `json:"slug,omitempty"`
	SemanticModel *string                    `json:"semanticModel,omitempty"`
	Visibility    *Visibility                `json:"visibility,omitempty"`
	Appearance    *dashboardappearance.Patch `json:"appearance,omitempty"`
}

func (MetadataPatch) authoringPayload() {}
func (MetadataPatch) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type UpsertPagePayload struct {
	Page dashboardmodel.Page `json:"page"`
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
	VisualID string                 `json:"visualId"`
	Visual   AuthoringVisualization `json:"visual"`
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
	PageID     string                                  `json:"pageId"`
	Canvas     *dashboardmodel.PageCanvas              `json:"canvas,omitempty"`
	Grid       *dashboardmodel.PageGrid                `json:"grid,omitempty"`
	Placements map[string]dashboardmodel.PagePlacement `json:"placements,omitempty"`
}

func (SetLayoutPayload) authoringPayload() {}
func (SetLayoutPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type SetFiltersPayload struct {
	Definitions map[string]dashboardfilter.Definition `json:"definitions,omitempty"`
	Bindings    map[string]dashboardfilter.Binding    `json:"bindings,omitempty"`
	Application *dashboardfilter.ApplicationPolicy    `json:"application,omitempty"`
	Clear       bool                                  `json:"clear,omitempty"`
}

func (SetFiltersPayload) authoringPayload() {}
func (SetFiltersPayload) RequiredAction() (AuthorizationAction, error) {
	return AuthorizationActionEdit, nil
}

type SetInteractionPayload struct {
	PageID      string       `json:"pageId,omitempty"`
	VisualID    string       `json:"visualId,omitempty"`
	Interaction *Interaction `json:"interaction,omitempty"`
	Clear       bool         `json:"clear,omitempty"`
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

func (c Command) Validate() error {
	if err := c.ID.Validate(); err != nil {
		return err
	}
	if err := c.DashboardID.Validate(); err != nil {
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
			if err := dashboardappearance.ValidatePatch(*value.Appearance); err != nil {
				return err
			}
		}
	case *UpsertPagePayload:
		if value.Page.ID == "" {
			return fmt.Errorf("%w: upsert page requires page id", ErrInvalidPayload)
		}
	case *RemovePagePayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: remove page requires page id", ErrInvalidPayload)
		}
	case *UpsertVisualPayload:
		if strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: upsert visual requires visual id", ErrInvalidPayload)
		}
		if (value.Visual.Chart == nil) == (value.Visual.Tabular == nil) {
			return fmt.Errorf("%w: upsert visual requires exactly one visual variant", ErrInvalidPayload)
		}
	case *RemoveVisualPayload:
		if strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: remove visual requires visual id", ErrInvalidPayload)
		}
	case *SetLayoutPayload:
		if strings.TrimSpace(value.PageID) == "" {
			return fmt.Errorf("%w: set layout requires page id", ErrInvalidPayload)
		}
		if value.Canvas == nil && value.Grid == nil && value.Placements == nil {
			return fmt.Errorf("%w: set layout has no edits", ErrInvalidPayload)
		}
	case *SetFiltersPayload:
		if value.Clear && (len(value.Definitions) != 0 || len(value.Bindings) != 0 || value.Application != nil) {
			return fmt.Errorf("%w: clear filters cannot include replacement values", ErrInvalidPayload)
		}
		if !value.Clear && len(value.Definitions) == 0 && len(value.Bindings) == 0 && value.Application == nil {
			return fmt.Errorf("%w: set filters has no edits", ErrInvalidPayload)
		}
	case *SetInteractionPayload:
		if strings.TrimSpace(value.PageID) == "" && strings.TrimSpace(value.VisualID) == "" {
			return fmt.Errorf("%w: set interaction requires a page or visual id", ErrInvalidPayload)
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
