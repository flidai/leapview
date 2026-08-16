package devloop

import (
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
	ProjectID           projectgraph.ResourceID
	Digest              string
	ProjectPath         string
	ProjectDigest       string
	ProjectArtifactPath string
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
		return store.verifyStoredSnapshot(ctx, request, destination)
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
			return store.verifyStoredSnapshot(ctx, request, destination)
		}
		return StoredSnapshot{}, err
	}
	return storedSnapshot(request, destination, compiled.Digest()), nil
}

func (store *TargetStore) verifyStoredSnapshot(
	ctx context.Context,
	request SynchronizationPlanRequest,
	directory string,
) (StoredSnapshot, error) {
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
	retained, err := os.ReadFile(filepath.Join(directory, targetProjectArtifact))
	if os.IsNotExist(err) {
		if err := writeRetainedProjectArtifact(
			filepath.Join(directory, targetProjectArtifact),
			compiled.Canonical(),
		); err != nil {
			return StoredSnapshot{}, fmt.Errorf("repair retained project artifact: %w", err)
		}
		retained = compiled.Canonical()
		err = nil
	}
	if err != nil {
		return StoredSnapshot{}, err
	}
	if string(retained) != string(compiled.Canonical()) {
		return StoredSnapshot{}, fmt.Errorf("retained project artifact does not match synchronized sources")
	}
	return storedSnapshot(request, directory, compiled.Digest()), nil
}

func writeRetainedProjectArtifact(path string, content []byte) error {
	return securefs.WritePrivateFileAtomic(path, content)
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
		ProjectPath:   filepath.Join(directory, "source", filepath.FromSlash(request.ProjectFile)),
		ProjectDigest: projectDigest, ProjectArtifactPath: artifactPath,
	}
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
