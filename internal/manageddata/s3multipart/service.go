package s3multipart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/control"
	"github.com/flidai/leapview/internal/manageddata/storage"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/strictjson"
)

const (
	integrityTerminalError = "completed object failed integrity verification"
	multipartClaimLease    = 5 * time.Minute
	multipartClaimRenewal  = time.Minute
)

type Service struct {
	repo       Repository
	store      MultipartStore
	backend    string
	signExpiry time.Duration
	now        func() time.Time
}

var _ Coordinator = (*Service)(nil)

func New(repo Repository, store MultipartStore, config Config) (*Service, error) {
	if repo == nil || store == nil {
		return nil, fmt.Errorf("%w: multipart repository and store are required", control.ErrInvalid)
	}
	if err := validateIdentity("storage backend", config.Backend, 128); err != nil {
		return nil, err
	}
	expiry := config.SignExpiry
	if expiry == 0 {
		expiry = defaultSignExpiry
	}
	if expiry < time.Minute || expiry > 24*time.Hour {
		return nil, fmt.Errorf("%w: signing expiry must be between one minute and 24 hours", control.ErrInvalid)
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{repo: repo, store: store, backend: config.Backend, signExpiry: expiry, now: clock}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (UploadResult, error) {
	session, manifest, err := s.scopedSession(ctx, request.Project, request.Connection, request.UploadSessionID)
	if err != nil {
		return UploadResult{}, err
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return UploadResult{}, err
	}
	file, err := manifestFile(manifest, request.Path)
	if err != nil {
		return UploadResult{}, err
	}
	if file.Size == 0 || file.Size > MaximumObjectSize {
		return UploadResult{}, fmt.Errorf("%w: file size is outside S3 multipart limits", control.ErrInvalid)
	}
	identity := identityHash("create", session.ID.String(), request.IdempotencyKey)
	id := manageddata.MultipartUploadID("multipart_" + identity)
	existingRecord := false
	if existing, lookupErr := s.repo.S3MultipartUploadByID(ctx, id); lookupErr == nil {
		existingRecord = true
		if !sameCreateIdentity(existing, session.ID, file, identity) {
			return UploadResult{}, control.ErrConflict
		}
		if existing.Status == manageddata.S3MultipartStatusCompleted || existing.Status == manageddata.S3MultipartStatusAborted {
			return resultFor(existing, session, file)
		}
	} else if !errors.Is(lookupErr, manageddata.ErrNotFound) {
		return UploadResult{}, repositoryError(lookupErr)
	}
	if err := requireOpenSession(session, s.now()); err != nil {
		return UploadResult{}, err
	}
	upload, err := s.repo.CreateS3MultipartUpload(ctx, manageddata.CreateS3MultipartUploadInput{
		ID: id, UploadSessionID: session.ID, LogicalPath: file.Path, SHA256: file.SHA256,
		SizeBytes: file.Size, IdempotencyIdentity: identity, AuditIntent: request.AuditIntent,
	})
	if err != nil {
		return UploadResult{}, repositoryError(err)
	}
	switch upload.Status {
	case manageddata.S3MultipartStatusOpen, manageddata.S3MultipartStatusCompleted:
		return resultFor(upload, session, file)
	case manageddata.S3MultipartStatusCreating:
		// A previous process may have received provider success and stopped
		// before persisting its upload ID. Never issue a second provider create
		// for an existing creating intent: reconcile the deterministic key first.
		if existingRecord {
			if err := s.reconcileCreating(ctx, upload); err != nil {
				return UploadResult{}, err
			}
			initialized, lookupErr := s.repo.S3MultipartUploadByID(ctx, upload.ID)
			if lookupErr != nil {
				return UploadResult{}, repositoryError(lookupErr)
			}
			return resultFor(initialized, session, file)
		}
	default:
		return UploadResult{}, fmt.Errorf("%w: multipart upload is %s", control.ErrConflict, upload.Status)
	}
	if ownerLister, ok := s.repo.(creatingMultipartLister); ok {
		owners, listErr := ownerLister.ListCreatingS3MultipartIDsByDigest(ctx, file.SHA256)
		if listErr != nil {
			return UploadResult{}, repositoryError(listErr)
		}
		if len(owners) > 0 && upload.ID.String() != owners[0] {
			return UploadResult{}, fmt.Errorf("%w: multipart creation is owned by another intent", control.ErrConflict)
		}
	}

	claimCtx, release, claimErr := acquireMultipartClaim(ctx, s.repo, file.SHA256, upload.ID.String())
	if claimErr != nil {
		return UploadResult{}, claimErr
	}
	defer release()
	provider, err := s.store.CreateMultipart(claimCtx, storage.Blob{SHA256: file.SHA256, Size: file.Size})
	if err != nil {
		return UploadResult{}, storageError(err)
	}
	if err := claimCtx.Err(); err != nil {
		if !provider.Existing {
			_ = s.store.AbortMultipart(context.WithoutCancel(ctx), provider)
		}
		return UploadResult{}, control.ErrConflict
	}
	initialized, initErr := s.repo.InitializeS3MultipartUpload(claimCtx, manageddata.InitializeS3MultipartUploadInput{
		ID: upload.ID, ObjectKey: provider.Key, ProviderUploadID: provider.UploadID, Existing: provider.Existing,
		AuditIntent: request.AuditIntent,
	})
	if initErr == nil {
		return resultFor(initialized, session, file)
	}
	if !provider.Existing {
		_ = s.store.AbortMultipart(ctx, provider)
	}
	current, lookupErr := s.repo.S3MultipartUploadByID(ctx, upload.ID)
	if lookupErr == nil && (current.Status == manageddata.S3MultipartStatusOpen || current.Status == manageddata.S3MultipartStatusCompleted) {
		return resultFor(current, session, file)
	}
	return UploadResult{}, repositoryError(initErr)
}

type multipartUploadLister interface {
	ListMultipartUploads(context.Context, storage.Blob) ([]storage.MultipartUpload, error)
}

type persistedMultipartProviderLister interface {
	ListS3MultipartProviderIDsByDigest(context.Context, string) ([]string, error)
}

type creatingMultipartLister interface {
	ListCreatingS3MultipartIDsByDigest(context.Context, string) ([]string, error)
}
type multipartClaimer interface {
	ClaimS3MultipartDigest(context.Context, string, string, time.Time) (int64, bool, error)
	RenewS3MultipartDigest(context.Context, string, string, int64, time.Time) (bool, error)
	ReleaseS3MultipartDigest(context.Context, string, string, int64) error
}

// reconcileCreating compensates provider-side uploads for a durable creating
// intent, then creates and persists exactly one replacement. Errors are
// returned to the caller so maintenance can retry instead of silently losing
// a crash window.
func (s *Service) reconcileCreating(ctx context.Context, upload manageddata.S3MultipartUpload) error {
	// Claim before inspecting or mutating provider state. Provider listing and
	// compensation are part of the same critical section as CreateMultipart;
	// otherwise two processes can both classify an upload as orphaned and race
	// to abort or replace it.
	claimCtx, release, claimErr := acquireMultipartClaim(ctx, s.repo, upload.SHA256, upload.ID.String())
	if claimErr != nil {
		return claimErr
	}
	defer release()
	lister, ok := s.store.(multipartUploadLister)
	if !ok {
		return storageError(fmt.Errorf("%w: multipart listing is unavailable", storage.ErrBackend))
	}
	found, err := lister.ListMultipartUploads(claimCtx, storage.Blob{SHA256: upload.SHA256, Size: upload.SizeBytes})
	if err != nil {
		return storageError(err)
	}
	known := map[string]struct{}{}
	if providerLister, ok := s.repo.(persistedMultipartProviderLister); ok {
		ids, listErr := providerLister.ListS3MultipartProviderIDsByDigest(ctx, upload.SHA256)
		if listErr != nil {
			return repositoryError(listErr)
		}
		for _, id := range ids {
			known[id] = struct{}{}
		}
	}
	unknown := make([]storage.MultipartUpload, 0, len(found))
	for _, provider := range found {
		if _, ok := known[provider.UploadID]; !ok {
			unknown = append(unknown, provider)
		}
	}
	// A provider upload already persisted by a sibling intent is live and must
	// never be touched. Only a single unknown upload can be safely attributed to
	// this unresolved creating row; multiple unknowns are a legacy ambiguity.
	if len(unknown) > 1 {
		if ownerLister, ok := s.repo.(creatingMultipartLister); ok {
			owners, listErr := ownerLister.ListCreatingS3MultipartIDsByDigest(ctx, upload.SHA256)
			if listErr != nil {
				return repositoryError(listErr)
			}
			if len(owners) > 0 && upload.ID.String() != owners[0] {
				return nil // deterministic owner will compensate the unknowns
			}
		}
		// The deterministic owner can safely compensate every provider that is
		// not persisted to a live sibling row; legacy creating rows are not live.
	}
	for _, provider := range unknown {
		if err := s.store.AbortMultipart(claimCtx, provider); err != nil {
			return storageError(err)
		}
	}
	provider, err := s.store.CreateMultipart(claimCtx, storage.Blob{SHA256: upload.SHA256, Size: upload.SizeBytes})
	if err != nil {
		return storageError(err)
	}
	if err := claimCtx.Err(); err != nil {
		if !provider.Existing {
			_ = s.store.AbortMultipart(context.WithoutCancel(ctx), provider)
		}
		return control.ErrConflict
	}
	if _, err := s.repo.InitializeS3MultipartUpload(claimCtx, manageddata.InitializeS3MultipartUploadInput{
		ID: upload.ID, ObjectKey: provider.Key, ProviderUploadID: provider.UploadID, Existing: provider.Existing,
	}); err != nil {
		if !provider.Existing {
			if abortErr := s.store.AbortMultipart(ctx, provider); abortErr != nil {
				return errors.Join(repositoryError(err), storageError(abortErr))
			}
		}
		return repositoryError(err)
	}
	return nil
}

func acquireMultipartClaim(parent context.Context, repository Repository, digest, owner string) (context.Context, func(), error) {
	claimer, ok := repository.(multipartClaimer)
	if !ok {
		return parent, func() {}, nil
	}
	generation, claimed, err := claimer.ClaimS3MultipartDigest(parent, digest, owner, time.Now().UTC().Add(multipartClaimLease))
	if err != nil {
		return nil, nil, repositoryError(err)
	}
	if !claimed {
		return nil, nil, control.ErrConflict
	}
	claimCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(multipartClaimRenewal)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-claimCtx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(context.Background(), 5*time.Second)
				renewed, renewErr := claimer.RenewS3MultipartDigest(renewCtx, digest, owner, generation, time.Now().UTC().Add(multipartClaimLease))
				renewCancel()
				if renewErr != nil || !renewed {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(done)
			cancel()
			releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer releaseCancel()
			_ = claimer.ReleaseS3MultipartDigest(releaseCtx, digest, owner, generation)
		})
	}
	return claimCtx, release, nil
}

func (s *Service) SignPart(ctx context.Context, request SignPartRequest) (SignedPartResult, error) {
	session, upload, file, err := s.scopedUpload(ctx, request.Project, request.Connection, request.UploadSessionID, request.MultipartUploadID)
	if err != nil {
		return SignedPartResult{}, err
	}
	if err := requireOpenSession(session, s.now()); err != nil {
		return SignedPartResult{}, err
	}
	if upload.Status != manageddata.S3MultipartStatusOpen {
		return SignedPartResult{}, fmt.Errorf("%w: multipart upload is %s", control.ErrConflict, upload.Status)
	}
	if request.PartNumber < 1 || request.PartNumber > MaximumParts || request.Size <= 0 || request.Size > MaximumPartSize || request.Size > file.Size {
		return SignedPartResult{}, fmt.Errorf("%w: part number or size is outside S3 multipart limits", control.ErrInvalid)
	}
	if request.SHA256 != "" {
		if err := validateDigest(request.SHA256); err != nil {
			return SignedPartResult{}, err
		}
	}
	part, err := s.repo.ReserveS3MultipartPart(ctx, manageddata.S3MultipartPart{
		MultipartUploadID: upload.ID, PartNumber: request.PartNumber, SizeBytes: request.Size, SHA256: request.SHA256,
	})
	if err != nil {
		return SignedPartResult{}, repositoryError(err)
	}
	signed, err := s.store.SignPart(ctx, providerUpload(upload), storage.MultipartPartRequest{Number: part.PartNumber, Size: part.SizeBytes, SHA256: part.SHA256})
	if err != nil {
		return SignedPartResult{}, storageError(err)
	}
	if signed.Number != part.PartNumber || !safeProviderValue(signed.URL, 8192) {
		return SignedPartResult{}, control.ErrBackend
	}
	headers, err := responseHeaders(signed.Headers)
	if err != nil {
		return SignedPartResult{}, err
	}
	return SignedPartResult{
		UploadSessionID: request.UploadSessionID, MultipartUploadID: request.MultipartUploadID,
		PartNumber: signed.Number, URL: signed.URL, Headers: headers,
		ExpiresAt: s.now().UTC().Add(s.signExpiry).Format(time.RFC3339Nano),
	}, nil
}

func (s *Service) Complete(ctx context.Context, request CompleteRequest) (UploadResult, error) {
	session, upload, file, err := s.scopedUpload(ctx, request.Project, request.Connection, request.UploadSessionID, request.MultipartUploadID)
	if err != nil {
		return UploadResult{}, err
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return UploadResult{}, err
	}
	ordered, requestHash, err := canonicalCompletedParts(request.Parts)
	if err != nil {
		return UploadResult{}, err
	}
	if upload.Status != manageddata.S3MultipartStatusCompleted {
		if err := requireOpenSession(session, s.now()); err != nil {
			return UploadResult{}, err
		}
		reserved, listErr := s.repo.ListS3MultipartParts(ctx, upload.ID)
		if listErr != nil {
			return UploadResult{}, repositoryError(listErr)
		}
		if err := validateCompletionShape(file.Size, reserved, ordered); err != nil {
			return UploadResult{}, err
		}
	}
	claim, err := s.repo.BeginS3MultipartCompletion(ctx, manageddata.BeginS3MultipartCompletionInput{
		ID: upload.ID, IdempotencyIdentity: identityHash("complete", upload.ID.String(), request.IdempotencyKey), RequestHash: requestHash,
		AuditIntent: request.AuditIntent,
	})
	if err != nil {
		return UploadResult{}, repositoryError(err)
	}
	if !claim.Execute {
		return resultFor(claim.Upload, session, file)
	}
	providerParts := make([]storage.CompletedMultipartPart, len(ordered))
	for index, part := range ordered {
		providerParts[index] = storage.CompletedMultipartPart{Number: part.PartNumber, ETag: part.ETag, SHA256: part.SHA256}
	}
	blob, err := s.store.CompleteMultipart(ctx, providerUpload(claim.Upload), providerParts)
	if err != nil {
		if errors.Is(err, storage.ErrIntegrity) {
			_, _ = s.repo.FailS3MultipartUpload(ctx, upload.ID, integrityTerminalError)
			return UploadResult{}, control.ErrIntegrity
		}
		return UploadResult{}, storageError(err)
	}
	if blob.SHA256 != file.SHA256 || blob.Size != file.Size {
		_, _ = s.repo.FailS3MultipartUpload(ctx, upload.ID, integrityTerminalError)
		return UploadResult{}, control.ErrIntegrity
	}
	completed, err := s.repo.FinishS3MultipartCompletion(ctx, upload.ID)
	if err != nil {
		return UploadResult{}, repositoryError(err)
	}
	return resultFor(completed, session, file)
}

func (s *Service) Abort(ctx context.Context, request AbortRequest) (UploadResult, error) {
	session, upload, file, err := s.scopedUpload(ctx, request.Project, request.Connection, request.UploadSessionID, request.MultipartUploadID)
	if err != nil {
		return UploadResult{}, err
	}
	if err := validateIdempotencyKey(request.IdempotencyKey); err != nil {
		return UploadResult{}, err
	}
	claim, err := s.repo.BeginS3MultipartAbort(ctx, manageddata.BeginS3MultipartAbortInput{
		ID: upload.ID, IdempotencyIdentity: identityHash("abort", upload.ID.String(), request.IdempotencyKey),
		AuditIntent: request.AuditIntent,
	})
	if err != nil {
		return UploadResult{}, repositoryError(err)
	}
	if claim.Execute && claim.Upload.ProviderUploadID != "" {
		if err := s.store.AbortMultipart(ctx, providerUpload(claim.Upload)); err != nil {
			return UploadResult{}, storageError(err)
		}
	}
	if claim.Execute {
		claim.Upload, err = s.repo.FinishS3MultipartAbort(ctx, upload.ID)
		if err != nil {
			return UploadResult{}, repositoryError(err)
		}
	}
	return resultFor(claim.Upload, session, file)
}

func (s *Service) RecoverOrphaned(ctx context.Context, before time.Time, limit int64) (RecoveryResult, error) {
	if ctx == nil || before.IsZero() {
		return RecoveryResult{}, fmt.Errorf("%w: context and recovery cutoff are required", control.ErrInvalid)
	}
	uploads, err := s.repo.ListRecoverableS3MultipartUploads(ctx, before, limit)
	if err != nil {
		return RecoveryResult{}, repositoryError(err)
	}
	result := RecoveryResult{}
	for _, upload := range uploads {
		if upload.Status == manageddata.S3MultipartStatusCompleting {
			parts, partsErr := s.repo.ListS3MultipartParts(ctx, upload.ID)
			if partsErr != nil {
				return result, repositoryError(partsErr)
			}
			providerParts := make([]storage.CompletedMultipartPart, len(parts))
			for i, part := range parts {
				providerParts[i] = storage.CompletedMultipartPart{Number: part.PartNumber, SHA256: part.SHA256}
			}
			blob, completeErr := s.store.CompleteMultipart(ctx, providerUpload(upload), providerParts)
			if completeErr != nil {
				return result, storageError(completeErr)
			}
			if blob.SHA256 != upload.SHA256 || blob.Size != upload.SizeBytes {
				_, _ = s.repo.FailS3MultipartUpload(ctx, upload.ID, integrityTerminalError)
				result.Failed++
				continue
			}
			if _, finishErr := s.repo.FinishS3MultipartCompletion(ctx, upload.ID); finishErr != nil {
				current, lookupErr := s.repo.S3MultipartUploadByID(ctx, upload.ID)
				if lookupErr == nil && current.Status == manageddata.S3MultipartStatusCompleted {
					result.Completed++
					continue
				}
				return result, repositoryError(finishErr)
			}
			result.Completed++
			continue
		}
		if upload.Status == manageddata.S3MultipartStatusCreating && upload.ProviderUploadID == "" {
			if err := s.reconcileCreating(ctx, upload); err != nil {
				return result, err
			}
			continue
		}
		if upload.Status != manageddata.S3MultipartStatusAborting {
			claim, claimErr := s.repo.BeginS3MultipartAbort(ctx, manageddata.BeginS3MultipartAbortInput{
				ID: upload.ID, IdempotencyIdentity: identityHash("recovery", upload.ID.String(), upload.ID.String()),
			})
			if claimErr != nil {
				return result, repositoryError(claimErr)
			}
			upload = claim.Upload
		}
		if err := s.store.AbortMultipart(ctx, providerUpload(upload)); err != nil {
			return result, storageError(err)
		}
		if _, err := s.repo.FinishS3MultipartAbort(ctx, upload.ID); err != nil {
			current, lookupErr := s.repo.S3MultipartUploadByID(ctx, upload.ID)
			if lookupErr == nil && current.Status == manageddata.S3MultipartStatusAborted {
				result.Aborted++
				continue
			}
			return result, repositoryError(err)
		}
		result.Aborted++
	}
	return result, nil
}

func (s *Service) scopedSession(ctx context.Context, project, connection, sessionID string) (manageddata.UploadSession, manageddata.Manifest, error) {
	if ctx == nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, fmt.Errorf("%w: context is required", control.ErrInvalid)
	}
	if err := validateScopeValue("project", project); err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, err
	}
	if err := validateScopeValue("connection", connection); err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, err
	}
	if err := validateIdentity("upload session id", sessionID, 160); err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, err
	}
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, fmt.Errorf("%w: invalid project id", control.ErrInvalid)
	}
	connectionID, err := projectgraph.NewResourceID(connection)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, fmt.Errorf("%w: invalid connection id", control.ErrInvalid)
	}
	collection, err := s.repo.CollectionByProjectConnection(ctx, projectID, connectionID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, repositoryError(err)
	}
	if collection.Status != manageddata.CollectionStatusActive {
		return manageddata.UploadSession{}, manageddata.Manifest{}, control.ErrConflict
	}
	uploadID, err := manageddata.ParseUploadID(sessionID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, fmt.Errorf("%w: invalid upload session id", control.ErrInvalid)
	}
	session, err := s.repo.UploadSessionByID(ctx, uploadID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, repositoryError(err)
	}
	if session.CollectionID != collection.ID {
		return manageddata.UploadSession{}, manageddata.Manifest{}, control.ErrNotFound
	}
	if session.StorageBackend != s.backend {
		return manageddata.UploadSession{}, manageddata.Manifest{}, fmt.Errorf("%w: upload session uses another storage backend", control.ErrConflict)
	}
	manifest, err := strictManifest(session.ManifestJSON)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.Manifest{}, err
	}
	return session, manifest, nil
}

func (s *Service) scopedUpload(ctx context.Context, project, connection, sessionID, multipartID string) (manageddata.UploadSession, manageddata.S3MultipartUpload, manageddata.File, error) {
	session, manifest, err := s.scopedSession(ctx, project, connection, sessionID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, err
	}
	if err := validateIdentity("multipart upload id", multipartID, 160); err != nil {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, err
	}
	multipartUploadID, err := manageddata.ParseMultipartUploadID(multipartID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, fmt.Errorf("%w: invalid multipart upload id", control.ErrInvalid)
	}
	upload, err := s.repo.S3MultipartUploadByID(ctx, multipartUploadID)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, repositoryError(err)
	}
	if upload.UploadSessionID != session.ID {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, control.ErrNotFound
	}
	file, err := manifestFile(manifest, upload.LogicalPath)
	if err != nil {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, control.ErrIntegrity
	}
	if file.SHA256 != upload.SHA256 || file.Size != upload.SizeBytes {
		return manageddata.UploadSession{}, manageddata.S3MultipartUpload{}, manageddata.File{}, control.ErrIntegrity
	}
	return session, upload, file, nil
}

func strictManifest(value string) (manageddata.Manifest, error) {
	var manifest manageddata.Manifest
	// PostgreSQL jsonb may rewrite object-key order and insignificant
	// whitespace. Decode the stored value strictly, then validate and
	// canonicalize its semantic manifest rather than requiring the raw bytes to
	// already be canonical. strictjson still rejects unknown fields, duplicate
	// keys, trailing values, and malformed JSON.
	if err := strictjson.Decode([]byte(value), &manifest); err != nil {
		return manageddata.Manifest{}, control.ErrIntegrity
	}
	if _, err := manifest.CanonicalJSON(); err != nil {
		return manageddata.Manifest{}, control.ErrIntegrity
	}
	return manifest, nil
}

func sameCreateIdentity(upload manageddata.S3MultipartUpload, sessionID manageddata.UploadID, file manageddata.File, identity string) bool {
	return upload.UploadSessionID == sessionID && upload.LogicalPath == file.Path && upload.SHA256 == file.SHA256 &&
		upload.SizeBytes == file.Size && upload.IdempotencyIdentity == identity
}

func manifestFile(manifest manageddata.Manifest, path string) (manageddata.File, error) {
	if path == "" || path != strings.TrimSpace(path) {
		return manageddata.File{}, fmt.Errorf("%w: canonical manifest path is required", control.ErrInvalid)
	}
	for _, file := range manifest.Files {
		if file.Path == path {
			return file, nil
		}
	}
	return manageddata.File{}, control.ErrNotFound
}

func requireOpenSession(session manageddata.UploadSession, now time.Time) error {
	if session.Status != manageddata.UploadStatusOpen {
		return fmt.Errorf("%w: upload session is %s", control.ErrConflict, session.Status)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, session.ExpiresAt)
	if err != nil {
		return control.ErrIntegrity
	}
	if !now.UTC().Before(expiresAt) {
		return control.ErrExpired
	}
	return nil
}

func canonicalCompletedParts(parts []CompletedPart) ([]CompletedPart, string, error) {
	if len(parts) == 0 || len(parts) > int(MaximumParts) {
		return nil, "", fmt.Errorf("%w: completion requires between 1 and %d parts", control.ErrInvalid, MaximumParts)
	}
	ordered := append([]CompletedPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PartNumber < ordered[j].PartNumber })
	for index, part := range ordered {
		if part.PartNumber < 1 || part.PartNumber > MaximumParts || !safeProviderValue(part.ETag, 1024) {
			return nil, "", fmt.Errorf("%w: completed part is invalid", control.ErrInvalid)
		}
		if index > 0 && ordered[index-1].PartNumber == part.PartNumber {
			return nil, "", fmt.Errorf("%w: completed part numbers must be unique", control.ErrInvalid)
		}
		if part.SHA256 != "" {
			if err := validateDigest(part.SHA256); err != nil {
				return nil, "", err
			}
		}
	}
	encoded, _ := json.Marshal(ordered)
	sum := sha256.Sum256(encoded)
	return ordered, hex.EncodeToString(sum[:]), nil
}

func validateCompletionShape(size int64, reserved []manageddata.S3MultipartPart, completed []CompletedPart) error {
	byNumber := make(map[int32]manageddata.S3MultipartPart, len(reserved))
	for _, part := range reserved {
		byNumber[part.PartNumber] = part
	}
	var total int64
	for index, part := range completed {
		reservation, exists := byNumber[part.PartNumber]
		if !exists || reservation.SHA256 != "" && reservation.SHA256 != part.SHA256 {
			return fmt.Errorf("%w: completed part does not match its signed request", control.ErrInvalid)
		}
		if index < len(completed)-1 && reservation.SizeBytes < MinimumPartSize {
			return fmt.Errorf("%w: every non-final S3 part must be at least %d bytes", control.ErrInvalid, MinimumPartSize)
		}
		if total > size-reservation.SizeBytes {
			return fmt.Errorf("%w: completed part sizes do not match the file", control.ErrInvalid)
		}
		total += reservation.SizeBytes
	}
	if total != size {
		return fmt.Errorf("%w: completed part sizes do not match the file", control.ErrInvalid)
	}
	return nil
}

func responseHeaders(headers map[string][]string) ([]Header, error) {
	names := make([]string, 0, len(headers))
	count := 0
	for name, values := range headers {
		if !safeProviderValue(name, 256) || len(values) == 0 {
			return nil, control.ErrBackend
		}
		names = append(names, name)
		count += len(values)
	}
	if count > 32 {
		return nil, control.ErrBackend
	}
	sort.Strings(names)
	result := make([]Header, 0, count)
	for _, name := range names {
		for _, value := range headers[name] {
			if !safeProviderValue(value, 8192) {
				return nil, control.ErrBackend
			}
			result = append(result, Header{Name: name, Value: value})
		}
	}
	return result, nil
}

func resultFor(upload manageddata.S3MultipartUpload, session manageddata.UploadSession, file manageddata.File) (UploadResult, error) {
	var status Status
	switch upload.Status {
	case manageddata.S3MultipartStatusOpen:
		status = StatusOpen
	case manageddata.S3MultipartStatusCompleted:
		status = StatusCompleted
	case manageddata.S3MultipartStatusAborted:
		status = StatusAborted
	default:
		return UploadResult{}, fmt.Errorf("%w: multipart upload transition is incomplete", control.ErrConflict)
	}
	return UploadResult{ID: upload.ID.String(), UploadSessionID: upload.UploadSessionID.String(), File: file, Status: status, Existing: upload.Existing, CreatedAt: upload.CreatedAt, ExpiresAt: session.ExpiresAt}, nil
}

func providerUpload(upload manageddata.S3MultipartUpload) storage.MultipartUpload {
	return storage.MultipartUpload{UploadID: upload.ProviderUploadID, SHA256: upload.SHA256, Size: upload.SizeBytes, Key: upload.ObjectKey, Existing: upload.Existing}
}

func validateScopeValue(name, value string) error {
	if len(value) < 1 || len(value) > 128 || value != strings.TrimSpace(value) {
		return fmt.Errorf("%w: %s is not canonical", control.ErrInvalid, name)
	}
	for index, char := range value {
		if char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || index > 0 && (char == '.' || char == '_' || char == '-') {
			continue
		}
		return fmt.Errorf("%w: %s is not canonical", control.ErrInvalid, name)
	}
	return nil
}

func validateIdentity(name, value string, max int) error {
	if value == "" || len(value) > max || value != strings.TrimSpace(value) || !safeProviderValue(value, max) {
		return fmt.Errorf("%w: %s is invalid", control.ErrInvalid, name)
	}
	return nil
}

func validateIdempotencyKey(value string) error {
	return validateIdentity("idempotency key", value, 255)
}

func validateDigest(value string) error {
	if err := storage.ValidateSHA256(value); err != nil {
		return fmt.Errorf("%w: SHA-256 is invalid", control.ErrInvalid)
	}
	return nil
}

func safeProviderValue(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func identityHash(operation string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(operation))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// DeterministicMultipartUploadID returns the idempotent multipart identity
// used by Create. Transports use it to build an audit intent before the
// repository's source transaction begins.
func DeterministicMultipartUploadID(uploadSessionID, idempotencyKey string) string {
	return "multipart_" + identityHash("create", uploadSessionID, idempotencyKey)
}

func repositoryError(err error) error {
	switch {
	case errors.Is(err, manageddata.ErrNotFound):
		return control.ErrNotFound
	case errors.Is(err, manageddata.ErrConflict):
		return control.ErrConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return control.ErrInternal
	}
}

func storageError(err error) error {
	switch {
	case errors.Is(err, storage.ErrInvalid):
		return control.ErrInvalid
	case errors.Is(err, storage.ErrNotFound):
		return control.ErrNotFound
	case errors.Is(err, storage.ErrIntegrity):
		return control.ErrIntegrity
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return control.ErrBackend
	}
}
