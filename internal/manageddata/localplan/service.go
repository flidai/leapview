// Package localplan discovers and hashes local files for managed data revisions.
package localplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/manageddata"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type Connection struct {
	// ID is the stable authored resource identity. The map key remains the
	// human-facing name so callers may select by name, while synchronization
	// always returns the canonical ID to the target API.
	ID    string
	Kind  string
	Root  string
	Scope string
}

type Source struct {
	Connection string
	Path       string
	Format     string
}

type Project struct {
	Connections map[string]Connection
	Sources     map[string]Source
}

type ProjectLoader func(string) (Project, error)

type Request struct {
	ProjectPath string
	Connection  string
	From        string
	Previous    *manageddata.Manifest
	Limits      manageddata.Limits
}

type Result struct {
	// Connection is the canonical authored resource ID used by target APIs.
	Connection string
	// ConnectionName retains the symbolic selector for human-facing output.
	ConnectionName string
	Root           string
	Sources        []string
	Manifest       manageddata.Manifest
	Diff           manageddata.Diff
}

type Service struct {
	loadProject ProjectLoader
	files       fileSystem
}

func NewService(loadProject ProjectLoader) *Service {
	return &Service{
		loadProject: loadProject,
		files:       osFileSystem{},
	}
}

func (s *Service) Plan(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.ProjectPath) == "" {
		return Result{}, fmt.Errorf("project path is required")
	}
	if strings.TrimSpace(request.Connection) == "" {
		return Result{}, fmt.Errorf("connection is required")
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("context is required")
	}
	if s == nil || s.loadProject == nil || s.files == nil {
		return Result{}, fmt.Errorf("local planner service is not configured")
	}

	project, err := s.loadProject(request.ProjectPath)
	if err != nil {
		return Result{}, fmt.Errorf("load project: %w", err)
	}
	connectionName, connection, connectionID, err := resolveConnection(project, request.Connection)
	if err != nil {
		return Result{}, err
	}
	if request.Previous != nil {
		if err := request.Previous.Validate(manageddata.Limits{}); err != nil {
			return Result{}, fmt.Errorf("previous manifest: %w", err)
		}
	}

	root, err := planningRoot(request.From, connection)
	if err != nil {
		return Result{}, fmt.Errorf("connection %q root: %w", request.Connection, err)
	}
	if err := validateRoot(s.files, root); err != nil {
		return Result{}, fmt.Errorf("connection %q root: %w", request.Connection, err)
	}

	sourceNames := selectedSourceNames(project, connectionName)
	files, err := discoverFiles(s.files, root, sourceNames, project)
	if err != nil {
		return Result{}, err
	}

	logicalPaths := make([]string, 0, len(files))
	for logicalPath := range files {
		logicalPaths = append(logicalPaths, logicalPath)
	}
	sort.Strings(logicalPaths)
	manifest := manageddata.Manifest{Files: make([]manageddata.File, 0, len(logicalPaths))}
	for _, logicalPath := range logicalPaths {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err := validateExactFile(s.files, root, logicalPath); err != nil {
			return Result{}, fmt.Errorf("validate %q before hashing: %w", logicalPath, err)
		}
		file, err := hashStableFile(ctx, s.files, files[logicalPath])
		if err != nil {
			return Result{}, fmt.Errorf("hash %q: %w", logicalPath, err)
		}
		if err := validateExactFile(s.files, root, logicalPath); err != nil {
			return Result{}, fmt.Errorf("validate %q after hashing: %w", logicalPath, err)
		}
		file.Path = logicalPath
		manifest.Files = append(manifest.Files, file)
	}
	if err := manifest.Validate(request.Limits); err != nil {
		return Result{}, err
	}

	previous := manageddata.Manifest{}
	if request.Previous != nil {
		previous = *request.Previous
	}
	return Result{
		Connection:     connectionID,
		ConnectionName: connectionName,
		Root:           root,
		Sources:        sourceNames,
		Manifest:       manifest,
		Diff:           manageddata.DiffManifests(previous, manifest),
	}, nil
}

// resolveConnection accepts either the authored display name or its explicit
// stable resource ID. It never derives an ID from the name or from a target
// lookup; the project projection must carry the authored ID.
func resolveConnection(project Project, selector string) (string, Connection, string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", Connection{}, "", fmt.Errorf("connection is required")
	}
	connectionName := selector
	connection, ok := project.Connections[selector]
	if !ok {
		for name, candidate := range project.Connections {
			if strings.TrimSpace(candidate.ID) == selector {
				connectionName, connection, ok = name, candidate, true
				break
			}
		}
	}
	if !ok {
		return "", Connection{}, "", fmt.Errorf("project has unknown connection %q", selector)
	}
	if connection.ID == "" || connection.ID != strings.TrimSpace(connection.ID) {
		return "", Connection{}, "", fmt.Errorf("connection %q has no canonical stable ID", connectionName)
	}
	connectionID, err := projectgraph.NewResourceID(connection.ID)
	if err != nil {
		return "", Connection{}, "", fmt.Errorf("connection %q has invalid stable ID %q: %w", connectionName, connection.ID, err)
	}
	return connectionName, connection, connectionID.String(), nil
}

func planningRoot(from string, connection Connection) (string, error) {
	if connection.Kind != "managed" {
		return "", fmt.Errorf("connection kind %q cannot plan managed data", connection.Kind)
	}
	if strings.TrimSpace(connection.Root) != "" || strings.TrimSpace(connection.Scope) != "" {
		return "", fmt.Errorf("managed connection cannot define root or scope")
	}
	if strings.TrimSpace(from) == "" {
		return "", fmt.Errorf("from is required for managed connection")
	}
	return absoluteRoot(from)
}

func absoluteRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func validateRoot(files fileSystem, root string) error {
	info, err := files.Lstat(root)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symbolic link", root)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return nil
}

func selectedSourceNames(project Project, connection string) []string {
	names := make([]string, 0)
	for name, source := range project.Sources {
		if source.Connection == connection {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

type sourcePattern struct {
	source  string
	pattern string
	matched bool
}

func discoverFiles(files fileSystem, root string, sourceNames []string, project Project) (map[string]string, error) {
	discovered := make(map[string]string)
	patterns := make([]sourcePattern, 0)
	for _, sourceName := range sourceNames {
		source := project.Sources[sourceName]
		if !isLocalPath(source.Path) {
			return nil, fmt.Errorf("source %q managed path must be local: %q", sourceName, source.Path)
		}
		if filepath.IsAbs(filepath.FromSlash(source.Path)) {
			return nil, fmt.Errorf("source %q managed path must be relative: %q", sourceName, source.Path)
		}
		logicalPath, glob, err := normalizeSourcePath(root, source.Path)
		if err != nil {
			return nil, fmt.Errorf("source %q path %q: %w", sourceName, source.Path, err)
		}
		if glob {
			patterns = append(patterns, sourcePattern{source: sourceName, pattern: logicalPath})
			continue
		}
		absolutePath := filepath.Join(root, filepath.FromSlash(logicalPath))
		if err := validateExactFile(files, root, logicalPath); err != nil {
			return nil, fmt.Errorf("source %q path %q: %w", sourceName, source.Path, err)
		}
		discovered[logicalPath] = absolutePath
	}

	if len(patterns) > 0 {
		err := files.WalkDir(root, func(absolutePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if absolutePath == root {
				return nil
			}
			relative, err := filepath.Rel(root, absolutePath)
			if err != nil {
				return err
			}
			logicalPath := filepath.ToSlash(relative)
			matched := matchingPatternIndexes(patterns, logicalPath)
			if entry.Type()&os.ModeSymlink != 0 {
				if len(matched) > 0 {
					return fmt.Errorf("path %q is a symbolic link", logicalPath)
				}
				return nil
			}
			if entry.IsDir() {
				if len(matched) > 0 {
					return fmt.Errorf("path %q is not a regular file", logicalPath)
				}
				return nil
			}
			if len(matched) == 0 {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("path %q is not a regular file", logicalPath)
			}
			for _, index := range matched {
				patterns[index].matched = true
			}
			discovered[logicalPath] = absolutePath
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, pattern := range patterns {
		if !pattern.matched {
			return nil, fmt.Errorf("source %q pattern %q matched no files", pattern.source, pattern.pattern)
		}
	}
	return discovered, nil
}

func isLocalPath(value string) bool {
	for _, prefix := range []string{"s3://", "r2://", "gcs://", "gs://", "az://", "azure://", "abfss://", "http://", "https://", "file://"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	return !strings.Contains(value, "://")
}

func normalizeSourcePath(root, value string) (string, bool, error) {
	if value == "" {
		return "", false, fmt.Errorf("path is required")
	}
	if strings.Contains(value, "\\") {
		return "", false, fmt.Errorf("path must use forward slashes")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	var relative string
	if filepath.IsAbs(cleaned) {
		var err error
		relative, err = filepath.Rel(root, cleaned)
		if err != nil {
			return "", false, err
		}
	} else {
		relative = cleaned
	}
	if relative == "." || relative == "" || pathEscapesRoot(relative) {
		return "", false, fmt.Errorf("path escapes connection root %q", root)
	}
	logicalPath := filepath.ToSlash(relative)
	isGlob := strings.ContainsAny(logicalPath, "*?[")
	if isGlob {
		if err := validateGlobPattern(logicalPath); err != nil {
			return "", false, err
		}
	}
	return logicalPath, isGlob, nil
}

func pathEscapesRoot(relative string) bool {
	return relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative)
}

func validateGlobPattern(pattern string) error {
	for _, segment := range strings.Split(pattern, "/") {
		if strings.Contains(segment, "**") && segment != "**" {
			return fmt.Errorf("recursive wildcard must be a complete path segment")
		}
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, "value"); err != nil {
			return fmt.Errorf("invalid glob pattern: %w", err)
		}
	}
	return nil
}

func matchingPatternIndexes(patterns []sourcePattern, logicalPath string) []int {
	indexes := make([]int, 0, 1)
	for index := range patterns {
		if matchGlob(patterns[index].pattern, logicalPath) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func matchGlob(pattern, name string) bool {
	patternSegments := strings.Split(pattern, "/")
	nameSegments := strings.Split(name, "/")
	type position struct{ pattern, name int }
	known := make(map[position]bool)
	results := make(map[position]bool)
	var match func(int, int) bool
	match = func(patternIndex, nameIndex int) bool {
		current := position{pattern: patternIndex, name: nameIndex}
		if known[current] {
			return results[current]
		}
		known[current] = true
		var result bool
		switch {
		case patternIndex == len(patternSegments):
			result = nameIndex == len(nameSegments)
		case patternSegments[patternIndex] == "**":
			result = match(patternIndex+1, nameIndex) || nameIndex < len(nameSegments) && match(patternIndex, nameIndex+1)
		case nameIndex < len(nameSegments):
			matched, err := path.Match(patternSegments[patternIndex], nameSegments[nameIndex])
			result = err == nil && matched && match(patternIndex+1, nameIndex+1)
		}
		results[current] = result
		return result
	}
	return match(0, 0)
}

func validateExactFile(files fileSystem, root, logicalPath string) error {
	if err := validateRoot(files, root); err != nil {
		return err
	}
	current := root
	parts := strings.Split(filepath.FromSlash(logicalPath), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := files.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link", current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%s is not a directory", current)
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", current)
		}
	}
	return nil
}

func hashStableFile(ctx context.Context, files fileSystem, name string) (manageddata.File, error) {
	before, err := files.Lstat(name)
	if err != nil {
		return manageddata.File{}, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return manageddata.File{}, fmt.Errorf("file is not a regular non-symlink file")
	}
	file, err := files.Open(name)
	if err != nil {
		return manageddata.File{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return manageddata.File{}, err
	}
	if !sameFileState(before, opened) {
		return manageddata.File{}, fmt.Errorf("file changed before hashing")
	}

	digest := sha256.New()
	bytesRead, err := io.Copy(digest, contextReader{ctx: ctx, reader: file})
	if err != nil {
		return manageddata.File{}, err
	}
	if bytesRead != before.Size() {
		return manageddata.File{}, fmt.Errorf("file size changed while hashing")
	}
	afterOpen, err := file.Stat()
	if err != nil {
		return manageddata.File{}, err
	}
	afterPath, err := files.Lstat(name)
	if err != nil {
		return manageddata.File{}, err
	}
	if !sameFileState(before, afterOpen) || !sameFileState(before, afterPath) {
		return manageddata.File{}, fmt.Errorf("file changed while hashing")
	}
	return manageddata.File{Size: before.Size(), SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func sameFileState(first, second os.FileInfo) bool {
	return first.Mode() == second.Mode() &&
		first.Size() == second.Size() &&
		first.ModTime() == second.ModTime() &&
		os.SameFile(first, second)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type openedFile interface {
	io.Reader
	io.Closer
	Stat() (os.FileInfo, error)
}

type fileSystem interface {
	Lstat(string) (os.FileInfo, error)
	Open(string) (openedFile, error)
	WalkDir(string, fs.WalkDirFunc) error
}

type osFileSystem struct{}

func (osFileSystem) Lstat(name string) (os.FileInfo, error) {
	return os.Lstat(name)
}

func (osFileSystem) Open(name string) (openedFile, error) {
	return os.Open(name)
}

func (osFileSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}
