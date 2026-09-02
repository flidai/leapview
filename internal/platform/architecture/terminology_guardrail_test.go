package architecture

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestRepositoryUsesCanonicalModelTerminology keeps authored repository prose
// and diagnostics from collapsing logical Models, semantic datasets, and
// physical materialization relations into one ambiguous phrase. Public surface
// roots also reject identifier separator and camel-case variants; internal
// physical and compatibility identifiers remain outside that stricter check.
func TestRepositoryUsesCanonicalModelTerminology(t *testing.T) {
	root := repoRoot(t)
	modelWord := "model"
	tableWord := "table"
	forbiddenProse := regexp.MustCompile(`(?i)\b` + modelWord + `[ -]` + tableWord + `s?\b`)
	forbiddenPublic := regexp.MustCompile(`(?i)\b` + modelWord + `(?:[ _-]` + tableWord + `s?|T` + tableWord[1:] + `s?)\b`)
	guardrail := filepath.Clean(filepath.Join(root, "internal", "platform", "architecture", "terminology_guardrail_test.go"))

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".data", ".leapview", ".terraform", ".tmp", "cache", "dist", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || filepath.Clean(path) == guardrail {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		relative = filepath.ToSlash(relative)
		matcher := forbiddenProse
		if isPublicTerminologySurface(relative) {
			matcher = forbiddenPublic
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(body) {
			return nil
		}
		for lineNumber, line := range strings.Split(string(body), "\n") {
			if matcher.MatchString(line) {
				t.Errorf("%s:%d: use canonical Model, semantic dataset, or materialized relation terminology", relative, lineNumber+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func isPublicTerminologySurface(path string) bool {
	for _, prefix := range []string{
		"adr/", "api/", "dashboards/", "docs/", "site/", "static/", "web/",
		"internal/admin/", "internal/analytics/dataquery/", "internal/analytics/queryaudit/",
		"internal/app/site/", "internal/dashboard/ui/", "internal/project/http/", "internal/project/ui/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return path == "spec.md"
}
