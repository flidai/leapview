// Package http exposes managed-data control operations over the generated API.
package http

import (
	"context"
	"errors"
	"log/slog"
	stdhttp "net/http"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/s3multipart"
)

var (
	ErrInvalid      = control.ErrInvalid
	ErrNotFound     = control.ErrNotFound
	ErrConflict     = control.ErrConflict
	ErrTooLarge     = apigenfailure.New("too_large", "managed-data request is too large")
	ErrBackend      = control.ErrBackend
	ErrUnavailable  = apigenfailure.New("unavailable", "managed-data service is not configured")
	ErrUnauthorized = errors.New("managed-data authentication is required")
	ErrForbidden    = access.ErrForbidden
)

type Principal struct {
	ID string
}

// CommandAuditInput is the transport-neutral fact set needed to persist the
// generated command's required success audit. Policy (action and privilege)
// is deliberately resolved by the managed-data module from generated APIGen
// contracts instead of being repeated in this HTTP adapter.
type CommandAuditInput struct {
	OperationID   string
	PrincipalID   string
	ProjectID     string
	ConnectionID  string
	TargetType    string
	TargetID      string
	RequestID     string
	CorrelationID string
	Surface       string
}

type RevisionMetadata = control.RevisionMetadata
type Repository = control.MetadataRepository

// ConnectionAuthorizer is the narrow authorization port for project-managed
// connection resources. The caller resolves principal groups and evaluates
// the active serving-generation snapshot; this transport layer never queries
// mutable access storage.
type ConnectionAuthorizer func(context.Context, string, string, string, access.Capability) (bool, error)

type UploadCoordinator interface {
	BeginUpload(context.Context, control.BeginUploadRequest) (control.UploadResult, error)
	RecoverUpload(context.Context, control.UploadRequest) (control.UploadResult, error)
	FinalizeUpload(context.Context, control.UploadRequest) (control.FinalizeResult, error)
	BeginFinalizeUpload(context.Context, control.UploadRequest) (control.UploadResult, error)
	CompleteFinalizeUpload(context.Context, control.UploadRequest) (control.FinalizeResult, error)
	AbortUpload(context.Context, control.UploadRequest) (control.UploadResult, error)
}

type Options struct {
	Repository            Repository
	Uploads               UploadCoordinator
	Multipart             s3multipart.Coordinator
	CurrentPrincipal      func(*stdhttp.Request) (Principal, bool)
	AuthorizeConnection   ConnectionAuthorizer
	MaxJSONBodyBytes      int64
	Environment           string
	EnqueueFinalize       func(context.Context, control.UploadRequest) error
	BeginFinalize         func(context.Context, control.UploadRequest) (control.UploadResult, error)
	RecordUploadCreated   func(context.Context, control.UploadResult) error
	RecordUploadCancelled func(context.Context, control.UploadResult) error
	AbortUpload           func(context.Context, control.UploadRequest) (control.UploadResult, error)
	RecordCommandAudit    func(context.Context, CommandAuditInput) error
	Logger                *slog.Logger
}

type Handler struct {
	options Options
}

func NewHandler(options Options) *Handler {
	if options.MaxJSONBodyBytes <= 0 {
		options.MaxJSONBodyBytes = 16 << 20
	}
	if options.Environment == "" {
		options.Environment = "dev"
	}
	return &Handler{options: options}
}
