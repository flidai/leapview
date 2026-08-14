package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	securejoin "github.com/cyphar/filepath-securejoin"
	dashboardappearance "github.com/flidai/leapview/internal/dashboard/appearance"
	"github.com/flidai/leapview/internal/platform/digest"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	"github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/workspace"
)

const (
	BundleFormat                     = "tar.gz"
	ProjectFile                      = "leapview.yaml"
	ProjectArtifactFile              = "compiled/project.json"
	CompiledProjectFile              = "compiled/workspace.json"
	compiledWorkspaceArtifactVersion = 3
)

type Validation struct {
	Digest                    string
	ManifestJSON              string
	RootDir                   string
	ProjectID                 string
	ProjectDigest             string
	ProjectWorkspaces         []string
	AccessPolicy              workspace.AccessPolicy
	DashboardPublicationsJSON string
	DashboardAppearancesJSON  string
	ManagedDataRevisions      map[string]string
	Graph                     workspace.AssetGraph
}

type Manifest struct {
	Version        int            `json:"version"`
	WorkspaceID    string         `json:"workspaceId"`
	WorkspaceTitle string         `json:"workspaceTitle"`
	Environment    string         `json:"environment"`
	ProjectDigest  string         `json:"projectDigest"`
	CatalogPath    string         `json:"catalogPath"`
	CompiledPath   string         `json:"compiledPath"`
	GraphHash      string         `json:"graphHash"`
	Files          []ManifestFile `json:"files"`
	SemanticModels []string       `json:"semanticModels"`
	Dashboards     []string       `json:"dashboards"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ValidateOptions struct {
	Environment string
}

type CompiledWorkspaceArtifact struct {
	Version              int                                    `json:"version"`
	ProjectID            string                                 `json:"projectId"`
	ProjectDigest        string                                 `json:"projectDigest"`
	ProjectWorkspaces    []string                               `json:"projectWorkspaces"`
	WorkspaceID          string                                 `json:"workspaceId"`
	WorkspaceTitle       string                                 `json:"workspaceTitle"`
	Environment          string                                 `json:"environment"`
	ServingStateID       string                                 `json:"servingStateId"`
	ManagedDataRevisions map[string]string                      `json:"managedDataRevisions"`
	Validation           CompiledArtifactValidation             `json:"validation"`
	Manifest             *manifest.Workspace                    `json:"manifest"`
	Graph                workspace.AssetGraph                   `json:"graph"`
	Plan                 workspacecompiler.ProjectPlanWorkspace `json:"plan"`
}

type CompiledArtifactValidation struct {
	Status        string                       `json:"status"`
	Diagnostics   []CompiledArtifactDiagnostic `json:"diagnostics,omitempty"`
	GraphHash     string                       `json:"graphHash"`
	SchemaVersion string                       `json:"schemaVersion"`
}

type CompiledArtifactDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

type PackProjectOptions struct {
	WorkspaceID          string
	Environment          string
	ServingStateID       string
	ActiveGraph          workspace.AssetGraph
	ManagedDataRevisions map[string]string
}

func normalizeEnvironment(value string) string {
	if strings.TrimSpace(value) == "" {
		return "dev"
	}
	return value
}

func PackProject(projectPath string, options PackProjectOptions, out io.Writer) (Manifest, string, error) {
	projectPath, err := filepath.Abs(projectPath)
	if err != nil {
		return Manifest{}, "", err
	}
	environment := normalizeEnvironment(options.Environment)
	if options.WorkspaceID == "" {
		return Manifest{}, "", fmt.Errorf("project candidate requires explicit workspace")
	}
	if options.ServingStateID == "" {
		return Manifest{}, "", fmt.Errorf("project candidate requires serving state id")
	}
	compiled, err := workspacecompiler.CompileProject(projectPath, workspacecompiler.Options{ServingStateID: workspace.ServingStateID(options.ServingStateID)})
	if err != nil {
		return Manifest{}, "", err
	}
	compiledWorkspace, ok := compiled.Workspace(options.WorkspaceID)
	if !ok {
		return Manifest{}, "", fmt.Errorf("project %q has no workspace %q", projectPath, options.WorkspaceID)
	}
	metadata := compiledWorkspace.Metadata()
	definition := compiledWorkspace.Manifest()
	if err := workspace.ValidateAssetGraphForServingState(metadata.Graph, workspace.WorkspaceID(options.WorkspaceID), workspace.ServingStateID(options.ServingStateID)); err != nil {
		return Manifest{}, "", err
	}
	plan, err := workspacecompiler.PlanProjectAgainstGraph(projectPath, options.WorkspaceID, options.ActiveGraph)
	if err != nil {
		return Manifest{}, "", err
	}
	workspacePlan, ok := projectPlanWorkspace(plan, options.WorkspaceID)
	if !ok {
		return Manifest{}, "", fmt.Errorf("project %q has no workspace %q in plan", projectPath, options.WorkspaceID)
	}
	pins := make(map[string]string, len(options.ManagedDataRevisions))
	for connection, digest := range options.ManagedDataRevisions {
		pins[connection] = digest
	}
	baseDir := filepath.Dir(projectPath)
	relFiles, err := collectProjectBundleFiles(baseDir, projectPath)
	if err != nil {
		return Manifest{}, "", err
	}
	projectDigest, err := digestProjectSources(baseDir, projectPath, relFiles)
	if err != nil {
		return Manifest{}, "", err
	}
	projectWorkspaces := compiled.WorkspaceIDs()
	compiledArtifact := CompiledWorkspaceArtifact{
		Version:              compiledWorkspaceArtifactVersion,
		ProjectID:            compiled.ID(),
		ProjectDigest:        projectDigest,
		ProjectWorkspaces:    projectWorkspaces,
		WorkspaceID:          options.WorkspaceID,
		WorkspaceTitle:       metadata.Title,
		Environment:          string(environment),
		ServingStateID:       string(options.ServingStateID),
		ManagedDataRevisions: pins,
		Validation: CompiledArtifactValidation{
			Status:        "passed",
			GraphHash:     graphHash(metadata.Graph),
			SchemaVersion: "leapview.dev/v1",
		},
		Manifest: definition,
		Graph:    metadata.Graph,
		Plan:     workspacePlan,
	}
	if err := ValidateCompiledWorkspaceArtifact(compiledArtifact); err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiledArtifact, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := Manifest{
		Version:        1,
		WorkspaceID:    options.WorkspaceID,
		WorkspaceTitle: metadata.Title,
		Environment:    string(environment),
		ProjectDigest:  projectDigest,
		CatalogPath:    ProjectFile,
		CompiledPath:   CompiledProjectFile,
		GraphHash:      digestBytes(compiledBytes),
		Files:          make([]ManifestFile, 0, len(relFiles)),
	}
	for _, model := range definition.Catalog.SemanticModels {
		manifest.SemanticModels = append(manifest.SemanticModels, model.ID)
	}
	for _, report := range definition.Catalog.Dashboards {
		manifest.Dashboards = append(manifest.Dashboards, report.ID)
	}
	return writeBundle(baseDir, relFiles, ProjectFile, projectPath, map[string][]byte{CompiledProjectFile: compiledBytes}, manifest, out)
}

// PackCompiledProject derives a target-specific workspace serving artifact
// from retained environment-neutral compiler bytes. It never reads or
// recompiles an authored checkout.
func PackCompiledProject(
	project projectartifact.Project,
	sourceDigest string,
	options PackProjectOptions,
	out io.Writer,
) (Manifest, string, error) {
	if out == nil || project.ID() == "" {
		return Manifest{}, "", fmt.Errorf("compiled project artifact and output are required")
	}
	if err := digest.ValidateSHA256Identity(sourceDigest); err != nil {
		return Manifest{}, "", fmt.Errorf("project source digest: %w", err)
	}
	environment := normalizeEnvironment(options.Environment)
	options.WorkspaceID = strings.TrimSpace(options.WorkspaceID)
	options.ServingStateID = strings.TrimSpace(options.ServingStateID)
	if options.WorkspaceID == "" {
		return Manifest{}, "", fmt.Errorf("project candidate requires explicit workspace")
	}
	if options.ServingStateID == "" {
		return Manifest{}, "", fmt.Errorf("project candidate requires serving state id")
	}
	selected, ok := project.Workspace(options.WorkspaceID)
	if !ok {
		return Manifest{}, "", fmt.Errorf(
			"compiled project %q has no workspace %q",
			project.ID(),
			options.WorkspaceID,
		)
	}
	definition := selected.Manifest()
	if definition == nil {
		return Manifest{}, "", fmt.Errorf(
			"compiled project %q workspace %q has no definition",
			project.ID(),
			options.WorkspaceID,
		)
	}
	metadata := selected.Metadata()
	plan, err := workspacecompiler.PlanCompiledProjectAgainstGraph(
		project,
		options.WorkspaceID,
		options.ActiveGraph,
	)
	if err != nil {
		return Manifest{}, "", err
	}
	workspacePlan, ok := projectPlanWorkspace(plan, options.WorkspaceID)
	if !ok {
		return Manifest{}, "", fmt.Errorf(
			"compiled project %q has no workspace %q in plan",
			project.ID(),
			options.WorkspaceID,
		)
	}
	compiled := CompiledWorkspaceArtifact{
		Version:              compiledWorkspaceArtifactVersion,
		ProjectID:            project.ID(),
		ProjectDigest:        sourceDigest,
		ProjectWorkspaces:    project.WorkspaceIDs(),
		WorkspaceID:          options.WorkspaceID,
		WorkspaceTitle:       metadata.Title,
		Environment:          environment,
		ServingStateID:       options.ServingStateID,
		ManagedDataRevisions: cloneStringMap(options.ManagedDataRevisions),
		Validation: CompiledArtifactValidation{
			Status:        "passed",
			GraphHash:     graphHash(metadata.Graph),
			SchemaVersion: projectAPIVersion,
		},
		Manifest: definition,
		Graph:    metadata.Graph,
		Plan:     workspacePlan,
	}
	if err := ValidateCompiledWorkspaceArtifact(compiled); err != nil {
		return Manifest{}, "", err
	}
	compiledBytes, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	manifest := Manifest{
		Version:        1,
		WorkspaceID:    options.WorkspaceID,
		WorkspaceTitle: metadata.Title,
		Environment:    environment,
		ProjectDigest:  sourceDigest,
		CatalogPath:    ProjectArtifactFile,
		CompiledPath:   CompiledProjectFile,
		GraphHash:      digestBytes(compiledBytes),
	}
	for _, model := range definition.Catalog.SemanticModels {
		manifest.SemanticModels = append(manifest.SemanticModels, model.ID)
	}
	for _, dashboard := range definition.Catalog.Dashboards {
		manifest.Dashboards = append(manifest.Dashboards, dashboard.ID)
	}
	return writeBundleBytes(
		map[string][]byte{ProjectArtifactFile: project.Canonical()},
		map[string][]byte{CompiledProjectFile: compiledBytes},
		manifest,
		out,
	)
}

func digestProjectSources(baseDir, projectPath string, relFiles []string) (string, error) {
	hash := sha256.New()
	for _, rel := range relFiles {
		sourcePath := filepath.Join(baseDir, filepath.FromSlash(rel))
		if rel == cleanBundlePath(filepath.Base(projectPath)) {
			sourcePath = projectPath
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%d:%s:%d:", len(rel), rel, len(content))
		_, _ = hash.Write(content)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func collectProjectBundleFiles(baseDir, projectPath string) ([]string, error) {
	relProject, err := filepath.Rel(baseDir, projectPath)
	if err != nil {
		return nil, err
	}
	relFiles := []string{cleanBundlePath(relProject)}
	for _, root := range []string{"connections", "sources", "workspaces"} {
		dir := filepath.Join(baseDir, root)
		if _, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".yaml" && filepath.Ext(path) != ".yml" {
				return nil
			}
			rel, err := filepath.Rel(baseDir, path)
			if err != nil {
				return err
			}
			relFiles = append(relFiles, cleanBundlePath(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(relFiles[1:])
	return relFiles, nil
}

func writeBundle(baseDir string, relFiles []string, rootRel string, rootPath string, generatedFiles map[string][]byte, manifest Manifest, out io.Writer) (Manifest, string, error) {
	sourceFiles := make(map[string][]byte, len(relFiles))
	for _, rel := range relFiles {
		sourcePath := filepath.Join(baseDir, rel)
		if rel == rootRel {
			sourcePath = rootPath
		}
		info, err := os.Stat(sourcePath)
		if err != nil {
			return Manifest{}, "", err
		}
		if info.IsDir() {
			return Manifest{}, "", fmt.Errorf("bundle path %s is a directory", rel)
		}
		content, err := os.ReadFile(sourcePath)
		if err != nil {
			return Manifest{}, "", err
		}
		sourceFiles[rel] = content
	}
	return writeBundleBytes(sourceFiles, generatedFiles, manifest, out)
}

func writeBundleBytes(
	sourceFiles, generatedFiles map[string][]byte,
	manifest Manifest,
	out io.Writer,
) (Manifest, string, error) {
	hash := sha256.New()
	mw := io.MultiWriter(out, hash)
	gz := gzip.NewWriter(mw)
	tw := tar.NewWriter(gz)
	seen := map[string]struct{}{}
	sourcePaths := make([]string, 0, len(sourceFiles))
	for rel := range sourceFiles {
		sourcePaths = append(sourcePaths, rel)
	}
	sort.Strings(sourcePaths)
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
		fileHash := sha256.Sum256(content)
		manifest.Files = append(manifest.Files, ManifestFile{
			Path:   rel,
			SHA256: hex.EncodeToString(fileHash[:]),
			Size:   int64(len(content)),
		})
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o644, Size: int64(len(content))}); err != nil {
			return Manifest{}, "", err
		}
		if _, err := tw.Write(content); err != nil {
			return Manifest{}, "", err
		}
	}
	generatedPaths := make([]string, 0, len(generatedFiles))
	for rel := range generatedFiles {
		generatedPaths = append(generatedPaths, rel)
	}
	sort.Strings(generatedPaths)
	for _, rel := range generatedPaths {
		cleanRel, err := safeBundlePath(rel)
		if err != nil {
			return Manifest{}, "", err
		}
		if _, ok := seen[cleanRel]; ok {
			return Manifest{}, "", fmt.Errorf("bundle generated path %s duplicates source file", cleanRel)
		}
		content := generatedFiles[rel]
		if err := tw.WriteHeader(&tar.Header{Name: cleanRel, Mode: 0o644, Size: int64(len(content))}); err != nil {
			return Manifest{}, "", err
		}
		if _, err := tw.Write(content); err != nil {
			return Manifest{}, "", err
		}
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, "", err
	}
	if err := tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o644, Size: int64(len(manifestBytes))}); err != nil {
		return Manifest{}, "", err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return Manifest{}, "", err
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, "", err
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, "", err
	}
	return manifest, hex.EncodeToString(hash.Sum(nil)), nil
}

func ValidateArtifact(path string, workspaceID string, servingStateID string) (Validation, error) {
	return ValidateArtifactWithOptions(path, workspaceID, servingStateID, ValidateOptions{})
}

func ValidateArtifactWithOptions(path string, workspaceID string, servingStateID string, options ValidateOptions) (Validation, error) {
	digest, err := fileDigest(path)
	if err != nil {
		return Validation{}, err
	}
	root, err := os.MkdirTemp("", "leapview-deploy-*")
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
	compiled, err := readCompiledWorkspaceArtifact(root, manifest)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	if workspaceID == "" {
		if strings.TrimSpace(manifest.WorkspaceID) == "" {
			os.RemoveAll(root)
			return Validation{}, fmt.Errorf("project artifact manifest requires workspaceId")
		}
		workspaceID = string(manifest.WorkspaceID)
	}
	if compiled.WorkspaceID != string(workspaceID) {
		os.RemoveAll(root)
		return Validation{}, fmt.Errorf("compiled artifact workspace = %q, want %q", compiled.WorkspaceID, workspaceID)
	}
	if strings.TrimSpace(compiled.Environment) == "" {
		os.RemoveAll(root)
		return Validation{}, fmt.Errorf("compiled artifact requires environment")
	}
	if strings.TrimSpace(manifest.Environment) == "" {
		os.RemoveAll(root)
		return Validation{}, fmt.Errorf("project artifact manifest requires environment")
	}
	if compiled.Environment != manifest.Environment {
		os.RemoveAll(root)
		return Validation{}, fmt.Errorf("compiled artifact environment = %q, manifest environment = %q", compiled.Environment, manifest.Environment)
	}
	if options.Environment != "" {
		expectedEnvironment := normalizeEnvironment(options.Environment)
		if string(compiled.Environment) != expectedEnvironment {
			os.RemoveAll(root)
			return Validation{}, fmt.Errorf("compiled artifact environment = %q, want %q", compiled.Environment, expectedEnvironment)
		}
	}
	if err := ValidateCompiledWorkspaceArtifact(compiled); err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	compiled.Graph = retargetArtifactGraph(compiled.Graph, workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID))
	if err := workspace.ValidateAssetGraphForServingState(compiled.Graph, workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID)); err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	publicationsJSON, err := json.Marshal(compiled.Manifest.Publications)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	appearances := map[string]dashboardappearance.Patch{}
	for _, dashboard := range compiled.Manifest.Catalog.Dashboards {
		if dashboard.Appearance.Icon != nil || dashboard.Appearance.Color != nil {
			appearances[dashboard.ID] = dashboard.Appearance
		}
	}
	appearancesJSON, err := json.Marshal(appearances)
	if err != nil {
		os.RemoveAll(root)
		return Validation{}, err
	}
	return Validation{
		Digest:                    digest,
		ManifestJSON:              string(manifestJSON),
		RootDir:                   root,
		ProjectID:                 compiled.ProjectID,
		ProjectDigest:             compiled.ProjectDigest,
		ProjectWorkspaces:         append([]string(nil), compiled.ProjectWorkspaces...),
		AccessPolicy:              compiled.Manifest.Access,
		DashboardPublicationsJSON: string(publicationsJSON),
		DashboardAppearancesJSON:  string(appearancesJSON),
		ManagedDataRevisions:      cloneStringMap(compiled.ManagedDataRevisions),
		Graph:                     compiled.Graph,
	}, nil
}

func retargetArtifactGraph(graph workspace.AssetGraph, workspaceID workspace.WorkspaceID, servingStateID workspace.ServingStateID) workspace.AssetGraph {
	out := workspace.AssetGraph{Assets: make([]workspace.Asset, 0, len(graph.Assets)), Edges: make([]workspace.AssetEdge, 0, len(graph.Edges))}
	for _, asset := range graph.Assets {
		asset.WorkspaceID = workspaceID
		asset.ServingStateID = servingStateID
		asset.SnapshotID = workspace.NewAssetSnapshotID(servingStateID, asset.ID)
		out.Assets = append(out.Assets, asset)
	}
	for _, edge := range graph.Edges {
		edge.WorkspaceID = workspaceID
		edge.ServingStateID = servingStateID
		edge.ID = workspace.NewAssetEdgeID(servingStateID, edge.FromAssetID, edge.ToAssetID, edge.Type)
		out.Edges = append(out.Edges, edge)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func writeExtractedRoot(root string, out io.Writer) error {
	hash := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, hash))
	tw := tar.NewWriter(gz)
	paths := []string{}
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
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return err
	}
	sort.Strings(paths)
	for _, rel := range paths {
		bytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0o644, Size: int64(len(bytes))}); err != nil {
			return err
		}
		if _, err := tw.Write(bytes); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func validateCompiledArtifactValidation(compiled CompiledWorkspaceArtifact) error {
	if compiled.Validation.Status != "passed" {
		return fmt.Errorf("compiled artifact validation status = %q, want passed", compiled.Validation.Status)
	}
	if compiled.Validation.SchemaVersion != projectAPIVersion {
		return fmt.Errorf("compiled artifact validation schemaVersion = %q, want %q", compiled.Validation.SchemaVersion, projectAPIVersion)
	}
	if want := graphHash(compiled.Graph); compiled.Validation.GraphHash != want {
		return fmt.Errorf("compiled artifact validation graphHash = %q, want %q", compiled.Validation.GraphHash, want)
	}
	return nil
}

func ValidateCompiledWorkspaceArtifact(compiled CompiledWorkspaceArtifact) error {
	if compiled.ProjectID == "" || compiled.ProjectID != strings.TrimSpace(compiled.ProjectID) {
		return fmt.Errorf("compiled artifact projectId is required")
	}
	if err := digest.ValidateSHA256Identity(compiled.ProjectDigest); err != nil {
		return fmt.Errorf("compiled artifact project digest: %w", err)
	}
	if len(compiled.ProjectWorkspaces) == 0 || !sort.StringsAreSorted(compiled.ProjectWorkspaces) {
		return fmt.Errorf("compiled artifact requires sorted project workspaces")
	}
	seenWorkspaces := make(map[string]struct{}, len(compiled.ProjectWorkspaces))
	for _, workspaceID := range compiled.ProjectWorkspaces {
		if strings.TrimSpace(workspaceID) == "" || workspaceID != strings.TrimSpace(workspaceID) {
			return fmt.Errorf("compiled artifact has invalid project workspace %q", workspaceID)
		}
		if _, duplicate := seenWorkspaces[workspaceID]; duplicate {
			return fmt.Errorf("compiled artifact has duplicate project workspace %q", workspaceID)
		}
		seenWorkspaces[workspaceID] = struct{}{}
	}
	if _, exists := seenWorkspaces[compiled.WorkspaceID]; !exists {
		return fmt.Errorf("compiled artifact project workspaces omit workspace %q", compiled.WorkspaceID)
	}
	if compiled.Manifest == nil {
		return fmt.Errorf("compiled artifact definition is required")
	}
	if compiled.ManagedDataRevisions == nil {
		return fmt.Errorf("compiled artifact managedDataRevisions object is required")
	}
	managedConnections, err := managedConnectionNames(compiled.Manifest)
	if err != nil {
		return err
	}
	if len(compiled.ManagedDataRevisions) != len(managedConnections) {
		return fmt.Errorf("compiled artifact managedDataRevisions must exactly match managed connections")
	}
	for _, connection := range managedConnections {
		digest, ok := compiled.ManagedDataRevisions[connection]
		if !ok {
			return fmt.Errorf("compiled artifact managedDataRevisions must exactly match managed connections")
		}
		if !canonicalManagedRevisionDigest(digest) {
			return fmt.Errorf("compiled artifact managedDataRevisions[%q] must be a canonical SHA-256 digest", connection)
		}
	}
	for connection := range compiled.ManagedDataRevisions {
		if connection == "" || connection != strings.TrimSpace(connection) {
			return fmt.Errorf("compiled artifact managedDataRevisions contains a non-canonical connection name")
		}
	}
	return validateCompiledArtifactValidation(compiled)
}

func managedConnectionNames(definition *manifest.Workspace) ([]string, error) {
	connections := map[string]semanticConnection{}
	for _, model := range definition.Models {
		if model == nil {
			return nil, fmt.Errorf("compiled artifact contains a nil model")
		}
		for authoredName, connection := range model.Connections {
			name := strings.TrimSpace(authoredName)
			kind := strings.TrimSpace(connection.Kind)
			if name == "" || name != authoredName || kind == "" || kind != connection.Kind {
				return nil, fmt.Errorf("compiled artifact contains non-canonical connection metadata")
			}
			if existing, ok := connections[name]; ok && !reflect.DeepEqual(existing.value, connection) {
				return nil, fmt.Errorf("compiled artifact connection %q has conflicting definitions", name)
			}
			connections[name] = semanticConnection{kind: kind, value: connection}
		}
	}
	names := make([]string, 0, len(connections))
	for name, connection := range connections {
		if connection.kind == "managed" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

type semanticConnection struct {
	kind  string
	value any
}

func canonicalManagedRevisionDigest(value string) bool {
	const prefix = "sha256:"
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	hexDigest := value[len(prefix):]
	if strings.ToLower(hexDigest) != hexDigest {
		return false
	}
	_, err := hex.DecodeString(hexDigest)
	return err == nil
}

const projectAPIVersion = "leapview.dev/v1"

func ExtractArtifact(path, dest string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := safeBundlePath(header.Name)
		if err != nil {
			return err
		}
		target, err := secureBundleTarget(dest, rel)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported bundle entry %q", header.Name)
		}
	}
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

func readManifest(root string) (Manifest, error) {
	bytes, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(bytes, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.CatalogPath == "" {
		manifest.CatalogPath = ProjectFile
	}
	if manifest.CompiledPath == "" {
		manifest.CompiledPath = CompiledProjectFile
	}
	return manifest, nil
}

func LoadCompiledWorkspaceArtifact(root string) (CompiledWorkspaceArtifact, Manifest, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return CompiledWorkspaceArtifact{}, Manifest{}, err
	}
	compiled, err := readCompiledWorkspaceArtifact(root, manifest)
	if err != nil {
		return CompiledWorkspaceArtifact{}, Manifest{}, err
	}
	return compiled, manifest, nil
}

func readCompiledWorkspaceArtifact(root string, manifest Manifest) (CompiledWorkspaceArtifact, error) {
	compiledRel, err := safeBundlePath(manifest.CompiledPath)
	if err != nil {
		return CompiledWorkspaceArtifact{}, fmt.Errorf("invalid compiled path: %w", err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, compiledRel))
	if err != nil {
		return CompiledWorkspaceArtifact{}, err
	}
	if manifest.GraphHash != "" && digestBytes(bytes) != manifest.GraphHash {
		return CompiledWorkspaceArtifact{}, fmt.Errorf("compiled artifact digest mismatch")
	}
	var compiled CompiledWorkspaceArtifact
	if err := json.Unmarshal(bytes, &compiled); err != nil {
		return CompiledWorkspaceArtifact{}, err
	}
	if compiled.Version != compiledWorkspaceArtifactVersion {
		return CompiledWorkspaceArtifact{}, fmt.Errorf("compiled artifact version = %d, want %d; redeploy the workspace", compiled.Version, compiledWorkspaceArtifactVersion)
	}
	if err := ValidateCompiledWorkspaceArtifact(compiled); err != nil {
		return CompiledWorkspaceArtifact{}, err
	}
	return compiled, nil
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
	seen := map[string]struct{}{}
	allowed := map[string]struct{}{
		"manifest.json": {},
		compiledRel:     {},
	}
	hasCatalog := false
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
		path := filepath.Join(root, rel)
		bytes, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(bytes)
		if got := hex.EncodeToString(sum[:]); got != file.SHA256 {
			return "", fmt.Errorf("file %s digest mismatch", file.Path)
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
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func digestBytes(bytes []byte) string {
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:])
}

func graphHash(graph workspace.AssetGraph) string {
	bytes, err := json.Marshal(graph)
	if err != nil {
		return ""
	}
	return digestBytes(bytes)
}

func projectPlanWorkspace(plan workspacecompiler.ProjectPlan, workspaceID string) (workspacecompiler.ProjectPlanWorkspace, bool) {
	for _, workspacePlan := range plan.Workspaces {
		if workspacePlan.ID == workspaceID {
			return workspacePlan, true
		}
	}
	return workspacecompiler.ProjectPlanWorkspace{}, false
}

func cleanBundlePath(path string) string {
	path = filepath.ToSlash(filepath.Clean(path))
	path = strings.TrimPrefix(path, "/")
	path = strings.TrimPrefix(path, "../")
	return path
}

func safeBundlePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("bundle path %q must be relative", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" {
		return "", fmt.Errorf("bundle path %q is empty", path)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("bundle path %q escapes bundle root", path)
		}
	}
	return clean, nil
}
