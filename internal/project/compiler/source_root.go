package compiler

import (
	"fmt"
	"path/filepath"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// LoadSourceRoot loads the resources discovered beneath sourceRoot's six
// conventional authoring directories. It never reads a root manifest or
// creates a sourceAssembly graph node.
func LoadSourceRoot(sourceRoot string) (sourceAssembly, error) {
	discovered, err := discoverAuthoredResources(sourceRoot)
	if err != nil {
		return sourceAssembly{}, err
	}
	project := newSourceAssembly(discovered.root)

	loaders := []struct {
		kind string
		load func(*sourceAssembly, []string) error
	}{
		{kind: "Connection", load: loadConnections},
		{kind: "Source", load: loadSources},
		{kind: "Model", load: loadFlatModels},
		{kind: "SemanticModel", load: loadFlatSemanticModels},
		{kind: "Pipeline", load: loadFlatPipelines},
		{kind: "Dashboard", load: loadFlatDashboards},
	}
	for _, loader := range loaders {
		paths, pathErr := discovered.pathsForKind(loader.kind)
		if pathErr != nil {
			return sourceAssembly{}, pathErr
		}
		if loadErr := loader.load(&project, paths); loadErr != nil {
			return sourceAssembly{}, loadErr
		}
	}
	if err := validateSourceAssembly(project); err != nil {
		return sourceAssembly{}, err
	}
	project.Graph, err = compileGraph(project)
	if err != nil {
		return sourceAssembly{}, err
	}
	project.Manifest, err = buildResourceManifest(project)
	if err != nil {
		return sourceAssembly{}, err
	}
	return project, nil
}

// CompileGraph compiles only the portable graph projection.
func compileSourceRootGraph(sourceRoot string) (projectgraph.ProjectGraph, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	return project.Graph, nil
}

func (discovery authoredResourceDiscovery) pathsForKind(kind string) ([]string, error) {
	paths := make([]string, 0)
	for _, resource := range discovery.resources {
		if resource.kind != kind {
			continue
		}
		relative, err := filepath.Rel(discovery.root, resource.path)
		if err != nil || filepath.IsAbs(relative) || filepathHasParent(relative) {
			return nil, fmt.Errorf("discovered authored resource %q escaped validated root %q", resource.path, discovery.root)
		}
		paths = append(paths, resource.path)
	}
	return paths, nil
}

func filepathHasParent(path string) bool {
	return path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator))
}
