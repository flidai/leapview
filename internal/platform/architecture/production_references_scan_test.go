package architecture

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceProductionReference is the stable identity of one authored
// production line that carries a workspace reference. Line numbers are
// deliberately not part of the identity: moving an unchanged line is not a
// new resource reference, while changing its contents is.
type WorkspaceProductionReference struct {
	Category string
	Path     string
	Hash     string
}

const workspaceProductionReferenceBaseline = "internal/platform/architecture/workspace-production-references.baseline"

// ObserveWorkspaceProductionReferences scans the repository's authored
// production surfaces and returns deterministic, multiplicity-preserving
// workspace observations.
func ObserveWorkspaceProductionReferences(root string) ([]WorkspaceProductionReference, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}

	observations := make([]WorkspaceProductionReference, 0)
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			if excludedProductionReferenceDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		category, ok := productionReferenceCategory(rel)
		if !ok {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(body), "\n") {
			normalized := normalizeProductionReferenceLine(line)
			if normalized == "" || !isWorkspaceProductionReference(normalized) {
				continue
			}
			observations = append(observations, WorkspaceProductionReference{
				Category: category,
				Path:     rel,
				Hash:     hashProductionReferenceLine(normalized),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan workspace production references: %w", err)
	}
	sortWorkspaceProductionReferences(observations)
	return observations, nil
}

// CheckWorkspaceProductionReferenceBaseline verifies the shrink-only
// contract. Every current observation must have a corresponding baseline row;
// baseline rows that no longer occur are intentionally tolerated.
func CheckWorkspaceProductionReferenceBaseline(root string, baseline []WorkspaceProductionReference) error {
	for index := 1; index < len(baseline); index++ {
		if workspaceProductionReferenceLess(baseline[index], baseline[index-1]) {
			return fmt.Errorf("workspace production-reference baseline rejected: rows are not sorted")
		}
	}
	observed, err := ObserveWorkspaceProductionReferences(root)
	if err != nil {
		return err
	}
	missing := missingWorkspaceProductionReferences(observed, baseline)
	if len(missing) == 0 {
		return nil
	}

	var message strings.Builder
	fmt.Fprintf(&message, "workspace production-reference baseline rejected: %d new or changed observation(s)", len(missing))
	for _, reference := range missing {
		fmt.Fprintf(&message, "\n  + %s\t%s\tsha256:%s", reference.Category, reference.Path, reference.Hash)
	}
	return fmt.Errorf("%s", message.String())
}

// ParseWorkspaceProductionReferenceBaseline parses the checked-in TSV
// baseline. Blank lines and comments beginning with # are ignored. Duplicate
// rows are retained because the baseline is a multiset, not a set.
func ParseWorkspaceProductionReferenceBaseline(r io.Reader) ([]WorkspaceProductionReference, error) {
	scanner := bufio.NewScanner(r)
	rows := make([]WorkspaceProductionReference, 0)
	lineNumber := 0
	var previous WorkspaceProductionReference
	havePrevious := false
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("baseline line %d: want category<TAB>path<TAB>sha256 hash", lineNumber)
		}
		for index := range fields {
			fields[index] = strings.TrimSpace(fields[index])
		}
		if fields[0] == "" || fields[1] == "" || fields[2] == "" {
			return nil, fmt.Errorf("baseline line %d: category, path, and hash are required", lineNumber)
		}
		hash := strings.TrimPrefix(fields[2], "sha256:")
		if len(hash) != sha256.Size*2 {
			return nil, fmt.Errorf("baseline line %d: invalid sha256 hash %q", lineNumber, fields[2])
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return nil, fmt.Errorf("baseline line %d: invalid sha256 hash %q", lineNumber, fields[2])
		}
		row := WorkspaceProductionReference{Category: fields[0], Path: fields[1], Hash: hash}
		if havePrevious && workspaceProductionReferenceLess(row, previous) {
			return nil, fmt.Errorf("baseline line %d: rows are not sorted", lineNumber)
		}
		rows = append(rows, row)
		previous = row
		havePrevious = true
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read baseline: %w", err)
	}
	return rows, nil
}

// FormatWorkspaceProductionReferenceBaseline emits the canonical checked-in
// representation. Rows are sorted and duplicate observations are preserved.
func FormatWorkspaceProductionReferenceBaseline(rows []WorkspaceProductionReference) string {
	rows = append([]WorkspaceProductionReference(nil), rows...)
	sortWorkspaceProductionReferences(rows)
	var output strings.Builder
	output.WriteString("# LeapView workspace production-reference baseline (intentionally shrink-only)\n")
	output.WriteString("# After this initial capture, only deliberate removals may edit it; never regenerate or append rows.\n")
	output.WriteString("# category<TAB>repo-relative path<TAB>sha256(normalized line)\n")
	for _, row := range rows {
		fmt.Fprintf(&output, "%s\t%s\tsha256:%s\n", row.Category, row.Path, row.Hash)
	}
	return output.String()
}

func missingWorkspaceProductionReferences(observed, baseline []WorkspaceProductionReference) []WorkspaceProductionReference {
	available := make(map[WorkspaceProductionReference]int, len(baseline))
	for _, reference := range baseline {
		available[reference]++
	}
	missing := make([]WorkspaceProductionReference, 0)
	for _, reference := range observed {
		if available[reference] == 0 {
			missing = append(missing, reference)
			continue
		}
		available[reference]--
	}
	return missing
}

func sortWorkspaceProductionReferences(rows []WorkspaceProductionReference) {
	sort.Slice(rows, func(i, j int) bool {
		return workspaceProductionReferenceLess(rows[i], rows[j])
	})
}

func workspaceProductionReferenceLess(left, right WorkspaceProductionReference) bool {
	if left.Category != right.Category {
		return left.Category < right.Category
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	return left.Hash < right.Hash
}

func normalizeProductionReferenceLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

func hashProductionReferenceLine(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func isWorkspaceProductionReference(normalized string) bool {
	return strings.Contains(strings.ToLower(normalized), "workspace")
}

func productionReferenceCategory(path string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	if isTestPath(path) {
		return "", false
	}
	if isProductionGoPath(path, ext) {
		return "go", true
	}
	if ext == ".sql" && !hasPathSegment(path, "migrations") {
		return "sql", true
	}
	if ext == ".tsp" {
		return "typespec", true
	}
	if ext == ".cue" {
		return "cue", true
	}
	if isBrowserSourcePath(path, ext) {
		return "browser", true
	}
	if isDesktopSourcePath(path, ext) {
		return "desktop", true
	}
	if isDashboardYAMLPath(path, ext) {
		return "dashboard", true
	}
	if isEvaluationYAMLPath(path, ext) {
		return "evaluation", true
	}
	if isConfigurationSourcePath(path, ext) {
		return "config", true
	}
	return "", false
}

func isProductionGoPath(path, ext string) bool {
	base := strings.ToLower(filepath.Base(path))
	return ext == ".go" && !strings.HasSuffix(base, "_test.go") && !strings.HasSuffix(base, ".gen.go") && !strings.HasSuffix(base, "_gen.go") && base != "generated.go"
}

func isBrowserSourcePath(path, ext string) bool {
	if !hasPathPrefix(path, "web") && !hasPathPrefix(path, "site/web") {
		return false
	}
	return isScriptExtension(ext) && !isTestPath(path)
}

func isDesktopSourcePath(path, ext string) bool {
	return hasPathPrefix(path, "desktop/src") && isScriptExtension(ext) && !isTestPath(path)
}

func isDashboardYAMLPath(path, ext string) bool {
	return hasPathPrefix(path, "dashboards") && (ext == ".yaml" || ext == ".yml")
}

func isEvaluationYAMLPath(path, ext string) bool {
	return hasPathPrefix(path, "evaluation") && (ext == ".yaml" || ext == ".yml")
}

func isConfigurationSourcePath(path, ext string) bool {
	if !isConfigExtension(ext) || hasPathSegment(path, "schemas") {
		return false
	}
	if hasPathSegment(path, "config") {
		return true
	}
	if hasPathPrefix(path, "api") {
		return true
	}
	if hasPathPrefix(path, "desktop") {
		return true
	}
	base := filepath.Base(path)
	if hasPathSegment(path, "docs") || hasPathSegment(path, "deploy") || hasPathSegment(path, ".github") {
		return false
	}
	for _, prefix := range []string{"tsconfig", "package", "sqlc"} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func isScriptExtension(ext string) bool {
	switch ext {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx":
		return true
	default:
		return false
	}
}

func isConfigExtension(ext string) bool {
	switch ext {
	case ".json", ".yaml", ".yml", ".toml":
		return true
	default:
		return false
	}
}

func isTestPath(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == "test" || part == "tests" || part == "testdata" || strings.Contains(part, ".test.") || strings.Contains(part, "_test.") {
			return true
		}
	}
	return false
}

func excludedProductionReferenceDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "docs", "adr", "static", "generated", "gen", "snapshot", "snapshots", "test", "tests", "testdata":
		return true
	default:
		return false
	}
}

func hasPathPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func hasPathSegment(path, want string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == want {
			return true
		}
	}
	return false
}
