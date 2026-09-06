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

// authoredResourceDirectory is the complete public analytics authoring
// registry. Directory ownership replaces the former Project include lists.
// Adding another public kind requires an explicit contract decision.
type authoredResourceDirectory struct {
	directory string
	kind      string
}

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
	root      string
	resources []discoveredAuthoredResource
}

// discoverAuthoredResources discovers YAML resources recursively beneath the
// six fixed authoring directories. Discovery is deterministic and independent
// of file names or include globs. Removed access/publication authoring is
// rejected explicitly so it cannot be silently omitted from a bundle.
func discoverAuthoredResources(sourceRoot string) (authoredResourceDiscovery, error) {
	root, err := authoredSourceRoot(sourceRoot)
	if err != nil {
		return authoredResourceDiscovery{}, err
	}
	if err := rejectLegacyProjectManifest(root); err != nil {
		return authoredResourceDiscovery{}, err
	}
	if err := rejectUnownedLeapViewResources(root); err != nil {
		return authoredResourceDiscovery{}, err
	}

	result := authoredResourceDiscovery{root: root}
	for _, owned := range authoredResourceDirectories {
		paths, discoverErr := discoverYAMLFiles(root, owned.directory)
		if discoverErr != nil {
			return authoredResourceDiscovery{}, discoverErr
		}
		for _, path := range paths {
			kind, recognized, headerErr := authoredResourceKind(path)
			if headerErr != nil {
				return authoredResourceDiscovery{}, headerErr
			}
			// Owned directories may contain source-layout YAML fragments (for
			// example dashboard visual/page includes). Only LeapView envelopes
			// are standalone resources; unrelated YAML is left for its owning
			// resource's include expansion.
			if !recognized {
				continue
			}
			if kind == "Project" {
				return authoredResourceDiscovery{}, projectAuthoringRemovedError(path)
			}
			if isRemovedAuthoringKind(kind) {
				return authoredResourceDiscovery{}, removedAuthoringKindError(path, kind)
			}
			if kind != owned.kind {
				return authoredResourceDiscovery{}, fmt.Errorf("%s: authored kind %q belongs in %s/, not %s/", path, kind, directoryForAuthoredKind(kind), owned.directory)
			}
			result.resources = append(result.resources, discoveredAuthoredResource{kind: kind, path: path})
		}
	}
	sortDiscoveredResources(result.resources)
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
	for _, name := range []string{"leapview.yaml", "leapview.yml"} {
		path := filepath.Join(root, name)
		_, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect legacy Project manifest %q: %w", path, err)
		}
		return fmt.Errorf("Project authoring was removed; pass the source root directory %q and delete %q", root, path)
	}
	return nil
}

// rejectUnownedLeapViewResources prevents a legacy or removed LeapView
// envelope from disappearing merely because it lives outside one of the six
// conventional directories. Unrelated YAML (for example dbt configuration)
// is ignored unless it declares LeapView's API version.
func rejectUnownedLeapViewResources(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("scan source root at %q: %w", path, walkErr)
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return fmt.Errorf("resolve source-root entry %q: %w", sourceRootRelative(root, path), resolveErr)
			}
			if err := ensureInsideSourceRoot(root, resolved); err != nil {
				return fmt.Errorf("source-root entry %q: %w", sourceRootRelative(root, path), err)
			}
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				return filepath.SkipDir
			}
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(relative) {
			return fmt.Errorf("source-root entry %q cannot be made relative to source root", relative)
		}
		if isOwnedAuthoredPath(relative) {
			return nil
		}
		kind, isLeapView, headerErr := resourceHeader(path)
		if headerErr != nil {
			// This scan is intentionally only a LeapView envelope probe. The
			// owning directory loader reports malformed authored YAML precisely.
			return nil
		}
		if !isLeapView {
			return nil
		}
		if kind == "Project" {
			return projectAuthoringRemovedError(path)
		}
		if isRemovedAuthoringKind(kind) {
			return removedAuthoringKindError(path, kind)
		}
		// Supported resources outside the six root-owned directories belong to
		// another nested source root or unrelated example and are not discovered
		// by this compilation. Project and removed control-plane kinds are still
		// rejected anywhere beneath the selected root above.
		return nil
	})
	return err
}

func projectAuthoringRemovedError(path string) error {
	return fmt.Errorf("%s: Project authoring was removed; place supported resources under their conventional directories and remove this Project manifest", path)
}

func resourceHeader(path string) (kind string, leapview bool, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(header.APIVersion) != sourceAPIVersion {
		return "", false, nil
	}
	return strings.TrimSpace(header.Kind), true, nil
}

func isOwnedAuthoredPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	for _, owned := range authoredResourceDirectories {
		prefix := owned.directory + "/"
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isRemovedAuthoringKind(kind string) bool {
	switch kind {
	case "Group", "RoleBinding", "Grant", "DataPolicy", "DashboardPublication":
		return true
	default:
		return false
	}
}

// discoverYAMLFiles walks all descendant directories, rather than using a
// shallow glob. Every encountered symlink is resolved and must remain inside
// the canonical source root; this includes YAML files linked from elsewhere.
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
	err = filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("discover authored resources at %q: %w", path, walkErr)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, resolveErr := filepath.EvalSymlinks(path)
			if resolveErr != nil {
				return fmt.Errorf("resolve authored resource %q: %w", sourceRootRelative(root, path), resolveErr)
			}
			if err := ensureInsideSourceRoot(root, resolved); err != nil {
				return fmt.Errorf("authored resource %q: %w", sourceRootRelative(root, path), err)
			}
			// WalkDir deliberately does not follow directory symlinks. Rejecting
			// them avoids an apparently successful, but incomplete, source set.
			resolvedInfo, statErr := os.Stat(path)
			if statErr != nil {
				return fmt.Errorf("inspect authored resource %q: %w", sourceRootRelative(root, path), statErr)
			}
			if resolvedInfo.IsDir() {
				return fmt.Errorf("authored resource directory symlink %q is not supported; use a directory within the source root", path)
			}
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if err := ensureInsideSourceRoot(root, path); err != nil {
			return fmt.Errorf("authored resource %q: %w", sourceRootRelative(root, path), err)
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(paths, func(i, j int) bool {
		return filepath.ToSlash(paths[i]) < filepath.ToSlash(paths[j])
	})
	return paths, nil
}

func ensureInsideSourceRoot(root, path string) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve source root %q: %w", root, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("resolves outside source root")
	}
	return nil
}

func sourceRootRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err == nil && !filepath.IsAbs(relative) {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(filepath.Base(path))
}

func authoredResourceKind(path string) (string, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("read authored resource %q: %w", path, err)
	}
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		return "", false, fmt.Errorf("decode authored resource header %q: %w", path, err)
	}
	apiVersion := strings.TrimSpace(header.APIVersion)
	kind := strings.TrimSpace(header.Kind)
	// Source-layout fragments are deliberately allowed only when neither
	// envelope field is present. A non-empty apiVersion is unambiguously a
	// resource document and must not be silently ignored when it is wrong.
	if apiVersion == "" && kind == "" {
		return "", false, nil
	}
	if apiVersion != sourceAPIVersion {
		return "", true, fmt.Errorf("%s: apiVersion %q is not supported; want %q", path, apiVersion, sourceAPIVersion)
	}
	if kind == "" {
		return "", true, fmt.Errorf("%s: kind is required", path)
	}
	return kind, true, nil
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
	return "the instance control API"
}

func sortDiscoveredResources(resources []discoveredAuthoredResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].kind == resources[j].kind {
			return filepath.ToSlash(resources[i].path) < filepath.ToSlash(resources[j].path)
		}
		return resources[i].kind < resources[j].kind
	})
}
