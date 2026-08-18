package devloop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/platform/digest"
	securefs "github.com/flidai/leapview/internal/platform/filesystem"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	maxTargetBlobBytes     = 16 << 20
	maxTargetSnapshotBytes = 64 << 20
	maxTargetSnapshotFiles = 10_000
	targetProjectArtifact  = "project.artifact.json"
)

// TargetStore retains content-addressed authoring blobs and atomically
// materializes compiler-validated source snapshots on the LeapView target.
type TargetStore struct {
	root      string
	blobs     string
	snapshots string
	commitMu  sync.Mutex
}

type StoredSnapshot struct {
	ProjectID               projectgraph.ResourceID
	Digest                  string
	SourceAttestationDigest string
	ProjectPath             string
	ProjectDigest           string
	ProjectArtifactPath     string
	// SourceRevision is retained in the immutable manifest alongside the
	// source bytes. It is deliberately not part of the byte-set digest, but a
	// revision change for the same bytes must conflict rather than silently
	// replacing provenance on a restarted target.
	SourceRevision *SourceRevision
}

// sourceAttestation is append-only provenance for a retained byte snapshot.
// The source digest remains the physical identity; each distinct revision is
// recorded separately so reusing identical bytes from another commit or
// repository never mutates or conflicts with the retained snapshot.
type sourceAttestation struct {
	SourceDigest   string          `json:"sourceDigest"`
	SourceRevision *SourceRevision `json:"sourceRevision,omitempty"`
}

// Snapshot returns one previously committed immutable snapshot by digest. The
// manifest is part of the retained snapshot and is revalidated before any
// paths are returned, so callers cannot turn a digest lookup into a mutable
// worktree read or accept a partially written directory.
func (store *TargetStore) Snapshot(ctx context.Context, projectID projectgraph.ResourceID, artifactDigest string) (StoredSnapshot, error) {
	return store.snapshot(ctx, projectID, artifactDigest, "")
}

// SnapshotAttestation resolves an exact append-only provenance record for a
// retained byte snapshot and verifies its content-addressed identity.
func (store *TargetStore) SnapshotAttestation(ctx context.Context, projectID projectgraph.ResourceID, artifactDigest, attestationDigest string) (StoredSnapshot, error) {
	return store.snapshot(ctx, projectID, artifactDigest, strings.TrimSpace(attestationDigest))
}

func (store *TargetStore) snapshot(ctx context.Context, projectID projectgraph.ResourceID, artifactDigest, attestationDigest string) (StoredSnapshot, error) {
	if store == nil {
		return StoredSnapshot{}, fmt.Errorf("project target store is not configured")
	}
	if err := projectID.Validate(); err != nil {
		return StoredSnapshot{}, err
	}
	request := SynchronizationPlanRequest{ProjectID: projectID, ArtifactDigest: strings.TrimSpace(artifactDigest)}
	if err := digest.ValidateSHA256Identity(request.ArtifactDigest); err != nil {
		return StoredSnapshot{}, err
	}
	directory := filepath.Join(store.snapshots, digestHex(request.ArtifactDigest))
	manifest, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return StoredSnapshot{}, err
	}
	var retained SynchronizationPlanRequest
	if err := json.Unmarshal(manifest, &retained); err != nil {
		return StoredSnapshot{}, fmt.Errorf("decode retained project snapshot manifest: %w", err)
	}
	retained, err = normalizePlanRequest(retained)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if retained.ProjectID != projectID || retained.ArtifactDigest != request.ArtifactDigest {
		return StoredSnapshot{}, fmt.Errorf("retained project snapshot identity does not match lookup")
	}
	if attestationDigest != "" {
		if err := digest.ValidateSHA256Identity(attestationDigest); err != nil {
			return StoredSnapshot{}, err
		}
		attestation, err := readSourceAttestation(directory, attestationDigest)
		if err != nil {
			return StoredSnapshot{}, err
		}
		if attestation.SourceDigest != retained.ArtifactDigest {
			return StoredSnapshot{}, fmt.Errorf("source attestation digest is bound to another snapshot")
		}
		retained.SourceRevision = cloneSourceRevision(attestation.SourceRevision)
	}
	stored, err := store.verifyStoredSnapshot(ctx, retained, directory)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if attestationDigest != "" && stored.SourceAttestationDigest != attestationDigest {
		return StoredSnapshot{}, fmt.Errorf("retained source attestation does not match requested digest")
	}
	return stored, nil
}

func NewTargetStore(root string) (*TargetStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("project target store root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	store := &TargetStore{
		root: absolute, blobs: filepath.Join(absolute, "blobs"),
		snapshots: filepath.Join(absolute, "snapshots"),
	}
	for _, directory := range []string{store.root, store.blobs, store.snapshots} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (store *TargetStore) Missing(
	ctx context.Context,
	request SynchronizationPlanRequest,
) ([]string, error) {
	request, err := normalizePlanRequest(request)
	if err != nil {
		return nil, err
	}
	missing := make([]string, 0)
	seen := make(map[string]struct{}, len(request.Artifacts))
	for _, reference := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[reference.Digest]; duplicate {
			continue
		}
		seen[reference.Digest] = struct{}{}
		if err := verifyBlob(store.blobPath(reference.Digest), reference.Digest); err != nil {
			if os.IsNotExist(err) {
				missing = append(missing, reference.Digest)
				continue
			}
			return nil, err
		}
	}
	sort.Strings(missing)
	return missing, nil
}

func (store *TargetStore) Put(ctx context.Context, identity string, source io.Reader) error {
	if store == nil || source == nil {
		return fmt.Errorf("project target store and source are required")
	}
	identity = strings.TrimSpace(identity)
	if err := digest.ValidateSHA256Identity(identity); err != nil {
		return fmt.Errorf("project source blob digest is invalid: %w", err)
	}
	target := store.blobPath(identity)
	if err := verifyBlob(target, identity); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, err := os.CreateTemp(store.blobs, ".upload-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(
		contextReader{ctx: ctx, source: source},
		maxTargetBlobBytes+1,
	))
	if copyErr != nil {
		_ = temporary.Close()
		return copyErr
	}
	if written > maxTargetBlobBytes {
		_ = temporary.Close()
		return fmt.Errorf("project source blob exceeds %d bytes", maxTargetBlobBytes)
	}
	actual := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if actual != identity {
		_ = temporary.Close()
		return fmt.Errorf("project source blob content does not match digest")
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if verifyErr := verifyBlob(target, identity); verifyErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func (store *TargetStore) Commit(
	ctx context.Context,
	request SynchronizationPlanRequest,
) (StoredSnapshot, error) {
	if store == nil {
		return StoredSnapshot{}, fmt.Errorf("project target store is not configured")
	}
	request, err := normalizePlanRequest(request)
	if err != nil {
		return StoredSnapshot{}, err
	}
	store.commitMu.Lock()
	defer store.commitMu.Unlock()

	destination := filepath.Join(store.snapshots, digestHex(request.ArtifactDigest))
	if _, err := os.Stat(destination); err == nil {
		stored, verifyErr := store.verifyStoredSnapshot(ctx, request, destination)
		if verifyErr != nil {
			return StoredSnapshot{}, verifyErr
		}
		if _, err := store.appendSourceAttestation(destination, request); err != nil {
			return StoredSnapshot{}, err
		}
		return storedSnapshot(request, destination, stored.ProjectDigest), nil
	} else if !os.IsNotExist(err) {
		return StoredSnapshot{}, err
	}
	missing, err := store.Missing(ctx, request)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if len(missing) != 0 {
		return StoredSnapshot{}, fmt.Errorf("project snapshot is missing %d source blobs", len(missing))
	}
	staging, err := os.MkdirTemp(store.snapshots, ".snapshot-*")
	if err != nil {
		return StoredSnapshot{}, err
	}
	defer os.RemoveAll(staging)
	sourceRoot := filepath.Join(staging, "source")
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		return StoredSnapshot{}, err
	}
	var total int64
	for _, reference := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return StoredSnapshot{}, err
		}
		target := filepath.Join(sourceRoot, filepath.FromSlash(reference.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return StoredSnapshot{}, err
		}
		size, err := copyRetainedBlob(store.blobPath(reference.Digest), target, reference.Digest)
		if err != nil {
			return StoredSnapshot{}, err
		}
		total += size
		if total > maxTargetSnapshotBytes {
			return StoredSnapshot{}, fmt.Errorf("project snapshot exceeds %d bytes", maxTargetSnapshotBytes)
		}
	}
	projectPath := filepath.Join(sourceRoot, filepath.FromSlash(request.ProjectFile))
	compiled, err := projectcompiler.Compile(projectPath)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if compiled.ProjectID() != request.ProjectID {
		return StoredSnapshot{}, fmt.Errorf(
			"compiled project id %q does not match synchronized project %q",
			compiled.ProjectID(), request.ProjectID,
		)
	}
	if err := writeRetainedProjectArtifact(
		filepath.Join(staging, targetProjectArtifact),
		compiled.Canonical(),
	); err != nil {
		return StoredSnapshot{}, err
	}
	manifest, err := json.Marshal(request)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), manifest, 0o600); err != nil {
		return StoredSnapshot{}, err
	}
	if err := os.Rename(staging, destination); err != nil {
		if _, statErr := os.Stat(destination); statErr == nil {
			stored, verifyErr := store.verifyStoredSnapshot(ctx, request, destination)
			if verifyErr != nil {
				return StoredSnapshot{}, verifyErr
			}
			if _, err := store.appendSourceAttestation(destination, request); err != nil {
				return StoredSnapshot{}, err
			}
			return storedSnapshot(request, destination, stored.ProjectDigest), nil
		}
		return StoredSnapshot{}, err
	}
	if _, err := store.appendSourceAttestation(destination, request); err != nil {
		return StoredSnapshot{}, err
	}
	return storedSnapshot(request, destination, compiled.Digest()), nil
}

func (store *TargetStore) verifyStoredSnapshot(
	ctx context.Context,
	request SynchronizationPlanRequest,
	directory string,
) (StoredSnapshot, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return StoredSnapshot{}, err
	}
	var manifestRequest SynchronizationPlanRequest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifestRequest); err != nil {
		return StoredSnapshot{}, fmt.Errorf("decode retained project snapshot manifest: %w", err)
	}
	manifestRequest, err = normalizePlanRequest(manifestRequest)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if manifestRequest.ProjectID != request.ProjectID || manifestRequest.ArtifactDigest != request.ArtifactDigest {
		return StoredSnapshot{}, fmt.Errorf("retained project snapshot identity does not match request")
	}
	sourceRoot := filepath.Join(directory, "source")
	for _, reference := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return StoredSnapshot{}, err
		}
		if err := verifyBlob(
			filepath.Join(sourceRoot, filepath.FromSlash(reference.Path)),
			reference.Digest,
		); err != nil {
			return StoredSnapshot{}, fmt.Errorf("verify stored project source %q: %w", reference.Path, err)
		}
	}
	projectPath := filepath.Join(sourceRoot, filepath.FromSlash(request.ProjectFile))
	compiled, err := projectcompiler.Compile(projectPath)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if compiled.ProjectID() != request.ProjectID {
		return StoredSnapshot{}, fmt.Errorf("stored project identity changed")
	}
	retainedArtifact, err := os.ReadFile(filepath.Join(directory, targetProjectArtifact))
	if os.IsNotExist(err) {
		if err := writeRetainedProjectArtifact(
			filepath.Join(directory, targetProjectArtifact),
			compiled.Canonical(),
		); err != nil {
			return StoredSnapshot{}, fmt.Errorf("repair retained project artifact: %w", err)
		}
		retainedArtifact = compiled.Canonical()
		err = nil
	}
	if err != nil {
		return StoredSnapshot{}, err
	}
	if string(retainedArtifact) != string(compiled.Canonical()) {
		return StoredSnapshot{}, fmt.Errorf("retained project artifact does not match synchronized sources")
	}
	if _, err := resolveSourceAttestation(directory, manifestRequest, ""); err != nil {
		return StoredSnapshot{}, err
	}
	return storedSnapshot(request, directory, compiled.Digest()), nil
}

func resolveSourceAttestation(directory string, request SynchronizationPlanRequest, expected string) (string, error) {
	content, err := json.Marshal(sourceAttestation{SourceDigest: request.ArtifactDigest, SourceRevision: cloneSourceRevision(request.SourceRevision)})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if expected != "" && actual != expected {
		return "", fmt.Errorf("source attestation digest mismatch")
	}
	path := filepath.Join(directory, "attestations", hex.EncodeToString(sum[:])+".json")
	stored, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read source attestation: %w", err)
	}
	if string(stored) != string(content) {
		return "", fmt.Errorf("source attestation content mismatch")
	}
	return actual, nil
}

func readSourceAttestation(directory, expected string) (sourceAttestation, error) {
	path := filepath.Join(directory, "attestations", digestHex(expected)+".json")
	content, err := os.ReadFile(path)
	if err != nil {
		return sourceAttestation{}, fmt.Errorf("read source attestation: %w", err)
	}
	sum := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != expected {
		return sourceAttestation{}, fmt.Errorf("source attestation content digest mismatch")
	}
	var attestation sourceAttestation
	if err := json.Unmarshal(content, &attestation); err != nil {
		return sourceAttestation{}, fmt.Errorf("decode source attestation: %w", err)
	}
	attestation.SourceDigest = strings.TrimSpace(attestation.SourceDigest)
	if err := digest.ValidateSHA256Identity(attestation.SourceDigest); err != nil {
		return sourceAttestation{}, err
	}
	var normalizeErr error
	attestation.SourceRevision, normalizeErr = normalizeSourceRevision(attestation.SourceRevision)
	if normalizeErr != nil {
		return sourceAttestation{}, normalizeErr
	}
	return attestation, nil
}

func writeRetainedProjectArtifact(path string, content []byte) error {
	return securefs.WritePrivateFileAtomic(path, content)
}

func (store *TargetStore) appendSourceAttestation(
	directory string,
	request SynchronizationPlanRequest,
) (string, error) {
	attestation := sourceAttestation{
		SourceDigest:   request.ArtifactDigest,
		SourceRevision: cloneSourceRevision(request.SourceRevision),
	}
	content, err := json.Marshal(attestation)
	if err != nil {
		return "", err
	}
	directory = filepath.Join(directory, "attestations")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	attestationDigest := "sha256:" + hex.EncodeToString(sum[:])
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".json")
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) != string(content) {
			return "", fmt.Errorf("source attestation identity collision")
		}
		return attestationDigest, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := securefs.WritePrivateFileAtomic(path, content); err != nil {
		if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == string(content) {
			return attestationDigest, nil
		}
		return "", err
	}
	return attestationDigest, nil
}

func sourceAttestationDigest(request SynchronizationPlanRequest) string {
	content, err := json.Marshal(sourceAttestation{SourceDigest: request.ArtifactDigest, SourceRevision: cloneSourceRevision(request.SourceRevision)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func normalizePlanRequest(request SynchronizationPlanRequest) (SynchronizationPlanRequest, error) {
	request = clonePlanRequest(request)
	request.ProjectFile = strings.TrimSpace(request.ProjectFile)
	request.ArtifactDigest = strings.TrimSpace(request.ArtifactDigest)
	request.ExpectedCandidateID = strings.TrimSpace(request.ExpectedCandidateID)
	request.ExpectedArtifactDigest = strings.TrimSpace(request.ExpectedArtifactDigest)
	if err := request.ProjectID.Validate(); err != nil || !canonicalArtifactPath(request.ProjectFile) ||
		len(request.Artifacts) == 0 || len(request.Artifacts) > maxTargetSnapshotFiles {
		return SynchronizationPlanRequest{}, fmt.Errorf("project synchronization manifest is incomplete")
	}
	if err := digest.ValidateSHA256Identity(request.ArtifactDigest); err != nil {
		return SynchronizationPlanRequest{}, fmt.Errorf("project synchronization digest is invalid: %w", err)
	}
	if request.ExpectedArtifactDigest != "" {
		if err := digest.ValidateSHA256Identity(request.ExpectedArtifactDigest); err != nil {
			return SynchronizationPlanRequest{}, fmt.Errorf("expected candidate digest is invalid: %w", err)
		}
	}
	if (request.ExpectedCandidateID == "") != (request.ExpectedArtifactDigest == "") {
		return SynchronizationPlanRequest{}, fmt.Errorf("expected candidate identity and digest must be supplied together")
	}
	seen := make(map[string]struct{}, len(request.Artifacts))
	artifacts := make([]Artifact, len(request.Artifacts))
	for index := range request.Artifacts {
		reference := &request.Artifacts[index]
		reference.Path = strings.TrimSpace(reference.Path)
		reference.Digest = strings.TrimSpace(reference.Digest)
		if !canonicalArtifactPath(reference.Path) {
			return SynchronizationPlanRequest{}, fmt.Errorf("project source path %q is unsafe", reference.Path)
		}
		if _, duplicate := seen[reference.Path]; duplicate {
			return SynchronizationPlanRequest{}, fmt.Errorf("project synchronization repeats path %q", reference.Path)
		}
		seen[reference.Path] = struct{}{}
		if err := digest.ValidateSHA256Identity(reference.Digest); err != nil {
			return SynchronizationPlanRequest{}, fmt.Errorf("project source %q digest is invalid: %w", reference.Path, err)
		}
		artifacts[index] = Artifact{Path: reference.Path, Digest: reference.Digest}
	}
	if _, exists := seen[request.ProjectFile]; !exists {
		return SynchronizationPlanRequest{}, fmt.Errorf("project entrypoint is absent from synchronization manifest")
	}
	sort.Slice(request.Artifacts, func(i, j int) bool {
		return request.Artifacts[i].Path < request.Artifacts[j].Path
	})
	if actual := candidateSetDigest(request.ProjectID, request.ProjectFile, artifacts); actual != request.ArtifactDigest {
		return SynchronizationPlanRequest{}, fmt.Errorf("project synchronization manifest does not match digest")
	}
	return clonePlanRequest(request), nil
}

func (store *TargetStore) blobPath(identity string) string {
	return filepath.Join(store.blobs, digestHex(identity))
}

func digestHex(identity string) string {
	return strings.TrimPrefix(strings.TrimSpace(identity), "sha256:")
}

func verifyBlob(path, identity string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxTargetBlobBytes {
		return fmt.Errorf("project source blob is not a bounded regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, source); err != nil {
		return err
	}
	if actual := "sha256:" + hex.EncodeToString(hash.Sum(nil)); actual != identity {
		return fmt.Errorf("retained project source blob does not match digest")
	}
	return nil
}

func copyRetainedBlob(sourcePath, targetPath, identity string) (int64, error) {
	if err := verifyBlob(sourcePath, identity); err != nil {
		return 0, err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	size, copyErr := io.Copy(target, source)
	syncErr := target.Sync()
	closeErr := target.Close()
	if copyErr != nil {
		return 0, copyErr
	}
	if syncErr != nil {
		return 0, syncErr
	}
	return size, closeErr
}

func storedSnapshot(
	request SynchronizationPlanRequest,
	directory, projectDigest string,
) StoredSnapshot {
	artifactPath := filepath.Join(directory, targetProjectArtifact)
	return StoredSnapshot{
		ProjectID: request.ProjectID, Digest: request.ArtifactDigest,
		SourceAttestationDigest: sourceAttestationDigest(request),
		ProjectPath:             filepath.Join(directory, "source", filepath.FromSlash(request.ProjectFile)),
		ProjectDigest:           projectDigest, ProjectArtifactPath: artifactPath,
		SourceRevision: cloneSourceRevision(request.SourceRevision),
	}
}

func cloneSourceRevision(value *SourceRevision) *SourceRevision {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}
