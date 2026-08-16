// Package control coordinates backend-neutral managed-data upload sessions and revisions.
package control

import (
	"context"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrInvalid    = apigenfailure.New("invalid", "invalid managed data request")
	ErrNotFound   = apigenfailure.New("not_found", "managed data resource not found")
	ErrConflict   = apigenfailure.New("conflict", "managed data conflict")
	ErrIncomplete = apigenfailure.New("incomplete", "managed data upload is incomplete")
	ErrExpired    = apigenfailure.New("expired", "managed data upload has expired")
	ErrIntegrity  = apigenfailure.New("integrity", "managed data integrity check failed")
	ErrBackend    = apigenfailure.New("backend", "managed data storage is unavailable")
	ErrInternal   = apigenfailure.New("internal", "managed data service failed")
)

type Repository interface {
	CreateCollection(context.Context, manageddata.CreateCollectionInput) (manageddata.Collection, error)
	CollectionByProjectConnection(context.Context, projectgraph.ResourceID, projectgraph.ResourceID) (manageddata.Collection, error)
	CreateUploadSession(context.Context, manageddata.CreateUploadSessionInput) (manageddata.UploadSession, error)
	UploadSessionByID(context.Context, manageddata.UploadID) (manageddata.UploadSession, error)
	UpdateUploadProgress(context.Context, manageddata.UploadID, manageddata.UploadProgress) error
	BeginUploadFinalization(context.Context, manageddata.UploadID, jobs.WorkflowIntent) (manageddata.UploadSession, error)
	FailUploadFinalization(context.Context, manageddata.UploadID, string) (manageddata.UploadSession, error)
	AbortUploadSession(context.Context, manageddata.UploadID) error
	ExpireUploadSessions(context.Context, time.Time) (int64, error)
	CompleteUpload(context.Context, manageddata.CompleteUploadInput) (manageddata.Revision, error)
	RevisionByID(context.Context, manageddata.RevisionID) (manageddata.Revision, error)
	ListRevisionFiles(context.Context, manageddata.RevisionID) ([]manageddata.RevisionFile, error)
}

var _ Repository = (manageddata.Repository)(nil)

type Config struct {
	Limits            manageddata.Limits
	UploadTTL         time.Duration
	VerifyConcurrency int
	Transport         Transport
	Clock             func() time.Time
}

type EnsureCollectionRequest struct {
	Project     string
	Connection  string
	Name        string
	Description string
	Actor       string
}

type BeginUploadRequest struct {
	Project        string
	Connection     string
	Manifest       manageddata.Manifest
	BaseRevisionID string
	Actor          string
	IdempotencyKey string
}

type UploadRequest struct {
	Project    string
	Connection string
	UploadID   string
	Actor      string
	Workflow   jobs.WorkflowIntent
}

type CollectionResult struct {
	ID          string
	Project     string
	Connection  string
	Name        string
	Description string
	Status      manageddata.CollectionStatus
	CreatedAt   string
	UpdatedAt   string
}

type Progress struct {
	VerifiedFiles int64
	VerifiedBytes int64
	ExpectedFiles int64
	ExpectedBytes int64
}

type FileStatus string

const (
	FileStatusPending  FileStatus = "pending"
	FileStatusVerified FileStatus = "verified"
)

type UploadFile struct {
	File      manageddata.File
	Status    FileStatus
	Transport TransportDescription
}

type MissingBlob struct {
	SHA256    string
	Size      int64
	Paths     []string
	Transport TransportDescription
}

type UploadResult struct {
	ID             string
	RevisionID     string
	Collection     CollectionResult
	BaseRevisionID string
	Status         manageddata.UploadStatus
	Manifest       manageddata.Manifest
	Files          []UploadFile
	MissingBlobs   []MissingBlob
	Progress       Progress
	CreatedAt      string
	ExpiresAt      string
	CompletedAt    string
	Error          string
}

type RevisionFileResult struct {
	Path       string
	Size       int64
	SHA256     string
	StorageURI string
	MediaType  string
	ETag       string
}

type RevisionResult struct {
	ID         string
	Collection CollectionResult
	Digest     string
	Sequence   int64
	Status     manageddata.RevisionStatus
	Manifest   manageddata.Manifest
	Files      []RevisionFileResult
	FileCount  int64
	SizeBytes  int64
	CreatedBy  string
	CreatedAt  string
	ReadyAt    string
}

type FinalizeResult struct {
	Upload   UploadResult
	Revision RevisionResult
}

type ExpireResult struct {
	Expired         int64
	Cleaned         int64
	CleanupBacklog  int64
	CleanupFailures int64
}
