package projectsource

// This file contains the native implementation of the deployment-facing
// CandidateSourceSynchronizer contract.  It deliberately does not depend on
// a filesystem workspace: source bytes are staged from immutable object-store
// references and the compiler receives an in-memory source set.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/platform/objectstore"
	"github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectpostgres "github.com/flidai/leapview/internal/project/postgres"
	"github.com/google/uuid"
)

const nativePlanLifetime = 5 * time.Minute

// NativeCandidateSourceConfig is the complete authority set required by the
// native synchronizer.  Begin owns only transaction acquisition; repository
// methods never acquire, commit, or roll back a transaction themselves.
type NativeCandidateSourceConfig struct {
	Begin                 BeginFunc
	Sources               NativeSourceRepository
	Objects               objectstore.ImmutableStore
	Compiler              CompilerPort
	StorageSecurityDomain string
	Now                   func() time.Time
	PlanLifetime          time.Duration
}

// NativeCandidateSourceSynchronizerConfig is retained as an expressive alias
// for callers that prefer the longer configuration name.
type NativeCandidateSourceSynchronizerConfig = NativeCandidateSourceConfig

// NativeCandidateSourceSynchronizer is a phased PostgreSQL/object-store
// synchronizer.  No compiler or object-store operation runs while a database
// transaction is active.
type NativeCandidateSourceSynchronizer struct {
	begin         BeginFunc
	sources       NativeSourceRepository
	objects       objectstore.ImmutableStore
	compiler      CompilerPort
	now           func() time.Time
	planLifetime  time.Duration
	storageDomain string
}

var _ project.CandidateSourceSynchronizer = (*NativeCandidateSourceSynchronizer)(nil)
var _ project.CandidateSourceObjectReader = (*NativeCandidateSourceSynchronizer)(nil)
var _ NativeSourceRepository = (*projectpostgres.Repository)(nil)

// NewNativeCandidateSourceSynchronizer constructs the native adapter without
// performing any I/O.  A source repository must implement the read methods
// used by the object-backed compiler and snapshot readers; those capabilities
// are checked when the corresponding phase is invoked.
func NewNativeCandidateSourceSynchronizer(config NativeCandidateSourceConfig) (*NativeCandidateSourceSynchronizer, error) {
	if config.Begin == nil || config.Sources == nil || config.Objects == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	if config.Compiler == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	lifetime := config.PlanLifetime
	if lifetime == 0 {
		lifetime = nativePlanLifetime
	}
	if lifetime <= 0 || lifetime > 15*time.Minute {
		return nil, project.ErrCandidateSourceInvalid
	}
	domain := config.StorageSecurityDomain
	if domain == "" || domain != strings.TrimSpace(domain) || !validNativeText(domain) || len(domain) > maxProjectIDBytes {
		return nil, project.ErrCandidateSourceInvalid
	}
	return &NativeCandidateSourceSynchronizer{
		begin: config.Begin, sources: config.Sources, objects: config.Objects,
		compiler: config.Compiler, now: now, planLifetime: lifetime, storageDomain: domain,
	}, nil
}

// NewNativeCandidateSourceSynchronizerWithPorts is a convenience for callers
// that already have the four narrow ports and do not need a config struct.
func NewNativeCandidateSourceSynchronizerWithPorts(begin BeginFunc, sources NativeSourceRepository, objects objectstore.ImmutableStore, compiler CompilerPort, storageSecurityDomain string) (*NativeCandidateSourceSynchronizer, error) {
	return NewNativeCandidateSourceSynchronizer(NativeCandidateSourceConfig{Begin: begin, Sources: sources, Objects: objects, Compiler: compiler, StorageSecurityDomain: storageSecurityDomain})
}

func (s *NativeCandidateSourceSynchronizer) Plan(ctx context.Context, scope project.CandidateSourceScope, request project.CandidateSynchronizationRequest) (project.CandidateSynchronizationPlan, error) {
	if s == nil || s.begin == nil || s.sources == nil || s.objects == nil {
		return project.CandidateSynchronizationPlan{}, project.ErrCandidateSourceUnavailable
	}
	normalized, entries, requestDigest, err := normalizeNativeRequest(scope, request)
	if err != nil {
		return project.CandidateSynchronizationPlan{}, err
	}
	if normalized.IdempotencyKey == "" {
		return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: synchronization plan requires an idempotency key", project.ErrCandidateSourceInvalid)
	}
	planID := deterministicUUID("source-plan", scope.ProjectID.String(), s.storageDomain, scope.OwnerID, normalized.CandidateKey, normalized.IdempotencyKey)
	operationID := deterministicUUID("source-operation", planID.String())
	now := s.now().UTC()
	input := projectpostgres.SyncPlanInput{
		PlanID: planID, OperationID: operationID,
		ProjectID: scope.ProjectID.String(), StorageSecurityDomain: s.storageDomain,
		OwnerID: scope.OwnerID, CandidateKey: normalized.CandidateKey,
		SourceDigest: normalized.ArtifactDigest, ProjectFile: normalized.ProjectFile,
		RequestDigest: requestDigest, ExpiresAt: now.Add(s.planLifetime),
		Entries: entries,
	}
	tx, err := s.beginTx(ctx)
	if err != nil {
		return project.CandidateSynchronizationPlan{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	// Lock/read first so exact replay preserves the originally persisted expiry;
	// a fresh request is inserted only after the absence is authoritative.
	plan, err := s.sources.SyncPlanForUpdateTx(ctx, tx, planID)
	if errors.Is(err, projectpostgres.ErrSourceNotFound) || errors.Is(err, projectpostgres.ErrNotFound) {
		plan, err = s.sources.CreateSyncPlanTx(ctx, tx, input)
		if err == nil {
			plan, err = s.sources.SyncPlanForUpdateTx(ctx, tx, planID)
		}
	}
	if err != nil {
		return project.CandidateSynchronizationPlan{}, mapNativeError(err)
	}
	if !samePlanRequest(plan, input) {
		return project.CandidateSynchronizationPlan{}, fmt.Errorf("%w: synchronization request drift", project.ErrCandidateSourceConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return project.CandidateSynchronizationPlan{}, err
	}
	committed = true
	if plan.State == "committed" {
		return project.CandidateSynchronizationPlan{PlanID: plan.PlanID.String(), ArtifactDigest: plan.SourceDigest}, nil
	}
	missing, err := s.missing(ctx, plan)
	if err != nil {
		return project.CandidateSynchronizationPlan{}, err
	}
	return project.CandidateSynchronizationPlan{PlanID: plan.PlanID.String(), ArtifactDigest: plan.SourceDigest, MissingDigests: missing}, nil
}

func (s *NativeCandidateSourceSynchronizer) Upload(ctx context.Context, scope project.CandidateSourceScope, planID, identity string, source io.Reader) error {
	if s == nil || s.begin == nil || s.sources == nil || s.objects == nil {
		return project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return err
	}
	id, err := parsePlanUUID(planID)
	if err != nil {
		return fmt.Errorf("%w: plan id: %v", project.ErrCandidateSourceConflict, err)
	}
	identity = strings.TrimSpace(identity)
	if !validNativeDigest(identity) || source == nil {
		return project.ErrCandidateSourceInvalid
	}
	// Phase 1 validates ownership, expiry, and solicitation in a short read
	// transaction.  The body is intentionally not consumed while it is held.
	tx, err := s.beginTx(ctx)
	if err != nil {
		return err
	}
	plan, err := s.sources.SyncPlanForUpdateTx(ctx, tx, id)
	if err == nil {
		err = validatePlanOwnerAndExpiry(plan, scope, s.storageDomain, false)
	}
	if err == nil && plan.State == "committed" {
		err = fmt.Errorf("%w: synchronization plan is already committed", project.ErrCandidateSourceConflict)
	}
	var expectedSize int64
	if err == nil {
		found := false
		for _, entry := range plan.Entries {
			if entry.Digest == identity {
				found = true
				expectedSize = entry.SizeBytes
				break
			}
		}
		if !found {
			err = fmt.Errorf("%w: source blob was not requested by this plan", project.ErrCandidateSourceConflict)
		}
		if err == nil {
			// This call is authoritative for the database clock and plan expiry;
			// an already-admitted digest is still a valid exact upload replay.
			_, err = s.sources.ListMissingPlanSourceBlobDigestsTx(ctx, tx, id, scope.OwnerID)
		}
	}
	_ = tx.Rollback(context.Background())
	if err != nil {
		return mapNativeError(err)
	}
	content, err := io.ReadAll(io.LimitReader(source, maxBlobBytes+1))
	if err != nil {
		return project.ErrCandidateSourceInvalid
	}
	if int64(len(content)) != expectedSize || sha256Identity(content) != identity {
		return fmt.Errorf("%w: source blob size or digest does not match synchronization plan", project.ErrCandidateSourceConflict)
	}
	key := sourceObjectKey(identity)
	metadata := objectstore.ObjectMetadata{StorageSecurityDomain: s.storageDomain, Digest: identity, SizeBytes: int64(len(content)), ContentType: "application/octet-stream", MetadataDigest: emptyMetadataDigest()}
	if err := s.putImmutable(ctx, key, content, metadata); err != nil {
		return mapNativeError(err)
	}
	// Phase 2 records the already-written immutable reference.  The repository
	// rechecks plan state/owner/expiry, so a concurrent expiry cannot admit it.
	tx, err = s.beginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = s.sources.InsertSourceBlobTx(ctx, tx, projectpostgres.SourceBlobInput{ProjectID: scope.ProjectID.String(), StorageSecurityDomain: s.storageDomain, Digest: identity, SizeBytes: int64(len(content)), ObjectKey: key, ContentType: metadata.ContentType, MetadataDigest: metadata.MetadataDigest, PlanID: id, OwnerID: scope.OwnerID})
	if err != nil {
		return mapNativeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}

func (s *NativeCandidateSourceSynchronizer) Commit(ctx context.Context, scope project.CandidateSourceScope, request project.CandidateSynchronizationRequest) (project.CandidateSourceSnapshot, error) {
	if s == nil || s.begin == nil || s.sources == nil || s.objects == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	planID, err := parsePlanUUID(request.PlanID)
	if err != nil {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: plan id: %v", project.ErrCandidateSourceConflict, err)
	}
	normalized, entries, requestDigest, err := normalizeNativeRequest(scope, request)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	// Read and lock the plan in a short transaction.  Committed plans return
	// their exact snapshot immediately and therefore never recompile on replay.
	tx, err := s.beginTx(ctx)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	plan, err := s.sources.SyncPlanForUpdateTx(ctx, tx, planID)
	if err == nil {
		err = validatePlanOwnerAndExpiry(plan, scope, s.storageDomain, true)
	}
	if err == nil && (plan.RequestDigest != requestDigest || plan.SourceDigest != normalized.ArtifactDigest || !sameEntries(plan.Entries, entries)) {
		err = fmt.Errorf("%w: synchronization request drift", project.ErrCandidateSourceConflict)
	}
	if err == nil && plan.State == "committed" {
		_ = tx.Rollback(context.Background())
		snapshot, snapErr := s.sources.Snapshot(ctx, scope.ProjectID.String(), s.storageDomain, normalized.ArtifactDigest)
		if snapErr != nil {
			return project.CandidateSourceSnapshot{}, mapNativeError(snapErr)
		}
		if validateErr := validateNativeSnapshot(snapshot, scope, normalized.ArtifactDigest, s.storageDomain); validateErr != nil {
			return project.CandidateSourceSnapshot{}, validateErr
		}
		return s.exactSnapshotResult(ctx, scope, snapshot, request.SourceRevision)
	}
	if err == nil {
		missing, missingErr := s.sources.ListMissingPlanSourceBlobDigestsTx(ctx, tx, planID, scope.OwnerID)
		if missingErr != nil {
			err = missingErr
		} else if len(missing) != 0 {
			err = fmt.Errorf("%w: source blobs remain missing", project.ErrCandidateSourceConflict)
		}
	}
	_ = tx.Rollback(context.Background())
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}

	refs, err := s.planObjectRefs(ctx, plan)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	files, err := s.stageSourceSet(ctx, plan, refs)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	compiled, err := s.compileSourceSet(ctx, CompileInput{ProjectID: scope.ProjectID.String(), StorageSecurityDomain: s.storageDomain, ProjectFile: plan.ProjectFile, SourceDigest: plan.SourceDigest, Files: files})
	if err != nil {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: compile project: %v", project.ErrCandidateSourceConflict, err)
	}
	compiled, err = normalizeNativeCompileOutput(compiled, scope.ProjectID.String(), plan.StorageSecurityDomain, plan.SourceDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	artifactMeta := objectstore.ObjectMetadata{StorageSecurityDomain: s.storageDomain, Digest: sha256Identity(compiled.ProjectArtifact), SizeBytes: int64(len(compiled.ProjectArtifact)), ContentType: "application/octet-stream", MetadataDigest: compiled.ProjectArtifactMetadataDigest}
	manifestMeta := objectstore.ObjectMetadata{StorageSecurityDomain: s.storageDomain, Digest: sha256Identity(compiled.Manifest), SizeBytes: int64(len(compiled.Manifest)), ContentType: "application/json", MetadataDigest: compiled.ManifestMetadataDigest}
	if _, err := s.putImmutableInfo(ctx, compiled.ProjectArtifactObjectKey, compiled.ProjectArtifact, artifactMeta); err != nil {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: project artifact: %v", project.ErrCandidateSourceConflict, err)
	}
	if _, err := s.putImmutableInfo(ctx, compiled.ManifestObjectKey, compiled.Manifest, manifestMeta); err != nil {
		return project.CandidateSourceSnapshot{}, fmt.Errorf("%w: source manifest: %v", project.ErrCandidateSourceConflict, err)
	}
	snapshotID := deterministicUUID("source-snapshot", plan.ProjectID, plan.StorageSecurityDomain, plan.SourceDigest)
	attestation := nativeAttestation(snapshotID, plan.SourceDigest, request.SourceRevision)
	commitInput := projectpostgres.CommitSnapshotInput{PlanID: plan.PlanID, OwnerID: plan.OwnerID, SnapshotID: snapshotID, ProjectID: plan.ProjectID, StorageSecurityDomain: plan.StorageSecurityDomain, SourceDigest: plan.SourceDigest, ProjectFile: plan.ProjectFile, ProjectDigest: compiled.ProjectDigest, ProjectArtifactObjectKey: compiled.ProjectArtifactObjectKey, ProjectArtifactDigest: artifactMeta.Digest, ProjectArtifactSizeBytes: artifactMeta.SizeBytes, ManifestObjectKey: compiled.ManifestObjectKey, ManifestObjectDigest: manifestMeta.Digest, ManifestObjectSizeBytes: manifestMeta.SizeBytes, CompilerVersion: compiled.CompilerVersion, SchemaVersion: compiled.SchemaVersion, Entries: nativePlanEntries(plan), Attestation: attestation}
	tx, err = s.beginTx(ctx)
	if err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot, err := s.sources.CommitSnapshotTx(ctx, tx, commitInput)
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	return s.exactSnapshotResult(ctx, scope, snapshot, request.SourceRevision)
}

// Snapshot is the provenance-neutral read path.  Revision evidence is only
// returned by SnapshotAttestation.
func (s *NativeCandidateSourceSynchronizer) Snapshot(ctx context.Context, scope project.CandidateSourceScope, artifactDigest string) (project.CandidateSourceSnapshot, error) {
	if s == nil || s.sources == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	artifactDigest = strings.TrimSpace(artifactDigest)
	if !validNativeDigest(artifactDigest) {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceInvalid
	}
	snapshot, err := s.sources.Snapshot(ctx, scope.ProjectID.String(), s.storageDomain, artifactDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}
	if validateErr := validateNativeSnapshot(snapshot, scope, artifactDigest, s.storageDomain); validateErr != nil {
		return project.CandidateSourceSnapshot{}, validateErr
	}
	return sourceSnapshotResult(snapshot), nil
}

// SnapshotAttestation resolves one exact append-only provenance record.
func (s *NativeCandidateSourceSynchronizer) SnapshotAttestation(ctx context.Context, scope project.CandidateSourceScope, artifactDigest, attestationDigest string) (project.CandidateSourceSnapshot, error) {
	if s == nil || s.sources == nil {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	artifactDigest, attestationDigest = strings.TrimSpace(artifactDigest), strings.TrimSpace(attestationDigest)
	if !validNativeDigest(artifactDigest) || !validNativeDigest(attestationDigest) {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceInvalid
	}
	snapshot, err := s.sources.Snapshot(ctx, scope.ProjectID.String(), s.storageDomain, artifactDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}
	if validateErr := validateNativeSnapshot(snapshot, scope, artifactDigest, s.storageDomain); validateErr != nil {
		return project.CandidateSourceSnapshot{}, validateErr
	}
	attestation, err := s.sources.SnapshotAttestation(ctx, snapshot.SnapshotID, attestationDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}
	if attestation.SnapshotID != snapshot.SnapshotID || attestation.SourceDigest != snapshot.SourceDigest || attestation.AttestationDigest != attestationDigest {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceConflict
	}
	result := sourceSnapshotResult(snapshot)
	result.SourceAttestationDigest = attestation.AttestationDigest
	result.SourceRevision = sourceRevisionFromAttestation(attestation)
	return result, nil
}

// SourceObjectRefs is an object-backed reader contract for callers that need
// to stage a retained snapshot without receiving local paths.
func (s *NativeCandidateSourceSynchronizer) SourceObjectRefs(ctx context.Context, scope project.CandidateSourceScope, artifactDigest string) ([]project.CandidateSourceObjectRef, error) {
	if s == nil || s.sources == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return nil, err
	}
	artifactDigest = strings.TrimSpace(artifactDigest)
	if !validNativeDigest(artifactDigest) {
		return nil, project.ErrCandidateSourceInvalid
	}
	refs, err := s.sources.SnapshotSourceObjectRefs(ctx, scope.ProjectID.String(), s.storageDomain, artifactDigest)
	if err != nil {
		return nil, mapNativeError(err)
	}
	out := make([]project.CandidateSourceObjectRef, len(refs))
	var snapshotID uuid.UUID
	for i, ref := range refs {
		if ref.SnapshotID == uuid.Nil || ref.ProjectID != scope.ProjectID.String() || ref.StorageSecurityDomain != s.storageDomain || ref.Ordinal != i || !canonicalNativePath(ref.Path) || !validNativeDigest(ref.Digest) || ref.SizeBytes < 0 || ref.SizeBytes > maxBlobBytes || !validNativeObjectKey(ref.ObjectKey) || ref.ContentType == "" || !validNativeText(ref.ContentType) || !validNativeDigest(ref.MetadataDigest) {
			return nil, project.ErrCandidateSourceConflict
		}
		if i == 0 {
			snapshotID = ref.SnapshotID
		} else if ref.SnapshotID != snapshotID {
			return nil, project.ErrCandidateSourceConflict
		}
		out[i] = project.CandidateSourceObjectRef{Path: ref.Path, Digest: ref.Digest, SizeBytes: ref.SizeBytes, ObjectKey: ref.ObjectKey, ContentType: ref.ContentType, MetadataDigest: ref.MetadataDigest, StorageSecurityDomain: ref.StorageSecurityDomain}
	}
	return out, nil
}

// OpenSourceObject opens one exact object key after checking its retained
// metadata.  The returned body is owned by the caller and has no path.
func (s *NativeCandidateSourceSynchronizer) OpenSourceObject(ctx context.Context, scope project.CandidateSourceScope, ref project.CandidateSourceObjectRef) (io.ReadCloser, error) {
	if s == nil || s.objects == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return nil, err
	}
	if ref.StorageSecurityDomain != s.storageDomain || !canonicalNativePath(ref.Path) || !validNativeDigest(ref.Digest) || ref.SizeBytes < 0 || ref.SizeBytes > maxBlobBytes || !validNativeObjectKey(ref.ObjectKey) || ref.ContentType == "" || !validNativeText(ref.ContentType) || !validNativeDigest(ref.MetadataDigest) {
		return nil, project.ErrCandidateSourceInvalid
	}
	blob, err := s.sources.SourceBlob(ctx, scope.ProjectID.String(), s.storageDomain, ref.Digest)
	if err != nil {
		return nil, mapNativeError(err)
	}
	if blob.ProjectID != scope.ProjectID.String() || blob.StorageSecurityDomain != s.storageDomain || blob.Digest != ref.Digest || blob.SizeBytes != ref.SizeBytes || blob.ObjectKey != ref.ObjectKey || blob.ContentType != ref.ContentType || blob.MetadataDigest != ref.MetadataDigest {
		return nil, project.ErrCandidateSourceConflict
	}
	obj, err := s.objects.Open(ctx, ref.ObjectKey)
	if err != nil {
		return nil, mapNativeError(err)
	}
	expected := objectstore.ObjectMetadata{StorageSecurityDomain: s.storageDomain, Digest: ref.Digest, SizeBytes: ref.SizeBytes, ContentType: ref.ContentType, MetadataDigest: ref.MetadataDigest}
	if err := verifyInfo(obj.Info, expected, ref.ObjectKey); err != nil {
		_ = obj.Body.Close()
		return nil, project.ErrCandidateSourceConflict
	}
	return obj.Body, nil
}

// OpenProjectArtifact opens the immutable compiler artifact for an exact
// retained source digest without exposing a local path.
func (s *NativeCandidateSourceSynchronizer) OpenProjectArtifact(ctx context.Context, scope project.CandidateSourceScope, artifactDigest string) (io.ReadCloser, error) {
	if s == nil || s.sources == nil || s.objects == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	if err := validateNativeScope(scope); err != nil {
		return nil, err
	}
	artifactDigest = strings.TrimSpace(artifactDigest)
	if !validNativeDigest(artifactDigest) {
		return nil, project.ErrCandidateSourceInvalid
	}
	snapshot, err := s.sources.Snapshot(ctx, scope.ProjectID.String(), s.storageDomain, artifactDigest)
	if err != nil {
		return nil, mapNativeError(err)
	}
	if validateErr := validateNativeSnapshot(snapshot, scope, artifactDigest, s.storageDomain); validateErr != nil {
		return nil, validateErr
	}
	obj, err := s.objects.Open(ctx, snapshot.ProjectArtifactObjectKey)
	if err != nil {
		return nil, mapNativeError(err)
	}
	if obj.Info.Key != snapshot.ProjectArtifactObjectKey || obj.Info.StorageSecurityDomain != s.storageDomain || obj.Info.Digest != snapshot.ProjectArtifactDigest || obj.Info.SizeBytes != snapshot.ProjectArtifactSizeBytes || obj.Info.ContentType != "application/octet-stream" || obj.Info.MetadataDigest != emptyMetadataDigest() {
		_ = obj.Body.Close()
		return nil, project.ErrCandidateSourceConflict
	}
	return obj.Body, nil
}

// SourceRepository read capabilities are deliberately optional on the base
// mutation port so Coordinator fakes and narrow test doubles remain valid.
type SourceBlobReader interface {
	SourceBlob(context.Context, string, string, string) (projectpostgres.SourceBlob, error)
}

// NativeSourceRepository is the fail-closed read/write authority required by
// the native adapter. Plan object refs are obtained by one bounded join inside
// a caller-owned transaction; no per-entry source lookups are used.
type NativeSourceRepository interface {
	SourceRepository
	SourceBlobReader
	PlanSourceObjectRefsTx(context.Context, projectpostgres.SourceTx, uuid.UUID, string) ([]projectpostgres.SourceSyncPlanObjectRef, error)
	SourceObjectRefReader
	SourceAttestationReader
}
type SourceObjectRefReader interface {
	SnapshotSourceObjectRefs(context.Context, string, string, string) ([]projectpostgres.SourceSnapshotObjectRef, error)
}
type SourceAttestationReader interface {
	SnapshotAttestation(context.Context, uuid.UUID, string) (projectpostgres.SourceAttestation, error)
}

func (s *NativeCandidateSourceSynchronizer) beginTx(ctx context.Context) (Tx, error) {
	tx, err := s.begin(contextOrBackground(ctx))
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, project.ErrCandidateSourceUnavailable
	}
	return tx, nil
}

func (s *NativeCandidateSourceSynchronizer) missing(ctx context.Context, plan projectpostgres.SyncPlan) ([]string, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	missing, err := s.sources.ListMissingPlanSourceBlobDigestsTx(ctx, tx, plan.PlanID, plan.OwnerID)
	if err != nil {
		return nil, mapNativeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	sort.Strings(missing)
	return missing, nil
}

func (s *NativeCandidateSourceSynchronizer) putImmutable(ctx context.Context, key string, body []byte, metadata objectstore.ObjectMetadata) error {
	_, err := s.putImmutableInfo(ctx, key, body, metadata)
	return err
}

func (s *NativeCandidateSourceSynchronizer) putImmutableInfo(ctx context.Context, key string, body []byte, metadata objectstore.ObjectMetadata) (objectstore.ObjectInfo, error) {
	info, err := s.objects.PutImmutable(ctx, key, bytes.NewReader(body), metadata)
	if err == nil {
		if verifyErr := verifyInfo(info, metadata, key); verifyErr != nil {
			return objectstore.ObjectInfo{}, verifyErr
		}
		return info, nil
	}
	if !errors.Is(err, objectstore.ErrAmbiguous) {
		return objectstore.ObjectInfo{}, err
	}
	obj, openErr := s.objects.Open(ctx, key)
	if openErr != nil {
		return objectstore.ObjectInfo{}, fmt.Errorf("%w: reconcile immutable object %q: %v", objectstore.ErrAmbiguous, key, openErr)
	}
	defer obj.Body.Close()
	if verifyErr := verifyInfo(obj.Info, metadata, key); verifyErr != nil {
		return objectstore.ObjectInfo{}, verifyErr
	}
	return obj.Info, nil
}

func (s *NativeCandidateSourceSynchronizer) planObjectRefs(ctx context.Context, plan projectpostgres.SyncPlan) ([]projectpostgres.SourceSyncPlanObjectRef, error) {
	tx, err := s.beginTx(ctx)
	if err != nil {
		return nil, err
	}
	refs, err := s.sources.PlanSourceObjectRefsTx(ctx, tx, plan.PlanID, plan.OwnerID)
	if err != nil {
		_ = tx.Rollback(context.Background())
		return nil, mapNativeError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return refs, nil
}

func (s *NativeCandidateSourceSynchronizer) stageSourceSet(ctx context.Context, plan projectpostgres.SyncPlan, refs []projectpostgres.SourceSyncPlanObjectRef) ([]SourceFile, error) {
	if len(refs) != len(plan.Entries) {
		return nil, project.ErrCandidateSourceConflict
	}
	files := make([]SourceFile, len(plan.Entries))
	for i, entry := range plan.Entries {
		ref := refs[i]
		if ref.PlanID != plan.PlanID || ref.SizeBytes != entry.SizeBytes || ref.ProjectID != plan.ProjectID || ref.StorageSecurityDomain != plan.StorageSecurityDomain || ref.Digest != entry.Digest || ref.Path != entry.Path || ref.Ordinal != i || !canonicalNativePath(ref.Path) || !validNativeDigest(ref.Digest) || !validNativeObjectKey(ref.ObjectKey) || ref.ContentType == "" || !validNativeText(ref.ContentType) || !validNativeDigest(ref.MetadataDigest) {
			return nil, project.ErrCandidateSourceConflict
		}
		obj, err := s.objects.Open(ctx, ref.ObjectKey)
		if err != nil {
			return nil, mapNativeError(err)
		}
		expected := objectstore.ObjectMetadata{StorageSecurityDomain: plan.StorageSecurityDomain, Digest: ref.Digest, SizeBytes: ref.SizeBytes, ContentType: ref.ContentType, MetadataDigest: ref.MetadataDigest}
		if err := verifyInfo(obj.Info, expected, ref.ObjectKey); err != nil {
			_ = obj.Body.Close()
			return nil, project.ErrCandidateSourceConflict
		}
		content, readErr := io.ReadAll(io.LimitReader(obj.Body, maxBlobBytes+1))
		_ = obj.Body.Close()
		if readErr != nil || int64(len(content)) != entry.SizeBytes || sha256Identity(content) != entry.Digest {
			return nil, project.ErrCandidateSourceConflict
		}
		files[i] = SourceFile{Path: entry.Path, Digest: entry.Digest, Bytes: content, ObjectKey: ref.ObjectKey, ContentType: ref.ContentType, MetadataDigest: ref.MetadataDigest, StorageSecurityDomain: plan.StorageSecurityDomain}
	}
	return files, nil
}

func (s *NativeCandidateSourceSynchronizer) compileSourceSet(ctx context.Context, set CompileInput) (CompileOutput, error) {
	set.Files = cloneFiles(set.Files)
	return s.compiler.Compile(ctx, set)
}

func (s *NativeCandidateSourceSynchronizer) exactSnapshotResult(ctx context.Context, scope project.CandidateSourceScope, snapshot projectpostgres.SourceSnapshot, revision *project.CandidateSourceRevision) (project.CandidateSourceSnapshot, error) {
	if err := validateNativeSnapshot(snapshot, scope, snapshot.SourceDigest, s.storageDomain); err != nil {
		return project.CandidateSourceSnapshot{}, err
	}
	expected := nativeAttestation(snapshot.SnapshotID, snapshot.SourceDigest, revision)
	attestation, err := s.sources.SnapshotAttestation(ctx, snapshot.SnapshotID, expected.AttestationDigest)
	if err != nil {
		return project.CandidateSourceSnapshot{}, mapNativeError(err)
	}
	if attestation.SnapshotID != snapshot.SnapshotID || attestation.SourceDigest != snapshot.SourceDigest || attestation.AttestationDigest != expected.AttestationDigest || attestation.Revision != expected.Revision || attestation.Repository != expected.Repository || attestation.Ref != expected.Ref || attestation.ChangeID != expected.ChangeID {
		return project.CandidateSourceSnapshot{}, project.ErrCandidateSourceConflict
	}
	result := sourceSnapshotResult(snapshot)
	result.SourceAttestationDigest = attestation.AttestationDigest
	result.SourceRevision = sourceRevisionFromAttestation(attestation)
	return result, nil
}

func sourceSnapshotResult(snapshot projectpostgres.SourceSnapshot) project.CandidateSourceSnapshot {
	return project.CandidateSourceSnapshot{ProjectID: projectgraph.ResourceID(snapshot.ProjectID), ArtifactDigest: snapshot.SourceDigest, ProjectFile: snapshot.ProjectFile, ProjectArtifactObjectKey: snapshot.ProjectArtifactObjectKey, ManifestObjectKey: snapshot.ManifestObjectKey, ProjectDigest: snapshot.ProjectDigest}
}

func validateNativeSnapshot(snapshot projectpostgres.SourceSnapshot, scope project.CandidateSourceScope, sourceDigest, domain string) error {
	if snapshot.SnapshotID == uuid.Nil || snapshot.ProjectID != scope.ProjectID.String() || snapshot.StorageSecurityDomain != domain || snapshot.SourceDigest != sourceDigest || !canonicalNativePath(snapshot.ProjectFile) || !validNativeDigest(snapshot.ProjectDigest) || !validNativeObjectKey(snapshot.ProjectArtifactObjectKey) || !validNativeDigest(snapshot.ProjectArtifactDigest) || snapshot.ProjectArtifactSizeBytes <= 0 || !validNativeObjectKey(snapshot.ManifestObjectKey) || !validNativeDigest(snapshot.ManifestObjectDigest) || snapshot.ManifestObjectSizeBytes <= 0 || strings.TrimSpace(snapshot.CompilerVersion) == "" || !validNativeText(snapshot.CompilerVersion) || snapshot.SchemaVersion <= 0 {
		return project.ErrCandidateSourceConflict
	}
	return nil
}

func validateNativeScope(scope project.CandidateSourceScope) error {
	if err := scope.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project identity: %v", project.ErrCandidateSourceInvalid, err)
	}
	if strings.TrimSpace(scope.OwnerID) == "" || scope.OwnerID != strings.TrimSpace(scope.OwnerID) || !validNativeText(scope.OwnerID) {
		return fmt.Errorf("%w: owner identity is required", project.ErrCandidateSourceInvalid)
	}
	return nil
}

func normalizeNativeRequest(scope project.CandidateSourceScope, request project.CandidateSynchronizationRequest) (project.CandidateSynchronizationRequest, []projectpostgres.SourceSyncPlanEntryInput, string, error) {
	if err := validateNativeScope(scope); err != nil {
		return project.CandidateSynchronizationRequest{}, nil, "", err
	}
	r := request
	r.ProjectFile = strings.TrimSpace(r.ProjectFile)
	r.ArtifactDigest = strings.TrimSpace(r.ArtifactDigest)
	r.CandidateKey = strings.TrimSpace(r.CandidateKey)
	r.IdempotencyKey = strings.TrimSpace(r.IdempotencyKey)
	scopeKey := strings.TrimSpace(scope.CandidateKey)
	if scopeKey == "" {
		scopeKey = "default"
	}
	if r.CandidateKey == "" {
		r.CandidateKey = scopeKey
	}
	if r.CandidateKey != scopeKey {
		return project.CandidateSynchronizationRequest{}, nil, "", fmt.Errorf("%w: candidate key scope mismatch", project.ErrCandidateSourceConflict)
	}
	if !canonicalNativePath(r.ProjectFile) || !validNativeText(r.CandidateKey) || !validNativeText(r.IdempotencyKey) {
		return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
	}
	if r.ExpectedCandidateID != "" {
		r.ExpectedCandidateID = strings.TrimSpace(r.ExpectedCandidateID)
		if r.ExpectedCandidateID == "" || !validNativeText(r.ExpectedCandidateID) || r.ExpectedArtifactDigest == "" {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
	}
	if r.ExpectedArtifactDigest != "" {
		r.ExpectedArtifactDigest = strings.TrimSpace(r.ExpectedArtifactDigest)
		if !validNativeDigest(r.ExpectedArtifactDigest) || r.ExpectedArtifactDigest != r.ArtifactDigest || r.ExpectedCandidateID == "" {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
	}
	if r.SourceRevision != nil {
		revision := *r.SourceRevision
		revision.Revision = strings.TrimSpace(revision.Revision)
		revision.Repository = strings.TrimSpace(revision.Repository)
		revision.Ref = strings.TrimSpace(revision.Ref)
		revision.ChangeID = strings.TrimSpace(revision.ChangeID)
		if len(revision.Revision) > 1024 || len(revision.Repository) > 1024 || len(revision.Ref) > 1024 || len(revision.ChangeID) > 1024 || !validNativeText(revision.Revision) || !validNativeText(revision.Repository) || !validNativeText(revision.Ref) || !validNativeText(revision.ChangeID) {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
		r.SourceRevision = &revision
	}
	artifacts := append([]project.CandidateSourceArtifact(nil), r.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	entries := make([]projectpostgres.SourceSyncPlanEntryInput, len(artifacts))
	seen := make(map[string]struct{}, len(artifacts))
	var total int64
	for i, artifact := range artifacts {
		artifact.Path = strings.TrimSpace(artifact.Path)
		artifact.Digest = strings.TrimSpace(artifact.Digest)
		if !canonicalNativePath(artifact.Path) || !validNativeDigest(artifact.Digest) || artifact.SizeBytes < 0 || artifact.SizeBytes > maxBlobBytes {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
		if _, exists := seen[artifact.Path]; exists {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
		seen[artifact.Path] = struct{}{}
		total += artifact.SizeBytes
		if total > maxSourceBytes {
			return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
		}
		entries[i] = projectpostgres.SourceSyncPlanEntryInput{Path: artifact.Path, Digest: artifact.Digest, SizeBytes: artifact.SizeBytes, Ordinal: i}
		artifacts[i] = artifact
	}
	if len(entries) == 0 || len(entries) > maxSourceFiles {
		return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
	}
	snapshotEntries := make([]projectpostgres.SourceSnapshotEntryInput, len(entries))
	for i, entry := range entries {
		snapshotEntries[i] = projectpostgres.SourceSnapshotEntryInput{Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: i}
	}
	canonicalDigest := projectpostgres.CanonicalSourceDigest(scope.ProjectID.String(), r.ProjectFile, snapshotEntries)
	if r.ArtifactDigest == "" {
		r.ArtifactDigest = canonicalDigest
	}
	if !validNativeDigest(r.ArtifactDigest) || r.ArtifactDigest != canonicalDigest {
		return project.CandidateSynchronizationRequest{}, nil, "", fmt.Errorf("%w: artifact digest does not match canonical source entries", project.ErrCandidateSourceInvalid)
	}
	r.Artifacts = artifacts
	requestDigest := nativeRequestDigest(r)
	if requestDigest == "" {
		return project.CandidateSynchronizationRequest{}, nil, "", project.ErrCandidateSourceInvalid
	}
	return r, entries, requestDigest, nil
}

func nativeRequestDigest(request project.CandidateSynchronizationRequest) string {
	request.PlanID = ""
	request.IdempotencyKey = ""
	request.Artifacts = append([]project.CandidateSourceArtifact(nil), request.Artifacts...)
	sort.Slice(request.Artifacts, func(i, j int) bool { return request.Artifacts[i].Path < request.Artifacts[j].Path })
	b, err := json.Marshal(request)
	if err != nil {
		return ""
	}
	return sha256Identity(b)
}

func samePlanRequest(plan projectpostgres.SyncPlan, input projectpostgres.SyncPlanInput) bool {
	if plan.PlanID != input.PlanID || plan.OperationID != input.OperationID || plan.ProjectID != input.ProjectID || plan.StorageSecurityDomain != input.StorageSecurityDomain || plan.OwnerID != input.OwnerID || plan.CandidateKey != input.CandidateKey || plan.SourceDigest != input.SourceDigest || plan.ProjectFile != input.ProjectFile || plan.RequestDigest != input.RequestDigest {
		return false
	}
	return sameEntries(plan.Entries, input.Entries)
}

func sameEntries(got []projectpostgres.SourceSyncPlanEntry, want []projectpostgres.SourceSyncPlanEntryInput) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Path != want[i].Path || got[i].Digest != want[i].Digest || got[i].SizeBytes != want[i].SizeBytes || got[i].Ordinal != want[i].Ordinal {
			return false
		}
	}
	return true
}

func validatePlanOwnerAndExpiry(plan projectpostgres.SyncPlan, scope project.CandidateSourceScope, domain string, checkCandidateKey bool) error {
	scopeCandidateKey := strings.TrimSpace(scope.CandidateKey)
	if scopeCandidateKey == "" {
		scopeCandidateKey = "default"
	}
	if plan.ProjectID != scope.ProjectID.String() || plan.StorageSecurityDomain != domain || plan.OwnerID != scope.OwnerID || checkCandidateKey && plan.CandidateKey != scopeCandidateKey {
		return projectpostgres.ErrSourceWrongOwner
	}
	if plan.State != "open" && plan.State != "committed" {
		return projectpostgres.ErrSourceConflict
	}
	return nil
}

func nativePlanEntries(plan projectpostgres.SyncPlan) []projectpostgres.SourceSnapshotEntryInput {
	entries := make([]projectpostgres.SourceSnapshotEntryInput, len(plan.Entries))
	for i, entry := range plan.Entries {
		entries[i] = projectpostgres.SourceSnapshotEntryInput{Path: entry.Path, Digest: entry.Digest, SizeBytes: entry.SizeBytes, Ordinal: i}
	}
	return entries
}

func nativeAttestation(snapshotID uuid.UUID, sourceDigest string, revision *project.CandidateSourceRevision) projectpostgres.SourceAttestationInput {
	payload := map[string]string{}
	if revision != nil {
		payload["revision"], payload["repository"], payload["ref"], payload["changeId"] = strings.TrimSpace(revision.Revision), strings.TrimSpace(revision.Repository), strings.TrimSpace(revision.Ref), strings.TrimSpace(revision.ChangeID)
	}
	b, _ := json.Marshal(payload)
	digest := sha256Identity(b)
	return projectpostgres.SourceAttestationInput{AttestationID: deterministicUUID("source-attestation", snapshotID.String(), digest), SnapshotID: snapshotID, SourceDigest: sourceDigest, AttestationDigest: digest, Payload: b, Revision: payload["revision"], Repository: payload["repository"], Ref: payload["ref"], ChangeID: payload["changeId"]}
}

func sourceRevisionFromAttestation(att projectpostgres.SourceAttestation) *project.CandidateSourceRevision {
	if att.Revision == "" && att.Repository == "" && att.Ref == "" && att.ChangeID == "" {
		return nil
	}
	return &project.CandidateSourceRevision{Revision: att.Revision, Repository: att.Repository, Ref: att.Ref, ChangeID: att.ChangeID}
}

func normalizeNativeCompileOutput(out CompileOutput, projectID, storageDomain, sourceDigest string) (CompileOutput, error) {
	out.ProjectDigest = strings.TrimSpace(out.ProjectDigest)
	out.CompilerVersion = strings.TrimSpace(out.CompilerVersion)
	if !validNativeDigest(out.ProjectDigest) || out.CompilerVersion == "" || out.SchemaVersion <= 0 || len(out.ProjectArtifact) == 0 || len(out.Manifest) == 0 || int64(len(out.ProjectArtifact)) > maxSourceBytes || int64(len(out.Manifest)) > maxSourceBytes {
		return CompileOutput{}, project.ErrCandidateSourceInvalid
	}
	// Object identity belongs to the adapter, never to compiler output. This
	// prevents a compiler from smuggling a host path into the immutable store.
	out.ProjectArtifactObjectKey = nativeArtifactKey("project", projectID, storageDomain, sourceDigest)
	out.ManifestObjectKey = nativeArtifactKey("manifest", projectID, storageDomain, sourceDigest)
	if out.ProjectArtifactMetadataDigest == "" {
		out.ProjectArtifactMetadataDigest = emptyMetadataDigest()
	}
	if out.ManifestMetadataDigest == "" {
		out.ManifestMetadataDigest = emptyMetadataDigest()
	}
	if !validNativeDigest(out.ProjectArtifactMetadataDigest) || !validNativeDigest(out.ManifestMetadataDigest) {
		return CompileOutput{}, project.ErrCandidateSourceInvalid
	}
	return out, nil
}

func nativeArtifactKey(kind, projectID, storageDomain, sourceDigest string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00" + storageDomain + "\x00" + sourceDigest))
	return "source-artifacts/" + kind + "/" + hex.EncodeToString(sum[:])
}
func sourceObjectKey(identity string) string {
	return "source-blobs/" + strings.TrimPrefix(identity, "sha256:")
}
func emptyMetadataDigest() string { return sha256Identity(nil) }

func deterministicUUID(label string, values ...string) uuid.UUID {
	h := sha256.New()
	h.Write([]byte(label))
	for _, value := range values {
		h.Write([]byte{0})
		h.Write([]byte(value))
	}
	var id uuid.UUID
	copy(id[:], h.Sum(nil)[:16])
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

func parsePlanUUID(value string) (uuid.UUID, error) {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, errors.New("canonical UUID is required")
	}
	return id, nil
}

func mapNativeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, projectpostgres.ErrSourceExpired) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceConflict, err)
	}
	if errors.Is(err, projectpostgres.ErrSourceWrongOwner) || errors.Is(err, projectpostgres.ErrSourceUnsolicited) || errors.Is(err, projectpostgres.ErrSourceConflict) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceConflict, err)
	}
	if errors.Is(err, projectpostgres.ErrSourceInvalid) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceInvalid, err)
	}
	if errors.Is(err, projectpostgres.ErrSourceNotFound) || errors.Is(err, projectpostgres.ErrNotFound) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceConflict, err)
	}
	if errors.Is(err, objectstore.ErrNotFound) || errors.Is(err, objectstore.ErrConflict) || errors.Is(err, objectstore.ErrCorrupt) || errors.Is(err, objectstore.ErrDomainMismatch) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceConflict, err)
	}
	if errors.Is(err, ErrConflict) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceConflict, err)
	}
	if errors.Is(err, objectstore.ErrInvalid) || errors.Is(err, objectstore.ErrInvalidKey) || errors.Is(err, objectstore.ErrInvalidPrefix) {
		return fmt.Errorf("%w: %v", project.ErrCandidateSourceInvalid, err)
	}
	return err
}

func cloneCandidateSourceRevision(value *project.CandidateSourceRevision) *project.CandidateSourceRevision {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validNativeDigest(value string) bool {
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value[7:])
	return err == nil
}
func validNativeObjectKey(value string) bool {
	if value == "" || len(value) > maxObjectKeyBytes || !validNativeText(value) || path.IsAbs(value) || path.Clean(value) != value || strings.HasPrefix(value, "../") || value == ".." || strings.Contains(value, `\`) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) >= 2 && segment[1] == ':' {
			return false
		}
	}
	return true
}
func canonicalNativePath(value string) bool {
	return value != "" && len(value) <= 1024 && validNativeText(value) && !path.IsAbs(value) && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && !strings.Contains(value, `\`)
}
func validNativeText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
