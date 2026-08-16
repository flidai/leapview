package manageddata

import (
	"context"
	"fmt"
	"strings"
	"time"

	apigenfailure "github.com/Yacobolo/toolbelt/apigen/runtime/failure"
	"github.com/flidai/leapview/internal/platform/jobs"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var (
	ErrNotFound = apigenfailure.New("not_found", "managed data record not found")
	ErrConflict = apigenfailure.New("conflict", "managed data conflict")
)

type CollectionStatus string

const (
	CollectionStatusActive   CollectionStatus = "active"
	CollectionStatusArchived CollectionStatus = "archived"
)

type RevisionStatus string

const (
	RevisionStatusPending RevisionStatus = "pending"
	RevisionStatusReady   RevisionStatus = "ready"
	RevisionStatusFailed  RevisionStatus = "failed"
)

func (s RevisionStatus) CanTransitionTo(target string) bool {
	return s == RevisionStatusPending && (RevisionStatus(target) == RevisionStatusReady || RevisionStatus(target) == RevisionStatusFailed)
}

type UploadStatus string

const (
	UploadStatusOpen       UploadStatus = "open"
	UploadStatusCommitting UploadStatus = "committing"
	UploadStatusComplete   UploadStatus = "complete"
	UploadStatusAborted    UploadStatus = "aborted"
	UploadStatusExpired    UploadStatus = "expired"
	UploadStatusFailed     UploadStatus = "failed"
)

func (s UploadStatus) CanTransitionTo(target string) bool {
	t := UploadStatus(target)
	switch s {
	case UploadStatusOpen:
		return t == UploadStatusCommitting || t == UploadStatusAborted || t == UploadStatusExpired
	case UploadStatusCommitting:
		return t == UploadStatusComplete || t == UploadStatusFailed
	default:
		return false
	}
}

type Environment string

func NormalizeEnvironment(value string) (Environment, error) {
	value = strings.TrimSpace(value)
	if err := validateSlug("environment", value); err != nil {
		return "", err
	}
	return Environment(value), nil
}

func ValidateCollectionID(value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("collection id must be canonical")
	}
	if _, err := projectgraph.NewResourceID(value); err != nil {
		return fmt.Errorf("collection id: %w", err)
	}
	return nil
}

type Collection struct {
	ID           projectgraph.ResourceID
	ProjectID    projectgraph.ResourceID
	ConnectionID projectgraph.ResourceID
	Name         string
	Description  string
	Status       CollectionStatus
	CreatedBy    string
	CreatedAt    string
	UpdatedAt    string
	ArchivedAt   string
}

type CreateCollectionInput struct {
	ID           projectgraph.ResourceID
	ProjectID    projectgraph.ResourceID
	ConnectionID projectgraph.ResourceID
	Name         string
	Description  string
	CreatedBy    string
}

type Revision struct {
	ID           RevisionID
	CollectionID projectgraph.ResourceID
	Sequence     int64
	Digest       string
	Status       RevisionStatus
	ManifestJSON string
	FileCount    int64
	SizeBytes    int64
	CreatedBy    string
	CreatedAt    string
	ReadyAt      string
	Error        string
}

type StoredFile struct {
	File
	StorageKey string
	MediaType  string
	ETag       string
}

type RevisionFile struct {
	RevisionID RevisionID
	StoredFile
	CreatedAt string
}

type UploadSession struct {
	ID                UploadID
	CollectionID      projectgraph.ResourceID
	BaseRevisionID    RevisionID
	RevisionID        RevisionID
	Status            UploadStatus
	ManifestJSON      string
	ExpectedFileCount int64
	ExpectedSizeBytes int64
	UploadedFileCount int64
	UploadedSizeBytes int64
	StorageBackend    string
	StagingPrefix     string
	CreatedBy         string
	CreatedAt         string
	UpdatedAt         string
	ExpiresAt         string
	CompletedAt       string
	Error             string
}

type CreateUploadSessionInput struct {
	ID             UploadID
	CollectionID   projectgraph.ResourceID
	BaseRevisionID RevisionID
	Manifest       Manifest
	StorageBackend string
	StagingPrefix  string
	CreatedBy      string
	ExpiresAt      time.Time
}

type UploadProgress struct {
	UploadedFileCount int64
	UploadedSizeBytes int64
}

type S3MultipartStatus string

const (
	S3MultipartStatusCreating   S3MultipartStatus = "creating"
	S3MultipartStatusOpen       S3MultipartStatus = "open"
	S3MultipartStatusCompleting S3MultipartStatus = "completing"
	S3MultipartStatusCompleted  S3MultipartStatus = "completed"
	S3MultipartStatusAborting   S3MultipartStatus = "aborting"
	S3MultipartStatusAborted    S3MultipartStatus = "aborted"
	S3MultipartStatusFailed     S3MultipartStatus = "failed"
)

type S3MultipartUpload struct {
	ID                    MultipartUploadID
	UploadSessionID       UploadID
	LogicalPath           string
	SHA256                string
	SizeBytes             int64
	ObjectKey             string
	ProviderUploadID      string
	Status                S3MultipartStatus
	Existing              bool
	IdempotencyIdentity   string
	CompletionIdentity    string
	CompletionRequestHash string
	AbortIdentity         string
	CreatedAt             string
	UpdatedAt             string
	CompletedAt           string
	AbortedAt             string
	Error                 string
}

type CreateS3MultipartUploadInput struct {
	ID                  MultipartUploadID
	UploadSessionID     UploadID
	LogicalPath         string
	SHA256              string
	SizeBytes           int64
	IdempotencyIdentity string
}

type InitializeS3MultipartUploadInput struct {
	ID               MultipartUploadID
	ObjectKey        string
	ProviderUploadID string
	Existing         bool
}

type S3MultipartPart struct {
	MultipartUploadID MultipartUploadID
	PartNumber        int32
	SizeBytes         int64
	SHA256            string
}

type BeginS3MultipartCompletionInput struct {
	ID                  MultipartUploadID
	IdempotencyIdentity string
	RequestHash         string
}

type S3MultipartCompletion struct {
	Upload  S3MultipartUpload
	Parts   []S3MultipartPart
	Execute bool
}

type BeginS3MultipartAbortInput struct {
	ID                  MultipartUploadID
	IdempotencyIdentity string
}

type S3MultipartAbort struct {
	Upload  S3MultipartUpload
	Execute bool
}

type CompleteUploadInput struct {
	SessionID  UploadID
	RevisionID RevisionID
	Files      []StoredFile
}

type EnvironmentPointer struct {
	CollectionID projectgraph.ResourceID
	Environment  Environment
	RevisionID   RevisionID
	DeploymentID string
	Generation   int64
	UpdatedBy    string
	UpdatedAt    string
}

type ServingStateBinding struct {
	Identity     projectgraph.ServingIdentity
	CollectionID projectgraph.ResourceID
	RevisionID   RevisionID
	BoundAt      string
}

// Repository owns project-global managed-data metadata. Implementations must
// make CompleteUpload and ReplaceServingStateBindings atomic.
type Repository interface {
	CreateCollection(context.Context, CreateCollectionInput) (Collection, error)
	CollectionByID(context.Context, projectgraph.ResourceID) (Collection, error)
	CollectionByProjectConnection(context.Context, projectgraph.ResourceID, projectgraph.ResourceID) (Collection, error)
	ListCollections(context.Context, bool) ([]Collection, error)
	ArchiveCollection(context.Context, projectgraph.ResourceID) error
	CreateUploadSession(context.Context, CreateUploadSessionInput) (UploadSession, error)
	UploadSessionByID(context.Context, UploadID) (UploadSession, error)
	UpdateUploadProgress(context.Context, UploadID, UploadProgress) error
	BeginUploadFinalization(context.Context, UploadID, jobs.WorkflowIntent) (UploadSession, error)
	FailUploadFinalization(context.Context, UploadID, string) (UploadSession, error)
	AbortUploadSession(context.Context, UploadID) error
	ExpireUploadSessions(context.Context, time.Time) (int64, error)
	CreateS3MultipartUpload(context.Context, CreateS3MultipartUploadInput) (S3MultipartUpload, error)
	S3MultipartUploadByID(context.Context, MultipartUploadID) (S3MultipartUpload, error)
	InitializeS3MultipartUpload(context.Context, InitializeS3MultipartUploadInput) (S3MultipartUpload, error)
	ReserveS3MultipartPart(context.Context, S3MultipartPart) (S3MultipartPart, error)
	ListS3MultipartParts(context.Context, MultipartUploadID) ([]S3MultipartPart, error)
	BeginS3MultipartCompletion(context.Context, BeginS3MultipartCompletionInput) (S3MultipartCompletion, error)
	FinishS3MultipartCompletion(context.Context, MultipartUploadID) (S3MultipartUpload, error)
	BeginS3MultipartAbort(context.Context, BeginS3MultipartAbortInput) (S3MultipartAbort, error)
	FinishS3MultipartAbort(context.Context, MultipartUploadID) (S3MultipartUpload, error)
	FailS3MultipartUpload(context.Context, MultipartUploadID, string) (S3MultipartUpload, error)
	ListRecoverableS3MultipartUploads(context.Context, time.Time, int64) ([]S3MultipartUpload, error)
	CompleteUpload(context.Context, CompleteUploadInput) (Revision, error)
	RevisionByID(context.Context, RevisionID) (Revision, error)
	ListRevisions(context.Context, projectgraph.ResourceID) ([]Revision, error)
	ListRevisionFiles(context.Context, RevisionID) ([]RevisionFile, error)
	EnvironmentPointer(context.Context, projectgraph.ResourceID, Environment) (EnvironmentPointer, error)
	ReplaceServingStateBindings(context.Context, projectgraph.ServingIdentity, []ServingStateBinding) error
	ListServingStateBindings(context.Context, projectgraph.ServingIdentity) ([]ServingStateBinding, error)
}

func validateSlug(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", kind)
	}
	if len(value) > 128 {
		return fmt.Errorf("%s exceeds 128 characters", kind)
	}
	for i, char := range []byte(value) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || i > 0 && (char == '-' || char == '_') {
			continue
		}
		return fmt.Errorf("%s must contain only lowercase letters, numbers, dashes, or underscores", kind)
	}
	return nil
}
