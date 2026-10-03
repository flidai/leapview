// Package credential owns encrypted customer credential drafts, isolated
// validation receipts and an authority-bound exact-version runtime reader.
// Saving or validating a draft does not publish it or authorize runtime use.
package credential

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential/encryption"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalid          = errors.New("invalid credential request")
	ErrForbidden        = errors.New("credential permission denied")
	ErrNotFound         = errors.New("credential draft not found")
	ErrUnavailable      = errors.New("credential service unavailable")
	ErrConflict         = errors.New("credential draft conflicts with stored state")
	ErrInvalidCursor    = errors.New("credential draft cursor is invalid")
	ErrValidationFailed = errors.New("credential validation failed")
)

const (
	DefaultDraftPageSize = 25
	MaxDraftPageSize     = 100
)

// Resource is a locator, never a caller declaration of ownership or destination.
type Resource struct {
	ScopeKind   string
	TargetID    string
	ProjectID   string
	Environment string
	ResourceID  string
}

func (r Resource) Validate() error {
	if !canonical(r.ResourceID) {
		return ErrInvalid
	}
	switch r.ScopeKind {
	case "connection":
		if !canonical(r.TargetID) || !canonical(r.ProjectID) || !canonical(r.Environment) {
			return ErrInvalid
		}
		if _, err := projectgraph.NewResourceID(r.ProjectID); err != nil {
			return ErrInvalid
		}
		if _, err := projectgraph.NewResourceID(r.ResourceID); err != nil {
			return ErrInvalid
		}
	case "agent":
		if r.TargetID != "" || r.ProjectID != "" || r.Environment != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func canonical(s string) bool {
	return s != "" && len(s) <= 255 && utf8.ValidString(s) && strings.TrimSpace(s) == s && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Scope comes only from the authoritative resource resolver, not a transport
// body. Destination is a digest of non-secret destination configuration.
type Scope struct {
	Resource    Resource
	OwnerID     string
	Purpose     string
	Provider    string
	Destination string
}

// ScopeResolver must resolve only resources in the instance's bound target and
// environment, with declared customer ownership. Bootstrap-owned accounts are
// not eligible. Callers cannot override the returned owner or destination.
type ScopeResolver interface {
	ResolveCredentialScope(context.Context, Resource) (Scope, error)
}

// Authorizer evaluates current principal authority AND API credential attenuation
// for the exact pair. Platform settings pairs also require the durable platform
// role; possession of an API permission pair alone never grants that role.
type Authorizer interface {
	RequirePermission(context.Context, string, access.PermissionPair) error
}

type Metadata struct {
	Binding   encryption.Binding
	ActorID   string
	CreatedAt time.Time
}

// DraftPage contains only metadata for drafts in one exact authorized resource
// scope. NextBeforeVersionID is an opaque continuation locator, not an
// activation or validation token.
type DraftPage struct {
	Items               []Metadata
	NextBeforeVersionID string
}

type StoredVersion struct {
	Metadata Metadata
	Envelope encryption.Envelope `json:"-"`
}

// Repository commits the draft and audit together. Budget reservations commit
// independently before encryption and must survive a later SaveDraft failure.
type Repository interface {
	encryption.Budget
	SaveDraft(context.Context, StoredVersion, access.AuditIntent) error
	GetDraftMetadata(context.Context, string, string, Resource, string) (Metadata, error)
	ListDrafts(context.Context, string, string, Resource, int, string) (DraftPage, error)
}

type Encryptor interface {
	DeploymentID() string
	Encrypt(context.Context, encryption.Budget, encryption.Binding, []byte) (encryption.Envelope, error)
}
