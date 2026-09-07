package devloop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const stableCaptureAttempts = 3

type FilesystemBuilder struct {
	// SourceRoot is the directory containing the conventional authoring
	// directories. Project identity is deliberately supplied by the target
	// profile, never read from source files.
	SourceRoot     string
	ProjectID      projectgraph.ResourceID
	SourceRevision *SourceRevision
	CandidateKey   string
}

func (builder FilesystemBuilder) Build(ctx context.Context) (Snapshot, error) {
	sourceRoot := strings.TrimSpace(builder.SourceRoot)
	if sourceRoot == "" {
		return Snapshot{}, fmt.Errorf("analytics source root is required")
	}
	sourceRoot, err := filepath.Abs(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	info, err := os.Stat(sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	if !info.IsDir() {
		return Snapshot{}, fmt.Errorf("Project authoring was removed; pass the analytics source root directory %q instead of %q", filepath.Dir(sourceRoot), sourceRoot)
	}
	sourceRoot, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve analytics source root: %w", err)
	}
	files, err := captureStableProjectSources(ctx, sourceRoot)
	if err != nil {
		return Snapshot{}, err
	}
	root, err := os.MkdirTemp("", "leapview-devloop-*")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(root)
	artifacts := make([]Artifact, 0, len(files))
	for path, content := range files {
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return Snapshot{}, err
		}
		target := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return Snapshot{}, err
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return Snapshot{}, err
		}
		artifacts = append(artifacts, contentArtifact(filepath.ToSlash(relative), content))
	}
	if _, err := projectcompiler.Compile(root); err != nil {
		return Snapshot{}, err
	}
	// The source bundle validates source-root resources and intentionally has
	// no Project identity. The target-bound identity remains on the snapshot so
	// remote synchronization is scoped to the authenticated target Project.
	projectID := builder.ProjectID
	return normalizeSnapshot(Snapshot{
		ProjectID: projectID,
		Digest:    candidateSetDigest(artifacts), Artifacts: artifacts,
		SourceRevision: builder.SourceRevision,
		CandidateKey:   builder.CandidateKey,
	})
}

func captureStableProjectSources(ctx context.Context, projectPath string) (map[string][]byte, error) {
	var previous map[string][]byte
	for attempt := 0; attempt < stableCaptureAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, err := readReachableProjectSources(projectPath)
		if err != nil {
			return nil, err
		}
		if previous != nil && equalSourceSets(previous, current) {
			return current, nil
		}
		previous = current
	}
	return nil, fmt.Errorf("project sources changed during coherent capture")
}

func readReachableProjectSources(projectPath string) (map[string][]byte, error) {
	paths, err := projectcompiler.SourceFiles(projectPath)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files[path] = content
	}
	return files, nil
}

func equalSourceSets(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for path, content := range left {
		other, ok := right[path]
		if !ok || !bytes.Equal(content, other) {
			return false
		}
	}
	return true
}

func contentArtifact(path string, content []byte) Artifact {
	sum := sha256.Sum256(content)
	return Artifact{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:]), SizeBytes: int64(len(content)), Content: append([]byte(nil), content...)}
}

func candidateSetDigest(artifacts []Artifact) string {
	ordered := append([]Artifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	hash := sha256.New()
	for _, artifact := range ordered {
		_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:%d:", len(artifact.Path), artifact.Path, len(artifact.Digest), artifact.Digest, artifact.SizeBytes)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
