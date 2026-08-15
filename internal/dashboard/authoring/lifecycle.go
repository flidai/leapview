package authoring

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// These IDs are intentionally distinct: a dashboard ID is stable across
// title, slug, and source-path changes, while drafts and revisions are not.
type DashboardID string
type DraftID string
type RevisionID string
type CommandID string

var (
	ErrInvalidAuthoring  = errors.New("invalid dashboard authoring contract")
	ErrInvalidIdentifier = errors.New("invalid dashboard authoring identifier")
	ErrInvalidTransition = errors.New("invalid dashboard lifecycle transition")
	ErrInvalidPayload    = errors.New("invalid dashboard authoring command payload")
	ErrStaleRevision     = errors.New("dashboard authoring revision is stale")
	ErrNotFound          = errors.New("dashboard authoring record not found")
	// ErrSourceUnavailable indicates that an authored source document is not
	// retained and therefore cannot be safely forked. Compiled dashboard
	// definitions are intentionally not accepted as a source substitute.
	ErrSourceUnavailable = errors.New("dashboard authoring source document unavailable")
	ErrConflict          = errors.New("dashboard authoring conflict")
	ErrCommandReuse      = errors.New("dashboard authoring command id was reused with a different request")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

func validateIdentifier(kind, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: %s %q", ErrInvalidIdentifier, kind, value)
	}
	return nil
}

func (id DashboardID) Validate() error { return validateIdentifier("dashboard id", string(id)) }
func (id DraftID) Validate() error     { return validateIdentifier("draft id", string(id)) }
func (id RevisionID) Validate() error  { return validateIdentifier("revision id", string(id)) }
func (id CommandID) Validate() error   { return validateIdentifier("command id", string(id)) }

func (id DashboardID) String() string { return string(id) }
func (id DraftID) String() string     { return string(id) }
func (id RevisionID) String() string  { return string(id) }
func (id CommandID) String() string   { return string(id) }

type Origin string

const (
	OriginUI    Origin = "ui"
	OriginFile  Origin = "file"
	OriginAgent Origin = "agent"
)

func (o Origin) Valid() bool { return o == OriginUI || o == OriginFile || o == OriginAgent }
func (o Origin) Validate() error {
	if !o.Valid() {
		return fmt.Errorf("%w: unsupported provenance origin %q", ErrInvalidAuthoring, o)
	}
	return nil
}

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityShared  Visibility = "shared"
)

func (v Visibility) Valid() bool { return v == VisibilityPrivate || v == VisibilityShared }
func (v Visibility) Validate() error {
	if !v.Valid() {
		return fmt.Errorf("%w: unsupported visibility %q", ErrInvalidAuthoring, v)
	}
	return nil
}

type LifecycleStatus string

const (
	LifecycleStatusDraft     LifecycleStatus = "draft"
	LifecycleStatusPublished LifecycleStatus = "published"
	LifecycleStatusArchived  LifecycleStatus = "archived"
)

func (s LifecycleStatus) Valid() bool {
	return s == LifecycleStatusDraft || s == LifecycleStatusPublished || s == LifecycleStatusArchived
}
func (s LifecycleStatus) Validate() error {
	if !s.Valid() {
		return fmt.Errorf("%w: unsupported lifecycle status %q", ErrInvalidAuthoring, s)
	}
	return nil
}

// Source metadata is optional evidence. Repository and ref are never workflow
// authorities; lifecycle state is managed by this contract alone.
type SourceMetadata struct {
	Path       string            `json:"path,omitempty"`
	Repository string            `json:"repository,omitempty"`
	Ref        string            `json:"ref,omitempty"`
	Revision   string            `json:"revision,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// ForkEvidence is typed, immutable provenance for a dashboard fork. The
// complete source revision token is retained so downstream consumers do not
// need to infer source identity from free-form metadata.
type ForkEvidence struct {
	SourceWorkspaceID string        `json:"sourceWorkspaceId"`
	SourceDashboardID DashboardID   `json:"sourceDashboardId"`
	SourceRevision    RevisionToken `json:"sourceRevision"`
}

func (e ForkEvidence) Validate() error {
	if err := validateRequiredLifecycleValue("fork source workspace id", e.SourceWorkspaceID); err != nil {
		return err
	}
	if err := e.SourceDashboardID.Validate(); err != nil {
		return fmt.Errorf("%w: fork source dashboard: %v", ErrInvalidAuthoring, err)
	}
	if err := e.SourceRevision.ValidateComplete(); err != nil {
		return fmt.Errorf("%w: fork source revision: %v", ErrInvalidAuthoring, err)
	}
	return nil
}

type Provenance struct {
	Origin                     Origin          `json:"origin"`
	ActorID                    string          `json:"actorId"`
	ConversationID             string          `json:"conversationId,omitempty"`
	ToolCallID                 string          `json:"toolCallId,omitempty"`
	BaseSemanticServingStateID string          `json:"baseSemanticServingStateId,omitempty"`
	Source                     *SourceMetadata `json:"source,omitempty"`
	ForkedFrom                 *ForkEvidence   `json:"forkedFrom,omitempty"`
}

// Clone returns a provenance value detached from caller-owned evidence maps.
// Provenance is carried into immutable revisions and lifecycle pointers, so a
// shallow struct copy must not leave Source.Metadata shared with a request.
func (p Provenance) Clone() Provenance {
	cloned := p
	if p.Source != nil {
		source := *p.Source
		if p.Source.Metadata != nil {
			source.Metadata = make(map[string]string, len(p.Source.Metadata))
			for key, value := range p.Source.Metadata {
				source.Metadata[key] = value
			}
		}
		cloned.Source = &source
	}
	if p.ForkedFrom != nil {
		fork := *p.ForkedFrom
		cloned.ForkedFrom = &fork
	}
	return cloned
}

func (p Provenance) Validate() error {
	if !p.Origin.Valid() {
		return fmt.Errorf("%w: unsupported provenance origin %q", ErrInvalidAuthoring, p.Origin)
	}
	if p.ForkedFrom != nil {
		if err := p.ForkedFrom.Validate(); err != nil {
			return err
		}
	}
	if err := validateProvenanceIdentifier("provenance actor id", p.ActorID); err != nil {
		return err
	}
	for _, item := range []struct {
		kind  string
		value string
	}{
		{kind: "provenance conversation id", value: p.ConversationID},
		{kind: "provenance tool call id", value: p.ToolCallID},
		{kind: "provenance base semantic serving state id", value: p.BaseSemanticServingStateID},
	} {
		value := item.value
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := validateProvenanceIdentifier(item.kind, value); err != nil {
			return err
		}
	}
	return nil
}

func validateProvenanceIdentifier(kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: provenance actor id is required", ErrInvalidAuthoring)
	}
	if value != strings.TrimSpace(value) || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalidAuthoring, kind, value)
	}
	return nil
}

// Digest is separate from document/content hashes and changes when authorship
// evidence changes even if the authored bytes stay the same.
func (p Provenance) Digest() string {
	return digestValue(struct {
		Origin                     Origin          `json:"origin"`
		ActorID                    string          `json:"actorId"`
		ConversationID             string          `json:"conversationId,omitempty"`
		ToolCallID                 string          `json:"toolCallId,omitempty"`
		BaseSemanticServingStateID string          `json:"baseSemanticServingStateId,omitempty"`
		Source                     *SourceMetadata `json:"source,omitempty"`
		ForkedFrom                 *ForkEvidence   `json:"forkedFrom,omitempty"`
	}{Origin: p.Origin, ActorID: strings.TrimSpace(p.ActorID), ConversationID: strings.TrimSpace(p.ConversationID), ToolCallID: strings.TrimSpace(p.ToolCallID), BaseSemanticServingStateID: strings.TrimSpace(p.BaseSemanticServingStateID), Source: p.Source, ForkedFrom: p.ForkedFrom})
}

type RevisionToken struct {
	RevisionID  RevisionID `json:"revisionId,omitempty"`
	Number      uint64     `json:"number,omitempty"`
	ContentHash string     `json:"contentHash,omitempty"`
}

func (t RevisionToken) IsZero() bool {
	return t.RevisionID == "" && t.Number == 0 && t.ContentHash == ""
}

func (t RevisionToken) Validate() error {
	if t.IsZero() {
		return nil
	}
	if t.RevisionID != "" {
		if err := t.RevisionID.Validate(); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("%w: revision id is required", ErrInvalidAuthoring)
	}
	if t.Number == 0 {
		return fmt.Errorf("%w: revision number is required", ErrInvalidAuthoring)
	}
	if t.ContentHash == "" {
		return fmt.Errorf("%w: revision content hash is required", ErrInvalidAuthoring)
	}
	if t.ContentHash != "" && !validSHA256(t.ContentHash) {
		return fmt.Errorf("%w: invalid revision content hash", ErrInvalidAuthoring)
	}
	return nil
}

func (t RevisionToken) ValidateComplete() error {
	if t.IsZero() {
		return fmt.Errorf("%w: revision pointer is required", ErrInvalidAuthoring)
	}
	return t.Validate()
}

// Revision owns a complete authored document copy. Draft and Published below
// only point at this immutable value.
type Revision struct {
	ID          RevisionID  `json:"id"`
	DashboardID DashboardID `json:"dashboardId"`
	Number      uint64      `json:"number"`
	Document    Dashboard   `json:"document"`
	ContentHash string      `json:"contentHash"`
	Provenance  Provenance  `json:"provenance"`
	CreatedAt   time.Time   `json:"createdAt,omitempty"`
}

func NewRevision(id RevisionID, dashboardID DashboardID, number uint64, createdAt time.Time, document Dashboard, provenance Provenance) (Revision, error) {
	if err := id.Validate(); err != nil {
		return Revision{}, err
	}
	if err := dashboardID.Validate(); err != nil {
		return Revision{}, err
	}
	if document.ID != string(dashboardID) {
		return Revision{}, fmt.Errorf("%w: dashboard document id %q does not match revision dashboard id %q", ErrInvalidAuthoring, document.ID, dashboardID)
	}
	if err := provenance.Validate(); err != nil {
		return Revision{}, err
	}
	if err := document.ValidateDraftStructure(); err != nil {
		return Revision{}, fmt.Errorf("%w: revision dashboard structure: %v", ErrInvalidAuthoring, err)
	}
	if number == 0 {
		return Revision{}, fmt.Errorf("%w: revision number is required", ErrInvalidAuthoring)
	}
	if createdAt.IsZero() || createdAt.Location() != time.UTC {
		return Revision{}, fmt.Errorf("%w: revision created_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	hash, err := DashboardContentHash(document)
	if err != nil {
		return Revision{}, err
	}
	cloned, err := document.Clone()
	if err != nil {
		return Revision{}, err
	}
	return Revision{ID: id, DashboardID: dashboardID, Number: number, Document: cloned, ContentHash: hash, Provenance: provenance.Clone(), CreatedAt: createdAt}, nil
}

func (r Revision) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return err
	}
	if err := r.DashboardID.Validate(); err != nil {
		return err
	}
	if r.Document.ID != string(r.DashboardID) {
		return fmt.Errorf("%w: dashboard document id %q does not match revision dashboard id %q", ErrInvalidAuthoring, r.Document.ID, r.DashboardID)
	}
	if r.Number == 0 {
		return fmt.Errorf("%w: revision number is required", ErrInvalidAuthoring)
	}
	if r.CreatedAt.IsZero() || r.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("%w: revision created_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	if !validSHA256(r.ContentHash) {
		return fmt.Errorf("%w: revision content hash is required", ErrInvalidAuthoring)
	}
	hash, err := DashboardContentHash(r.Document)
	if err != nil || hash != r.ContentHash {
		return fmt.Errorf("%w: revision content hash does not match document", ErrInvalidAuthoring)
	}
	if err := r.Document.ValidateDraftStructure(); err != nil {
		return fmt.Errorf("%w: revision dashboard structure: %v", ErrInvalidAuthoring, err)
	}
	return r.Provenance.Validate()
}

func (r Revision) Token() RevisionToken {
	return RevisionToken{RevisionID: r.ID, Number: r.Number, ContentHash: r.ContentHash}
}

type Draft struct {
	ID          DraftID       `json:"id"`
	DashboardID DashboardID   `json:"dashboardId"`
	Revision    RevisionToken `json:"revision"`
	Provenance  Provenance    `json:"provenance"`
}

type Published struct {
	Revision    RevisionToken         `json:"revision"`
	Compilation CompiledRevisionToken `json:"compilation"`
	PublishedAt time.Time             `json:"publishedAt,omitempty"`
	Provenance  Provenance            `json:"provenance"`
}

func (d Draft) Validate() error {
	if err := d.ID.Validate(); err != nil {
		return err
	}
	if err := d.DashboardID.Validate(); err != nil {
		return err
	}
	if err := d.Revision.ValidateComplete(); err != nil {
		return err
	}
	return d.Provenance.Validate()
}

func (p Published) Validate() error {
	if err := p.Revision.ValidateComplete(); err != nil {
		return err
	}
	if err := p.Compilation.Validate(); err != nil {
		return err
	}
	if p.Compilation.AuthoredRevision != p.Revision {
		return fmt.Errorf("%w: published compilation must reference the published authored revision", ErrInvalidAuthoring)
	}
	if p.PublishedAt.IsZero() || p.PublishedAt.Location() != time.UTC {
		return fmt.Errorf("%w: published_at must be a non-zero UTC timestamp", ErrInvalidAuthoring)
	}
	return p.Provenance.Validate()
}

// DashboardLifecycle is the mutable identity record around immutable
// revisions. Slug and title may change without changing ID.
type DashboardLifecycle struct {
	WorkspaceID      string          `json:"workspaceId"`
	ID               DashboardID     `json:"id"`
	OwnerPrincipalID string          `json:"ownerPrincipalId"`
	Slug             string          `json:"slug"`
	Title            string          `json:"title"`
	SemanticModel    string          `json:"semanticModel"`
	Visibility       Visibility      `json:"visibility"`
	Status           LifecycleStatus `json:"status"`
	Draft            *Draft          `json:"draft,omitempty"`
	Published        *Published      `json:"published,omitempty"`
}

type NewDashboardLifecycleInput struct {
	WorkspaceID      string
	ID               DashboardID
	OwnerPrincipalID string
	Slug             string
	Title            string
	SemanticModel    string
	Visibility       Visibility
	Draft            *Draft
}

func NewDashboardLifecycle(input NewDashboardLifecycleInput) (DashboardLifecycle, error) {
	draft := input.Draft
	if draft != nil {
		draftCopy := *draft
		draftCopy.Provenance = draft.Provenance.Clone()
		draft = &draftCopy
	}
	lifecycle := DashboardLifecycle{
		WorkspaceID: input.WorkspaceID, ID: input.ID, OwnerPrincipalID: input.OwnerPrincipalID,
		Slug: input.Slug, Title: input.Title, SemanticModel: input.SemanticModel,
		Visibility: input.Visibility, Status: LifecycleStatusDraft, Draft: draft,
	}
	if err := lifecycle.Validate(); err != nil {
		return DashboardLifecycle{}, err
	}
	return lifecycle, nil
}

func (d DashboardLifecycle) Validate() error {
	if err := validateRequiredLifecycleValue("dashboard workspace id", d.WorkspaceID); err != nil {
		return err
	}
	if err := d.ID.Validate(); err != nil {
		return err
	}
	if err := validateRequiredLifecycleValue("dashboard owner principal id", d.OwnerPrincipalID); err != nil {
		return err
	}
	if !slugPattern.MatchString(d.Slug) {
		return fmt.Errorf("%w: invalid dashboard slug %q", ErrInvalidAuthoring, d.Slug)
	}
	if strings.TrimSpace(d.Title) == "" {
		return fmt.Errorf("%w: dashboard title is required", ErrInvalidAuthoring)
	}
	if err := validateRequiredLifecycleValue("dashboard semantic model", d.SemanticModel); err != nil {
		return err
	}
	if !d.Visibility.Valid() {
		return fmt.Errorf("%w: unsupported visibility %q", ErrInvalidAuthoring, d.Visibility)
	}
	if !d.Status.Valid() {
		return fmt.Errorf("%w: unsupported lifecycle status %q", ErrInvalidAuthoring, d.Status)
	}
	if d.Status == LifecycleStatusDraft && d.Draft == nil {
		return fmt.Errorf("%w: draft status requires a draft pointer", ErrInvalidAuthoring)
	}
	if d.Status == LifecycleStatusPublished && d.Published == nil {
		return fmt.Errorf("%w: published status requires a published pointer", ErrInvalidAuthoring)
	}
	if d.Status == LifecycleStatusArchived && d.Draft == nil && d.Published == nil {
		return fmt.Errorf("%w: archived status must retain a lifecycle pointer", ErrInvalidAuthoring)
	}
	if d.Draft != nil {
		if d.Draft.DashboardID != d.ID {
			return fmt.Errorf("%w: draft belongs to dashboard %q", ErrInvalidAuthoring, d.Draft.DashboardID)
		}
		if err := d.Draft.Validate(); err != nil {
			return err
		}
	}
	if d.Published != nil {
		if err := d.Published.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validateRequiredLifecycleValue(kind, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidAuthoring, kind)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s cannot have surrounding whitespace", ErrInvalidAuthoring, kind)
	}
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalidAuthoring, kind, value)
	}
	return nil
}

func CanTransition(from, to LifecycleStatus) bool {
	switch from {
	case LifecycleStatusDraft:
		return to == LifecycleStatusPublished || to == LifecycleStatusArchived
	case LifecycleStatusPublished:
		return to == LifecycleStatusArchived
	default:
		return false
	}
}

func ValidateTransition(from, to LifecycleStatus) error {
	if !from.Valid() || !to.Valid() || !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	return nil
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func digestValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func DashboardContentHash(document Dashboard) (string, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", fmt.Errorf("%w: dashboard document: %v", ErrInvalidAuthoring, err)
	}
	hash := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

// Clone performs a typed deep copy and surfaces unsupported reference values
// rather than dropping authored fields or changing concrete interface values.
func (d Dashboard) Clone() (Dashboard, error) {
	cloned, err := cloneValue(reflect.ValueOf(d), "dashboard")
	if err != nil {
		return Dashboard{}, fmt.Errorf("%w: clone dashboard document: %v", ErrInvalidAuthoring, err)
	}
	return cloned.Interface().(Dashboard), nil
}

func cloneValue(value reflect.Value, path string) (reflect.Value, error) {
	if !value.IsValid() {
		return value, nil
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneValue(value.Elem(), path)
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type()).Elem()
		if cloned.Type().AssignableTo(value.Type()) {
			out.Set(cloned)
		} else if cloned.Type().Implements(value.Type()) {
			out.Set(cloned)
		} else {
			return reflect.Value{}, fmt.Errorf("%s has non-assignable interface value %s", path, cloned.Type())
		}
		return out, nil
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		cloned, err := cloneValue(value.Elem(), path+".*")
		if err != nil {
			return reflect.Value{}, err
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloned)
		return out, nil
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := 0; i < value.NumField(); i++ {
			field := out.Field(i)
			// Unexported fields are copied as part of the struct value. Authored
			// documents contain no mutable unexported fields; this also preserves
			// opaque values such as time.Time without unsafe operations.
			if !field.CanSet() || !value.Field(i).CanInterface() {
				continue
			}
			cloned, err := cloneValue(value.Field(i), fmt.Sprintf("%s.%s", path, value.Type().Field(i).Name))
			if err != nil {
				return reflect.Value{}, err
			}
			field.Set(cloned)
		}
		return out, nil
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			key, err := cloneValue(iter.Key(), path+".key")
			if err != nil {
				return reflect.Value{}, err
			}
			item, err := cloneValue(iter.Value(), path+"[value]")
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(key, item)
		}
		return out, nil
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned, err := cloneValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(cloned)
		}
		return out, nil
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned, err := cloneValue(value.Index(i), fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(cloned)
		}
		return out, nil
	case reflect.Func, reflect.Chan:
		if value.IsNil() {
			return reflect.Zero(value.Type()), nil
		}
		return reflect.Value{}, fmt.Errorf("%s contains unsupported %s", path, value.Kind())
	case reflect.UnsafePointer:
		if value.Pointer() == 0 {
			return reflect.Zero(value.Type()), nil
		}
		return reflect.Value{}, fmt.Errorf("%s contains unsupported %s", path, value.Kind())
	default:
		return value, nil
	}
}
