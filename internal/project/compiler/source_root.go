package compiler

import (
	"fmt"
	"path/filepath"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// LoadSourceRoot compiles the analytics resources discovered in the fixed
// conventional directories beneath sourceRoot. The synthesized Project root
// is an internal graph compatibility detail and is never authored.
func LoadSourceRoot(sourceRoot string) (Project, error) {
	discovered, err := discoverAuthoredResources(sourceRoot)
	if err != nil {
		return Project{}, err
	}
	project := newProjectAssembly(
		syntheticSourceRootID,
		syntheticSourceRootName,
		discovered.root,
		discovered.root,
		projectgraph.Metadata{DisplayName: "Analytics source root"},
	)
	project.ResourceIDOwners[syntheticSourceRootID.String()] = "internal source root"

	loaders := []struct {
		kind string
		load func(*Project, []string) error
	}{
		{kind: "Connection", load: loadConnections},
		{kind: "Source", load: loadSources},
		{kind: "Model", load: loadFlatModels},
		{kind: "SemanticModel", load: loadFlatSemanticModels},
		{kind: "Pipeline", load: loadFlatPipelines},
		{kind: "Dashboard", load: loadFlatDashboards},
	}
	for _, loader := range loaders {
		includes, includeErr := discovered.includes(loader.kind)
		if includeErr != nil {
			return Project{}, includeErr
		}
		if loadErr := loader.load(&project, includes); loadErr != nil {
			return Project{}, loadErr
		}
	}
	transitionalIncludes, err := discovered.transitionalIncludes()
	if err != nil {
		return Project{}, err
	}
	if err := loadFlatAccess(&project, transitionalIncludes); err != nil {
		return Project{}, err
	}
	if err := validateFlatProject(project); err != nil {
		return Project{}, err
	}
	project.Graph, err = compileProjectGraph(project)
	if err != nil {
		return Project{}, err
	}
	project.Manifest, err = projectManifest(project)
	if err != nil {
		return Project{}, err
	}
	return project, nil
}

// CompileSourceRoot emits the existing immutable graph/artifact format from a
// conventional source root. It intentionally does not alter graph hashing.
func CompileSourceRoot(sourceRoot string) (projectartifact.Project, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return projectartifact.Project{}, err
	}
	return projectartifact.NewProject(project.Graph, project.Manifest)
}

// CompileSourceRootGraph compiles only the portable graph projection.
func CompileSourceRootGraph(sourceRoot string) (projectgraph.ProjectGraph, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return projectgraph.ProjectGraph{}, err
	}
	return project.Graph, nil
}

func (discovery authoredResourceDiscovery) includes(kind string) ([]string, error) {
	return relativeDiscoveredPaths(discovery.root, discovery.resources, kind)
}

func (discovery authoredResourceDiscovery) transitionalIncludes() ([]string, error) {
	return relativeDiscoveredPaths(discovery.root, discovery.transitionalDataPolicies, "DataPolicy")
}

func relativeDiscoveredPaths(root string, resources []discoveredAuthoredResource, kind string) ([]string, error) {
	paths := make([]string, 0, len(resources))
	for _, resource := range resources {
		if resource.kind != kind {
			continue
		}
		relative, err := filepath.Rel(root, resource.path)
		if err != nil || filepath.IsAbs(relative) || relative == ".." {
			return nil, fmt.Errorf("discovered authored resource %q escaped validated root %q", resource.path, root)
		}
		paths = append(paths, filepath.ToSlash(relative))
	}
	return paths, nil
}
