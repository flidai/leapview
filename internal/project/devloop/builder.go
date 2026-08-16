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

	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const stableCaptureAttempts = 3

type FilesystemBuilder struct {
	ProjectPath    string
	SourceRevision *SourceRevision
	CandidateKey   string
}

func (builder FilesystemBuilder) Build(ctx context.Context) (Snapshot, error) {
	projectPath, err := filepath.Abs(builder.ProjectPath)
	if err != nil {
		return Snapshot{}, err
	}
	files, err := captureStableProjectSources(ctx, projectPath)
	if err != nil {
		return Snapshot{}, err
	}
	root, err := os.MkdirTemp("", "leapview-devloop-*")
	if err != nil {
		return Snapshot{}, err
	}
	defer os.RemoveAll(root)
	sourceRoot := filepath.Dir(projectPath)
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
	compiled, err := projectcompiler.Compile(filepath.Join(root, filepath.Base(projectPath)))
	if err != nil {
		return Snapshot{}, err
	}
	projectFile := filepath.ToSlash(filepath.Base(projectPath))
	projectID := compiled.ProjectID()
	return normalizeSnapshot(Snapshot{
		ProjectID: projectID, ProjectFile: projectFile,
		Digest: candidateSetDigest(projectID, projectFile, artifacts), Artifacts: artifacts,
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
	return Artifact{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:]), Content: append([]byte(nil), content...)}
}

func candidateSetDigest(projectID projectgraph.ResourceID, projectFile string, artifacts []Artifact) string {
	ordered := append([]Artifact(nil), artifacts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	hash := sha256.New()
	projectIDValue := projectID.String()
	_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:", len(projectIDValue), projectIDValue, len(projectFile), projectFile)
	for _, artifact := range ordered {
		_, _ = fmt.Fprintf(hash, "%d:%s:%d:%s:", len(artifact.Path), artifact.Path, len(artifact.Digest), artifact.Digest)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
