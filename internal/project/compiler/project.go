package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/publication"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/project/manifest"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/workspace"
)

const projectAPIVersion = "leapview.dev/v1"

type Project struct {
	Name            string
	BaseDir         string
	Connections     map[string]semanticmodel.Connection
	ConnectionPaths map[string]string
	Sources         map[string]semanticmodel.Source
	SourcePaths     map[string]string
	Workspaces      map[string]*WorkspaceProject
}

type WorkspaceProject struct {
	ID                    string
	Title                 string
	Description           string
	AllowedSources        map[string]struct{}
	Models                map[string]semanticmodel.Table
	SemanticModels        map[string]projectSemanticModelSpec
	Dashboards            map[string]*dashboardauthoring.Dashboard
	Publications          map[string]publication.Definition
	AccessGroups          map[string]workspace.WorkspaceGroup
	AccessRoleBindings    map[string]workspace.WorkspaceRoleBinding
	AccessGrants          map[string]workspace.WorkspaceGrant
	AccessDataPolicies    map[string]workspace.WorkspaceDataPolicy
	RefreshPipelines      map[string]refreshschedule.Definition
	ModelTitles           map[string]string
	ModelDescriptions     map[string]string
	DashboardTitles       map[string]string
	DashboardDescriptions map[string]string
	DashboardOwners       map[string]string
	DashboardTags         map[string][]string
	Path                  string
	ModelPaths            map[string]string
	SemanticModelPaths    map[string]string
	DashboardPaths        map[string]string
	PublicationPaths      map[string]string
	AccessPaths           map[string]string
	RefreshPipelinePaths  map[string]string
}

func CompileProject(projectPath string, opts Options) (projectartifact.Project, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return projectartifact.Project{}, err
	}
	workspaces := make(map[string]projectartifact.WorkspaceInput, len(project.Workspaces))
	for id, workspaceProject := range project.Workspaces {
		definition, err := workspaceProject.definition(project)
		if err != nil {
			return projectartifact.Project{}, err
		}
		servingStateID := opts.ServingStateID
		workspaceID := workspace.WorkspaceID(id)
		graph, err := ExtractLineage(workspaceID, servingStateID, definition)
		if err != nil {
			return projectartifact.Project{}, err
		}
		if err := compilePublicationClosures(definition, graph); err != nil {
			return projectartifact.Project{}, err
		}
		workspaces[id] = projectartifact.WorkspaceInput{
			Metadata: workspace.Workspace{
				ID:          workspaceID,
				Title:       workspaceProject.Title,
				Description: workspaceProject.Description,
				BaseDir:     project.BaseDir,
				Graph:       graph,
			},
			Manifest: definition,
		}
	}
	return projectartifact.NewProject(project.Name, workspaces)
}

// CompileProjectArtifact produces the environment-neutral compiler output
// retained for authoring, publication, promotion, and rollback. Checkout
// locations and target serving identities are diagnostic/runtime concerns and
// therefore cannot contribute to these immutable bytes.
func CompileProjectArtifact(projectPath string) (projectartifact.Project, error) {
	absoluteProjectPath, err := filepath.Abs(projectPath)
	if err != nil {
		return projectartifact.Project{}, err
	}
	compiled, err := CompileProject(absoluteProjectPath, Options{})
	if err != nil {
		return projectartifact.Project{}, err
	}
	root := filepath.Dir(absoluteProjectPath)
	workspaces := make(map[string]projectartifact.WorkspaceInput, len(compiled.WorkspaceIDs()))
	for _, workspaceID := range compiled.WorkspaceIDs() {
		item, ok := compiled.Workspace(workspaceID)
		if !ok {
			return projectartifact.Project{}, fmt.Errorf("compiled project artifact lost workspace %q", workspaceID)
		}
		metadata := item.Metadata()
		definition := item.Manifest()
		if definition == nil {
			return projectartifact.Project{}, fmt.Errorf("compiled project artifact workspace %q has no definition", workspaceID)
		}
		metadata.BaseDir = ""
		definition.BaseDir = ""
		definition.SourceFiles, err = neutralSourceFiles(root, definition.SourceFiles)
		if err != nil {
			return projectartifact.Project{}, fmt.Errorf("workspace %q source files: %w", workspaceID, err)
		}
		for dashboardID, source := range definition.DashboardSources {
			if strings.TrimSpace(source.Path) != "" {
				source.Path, err = neutralSourcePath(root, source.Path)
				if err != nil {
					return projectartifact.Project{}, fmt.Errorf("workspace %q dashboard %q source: %w", workspaceID, dashboardID, err)
				}
			}
			definition.DashboardSources[dashboardID] = source
		}
		metadata.Graph, err = neutralAssetGraph(root, metadata.Graph)
		if err != nil {
			return projectartifact.Project{}, fmt.Errorf("workspace %q asset graph: %w", workspaceID, err)
		}
		workspaces[workspaceID] = projectartifact.WorkspaceInput{
			Metadata: metadata,
			Manifest: definition,
		}
	}
	return projectartifact.NewProject(compiled.ID(), workspaces)
}

func neutralSourceFiles(root string, values map[string]string) (map[string]string, error) {
	if values == nil {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for id, source := range values {
		relative, err := neutralSourcePath(root, source)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		result[id] = relative
	}
	return result, nil
}

func neutralAssetGraph(root string, graph workspace.AssetGraph) (workspace.AssetGraph, error) {
	result := workspace.AssetGraph{
		Assets: make([]workspace.Asset, len(graph.Assets)),
		Edges:  make([]workspace.AssetEdge, len(graph.Edges)),
	}
	for index, asset := range graph.Assets {
		source, err := neutralSourcePath(root, asset.SourceFile)
		if err != nil {
			return workspace.AssetGraph{}, fmt.Errorf("%s: %w", asset.ID, err)
		}
		asset.SourceFile = source
		asset.ServingStateID = ""
		asset.SnapshotID = ""
		result.Assets[index] = asset
	}
	for index, edge := range graph.Edges {
		edge.ServingStateID = ""
		edge.ID = ""
		result.Edges[index] = edge
	}
	return result, nil
}

func neutralSourcePath(root, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("source path is required")
	}
	absolute := source
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return "", err
	}
	relative = filepath.ToSlash(filepath.Clean(relative))
	if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", fmt.Errorf("source path %q escapes the project", source)
	}
	return relative, nil
}

func compilePublicationClosures(definition *manifest.Workspace, graph workspace.AssetGraph) error {
	types := make(map[workspace.AssetID]workspace.AssetType, len(graph.Assets))
	parents := make(map[workspace.AssetID]workspace.AssetID, len(graph.Assets))
	for _, asset := range graph.Assets {
		types[asset.ID] = asset.Type
		parents[asset.ID] = asset.ParentID
	}
	adjacent := make(map[workspace.AssetID][]workspace.AssetEdge, len(graph.Assets))
	for _, edge := range graph.Edges {
		adjacent[edge.FromAssetID] = append(adjacent[edge.FromAssetID], edge)
	}
	for name, publication := range definition.Publications {
		root := workspace.NewAssetID(workspace.AssetTypeDashboard, definition.Catalog.Workspace.ID+"."+publication.Dashboard)
		seen := map[workspace.AssetID]struct{}{root: {}}
		queue := []workspace.AssetID{root}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			if parent := parents[current]; parent != "" {
				if _, ok := seen[parent]; !ok {
					seen[parent] = struct{}{}
					queue = append(queue, parent)
				}
			}
			for _, edge := range adjacent[current] {
				if edge.Type == workspace.AssetEdgeContains {
					switch types[current] {
					case workspace.AssetTypeCatalog, workspace.AssetTypeSemanticModel, workspace.AssetTypeSemanticTable:
						continue
					}
				}
				next := edge.ToAssetID
				if _, ok := seen[next]; ok {
					continue
				}
				seen[next] = struct{}{}
				queue = append(queue, next)
			}
		}
		publication.DependencyAssetIDs = make([]string, 0, len(seen))
		for id := range seen {
			publication.DependencyAssetIDs = append(publication.DependencyAssetIDs, string(id))
		}
		sort.Strings(publication.DependencyAssetIDs)
		publication.ConfigurationDigest = ""
		payload, err := json.Marshal(publication)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(payload)
		publication.ConfigurationDigest = "sha256:" + hex.EncodeToString(sum[:])
		definition.Publications[name] = publication
	}
	return nil
}
