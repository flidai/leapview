package devloop

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
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
	// rootFS is the canonical project-root containment boundary. All paths
	// derived from synchronization manifests are opened relative to this
	// descriptor, so traversal and symlink escapes cannot leave the store.
	rootFS   *os.Root
	commitMu sync.Mutex
}

type StoredSnapshot struct {
	ProjectID               projectgraph.ResourceID
	Digest                  string
	SourceAttestationDigest string
	ProjectPath             string
	ProjectDigest           string
	ProjectArtifactPath     string
	// SourceRevision is carried only when resolving an exact source attestation;
	// the portable retained byte manifest deliberately omits revision provenance.
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

// retainedSourceManifest is the portable immutable source identity. Protocol
// retry, candidate-concurrency, and operation fields are intentionally absent.
type retainedSourceManifest struct {
	ProjectID      projectgraph.ResourceID `json:"projectId"`
	ProjectFile    string                  `json:"projectFile"`
	ArtifactDigest string                  `json:"artifactDigest"`
	Artifacts      []ArtifactReference     `json:"artifacts"`
}

func sourceManifest(request SynchronizationPlanRequest) retainedSourceManifest {
	return retainedSourceManifest{ProjectID: request.ProjectID, ProjectFile: request.ProjectFile, ArtifactDigest: request.ArtifactDigest, Artifacts: append([]ArtifactReference(nil), request.Artifacts...)}
}

func sourceManifestRequest(manifest retainedSourceManifest) SynchronizationPlanRequest {
	return SynchronizationPlanRequest{ProjectID: manifest.ProjectID, ProjectFile: manifest.ProjectFile, ArtifactDigest: manifest.ArtifactDigest, Artifacts: append([]ArtifactReference(nil), manifest.Artifacts...)}
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
	directory := store.snapshotRelativePath(request.ArtifactDigest)
	manifestBytes, err := store.readFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return StoredSnapshot{}, err
	}
	var manifest retainedSourceManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return StoredSnapshot{}, fmt.Errorf("decode retained project snapshot manifest: %w", err)
	}
	retained, err := normalizePlanRequest(sourceManifestRequest(manifest))
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
		attestation, err := store.readSourceAttestation(directory, attestationDigest)
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
	if attestationDigest == "" {
		// A byte-digest lookup is intentionally provenance-neutral. Callers that
		// need revision evidence must resolve an exact SnapshotAttestation.
		stored.SourceAttestationDigest = ""
		stored.SourceRevision = nil
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
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, err
	}
	// Resolve the configured root once. The descriptor opened below remains
	// anchored to this canonical directory even if its name is later moved.
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	rootFS, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, err
	}
	store := &TargetStore{
		root: absolute, blobs: filepath.Join(absolute, "blobs"),
		snapshots: filepath.Join(absolute, "snapshots"), rootFS: rootFS,
	}
	for _, directory := range []string{"blobs", "snapshots"} {
		if err := store.mkdirAll(directory); err != nil {
			_ = rootFS.Close()
			return nil, err
		}
		if err := store.rootFS.Chmod(directory, 0o700); err != nil {
			_ = rootFS.Close()
			return nil, err
		}
	}
	return store, nil
}

// Close releases the descriptor that anchors all target-store operations.
// Callers that construct short-lived stores should close them; the project
// module retains its store for the application lifetime.
func (store *TargetStore) Close() error {
	if store == nil || store.rootFS == nil {
		return nil
	}
	return store.rootFS.Close()
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
		if err := store.verifyBlob(reference.Digest, reference.SizeBytes); err != nil {
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
	target := store.blobRelativePath(identity)
	if err := store.verifyBlob(identity); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary, temporaryRelative, err := store.createTemp("blobs", ".upload-")
	if err != nil {
		return err
	}
	defer func() { _ = store.remove(temporaryRelative) }()
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
	if err := store.link(temporaryRelative, target); err != nil {
		if verifyErr := store.verifyBlob(identity); verifyErr == nil {
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

	destination := store.snapshotRelativePath(request.ArtifactDigest)
	if _, err := store.stat(destination); err == nil {
		stored, verifyErr := store.verifyStoredSnapshot(ctx, request, destination)
		if verifyErr != nil {
			return StoredSnapshot{}, verifyErr
		}
		if _, err := store.appendSourceAttestation(destination, request); err != nil {
			return StoredSnapshot{}, err
		}
		return store.storedSnapshot(request, destination, stored.ProjectDigest)
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
	staging, err := store.createTempDirectory("snapshots", ".snapshot-")
	if err != nil {
		return StoredSnapshot{}, err
	}
	stagingRelative := staging
	defer func() { _ = store.removeAll(stagingRelative) }()
	sourceRootRelative := filepath.Join(stagingRelative, "source")
	if err := store.mkdirAll(sourceRootRelative); err != nil {
		return StoredSnapshot{}, err
	}
	sourceFiles := make(map[string][]byte, len(request.Artifacts))
	var total int64
	for _, reference := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return StoredSnapshot{}, err
		}
		targetRelative := filepath.Join(sourceRootRelative, filepath.FromSlash(reference.Path))
		if err := store.mkdirAll(filepath.Dir(targetRelative)); err != nil {
			return StoredSnapshot{}, err
		}
		size, err := store.copyRetainedBlob(targetRelative, reference.Digest, reference.SizeBytes)
		if err != nil {
			return StoredSnapshot{}, err
		}
		total += size
		if total > maxTargetSnapshotBytes {
			return StoredSnapshot{}, fmt.Errorf("project snapshot exceeds %d bytes", maxTargetSnapshotBytes)
		}
		content, err := store.readFile(targetRelative)
		if err != nil {
			return StoredSnapshot{}, err
		}
		sourceFiles[reference.Path] = content
	}
	compiled, err := projectcompiler.CompileProjectFiles(sourceFiles, request.ProjectFile)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if compiled.ProjectID() != request.ProjectID {
		return StoredSnapshot{}, fmt.Errorf(
			"compiled project id %q does not match synchronized project %q",
			compiled.ProjectID(), request.ProjectID,
		)
	}
	if err := store.writePrivateFile(filepath.Join(stagingRelative, targetProjectArtifact), compiled.Canonical()); err != nil {
		return StoredSnapshot{}, err
	}
	manifest, err := json.Marshal(sourceManifest(request))
	if err != nil {
		return StoredSnapshot{}, err
	}
	if err := store.writePrivateFile(filepath.Join(stagingRelative, "manifest.json"), manifest); err != nil {
		return StoredSnapshot{}, err
	}
	if err := store.rename(stagingRelative, destination); err != nil {
		if _, statErr := store.stat(destination); statErr == nil {
			stored, verifyErr := store.verifyStoredSnapshot(ctx, request, destination)
			if verifyErr != nil {
				return StoredSnapshot{}, verifyErr
			}
			if _, err := store.appendSourceAttestation(destination, request); err != nil {
				return StoredSnapshot{}, err
			}
			return store.storedSnapshot(request, destination, stored.ProjectDigest)
		}
		return StoredSnapshot{}, err
	}
	if _, err := store.appendSourceAttestation(destination, request); err != nil {
		return StoredSnapshot{}, err
	}
	return store.storedSnapshot(request, destination, compiled.Digest())
}

func (store *TargetStore) verifyStoredSnapshot(
	ctx context.Context,
	request SynchronizationPlanRequest,
	directory string,
) (StoredSnapshot, error) {
	manifestBytes, err := store.readFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return StoredSnapshot{}, err
	}
	var manifest retainedSourceManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return StoredSnapshot{}, fmt.Errorf("decode retained project snapshot manifest: %w", err)
	}
	manifestRequest, err := normalizePlanRequest(sourceManifestRequest(manifest))
	if err != nil {
		return StoredSnapshot{}, err
	}
	if manifestRequest.ProjectID != request.ProjectID || manifestRequest.ArtifactDigest != request.ArtifactDigest {
		return StoredSnapshot{}, fmt.Errorf("retained project snapshot identity does not match request")
	}
	sourceRoot := filepath.Join(directory, "source")
	sourceFiles := make(map[string][]byte, len(request.Artifacts))
	for _, reference := range request.Artifacts {
		if err := ctx.Err(); err != nil {
			return StoredSnapshot{}, err
		}
		if err := store.verifyBlobAt(
			filepath.Join(sourceRoot, filepath.FromSlash(reference.Path)),
			reference.Digest,
			reference.SizeBytes,
		); err != nil {
			return StoredSnapshot{}, fmt.Errorf("verify stored project source %q: %w", reference.Path, err)
		}
		content, err := store.readFile(filepath.Join(sourceRoot, filepath.FromSlash(reference.Path)))
		if err != nil {
			return StoredSnapshot{}, err
		}
		sourceFiles[reference.Path] = content
	}
	compiled, err := projectcompiler.CompileProjectFiles(sourceFiles, request.ProjectFile)
	if err != nil {
		return StoredSnapshot{}, err
	}
	if compiled.ProjectID() != request.ProjectID {
		return StoredSnapshot{}, fmt.Errorf("stored project identity changed")
	}
	retainedArtifact, err := store.readFile(filepath.Join(directory, targetProjectArtifact))
	if os.IsNotExist(err) {
		if err := store.writePrivateFile(filepath.Join(directory, targetProjectArtifact), compiled.Canonical()); err != nil {
			existing, readErr := store.readFile(filepath.Join(directory, targetProjectArtifact))
			if readErr != nil || !bytes.Equal(existing, compiled.Canonical()) {
				return StoredSnapshot{}, fmt.Errorf("repair retained project artifact: %w", err)
			}
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
	return store.storedSnapshot(request, directory, compiled.Digest())
}

func (store *TargetStore) readSourceAttestation(directory, expected string) (sourceAttestation, error) {
	path := filepath.Join(directory, "attestations", digestHex(expected)+".json")
	content, err := store.readFile(path)
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
	if err := store.mkdirAll(directory); err != nil {
		return "", err
	}
	sum := sha256.Sum256(content)
	attestationDigest := "sha256:" + hex.EncodeToString(sum[:])
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".json")
	if existing, err := store.readFile(path); err == nil {
		if string(existing) != string(content) {
			return "", fmt.Errorf("source attestation identity collision")
		}
		return attestationDigest, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := store.writePrivateFile(path, content); err != nil {
		if existing, readErr := store.readFile(path); readErr == nil && string(existing) == string(content) {
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
		if reference.SizeBytes < 0 || reference.SizeBytes > maxTargetBlobBytes {
			return SynchronizationPlanRequest{}, fmt.Errorf("project source %q size is invalid", reference.Path)
		}
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
		artifacts[index] = Artifact{Path: reference.Path, Digest: reference.Digest, SizeBytes: reference.SizeBytes}
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

func (store *TargetStore) blobRelativePath(identity string) string {
	return filepath.Join("blobs", digestHex(identity))
}

func (store *TargetStore) snapshotRelativePath(identity string) string {
	return filepath.Join("snapshots", digestHex(identity))
}

func digestHex(identity string) string {
	return strings.TrimPrefix(strings.TrimSpace(identity), "sha256:")
}

// cleanRelativePath is the one path boundary for all names supplied to the
// descriptor-backed store. os.Root rejects escapes itself, while this check
// keeps callers from accidentally passing absolute or parent-traversing names.
func cleanRelativePath(value string) (string, error) {
	if value == "" || filepath.IsAbs(value) || filepath.VolumeName(value) != "" || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("target-store path must be relative")
	}
	clean := filepath.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("target-store path escapes project root")
	}
	return clean, nil
}

func (store *TargetStore) absolutePath(relative string) (string, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return "", err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return "", err
	}
	return filepath.Join(store.root, clean), nil
}

// rejectSymlinkComponents validates the existing portion of a path before a
// descriptor-relative operation. os.Root prevents escapes even if a path is
// swapped concurrently; rejecting links here also prevents surprising
// in-root redirection and makes retained snapshots structurally immutable.
func (store *TargetStore) rejectSymlinkComponents(relative string) error {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return err
	}
	current := ""
	components := strings.Split(filepath.ToSlash(clean), "/")
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, statErr := store.rootFS.Lstat(current)
		if os.IsNotExist(statErr) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("target-store path %q contains a symlink", relative)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("target-store path %q has a non-directory parent", relative)
		}
	}
	return nil
}

func (store *TargetStore) mkdirAll(relative string) error {
	if relative == "." {
		return nil
	}
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return err
	}
	current := ""
	for _, component := range strings.Split(filepath.ToSlash(clean), "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if err := store.rootFS.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := store.rootFS.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("target-store path %q is not a directory", current)
		}
	}
	return nil
}

func (store *TargetStore) createTemp(directory, prefix string) (*os.File, string, error) {
	cleanDirectory, err := cleanRelativePath(directory)
	if err != nil {
		return nil, "", err
	}
	if err := store.mkdirAll(cleanDirectory); err != nil {
		return nil, "", err
	}
	for range 32 {
		var suffix [16]byte
		if _, err := cryptorand.Read(suffix[:]); err != nil {
			return nil, "", err
		}
		relative := filepath.Join(cleanDirectory, prefix+hex.EncodeToString(suffix[:]))
		file, err := store.openFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return file, relative, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("could not allocate target-store temporary file")
}

func (store *TargetStore) createTempDirectory(directory, prefix string) (string, error) {
	cleanDirectory, err := cleanRelativePath(directory)
	if err != nil {
		return "", err
	}
	if err := store.mkdirAll(cleanDirectory); err != nil {
		return "", err
	}
	for range 32 {
		var suffix [16]byte
		if _, err := cryptorand.Read(suffix[:]); err != nil {
			return "", err
		}
		relative := filepath.Join(cleanDirectory, prefix+hex.EncodeToString(suffix[:]))
		if err := store.rootFS.Mkdir(relative, 0o700); err == nil {
			return relative, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("could not allocate target-store temporary directory")
}

func (store *TargetStore) stat(relative string) (os.FileInfo, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return nil, err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return nil, err
	}
	return store.rootFS.Stat(clean)
}

func (store *TargetStore) readFile(relative string) ([]byte, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return nil, err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return nil, err
	}
	return store.rootFS.ReadFile(clean)
}

func (store *TargetStore) open(relative string) (*os.File, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return nil, err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return nil, err
	}
	return store.rootFS.Open(clean)
}

func (store *TargetStore) openFile(relative string, flag int, mode os.FileMode) (*os.File, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return nil, err
	}
	if err := store.rejectSymlinkComponents(filepath.Dir(clean)); err != nil {
		return nil, err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return nil, err
	}
	return store.rootFS.OpenFile(clean, flag, mode)
}

func (store *TargetStore) rename(oldRelative, newRelative string) error {
	oldClean, err := cleanRelativePath(oldRelative)
	if err != nil {
		return err
	}
	newClean, err := cleanRelativePath(newRelative)
	if err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(oldClean); err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(filepath.Dir(newClean)); err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(newClean); err != nil {
		return err
	}
	return store.rootFS.Rename(oldClean, newClean)
}

func (store *TargetStore) link(oldRelative, newRelative string) error {
	oldClean, err := cleanRelativePath(oldRelative)
	if err != nil {
		return err
	}
	newClean, err := cleanRelativePath(newRelative)
	if err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(oldClean); err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(filepath.Dir(newClean)); err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(newClean); err != nil {
		return err
	}
	return store.rootFS.Link(oldClean, newClean)
}

func (store *TargetStore) remove(relative string) error {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return err
	}
	return store.rootFS.Remove(clean)
}

func (store *TargetStore) removeAll(relative string) error {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return err
	}
	if err := store.rejectSymlinkComponents(clean); err != nil {
		return err
	}
	return store.rootFS.RemoveAll(clean)
}

func (store *TargetStore) writePrivateFile(relative string, content []byte) error {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return err
	}
	if err := store.mkdirAll(filepath.Dir(clean)); err != nil {
		return err
	}
	file, temporary, err := store.createTemp(filepath.Dir(clean), "."+filepath.Base(clean)+".tmp-")
	if err != nil {
		return err
	}
	defer func() { _ = store.remove(temporary) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return store.link(temporary, clean)
}

func (store *TargetStore) verifyBlob(identity string, expectedSize ...int64) error {
	return store.verifyBlobAt(store.blobRelativePath(identity), identity, expectedSize...)
}

func (store *TargetStore) verifyBlobAt(relativePath, identity string, expectedSize ...int64) error {
	source, err := store.open(relativePath)
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
	if len(expectedSize) > 0 && info.Size() != expectedSize[0] {
		return fmt.Errorf("project source blob size does not match manifest")
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

func (store *TargetStore) copyRetainedBlob(targetPath, identity string, expectedSize ...int64) (int64, error) {
	sourcePath := store.blobRelativePath(identity)
	if err := store.verifyBlobAt(sourcePath, identity, expectedSize...); err != nil {
		return 0, err
	}
	source, err := store.open(sourcePath)
	if err != nil {
		return 0, err
	}
	defer source.Close()
	target, err := store.openFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
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

func (store *TargetStore) storedSnapshot(
	request SynchronizationPlanRequest,
	directory, projectDigest string,
) (StoredSnapshot, error) {
	projectPath, err := store.absolutePath(filepath.Join(directory, "source", filepath.FromSlash(request.ProjectFile)))
	if err != nil {
		return StoredSnapshot{}, err
	}
	artifactPath, err := store.absolutePath(filepath.Join(directory, targetProjectArtifact))
	if err != nil {
		return StoredSnapshot{}, err
	}
	return StoredSnapshot{
		ProjectID: request.ProjectID, Digest: request.ArtifactDigest,
		SourceAttestationDigest: sourceAttestationDigest(request),
		ProjectPath:             projectPath,
		ProjectDigest:           projectDigest, ProjectArtifactPath: artifactPath,
		SourceRevision: cloneSourceRevision(request.SourceRevision),
	}, nil
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
