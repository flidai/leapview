// Package bundle owns the deterministic source-and-compiled source bundle.
// A bundle contains one compiled/source-bundle.json and no target selector. Serving
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
	"github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

const (
	BundleFormat             = "tar.gz"
	BundleContentType        = "application/gzip"
	CompiledSourceBundleFile = "compiled/source-bundle.json"
	// CompiledProjectFile is retained as the serving-loader compatibility name;
	// Project-free bundles always use CompiledSourceBundleFile on the wire.
	CompiledProjectFile         = CompiledSourceBundleFile
	sourceBundleVersion         = 2
	compiledSourceBundleVersion = 3
	sourceBundleAPIVersion      = "leapview.dev/v1"
	MaxBundleBytes              = int64(64 << 20)
	MaxBundleUncompressedBytes  = int64(128 << 20)
	MaxBundleFileBytes          = int64(64 << 20)
	MaxBundleFiles              = 10_000
)

// Validation is the source-bundle projection consumed by deployment and
// runtime adapters. It deliberately has no target/environment selector.
type Validation struct {
	Digest       string
	ManifestJSON string
	RootDir      string
	// ProjectID and ProjectDigest are empty for portable source bundles. They
	// remain available to native serving adapters while target binding is
	// introduced outside this package.
	ProjectID     string
	ProjectDigest string
	BundleDigest  string
	Graph         projectgraph.ProjectGraph
	Manifest      projectmanifest.ResourceManifest
}

// Manifest is the deterministic bundle index. Files lists authored source
// files; the compiled path is a generated source-bundle artifact and is validated
// separately by CompiledSHA256.
type Manifest struct {
	Version        int            `json:"version"`
	BundleDigest   string         `json:"bundleDigest"`
	GraphDigest    string         `json:"graphDigest"`
	CompiledPath   string         `json:"compiledPath"`
	CompiledSHA256 string         `json:"compiledSha256"`
	Files          []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// CompiledSourceBundleArtifact is the generated source bundle contract retained in
// compiled/source-bundle.json. It contains one graph, one resource manifest,
// and one bundle-wide plan. Environment and generation are intentionally absent.
type CompiledSourceBundleArtifact struct {
	Version       int                               `json:"version"`
	ProjectID     projectgraph.ResourceID           `json:"-"`
	ProjectDigest string                            `json:"-"`
	BundleDigest  string                            `json:"bundleDigest"`
	GraphDigest   string                            `json:"graphDigest"`
	Validation    CompiledArtifactValidation        `json:"validation"`
	Manifest      projectmanifest.ResourceManifest  `json:"manifest"`
	Runtime       projectartifact.RuntimeProjection `json:"runtime"`
	Graph         projectgraph.ProjectGraph         `json:"graph"`
	Plan          projectcompiler.BundlePlan        `json:"plan"`
}

// CompiledProjectArtifact is the serving-loader compatibility name. Its wire
// representation is the portable source bundle; target identity is never
// serialized by this package.
type CompiledProjectArtifact = CompiledSourceBundleArtifact

// Plan is the portable compiler plan contract.
type Plan = projectcompiler.BundlePlan

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

// PackSourceBundleOptions is the explicit compiler-to-bundle seam. LEA-372's
// compiler supplies SourceBundle and Plan. SourceRoot may repeat sourceRoot for
// callers that carry the root in options; SourceFiles can be used by an export
// adapter when the checkout is unavailable.
type PackSourceBundleOptions struct {
	SourceBundle projectartifact.SourceBundle
	Plan         projectcompiler.BundlePlan
	SourceRoot   string
	SourceFiles  map[string][]byte
}

// PackSourceBundle writes source-root files and one generated compiled/source-bundle.json.
// The supplied source bundle is already compiled and immutable; this package never
// recompiles or selects a target.
func PackSourceBundle(sourceRoot string, options PackSourceBundleOptions, out io.Writer) (Manifest, string, error) {
	if out == nil {
		return Manifest{}, "", errors.New("bundle output is required")
	}
	if err := options.SourceBundle.Graph().Validate(); err != nil {
		return Manifest{}, "", fmt.Errorf("source bundle is required: %w", err)
	}
	root, err := filepath.Abs(sourceRoot)
	if err != nil {
		return Manifest{}, "", err
	}
	if options.SourceRoot != "" {
		optionRoot, optionErr := filepath.Abs(options.SourceRoot)
		if optionErr != nil {
			return Manifest{}, "", optionErr
		}
		if filepath.Clean(optionRoot) != filepath.Clean(root) {
			return Manifest{}, "", errors.New("source root is specified twice with different values")
		}
	}
	sources := options.SourceFiles
	if sources == nil {
		files, err := collectSourceBundleFiles(root, options.SourceBundle)
		if err != nil {
			return Manifest{}, "", err
		}
		sources, err = readSourceFiles(root, files)
		if err != nil {
			return Manifest{}, "", err
		}
	} else {
		sources, err = validateSuppliedSourceFiles(root, sources, options.SourceBundle)
		if err != nil {
			return Manifest{}, "", err
		}
	}
	compiled, err := compiledSourceBundle(options.SourceBundle, options.Plan)
	if err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := newManifest(options.SourceBundle, compiledBytes, sources)
	return writeBundleBytes(sources, map[string][]byte{CompiledSourceBundleFile: compiledBytes}, manifest, out)
}

// PackCompiledSourceBundle writes only the generated source bundle artifact and manifest.
// It is useful when source bytes were retained separately by a release store.
func PackCompiledSourceBundle(sourceBundle projectartifact.SourceBundle, plan projectcompiler.BundlePlan, out io.Writer) (Manifest, string, error) {
	if out == nil {
		return Manifest{}, "", errors.New("compiled source bundle and output are required")
	}
	if err := sourceBundle.Graph().Validate(); err != nil {
		return Manifest{}, "", fmt.Errorf("source bundle is required: %w", err)
	}
	compiled, err := compiledSourceBundle(sourceBundle, plan)
	if err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := newManifest(sourceBundle, compiledBytes, nil)
	return writeBundleBytes(nil, map[string][]byte{CompiledSourceBundleFile: compiledBytes}, manifest, out)
}

// PackCompiledProject is retained for serving callers while the compiled
// artifact contract is portable. It accepts the source bundle and explicit
// bundle plan; no target Project identity is inferred here.
func PackCompiledProject(sourceBundle projectartifact.SourceBundle, plan projectcompiler.BundlePlan, out io.Writer) (Manifest, string, error) {
	return PackCompiledSourceBundle(sourceBundle, plan, out)
}

func compiledSourceBundle(sourceBundle projectartifact.SourceBundle, plan projectcompiler.BundlePlan) (CompiledSourceBundleArtifact, error) {
	graph := sourceBundle.Graph()
	if err := graph.Validate(); err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	if err := validatePlan(plan, graph); err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	compiled := CompiledSourceBundleArtifact{
		Version:      compiledSourceBundleVersion,
		BundleDigest: sourceBundle.Digest(), GraphDigest: graph.Digest(),
		Validation: CompiledArtifactValidation{Status: "passed", SchemaVersion: sourceBundleAPIVersion},
		Manifest:   sourceBundle.Manifest(), Runtime: sourceBundle.RuntimeProjection(), Graph: graph, Plan: plan,
	}
	if err := ValidateCompiledSourceBundleArtifact(compiled); err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	return compiled, nil
}

func validatePlan(plan projectcompiler.BundlePlan, graph projectgraph.ProjectGraph) error {
	expected := map[string][]string{
		"connections":    resourceIDsByKind(graph, projectgraph.KindConnection),
		"sources":        resourceIDsByKind(graph, projectgraph.KindSource),
		"models":         resourceIDsByKind(graph, projectgraph.KindModel),
		"semanticModels": resourceIDsByKind(graph, projectgraph.KindSemanticModel),
		"pipelines":      resourceIDsByKind(graph, projectgraph.KindPipeline),
		"dashboards":     resourceIDsByKind(graph, projectgraph.KindDashboard),
	}
	actual := map[string][]string{
		"connections": plan.Connections, "sources": plan.Sources, "models": plan.Models,
		"semanticModels": plan.SemanticModels, "pipelines": plan.Pipelines,
		"dashboards": plan.Dashboards,
	}
	for kind, want := range expected {
		if !equalStringSlices(actual[kind], want) {
			return fmt.Errorf("bundle plan %s = %v, want graph resources %v", kind, actual[kind], want)
		}
	}
	for index, change := range plan.Changes {
		if !projectgraph.ResourceID(change.ID).Valid() || strings.TrimSpace(change.Action) == "" {
			return fmt.Errorf("bundle plan change %d has invalid identity", index)
		}
	}
	for index, change := range plan.DependencyChanges {
		if !projectgraph.ResourceID(change.From).Valid() || !projectgraph.ResourceID(change.To).Valid() || strings.TrimSpace(change.Action) == "" {
			return fmt.Errorf("bundle plan dependency change %d has invalid identity", index)
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

func newManifest(sourceBundle projectartifact.SourceBundle, compiled []byte, sourceFiles map[string][]byte) Manifest {
	graph := sourceBundle.Graph()
	manifest := Manifest{
		Version: sourceBundleVersion, BundleDigest: sourceBundle.Digest(), GraphDigest: graph.Digest(), CompiledPath: CompiledSourceBundleFile,
		CompiledSHA256: digestBytes(compiled), Files: make([]ManifestFile, 0, len(sourceFiles)),
	}
	return manifest
}

func readSourceFiles(root string, files []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(files))
	for _, rel := range files {
		rel, err := safeBundlePath(rel)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
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

func collectSourceBundleFiles(baseDir string, sourceBundle projectartifact.SourceBundle) ([]string, error) {
	paths := map[string]struct{}{}
	manifest := sourceBundle.Manifest()
	graph := sourceBundle.Graph()
	if err := validateResourceFiles(sourceBundle); err != nil {
		return nil, err
	}
	for resourceID, path := range manifest.ResourceFiles {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("manifest resource file %q has an empty path", resourceID)
		}
		if _, err := safeBundlePath(path); err != nil {
			return nil, fmt.Errorf("manifest resource file %q: %w", path, err)
		}
		if err := addAuthoredPath(paths, baseDir, path); err != nil {
			return nil, fmt.Errorf("manifest resource file %q: %w", path, err)
		}
	}
	for _, resource := range graph.Resources() {
		path := strings.TrimSpace(resource.Provenance.Path)
		if path == "" {
			continue
		}
		if err := addAuthoredPath(paths, baseDir, path); err != nil {
			return nil, fmt.Errorf("graph resource %s provenance %q: %w", resource.ID, path, err)
		}
		if resource.Kind != projectgraph.KindDashboard {
			continue
		}
		dashboardPath := filepath.Join(baseDir, filepath.FromSlash(path))
		value, err := projectcompiler.LoadDashboardDocument(dashboardPath)
		if err != nil {
			return nil, fmt.Errorf("load dashboard source %q: %w", path, err)
		}
		expanded, err := document.ExpandDashboardFragments(value, dashboardPath, baseDir)
		if err != nil {
			return nil, fmt.Errorf("expand dashboard source %q: %w", path, err)
		}
		for _, fragmentPath := range expanded.Paths {
			if err := addAuthoredPath(paths, baseDir, fragmentPath); err != nil {
				return nil, fmt.Errorf("dashboard fragment %q: %w", fragmentPath, err)
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

func validateSuppliedSourceFiles(baseDir string, sourceFiles map[string][]byte, sourceBundle projectartifact.SourceBundle) (map[string][]byte, error) {
	expected, err := collectSourceBundleFiles(baseDir, sourceBundle)
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
			return nil, fmt.Errorf("source file %q is not an expected source-bundle resource", clean)
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

func validateResourceFiles(sourceBundle projectartifact.SourceBundle) error {
	graph := sourceBundle.Graph()
	manifest := sourceBundle.Manifest()
	expected := map[string]struct{}{}
	for _, resource := range graph.Resources() {
		expected[resource.ID.String()] = struct{}{}
	}
	actual := make(map[string]struct{}, len(manifest.ResourceFiles))
	for id := range manifest.ResourceFiles {
		if _, ok := expected[id]; !ok {
			return fmt.Errorf("manifest resource file key %q is not a source-bundle resource", id)
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
	if clean == "leapview.yaml" {
		return errors.New("legacy project root path is not supported in a source bundle")
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

// ValidateArtifact extracts and validates one source bundle.
func ValidateArtifact(path string) (Validation, error) {
	digestValue, err := fileDigest(path)
	if err != nil {
		return Validation{}, err
	}
	root, err := os.MkdirTemp("", "leapview-deploy-source-bundle-*")
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
	compiled, err := readCompiledSourceBundleArtifact(root, manifest)
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
		BundleDigest: compiled.BundleDigest, Graph: compiled.Graph, Manifest: compiled.Manifest}, nil
}

// ValidateArtifactBytes validates one bounded portable bundle without
// extracting it. It mirrors ValidateArtifactReader for object-backed callers.
func ValidateArtifactBytes(data []byte) (Validation, CompiledProjectArtifact, error) {
	if int64(len(data)) > MaxBundleBytes {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size exceeds limit %d", MaxBundleBytes)
	}
	return validateArtifactBytes(data)
}

func ValidateArtifactReader(reader io.Reader, expectedSize int64) (Validation, CompiledProjectArtifact, error) {
	if reader == nil {
		return Validation{}, CompiledProjectArtifact{}, errors.New("bundle reader is required")
	}
	if expectedSize < 0 || expectedSize > MaxBundleBytes {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle expected size %d is outside limit", expectedSize)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxBundleBytes+1))
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("read bundle: %w", err)
	}
	if int64(len(data)) != expectedSize {
		return Validation{}, CompiledProjectArtifact{}, fmt.Errorf("bundle compressed size = %d, want %d", len(data), expectedSize)
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
	compiledData, ok := entries[CompiledSourceBundleFile]
	if !ok {
		return Validation{}, CompiledProjectArtifact{}, errors.New("compiled source bundle artifact is missing")
	}
	compiled, err := decodeCompiledSourceBundleArtifact(compiledData, manifest)
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return Validation{}, CompiledProjectArtifact{}, err
	}
	return Validation{Digest: digestBytesPrefixed(data), ManifestJSON: string(manifestJSON), BundleDigest: compiled.BundleDigest, Graph: compiled.Graph, Manifest: compiled.Manifest}, compiled, nil
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

func ValidateCompiledSourceBundleArtifact(compiled CompiledSourceBundleArtifact) error {
	if compiled.Version != compiledSourceBundleVersion {
		return fmt.Errorf("compiled source bundle version = %d, want %d", compiled.Version, compiledSourceBundleVersion)
	}
	if err := compiled.Graph.Validate(); err != nil {
		return fmt.Errorf("compiled source bundle graph: %w", err)
	}
	if err := digest.ValidateSHA256Identity(compiled.BundleDigest); err != nil {
		return fmt.Errorf("compiled bundle digest must be a canonical SHA-256 digest: %w", err)
	}
	if compiled.GraphDigest != compiled.Graph.Digest() {
		return fmt.Errorf("compiled graph digest = %q, want %q", compiled.GraphDigest, compiled.Graph.Digest())
	}
	manifest := compiled.Manifest
	if err := projectartifact.RestoreRuntimeProjection(&manifest, compiled.Runtime); err != nil {
		return fmt.Errorf("compiled source bundle runtime projection: %w", err)
	}
	reconstructed, err := projectartifact.NewSourceBundle(compiled.Graph, manifest)
	if err != nil {
		return fmt.Errorf("compiled source bundle manifest: %w", err)
	}
	if reconstructed.Digest() != compiled.BundleDigest {
		return fmt.Errorf("compiled bundle digest = %q, reconstructed source bundle digest = %q", compiled.BundleDigest, reconstructed.Digest())
	}
	if compiled.Validation.Status != "passed" || compiled.Validation.SchemaVersion != sourceBundleAPIVersion {
		return fmt.Errorf("compiled source bundle validation must be passed %s", sourceBundleAPIVersion)
	}
	if err := validatePlan(compiled.Plan, compiled.Graph); err != nil {
		return err
	}
	return nil
}

func ValidateCompiledProjectArtifact(compiled CompiledProjectArtifact) error {
	return ValidateCompiledSourceBundleArtifact(compiled)
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
	if manifest.Version != sourceBundleVersion {
		return Manifest{}, fmt.Errorf("unsupported bundle manifest version %d", manifest.Version)
	}
	if manifest.CompiledPath != CompiledSourceBundleFile {
		return Manifest{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledSourceBundleFile)
	}
	return manifest, nil
}

func LoadCompiledSourceBundleArtifact(root string) (CompiledSourceBundleArtifact, Manifest, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return CompiledSourceBundleArtifact{}, Manifest{}, err
	}
	compiled, err := readCompiledSourceBundleArtifact(root, manifest)
	if err != nil {
		return CompiledSourceBundleArtifact{}, Manifest{}, err
	}
	return compiled, manifest, nil
}

func LoadCompiledProjectArtifact(root string) (CompiledProjectArtifact, Manifest, error) {
	return LoadCompiledSourceBundleArtifact(root)
}

func readCompiledSourceBundleArtifact(root string, manifest Manifest) (CompiledSourceBundleArtifact, error) {
	if manifest.CompiledPath != CompiledSourceBundleFile {
		return CompiledSourceBundleArtifact{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledSourceBundleFile)
	}
	data, err := os.ReadFile(filepath.Join(root, CompiledSourceBundleFile))
	if err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	return decodeCompiledSourceBundleArtifact(data, manifest)
}

func decodeCompiledSourceBundleArtifact(data []byte, manifest Manifest) (CompiledSourceBundleArtifact, error) {
	if manifest.CompiledPath != CompiledSourceBundleFile {
		return CompiledSourceBundleArtifact{}, fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledSourceBundleFile)
	}
	if manifest.CompiledSHA256 != digestBytes(data) {
		return CompiledSourceBundleArtifact{}, errors.New("compiled source bundle digest mismatch")
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return CompiledSourceBundleArtifact{}, fmt.Errorf("decode compiled source bundle: %w", err)
	}
	var compiled CompiledSourceBundleArtifact
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&compiled); err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return CompiledSourceBundleArtifact{}, errors.New("compiled source bundle contains trailing JSON value")
	} else if !errors.Is(err, io.EOF) {
		return CompiledSourceBundleArtifact{}, fmt.Errorf("compiled source bundle trailing data: %w", err)
	}
	if err := ValidateCompiledSourceBundleArtifact(compiled); err != nil {
		return CompiledSourceBundleArtifact{}, err
	}
	if err := projectartifact.RestoreRuntimeProjection(&compiled.Manifest, compiled.Runtime); err != nil {
		return CompiledSourceBundleArtifact{}, fmt.Errorf("compiled source bundle runtime projection: %w", err)
	}
	if compiled.BundleDigest != manifest.BundleDigest || compiled.GraphDigest != manifest.GraphDigest {
		return CompiledSourceBundleArtifact{}, errors.New("compiled source bundle identity does not match bundle manifest")
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
	compiledRel, err := safeBundlePath(manifest.CompiledPath)
	if err != nil {
		return "", fmt.Errorf("invalid compiled path: %w", err)
	}
	if compiledRel != CompiledSourceBundleFile {
		return "", fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledSourceBundleFile)
	}
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"manifest.json": {}, compiledRel: {}}
	for _, file := range manifest.Files {
		rel, err := safeBundlePath(file.Path)
		if err != nil {
			return "", fmt.Errorf("invalid manifest file path %q: %w", file.Path, err)
		}
		if rel == "leapview.yaml" {
			return "", errors.New("legacy project root path is not supported in a source bundle")
		}
		if _, ok := seen[rel]; ok {
			return "", fmt.Errorf("duplicate manifest file path %q", rel)
		}
		seen[rel] = struct{}{}
		allowed[rel] = struct{}{}
		bytes, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(bytes)
		if hex.EncodeToString(sum[:]) != file.SHA256 || int64(len(bytes)) != file.Size {
			return "", fmt.Errorf("file %s digest or size mismatch", file.Path)
		}
	}
	if err := validateNoUnlistedBundleFiles(root, allowed); err != nil {
		return "", err
	}
	return compiledRel, nil
}

func validateManifestEntries(entries map[string][]byte, manifest Manifest) (string, error) {
	compiledRel, err := safeBundlePath(manifest.CompiledPath)
	if err != nil {
		return "", fmt.Errorf("invalid compiled path: %w", err)
	}
	if compiledRel != CompiledSourceBundleFile {
		return "", fmt.Errorf("compiled path = %q, want %q", manifest.CompiledPath, CompiledSourceBundleFile)
	}
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{"manifest.json": {}, compiledRel: {}}
	for _, file := range manifest.Files {
		rel, err := safeBundlePath(file.Path)
		if err != nil {
			return "", fmt.Errorf("invalid manifest file path %q: %w", file.Path, err)
		}
		if rel == "leapview.yaml" {
			return "", errors.New("legacy project root path is not supported in a source bundle")
		}
		if _, ok := seen[rel]; ok {
			return "", fmt.Errorf("duplicate manifest file path %q", rel)
		}
		seen[rel] = struct{}{}
		allowed[rel] = struct{}{}
		content, ok := entries[rel]
		if !ok {
			return "", os.ErrNotExist
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != file.SHA256 || int64(len(content)) != file.Size {
			return "", fmt.Errorf("file %s digest or size mismatch", file.Path)
		}
	}
	for rel := range entries {
		if _, ok := allowed[rel]; !ok {
			return "", fmt.Errorf("bundle file %q is not listed in manifest", rel)
		}
	}
	return compiledRel, nil
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
		target, err := secureBundleTarget(dest, rel)
		if err != nil {
			_ = gz.Close()
			return err
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
