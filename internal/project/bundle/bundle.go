// Package bundle owns the deterministic source-and-compiled project bundle.
// A bundle contains one compiled/project.json and no target selector. Serving
// environment and generation identity are introduced by deployment (LEA-374).
package bundle

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

const (
	BundleFormat           = "tar.gz"
	ProjectFile            = "leapview.yaml"
	CompiledProjectFile    = "compiled/project.json"
	projectBundleVersion   = 1
	compiledProjectVersion = 2
	projectAPIVersion      = "leapview.dev/v1"

	// MaxBundleBytes bounds the complete compressed tar.gz artifact.  It is
	// deliberately aligned with the project artifact storage limit so a
	// reader cannot admit a value that the release/object stores cannot retain.
	MaxBundleBytes int64 = 64 << 20
	// MaxBundleUncompressedBytes bounds the sum of regular-file payloads in a
	// bundle.  Keeping a separate expanded limit prevents small compressed
	// archives from becoming unbounded memory allocations during validation.
	MaxBundleUncompressedBytes int64 = 128 << 20
	// MaxBundleFileBytes bounds one regular-file payload.
	MaxBundleFileBytes int64 = 64 << 20
	// MaxBundleFiles bounds the number of regular-file entries, including the
	// generated manifest.json entry.
	MaxBundleFiles = 10_000
)

// Validation is the project-level projection consumed by deployment and
// runtime adapters. It deliberately has no target/environment selector.
type Validation struct {
	Digest        string
	ManifestJSON  string
	RootDir       string
	ProjectID     string
	ProjectDigest string
	Graph         projectgraph.ProjectGraph
	Manifest      projectmanifest.Project
}

// Manifest is the deterministic bundle index. Files lists authored source
// files; the compiled path is a generated project artifact and is validated
// separately by CompiledSHA256.
type Manifest struct {
	Version        int            `json:"version"`
	ProjectID      string         `json:"projectId"`
	ProjectDigest  string         `json:"projectDigest"`
	GraphDigest    string         `json:"graphDigest"`
	CatalogPath    string         `json:"catalogPath"`
	CompiledPath   string         `json:"compiledPath"`
	CompiledSHA256 string         `json:"compiledSha256"`
	Files          []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Plan is the compiler's canonical project-wide plan contract. Keeping this
// alias avoids a second bundle-specific plan schema drifting from compiler.
type Plan = projectcompiler.ProjectPlan

// CompiledProjectArtifact is the generated project contract retained in
// compiled/project.json. It contains one graph, one project manifest, and one
// project plan. Environment and generation are intentionally absent.
type CompiledProjectArtifact struct {
	Version       int                               `json:"version"`
	ProjectID     projectgraph.ResourceID           `json:"projectId"`
	ProjectDigest string                            `json:"projectDigest"`
	GraphDigest   string                            `json:"graphDigest"`
	Validation    CompiledArtifactValidation        `json:"validation"`
	Manifest      projectmanifest.Project           `json:"manifest"`
	Runtime       projectartifact.RuntimeProjection `json:"runtime"`
	Graph         projectgraph.ProjectGraph         `json:"graph"`
	Plan          Plan                              `json:"plan"`
}

type CompiledArtifactValidation struct {
	Status        string                       `json:"status"`
	Diagnostics   []CompiledArtifactDiagnostic `json:"diagnostics,omitempty"`
	SchemaVersion string                       `json:"schemaVersion"`
}

type CompiledArtifactDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// PackProjectOptions is the explicit compiler-to-bundle seam. LEA-372's
// compiler supplies Project and Plan. SourceRoot defaults to the authored
// project path's directory; SourceFiles can be used by an export adapter when
// the checkout is unavailable.
type PackProjectOptions struct {
	Project     projectartifact.Project
	Plan        projectcompiler.ProjectPlan
	SourceRoot  string
	SourceFiles map[string][]byte
}

// PackProject writes authored source and one generated compiled/project.json.
// The supplied project is already compiled and immutable; this package never
// recompiles or selects a target.
func PackProject(projectPath string, options PackProjectOptions, out io.Writer) (Manifest, string, error) {
	if out == nil {
		return Manifest{}, "", errors.New("bundle output is required")
	}
	if options.Project.ProjectID() == "" {
		return Manifest{}, "", errors.New("compiled project artifact is required")
	}
	absoluteProjectPath, err := filepath.Abs(projectPath)
	if err != nil {
		return Manifest{}, "", err
	}
	root := options.SourceRoot
	if root == "" {
		root = filepath.Dir(absoluteProjectPath)
	} else if root, err = filepath.Abs(root); err != nil {
		return Manifest{}, "", err
	}
	if _, err := relativeBundlePath(root, absoluteProjectPath); err != nil {
		return Manifest{}, "", err
	}
	sources := options.SourceFiles
	if sources == nil {
		files, err := collectProjectBundleFiles(root, absoluteProjectPath, options.Project)
		if err != nil {
			return Manifest{}, "", err
		}
		sources, err = readSourceFiles(root, absoluteProjectPath, files)
		if err != nil {
			return Manifest{}, "", err
		}
	} else {
		sources, err = validateSuppliedSourceFiles(root, absoluteProjectPath, sources, options.Project)
		if err != nil {
			return Manifest{}, "", err
		}
	}
	compiled, err := compiledProject(options.Project, options.Plan)
	if err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := newManifest(options.Project, compiledBytes, sources)
	return writeBundleBytes(sources, map[string][]byte{CompiledProjectFile: compiledBytes}, manifest, out)
}

// PackCompiledProject writes only the generated project artifact and manifest.
// It is useful when source bytes were retained separately by a release store.
func PackCompiledProject(project projectartifact.Project, plan projectcompiler.ProjectPlan, out io.Writer) (Manifest, string, error) {
	if out == nil || project.ProjectID() == "" {
		return Manifest{}, "", errors.New("compiled project artifact and output are required")
	}
	compiled, err := compiledProject(project, plan)
	if err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := newManifest(project, compiledBytes, nil)
	return writeBundleBytes(nil, map[string][]byte{CompiledProjectFile: compiledBytes}, manifest, out)
}

func compiledProject(project projectartifact.Project, plan projectcompiler.ProjectPlan) (CompiledProjectArtifact, error) {
	graph := project.Graph()
	if err := graph.Validate(); err != nil {
		return CompiledProjectArtifact{}, err
	}
	if err := validatePlan(plan, graph, project.Manifest()); err != nil {
		return CompiledProjectArtifact{}, err
	}
	compiled := CompiledProjectArtifact{
		Version:   compiledProjectVersion,
		ProjectID: graph.ProjectID(), ProjectDigest: project.Digest(), GraphDigest: graph.Digest(),
		Validation: CompiledArtifactValidation{Status: "passed", SchemaVersion: projectAPIVersion},
		Manifest:   project.Manifest(), Runtime: project.RuntimeProjection(), Graph: graph, Plan: plan,
	}
	if err := ValidateCompiledProjectArtifact(compiled); err != nil {
		return CompiledProjectArtifact{}, err
	}
	return compiled, nil
}

func validatePlan(plan projectcompiler.ProjectPlan, graph projectgraph.ProjectGraph, manifest projectmanifest.Project) error {
	if plan.Project != graph.ProjectID().String() {
		return fmt.Errorf("project plan identity = %q, graph = %q", plan.Project, graph.ProjectID())
	}
	expected := map[string][]string{
		"connections":    resourceIDsByKind(graph, projectgraph.KindConnection),
		"sources":        resourceIDsByKind(graph, projectgraph.KindSource),
		"models":         resourceIDsByKind(graph, projectgraph.KindModel),
		"semanticModels": resourceIDsByKind(graph, projectgraph.KindSemanticModel),
		"pipelines":      resourceIDsByKind(graph, projectgraph.KindPipeline),
		"dashboards":     resourceIDsByKind(graph, projectgraph.KindDashboard),
		"groups":         accessIDs(manifest.Access.Groups),
		"roleBindings":   accessIDs(manifest.Access.RoleBindings),
		"grants":         accessIDs(manifest.Access.Grants),
		"dataPolicies":   accessIDs(manifest.Access.DataPolicies),
	}
	actual := map[string][]string{
		"connections": plan.Connections, "sources": plan.Sources, "models": plan.Models,
		"semanticModels": plan.SemanticModels, "pipelines": plan.Pipelines,
		"dashboards": plan.Dashboards, "groups": plan.Groups, "roleBindings": plan.RoleBindings,
		"grants": plan.Grants, "dataPolicies": plan.DataPolicies,
	}
	for kind, want := range expected {
		if !equalStringSlices(actual[kind], want) {
			return fmt.Errorf("project plan %s = %v, want graph resources %v", kind, actual[kind], want)
		}
	}
	for index, change := range plan.Changes {
		if !projectgraph.ResourceID(change.ID).Valid() || strings.TrimSpace(change.Action) == "" {
			return fmt.Errorf("project plan change %d has invalid identity", index)
		}
	}
	for index, change := range plan.DependencyChanges {
		if !projectgraph.ResourceID(change.From).Valid() || !projectgraph.ResourceID(change.To).Valid() || strings.TrimSpace(change.Action) == "" {
			return fmt.Errorf("project plan dependency change %d has invalid identity", index)
		}
	}
	return nil
}

func resourceIDsByKind(graph projectgraph.ProjectGraph, kind projectgraph.Kind) []string {
	ids := make([]string, 0)
	for _, resource := range graph.Resources() {
		if resource.Kind == kind {
			ids = append(ids, resource.ID.String())
		}
	}
	sort.Strings(ids)
	return ids
}

func accessIDs[T any](values map[string]T) []string {
	ids := make([]string, 0, len(values))
	for _, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			panic(fmt.Sprintf("encode access resource: %v", err))
		}
		var wire struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			panic(fmt.Sprintf("decode access resource: %v", err))
		}
		if strings.TrimSpace(wire.ID) != "" {
			ids = append(ids, wire.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func newManifest(project projectartifact.Project, compiled []byte, sourceFiles map[string][]byte) Manifest {
	graph := project.Graph()
	catalogPath := CompiledProjectFile
	for path := range sourceFiles {
		if clean, err := safeBundlePath(path); err == nil && clean == ProjectFile {
			catalogPath = ProjectFile
			break
		}
	}
	manifest := Manifest{
		Version: projectBundleVersion, ProjectID: graph.ProjectID().String(), ProjectDigest: project.Digest(),
		GraphDigest: graph.Digest(), CatalogPath: catalogPath, CompiledPath: CompiledProjectFile,
		CompiledSHA256: digestBytes(compiled), Files: make([]ManifestFile, 0, len(sourceFiles)),
	}
	return manifest
}

func readSourceFiles(root, projectPath string, files []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(files))
	projectRel, err := relativeBundlePath(root, projectPath)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		rel, err = safeBundlePath(rel)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if rel == projectRel {
			path = projectPath
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.IsDir() {
			return nil, fmt.Errorf("bundle path %s is a directory", rel)
		}
		bytes, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		result[rel] = bytes
	}
	return result, nil
}

func collectProjectBundleFiles(baseDir, projectPath string, project projectartifact.Project) ([]string, error) {
	relProject, err := relativeBundlePath(baseDir, projectPath)
	if err != nil {
		return nil, err
	}
	paths := map[string]struct{}{relProject: {}}
	manifest := project.Manifest()
	graph := project.Graph()
	if err := validateResourceFiles(project); err != nil {
		return nil, err
	}
	for resourceID, path := range manifest.ResourceFiles {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("manifest resource file %q has an empty path", resourceID)
		}
		cleanPath, err := safeBundlePath(path)
		if err != nil {
			return nil, fmt.Errorf("manifest resource file %q: %w", path, err)
		}
		if resourceID == graph.ProjectID().String() && cleanPath != relProject {
			return nil, fmt.Errorf("manifest project resource path = %q, want %q", cleanPath, relProject)
		}
		if err := addAuthoredPath(paths, baseDir, path); err != nil {
			return nil, fmt.Errorf("manifest resource file %q: %w", path, err)
		}
	}
	for _, resource := range graph.Resources() {
		if path := strings.TrimSpace(resource.Provenance.Path); path != "" {
			if err := addAuthoredPath(paths, baseDir, path); err != nil {
				return nil, fmt.Errorf("graph resource %s provenance %q: %w", resource.ID, path, err)
			}
		}
	}
	files := make([]string, 0, len(paths))
	for path := range paths {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func validateSuppliedSourceFiles(baseDir, projectPath string, sourceFiles map[string][]byte, project projectartifact.Project) (map[string][]byte, error) {
	expected, err := collectProjectBundleFiles(baseDir, projectPath, project)
	if err != nil {
		return nil, err
	}
	want := make(map[string]struct{}, len(expected))
	for _, path := range expected {
		want[path] = struct{}{}
	}
	result := make(map[string][]byte, len(sourceFiles))
	for path, content := range sourceFiles {
		clean, err := safeBundlePath(path)
		if err != nil {
			return nil, fmt.Errorf("source file %q: %w", path, err)
		}
		if _, exists := result[clean]; exists {
			return nil, fmt.Errorf("source file %q is duplicated", clean)
		}
		if _, ok := want[clean]; !ok {
			return nil, fmt.Errorf("source file %q is not an expected project resource", clean)
		}
		result[clean] = append([]byte(nil), content...)
	}
	for path := range want {
		if _, ok := result[path]; !ok {
			return nil, fmt.Errorf("source file %q is missing", path)
		}
	}
	return result, nil
}

func validateResourceFiles(project projectartifact.Project) error {
	graph := project.Graph()
	manifest := project.Manifest()
	expected := map[string]struct{}{graph.ProjectID().String(): {}}
	for _, resource := range graph.Resources() {
		expected[resource.ID.String()] = struct{}{}
	}
	for _, id := range manifest.NameIndex.Publications {
		if strings.TrimSpace(id) != "" {
			expected[id] = struct{}{}
		}
	}
	for id := range manifest.Publications {
		if strings.TrimSpace(id) != "" {
			expected[id] = struct{}{}
		}
	}
	for _, id := range accessIDs(manifest.Access.Groups) {
		expected[id] = struct{}{}
	}
	for _, id := range accessIDs(manifest.Access.RoleBindings) {
		expected[id] = struct{}{}
	}
	for _, id := range accessIDs(manifest.Access.Grants) {
		expected[id] = struct{}{}
	}
	for _, id := range accessIDs(manifest.Access.DataPolicies) {
		expected[id] = struct{}{}
	}
	actual := make(map[string]struct{}, len(manifest.ResourceFiles))
	for id := range manifest.ResourceFiles {
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("manifest resource file key %q is not a project resource", id)
		}
		actual[id] = struct{}{}
	}
	for id := range expected {
		if _, ok := actual[id]; !ok {
			return fmt.Errorf("manifest resource file key %q is missing", id)
		}
	}
	return nil
}

func addAuthoredPath(paths map[string]struct{}, baseDir, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if filepath.IsAbs(value) {
		return fmt.Errorf("authored path %q must be relative", value)
	}
	clean, err := safeBundlePath(value)
	if err != nil {
		return err
	}
	paths[clean] = struct{}{}
	return nil
}

func writeBundleBytes(sourceFiles, generatedFiles map[string][]byte, manifest Manifest, out io.Writer) (Manifest, string, error) {
	if len(sourceFiles)+len(generatedFiles)+1 > MaxBundleFiles {
		return Manifest{}, "", fmt.Errorf("bundle file count exceeds limit %d", MaxBundleFiles)
	}
	var uncompressedBytes int64
	for path, content := range sourceFiles {
		if int64(len(content)) > MaxBundleFileBytes {
			return Manifest{}, "", fmt.Errorf("bundle file %q exceeds maximum file size %d", path, MaxBundleFileBytes)
		}
		if int64(len(content)) > MaxBundleUncompressedBytes-uncompressedBytes {
			return Manifest{}, "", fmt.Errorf("bundle uncompressed size exceeds limit %d", MaxBundleUncompressedBytes)
		}
		uncompressedBytes += int64(len(content))
	}
	for path, content := range generatedFiles {
		if int64(len(content)) > MaxBundleFileBytes {
			return Manifest{}, "", fmt.Errorf("bundle file %q exceeds maximum file size %d", path, MaxBundleFileBytes)
		}
		if int64(len(content)) > MaxBundleUncompressedBytes-uncompressedBytes {
			return Manifest{}, "", fmt.Errorf("bundle uncompressed size exceeds limit %d", MaxBundleUncompressedBytes)
		}
		uncompressedBytes += int64(len(content))
	}
	hash := sha256.New()
	limitedOut := &bundleSizeWriter{out: io.MultiWriter(out, hash)}
	gz := gzip.NewWriter(limitedOut)
	tw := tar.NewWriter(gz)
	seen := map[string]struct{}{}
	sourcePaths := sortedKeys(sourceFiles)
	for _, authoredPath := range sourcePaths {
		rel, err := safeBundlePath(authoredPath)
		if err != nil {
			return Manifest{}, "", err
		}
		if _, ok := seen[rel]; ok {
			return Manifest{}, "", fmt.Errorf("bundle source path %s is duplicated", rel)
		}
		seen[rel] = struct{}{}
		content := sourceFiles[authoredPath]
		sum := sha256.Sum256(content)
		manifest.Files = append(manifest.Files, ManifestFile{Path: rel, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content))})
		if err := writeTarFile(tw, rel, content); err != nil {
			return Manifest{}, "", err
		}
	}
	for _, authoredPath := range sortedKeys(generatedFiles) {
		rel, err := safeBundlePath(authoredPath)
		if err != nil {
			return Manifest{}, "", err
		}
		if _, ok := seen[rel]; ok {
			return Manifest{}, "", fmt.Errorf("bundle generated path %s duplicates source file", rel)
		}
		seen[rel] = struct{}{}
		if err := writeTarFile(tw, rel, generatedFiles[authoredPath]); err != nil {
			return Manifest{}, "", err
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	if int64(len(manifestBytes)) > MaxBundleFileBytes {
		return Manifest{}, "", fmt.Errorf("bundle file %q exceeds maximum file size %d", "manifest.json", MaxBundleFileBytes)
	}
	if int64(len(manifestBytes)) > MaxBundleUncompressedBytes-uncompressedBytes {
		return Manifest{}, "", fmt.Errorf("bundle uncompressed size exceeds limit %d", MaxBundleUncompressedBytes)
	}
	if _, ok := seen["manifest.json"]; ok {
		return Manifest{}, "", errors.New("bundle generated path manifest.json duplicates an existing file")
	}
	seen["manifest.json"] = struct{}{}
	if err := writeTarFile(tw, "manifest.json", manifestBytes); err != nil {
		return Manifest{}, "", err
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, "", err
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, "", err
	}
	return manifest, "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type bundleSizeWriter struct {
	out io.Writer
	n   int64
}

func (w *bundleSizeWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > MaxBundleBytes-w.n {
		return 0, fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	n, err := w.out.Write(p)
	w.n += int64(n)
	return n, err
}

func writeTarFile(tw *tar.Writer, name string, content []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

// ValidateArtifact extracts and validates one project bundle.
func ValidateArtifact(path string) (Validation, error) {
	digestValue, err := fileDigest(path)
	if err != nil {
		return Validation{}, err
	}
	root, err := os.MkdirTemp("", "leapview-deploy-project-*")
	if err != nil {
		return Validation{}, err
	}
	if err := ExtractArtifact(path, root); err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	manifest, err := readManifest(root)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	if _, err := validateManifestFiles(root, manifest); err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	compiled, err := readCompiledProjectArtifact(root, manifest)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	return Validation{Digest: digestValue, ManifestJSON: string(manifestJSON), RootDir: root,
		ProjectID: compiled.ProjectID.String(), ProjectDigest: compiled.ProjectDigest,
		Graph: compiled.Graph, Manifest: compiled.Manifest}, nil
}

// ValidateArtifactReader validates one bounded project bundle directly from a
// reader. expectedSize is the exact compressed artifact size supplied by the
// object-store metadata and must be non-negative. Unlike ValidateArtifact,
// this function never creates a temporary directory, and therefore returns an
// empty Validation.RootDir. The returned compiled artifact is the exact
// generated project contract admitted by the bundle.
func ValidateArtifactReader(reader io.Reader, expectedSize int64) (Validation, CompiledProjectArtifact, error) {
	if reader == nil {
		return Validation{}, CompiledProjectArtifact{}, errors.New("bundle reader is required")
	}
	if expectedSize < 0 {
		return Validation{}, CompiledProjectArtifact{}, errors.New("bundle expected size must be non-negative")
	}
	if expectedSize > MaxBundleBytes {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size %d exceeds limit %d", expectedSize, MaxBundleBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxBundleBytes+1))
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("read bundle: %w", err)
	}
	if int64(len(data)) > MaxBundleBytes {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	if int64(len(data)) != expectedSize {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size = %d, want %d", len(data), expectedSize)
	}
	return validateArtifactBytes(data)
}

// ValidateArtifactBytes validates one bounded project bundle from immutable
// in-memory bytes. It is equivalent to ValidateArtifactReader with an exact
// expected size of len(data), and never writes or extracts files.
func ValidateArtifactBytes(data []byte) (Validation, CompiledProjectArtifact, error) {
	if int64(len(data)) > MaxBundleBytes {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	return validateArtifactBytes(data)
}

func validateArtifactBytes(data []byte) (Validation, CompiledProjectArtifact, error) {
	entries, err := readBundleEntries(bytes.NewReader(data))
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	manifestData, ok := entries["manifest.json"]
	if !ok {
		return Validation{}, CompiledProjectArtifact{}, errors.New("bundle manifest.json is missing")
	}
	manifest, err := decodeManifest(manifestData)
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	if _, err := validateManifestEntries(entries, manifest); err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	compiledData, ok := entries[CompiledProjectFile]
	if !ok {
		return Validation{}, CompiledProjectArtifact{}, errors.New("compiled project artifact is missing")
	}
	compiled, err := decodeCompiledProjectArtifact(compiledData, manifest)
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	digestValue := digestBytesPrefixed(data)
	return Validation{Digest: digestValue, ManifestJSON: string(manifestJSON),
		ProjectID: compiled.ProjectID.String(), ProjectDigest: compiled.ProjectDigest,
		Graph: compiled.Graph, Manifest: compiled.Manifest}, compiled, nil
}

func readBundleEntries(data io.Reader) (map[string][]byte, error) {
	if data == nil {
		return nil, errors.New("bundle reader is required")
	}
	// Buffer the bounded compressed object once. This gives the reader and byte
	// APIs identical digest input and lets us reject bytes after the gzip member.
	raw, err := io.ReadAll(io.LimitReader(data, MaxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read bundle: %w", err)
	}
	if int64(len(raw)) > MaxBundleBytes {
		return nil, fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	compressed := bufio.NewReader(bytes.NewReader(raw))
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, fmt.Errorf("decode bundle gzip: %w", err)
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	entries := make(map[string][]byte)
	var expanded int64
	for count := 0; ; count++ {
		header, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			_ = gz.Close()
			return nil, fmt.Errorf("decode bundle tar: %w", nextErr)
		}
		if count >= MaxBundleFiles {
			_ = gz.Close()
			return nil, fmt.Errorf("bundle file count exceeds limit %d", MaxBundleFiles)
		}
		if header.Format == tar.FormatGNU || header.PAXRecords["path"] != "" || header.PAXRecords["linkpath"] != "" {
			_ = gz.Close()
			return nil, fmt.Errorf("unsupported extended bundle path %q", header.Name)
		}
		rel, pathErr := safeBundlePath(header.Name)
		if pathErr != nil {
			_ = gz.Close()
			return nil, pathErr
		}
		if _, exists := entries[rel]; exists {
			_ = gz.Close()
			return nil, fmt.Errorf("duplicate bundle entry %q", rel)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gz.Close()
			return nil, fmt.Errorf("unsupported bundle entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > MaxBundleFileBytes {
			_ = gz.Close()
			return nil, fmt.Errorf("bundle file %q exceeds maximum file size %d", rel, MaxBundleFileBytes)
		}
		if header.Size > MaxBundleUncompressedBytes-expanded {
			_ = gz.Close()
			return nil, fmt.Errorf("bundle uncompressed size exceeds limit %d", MaxBundleUncompressedBytes)
		}
		content, readErr := io.ReadAll(io.LimitReader(tr, MaxBundleFileBytes+1))
		if readErr != nil {
			_ = gz.Close()
			return nil, fmt.Errorf("read bundle file %q: %w", rel, readErr)
		}
		if int64(len(content)) != header.Size {
			_ = gz.Close()
			return nil, fmt.Errorf("bundle file %q size = %d, want %d", rel, len(content), header.Size)
		}
		expanded += int64(len(content))
		entries[rel] = content
	}
	// tar.Reader stops at its two zero blocks. Consume the decompressed stream
	// to distinguish a valid end-of-archive from trailing tar bytes.
	trailingTar, tailErr := io.ReadAll(gz)
	if tailErr != nil {
		_ = gz.Close()
		return nil, fmt.Errorf("decode bundle trailing data: %w", tailErr)
	}
	if len(trailingTar) != 0 {
		_ = gz.Close()
		return nil, errors.New("bundle archive contains trailing tar data")
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("close bundle gzip: %w", err)
	}
	// Multistream(false) leaves bytes after the first gzip member in the
	// buffered source. Any such bytes are trailing archive data, including a
	// second valid gzip member.
	trailingGzip, err := io.ReadAll(compressed)
	if err != nil {
		return nil, fmt.Errorf("read bundle trailing bytes: %w", err)
	}
	if len(trailingGzip) != 0 {
		return nil, errors.New("bundle archive contains trailing gzip data")
	}
	return entries, nil
}

func ValidateCompiledProjectArtifact(compiled CompiledProjectArtifact) error {
	if compiled.Version != compiledProjectVersion {
		return fmt.Errorf("compiled project artifact version = %d, want %d", compiled.Version, compiledProjectVersion)
	}
	if err := compiled.Graph.Validate(); err != nil {
		return fmt.Errorf("compiled project graph: %w", err)
	}
	if compiled.ProjectID != compiled.Graph.ProjectID() || compiled.Manifest.ID != compiled.ProjectID.String() {
		return fmt.Errorf("compiled project identity does not match graph project id %q", compiled.ProjectID)
	}
	if err := digest.ValidateSHA256Identity(compiled.ProjectDigest); err != nil {
		return fmt.Errorf("compiled project digest must be a canonical SHA-256 digest: %w", err)
	}
	if compiled.GraphDigest != compiled.Graph.Digest() {
		return fmt.Errorf("compiled graph digest = %q, want %q", compiled.GraphDigest, compiled.Graph.Digest())
	}
	manifest := compiled.Manifest
	if err := projectartifact.RestoreRuntimeProjection(&manifest, compiled.Runtime); err != nil {
		return fmt.Errorf("compiled project runtime projection: %w", err)
	}
	reconstructed, err := projectartifact.NewProject(compiled.Graph, manifest)
	if err != nil {
		return fmt.Errorf("compiled project manifest: %w", err)
	}
	if reconstructed.Digest() != compiled.ProjectDigest {
		return fmt.Errorf("compiled project digest = %q, reconstructed manifest digest = %q", compiled.ProjectDigest, reconstructed.Digest())
	}
	if compiled.Validation.Status != "passed" || compiled.Validation.SchemaVersion != projectAPIVersion {
		return fmt.Errorf("compiled project validation must be passed %s", projectAPIVersion)
	}
	if err := validatePlan(compiled.Plan, compiled.Graph, compiled.Manifest); err != nil {
		return err
	}
	return nil
}

func readManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	return decodeManifest(data)
}

func decodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, fmt.Errorf("decode bundle manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Manifest{}, errors.New("bundle manifest contains trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return Manifest{}, fmt.Errorf("bundle manifest trailing data: %w", err)
	}
	if manifest.Version != projectBundleVersion {
		return Manifest{}, fmt.Errorf("unsupported bundle manifest version %d", manifest.Version)
	}
	if manifest.CatalogPath != ProjectFile && manifest.CatalogPath != CompiledProjectFile {
		return Manifest{}, fmt.Errorf("catalog path = %q, want %q or %q", manifest.CatalogPath, ProjectFile, CompiledProjectFile)
	}
	if manifest.CompiledPath != CompiledProjectFile {
		return Manifest{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledProjectFile)
	}
	return manifest, nil
}

func LoadCompiledProjectArtifact(root string) (CompiledProjectArtifact, Manifest, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return CompiledProjectArtifact{}, Manifest{}, err
	}
	compiled, err := readCompiledProjectArtifact(root, manifest)
	if err != nil {
		return CompiledProjectArtifact{}, Manifest{}, err
	}
	return compiled, manifest, nil
}

func readCompiledProjectArtifact(root string, manifest Manifest) (CompiledProjectArtifact, error) {
	if manifest.CompiledPath != CompiledProjectFile {
		return CompiledProjectArtifact{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledProjectFile)
	}
	data, err := os.ReadFile(filepath.Join(root, CompiledProjectFile))
	if err != nil {
		return CompiledProjectArtifact{}, err
	}
	if manifest.CompiledSHA256 != digestBytes(data) {
		return CompiledProjectArtifact{}, errors.New("compiled project artifact digest mismatch")
	}
	return decodeCompiledProjectArtifact(data, manifest)
}

func decodeCompiledProjectArtifact(data []byte, manifest Manifest) (CompiledProjectArtifact, error) {
	if manifest.CompiledPath != CompiledProjectFile {
		return CompiledProjectArtifact{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledProjectFile)
	}
	if manifest.CompiledSHA256 != digestBytes(data) {
		return CompiledProjectArtifact{}, errors.New("compiled project artifact digest mismatch")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("decode compiled project artifact: %w", err)
	}
	var compiled CompiledProjectArtifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&compiled); err != nil {
		return CompiledProjectArtifact{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return CompiledProjectArtifact{}, errors.New("compiled project artifact contains trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return CompiledProjectArtifact{}, fmt.Errorf("compiled project artifact trailing data: %w", err)
	}
	if err := ValidateCompiledProjectArtifact(compiled); err != nil {
		return CompiledProjectArtifact{}, err
	}
	if err := projectartifact.RestoreRuntimeProjection(&compiled.Manifest, compiled.Runtime); err != nil {
		return CompiledProjectArtifact{}, fmt.Errorf("compiled project runtime projection: %w", err)
	}
	if compiled.ProjectID.String() != manifest.ProjectID || compiled.ProjectDigest != manifest.ProjectDigest || compiled.GraphDigest != manifest.GraphDigest {
		return CompiledProjectArtifact{}, errors.New("compiled project identity does not match bundle manifest")
	}
	return compiled, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decodeUniqueJSON(decoder, &value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("trailing JSON value")
	} else if err != io.EOF {
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func decodeUniqueJSON(decoder *json.Decoder, target *any) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				canonicalKey := strings.ToLower(key)
				if _, exists := seen[canonicalKey]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[canonicalKey] = struct{}{}
				var child any
				if err := decodeUniqueJSON(decoder, &child); err != nil {
					return err
				}
				object[key] = child
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			*target = object
		case '[':
			array := []any{}
			for decoder.More() {
				var child any
				if err := decodeUniqueJSON(decoder, &child); err != nil {
					return err
				}
				array = append(array, child)
			}
			if _, err := decoder.Token(); err != nil {
				return err
			}
			*target = array
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
		return nil
	}
	*target = token
	return nil
}

func validateManifestFiles(root string, manifest Manifest) (string, error) {
	catalogRel, err := safeBundlePath(manifest.CatalogPath)
	if err != nil {
		return "", fmt.Errorf("invalid catalog path: %w", err)
	}
	compiledRel, err := safeBundlePath(manifest.CompiledPath)
	if err != nil {
		return "", fmt.Errorf("invalid compiled path: %w", err)
	}
	if catalogRel != ProjectFile && catalogRel != CompiledProjectFile {
		return "", fmt.Errorf("catalog path = %q, want %q or %q", manifest.CatalogPath, ProjectFile, CompiledProjectFile)
	}
	if compiledRel != CompiledProjectFile {
		return "", fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledProjectFile)
	}
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"manifest.json": {}, compiledRel: {}}
	hasCatalog := catalogRel == compiledRel
	for _, file := range manifest.Files {
		rel, err := safeBundlePath(file.Path)
		if err != nil {
			return "", fmt.Errorf("invalid manifest file path %q: %w", file.Path, err)
		}
		if _, ok := seen[rel]; ok {
			return "", fmt.Errorf("duplicate manifest file path %q", rel)
		}
		seen[rel] = struct{}{}
		allowed[rel] = struct{}{}
		if rel == catalogRel {
			hasCatalog = true
		}
		bytes, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != file.SHA256 || int64(len(bytes)) != file.Size {
			return "", fmt.Errorf("file %s digest or size mismatch", file.Path)
		}
	}
	if !hasCatalog {
		return "", fmt.Errorf("catalog path %q is not listed in manifest files", manifest.CatalogPath)
	}
	if err := validateNoUnlistedBundleFiles(root, allowed); err != nil {
		return "", err
	}
	return catalogRel, nil
}

func validateManifestEntries(entries map[string][]byte, manifest Manifest) (string, error) {
	catalogRel, err := safeBundlePath(manifest.CatalogPath)
	if err != nil {
		return "", fmt.Errorf("invalid catalog path: %w", err)
	}
	compiledRel, err := safeBundlePath(manifest.CompiledPath)
	if err != nil {
		return "", fmt.Errorf("invalid compiled path: %w", err)
	}
	if catalogRel != ProjectFile && catalogRel != CompiledProjectFile {
		return "", fmt.Errorf("catalog path = %q, want %q or %q", manifest.CatalogPath, ProjectFile, CompiledProjectFile)
	}
	if compiledRel != CompiledProjectFile {
		return "", fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledProjectFile)
	}
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"manifest.json": {}, compiledRel: {}}
	hasCatalog := catalogRel == compiledRel
	for _, file := range manifest.Files {
		rel, err := safeBundlePath(file.Path)
		if err != nil {
			return "", fmt.Errorf("invalid manifest file path %q: %w", file.Path, err)
		}
		if _, ok := seen[rel]; ok {
			return "", fmt.Errorf("duplicate manifest file path %q", rel)
		}
		seen[rel] = struct{}{}
		allowed[rel] = struct{}{}
		if rel == catalogRel {
			hasCatalog = true
		}
		content, ok := entries[rel]
		if !ok {
			return "", os.ErrNotExist
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != file.SHA256 || int64(len(content)) != file.Size {
			return "", fmt.Errorf("file %s digest or size mismatch", file.Path)
		}
	}
	if !hasCatalog {
		return "", fmt.Errorf("catalog path %q is not listed in manifest files", manifest.CatalogPath)
	}
	for rel := range entries {
		if _, ok := allowed[rel]; !ok {
			return "", fmt.Errorf("bundle file %q is not listed in manifest", rel)
		}
	}
	return catalogRel, nil
}

func validateNoUnlistedBundleFiles(root string, allowed map[string]struct{}) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := allowed[rel]; !ok {
			return fmt.Errorf("bundle file %q is not listed in manifest", rel)
		}
		return nil
	})
}

func ExtractArtifact(path, dest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil {
		return statErr
	} else if info.Size() > MaxBundleBytes {
		return fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	compressed := bufio.NewReader(file)
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return err
	}
	gz.Multistream(false)
	tr := tar.NewReader(gz)
	seen := map[string]struct{}{}
	var expanded int64
	for count := 0; ; count++ {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = gz.Close()
			return err
		}
		if count >= MaxBundleFiles {
			_ = gz.Close()
			return fmt.Errorf("bundle file count exceeds limit %d", MaxBundleFiles)
		}
		if header.Format == tar.FormatGNU || header.PAXRecords["path"] != "" || header.PAXRecords["linkpath"] != "" {
			_ = gz.Close()
			return fmt.Errorf("unsupported extended bundle path %q", header.Name)
		}
		rel, err := safeBundlePath(header.Name)
		if err != nil {
			_ = gz.Close()
			return err
		}
		if _, ok := seen[rel]; ok {
			_ = gz.Close()
			return fmt.Errorf("duplicate bundle entry %q", rel)
		}
		seen[rel] = struct{}{}
		target, err := secureBundleTarget(dest, rel)
		if err != nil {
			_ = gz.Close()
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			_ = gz.Close()
			return fmt.Errorf("unsupported bundle entry %q", header.Name)
		}
		if header.Size < 0 || header.Size > MaxBundleFileBytes {
			_ = gz.Close()
			return fmt.Errorf("bundle file %q exceeds maximum file size %d", rel, MaxBundleFileBytes)
		}
		if header.Size > MaxBundleUncompressedBytes-expanded {
			_ = gz.Close()
			return fmt.Errorf("bundle uncompressed size exceeds limit %d", MaxBundleUncompressedBytes)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			_ = gz.Close()
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			_ = gz.Close()
			return err
		}
		if _, err := io.CopyN(out, tr, header.Size); err != nil {
			out.Close()
			_ = gz.Close()
			return err
		}
		expanded += header.Size
		if err := out.Close(); err != nil {
			_ = gz.Close()
			return err
		}
	}
	trailingTar, err := io.ReadAll(gz)
	if err != nil {
		_ = gz.Close()
		return err
	}
	if len(trailingTar) != 0 {
		_ = gz.Close()
		return errors.New("bundle archive contains trailing tar data")
	}
	if err := gz.Close(); err != nil {
		return err
	}
	trailingGzip, err := io.ReadAll(compressed)
	if err != nil {
		return err
	}
	if len(trailingGzip) != 0 {
		return errors.New("bundle archive contains trailing gzip data")
	}
	return nil
}

func secureBundleTarget(dest, rel string) (string, error) {
	target, err := securejoin.SecureJoin(dest, rel)
	if err != nil {
		return "", fmt.Errorf("secure bundle path %q: %w", rel, err)
	}
	lexicalTarget := filepath.Join(filepath.Clean(dest), filepath.FromSlash(rel))
	if filepath.Clean(target) != filepath.Clean(lexicalTarget) {
		return "", fmt.Errorf("bundle path %q resolves through a symlink", rel)
	}
	return target, nil
}

func writeExtractedRoot(root string, out io.Writer) error {
	files := []string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(files)
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, hash))
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := writeTarFile(tw, rel, content); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func digestBytesPrefixed(value []byte) string {
	return "sha256:" + digestBytes(value)
}

func sortedKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func safeBundlePath(path string) (string, error) {
	// Tar paths are slash-separated on every platform. Rejecting backslashes
	// keeps validation independent of the host OS and prevents a bundle that is
	// benign on Unix from becoming traversal on Windows.
	if strings.Contains(path, `\`) {
		return "", fmt.Errorf("bundle path %q contains a backslash", path)
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("bundle path %q must be relative", path)
	}
	raw := filepath.ToSlash(path)
	for _, part := range strings.Split(raw, "/") {
		if part == ".." {
			return "", fmt.Errorf("bundle path %q escapes bundle root", path)
		}
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("bundle path %q is empty", path)
	}
	if clean != raw {
		return "", fmt.Errorf("bundle path %q is not canonical", path)
	}
	return clean, nil
}

func relativeBundlePath(root, path string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return safeBundlePath(filepath.ToSlash(rel))
}
