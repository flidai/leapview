// Command duckdbsqldocsgen generates the SQL-facing documentation overlay
// from the pinned DuckDB core_functions JSON snapshot.
//
// The source JSON contains C++ implementation fields (struct and
// extra_functions). They are intentionally decoded but never emitted.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/pkg/duckdbsql"
)

const (
	defaultSource = "pkg/duckdbsql/internal/upstream/function_docs/extension/core_functions"
	defaultLock   = "pkg/duckdbsql/internal/upstream/function_docs.lock.json"
	defaultOutput = "pkg/duckdbsql/function_docs_generated.go"
)

type sourceLock struct {
	DuckDBVersion      string            `json:"duckdb_version"`
	DuckDBSourceCommit string            `json:"duckdb_source_commit"`
	SourceRoot         string            `json:"source_root"`
	Files              map[string]string `json:"files"`
}

// sourceFunction deliberately omits struct and extra_functions. Those keys
// describe C++ implementation details and are not SQL documentation.
type sourceFunction struct {
	Name        string                  `json:"name"`
	Parameters  string                  `json:"parameters"`
	Description string                  `json:"description"`
	Example     stringOrStrings         `json:"example"`
	Examples    []string                `json:"examples"`
	Aliases     []string                `json:"aliases"`
	Categories  []string                `json:"categories"`
	Type        string                  `json:"type"`
	Variants    []sourceFunctionVariant `json:"variants"`
	// These fields drive DuckDB's C++ registration generator, not its SQL
	// documentation. They are decoded so every upstream field is accounted
	// for, then deliberately omitted from the public metadata artifact.
	Struct         json.RawMessage `json:"struct"`
	ExtraFunctions json.RawMessage `json:"extra_functions"`
}

type sourceFunctionVariant struct {
	Parameters  []sourceParameter `json:"parameters"`
	Description string            `json:"description"`
	Example     stringOrStrings   `json:"example"`
	Examples    []string          `json:"examples"`
	Categories  []string          `json:"categories"`
}

type sourceParameter struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// stringOrStrings accepts the two forms used by the pinned source JSON. A
// singular example is retained in Example; an examples array is retained in
// Examples.
type stringOrStrings string

func (s *stringOrStrings) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*s = stringOrStrings(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return fmt.Errorf("example must be string or string array: %w", err)
	}
	if len(many) > 0 {
		*s = stringOrStrings(many[0])
	}
	return nil
}

type documentationInput struct {
	Docs  []duckdbsql.FunctionDocumentation
	Files []duckdbsql.FunctionDocumentationSourceFile
	Lock  sourceLock
}

func main() {
	var source, lockPath, output string
	var check bool
	flag.StringVar(&source, "source", "", "DuckDB extension/core_functions source root (validates pinned commit and hashes)")
	flag.StringVar(&lockPath, "lock", defaultLock, "source provenance lock path")
	flag.StringVar(&output, "output", defaultOutput, "generated Go output path")
	flag.BoolVar(&check, "check", false, "verify generated output is current")
	flag.Parse()

	sourceRoot := defaultSource
	requireCommit := false
	if source != "" {
		sourceRoot = source
		requireCommit = true
	}
	input, err := loadInput(sourceRoot, lockPath, requireCommit)
	if err != nil {
		fatal(err)
	}
	content, err := render(input)
	if err != nil {
		fatal(err)
	}
	if check {
		got, err := os.ReadFile(repoPath(output))
		if err != nil {
			fatal(fmt.Errorf("read generated output: %w", err))
		}
		if string(got) != content {
			fatal(fmt.Errorf("%s is stale; run task duckdbsql:generate", output))
		}
		return
	}
	output = repoPath(output)
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(output, []byte(content), 0o644); err != nil {
		fatal(fmt.Errorf("write generated output: %w", err))
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "duckdbsqldocsgen:", err)
	os.Exit(1)
}

func loadInput(sourceRoot, lockPath string, requireCommit bool) (documentationInput, error) {
	sourceRoot = repoPath(sourceRoot)
	lockPath = repoPath(lockPath)
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return documentationInput{}, fmt.Errorf("read source lock: %w", err)
	}
	var lock sourceLock
	if err := json.Unmarshal(lockBytes, &lock); err != nil {
		return documentationInput{}, fmt.Errorf("decode source lock: %w", err)
	}
	if lock.DuckDBVersion == "" || lock.DuckDBSourceCommit == "" || lock.SourceRoot == "" || len(lock.Files) == 0 {
		return documentationInput{}, errors.New("source lock is incomplete")
	}
	if err := validateSource(sourceRoot, lock, requireCommit); err != nil {
		return documentationInput{}, err
	}

	docs := make([]duckdbsql.FunctionDocumentation, 0)
	files := make([]duckdbsql.FunctionDocumentationSourceFile, 0, len(lock.Files))
	paths := make([]string, 0, len(lock.Files))
	for path := range lock.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		absolute := filepath.Join(sourceRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(absolute)
		if err != nil {
			return documentationInput{}, fmt.Errorf("read %s: %w", rel, err)
		}
		entries, err := decodeSourceFunctions(data)
		if err != nil {
			return documentationInput{}, fmt.Errorf("decode %s: %w", rel, err)
		}
		functionType, err := functionTypeForPath(rel)
		if err != nil {
			return documentationInput{}, err
		}
		for i, entry := range entries {
			if entry.Name == "" {
				return documentationInput{}, fmt.Errorf("%s entry %d has empty name", rel, i)
			}
			if functionType == "" {
				return documentationInput{}, fmt.Errorf("%s entry %s has unknown function type", rel, entry.Name)
			}
			if entry.Type == "" {
				return documentationInput{}, fmt.Errorf("%s entry %s has empty source type", rel, entry.Name)
			}
			doc := duckdbsql.FunctionDocumentation{
				Name: entry.Name, Parameters: entry.Parameters, Description: entry.Description,
				Example: string(entry.Example), Examples: append([]string(nil), entry.Examples...),
				Aliases: append([]string(nil), entry.Aliases...), Categories: append([]string(nil), entry.Categories...),
				Category: filepath.Base(filepath.Dir(rel)), FunctionType: entry.Type, Kind: functionType,
				SourcePath: filepath.ToSlash(filepath.Join(lock.SourceRoot, rel)),
			}
			for _, variant := range entry.Variants {
				out := duckdbsql.FunctionDocumentationVariant{
					Description: variant.Description, Example: string(variant.Example),
					Examples: append([]string(nil), variant.Examples...), Categories: append([]string(nil), variant.Categories...),
				}
				for _, parameter := range variant.Parameters {
					out.Parameters = append(out.Parameters, duckdbsql.FunctionDocumentationParameter{Name: parameter.Name, Type: parameter.Type})
				}
				doc.Variants = append(doc.Variants, out)
			}
			docs = append(docs, doc)
		}
		files = append(files, duckdbsql.FunctionDocumentationSourceFile{Path: filepath.ToSlash(filepath.Join(lock.SourceRoot, rel)), SHA256: lock.Files[rel]})
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Name != docs[j].Name {
			return docs[i].Name < docs[j].Name
		}
		if docs[i].FunctionType != docs[j].FunctionType {
			return docs[i].FunctionType < docs[j].FunctionType
		}
		return docs[i].SourcePath < docs[j].SourcePath
	})
	return documentationInput{Docs: docs, Files: files, Lock: lock}, nil
}

func decodeSourceFunctions(data []byte) ([]sourceFunction, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var entries []sourceFunction
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("source contains trailing JSON values")
		}
		return nil, err
	}
	return entries, nil
}

func repoPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	working, err := os.Getwd()
	if err != nil {
		return path
	}
	for dir := working; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path
		}
	}
}

func functionTypeForPath(path string) (string, error) {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < 3 || parts[0] != "aggregate" && parts[0] != "scalar" {
		return "", fmt.Errorf("source path %q is outside aggregate/scalar", path)
	}
	switch parts[0] {
	case "aggregate":
		return "aggregate", nil
	case "scalar":
		return "scalar", nil
	default:
		return "", nil
	}
}

func validateSource(sourceRoot string, lock sourceLock, requireCommit bool) error {
	sourceRoot = repoPath(sourceRoot)
	if info, err := os.Stat(sourceRoot); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return fmt.Errorf("source root %s: %w", sourceRoot, err)
	}
	if requireCommit {
		repoRoot := filepath.Dir(filepath.Dir(sourceRoot))
		command := exec.Command("git", "-C", repoRoot, "rev-parse", "HEAD")
		got, err := command.Output()
		if err != nil {
			return fmt.Errorf("resolve DuckDB source commit: %w", err)
		}
		if strings.TrimSpace(string(got)) != lock.DuckDBSourceCommit {
			return fmt.Errorf("DuckDB source commit mismatch: got %q want %q", strings.TrimSpace(string(got)), lock.DuckDBSourceCommit)
		}
	}
	seen := map[string]bool{}
	for path := range lock.Files {
		if filepath.IsAbs(path) || filepath.Clean(path) != path || strings.HasPrefix(path, "../") {
			return fmt.Errorf("invalid locked source path %q", path)
		}
		absolute := filepath.Join(sourceRoot, filepath.FromSlash(path))
		data, err := os.ReadFile(absolute)
		if err != nil {
			return fmt.Errorf("read locked source %s: %w", path, err)
		}
		digest := sha256.Sum256(data)
		got := hex.EncodeToString(digest[:])
		if got != lock.Files[path] {
			return fmt.Errorf("source hash mismatch for %s: got %s want %s", path, got, lock.Files[path])
		}
		seen[path] = true
	}
	var unexpected []string
	err := filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "functions.json" {
			return nil
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if !seen[filepath.ToSlash(rel)] {
			unexpected = append(unexpected, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk source root: %w", err)
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("unexpected functions.json files: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func render(input documentationInput) (string, error) {
	var b strings.Builder
	b.WriteString("// Code generated by pkg/duckdbsql/internal/cmd/duckdbsqldocsgen; DO NOT EDIT.\n")
	fmt.Fprintf(&b, "// DuckDB %s; upstream git commit %s; source files are hash-locked.\n", input.Lock.DuckDBVersion, input.Lock.DuckDBSourceCommit)
	b.WriteString("package duckdbsql\n\n")
	fmt.Fprintf(&b, "const generatedFunctionDocumentationDuckDBVersion = %q\n", input.Lock.DuckDBVersion)
	fmt.Fprintf(&b, "const generatedFunctionDocumentationDuckDBSourceCommit = %q\n\n", input.Lock.DuckDBSourceCommit)
	b.WriteString("var generatedFunctionDocumentationSourceFiles = []FunctionDocumentationSourceFile{\n")
	for _, file := range input.Files {
		fmt.Fprintf(&b, "\t{Path: %q, SHA256: %q},\n", file.Path, file.SHA256)
	}
	b.WriteString("}\n\nvar generatedFunctionDocumentation = []FunctionDocumentation{\n")
	for _, doc := range input.Docs {
		fmt.Fprintf(&b, "\t{Name: %q, Parameters: %q, Description: %q, Example: %q, Examples: %s, Aliases: %s, Categories: %s, Category: %q, FunctionType: %q, Kind: %q, SourcePath: %q, Variants: %s},\n", doc.Name, doc.Parameters, doc.Description, doc.Example, quoteStrings(doc.Examples), quoteStrings(doc.Aliases), quoteStrings(doc.Categories), doc.Category, doc.FunctionType, doc.Kind, doc.SourcePath, renderVariants(doc.Variants))
	}
	b.WriteString("}\n")
	formatted, err := format.Source([]byte(b.String()))
	if err != nil {
		return "", fmt.Errorf("format generated Go: %w", err)
	}
	return string(formatted), nil
}

func renderVariants(variants []duckdbsql.FunctionDocumentationVariant) string {
	if len(variants) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]FunctionDocumentationVariant{")
	for _, variant := range variants {
		b.WriteString("{Parameters: []FunctionDocumentationParameter{")
		for _, parameter := range variant.Parameters {
			fmt.Fprintf(&b, "{Name: %q, Type: %q},", parameter.Name, parameter.Type)
		}
		fmt.Fprintf(&b, "}, Description: %q, Example: %q, Examples: %s, Categories: %s},", variant.Description, variant.Example, quoteStrings(variant.Examples), quoteStrings(variant.Categories))
	}
	b.WriteByte('}')
	return b.String()
}

func quoteStrings(values []string) string {
	if len(values) == 0 {
		return "nil"
	}
	var b strings.Builder
	b.WriteString("[]string{")
	for _, value := range values {
		fmt.Fprintf(&b, "%q,", value)
	}
	b.WriteByte('}')
	return b.String()
}
