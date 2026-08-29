package ducklake

// This file contains the small, deliberately boring policy layer which keeps
// DuckLake's catalog-local capabilities from being used as a physical-pool
// garbage collector.  DuckLake's native maintenance functions only know about
// one metadata catalog; LeapView catalogs can share a data namespace, so that
// distinction is important.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/physicalpool"
)

const CompatibilityEvidenceVersion = 1

// These aliases keep DuckLake's runtime package on the canonical physical-pool
// admission contract. No second tuple or evidence schema is persisted here.
type CompatibilityTuple = physicalpool.Compatibility
type CompatibilityEvidence = physicalpool.Evidence

// PoolContract is the immutable, target-controlled admission record required
// before a catalog can attach to a shared physical pool.  The runtime keeps
// the complete canonical contract together so callers cannot accidentally
// provide a pool ID or compatibility tuple without the evidence which
// admitted it.
type PoolContract struct {
	Pool      physicalpool.PhysicalPool
	Tuple     physicalpool.Compatibility
	Admission physicalpool.PoolAdmission
	Evidence  physicalpool.Evidence
}

func (c *PoolContract) Validate() error {
	if c == nil {
		return fmt.Errorf("shared physical-pool contract is required")
	}
	if err := physicalpool.VerifyAdmission(c.Pool, c.Tuple, c.Admission, c.Evidence); err != nil {
		return err
	}
	if c.Evidence.ConformanceVersion != SharedPoolConformanceVersion {
		return fmt.Errorf("shared physical-pool evidence version %q is not %q", c.Evidence.ConformanceVersion, SharedPoolConformanceVersion)
	}
	if err := validateConformanceCheckSet(c.Evidence.Checks); err != nil {
		return err
	}
	return nil
}

func validateConformanceCheckSet(checks []physicalpool.EvidenceCheck) error {
	if len(checks) != len(SharedPoolConformanceChecks) {
		return fmt.Errorf("shared physical-pool evidence has %d checks, want %d", len(checks), len(SharedPoolConformanceChecks))
	}
	want := make(map[string]struct{}, len(SharedPoolConformanceChecks))
	for _, name := range SharedPoolConformanceChecks {
		want[name] = struct{}{}
	}
	for _, check := range checks {
		if _, ok := want[check.ID]; !ok {
			return fmt.Errorf("shared physical-pool evidence has unknown check %q", check.ID)
		}
		if !check.Passed {
			return fmt.Errorf("shared physical-pool evidence check %q did not pass", check.ID)
		}
		if !validObservationDigest(check.ObservationDigest) {
			return fmt.Errorf("shared physical-pool evidence check %q has invalid observation digest", check.ID)
		}
		delete(want, check.ID)
	}
	if len(want) != 0 {
		return fmt.Errorf("shared physical-pool evidence is missing checks")
	}
	return nil
}

func validObservationDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

// ValidateDataPathBinding binds DuckLake's DATA_PATH to the admitted pool's
// storage location plus namespace. Local pools use filepath.Join(location,
// namespace); object stores use URL path joining (for example,
// s3://bucket/base + tenant-a => s3://bucket/base/tenant-a). The comparison is
// canonical and exact, so a contract cannot attach a sibling pool prefix.
func (c *PoolContract) ValidateDataPathBinding(dataPath string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	expected, err := c.Pool.DataPath()
	if err != nil {
		return err
	}
	actual, err := canonicalDataPath(dataPath)
	if err != nil {
		return err
	}
	if expected != actual {
		return fmt.Errorf("DuckLake DATA_PATH does not match admitted physical-pool namespace")
	}
	return nil
}

func canonicalDataPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("DuckLake DATA_PATH is required for a shared physical pool")
	}
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
			return "", fmt.Errorf("DuckLake DATA_PATH URL is invalid")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		if parsed.Scheme == "file" {
			if parsed.Host != "" || parsed.Path == "" {
				return "", fmt.Errorf("DuckLake DATA_PATH file URL is invalid")
			}
			return canonicalLocalPath(parsed.Path)
		}
		if parsed.Scheme != "s3" && parsed.Scheme != "gs" && parsed.Scheme != "az" && parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", fmt.Errorf("DuckLake DATA_PATH scheme %q is unsupported", parsed.Scheme)
		}
		parsed.Path = path.Clean(parsed.Path)
		parsed.RawPath = ""
		return parsed.String(), nil
	}
	return canonicalLocalPath(value)
}

// CanonicalDataPath exposes the same storage-path normalization used by
// physical-pool admission to operation packages that verify an attached
// DuckLake catalog. Keeping one implementation prevents false mismatches for
// URL host casing, trailing separators, and local relative paths.
func CanonicalDataPath(value string) (string, error) {
	return canonicalDataPath(value)
}

func canonicalLocalPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize local DuckLake DATA_PATH: %w", err)
	}
	return filepath.Clean(absolute), nil
}

var (
	// ErrSharedPoolMaintenance is returned before a native function which can
	// delete physical files is sent to DuckDB.
	ErrSharedPoolMaintenance = errors.New("native DuckLake physical maintenance is disabled for shared pools")
	ErrUnsafeCheckpoint      = errors.New("DuckLake CHECKPOINT is disabled for shared pools")
	ErrInliningNotDisabled   = errors.New("DuckLake data inlining is not disabled")
	ErrLiveInlineData        = errors.New("DuckLake catalog still contains live inlined data")
)

type CompatibilityCheck struct {
	Name   string
	Passed bool
	Digest string
}

func NewCompatibilityEvidence(tuple CompatibilityTuple, checks []CompatibilityCheck) (CompatibilityEvidence, error) {
	converted := make([]physicalpool.EvidenceCheck, 0, len(checks))
	for _, check := range checks {
		converted = append(converted, physicalpool.EvidenceCheck{ID: check.Name, Passed: check.Passed, ObservationDigest: check.Digest})
	}
	return physicalpool.NewEvidence(physicalpool.EvidenceInput{
		Compatibility: tuple, ConformanceVersion: fmt.Sprintf("ducklake/%d", CompatibilityEvidenceVersion), Checks: converted,
	})
}

func ValidateCompatibilityEvidence(e CompatibilityEvidence, expected CompatibilityTuple) error {
	if expected != (CompatibilityTuple{}) && e.Compatibility != expected {
		return fmt.Errorf("DuckLake compatibility tuple does not match the admitted pool")
	}
	return e.Verify()
}

type InliningScope string

const (
	InliningProcess InliningScope = "PROCESS"
	InliningAttach  InliningScope = "ATTACH"
	InliningGlobal  InliningScope = "GLOBAL"
	InliningSchema  InliningScope = "SCHEMA"
	InliningTable   InliningScope = "TABLE"
)

type PersistedInliningOption struct {
	Scope InliningScope
	Entry string
	Limit uint64
}

// DataInliningPolicy records all three policy layers.  ProcessSet and
// AttachSet distinguish an explicit zero from an absent value when inspecting
// a catalog created by older runtimes.
type DataInliningPolicy struct {
	ProcessLimit uint64
	ProcessSet   bool
	AttachLimit  uint64
	AttachSet    bool
	Persisted    []PersistedInliningOption
}

type EffectiveInlining struct {
	Scope  InliningScope
	Entry  string
	Limit  uint64
	Source InliningScope
}

func (p DataInliningPolicy) Effective(schema, table string) EffectiveInlining {
	for _, option := range p.Persisted {
		if option.Scope == InliningTable && sameTableIdentifier(option.Entry, schema, table) {
			return EffectiveInlining{InliningTable, option.Entry, option.Limit, InliningTable}
		}
	}
	for _, option := range p.Persisted {
		if option.Scope == InliningSchema && sameIdentifier(option.Entry, schema) {
			return EffectiveInlining{InliningSchema, option.Entry, option.Limit, InliningSchema}
		}
	}
	for _, option := range p.Persisted {
		if option.Scope == InliningGlobal {
			return EffectiveInlining{InliningGlobal, "", option.Limit, InliningGlobal}
		}
	}
	if p.AttachSet {
		return EffectiveInlining{InliningAttach, "", p.AttachLimit, InliningAttach}
	}
	if p.ProcessSet {
		return EffectiveInlining{InliningProcess, "", p.ProcessLimit, InliningProcess}
	}
	// DuckLake's documented fallback has changed between releases. Returning a
	// non-zero unknown value makes validation fail closed.
	return EffectiveInlining{InliningProcess, "", 1, InliningProcess}
}

func (p DataInliningPolicy) ValidateZero() error {
	if p.ProcessSet && p.ProcessLimit != 0 {
		return fmt.Errorf("process data_inlining_row_limit is %d: %w", p.ProcessLimit, ErrInliningNotDisabled)
	}
	if p.AttachSet && p.AttachLimit != 0 {
		return fmt.Errorf("attach data_inlining_row_limit is %d: %w", p.AttachLimit, ErrInliningNotDisabled)
	}
	for _, option := range p.Persisted {
		if option.Limit != 0 {
			return fmt.Errorf("%s data_inlining_row_limit for %q is %d: %w", option.Scope, option.Entry, option.Limit, ErrInliningNotDisabled)
		}
	}
	return nil
}

func sameIdentifier(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func sameTableIdentifier(entry, schema, table string) bool {
	entry = strings.TrimSpace(entry)
	table = strings.TrimSpace(table)
	if sameIdentifier(entry, table) {
		return true
	}
	return sameIdentifier(entry, strings.TrimSpace(schema)+"."+table)
}

type FileKind string

const (
	DataFile   FileKind = "data"
	DeleteFile FileKind = "delete"
)

type CatalogFileSet struct {
	CatalogID   string
	DataFiles   []string
	DeleteFiles []string
}

type PoolObject struct {
	Path string
	Kind FileKind
}

type FileClassification struct {
	PoolObject
	Live       bool
	CatalogIDs []string
}

// CrossCatalogLiveFileUnion is the global physical reachability mark set. The
// kind is part of the identity so a data file and a delete file with the same
// key cannot accidentally collapse into one mark.
func CrossCatalogLiveFileUnion(catalogs []CatalogFileSet) []FileClassification {
	marks := map[string]FileClassification{}
	add := func(catalogID, path string, kind FileKind) {
		path = normalizeObjectKey(path)
		if path == "" || (kind != DataFile && kind != DeleteFile) {
			return
		}
		key := string(kind) + "\x00" + path
		entry := marks[key]
		entry.Path, entry.Kind, entry.Live = path, kind, true
		if catalogID != "" && !containsString(entry.CatalogIDs, catalogID) {
			entry.CatalogIDs = append(entry.CatalogIDs, catalogID)
			sort.Strings(entry.CatalogIDs)
		}
		marks[key] = entry
	}
	for _, catalog := range catalogs {
		for _, path := range catalog.DataFiles {
			add(catalog.CatalogID, path, DataFile)
		}
		for _, path := range catalog.DeleteFiles {
			add(catalog.CatalogID, path, DeleteFile)
		}
	}
	result := make([]FileClassification, 0, len(marks))
	for _, mark := range marks {
		result = append(result, mark)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func ClassifyPoolObjects(objects []PoolObject, catalogs []CatalogFileSet) []FileClassification {
	marks := CrossCatalogLiveFileUnion(catalogs)
	byKey := make(map[string]FileClassification, len(marks))
	for _, mark := range marks {
		byKey[string(mark.Kind)+"\x00"+mark.Path] = mark
	}
	result := make([]FileClassification, 0, len(objects))
	for _, object := range objects {
		object.Path = normalizeObjectKey(object.Path)
		if mark, ok := byKey[string(object.Kind)+"\x00"+object.Path]; ok {
			result = append(result, mark)
			continue
		}
		result = append(result, FileClassification{PoolObject: object})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func normalizeObjectKey(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.TrimPrefix(path, "./")
	return path
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
