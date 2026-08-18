// Package catalogseal implements the immutable catalog seal boundary.
//
// A detached catalog is private, disposable state until this package records
// its exact byte identity.  The seal repository is deliberately an interface:
// the control-plane adapter can use SQLite (or another durable store) without
// making this package depend on deployment records.  Object storage is also an
// interface and is create-only; an existing object is never overwritten.
package catalogseal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

var (
	ErrInvalidRequest       = errors.New("catalog seal request is invalid")
	ErrLocalCatalog         = errors.New("detached local catalog is unavailable")
	ErrLocalCatalogDigest   = errors.New("detached local catalog changed or has invalid bytes")
	ErrSealRepository       = errors.New("catalog seal repository operation failed")
	ErrObjectUpload         = errors.New("catalog object upload failed")
	ErrObjectExists         = errors.New("catalog object already exists")
	ErrObjectAmbiguous      = errors.New("catalog object create acknowledgement is ambiguous")
	ErrObjectCorrupt        = errors.New("pre-existing catalog object does not match the seal")
	ErrRemoteVerification   = errors.New("remote catalog verification failed")
	ErrIdentityConflict     = errors.New("catalog seal identity conflicts with durable state")
	ErrRepositoryTransition = errors.New("catalog seal repository transition failed")
	ErrSealNotFound         = errors.New("catalog seal is not found")
)

const (
	// MetadataDigest and MetadataSize are required on every immutable catalog
	// object.  They are intentionally namespaced so adapters can map them to
	// provider metadata without exposing credentials.
	MetadataDigest = "leapview-catalog-digest"
	MetadataSize   = "leapview-catalog-size"

	// CatalogObjectPrefix is the canonical namespace for content-addressed
	// catalog artifacts.
	CatalogObjectPrefix = "catalogs/sha256/"
	CatalogObjectSuffix = ".ducklake"
)

// SealStatus is the small cross-store state machine.  A candidate is not
// queryable until Verified, and Ready is only produced by CompleteVerified.
type SealStatus string

const (
	SealPreparing SealStatus = "preparing"
	SealUploaded  SealStatus = "uploaded"
	SealVerified  SealStatus = "verified"
)

// AttemptIdentity binds one private build attempt and its writer lease.
// Values are opaque control-plane identities; this package does not depend on
// deployment's richer records.
type AttemptIdentity struct {
	ID            string
	WriterLeaseID string
}

// PlanIdentity binds the canonical plan and execution inputs which produced
// the catalog bytes.
type PlanIdentity struct {
	ID              string
	Digest          string
	ExecutionDigest string
}

// PoolIdentity identifies the physical pool and its admitted compatibility
// contract.  CompatibilityDigest is intentionally accepted as an opaque
// digest: the physical-pool package remains the authority for tuple meaning.
type PoolIdentity struct {
	ID                  string
	CompatibilityDigest string
}

// QualificationIdentity and ClosureIdentity are exact evidence digests.  The
// qualifier's concrete record is deliberately not imported here.
type QualificationIdentity struct{ Digest string }
type ClosureIdentity struct{ Digest string }

// CandidateIdentity is the candidate that becomes ready atomically with the
// verified seal.  Additional candidate fields remain owned by deployment.
type CandidateIdentity struct {
	ID                    string
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
}

// SealIdentity is the complete immutable identity persisted before upload.
// CatalogDigest/ObjectKey/ObjectSize are filled by Seal from the detached
// catalog bytes and must not be supplied by callers.
type SealIdentity struct {
	SealID        string
	Attempt       AttemptIdentity
	Plan          PlanIdentity
	Pool          PoolIdentity
	Qualification QualificationIdentity
	Closure       ClosureIdentity
	Candidate     CandidateIdentity
	CatalogDigest string
	ObjectKey     string
	ObjectSize    int64
}

// SealRecord is returned by repository transitions.  Repository adapters may
// retain richer fields, but these are sufficient for retry convergence.
type SealRecord struct {
	Identity SealIdentity
	Status   SealStatus
}

// CompleteInput is the only operation allowed to expose a ready candidate.
// The repository must commit the verified seal, ready candidate, and writer
// lease release in one durable transaction.
type CompleteInput struct {
	Seal                SealIdentity
	SealID              string
	CandidateID         string
	ClosureDigest       string
	QualificationDigest string
	// ResolvedInputsJSON/Digest carry opaque deployment-owned build evidence
	// through the generic seal boundary without importing deployment types.
	// The durable adapter validates and persists the bytes atomically with the
	// ready candidate.
	ResolvedInputsJSON   string
	ResolvedInputsDigest string
}

// Completion is the result of the atomic durable completion.  CandidateID is
// repeated to make idempotent retries easy for callers to inspect.
type Completion struct {
	Seal          SealRecord
	CandidateID   string
	LeaseReleased bool
}

// SealRepository is the narrow durable control-plane contract. Prepare must
// durably persist preparing identity before any upload. MarkUploaded and
// CompleteVerified are idempotent for identical identity/evidence. The latter
// must atomically mark the seal verified, create/ready the candidate, and
// release the writer lease.
type SealRepository interface {
	// Lookup is used before local work so a retry can recover after the
	// detached staging has been lost. ErrSealNotFound means no preparing
	// identity exists yet; every other error is a repository failure.
	Lookup(context.Context, string) (SealRecord, error)
	Prepare(context.Context, SealIdentity) (SealRecord, error)
	MarkUploaded(context.Context, string) (SealRecord, error)
	CompleteVerified(context.Context, CompleteInput) (Completion, error)
}

// ObjectMetadata is provider-neutral object metadata. Keys and values are
// non-secret; credentials never enter this package.
type ObjectMetadata map[string]string

// Object is an immutable object opened by exact key. Metadata is returned by
// the provider and is verified alongside the bytes and size.
type Object struct {
	Body     io.ReadCloser
	Size     int64
	Metadata ObjectMetadata
}

// ObjectStore provides create-only upload and exact-key reads. Create must
// never overwrite an existing key. Implementations should return
// ErrObjectExists for a known pre-existing key and ErrObjectAmbiguous when the
// acknowledgement is unknown; Seal reconciles either by opening the exact key.
type ObjectStore interface {
	Create(context.Context, string, io.Reader, ObjectMetadata) error
	Open(context.Context, string) (Object, error)
}

// RemoteVerifier attaches the exact uploaded object read-only and verifies its
// DuckLake state. A verifier must check one retained/current snapshot and the
// exact closure represented by ClosureDigest. It receives an open capability,
// rather than credentials or a filesystem path.
type RemoteVerifier interface {
	Verify(context.Context, RemoteVerification) error
}

// RemoteVerification is the verifier's immutable evidence input.
type RemoteVerification struct {
	Identity SealIdentity
	Open     func(context.Context) (Object, error)
}

// DetachedCatalog opens a closed local catalog from byte zero. Open must
// return a fresh reader on every call so hashing and upload cannot substitute
// remote bytes or stale readers.
type DetachedCatalog interface {
	Open(context.Context) (io.ReadCloser, error)
}

// FileCatalog adapts a closed local catalog file. The file is opened read-only
// and is never removed by this package.
type FileCatalog struct{ Path string }

func (f FileCatalog) Open(_ context.Context) (io.ReadCloser, error) {
	if strings.TrimSpace(f.Path) == "" {
		return nil, ErrLocalCatalog
	}
	r, err := os.Open(f.Path)
	if err != nil {
		return nil, ErrLocalCatalog
	}
	return r, nil
}

// Request describes one sealing attempt. All fields except Store,
// Repository, Verifier, and Catalog are exact control-plane inputs. SealID is
// caller-owned and must be stable across retries.
type Request struct {
	SealID               string
	Attempt              AttemptIdentity
	Plan                 PlanIdentity
	Pool                 PoolIdentity
	Qualification        QualificationIdentity
	Closure              ClosureIdentity
	Candidate            CandidateIdentity
	Catalog              DetachedCatalog
	Store                ObjectStore
	Repository           SealRepository
	Verifier             RemoteVerifier
	ResolvedInputsJSON   string
	ResolvedInputsDigest string
}

// Seal hashes the detached bytes, records preparing identity before upload,
// performs a create-only upload with exact-key reconciliation, verifies the
// remote read-only state, and atomically completes the durable seal/candidate/
// lease transition. Repeating identical input converges without overwrites.
func Seal(ctx context.Context, request Request) (Completion, error) {
	if err := request.validate(); err != nil {
		return Completion{}, err
	}
	record, lookupErr := request.Repository.Lookup(ctx, request.SealID)
	if lookupErr == nil {
		if err := validatePersistedRecord(record, request); err != nil {
			return Completion{}, err
		}
		return continuePersisted(ctx, request, record)
	}
	if !errors.Is(lookupErr, ErrSealNotFound) {
		return Completion{}, fmt.Errorf("%w: lookup", ErrSealRepository)
	}
	// No durable preparing identity exists. A local detached catalog is
	// mandatory here; remote bytes can never establish a new identity.
	if request.Catalog == nil {
		return Completion{}, ErrLocalCatalog
	}
	identity, err := localIdentity(ctx, request)
	if err != nil {
		return Completion{}, err
	}
	record, err = request.Repository.Prepare(ctx, identity)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: prepare", ErrSealRepository)
	}
	if err := validateRecord(record, identity); err != nil {
		return Completion{}, err
	}
	return continuePersisted(ctx, request, record)
}

func continuePersisted(ctx context.Context, request Request, record SealRecord) (Completion, error) {
	identity := record.Identity
	switch record.Status {
	case SealPreparing:
		if request.Catalog != nil {
			if err := localMatches(ctx, request.Catalog, identity); err != nil {
				return Completion{}, err
			}
			if err := uploadAndReconcile(ctx, request.Catalog, request.Store, identity); err != nil {
				return Completion{}, err
			}
		} else {
			// A preparing record may have reached object storage before the
			// acknowledgement was lost. Recover only from the exact key after
			// verifying its bytes and required metadata. If no object exists,
			// local loss happened before upload and no substitution is allowed.
			if err := verifyStoredObject(ctx, request.Store, identity); err != nil {
				if errors.Is(err, ErrObjectUpload) {
					return Completion{}, ErrLocalCatalog
				}
				return Completion{}, err
			}
		}
		var err error
		record, err = request.Repository.MarkUploaded(ctx, identity.SealID)
		if err != nil {
			return Completion{}, fmt.Errorf("%w: mark uploaded", ErrSealRepository)
		}
		if err := validateRecord(record, identity); err != nil {
			return Completion{}, err
		}
		if record.Status != SealUploaded && record.Status != SealVerified {
			return Completion{}, fmt.Errorf("%w: upload state did not advance", ErrRepositoryTransition)
		}
		if record.Status == SealUploaded {
			if err := verifyStoredObject(ctx, request.Store, identity); err != nil {
				return Completion{}, err
			}
			if err := verifyRemote(ctx, request, identity); err != nil {
				return Completion{}, err
			}
		}
	case SealUploaded:
		if err := verifyStoredObject(ctx, request.Store, identity); err != nil {
			return Completion{}, err
		}
		if err := verifyRemote(ctx, request, identity); err != nil {
			return Completion{}, err
		}
	case SealVerified:
		// The prior verifier and durable transaction may have completed in
		// either order. Re-read exact bytes before completing an idempotent
		// retry, but do not require the lost local staging or re-run policy.
		if err := verifyStoredObject(ctx, request.Store, identity); err != nil {
			return Completion{}, err
		}
	default:
		return Completion{}, fmt.Errorf("%w: unexpected seal state", ErrRepositoryTransition)
	}
	return complete(ctx, request.Repository, identity, request.ResolvedInputsJSON, request.ResolvedInputsDigest)
}

func verifyRemote(ctx context.Context, request Request, identity SealIdentity) error {
	if request.Verifier == nil {
		return fmt.Errorf("%w: verifier is required", ErrInvalidRequest)
	}
	open := func(openCtx context.Context) (Object, error) {
		object, openErr := request.Store.Open(openCtx, identity.ObjectKey)
		if openErr != nil {
			return Object{}, ErrObjectUpload
		}
		return object, nil
	}
	if err := request.Verifier.Verify(ctx, RemoteVerification{Identity: identity, Open: open}); err != nil {
		return fmt.Errorf("%w: read-only remote state", ErrRemoteVerification)
	}
	return nil
}

func complete(ctx context.Context, repository SealRepository, identity SealIdentity, resolvedInputsJSON, resolvedInputsDigest string) (Completion, error) {
	completion, err := repository.CompleteVerified(ctx, CompleteInput{
		Seal: identity, SealID: identity.SealID, CandidateID: identity.Candidate.ID,
		ClosureDigest: identity.Closure.Digest, QualificationDigest: identity.Qualification.Digest,
		ResolvedInputsJSON: resolvedInputsJSON, ResolvedInputsDigest: resolvedInputsDigest,
	})
	if err != nil {
		return Completion{}, fmt.Errorf("%w: complete", ErrSealRepository)
	}
	if completion.CandidateID != identity.Candidate.ID {
		return Completion{}, ErrIdentityConflict
	}
	return completion, nil
}

// Complete is a descriptive alias for Seal.
func Complete(ctx context.Context, request Request) (Completion, error) {
	return Seal(ctx, request)
}

// CanonicalObjectKey derives the immutable key from a sha256 digest.
func CanonicalObjectKey(digest string) string {
	if !validDigest(digest) {
		return ""
	}
	return CatalogObjectPrefix + strings.TrimPrefix(digest, "sha256:") + CatalogObjectSuffix
}

func (r Request) validate() error {
	if r.Store == nil || r.Repository == nil {
		return fmt.Errorf("%w: store and repository are required", ErrInvalidRequest)
	}
	for name, value := range map[string]string{
		"seal": r.SealID, "attempt": r.Attempt.ID, "writer lease": r.Attempt.WriterLeaseID,
		"plan": r.Plan.ID, "pool": r.Pool.ID, "candidate": r.Candidate.ID,
		"serving artifact": r.Candidate.ServingArtifactID,
		"serving state":    r.Candidate.ServingStateID,
	} {
		if !validIdentity(value) {
			return fmt.Errorf("%w: %s identity", ErrInvalidRequest, name)
		}
	}
	for name, value := range map[string]string{
		"plan": r.Plan.Digest, "execution": r.Plan.ExecutionDigest,
		"compatibility": r.Pool.CompatibilityDigest, "qualification": r.Qualification.Digest,
		"closure": r.Closure.Digest, "serving artifact": r.Candidate.ServingArtifactDigest,
	} {
		if !validDigest(value) {
			return fmt.Errorf("%w: %s digest", ErrInvalidRequest, name)
		}
	}
	return nil
}

func validIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && (char < '0' || char > '9') && !strings.ContainsRune("._:/-", char) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validateRecord(record SealRecord, expected SealIdentity) error {
	if !sameIdentity(record.Identity, expected) {
		return ErrIdentityConflict
	}
	if !validDigest(record.Identity.CatalogDigest) || record.Identity.ObjectSize <= 0 ||
		record.Identity.ObjectKey != CanonicalObjectKey(record.Identity.CatalogDigest) || !validObjectKey(record.Identity.ObjectKey) {
		return ErrIdentityConflict
	}
	if record.Status != SealPreparing && record.Status != SealUploaded && record.Status != SealVerified {
		return fmt.Errorf("%w: unsupported persisted state", ErrRepositoryTransition)
	}
	return nil
}

func validatePersistedRecord(record SealRecord, request Request) error {
	if record.Identity.SealID != request.SealID ||
		record.Identity.Attempt != request.Attempt ||
		record.Identity.Plan != request.Plan ||
		record.Identity.Pool != request.Pool ||
		record.Identity.Qualification != request.Qualification ||
		record.Identity.Closure != request.Closure ||
		record.Identity.Candidate != request.Candidate {
		return ErrIdentityConflict
	}
	return validateRecord(record, record.Identity)
}

func localIdentity(ctx context.Context, request Request) (SealIdentity, error) {
	digest, size, err := hashCatalog(ctx, request.Catalog)
	if err != nil {
		return SealIdentity{}, err
	}
	identity := SealIdentity{
		SealID: request.SealID, Attempt: request.Attempt, Plan: request.Plan,
		Pool: request.Pool, Qualification: request.Qualification, Closure: request.Closure,
		Candidate: request.Candidate, CatalogDigest: digest,
		ObjectKey: CanonicalObjectKey(digest), ObjectSize: size,
	}
	if identity.ObjectSize <= 0 {
		return SealIdentity{}, ErrLocalCatalogDigest
	}
	return identity, nil
}

func localMatches(ctx context.Context, catalog DetachedCatalog, expected SealIdentity) error {
	digest, size, err := hashCatalog(ctx, catalog)
	if err != nil {
		return err
	}
	if digest != expected.CatalogDigest || size != expected.ObjectSize {
		return ErrLocalCatalogDigest
	}
	return nil
}

func sameIdentity(a, b SealIdentity) bool {
	return a.SealID == b.SealID && a.Attempt == b.Attempt && a.Plan == b.Plan &&
		a.Pool == b.Pool && a.Qualification == b.Qualification && a.Closure == b.Closure &&
		a.Candidate == b.Candidate && a.CatalogDigest == b.CatalogDigest &&
		a.ObjectKey == b.ObjectKey && a.ObjectSize == b.ObjectSize
}

func hashCatalog(ctx context.Context, catalog DetachedCatalog) (string, int64, error) {
	r, err := catalog.Open(ctx)
	if err != nil || r == nil {
		return "", 0, ErrLocalCatalog
	}
	defer r.Close()
	h := sha256.New()
	n, copyErr := io.Copy(h, contextReader{ctx: ctx, reader: r})
	if copyErr != nil {
		return "", 0, ErrLocalCatalogDigest
	}
	if n <= 0 {
		return "", 0, ErrLocalCatalogDigest
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

func uploadAndReconcile(ctx context.Context, catalog DetachedCatalog, store ObjectStore, identity SealIdentity) error {
	r, err := catalog.Open(ctx)
	if err != nil || r == nil {
		return ErrLocalCatalog
	}
	defer r.Close()
	h := sha256.New()
	reader := &digestReader{ctx: ctx, reader: r, hash: h}
	metadata := ObjectMetadata{MetadataDigest: identity.CatalogDigest, MetadataSize: strconv.FormatInt(identity.ObjectSize, 10)}
	createErr := store.Create(ctx, identity.ObjectKey, reader, metadata)
	// Providers commonly reject an existing key before consuming the body. Drain
	// the local reader after every acknowledgement so a changed local source is
	// never silently substituted by a matching remote object.
	_, drainErr := io.Copy(io.Discard, reader)
	if drainErr != nil || reader.n != identity.ObjectSize || digestFor(h) != identity.CatalogDigest {
		return ErrLocalCatalogDigest
	}
	if createErr == nil {
		object, openErr := store.Open(ctx, identity.ObjectKey)
		if openErr != nil {
			return ErrObjectUpload
		}
		if !verifyObject(object, identity) {
			return ErrObjectCorrupt
		}
		return nil
	}
	// A known existing key or an ambiguous acknowledgement is reconciled by
	// opening the exact key. This is safe for all create errors and prevents a
	// provider-specific error classification from weakening immutability.
	object, openErr := store.Open(ctx, identity.ObjectKey)
	if openErr != nil {
		return ErrObjectUpload
	}
	if !verifyObject(object, identity) {
		return ErrObjectCorrupt
	}
	return nil
}

func verifyObject(object Object, identity SealIdentity) bool {
	if object.Body == nil || object.Size != identity.ObjectSize || object.Metadata == nil ||
		object.Metadata[MetadataDigest] != identity.CatalogDigest || object.Metadata[MetadataSize] != strconv.FormatInt(identity.ObjectSize, 10) {
		if object.Body != nil {
			_ = object.Body.Close()
		}
		return false
	}
	h := sha256.New()
	n, err := io.Copy(h, object.Body)
	closeErr := object.Body.Close()
	return err == nil && closeErr == nil && n == identity.ObjectSize && digestFor(h) == identity.CatalogDigest
}

func verifyStoredObject(ctx context.Context, store ObjectStore, identity SealIdentity) error {
	object, err := store.Open(ctx, identity.ObjectKey)
	if err != nil {
		return ErrObjectUpload
	}
	if !verifyObject(object, identity) {
		return ErrObjectCorrupt
	}
	return nil
}

func digestFor(h io.Writer) string {
	// hash.Hash is intentionally not required by the public interfaces; this
	// helper is called only with crypto/sha256.Hash values.
	if value, ok := h.(interface{ Sum([]byte) []byte }); ok {
		return "sha256:" + hex.EncodeToString(value.Sum(nil))
	}
	return ""
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

type digestReader struct {
	ctx    context.Context
	reader io.Reader
	hash   interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (r *digestReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if n > 0 {
		r.n += int64(n)
		_, _ = r.hash.Write(p[:n])
	}
	return n, err
}

// Compile-time checks for common accidental key/path mistakes live here so
// adapters can use the same canonical key routine.
func validObjectKey(key string) bool {
	if key == "" || strings.TrimSpace(key) != key || strings.ContainsAny(key, `\\?#`) || strings.Contains(key, "://") {
		return false
	}
	clean := path.Clean(key)
	return clean == key && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
