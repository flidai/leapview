package duckdbsql

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed upstream.lock.json
var upstreamLockBytes []byte

// SourceIdentity identifies the immutable upstream inputs used to produce an
// artifact.  The git commit and descriptor hashes are checked before source
// derived generation is allowed.
type SourceIdentity struct {
	DuckDBVersion     string
	DuckDBGitCommit   string
	GoModule          string
	GoModuleVersion   string
	BindingsModule    string
	BindingsVersion   string
	DescriptorRoot    string
	DescriptorFileSHA map[string]string
	EnumFileSHA       map[string]string
}

// FunctionMetadata is intentionally descriptive. Catalog OIDs are omitted;
// documentation and classification fields are retained when DuckDB exposes
// them so generated metadata remains useful without becoming policy.
type FunctionMetadata struct {
	DatabaseName    string
	SchemaName      string
	FunctionName    string
	AliasOf         string
	FunctionType    string
	Description     string
	Comment         string
	Tags            map[string]string
	ReturnType      string
	Parameters      []string
	ParameterTypes  []string
	Varargs         string
	MacroDefinition string
	HasSideEffects  bool
	Internal        bool
	Stability       string
	Categories      []string
	Examples        []string
}

// KeywordMetadata describes one parser keyword and its DuckDB category.
type KeywordMetadata struct {
	Name     string
	Category string
}

// TypeMetadata omits catalog OIDs while retaining stable descriptive runtime
// fields. It does not make any type-admission decision.
type TypeMetadata struct {
	DatabaseName string
	SchemaName   string
	TypeName     string
	TypeSize     int64
	LogicalType  string
	TypeCategory string
	Comment      string
	Tags         map[string]string
	Internal     bool
	Labels       []string
}

// MetadataInventory contains the descriptive runtime catalog inventories.
type MetadataInventory struct {
	Functions []FunctionMetadata
	Keywords  []KeywordMetadata
	Types     []TypeMetadata
}

// DescriptorProvenance records one source-derived serializer descriptor hash.
type DescriptorProvenance struct {
	Path   string
	SHA256 string
}

// serializedNodeSchema describes the fields accepted by one DuckDB JSON
// serializer discriminator. It is package-private so callers cannot treat the
// serializer wire format as a general-purpose AST API.
type serializedNodeSchema struct {
	Discriminator  string
	AllowedFields  []string
	RequiredFields []string
}

type lockFile struct {
	DuckDBVersion    string              `json:"duckdb_version"`
	DuckDBGitCommit  string              `json:"duckdb_git_commit"`
	GoModule         string              `json:"go_module"`
	GoModuleVersion  string              `json:"go_module_version"`
	BindingsModule   string              `json:"bindings_module"`
	BindingsVersion  string              `json:"bindings_module_version"`
	DescriptorRoot   string              `json:"descriptor_root"`
	DescriptorFiles  map[string]string   `json:"descriptor_files"`
	EnumFiles        map[string]string   `json:"enum_files"`
	InventoryColumns map[string][]string `json:"inventory_columns"`
}

// GeneratedDescriptorManifestSnapshot returns a copy of source descriptor
// provenance for diagnostics and conformance checks.
func GeneratedDescriptorManifestSnapshot() []DescriptorProvenance {
	return append([]DescriptorProvenance(nil), generatedDescriptorManifest...)
}

// GeneratedEnumManifestSnapshot returns source provenance for generated
// closed-enum inventories used by the JSON decoder.
func GeneratedEnumManifestSnapshot() []DescriptorProvenance {
	return append([]DescriptorProvenance(nil), generatedEnumManifest...)
}

// GeneratedInventorySnapshot returns a deep copy of descriptive runtime
// metadata so callers cannot mutate generated process-global state.
func GeneratedInventorySnapshot() MetadataInventory {
	return cloneMetadataInventory(generatedInventory)
}

func cloneMetadataInventory(in MetadataInventory) MetadataInventory {
	out := MetadataInventory{
		Functions: append([]FunctionMetadata(nil), in.Functions...),
		Keywords:  append([]KeywordMetadata(nil), in.Keywords...),
		Types:     append([]TypeMetadata(nil), in.Types...),
	}
	for i := range out.Functions {
		out.Functions[i].Parameters = append([]string(nil), out.Functions[i].Parameters...)
		out.Functions[i].ParameterTypes = append([]string(nil), out.Functions[i].ParameterTypes...)
		out.Functions[i].Categories = append([]string(nil), out.Functions[i].Categories...)
		out.Functions[i].Tags = cloneStringMap(out.Functions[i].Tags)
		out.Functions[i].Examples = append([]string(nil), out.Functions[i].Examples...)
	}
	for i := range out.Types {
		out.Types[i].Labels = append([]string(nil), out.Types[i].Labels...)
		out.Types[i].Tags = cloneStringMap(out.Types[i].Tags)
	}
	return out
}

func readLock() (lockFile, error) {
	var lock lockFile
	if err := json.Unmarshal(upstreamLockBytes, &lock); err != nil {
		return lock, fmt.Errorf("decode DuckDB upstream lock: %w", err)
	}
	return lock, nil
}

// UpstreamSourceIdentity returns the pinned source identity recorded in the
// repository lock file.
func UpstreamSourceIdentity() (SourceIdentity, error) {
	lock, err := readLock()
	if err != nil {
		return SourceIdentity{}, err
	}
	return SourceIdentity{
		DuckDBVersion: lock.DuckDBVersion, DuckDBGitCommit: lock.DuckDBGitCommit,
		GoModule: lock.GoModule, GoModuleVersion: lock.GoModuleVersion,
		BindingsModule: lock.BindingsModule, BindingsVersion: lock.BindingsVersion,
		DescriptorRoot:    lock.DescriptorRoot,
		DescriptorFileSHA: cloneStringMap(lock.DescriptorFiles),
		EnumFileSHA:       cloneStringMap(lock.EnumFiles),
	}, nil
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// InventoryColumns returns the exact v1.5.4 runtime schema for a catalog
// inventory table.  A copy is returned so callers cannot mutate the contract.
func InventoryColumns(table string) ([]string, error) {
	lock, err := readLock()
	if err != nil {
		return nil, err
	}
	columns, ok := lock.InventoryColumns[table]
	if !ok {
		return nil, fmt.Errorf("unknown DuckDB inventory table %q", table)
	}
	return append([]string(nil), columns...), nil
}

// ValidateSourceCheckout verifies both the immutable git identity and every
// generated-serializer descriptor hash. It never fetches or modifies a repo.
func ValidateSourceCheckout(sourceDir string) error {
	identity, err := UpstreamSourceIdentity()
	if err != nil {
		return err
	}
	if sourceDir == "" {
		return errors.New("DuckDB source checkout path is empty")
	}
	abs, err := filepath.Abs(sourceDir)
	if err != nil {
		return fmt.Errorf("resolve DuckDB source path: %w", err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return fmt.Errorf("DuckDB source checkout %q: %w", abs, err)
	}
	cmd := exec.Command("git", "-C", abs, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read DuckDB source git identity: %w", err)
	}
	if got := strings.TrimSpace(string(out)); got != identity.DuckDBGitCommit {
		return fmt.Errorf("DuckDB source commit mismatch: got %s, want %s", got, identity.DuckDBGitCommit)
	}
	for name, want := range identity.DescriptorFileSHA {
		path := filepath.Join(abs, identity.DescriptorRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read descriptor %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != want {
			return fmt.Errorf("descriptor %s hash mismatch: got %s, want %s", name, got, want)
		}
	}
	for path, want := range identity.EnumFileSHA {
		file := filepath.Join(abs, filepath.FromSlash(path))
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read enum source %s: %w", path, err)
		}
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if got != want {
			return fmt.Errorf("enum source %s hash mismatch: got %s, want %s", path, got, want)
		}
	}
	return nil
}

// SortInventory applies a stable lexical ordering to all inventory families.
func SortInventory(in *MetadataInventory) {
	sort.Slice(in.Functions, func(i, j int) bool {
		a, b := in.Functions[i], in.Functions[j]
		return functionSortKey(a) < functionSortKey(b)
	})
	sort.Slice(in.Keywords, func(i, j int) bool {
		if in.Keywords[i].Name != in.Keywords[j].Name {
			return in.Keywords[i].Name < in.Keywords[j].Name
		}
		return in.Keywords[i].Category < in.Keywords[j].Category
	})
	sort.Slice(in.Types, func(i, j int) bool {
		a, b := in.Types[i], in.Types[j]
		return typeSortKey(a) < typeSortKey(b)
	})
}

func functionSortKey(value FunctionMetadata) string {
	return strings.Join([]string{value.DatabaseName, value.SchemaName, value.FunctionName, value.AliasOf, value.FunctionType, value.Description, value.Comment, stringMapSortKey(value.Tags), value.ReturnType, strings.Join(value.Parameters, "\x00"), strings.Join(value.ParameterTypes, "\x00"), value.Varargs, value.MacroDefinition, fmt.Sprint(value.HasSideEffects), fmt.Sprint(value.Internal), value.Stability, strings.Join(value.Categories, "\x00"), strings.Join(value.Examples, "\x00")}, "\x00")
}

func typeSortKey(value TypeMetadata) string {
	return strings.Join([]string{value.DatabaseName, value.SchemaName, value.TypeName, fmt.Sprint(value.TypeSize), value.LogicalType, value.TypeCategory, value.Comment, stringMapSortKey(value.Tags), fmt.Sprint(value.Internal), strings.Join(value.Labels, "\x00")}, "\x00")
}

func stringMapSortKey(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys)*2)
	for _, key := range keys {
		parts = append(parts, key, values[key])
	}
	return strings.Join(parts, "\x00")
}
