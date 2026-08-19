package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/manageddata"
	"github.com/flidai/leapview/internal/manageddata/storage"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
	"golang.org/x/sync/errgroup"
)

const defaultVerifyConcurrency = 8
const terminalCleanupBatchSize int64 = 100

type Service struct {
	repo              Repository
	blobs             storage.BlobStore
	limits            manageddata.Limits
	uploadTTL         time.Duration
	verifyConcurrency int
	transport         Transport
	now               func() time.Time
	finalizeMu        sync.Mutex
	finalizers        map[string]*finalizeLock
}

type finalizeLock struct {
	mu   sync.Mutex
	refs int
}

func New(repo Repository, blobs storage.BlobStore, config Config) (*Service, error) {
	if repo == nil || blobs == nil || config.Transport == nil {
		return nil, fmt.Errorf("%w: repository, blob store, and transport are required", ErrInvalid)
	}
	if strings.TrimSpace(config.Transport.Backend()) == "" {
		return nil, fmt.Errorf("%w: storage backend is required", ErrInvalid)
	}
	if config.UploadTTL <= 0 {
		return nil, fmt.Errorf("%w: upload TTL must be positive", ErrInvalid)
	}
	if config.Limits.MaxFiles < 0 || config.Limits.MaxFileBytes < 0 || config.Limits.MaxRevisionBytes < 0 {
		return nil, fmt.Errorf("%w: upload limits must not be negative", ErrInvalid)
	}
	concurrency := config.VerifyConcurrency
	if concurrency == 0 {
		concurrency = defaultVerifyConcurrency
	}
	if concurrency < 1 || concurrency > 128 {
		return nil, fmt.Errorf("%w: verification concurrency must be between 1 and 128", ErrInvalid)
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		repo: repo, blobs: blobs, limits: config.Limits, uploadTTL: config.UploadTTL,
		verifyConcurrency: concurrency, transport: config.Transport, now: clock,
		finalizers: map[string]*finalizeLock{},
	}, nil
}

func (s *Service) EnsureCollection(ctx context.Context, request EnsureCollectionRequest) (CollectionResult, error) {
	project, connection, err := validateScope(ctx, request.Project, request.Connection)
	if err != nil {
		return CollectionResult{}, err
	}
	projectID, connectionID, err := parseScope(project, connection)
	if err != nil {
		return CollectionResult{}, err
	}
	collection, err := s.repo.CollectionByProjectConnection(ctx, projectID, connectionID)
	if err == nil {
		if collection.Status != manageddata.CollectionStatusActive {
			return CollectionResult{}, fmt.Errorf("%w: managed data collection is archived", ErrConflict)
		}
		return collectionResult(collection), nil
	}
	if !errors.Is(err, manageddata.ErrNotFound) {
		return CollectionResult{}, repositoryError(err)
	}
	name := strings.TrimSpace(request.Name)
	if name == "" {
		name = connection
	}
	collection, err = s.repo.CreateCollection(ctx, manageddata.CreateCollectionInput{
		ProjectID: projectID, ConnectionID: connectionID, Name: name,
		Description: strings.TrimSpace(request.Description), CreatedBy: strings.TrimSpace(request.Actor),
	})
	if errors.Is(err, manageddata.ErrConflict) {
		collection, err = s.repo.CollectionByProjectConnection(ctx, projectID, connectionID)
	}
	if err != nil {
		return CollectionResult{}, repositoryError(err)
	}
	if collection.Status != manageddata.CollectionStatusActive {
		return CollectionResult{}, fmt.Errorf("%w: managed data collection is archived", ErrConflict)
	}
	return collectionResult(collection), nil
}

func (s *Service) BeginUpload(ctx context.Context, request BeginUploadRequest) (UploadResult, error) {
	project, connection, err := validateScope(ctx, request.Project, request.Connection)
	if err != nil {
		return UploadResult{}, err
	}
	manifest, err := canonicalManifest(request.Manifest, s.limits)
	if err != nil {
		return UploadResult{}, err
	}
	collection, err := s.EnsureCollection(ctx, EnsureCollectionRequest{
		Project: project, Connection: connection, Actor: request.Actor,
	})
	if err != nil {
		return UploadResult{}, err
	}
	baseRevisionID := strings.TrimSpace(request.BaseRevisionID)
	if request.BaseRevisionID != baseRevisionID {
		return UploadResult{}, fmt.Errorf("%w: base revision id must be canonical", ErrInvalid)
	}
	var baseRevision manageddata.RevisionID
	if baseRevisionID != "" {
		var parseErr error
		baseRevision, parseErr = manageddata.ParseRevisionID(baseRevisionID)
		if parseErr != nil {
			return UploadResult{}, fmt.Errorf("%w: invalid base revision id", ErrInvalid)
		}
		base, lookupErr := s.repo.RevisionByID(ctx, baseRevision)
		if lookupErr != nil {
			if errors.Is(lookupErr, manageddata.ErrNotFound) {
				return UploadResult{}, fmt.Errorf("%w: base revision does not exist", ErrConflict)
			}
			return UploadResult{}, repositoryError(lookupErr)
		}
		if base.CollectionID.String() != collection.ID || base.Status != manageddata.RevisionStatusReady {
			return UploadResult{}, fmt.Errorf("%w: base revision is not available for this collection", ErrConflict)
		}
	}
	uploadID, err := newUploadID(project, connection, strings.TrimSpace(request.IdempotencyKey))
	if err != nil {
		return UploadResult{}, err
	}
	expiresAt := s.now().UTC().Add(s.uploadTTL)
	session, err := s.repo.CreateUploadSession(ctx, manageddata.CreateUploadSessionInput{
		ID: manageddata.UploadID(uploadID), CollectionID: projectgraph.ResourceID(collection.ID), BaseRevisionID: baseRevision,
		Manifest: manifest, StorageBackend: s.transport.Backend(), StagingPrefix: "uploads/" + uploadID,
		CreatedBy: strings.TrimSpace(request.Actor), ExpiresAt: expiresAt,
	})
	if errors.Is(err, manageddata.ErrConflict) {
		session, err = s.repo.UploadSessionByID(ctx, manageddata.UploadID(uploadID))
		if err == nil && !sameUpload(session, projectgraph.ResourceID(collection.ID), baseRevision, manifest, s.transport.Backend()) {
			return UploadResult{}, fmt.Errorf("%w: idempotency key was already used for another upload", ErrConflict)
		}
	}
	if err != nil {
		return UploadResult{}, repositoryError(err)
	}
	return s.inspectUpload(ctx, collection, session, true)
}

func (s *Service) RecoverUpload(ctx context.Context, request UploadRequest) (UploadResult, error) {
	collection, session, err := s.scopedSession(ctx, request)
	if err != nil {
		return UploadResult{}, err
	}
	if session.Status == manageddata.UploadStatusOpen && s.expired(session) {
		if _, expireErr := s.repo.ExpireUploadSessions(ctx, s.now().UTC()); expireErr != nil {
			return UploadResult{}, repositoryError(expireErr)
		}
		session, err = s.repo.UploadSessionByID(ctx, session.ID)
		if err != nil {
			return UploadResult{}, repositoryError(err)
		}
	}
	if session.Status == manageddata.UploadStatusExpired {
		manifest, decodeErr := decodeManifest(session.ManifestJSON)
		if decodeErr != nil {
			return UploadResult{}, decodeErr
		}
		if cleanupErr := s.cleanupTerminalAndAck(ctx, session, manifest); cleanupErr != nil {
			return terminalUpload(collection, session, manifest), cleanupErr
		}
		return terminalUpload(collection, session, manifest), nil
	}
	if session.Status == manageddata.UploadStatusComplete {
		result, _, inspectErr := s.inspect(ctx, collection, session, false)
		if inspectErr != nil {
			return result, inspectErr
		}
		if len(result.MissingBlobs) > 0 {
			return result, ErrIntegrity
		}
		manifest, manifestErr := decodeManifest(session.ManifestJSON)
		if manifestErr != nil {
			return result, manifestErr
		}
		if cleanupErr := s.cleanupTerminalAndAck(ctx, session, manifest); cleanupErr != nil {
			return result, cleanupErr
		}
		return result, nil
	}
	if isTerminal(session.Status) {
		manifest, decodeErr := decodeManifest(session.ManifestJSON)
		if decodeErr != nil {
			return UploadResult{}, decodeErr
		}
		result := terminalUpload(collection, session, manifest)
		if cleanupErr := s.cleanupTerminalAndAck(ctx, session, manifest); cleanupErr != nil {
			return result, cleanupErr
		}
		return result, nil
	}
	return s.inspectUpload(ctx, collection, session, true)
}

func (s *Service) FinalizeUpload(ctx context.Context, request UploadRequest) (FinalizeResult, error) {
	release := s.lockFinalization(strings.TrimSpace(request.UploadID))
	defer release()
	started, err := s.BeginFinalizeUpload(ctx, request)
	if err != nil {
		return FinalizeResult{Upload: started}, err
	}
	if started.Status == manageddata.UploadStatusComplete {
		collection, session, scopedErr := s.scopedSession(ctx, request)
		if scopedErr != nil {
			return FinalizeResult{Upload: started}, scopedErr
		}
		return s.finalizedResult(ctx, collection, session, started)
	}
	return s.CompleteFinalizeUpload(ctx, request)
}

func (s *Service) lockFinalization(uploadID string) func() {
	s.finalizeMu.Lock()
	lock := s.finalizers[uploadID]
	if lock == nil {
		lock = &finalizeLock{}
		s.finalizers[uploadID] = lock
	}
	lock.refs++
	s.finalizeMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.finalizeMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.finalizers, uploadID)
		}
		s.finalizeMu.Unlock()
	}
}

func (s *Service) BeginFinalizeUpload(ctx context.Context, request UploadRequest) (UploadResult, error) {
	collection, session, err := s.scopedSession(ctx, request)
	if err != nil {
		return UploadResult{}, err
	}
	if session.Status == manageddata.UploadStatusOpen && s.expired(session) {
		upload, recoverErr := s.RecoverUpload(ctx, request)
		if recoverErr != nil {
			return upload, recoverErr
		}
		return upload, ErrExpired
	}
	if session.Status == manageddata.UploadStatusComplete {
		upload, inspectErr := s.inspectUpload(ctx, collection, session, false)
		if inspectErr != nil {
			return upload, inspectErr
		}
		if len(upload.MissingBlobs) > 0 {
			return upload, ErrIntegrity
		}
		return upload, nil
	}
	if session.Status == manageddata.UploadStatusCommitting {
		if request.Workflow.Job.ID != "" {
			if _, queueErr := s.repo.BeginUploadFinalization(ctx, session.ID, request.Workflow); queueErr != nil {
				return UploadResult{}, repositoryError(queueErr)
			}
		}
		upload, _, inspectErr := s.inspect(ctx, collection, session, true)
		if inspectErr != nil {
			return upload, inspectErr
		}
		upload.Status = manageddata.UploadStatusCommitting
		return upload, nil
	}
	if session.Status != manageddata.UploadStatusOpen {
		manifest, decodeErr := decodeManifest(session.ManifestJSON)
		if decodeErr != nil {
			return UploadResult{}, decodeErr
		}
		return terminalUpload(collection, session, manifest),
			fmt.Errorf("%w: upload session has status %q", ErrConflict, session.Status)
	}
	upload, _, err := s.inspect(ctx, collection, session, true)
	if err != nil {
		return upload, err
	}
	if len(upload.MissingBlobs) > 0 {
		return upload, ErrIncomplete
	}
	session, err = s.repo.BeginUploadFinalization(ctx, session.ID, request.Workflow)
	if err != nil {
		if errors.Is(err, manageddata.ErrConflict) {
			concurrent, loadErr := s.repo.UploadSessionByID(ctx, session.ID)
			if loadErr == nil {
				switch concurrent.Status {
				case manageddata.UploadStatusCommitting:
					upload.Status = concurrent.Status
					return upload, nil
				case manageddata.UploadStatusComplete:
					return s.inspectUpload(ctx, collection, concurrent, false)
				}
			}
		}
		return upload, repositoryError(err)
	}
	upload.Status = session.Status
	return upload, nil
}

func (s *Service) CompleteFinalizeUpload(ctx context.Context, request UploadRequest) (FinalizeResult, error) {
	collection, session, err := s.scopedSession(ctx, request)
	if err != nil {
		return FinalizeResult{}, err
	}
	if session.Status == manageddata.UploadStatusComplete {
		upload, inspectErr := s.inspectUpload(ctx, collection, session, false)
		if inspectErr != nil {
			return FinalizeResult{Upload: upload}, inspectErr
		}
		return s.finalizedResult(ctx, collection, session, upload)
	}
	if session.Status != manageddata.UploadStatusCommitting {
		manifest, decodeErr := decodeManifest(session.ManifestJSON)
		if decodeErr != nil {
			return FinalizeResult{}, decodeErr
		}
		return FinalizeResult{Upload: terminalUpload(collection, session, manifest)}, fmt.Errorf("%w: upload session has status %q", ErrConflict, session.Status)
	}
	upload, inspection, err := s.inspect(ctx, collection, session, true)
	if err != nil {
		return s.failFinalizeUpload(ctx, session.ID, upload, err)
	}
	if len(upload.MissingBlobs) > 0 {
		return s.failFinalizeUpload(ctx, session.ID, upload, ErrIntegrity)
	}
	stored := make([]manageddata.StoredFile, 0, len(upload.Manifest.Files))
	for _, file := range upload.Manifest.Files {
		blob := inspection[file.SHA256]
		stored = append(stored, manageddata.StoredFile{
			File: file, StorageKey: blob.URI, MediaType: mediaType(file.Path),
		})
	}
	revision, err := s.repo.CompleteUpload(ctx, manageddata.CompleteUploadInput{
		SessionID: session.ID, RevisionID: revisionID(session.ID.String()), Files: stored,
	})
	if err != nil {
		if errors.Is(err, manageddata.ErrConflict) {
			return s.waitForConcurrentFinalization(ctx, collection, session.ID, upload)
		}
		return s.failFinalizeUpload(ctx, session.ID, upload, repositoryError(err))
	}
	session, err = s.repo.UploadSessionByID(ctx, session.ID)
	if err != nil {
		return FinalizeResult{Upload: upload}, repositoryError(err)
	}
	result, resultErr := s.finalizedResultWithRevision(ctx, collection, session, upload, revision)
	if resultErr != nil {
		return result, resultErr
	}
	manifest, manifestErr := decodeManifest(session.ManifestJSON)
	if manifestErr != nil {
		return result, manifestErr
	}
	if cleanupErr := s.cleanupTransport(ctx, session, manifest); cleanupErr != nil {
		return result, cleanupErr
	}
	if ackErr := s.acknowledgeCleanup(ctx, session.ID); ackErr != nil {
		return result, ackErr
	}
	return result, nil
}

func (s *Service) waitForConcurrentFinalization(ctx context.Context, collection CollectionResult, sessionID manageddata.UploadID, upload UploadResult) (FinalizeResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		session, err := s.repo.UploadSessionByID(waitCtx, sessionID)
		if err != nil {
			return FinalizeResult{Upload: upload}, repositoryError(err)
		}
		switch session.Status {
		case manageddata.UploadStatusComplete:
			return s.finalizedResult(waitCtx, collection, session, upload)
		case manageddata.UploadStatusFailed, manageddata.UploadStatusAborted, manageddata.UploadStatusExpired:
			return FinalizeResult{Upload: terminalUpload(collection, session, upload.Manifest)}, fmt.Errorf("%w: concurrent finalization ended with status %q", ErrConflict, session.Status)
		}
		select {
		case <-waitCtx.Done():
			return FinalizeResult{Upload: upload}, fmt.Errorf("%w: wait for concurrent finalization: %v", ErrConflict, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Service) failFinalizeUpload(ctx context.Context, sessionID manageddata.UploadID, upload UploadResult, finalizationErr error) (FinalizeResult, error) {
	failed, persistErr := s.repo.FailUploadFinalization(ctx, sessionID, finalizationErr.Error())
	if persistErr == nil {
		upload.Status = failed.Status
		upload.Error = failed.Error
		return FinalizeResult{Upload: upload}, finalizationErr
	}
	return FinalizeResult{Upload: upload}, errors.Join(finalizationErr, repositoryError(persistErr))
}

func (s *Service) AbortUpload(ctx context.Context, request UploadRequest) (UploadResult, error) {
	collection, session, err := s.scopedSession(ctx, request)
	if err != nil {
		return UploadResult{}, err
	}
	manifest, err := decodeManifest(session.ManifestJSON)
	if err != nil {
		return UploadResult{}, err
	}
	if session.Status == manageddata.UploadStatusComplete {
		return terminalUpload(collection, session, manifest), fmt.Errorf("%w: completed upload cannot be aborted", ErrConflict)
	}
	if session.Status == manageddata.UploadStatusOpen {
		var abortErr error
		if request.Workflow.Event.EventType != "" {
			if workflowRepo, ok := s.repo.(interface {
				AbortUploadSessionWithWorkflow(context.Context, manageddata.UploadID, jobs.WorkflowIntent) error
			}); ok {
				abortErr = workflowRepo.AbortUploadSessionWithWorkflow(ctx, session.ID, request.Workflow)
			} else {
				abortErr = s.repo.AbortUploadSession(ctx, session.ID)
			}
		} else {
			abortErr = s.repo.AbortUploadSession(ctx, session.ID)
		}
		if abortErr != nil && !errors.Is(abortErr, manageddata.ErrConflict) {
			return UploadResult{}, repositoryError(abortErr)
		}
		session, err = s.repo.UploadSessionByID(ctx, session.ID)
		if err != nil {
			return UploadResult{}, repositoryError(err)
		}
		if session.Status == manageddata.UploadStatusComplete {
			return terminalUpload(collection, session, manifest), fmt.Errorf("%w: upload completed while aborting", ErrConflict)
		}
	}
	result := terminalUpload(collection, session, manifest)
	if session.Status != manageddata.UploadStatusAborted && session.Status != manageddata.UploadStatusExpired {
		return result, fmt.Errorf("%w: upload session has status %q", ErrConflict, session.Status)
	}
	if err := s.cleanupTerminalAndAck(ctx, session, manifest); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Service) ExpireUploads(ctx context.Context) (ExpireResult, error) {
	if ctx == nil {
		return ExpireResult{}, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	count, err := s.repo.ExpireUploadSessions(ctx, s.now().UTC())
	if err != nil {
		return ExpireResult{}, repositoryError(err)
	}
	result := ExpireResult{Expired: count}
	cleanup, cleanupErr := s.CleanupTerminalUploads(ctx, terminalCleanupBatchSize)
	if cleanupErr != nil {
		return result, cleanupErr
	}
	result.Cleaned, result.CleanupBacklog, result.CleanupFailures = cleanup.Cleaned, cleanup.Backlog, cleanup.Failures
	return result, nil
}

// terminalUploadLister is intentionally optional: adapters used by tests and
// non-SQL control planes can continue to implement the core Repository port.
// The production SQLite repository supplies this bounded scan, keeping
// terminal sessions discoverable until their transport staging is reclaimed.
type terminalUploadLister interface {
	ListUploadSessionsForCleanup(context.Context, int64) ([]manageddata.UploadSession, error)
}

type terminalUploadAcker interface {
	MarkUploadCleanupComplete(context.Context, manageddata.UploadID) error
}

func (s *Service) acknowledgeCleanup(ctx context.Context, uploadID manageddata.UploadID) error {
	if acker, ok := s.repo.(terminalUploadAcker); ok {
		return repositoryError(acker.MarkUploadCleanupComplete(ctx, uploadID))
	}
	return nil
}

func (s *Service) cleanupTerminalAndAck(ctx context.Context, session manageddata.UploadSession, manifest manageddata.Manifest) error {
	if err := s.cleanupTransport(ctx, session, manifest); err != nil {
		return err
	}
	return s.acknowledgeCleanup(ctx, session.ID)
}

type TerminalCleanupResult struct {
	Cleaned  int64
	Backlog  int64
	Failures int64
}

func (s *Service) CleanupTerminalUploads(ctx context.Context, limit int64) (TerminalCleanupResult, error) {
	if ctx == nil {
		return TerminalCleanupResult{}, fmt.Errorf("%w: context is required", ErrInvalid)
	}
	lister, ok := s.repo.(terminalUploadLister)
	if !ok {
		return TerminalCleanupResult{}, nil
	}
	if limit <= 0 {
		limit = terminalCleanupBatchSize
	}
	// Read one sentinel row so a full batch is not reported as backlog when it
	// was exactly the final page. The durable ack column ensures later passes
	// continue from the next uncleaned session.
	sessions, err := lister.ListUploadSessionsForCleanup(ctx, limit+1)
	if err != nil {
		return TerminalCleanupResult{}, repositoryError(err)
	}
	result := TerminalCleanupResult{}
	if int64(len(sessions)) > limit {
		result.Backlog = 1 // at least one more row is known to remain
		sessions = sessions[:limit]
	}
	acker, _ := s.repo.(terminalUploadAcker)
	for _, session := range sessions {
		if !isTerminal(session.Status) {
			continue
		}
		manifest, decodeErr := decodeManifest(session.ManifestJSON)
		if decodeErr != nil {
			result.Failures++
			continue
		}
		if cleanupErr := s.cleanupTransport(ctx, session, manifest); cleanupErr != nil {
			result.Failures++
			continue
		}
		if acker != nil {
			if ackErr := acker.MarkUploadCleanupComplete(ctx, session.ID); ackErr != nil {
				result.Failures++
				continue
			}
		}
		result.Cleaned++
	}
	return result, nil
}

type inspectedBlobs map[string]storage.Blob

func (s *Service) inspectUpload(ctx context.Context, collection CollectionResult, session manageddata.UploadSession, describe bool) (UploadResult, error) {
	result, _, err := s.inspect(ctx, collection, session, describe)
	return result, err
}

func (s *Service) inspect(ctx context.Context, collection CollectionResult, session manageddata.UploadSession, describe bool) (UploadResult, inspectedBlobs, error) {
	manifest, err := decodeManifest(session.ManifestJSON)
	if err != nil {
		return UploadResult{}, nil, err
	}
	expectations := uniqueBlobs(manifest)
	verified := make([]storage.Blob, len(expectations))
	present := make([]bool, len(expectations))
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(s.verifyConcurrency)
	for index := range expectations {
		index := index
		group.Go(func() error {
			expected := expectations[index]
			blob, statErr := s.blobs.Stat(groupContext, expected.SHA256)
			if errors.Is(statErr, storage.ErrNotFound) {
				return nil
			}
			if statErr != nil {
				return storageError(statErr)
			}
			stable, stableErr := verifiedBlob(expected, blob)
			if stableErr != nil {
				return stableErr
			}
			verified[index] = stable
			present[index] = true
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return baseUpload(collection, session, manifest), nil, err
	}
	byDigest := make(inspectedBlobs, len(expectations))
	missing := make([]MissingBlob, 0)
	for index, expected := range expectations {
		if present[index] {
			byDigest[expected.SHA256] = verified[index]
			continue
		}
		item := MissingBlob{SHA256: expected.SHA256, Size: expected.Size, Paths: append([]string(nil), expected.Paths...)}
		if describe {
			description, describeErr := s.transport.Describe(ctx, TransportRequest{
				UploadID: session.ID.String(), SHA256: expected.SHA256, Size: expected.Size,
				Paths: append([]string(nil), expected.Paths...), ExpiresAt: timestampValue(session.ExpiresAt),
			})
			if describeErr != nil {
				return baseUpload(collection, session, manifest), nil, storageError(describeErr)
			}
			item.Transport = description
		}
		missing = append(missing, item)
	}
	result := baseUpload(collection, session, manifest)
	result.MissingBlobs = missing
	missingByDigest := make(map[string]TransportDescription, len(missing))
	for _, item := range missing {
		missingByDigest[item.SHA256] = item.Transport
	}
	for index, file := range manifest.Files {
		if _, ok := byDigest[file.SHA256]; ok {
			result.Files[index].Status = FileStatusVerified
			result.Files[index].Transport = TransportDescription{Protocol: ProtocolAlreadyPresent}
			result.Progress.VerifiedFiles++
			result.Progress.VerifiedBytes += file.Size
		} else {
			result.Files[index].Transport = missingByDigest[file.SHA256]
		}
	}
	if session.Status == manageddata.UploadStatusOpen {
		err = s.repo.UpdateUploadProgress(ctx, session.ID, manageddata.UploadProgress{
			UploadedFileCount: result.Progress.VerifiedFiles, UploadedSizeBytes: result.Progress.VerifiedBytes,
		})
		if err != nil && !errors.Is(err, manageddata.ErrConflict) {
			return result, nil, repositoryError(err)
		}
	}
	return result, byDigest, nil
}

func (s *Service) scopedSession(ctx context.Context, request UploadRequest) (CollectionResult, manageddata.UploadSession, error) {
	project, connection, err := validateScope(ctx, request.Project, request.Connection)
	if err != nil {
		return CollectionResult{}, manageddata.UploadSession{}, err
	}
	if request.UploadID != strings.TrimSpace(request.UploadID) {
		return CollectionResult{}, manageddata.UploadSession{}, fmt.Errorf("%w: upload id must be canonical", ErrInvalid)
	}
	if strings.TrimSpace(request.UploadID) == "" {
		return CollectionResult{}, manageddata.UploadSession{}, fmt.Errorf("%w: upload id is required", ErrInvalid)
	}
	projectID, connectionID, err := parseScope(project, connection)
	if err != nil {
		return CollectionResult{}, manageddata.UploadSession{}, err
	}
	collection, err := s.repo.CollectionByProjectConnection(ctx, projectID, connectionID)
	if err != nil {
		return CollectionResult{}, manageddata.UploadSession{}, repositoryError(err)
	}
	uploadID, parseErr := manageddata.ParseUploadID(strings.TrimSpace(request.UploadID))
	if parseErr != nil {
		return CollectionResult{}, manageddata.UploadSession{}, fmt.Errorf("%w: invalid upload id", ErrInvalid)
	}
	session, err := s.repo.UploadSessionByID(ctx, uploadID)
	if err != nil {
		return CollectionResult{}, manageddata.UploadSession{}, repositoryError(err)
	}
	if session.CollectionID != collection.ID {
		return CollectionResult{}, manageddata.UploadSession{}, ErrNotFound
	}
	if timestampValue(session.ExpiresAt).IsZero() {
		return CollectionResult{}, manageddata.UploadSession{}, ErrIntegrity
	}
	return collectionResult(collection), session, nil
}

func (s *Service) finalizedResult(ctx context.Context, collection CollectionResult, session manageddata.UploadSession, upload UploadResult) (FinalizeResult, error) {
	revision, err := s.repo.RevisionByID(ctx, session.RevisionID)
	if err != nil {
		return FinalizeResult{Upload: upload}, repositoryError(err)
	}
	result, resultErr := s.finalizedResultWithRevision(ctx, collection, session, upload, revision)
	if resultErr != nil {
		return result, resultErr
	}
	// Publish the immutable revision first. Staging remains discoverable until
	// this point, so a crash can be repaired by the terminal cleanup worker.
	manifest, manifestErr := decodeManifest(session.ManifestJSON)
	if manifestErr != nil {
		return result, manifestErr
	}
	if cleanupErr := s.cleanupTransport(ctx, session, manifest); cleanupErr != nil {
		return result, cleanupErr
	}
	if ackErr := s.acknowledgeCleanup(ctx, session.ID); ackErr != nil {
		return result, ackErr
	}
	return result, nil
}

func (s *Service) finalizedResultWithRevision(ctx context.Context, collection CollectionResult, session manageddata.UploadSession, upload UploadResult, revision manageddata.Revision) (FinalizeResult, error) {
	files, err := s.repo.ListRevisionFiles(ctx, revision.ID)
	if err != nil {
		return FinalizeResult{Upload: upload}, repositoryError(err)
	}
	manifest, err := decodeManifest(revision.ManifestJSON)
	if err != nil {
		return FinalizeResult{Upload: upload}, err
	}
	if err := validateRevision(collection, session, revision, manifest, files); err != nil {
		return FinalizeResult{Upload: upload}, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	upload.Status = manageddata.UploadStatusComplete
	upload.RevisionID = revision.ID.String()
	upload.CompletedAt = normalizeTimestamp(session.CompletedAt)
	result := RevisionResult{
		ID: revision.ID.String(), Collection: collection, Digest: revision.Digest, Sequence: revision.Sequence,
		Status: revision.Status, Manifest: manifest, FileCount: revision.FileCount, SizeBytes: revision.SizeBytes,
		CreatedBy: revision.CreatedBy, CreatedAt: normalizeTimestamp(revision.CreatedAt), ReadyAt: normalizeTimestamp(revision.ReadyAt),
		Files: make([]RevisionFileResult, len(files)),
	}
	for index, file := range files {
		result.Files[index] = RevisionFileResult{
			Path: file.Path, Size: file.Size, SHA256: file.SHA256, StorageURI: file.StorageKey,
			MediaType: file.MediaType, ETag: file.ETag,
		}
	}
	return FinalizeResult{Upload: upload, Revision: result}, nil
}

func (s *Service) cleanupTransport(ctx context.Context, session manageddata.UploadSession, manifest manageddata.Manifest) error {
	var failed bool
	for _, expected := range uniqueBlobs(manifest) {
		if err := s.transport.Abort(ctx, TransportRequest{
			UploadID: session.ID.String(), SHA256: expected.SHA256, Size: expected.Size,
			Paths: append([]string(nil), expected.Paths...), ExpiresAt: timestampValue(session.ExpiresAt),
		}); err != nil && !errors.Is(err, storage.ErrNotFound) {
			failed = true
		}
	}
	if failed {
		return ErrBackend
	}
	return nil
}

func (s *Service) expired(session manageddata.UploadSession) bool {
	expiresAt := timestampValue(session.ExpiresAt)
	return !expiresAt.IsZero() && !expiresAt.After(s.now().UTC())
}

type blobExpectation struct {
	SHA256 string
	Size   int64
	Paths  []string
}

func uniqueBlobs(manifest manageddata.Manifest) []blobExpectation {
	byDigest := make(map[string]*blobExpectation)
	for _, file := range manifest.Files {
		expected := byDigest[file.SHA256]
		if expected == nil {
			expected = &blobExpectation{SHA256: file.SHA256, Size: file.Size}
			byDigest[file.SHA256] = expected
		}
		expected.Paths = append(expected.Paths, file.Path)
	}
	result := make([]blobExpectation, 0, len(byDigest))
	for _, expected := range byDigest {
		sort.Strings(expected.Paths)
		result = append(result, *expected)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SHA256 < result[j].SHA256 })
	return result
}

func canonicalManifest(manifest manageddata.Manifest, limits manageddata.Limits) (manageddata.Manifest, error) {
	if len(manifest.Files) == 0 {
		return manageddata.Manifest{}, fmt.Errorf("%w: manifest requires at least one file", ErrInvalid)
	}
	if err := manifest.Validate(limits); err != nil {
		return manageddata.Manifest{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	sizes := make(map[string]int64, len(manifest.Files))
	for _, file := range manifest.Files {
		if size, ok := sizes[file.SHA256]; ok && size != file.Size {
			return manageddata.Manifest{}, fmt.Errorf("%w: the same content digest has inconsistent sizes", ErrInvalid)
		}
		sizes[file.SHA256] = file.Size
	}
	canonical, err := manifest.CanonicalJSON()
	if err != nil {
		return manageddata.Manifest{}, fmt.Errorf("%w: manifest is invalid", ErrInvalid)
	}
	return decodeManifest(string(canonical))
}

func decodeManifest(value string) (manageddata.Manifest, error) {
	var manifest manageddata.Manifest
	if err := json.Unmarshal([]byte(value), &manifest); err != nil {
		return manageddata.Manifest{}, ErrIntegrity
	}
	if err := manifest.Validate(manageddata.Limits{}); err != nil || len(manifest.Files) == 0 {
		return manageddata.Manifest{}, ErrIntegrity
	}
	return manifest, nil
}

func sameUpload(session manageddata.UploadSession, collectionID projectgraph.ResourceID, baseRevisionID manageddata.RevisionID, manifest manageddata.Manifest, backend string) bool {
	canonical, err := manifest.CanonicalJSON()
	return err == nil && session.CollectionID == collectionID && session.BaseRevisionID == baseRevisionID &&
		session.ManifestJSON == string(canonical) && session.StorageBackend == backend
}

func verifiedBlob(expected blobExpectation, actual storage.Blob) (storage.Blob, error) {
	if actual.SHA256 != expected.SHA256 || actual.Size != expected.Size {
		return storage.Blob{}, ErrIntegrity
	}
	uri, err := url.Parse(actual.URI)
	if err != nil || uri.Scheme == "" || uri.User != nil || uri.RawQuery != "" || uri.Fragment != "" ||
		!strings.Contains(uri.Host+uri.Path, expected.SHA256) {
		return storage.Blob{}, ErrIntegrity
	}
	return storage.Blob{SHA256: actual.SHA256, Size: actual.Size, URI: uri.String()}, nil
}

func baseUpload(collection CollectionResult, session manageddata.UploadSession, manifest manageddata.Manifest) UploadResult {
	result := UploadResult{
		ID: session.ID.String(), RevisionID: revisionID(session.ID.String()).String(), Collection: collection,
		BaseRevisionID: session.BaseRevisionID.String(), Status: session.Status, Manifest: manifest,
		Progress:  Progress{ExpectedFiles: session.ExpectedFileCount, ExpectedBytes: session.ExpectedSizeBytes},
		CreatedAt: normalizeTimestamp(session.CreatedAt), ExpiresAt: normalizeTimestamp(session.ExpiresAt),
		CompletedAt: normalizeTimestamp(session.CompletedAt), Error: safePersistedError(session.Error),
		Files: make([]UploadFile, len(manifest.Files)),
	}
	if session.RevisionID != "" {
		result.RevisionID = session.RevisionID.String()
	}
	for index, file := range manifest.Files {
		result.Files[index] = UploadFile{File: file, Status: FileStatusPending}
	}
	return result
}

func terminalUpload(collection CollectionResult, session manageddata.UploadSession, manifest manageddata.Manifest) UploadResult {
	return baseUpload(collection, session, manifest)
}

func collectionResult(collection manageddata.Collection) CollectionResult {
	return CollectionResult{
		ID: collection.ID.String(), Project: collection.ProjectID.String(), Connection: collection.ConnectionID.String(),
		Name: collection.Name, Description: collection.Description, Status: collection.Status,
		CreatedAt: normalizeTimestamp(collection.CreatedAt), UpdatedAt: normalizeTimestamp(collection.UpdatedAt),
	}
}

func newUploadID(project, connection, idempotencyKey string) (string, error) {
	if idempotencyKey != "" {
		sum := sha256.Sum256([]byte(project + "\x00" + connection + "\x00" + idempotencyKey))
		return "upload_" + hex.EncodeToString(sum[:]), nil
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", ErrInternal
	}
	return "upload_" + hex.EncodeToString(value[:]), nil
}

func revisionID(uploadID string) manageddata.RevisionID {
	sum := sha256.Sum256([]byte(uploadID))
	return manageddata.RevisionID("revision_" + hex.EncodeToString(sum[:]))
}

func validateScope(ctx context.Context, project, connection string) (string, string, error) {
	if ctx == nil {
		return "", "", fmt.Errorf("%w: context is required", ErrInvalid)
	}
	if project == "" || connection == "" || project != strings.TrimSpace(project) || connection != strings.TrimSpace(connection) {
		return "", "", fmt.Errorf("%w: project and connection are required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return project, connection, nil
}

func parseScope(project, connection string) (projectgraph.ResourceID, projectgraph.ResourceID, error) {
	projectID, err := projectgraph.NewResourceID(project)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid project id", ErrInvalid)
	}
	connectionID, err := projectgraph.NewResourceID(connection)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid connection id", ErrInvalid)
	}
	return projectID, connectionID, nil
}

func repositoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, manageddata.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, manageddata.ErrConflict):
		return ErrConflict
	default:
		return ErrInternal
	}
}

func storageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, ErrInvalid), errors.Is(err, storage.ErrInvalid):
		return ErrInvalid
	case errors.Is(err, ErrIntegrity), errors.Is(err, storage.ErrIntegrity):
		return ErrIntegrity
	default:
		return ErrBackend
	}
}

func isTerminal(status manageddata.UploadStatus) bool {
	return status == manageddata.UploadStatusComplete || status == manageddata.UploadStatusAborted ||
		status == manageddata.UploadStatusExpired || status == manageddata.UploadStatusFailed
}

func mediaType(logicalPath string) string {
	switch strings.ToLower(path.Ext(logicalPath)) {
	case ".csv":
		return "text/csv"
	case ".json", ".jsonl", ".ndjson":
		return "application/json"
	case ".parquet":
		return "application/vnd.apache.parquet"
	case ".arrow", ".feather":
		return "application/vnd.apache.arrow.file"
	default:
		return "application/octet-stream"
	}
}

func validateRevision(collection CollectionResult, session manageddata.UploadSession, revision manageddata.Revision, manifest manageddata.Manifest, files []manageddata.RevisionFile) error {
	var sizeBytes int64
	expected := make(map[string]manageddata.File, len(manifest.Files))
	for _, file := range manifest.Files {
		expected[file.Path] = file
		sizeBytes += file.Size
	}
	if revision.ID == "" || revision.ID != session.RevisionID || revision.CollectionID.String() != collection.ID ||
		revision.Status != manageddata.RevisionStatusReady || revision.Digest != manifest.RevisionID() ||
		revision.FileCount != int64(len(manifest.Files)) || revision.SizeBytes != sizeBytes || len(files) != len(manifest.Files) {
		return ErrIntegrity
	}
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		want, ok := expected[file.Path]
		if !ok || file.RevisionID != revision.ID || file.Size != want.Size || file.SHA256 != want.SHA256 {
			return ErrIntegrity
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return ErrIntegrity
		}
		seen[file.Path] = struct{}{}
		if _, err := verifiedBlob(blobExpectation{SHA256: file.SHA256, Size: file.Size}, storage.Blob{
			SHA256: file.SHA256, Size: file.Size, URI: file.StorageKey,
		}); err != nil {
			return ErrIntegrity
		}
	}
	return nil
}

func normalizeTimestamp(value string) string {
	parsed := timestampValue(value)
	if parsed.IsZero() {
		return ""
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func timestampValue(value string) time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func safePersistedError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "managed data upload failed"
}
