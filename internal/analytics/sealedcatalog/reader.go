// Package sealedcatalog serves immutable DuckLake catalog artifacts.
//
// A sealed catalog is a content-addressed object and not a path selected by a
// caller. This package gives the serving side one small capability: it
// acquires a durable query root, reads the exact object into private target
// storage, verifies the bytes and provider metadata, and attaches the copy
// read-only. The query root is held until Close, so publication or retention
// work cannot collect the catalog while a reader is using it.
package sealedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogartifact"
	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	"github.com/flidai/leapview/internal/extension"
)

var (
	ErrInvalidRequest      = errors.New("sealed catalog request is invalid")
	ErrArtifactUnavailable = errors.New("sealed catalog artifact is unavailable")
	ErrArtifactCorrupt     = errors.New("sealed catalog artifact is corrupt")
	ErrArtifactDigest      = errors.New("sealed catalog artifact digest mismatch")
	ErrArtifactSize        = errors.New("sealed catalog artifact size mismatch")
	ErrArtifactMetadata    = errors.New("sealed catalog artifact metadata mismatch")
	ErrArtifactEvidence    = errors.New("sealed catalog artifact evidence mismatch")
	ErrLeaseRequired       = errors.New("sealed catalog query lease is required")
	ErrAuthorization       = errors.New("sealed catalog live authorization is required")
	ErrLeaseAcquire        = errors.New("sealed catalog query lease acquisition failed")
	ErrLeaseRelease        = errors.New("sealed catalog query lease release failed")
	ErrLeaseRenewal        = errors.New("sealed catalog query lease renewal failed")
	ErrReadOnlyAttach      = errors.New("sealed catalog read-only attach failed")
	ErrStaging             = errors.New("sealed catalog private staging failed")
)

// Object is the provider-neutral result of opening one exact object key.
// Providers must return the object's immutable provider metadata; callers do
// not get a path or storage credentials.
type Object struct {
	Body     io.ReadCloser
	Size     int64
	Metadata map[string]string
}

// ObjectStore is intentionally read-only. Serving has no capability to
// create or overwrite catalog artifacts.
type ObjectStore interface {
	Open(context.Context, string) (Object, error)
}

// LeaseInput, QueryLease, and LeaseRepository are aliases to the narrow
// control-plane contract package. Aliases keep the reader API convenient
// without making deployment storage part of the analytics reader boundary.
type LeaseInput = catalogartifact.LeaseInput
type QueryLease = catalogartifact.QueryLease
type LeaseRepository = catalogartifact.LeaseRepository

// Authorization is a live policy capability. It runs before query-lease
// acquisition, so candidate ownership or a stale browser decision cannot
// obtain an attached catalog. The callback receives no object-store path or
// credentials.
type Authorization func(context.Context, Artifact, LeaseInput) error

// Artifact is the complete exact identity needed for a read-only attach.
// PoolContract is target-owned admission evidence and cannot be inferred from
// the object itself.
type Artifact struct {
	ObjectKey           string
	SealID              string
	CatalogDigest       string
	SizeBytes           int64
	ClosureDigest       string
	QualificationDigest string
	PhysicalPoolID      string
	Compatibility       physicalpool.Compatibility
	PoolContract        *ducklake.PoolContract
}

// Request controls one serving attach. StagingRoot is target-owned; object
// bytes are always copied beneath a private child directory before attach.
type Request struct {
	Artifact            Artifact
	Store               ObjectStore
	Leases              LeaseRepository
	Lease               LeaseInput
	Authorize           Authorization
	StagingRoot         string
	CredentialBootstrap ducklake.CredentialBootstrap
	ExtensionAdmission  extension.Admission
	MaxConnections      int
	MemoryMaxBytes      int64
	TempMaxBytes        int64
	MaxThreads          int
	TempDir             string
	// OnLeaseRenewalFailure is invoked from the heartbeat goroutine with the
	// renewal error, and with nil after a subsequent successful renewal. It is
	// intentionally a narrow health signal; callers still own Close and lease
	// release.
	OnLeaseRenewalFailure func(error)
}

// Reader owns the attached immutable catalog and its durable query lease.
// Environment is read-only and exposes DuckLake's normal query capabilities;
// mutating methods fail with ducklake.ErrReadOnlyEnvironment.
type Reader struct {
	env                   *ducklake.Environment
	detach                func() error
	leases                LeaseRepository
	leaseID               string
	staging               string
	heartbeatCancel       context.CancelFunc
	heartbeatDone         chan struct{}
	heartbeatMu           sync.Mutex
	heartbeatErr          error
	leaseExpiresAt        time.Time
	onLeaseRenewalFailure func(error)

	mu       sync.Mutex
	closed   bool
	closeErr error
}

// Environment returns the read-only DuckLake environment. Callers must not
// retain it after Reader.Close.
func (r *Reader) Environment() *ducklake.Environment {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	return r.env
}

// LeaseID identifies the durable root held by this reader.
func (r *Reader) LeaseID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.leaseID
}

// LeaseRenewalError reports the latest heartbeat failure while the reader is
// still attached. Health callers can observe a lapse before Close is invoked.
func (r *Reader) LeaseRenewalError() error {
	if r == nil {
		return nil
	}
	r.heartbeatMu.Lock()
	defer r.heartbeatMu.Unlock()
	return r.heartbeatErr
}

// Close detaches DuckLake before releasing the durable query lease. It is
// idempotent and safe to call from cleanup paths after a failed query.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	env, leases, leaseID, staging := r.env, r.leases, r.leaseID, r.staging
	if r.heartbeatCancel != nil {
		r.heartbeatCancel()
		if r.heartbeatDone != nil {
			<-r.heartbeatDone
		}
	}
	r.heartbeatMu.Lock()
	heartbeatErr := r.heartbeatErr
	r.heartbeatMu.Unlock()
	// Keep the mutex while detaching so a second Close cannot release the
	// durable root while the first detach is still uncertain.
	closeEnvironment := r.detach
	if closeEnvironment == nil && env != nil {
		closeEnvironment = env.Close
	}
	if closeEnvironment != nil {
		if err := closeEnvironment(); err != nil {
			// An uncertain DuckLake close means the private artifact may still
			// be live. Keep both it and its durable lease rooted; recovery can
			// retry Close or let the lease expire.
			r.closeErr = err
			r.mu.Unlock()
			return err
		}
	}
	var err error
	err = errors.Join(err, heartbeatErr)
	if staging != "" {
		err = errors.Join(err, os.RemoveAll(staging))
	}
	if leases != nil && leaseID != "" {
		if releaseErr := leases.ReleaseQueryLease(context.Background(), leaseID); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("%w: %v", ErrLeaseRelease, releaseErr))
		}
	}
	r.closed = true
	r.closeErr = err
	r.mu.Unlock()
	return err
}

// Open verifies and attaches one exact sealed catalog. Lease acquisition is
// deliberately the first external operation after request validation.
func Open(ctx context.Context, request Request) (*Reader, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	if request.Authorize == nil {
		return nil, ErrAuthorization
	}
	if err := request.Authorize(ctx, request.Artifact, request.Lease); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAuthorization, err)
	}
	lease, err := request.Leases.AcquireQueryLease(ctx, request.Lease)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLeaseAcquire, err)
	}
	if strings.TrimSpace(lease.ID) == "" || lease.ID != request.Lease.ID {
		return nil, fmt.Errorf("%w: adapter returned a lease with the wrong identity", ErrLeaseAcquire)
	}
	release := func() error {
		if releaseErr := request.Leases.ReleaseQueryLease(context.Background(), lease.ID); releaseErr != nil {
			return fmt.Errorf("%w: %v", ErrLeaseRelease, releaseErr)
		}
		return nil
	}
	cleanup := func(baseErr error, staging string) error {
		if staging != "" {
			baseErr = errors.Join(baseErr, os.RemoveAll(staging))
		}
		return errors.Join(baseErr, release())
	}

	stagingRoot := strings.TrimSpace(request.StagingRoot)
	if stagingRoot == "" {
		stagingRoot = filepath.Join(os.TempDir(), "leapview-sealed-catalogs")
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: create root: %v", ErrStaging, err), release())
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: secure root: %v", ErrStaging, err), release())
	}
	staging, err := os.MkdirTemp(stagingRoot, ".sealed-")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: create private child: %v", ErrStaging, err), release())
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return nil, errors.Join(fmt.Errorf("%w: secure private child: %v", ErrStaging, err), release())
	}
	catalogPath := filepath.Join(staging, "catalog.duckdb")
	if err := copyAndVerify(ctx, request.Store, request.Artifact, catalogPath); err != nil {
		return nil, cleanup(err, staging)
	}

	dataPath, err := request.Artifact.PoolContract.Pool.DataPath()
	if err != nil {
		return nil, cleanup(fmt.Errorf("%w: data path: %v", ErrReadOnlyAttach, err), staging)
	}
	env, err := ducklake.Open(ctx, ducklake.Config{
		RootDir: staging, CatalogPath: catalogPath, DataPath: dataPath,
		PhysicalPoolID: request.Artifact.PhysicalPoolID, SharedPool: true,
		Compatibility: request.Artifact.Compatibility, PoolContract: request.Artifact.PoolContract,
		ReadOnly: true, CredentialBootstrap: request.CredentialBootstrap, ExtensionAdmission: request.ExtensionAdmission,
		MaxConnections: request.MaxConnections, MemoryMaxBytes: request.MemoryMaxBytes,
		TempMaxBytes: request.TempMaxBytes, MaxThreads: request.MaxThreads, TempDir: request.TempDir,
	})
	if err != nil {
		return nil, cleanup(fmt.Errorf("%w: %v", ErrReadOnlyAttach, err), staging)
	}
	r := &Reader{env: env, leases: request.Leases, leaseID: lease.ID, staging: staging, leaseExpiresAt: request.Lease.ExpiresAt, onLeaseRenewalFailure: request.OnLeaseRenewalFailure}
	if renewer, ok := request.Leases.(catalogartifact.LeaseRenewer); ok {
		heartbeatCtx, cancel := context.WithCancel(context.Background())
		r.heartbeatCancel, r.heartbeatDone = cancel, make(chan struct{})
		go r.heartbeat(heartbeatCtx, renewer, request.Lease.ExpiresAt.Sub(request.Lease.CreatedAt))
	}
	return r, nil
}

func (r *Reader) heartbeat(ctx context.Context, renewer catalogartifact.LeaseRenewer, lifetime time.Duration) {
	defer close(r.heartbeatDone)
	interval := lifetime / 2
	if interval <= 0 || interval > time.Minute {
		interval = time.Minute
	}
	deadline := r.leaseExpiresAt
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(lifetime)
	}
	if remaining := time.Until(deadline); remaining <= 0 {
		interval = 0
	} else if interval > remaining {
		interval = remaining
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			expires := time.Now().UTC().Add(lifetime)
			renewCtx, cancel := context.WithDeadline(ctx, deadline)
			err := renewer.RenewQueryLease(renewCtx, r.leaseID, expires)
			cancel()
			if err == nil && !time.Now().UTC().Before(deadline) {
				err = context.DeadlineExceeded
			}
			if err != nil {
				// A transient provider error is retried while the last confirmed
				// durable lease expiry remains in the future. Do not poison reader
				// health until that deadline; a renewal call itself is bounded by
				// the same deadline so GC cannot outlive an unreported expiry.
				if time.Now().UTC().Before(deadline) {
					retry := interval / 4
					if retry <= 0 {
						retry = time.Millisecond
					}
					remaining := time.Until(deadline)
					if remaining > 0 && retry > remaining {
						retry = remaining
					}
					timer.Reset(retry)
					continue
				}
				healthErr := fmt.Errorf("%w: %w", ErrLeaseRenewal, err)
				r.heartbeatMu.Lock()
				r.heartbeatErr = healthErr
				r.heartbeatMu.Unlock()
				if callback := r.onLeaseRenewalFailure; callback != nil {
					callback(healthErr)
				}
				// A sustained provider failure through the confirmed expiry
				// poisons the attached environment so callers cannot continue
				// queries against an unrooted catalog; Close can then detach it.
				if r.env != nil {
					r.env.MarkFatal(fmt.Errorf("%w: lease expired after renewal failures", ErrLeaseRenewal))
				}
				return
			}
			deadline = expires
			r.heartbeatMu.Lock()
			r.heartbeatErr = nil
			r.heartbeatMu.Unlock()
			if callback := r.onLeaseRenewalFailure; callback != nil {
				callback(nil)
			}
			timer.Reset(interval)
		}
	}
}

func validateRequest(request Request) error {
	a := request.Artifact
	if request.Store == nil || request.Leases == nil {
		return fmt.Errorf("%w: object store and lease repository are required", ErrInvalidRequest)
	}
	if a.PoolContract == nil {
		return fmt.Errorf("%w: admitted physical-pool contract is required", ErrInvalidRequest)
	}
	if err := a.PoolContract.Validate(); err != nil {
		return fmt.Errorf("%w: pool contract: %v", ErrInvalidRequest, err)
	}
	if strings.TrimSpace(a.SealID) == "" || !validDigest(a.CatalogDigest) || !validDigest(a.ClosureDigest) || !validDigest(a.QualificationDigest) || a.SizeBytes <= 0 {
		return fmt.Errorf("%w: seal/evidence digests and positive size are required", ErrInvalidRequest)
	}
	if a.ObjectKey != catalogseal.CanonicalObjectKey(a.CatalogDigest) {
		return fmt.Errorf("%w: object key is not the canonical content-addressed key", ErrInvalidRequest)
	}
	if a.PhysicalPoolID == "" || a.PhysicalPoolID != a.PoolContract.Pool.ID.String() {
		return fmt.Errorf("%w: physical pool identity does not match admission", ErrInvalidRequest)
	}
	if a.Compatibility != a.PoolContract.Tuple {
		return fmt.Errorf("%w: compatibility tuple does not match admission", ErrInvalidRequest)
	}
	if request.Lease.SealID != a.SealID || request.Lease.CatalogDigest != a.CatalogDigest || request.Lease.ObjectKey != a.ObjectKey || request.Lease.ObjectSize != a.SizeBytes || request.Lease.ClosureDigest != a.ClosureDigest || request.Lease.QualificationDigest != a.QualificationDigest || request.Lease.PhysicalPoolID != a.PhysicalPoolID {
		return fmt.Errorf("%w: query lease is not bound to exact artifact", ErrInvalidRequest)
	}
	if (strings.TrimSpace(request.Lease.CandidateID) == "") == (strings.TrimSpace(request.Lease.GenerationID) == "") {
		return fmt.Errorf("%w: query lease must name one candidate or generation", ErrInvalidRequest)
	}
	if strings.TrimSpace(request.Lease.ID) == "" || strings.TrimSpace(request.Lease.HolderID) == "" {
		return fmt.Errorf("%w: query lease identity is required", ErrInvalidRequest)
	}
	if request.Lease.CreatedAt.IsZero() || request.Lease.ExpiresAt.IsZero() || request.Lease.CreatedAt.Location() != time.UTC || request.Lease.ExpiresAt.Location() != time.UTC || !request.Lease.ExpiresAt.After(request.Lease.CreatedAt) {
		return fmt.Errorf("%w: query lease times are invalid", ErrInvalidRequest)
	}
	return nil
}

func copyAndVerify(ctx context.Context, store ObjectStore, artifact Artifact, path string) error {
	object, err := store.Open(ctx, artifact.ObjectKey)
	if err != nil || object.Body == nil {
		if object.Body != nil {
			_ = object.Body.Close()
		}
		return fmt.Errorf("%w: open exact object: %v", ErrArtifactUnavailable, err)
	}
	if object.Size != artifact.SizeBytes {
		_ = object.Body.Close()
		return ErrArtifactSize
	}
	if object.Metadata == nil || object.Metadata[catalogseal.MetadataDigest] != artifact.CatalogDigest || object.Metadata[catalogseal.MetadataSize] != strconv.FormatInt(artifact.SizeBytes, 10) {
		_ = object.Body.Close()
		return ErrArtifactMetadata
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = object.Body.Close()
		return fmt.Errorf("%w: create private copy: %v", ErrStaging, err)
	}
	hash := sha256.New()
	count, copyErr := io.Copy(file, io.TeeReader(contextReader{ctx: ctx, reader: object.Body}, hash))
	closeFileErr := file.Close()
	closeObjectErr := object.Body.Close()
	if copyErr != nil || closeFileErr != nil || closeObjectErr != nil {
		return fmt.Errorf("%w: copy failed", ErrArtifactUnavailable)
	}
	if count != artifact.SizeBytes {
		return ErrArtifactSize
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != artifact.CatalogDigest {
		return ErrArtifactDigest
	}
	return nil
}

// VerifyObject opens and verifies one exact object without creating a local
// catalog or acquiring a query lease. It is useful to control-plane readers
// and GC evidence checks that need to prove the immutable object before any
// DuckLake attach. The body is always closed before returning.
func VerifyObject(ctx context.Context, store ObjectStore, artifact Artifact) error {
	if store == nil {
		return fmt.Errorf("%w: object store is required", ErrInvalidRequest)
	}
	if !validDigest(artifact.CatalogDigest) || artifact.SizeBytes <= 0 || artifact.ObjectKey != catalogseal.CanonicalObjectKey(artifact.CatalogDigest) {
		return fmt.Errorf("%w: artifact identity is invalid", ErrInvalidRequest)
	}
	object, err := store.Open(ctx, artifact.ObjectKey)
	if err != nil || object.Body == nil {
		if object.Body != nil {
			_ = object.Body.Close()
		}
		return fmt.Errorf("%w: open exact object: %v", ErrArtifactUnavailable, err)
	}
	if object.Size != artifact.SizeBytes {
		_ = object.Body.Close()
		return ErrArtifactSize
	}
	if object.Metadata == nil || object.Metadata[catalogseal.MetadataDigest] != artifact.CatalogDigest || object.Metadata[catalogseal.MetadataSize] != strconv.FormatInt(artifact.SizeBytes, 10) {
		_ = object.Body.Close()
		return ErrArtifactMetadata
	}
	hash := sha256.New()
	count, copyErr := io.Copy(io.Discard, io.TeeReader(contextReader{ctx: ctx, reader: object.Body}, hash))
	closeErr := object.Body.Close()
	if copyErr != nil || closeErr != nil {
		return fmt.Errorf("%w: read exact object", ErrArtifactUnavailable)
	}
	if count != artifact.SizeBytes {
		return ErrArtifactSize
	}
	if got := "sha256:" + hex.EncodeToString(hash.Sum(nil)); got != artifact.CatalogDigest {
		return ErrArtifactDigest
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
