// Package developmentinput validates explicit, reproducible local fixture inputs.
package developmentinput

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	Version             = 1
	DefaultRelativePath = ".leapview/development-inputs.yaml"
	MaxDocumentBytes    = 1 << 20
	MaxFiles            = 256
	MaxTotalBytes       = 64 << 20
	MaxRows             = 1_000_000
	maxYAMLDepth        = 32
	maxYAMLNodes        = 32768
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var generatorPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var portablePathPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

type Manifest struct {
	Version int              `yaml:"version"`
	Inputs  map[string]Input `yaml:"inputs"`
}

type Input struct {
	Connection string          `yaml:"connection"`
	From       string          `yaml:"from"`
	Provenance Provenance      `yaml:"provenance"`
	Files      map[string]File `yaml:"files"`
}

type Provenance struct {
	Kind      string `yaml:"kind"`
	Generator string `yaml:"generator"`
	Rows      int64  `yaml:"rows"`
	Bounded   bool   `yaml:"bounded"`
}

type File struct {
	SHA256    string `yaml:"sha256"`
	SizeBytes int64  `yaml:"sizeBytes"`
}

type Selected struct {
	Name        string
	ProjectRoot string
	Connection  string
	Root        string
	Provenance  Provenance
	Files       []SelectedFile
}

type SelectedFile struct {
	Path      string
	Absolute  string
	SHA256    string
	SizeBytes int64
}

// Load verifies the exact declared files before they can be planned or staged.
func Load(projectRoot, name string) (Selected, error) {
	projectFS, root, err := openCanonicalRoot(projectRoot)
	if err != nil {
		return Selected{}, fmt.Errorf("development input project root: %w", err)
	}
	defer projectFS.Close()
	if err := rejectPathSymlinks(projectFS, filepath.FromSlash(DefaultRelativePath)); err != nil {
		return Selected{}, fmt.Errorf("development inputs: %w", err)
	}
	content, err := readBounded(projectFS, DefaultRelativePath)
	if err != nil {
		return Selected{}, fmt.Errorf("development inputs: %w", err)
	}
	if !utf8.Valid(content) {
		return Selected{}, errors.New("development inputs must be UTF-8")
	}
	if err := rejectUnsafeYAML(content); err != nil {
		return Selected{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Selected{}, fmt.Errorf("development inputs schema: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Selected{}, errors.New("development inputs must contain one YAML document")
		}
		return Selected{}, fmt.Errorf("development inputs schema: %w", err)
	}
	if manifest.Version != Version {
		return Selected{}, fmt.Errorf("unsupported development inputs version %d", manifest.Version)
	}
	if name != strings.TrimSpace(name) || !namePattern.MatchString(name) {
		return Selected{}, errors.New("development input name is invalid")
	}
	input, ok := manifest.Inputs[name]
	if !ok {
		return Selected{}, fmt.Errorf("unknown development input %q", name)
	}
	if !namePattern.MatchString(input.Connection) {
		return Selected{}, fmt.Errorf("development input %q has invalid connection", name)
	}
	if input.Provenance.Kind != "synthetic" || !generatorPattern.MatchString(input.Provenance.Generator) || input.Provenance.Rows < 1 || input.Provenance.Rows > MaxRows || !input.Provenance.Bounded {
		return Selected{}, fmt.Errorf("development input %q requires bounded synthetic provenance", name)
	}
	from, err := safeRelativePath(input.From)
	if err != nil {
		return Selected{}, fmt.Errorf("development input %q root: %w", name, err)
	}
	if filepath.ToSlash(from) != input.From {
		return Selected{}, fmt.Errorf("development input %q root must be canonical", name)
	}
	if !portablePathPattern.MatchString(input.From) {
		return Selected{}, fmt.Errorf("development input %q root is not portable", name)
	}
	if err := rejectPathSymlinks(projectFS, from); err != nil {
		return Selected{}, fmt.Errorf("development input %q root: %w", name, err)
	}
	inputRoot := filepath.Join(root, from)
	inputFS, err := projectFS.OpenRoot(input.From)
	if err != nil {
		return Selected{}, fmt.Errorf("development input %q root: %w", name, err)
	}
	defer inputFS.Close()
	inputInfo, err := inputFS.Stat(".")
	if err != nil || !inputInfo.IsDir() {
		return Selected{}, fmt.Errorf("development input %q root is not a directory", name)
	}
	if !within(root, inputRoot) {
		return Selected{}, fmt.Errorf("development input %q root escapes project", name)
	}
	if len(input.Files) == 0 {
		return Selected{}, fmt.Errorf("development input %q declares no files", name)
	}
	if len(input.Files) > MaxFiles {
		return Selected{}, fmt.Errorf("development input %q declares more than %d files", name, MaxFiles)
	}
	paths := make([]string, 0, len(input.Files))
	for path := range input.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	selected := Selected{Name: name, ProjectRoot: root, Connection: input.Connection, Root: inputRoot, Provenance: input.Provenance, Files: make([]SelectedFile, 0, len(paths))}
	declared := make(map[string]File, len(paths))
	var totalBytes int64
	for _, path := range paths {
		relative, err := safeRelativePath(path)
		if err != nil {
			return Selected{}, fmt.Errorf("development input %q file path: %w", name, err)
		}
		logical := filepath.ToSlash(relative)
		if logical != path {
			return Selected{}, fmt.Errorf("development input %q file path %q must be canonical", name, path)
		}
		if !portablePathPattern.MatchString(path) {
			return Selected{}, fmt.Errorf("development input %q file path %q is not portable", name, path)
		}
		declaration := input.Files[path]
		if !digestPattern.MatchString(declaration.SHA256) || declaration.SizeBytes < 1 {
			return Selected{}, fmt.Errorf("development input %q file %q has invalid identity", name, logical)
		}
		if declaration.SizeBytes > MaxTotalBytes-totalBytes {
			return Selected{}, fmt.Errorf("development input %q exceeds %d bytes", name, MaxTotalBytes)
		}
		totalBytes += declaration.SizeBytes
		if _, exists := declared[logical]; exists {
			return Selected{}, fmt.Errorf("development input %q declares duplicate canonical file %q", name, logical)
		}
		declared[logical] = declaration
	}
	if err := verifyInventory(inputFS, declared); err != nil {
		return Selected{}, fmt.Errorf("development input %q: %w", name, err)
	}
	for _, path := range paths {
		declaration := input.Files[path]
		absolute := filepath.Join(inputRoot, filepath.FromSlash(path))
		if err := verifyFile(inputFS, path, declaration); err != nil {
			return Selected{}, fmt.Errorf("development input %q file %q: %w", name, path, err)
		}
		selected.Files = append(selected.Files, SelectedFile{Path: path, Absolute: absolute, SHA256: declaration.SHA256, SizeBytes: declaration.SizeBytes})
	}
	return selected, nil
}

func readBounded(root *os.Root, path string) ([]byte, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("manifest must be a regular file, not a symlink")
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("manifest changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxDocumentBytes {
		return nil, errors.New("document exceeds one MiB")
	}
	return content, nil
}

func rejectPathSymlinks(root *os.Root, relative string) error {
	current := ""
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symbolic link", filepath.ToSlash(relative))
		}
	}
	return nil
}

func rejectUnsafeYAML(content []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return fmt.Errorf("development inputs YAML: %w", err)
	}
	nodes := 0
	var visit func(*yaml.Node, int) error
	visit = func(node *yaml.Node, depth int) error {
		nodes++
		if depth > maxYAMLDepth || nodes > maxYAMLNodes {
			return errors.New("development inputs YAML exceeds structural limits")
		}
		if node.Alias != nil || node.Kind == yaml.AliasNode || node.Tag == "!!merge" || node.Value == "<<" {
			return errors.New("development inputs do not allow aliases or merge keys")
		}
		if node.Tag != "" && !allowedYAMLTag(node.Tag) {
			return fmt.Errorf("development inputs do not allow YAML tag %q at line %d", node.Tag, node.Line)
		}
		if node.Style&yaml.TaggedStyle != 0 {
			return fmt.Errorf("development inputs do not allow explicit YAML tags at line %d", node.Line)
		}
		if node.Kind == yaml.MappingNode {
			seen := map[string]struct{}{}
			for index := 0; index < len(node.Content); index += 2 {
				key := node.Content[index]
				if _, exists := seen[key.Value]; exists {
					return fmt.Errorf("development inputs contain duplicate key at line %d", key.Line)
				}
				seen[key.Value] = struct{}{}
			}
		}
		for _, child := range node.Content {
			if err := visit(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(&root, 0)
}

func allowedYAMLTag(tag string) bool {
	switch tag {
	case "!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null",
		"tag:yaml.org,2002:map", "tag:yaml.org,2002:seq", "tag:yaml.org,2002:str",
		"tag:yaml.org,2002:int", "tag:yaml.org,2002:bool", "tag:yaml.org,2002:null":
		return true
	default:
		return false
	}
}

func openCanonicalRoot(path string) (*os.Root, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", errors.New("path must be a real directory, not a symlink")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, "", err
	}
	canonicalInfo, err := os.Stat(canonical)
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(info, canonicalInfo) {
		return nil, "", errors.New("path changed while resolving")
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, "", err
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(canonicalInfo, openedInfo) {
		root.Close()
		return nil, "", errors.New("path changed while opening")
	}
	return root, canonical, nil
}

func safeRelativePath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || filepath.IsAbs(filepath.FromSlash(value)) {
		return "", errors.New("path must be a relative forward-slash path")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes its declared root")
	}
	return clean, nil
}

func within(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func verifyFile(root *os.Root, path string, declaration File) error {
	info, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("must be a regular file, not a symlink")
	}
	if info.Size() != declaration.SizeBytes {
		return fmt.Errorf("size does not match declared provenance")
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) || openedInfo.Size() != declaration.SizeBytes {
		return errors.New("file changed while opening provenance")
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, declaration.SizeBytes+1))
	if err != nil {
		return err
	}
	if written != declaration.SizeBytes {
		return errors.New("size changed while verifying provenance")
	}
	if hex.EncodeToString(hasher.Sum(nil)) != declaration.SHA256 {
		return errors.New("digest does not match declared provenance")
	}
	return nil
}

func verifyInventory(root *os.Root, declared map[string]File) error {
	seen := make(map[string]struct{}, len(declared))
	if err := fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		logical := filepath.ToSlash(path)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symbolic link", logical)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("path %q is not a regular file", logical)
		}
		if _, ok := declared[logical]; !ok {
			return fmt.Errorf("undeclared file %q", logical)
		}
		seen[logical] = struct{}{}
		return nil
	}); err != nil {
		return err
	}
	for logical := range declared {
		if _, ok := seen[logical]; !ok {
			return fmt.Errorf("declared file %q is missing", logical)
		}
	}
	return nil
}
