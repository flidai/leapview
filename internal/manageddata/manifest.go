// Package manageddata defines immutable, project-global managed dataset revisions.
package manageddata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Files []File `json:"files"`
}

// MaxManifestFiles is the hard upper bound shared by managed-data transport
// admission and recovery observation capture. Configured limits may be lower,
// but no production ingress path may admit a revision above this bound.
const MaxManifestFiles = 10_000

type Limits struct {
	MaxFiles         int
	MaxFileBytes     int64
	MaxRevisionBytes int64
}

type Diff struct {
	Added     []File
	Changed   []File
	Removed   []File
	Unchanged []File
}

func (m Manifest) Validate(limits Limits) error {
	if limits.MaxFiles > 0 && len(m.Files) > limits.MaxFiles {
		return fmt.Errorf("manifest file count %d exceeds limit %d", len(m.Files), limits.MaxFiles)
	}
	seen := make(map[string]string, len(m.Files))
	var total int64
	for _, file := range m.Files {
		if err := validateLogicalPath(file.Path); err != nil {
			return err
		}
		folded := strings.ToLower(file.Path)
		if previous, ok := seen[folded]; ok {
			if previous == file.Path {
				return fmt.Errorf("duplicate manifest path %q", file.Path)
			}
			return fmt.Errorf("case-folding path collision between %q and %q", previous, file.Path)
		}
		seen[folded] = file.Path
		if file.Size < 0 {
			return fmt.Errorf("manifest file %q has negative size", file.Path)
		}
		if limits.MaxFileBytes > 0 && file.Size > limits.MaxFileBytes {
			return fmt.Errorf("manifest file %q exceeds maximum file size %d", file.Path, limits.MaxFileBytes)
		}
		if err := validateDigest(file.SHA256); err != nil {
			return fmt.Errorf("manifest file %q: %w", file.Path, err)
		}
		if file.Size > 0 && total > int64(^uint64(0)>>1)-file.Size {
			return fmt.Errorf("manifest revision size overflows int64")
		}
		total += file.Size
	}
	if limits.MaxRevisionBytes > 0 && total > limits.MaxRevisionBytes {
		return fmt.Errorf("manifest revision size %d exceeds limit %d", total, limits.MaxRevisionBytes)
	}
	return nil
}

func (m Manifest) CanonicalJSON() ([]byte, error) {
	if err := m.Validate(Limits{}); err != nil {
		return nil, err
	}
	canonical := Manifest{Files: append([]File(nil), m.Files...)}
	sort.Slice(canonical.Files, func(i, j int) bool { return canonical.Files[i].Path < canonical.Files[j].Path })
	return json.Marshal(canonical)
}

func (m Manifest) RevisionID() string {
	canonical, err := m.CanonicalJSON()
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func ValidateRevisionID(value string) error {
	if err := digest.ValidateSHA256Identity(value); err != nil {
		return fmt.Errorf("revision ID must contain a canonical SHA-256 digest: %w", err)
	}
	return nil
}

func DiffManifests(oldManifest, newManifest Manifest) Diff {
	oldFiles := filesByPath(oldManifest.Files)
	newFiles := filesByPath(newManifest.Files)
	var diff Diff
	for logicalPath, file := range newFiles {
		old, ok := oldFiles[logicalPath]
		switch {
		case !ok:
			diff.Added = append(diff.Added, file)
		case old.Size == file.Size && old.SHA256 == file.SHA256:
			diff.Unchanged = append(diff.Unchanged, file)
		default:
			diff.Changed = append(diff.Changed, file)
		}
	}
	for logicalPath, file := range oldFiles {
		if _, ok := newFiles[logicalPath]; !ok {
			diff.Removed = append(diff.Removed, file)
		}
	}
	sortFiles(diff.Added)
	sortFiles(diff.Changed)
	sortFiles(diff.Removed)
	sortFiles(diff.Unchanged)
	return diff
}

func validateLogicalPath(value string) error {
	if value == "" {
		return fmt.Errorf("manifest path is required")
	}
	if strings.Contains(value, "\\") {
		return fmt.Errorf("manifest path %q must use forward slashes", value)
	}
	if path.IsAbs(value) {
		return fmt.Errorf("manifest path %q must be relative", value)
	}
	cleaned := path.Clean(value)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("manifest path %q contains traversal", value)
	}
	if cleaned != value || value == "." {
		return fmt.Errorf("manifest path %q is not canonical", value)
	}
	return nil
}

func validateDigest(value string) error {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return fmt.Errorf("SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("SHA-256 must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func filesByPath(files []File) map[string]File {
	result := make(map[string]File, len(files))
	for _, file := range files {
		result[file.Path] = file
	}
	return result
}

func sortFiles(files []File) {
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
}
