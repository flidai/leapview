// Package candidatecatalog constructs disposable, private DuckLake catalogs
// for one exact build attempt.
//
// A working catalog is deliberately not a candidate.  It is a temporary
// writable DuckLake environment which is either returned to the caller for
// qualification/materialization or closed and removed.  The only physical
// state which can outlive the handle is immutable data written by DuckLake to
// the admitted physical pool; this package never performs pool cleanup.
package candidatecatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/ducklake"
	"github.com/flidai/leapview/internal/analytics/physicalpool"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/extension"
)

var (
	ErrInvalidRequest             = errors.New("candidate catalog request is invalid")
	ErrLeaseRequired              = errors.New("an active exact writer lease verifier is required")
	ErrLeaseMismatch              = errors.New("writer lease does not match build attempt or physical pool")
	ErrLeaseExpired               = errors.New("writer lease is expired")
	ErrPoolContractRequired       = errors.New("an admitted physical-pool contract is required")
	ErrExtensionAdmissionRequired = errors.New("an exact DuckDB extension admission is required")
	ErrBaseMismatch               = errors.New("sealed base catalog does not match the admitted physical pool")
	ErrArtifactDigest             = errors.New("sealed catalog artifact digest mismatch")
	ErrArtifactSize               = errors.New("sealed catalog artifact size mismatch")
	ErrArtifactSource             = errors.New("sealed catalog artifact reader failed")
	ErrMutationFailed             = errors.New("candidate catalog mutation failed")
	ErrClosed                     = errors.New("candidate catalog working handle is closed")
)

const (
	// LeaseActive is the only writer-lease status admitted for construction.
	LeaseActive = "active"

	// DefaultStagingDir is intentionally private to the process. Callers
	// should normally supply StagingRoot so lifecycle cleanup stays in a
	// target-owned directory.
	DefaultStagingDir = "candidatecatalog"
)

// ArtifactReader opens an immutable sealed catalog object. It intentionally
// has no local-path operation: callers provide an object-store abstraction (or
// an in-memory immutable reader in tests), never a source database filename.
// Open must return a fresh reader positioned at byte zero.
type ArtifactReader interface {
	Open(context.Context) (io.ReadCloser, error)
}

// ArtifactReaderFunc adapts a function to ArtifactReader.
type ArtifactReaderFunc func(context.Context) (io.ReadCloser, error)

func (f ArtifactReaderFunc) Open(ctx context.Context) (io.ReadCloser, error) {
	if f == nil {
		return nil, errors.New("nil artifact reader")
	}
	return f(ctx)
}

// ObjectStore is the minimal target-controlled object abstraction needed to
// read a sealed catalog. It deliberately exposes bytes by key, not a local
// source path.
type ObjectStore interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

// ObjectReader reads one key from an ObjectStore.
type ObjectReader struct {
	Store ObjectStore
	Key   string
}

func (r ObjectReader) Open(ctx context.Context) (io.ReadCloser, error) {
	if r.Store == nil || strings.TrimSpace(r.Key) == "" {
		return nil, errors.New("object reader requires store and key")
	}
	return r.Store.Open(ctx, r.Key)
}

// SealedArtifact is the complete expected identity of an immutable catalog
// artifact. The artifact's reader is the only source of bytes; no local source
// path is accepted by this package.
type SealedArtifact struct {
	ObjectKey      string
	Digest         string
	SizeBytes      int64
	PhysicalPoolID string
	Compatibility  physicalpool.Compatibility
	Reader         ArtifactReader
}

// ImmutableArtifact and Artifact are readable aliases useful to callers which
// keep sealed artifacts in a control-plane model.
type ImmutableArtifact = SealedArtifact
type Artifact = SealedArtifact

// WriterLease is the small construction-time lease identity. Durable
// deployments may adapt their richer control-plane lease to this value; the
// verifier remains the source of truth for active status and fencing.
type WriterLease struct {
	ID             string
	AttemptID      string
	PhysicalPoolID string
	HolderID       string
	Epoch          int64
	ExpiresAt      time.Time
	Status         string
}

// LeaseVerifier is called before any physical work and again before each
// operation which can mutate the private catalog. It must verify the exact
// durable lease, attempt, pool, and current fencing epoch in its own control
// transaction.
type LeaseVerifier func(context.Context, WriterLease) error

// Mutation receives a working handle and may use normal logical relation
// names through Commit/Exec. It must not retain the handle after Build returns.
type Mutation func(context.Context, *WorkingCatalog) error

// Request describes one exact durable build attempt. AttemptID is part of the
// staging identity; every invocation gets a fresh staging even when a
// previous attempt failed.
type Request struct {
	AttemptID    string
	StagingRoot  string
	PoolContract *ducklake.PoolContract
	// ExtensionAdmission is the target-reviewed source of exact DuckDB
	// extension artifacts. Candidate catalogs never install or resolve
	// extensions implicitly.
	ExtensionAdmission extension.Admission
	// CredentialBootstrap provisions ephemeral target-owned object-store
	// credentials for every DuckDB connector. Secrets never enter the request
	// identity or pool contract; nil is valid for public/local pools.
	CredentialBootstrap ducklake.CredentialBootstrap
	Lease               WriterLease
	VerifyLease         LeaseVerifier
	Base                *SealedArtifact
	DataPath            string
	Now                 func() time.Time
}

// BuildRequest is a readable alias for Request.
type BuildRequest = Request

// WorkingCatalog is a private writable DuckLake environment for exactly one
// build attempt. It has no ready/candidate state and cannot be sealed through
// this package.
type WorkingCatalog struct {
	env     *ducklake.Environment
	staging string
	request Request

	mu         sync.Mutex
	closed     bool
	detached   *detachedState
	normalized *NormalizationResult
}

// RememberNormalization binds the exact metadata result produced during the
// durable normalizing phase. Qualification consumes it without re-running
// normalization; callers must not mutate the supplied value afterward.
func (w *WorkingCatalog) RememberNormalization(result NormalizationResult) error {
	if err := w.checkOpen(); err != nil {
		return err
	}
	clone := result
	clone.Snapshots = append([]ducklake.Snapshot(nil), result.Snapshots...)
	clone.Tables = append([]CatalogTable(nil), result.Tables...)
	clone.Closure = cloneQualificationClosure(result.Closure)
	w.normalized = &clone
	return nil
}

// DetachedCatalog is a closed private catalog handed to the caller for
// normalization, qualification, and sealing. It is still only a working
// artifact: this package creates no candidate or ready record. The caller
// owns its lifecycle after DetachForSeal and must Remove it if sealing is
// abandoned.
type DetachedCatalog struct {
	state *detachedState
}

type detachedState struct {
	staging     string
	catalogPath string
	removed     bool
	mu          sync.Mutex
}

// Open creates a private working catalog. If Base is non-nil its immutable
// bytes are streamed into a newly created 0600 file while hashing and then
// verified byte-for-byte before DuckLake opens it. A nil Base creates an empty
// target-owned catalog.
func Open(ctx context.Context, request Request) (*WorkingCatalog, error) {
	if err := validateRequest(ctx, request); err != nil {
		return nil, err
	}
	if err := verifyLease(ctx, request); err != nil {
		return nil, err
	}

	stagingRoot := strings.TrimSpace(request.StagingRoot)
	if stagingRoot == "" {
		stagingRoot = filepath.Join(os.TempDir(), DefaultStagingDir)
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create candidate staging root: %w", err)
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("secure candidate staging root: %w", err)
	}
	staging, err := os.MkdirTemp(stagingRoot, ".attempt-"+safePrefix(request.AttemptID)+"-")
	if err != nil {
		return nil, fmt.Errorf("create private candidate staging: %w", err)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("secure candidate staging: %w", err)
	}

	catalogPath := filepath.Join(staging, "catalog.duckdb")
	if request.Base != nil {
		if err := copyArtifact(ctx, request, catalogPath); err != nil {
			_ = os.RemoveAll(staging)
			return nil, err
		}
	}
	if err := verifyLease(ctx, request); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}

	dataPath, err := request.PoolContract.Pool.DataPath()
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("resolve admitted physical-pool DATA_PATH: %w", err)
	}
	if request.Base != nil {
		// Verify that the copied bytes are a readable sealed DuckLake catalog
		// before opening the same file writable for build-local mutations. The
		// read-only attach is explicit and CREATE_IF_NOT_EXISTS is disabled by
		// ducklake.Open, so a missing or malformed artifact cannot be silently
		// replaced with an empty catalog.
		readOnly, readErr := ducklake.Open(ctx, ducklake.Config{
			RootDir:             staging,
			CatalogPath:         catalogPath,
			DataPath:            dataPath,
			PhysicalPoolID:      request.PoolContract.Pool.ID.String(),
			SharedPool:          true,
			Compatibility:       request.PoolContract.Tuple,
			PoolContract:        request.PoolContract,
			ExtensionAdmission:  request.ExtensionAdmission,
			CredentialBootstrap: request.CredentialBootstrap,
			ReadOnly:            true,
		})
		if readErr != nil {
			_ = os.RemoveAll(staging)
			return nil, fmt.Errorf("verify copied sealed catalog read-only: %w", readErr)
		}
		if closeErr := readOnly.Close(); closeErr != nil {
			_ = os.RemoveAll(staging)
			return nil, fmt.Errorf("close read-only sealed catalog verification: %w", closeErr)
		}
		if err := verifyLease(ctx, request); err != nil {
			_ = os.RemoveAll(staging)
			return nil, err
		}
	}
	env, err := ducklake.Open(ctx, ducklake.Config{
		RootDir:             staging,
		CatalogPath:         catalogPath,
		DataPath:            dataPath,
		PhysicalPoolID:      request.PoolContract.Pool.ID.String(),
		SharedPool:          true,
		Compatibility:       request.PoolContract.Tuple,
		PoolContract:        request.PoolContract,
		ExtensionAdmission:  request.ExtensionAdmission,
		CredentialBootstrap: request.CredentialBootstrap,
	})
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("open private candidate catalog: %w", err)
	}
	// Open may create the catalog file for an empty target. Keep the explicit
	// private mode invariant for both empty and copied catalogs.
	if err := os.Chmod(catalogPath, 0o600); err != nil {
		_ = env.Close()
		_ = os.RemoveAll(staging)
		return nil, fmt.Errorf("secure candidate catalog: %w", err)
	}
	if err := verifyLease(ctx, request); err != nil {
		_ = env.Close()
		_ = os.RemoveAll(staging)
		return nil, err
	}
	return &WorkingCatalog{env: env, staging: staging, request: request}, nil
}

// New is an alias for Open.
func New(ctx context.Context, request Request) (*WorkingCatalog, error) {
	return Open(ctx, request)
}

// Build creates a private catalog, applies mutation, and returns the working
// handle only on success. A mutation error closes/removes the private catalog
// and returns no output handle, so callers cannot accidentally treat failed
// work as ready state.
func Build(ctx context.Context, request Request, mutation Mutation) (*WorkingCatalog, error) {
	working, err := Open(ctx, request)
	if err != nil {
		return nil, err
	}
	if mutation == nil {
		return working, nil
	}
	if err := working.Apply(ctx, mutation); err != nil {
		return nil, err
	}
	return working, nil
}

// Construct is a descriptive alias for Build.
func Construct(ctx context.Context, request Request, mutation Mutation) (*WorkingCatalog, error) {
	return Build(ctx, request, mutation)
}

// StagingPath is target-local disposable state and is intended for tests or
// diagnostics only. It is never an artifact identity or a source path.
func (w *WorkingCatalog) StagingPath() string {
	if w == nil {
		return ""
	}
	return w.staging
}

// CatalogPath returns the private metadata database path.
func (w *WorkingCatalog) CatalogPath() string {
	if w == nil || w.env == nil {
		return ""
	}
	return w.env.Path()
}

// Query reads the private catalog after checking the exact writer lease. It
// is intentionally a narrow wrapper rather than exposing the Environment,
// which would let callers bypass this package's durable lease verifier.
func (w *WorkingCatalog) Query(ctx context.Context, plan semanticquery.Plan) (semanticquery.Rows, error) {
	if err := w.checkOpen(); err != nil {
		return nil, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return nil, err
	}
	return w.env.Query(ctx, plan)
}

// CurrentFileSet reads DuckLake's authoritative current data/delete closure
// for one logical table after checking the exact writer lease.
func (w *WorkingCatalog) CurrentFileSet(ctx context.Context, catalogID, schema, table string) (ducklake.CatalogFileSet, error) {
	if err := w.checkOpen(); err != nil {
		return ducklake.CatalogFileSet{}, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return ducklake.CatalogFileSet{}, err
	}
	return w.env.CurrentFileSet(ctx, catalogID, schema, table)
}

// Snapshots exposes build-local snapshot evidence while the handle is open.
func (w *WorkingCatalog) Snapshots(ctx context.Context) ([]ducklake.Snapshot, error) {
	if err := w.checkOpen(); err != nil {
		return nil, err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return nil, err
	}
	return w.env.Snapshots(ctx)
}

// Apply invokes mutation while the exact writer lease remains active. A
// mutation error permanently closes and removes this working handle.
func (w *WorkingCatalog) Apply(ctx context.Context, mutation Mutation) error {
	if mutation == nil {
		return fmt.Errorf("%w: mutation is required", ErrInvalidRequest)
	}
	if err := w.checkOpen(); err != nil {
		return err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		_ = w.Close()
		return err
	}
	if err := mutation(ctx, w); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: %v", ErrMutationFailed, err)
	}
	return nil
}

// Commit executes one private DuckLake transaction using ordinary logical
// names. The transaction is disposable build-local state; no candidate or
// ready record is produced here.
func (w *WorkingCatalog) Commit(ctx context.Context, servingStateID string, extra map[string]string, fn func(*sql.Tx) error) (int64, error) {
	if err := w.checkOpen(); err != nil {
		return 0, err
	}
	if fn == nil {
		return 0, fmt.Errorf("%w: commit function is required", ErrInvalidRequest)
	}
	if err := verifyLease(ctx, w.request); err != nil {
		_ = w.Close()
		return 0, err
	}
	snapshot, err := w.env.Commit(ctx, servingStateID, extra, fn)
	if err != nil {
		_ = w.Close()
		return 0, fmt.Errorf("%w: %v", ErrMutationFailed, err)
	}
	if err := verifyLease(ctx, w.request); err != nil {
		_ = w.Close()
		return 0, err
	}
	return snapshot, nil
}

// Exec executes one private SQL statement after checking the exact lease.
func (w *WorkingCatalog) Exec(ctx context.Context, statement string) error {
	if err := w.checkOpen(); err != nil {
		return err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.env.Exec(ctx, statement); err != nil {
		_ = w.Close()
		return fmt.Errorf("%w: %v", ErrMutationFailed, err)
	}
	if err := verifyLease(ctx, w.request); err != nil {
		_ = w.Close()
		return err
	}
	return nil
}

// WithEnvironment grants a short-lived, lease-checked view of the private
// DuckLake environment to target-owned materialization adapters. The callback
// must not retain the pointer; the handle remains owned by candidatecatalog
// and is still closed by DetachForSeal/Close.
func (w *WorkingCatalog) WithEnvironment(ctx context.Context, fn func(*ducklake.Environment) error) error {
	if fn == nil {
		return fmt.Errorf("%w: environment callback is required", ErrInvalidRequest)
	}
	if err := w.checkOpen(); err != nil {
		return err
	}
	if err := verifyLease(ctx, w.request); err != nil {
		return err
	}
	if err := fn(w.env); err != nil {
		return err
	}
	return verifyLease(ctx, w.request)
}

// DetachForSeal closes the working environment but preserves its private
// catalog directory for the caller's normalization/seal phase. Repeated calls
// are idempotent and return the same detached identity. The returned value is
// not a ready candidate and must be removed if sealing is abandoned.
func (w *WorkingCatalog) DetachForSeal() (DetachedCatalog, error) {
	if w == nil {
		return DetachedCatalog{}, ErrClosed
	}
	w.mu.Lock()
	if w.detached != nil {
		detached := DetachedCatalog{state: w.detached}
		w.mu.Unlock()
		return detached, nil
	}
	w.mu.Unlock()
	if err := verifyLease(context.Background(), w.request); err != nil {
		_ = w.Close()
		return DetachedCatalog{}, err
	}
	w.mu.Lock()
	// Re-check after verification in case another goroutine detached while the
	// durable verifier was running.
	if w.detached != nil {
		detached := DetachedCatalog{state: w.detached}
		w.mu.Unlock()
		return detached, nil
	}
	if w.closed {
		w.mu.Unlock()
		return DetachedCatalog{}, ErrClosed
	}
	w.closed = true
	env := w.env
	state := &detachedState{staging: w.staging, catalogPath: w.CatalogPath()}
	if env != nil {
		if err := env.Close(); err != nil {
			cleanupErr := os.RemoveAll(state.staging)
			state.mu.Lock()
			state.removed = true
			state.mu.Unlock()
			w.mu.Unlock()
			return DetachedCatalog{}, errors.Join(err, cleanupErr)
		}
	}
	// Publish the detached identity only after the metadata database is closed;
	// a concurrent idempotent call must never observe a still-open catalog.
	w.detached = state
	w.mu.Unlock()
	return DetachedCatalog{state: state}, nil
}

// CatalogPath returns the detached private catalog path.
func (d DetachedCatalog) CatalogPath() string {
	if d.state == nil {
		return ""
	}
	return d.state.catalogPath
}

// StagingPath returns the detached private staging path.
func (d DetachedCatalog) StagingPath() string {
	if d.state == nil {
		return ""
	}
	return d.state.staging
}

// Remove abandons the detached catalog and removes only its private
// staging. Shared physical-pool objects remain untouched.
func (d *DetachedCatalog) Remove() error {
	if d == nil {
		return nil
	}
	if d.state == nil {
		return nil
	}
	d.state.mu.Lock()
	if d.state.removed {
		d.state.mu.Unlock()
		return nil
	}
	d.state.removed = true
	staging := d.state.staging
	d.state.mu.Unlock()
	if staging == "" {
		return nil
	}
	return os.RemoveAll(staging)
}

// Close abandons the disposable catalog and removes only its private
// staging. Shared physical-pool objects are intentionally left for the
// global collector and are never deleted here.
func (w *WorkingCatalog) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	var errs []error
	if w.env != nil {
		if err := w.env.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if w.staging != "" {
		if err := os.RemoveAll(w.staging); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *WorkingCatalog) checkOpen() error {
	if w == nil || w.env == nil {
		return ErrClosed
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return nil
}

func validateRequest(ctx context.Context, request Request) error {
	if request.AttemptID == "" {
		return fmt.Errorf("%w: attempt ID is required", ErrInvalidRequest)
	}
	if request.PoolContract == nil {
		return ErrPoolContractRequired
	}
	if request.ExtensionAdmission == nil {
		return ErrExtensionAdmissionRequired
	}
	if err := request.PoolContract.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrPoolContractRequired, err)
	}
	if err := validateLease(request.Lease, request.AttemptID, request.PoolContract.Pool.ID.String(), now(request)); err != nil {
		return err
	}
	if request.VerifyLease == nil {
		return ErrLeaseRequired
	}
	if request.Base != nil {
		if err := validateArtifact(*request.Base, request.PoolContract); err != nil {
			return err
		}
	}
	if request.DataPath != "" {
		if err := request.PoolContract.ValidateDataPathBinding(request.DataPath); err != nil {
			return fmt.Errorf("%w: DATA_PATH does not match admitted pool: %v", ErrBaseMismatch, err)
		}
	}
	_ = ctx // reserved for future durable verifier context checks
	return nil
}

func verifyLease(ctx context.Context, request Request) error {
	if err := validateLease(request.Lease, request.AttemptID, request.PoolContract.Pool.ID.String(), now(request)); err != nil {
		return err
	}
	if err := request.VerifyLease(ctx, request.Lease); err != nil {
		return fmt.Errorf("%w: %v", ErrLeaseMismatch, err)
	}
	return nil
}

func validateLease(lease WriterLease, attemptID, poolID string, current time.Time) error {
	if lease.Status != LeaseActive {
		return fmt.Errorf("%w: lease status is %q", ErrLeaseMismatch, lease.Status)
	}
	if lease.ID == "" || lease.AttemptID == "" || lease.PhysicalPoolID == "" || lease.AttemptID != attemptID || lease.PhysicalPoolID != poolID {
		return ErrLeaseMismatch
	}
	if lease.Epoch < 1 || lease.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: lease epoch or expiry is invalid", ErrLeaseMismatch)
	}
	if !current.Before(lease.ExpiresAt) {
		return ErrLeaseExpired
	}
	return nil
}

func validateArtifact(artifact SealedArtifact, contract *ducklake.PoolContract) error {
	if artifact.Reader == nil {
		return fmt.Errorf("%w: reader is required", ErrInvalidRequest)
	}
	if !validObjectKey(artifact.ObjectKey) {
		return fmt.Errorf("%w: object key is invalid", ErrInvalidRequest)
	}
	if !validDigest(artifact.Digest) {
		return fmt.Errorf("%w: expected digest is invalid", ErrInvalidRequest)
	}
	if artifact.SizeBytes <= 0 {
		return fmt.Errorf("%w: expected size must be positive", ErrInvalidRequest)
	}
	if artifact.PhysicalPoolID != contract.Pool.ID.String() {
		return ErrBaseMismatch
	}
	if !artifact.Compatibility.Equal(contract.Tuple) {
		return ErrBaseMismatch
	}
	return nil
}

func copyArtifact(ctx context.Context, request Request, destination string) error {
	artifact := *request.Base
	reader, err := artifact.Reader.Open(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArtifactSource, err)
	}
	defer reader.Close()
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create private candidate catalog: %w", err)
	}
	closeFile := func(closeErr error) error {
		if err := file.Close(); closeErr == nil {
			closeErr = err
		}
		return closeErr
	}
	hasher := sha256.New()
	counting := &leaseReader{ctx: ctx, reader: reader, request: request, hasher: hasher}
	count, copyErr := io.Copy(file, counting)
	if copyErr == nil && count != artifact.SizeBytes {
		copyErr = fmt.Errorf("%w: expected %d bytes, copied %d", ErrArtifactSize, artifact.SizeBytes, count)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if copyErr == nil && digest != artifact.Digest {
		copyErr = fmt.Errorf("%w: expected %s, copied %s", ErrArtifactDigest, artifact.Digest, digest)
	}
	if syncErr := file.Sync(); copyErr == nil && syncErr != nil {
		copyErr = fmt.Errorf("sync private catalog: %w", syncErr)
	}
	if closeErr := closeFile(copyErr); closeErr != nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return copyErr
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("secure private catalog: %w", err)
	}
	if err := verifyFile(destination, artifact); err != nil {
		return err
	}
	return nil
}

func verifyFile(path string, artifact SealedArtifact) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("verify private catalog: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return fmt.Errorf("verify private catalog bytes: %w", err)
	}
	if size != artifact.SizeBytes {
		return fmt.Errorf("%w: destination is %d bytes, expected %d", ErrArtifactSize, size, artifact.SizeBytes)
	}
	digest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if digest != artifact.Digest {
		return fmt.Errorf("%w: destination is %s, expected %s", ErrArtifactDigest, digest, artifact.Digest)
	}
	return nil
}

type leaseReader struct {
	ctx     context.Context
	reader  io.Reader
	request Request
	hasher  io.Writer
}

func (r *leaseReader) Read(p []byte) (int, error) {
	if err := verifyLease(r.ctx, r.request); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		_, _ = r.hasher.Write(p[:n])
	}
	return n, err
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validObjectKey(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.HasPrefix(value, "/") || strings.ContainsAny(value, `\\?#`) || strings.Contains(value, "://") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.Contains(clean, "/../")
}

func safePrefix(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "build"
	}
	return b.String()
}

func now(request Request) time.Time {
	if request.Now != nil {
		return request.Now().UTC()
	}
	return time.Now().UTC()
}
