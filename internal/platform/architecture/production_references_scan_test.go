package architecture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProductionReference identifies one authored production line that still
// contains a removed workspace identity, route, or schema symbol. The scanner
// deliberately records duplicate lines so a caller can see every occurrence.
type ProductionReference struct {
	Category string
	Path     string
	Hash     string
}

// ObserveProductionReferences scans authored production surfaces for the
// retired workspace vocabulary. Tests and generated trees are excluded so the
// invariant covers only code and source contracts that ship to users.
func ObserveProductionReferences(root string) ([]ProductionReference, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}

	observations := make([]ProductionReference, 0)
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
			if normalized == "" || !isRetiredWorkspaceReference(normalized) {
				continue
			}
			observations = append(observations, ProductionReference{
				Category: category,
				Path:     rel,
				Hash:     hashProductionReferenceLine(normalized),
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan retired workspace production references: %w", err)
	}
	sortProductionReferences(observations)
	return observations, nil
}

// CheckNoWorkspaceProductionReferences enforces the canonical zero-reference
// invariant. Historical ADRs and tests may use the migration vocabulary, but
// no production source or browser/configuration contract may retain it.
func CheckNoWorkspaceProductionReferences(root string) error {
	observed, err := ObserveProductionReferences(root)
	if err != nil {
		return err
	}
	if len(observed) == 0 {
		return nil
	}
	var message strings.Builder
	fmt.Fprintf(&message, "retired workspace production references found: %d", len(observed))
	for _, reference := range observed {
		fmt.Fprintf(&message, "\n  - %s\t%s\tsha256:%s", reference.Category, reference.Path, reference.Hash)
	}
	return fmt.Errorf("%s", message.String())
}

func normalizeProductionReferenceLine(line string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
}

func hashProductionReferenceLine(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func isRetiredWorkspaceReference(normalized string) bool {
	return strings.Contains(strings.ToLower(normalized), "workspace")
}

func sortProductionReferences(rows []ProductionReference) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Category != rows[j].Category {
			return rows[i].Category < rows[j].Category
		}
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].Hash < rows[j].Hash
	})
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
	if hasPathSegment(path, "config") || hasPathPrefix(path, "api") || hasPathPrefix(path, "desktop") {
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
