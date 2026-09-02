package compiler

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type authoredResourceDirectory struct {
	directory string
	kind      string
}

// authoredResourceDirectories is the complete public analytics authoring
// registry. Directory ownership replaces the former Project include lists:
// adding a seventh public kind requires an explicit contract decision.
var authoredResourceDirectories = [...]authoredResourceDirectory{
	{directory: "connections", kind: "Connection"},
	{directory: "sources", kind: "Source"},
	{directory: "models", kind: "Model"},
	{directory: "semantic-models", kind: "SemanticModel"},
	{directory: "pipelines", kind: "Pipeline"},
	{directory: "dashboards", kind: "Dashboard"},
}

type discoveredAuthoredResource struct {
	kind string
	path string
}

type authoredResourceDiscovery struct {
	root                     string
	resources                []discoveredAuthoredResource
	transitionalDataPolicies []discoveredAuthoredResource
}

// discoverAuthoredResources returns the deterministic source set owned by the
// conventional authoring directories. DataPolicy remains a deliberately
// separate transitional input until ADR-0017 qualifies its replacement.
func discoverAuthoredResources(sourceRoot string) (authoredResourceDiscovery, error) {
	root, err := authoredSourceRoot(sourceRoot)
	if err != nil {
		return authoredResourceDiscovery{}, err
	}
	if err := rejectLegacyProjectManifest(root); err != nil {
		return authoredResourceDiscovery{}, err
	}

	result := authoredResourceDiscovery{root: root}
	for _, owned := range authoredResourceDirectories {
		paths, discoverErr := discoverYAMLFiles(root, owned.directory)
		if discoverErr != nil {
			return authoredResourceDiscovery{}, discoverErr
		}
		for _, path := range paths {
			kind, headerErr := authoredResourceKind(path)
			if headerErr != nil {
				return authoredResourceDiscovery{}, headerErr
			}
			if kind != owned.kind {
				return authoredResourceDiscovery{}, fmt.Errorf("%s: authored kind %q belongs in %s/, not %s/", path, kind, directoryForAuthoredKind(kind), owned.directory)
			}
			result.resources = append(result.resources, discoveredAuthoredResource{kind: kind, path: path})
		}
	}

	policies, err := discoverYAMLFiles(root, "access")
	if err != nil {
		return authoredResourceDiscovery{}, err
	}
	for _, path := range policies {
		kind, headerErr := authoredResourceKind(path)
		if headerErr != nil {
			return authoredResourceDiscovery{}, headerErr
		}
		if kind != "DataPolicy" {
			return authoredResourceDiscovery{}, removedAuthoringKindError(path, kind)
		}
		result.transitionalDataPolicies = append(result.transitionalDataPolicies, discoveredAuthoredResource{kind: kind, path: path})
	}
	if err := rejectRemovedDirectory(root, "publications"); err != nil {
		return authoredResourceDiscovery{}, err
	}

	sortDiscoveredResources(result.resources)
	sortDiscoveredResources(result.transitionalDataPolicies)
	return result, nil
}

func authoredSourceRoot(sourceRoot string) (string, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return "", fmt.Errorf("analytics source root is required")
	}
	absolute, err := filepath.Abs(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("resolve analytics source root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("stat analytics source root %q: %w", absolute, err)
	}
	if !info.IsDir() {
		if projectConfigFile(absolute) {
			return "", fmt.Errorf("Project authoring was removed; pass the source root directory %q instead of %q", filepath.Dir(absolute), absolute)
		}
		return "", fmt.Errorf("analytics source root %q is not a directory", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve analytics source root %q: %w", absolute, err)
	}
	return filepath.Clean(resolved), nil
}

func rejectLegacyProjectManifest(root string) error {
	path := filepath.Join(root, "leapview.yaml")
	_, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy Project manifest %q: %w", path, err)
	}
	return fmt.Errorf("Project authoring was removed; pass the source root directory %q and delete %q", root, path)
}

func discoverYAMLFiles(root, directory string) ([]string, error) {
	base := filepath.Join(root, directory)
	info, err := os.Stat(base)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect authored resource directory %q: %w", base, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("authored resource directory %q is not a directory", base)
	}
	paths := []string{}
	for _, extension := range []string{"*.yaml", "*.yml"} {
		matches, globErr := filepath.Glob(filepath.Join(base, extension))
		if globErr != nil {
			return nil, fmt.Errorf("discover %s resources: %w", directory, globErr)
		}
		paths = append(paths, matches...)
	}
	sort.Strings(paths)
	for _, path := range paths {
		resolved, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve authored resource %q: %w", path, resolveErr)
		}
		relative, relativeErr := filepath.Rel(root, resolved)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("authored resource %q escapes source root %q", path, root)
		}
	}
	return paths, nil
}

func authoredResourceKind(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read authored resource %q: %w", path, err)
	}
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		return "", fmt.Errorf("decode authored resource header %q: %w", path, err)
	}
	if strings.TrimSpace(header.APIVersion) != projectAPIVersion {
		return "", fmt.Errorf("%s: apiVersion = %q, want %q", path, header.APIVersion, projectAPIVersion)
	}
	kind := strings.TrimSpace(header.Kind)
	if kind == "" {
		return "", fmt.Errorf("%s: kind is required", path)
	}
	return kind, nil
}

func rejectRemovedDirectory(root, directory string) error {
	paths, err := discoverYAMLFiles(root, directory)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	kind, err := authoredResourceKind(paths[0])
	if err != nil {
		return err
	}
	return removedAuthoringKindError(paths[0], kind)
}

func removedAuthoringKindError(path, kind string) error {
	return fmt.Errorf("%s: authored kind %q was removed from analytics source; administer it through the instance control API", path, kind)
}

func directoryForAuthoredKind(kind string) string {
	for _, owned := range authoredResourceDirectories {
		if owned.kind == kind {
			return owned.directory
		}
	}
	if kind == "DataPolicy" {
		return "access"
	}
	return "the instance control API"
}

func sortDiscoveredResources(resources []discoveredAuthoredResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].kind == resources[j].kind {
			return resources[i].path < resources[j].path
		}
		return resources[i].kind < resources[j].kind
	})
}
