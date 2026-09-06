package compiler

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Keep the direct compiler boundary aligned with the native source admission
// limits. Native callers already enforce these limits, but the CLI and package
// API may compile a local checkout directly.
const (
	maxAuthoredSourceFiles           = 10_000
	maxAuthoredSourceEntries         = 100_000
	maxAuthoredSourceBytes     int64 = 64 << 20
	maxAuthoredSourceFileBytes       = 16 << 20
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
	reader    *snapshotSourceReader
	resources []discoveredAuthoredResource
}

type snapshottedSourceFile struct {
	relative string
	content  []byte
}

// discoverAuthoredResources discovers YAML resources recursively beneath the
// six fixed authoring directories. The source tree is read through one
// descriptor-bound os.Root and snapshotted before decoding. A concurrent
// rename or symlink swap therefore cannot redirect a later compiler read.
func discoverAuthoredResources(sourceRoot string) (authoredResourceDiscovery, error) {
	rootPath, root, err := openAuthoredSourceRoot(sourceRoot)
	if err != nil {
		return authoredResourceDiscovery{}, err
	}
	defer root.Close()

	if err := validateAuthoredDirectories(rootPath, root); err != nil {
		return authoredResourceDiscovery{}, err
	}
	if err := rejectLegacyProjectManifest(rootPath, root); err != nil {
		return authoredResourceDiscovery{}, err
	}
	reader, files, err := snapshotYAMLFiles(rootPath, root)
	if err != nil {
		return authoredResourceDiscovery{}, err
	}

	result := authoredResourceDiscovery{root: rootPath, reader: reader}
	for _, file := range files {
		path := reader.absolutePath(file.relative)
		owned, isOwned := authoredDirectoryForPath(file.relative)
		if !isOwned {
			kind, isLeapView, headerErr := resourceHeaderBytes(file.content)
			if headerErr != nil || !isLeapView {
				// Outside an owned directory this is only an envelope probe;
				// malformed unrelated YAML remains outside compiler ownership.
				continue
			}
			if kind == "Project" {
				return authoredResourceDiscovery{}, projectAuthoringRemovedError(path)
			}
			if isRemovedAuthoringKind(kind) {
				return authoredResourceDiscovery{}, removedAuthoringKindError(path, kind)
			}
			continue
		}

		kind, recognized, headerErr := authoredResourceKindBytes(path, file.content)
		if headerErr != nil {
			return authoredResourceDiscovery{}, headerErr
		}
		// Owned directories may contain dashboard fragments. Only LeapView
		// envelopes are standalone resources.
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
	sortDiscoveredResources(result.resources)
	return result, nil
}

func openAuthoredSourceRoot(sourceRoot string) (string, *os.Root, error) {
	sourceRoot = strings.TrimSpace(sourceRoot)
	if sourceRoot == "" {
		return "", nil, fmt.Errorf("analytics source root is required")
	}
	absolute, err := filepath.Abs(sourceRoot)
	if err != nil {
		return "", nil, fmt.Errorf("resolve analytics source root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		if projectConfigFile(absolute) {
			return "", nil, fmt.Errorf("Project authoring was removed; pass the source root directory %q instead of %q", filepath.Dir(absolute), absolute)
		}
		return "", nil, fmt.Errorf("resolve analytics source root %q: %w", absolute, err)
	}
	resolved = filepath.Clean(resolved)
	root, err := os.OpenRoot(resolved)
	if err != nil {
		if projectConfigFile(resolved) {
			return "", nil, fmt.Errorf("Project authoring was removed; pass the source root directory %q instead of %q", filepath.Dir(absolute), absolute)
		}
		return "", nil, fmt.Errorf("open analytics source root %q: %w", absolute, err)
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return "", nil, fmt.Errorf("stat analytics source root %q: %w", absolute, err)
	}
	if !info.IsDir() {
		root.Close()
		return "", nil, fmt.Errorf("analytics source root %q is not a directory", absolute)
	}
	return resolved, root, nil
}

func validateAuthoredDirectories(rootPath string, root *os.Root) error {
	for _, owned := range authoredResourceDirectories {
		entry, err := root.Lstat(owned.directory)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect authored resource directory %q: %w", filepath.Join(rootPath, owned.directory), err)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("authored resource directory symlink %q is not supported; use a directory within the source root", filepath.Join(rootPath, owned.directory))
		}
		if !entry.IsDir() {
			return fmt.Errorf("authored resource directory %q is not a directory", filepath.Join(rootPath, owned.directory))
		}
	}
	return nil
}

func rejectLegacyProjectManifest(rootPath string, root *os.Root) error {
	for _, name := range []string{"leapview.yaml", "leapview.yml"} {
		_, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect legacy Project manifest %q: %w", name, err)
		}
		return fmt.Errorf("Project authoring was removed; pass the source root directory %q and delete %q", rootPath, filepath.Join(rootPath, name))
	}
	return nil
}

func snapshotYAMLFiles(rootPath string, root *os.Root) (*snapshotSourceReader, []snapshottedSourceFile, error) {
	reader := &snapshotSourceReader{root: rootPath, files: make(map[string][]byte), paths: make(map[string]struct{})}
	files := make([]snapshottedSourceFile, 0)
	var total int64
	entryCount := 0
	fileCount := 0
	err := fs.WalkDir(root.FS(), ".", func(relative string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("scan source root at %q: %w", displaySourcePath(relative), walkErr)
		}
		if relative == "." {
			return nil
		}
		entryCount++
		if entryCount > maxAuthoredSourceEntries {
			return fmt.Errorf("analytics source exceeds %d filesystem entries", maxAuthoredSourceEntries)
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			info, err := root.Stat(filepath.FromSlash(relative))
			if err != nil {
				return fmt.Errorf("source-root entry %q resolves outside source root", displaySourcePath(relative))
			}
			if info.IsDir() {
				return fmt.Errorf("authored resource directory symlink %q is not supported; use a directory within the source root", displaySourcePath(relative))
			}
			return fmt.Errorf("authored source symlink %q is not supported; use a regular file within the source root", displaySourcePath(relative))
		}
		if entry.IsDir() {
			return nil
		}
		fileCount++
		if fileCount > maxAuthoredSourceFiles {
			return fmt.Errorf("analytics source exceeds %d files", maxAuthoredSourceFiles)
		}
		targetInfo, err := root.Lstat(filepath.FromSlash(relative))
		if err != nil {
			return fmt.Errorf("inspect authored source %q: %w", displaySourcePath(relative), err)
		}
		reader.paths[relative] = struct{}{}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		if !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("authored source %q is not a regular file", displaySourcePath(relative))
		}
		content, err := readRootRegularFile(root, relative)
		if err != nil {
			return fmt.Errorf("read authored source %q: %w", displaySourcePath(relative), err)
		}
		if int64(len(content)) > maxAuthoredSourceFileBytes {
			return fmt.Errorf("authored source %q exceeds %d byte file limit", displaySourcePath(relative), maxAuthoredSourceFileBytes)
		}
		if int64(len(content)) > maxAuthoredSourceBytes-total {
			return fmt.Errorf("analytics source exceeds %d byte total limit", maxAuthoredSourceBytes)
		}
		total += int64(len(content))
		reader.files[relative] = content
		files = append(files, snapshottedSourceFile{relative: relative, content: content})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return reader, files, nil
}

func readRootRegularFile(root *os.Root, relative string) ([]byte, error) {
	file, err := root.Open(filepath.FromSlash(relative))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > maxAuthoredSourceFileBytes {
		return nil, fmt.Errorf("path exceeds %d byte file limit", maxAuthoredSourceFileBytes)
	}
	content, err := io.ReadAll(io.LimitReader(file, maxAuthoredSourceFileBytes+1))
	if err != nil {
		return nil, err
	}
	return content, nil
}

func authoredDirectoryForPath(relative string) (authoredResourceDirectory, bool) {
	relative = filepath.ToSlash(filepath.Clean(relative))
	for _, owned := range authoredResourceDirectories {
		if strings.HasPrefix(relative, owned.directory+"/") {
			return owned, true
		}
	}
	return authoredResourceDirectory{}, false
}

func cleanSourceRelativePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) || isWindowsAbsolute(path) {
		return "", fmt.Errorf("path escapes source root")
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." {
			return "", fmt.Errorf("path escapes source root")
		}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes source root")
	}
	return clean, nil
}

func isWindowsAbsolute(path string) bool {
	return len(path) >= 3 && ((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func displaySourcePath(relative string) string {
	relative = filepath.ToSlash(relative)
	if relative == "." || relative == "" {
		return "."
	}
	return relative
}

func projectAuthoringRemovedError(path string) error {
	return fmt.Errorf("%s: Project authoring was removed; place supported resources under their conventional directories and remove this Project manifest", path)
}

func resourceHeaderBytes(content []byte) (kind string, leapview bool, err error) {
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

func authoredResourceKindBytes(path string, content []byte) (string, bool, error) {
	var header struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       string `yaml:"kind"`
	}
	if err := yaml.Unmarshal(content, &header); err != nil {
		return "", false, fmt.Errorf("decode authored resource header %q: %w", path, err)
	}
	apiVersion := strings.TrimSpace(header.APIVersion)
	kind := strings.TrimSpace(header.Kind)
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

func isRemovedAuthoringKind(kind string) bool {
	switch kind {
	case "Group", "RoleBinding", "Grant", "DataPolicy", "DashboardPublication":
		return true
	default:
		return false
	}
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
