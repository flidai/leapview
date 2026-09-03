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
	forbiddenProse, forbiddenPublic := terminologyMatchers()
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
			if offending := matcher.FindString(line); offending != "" {
				t.Errorf("%s:%d: forbidden terminology %q; use canonical Model, semantic dataset, or materialized relation terminology", relative, lineNumber+1, offending)
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

func terminologyMatchers() (prose, public *regexp.Regexp) {
	modelWord := "model"
	tableWord := "table"
	prose = regexp.MustCompile(`(?i)\b` + modelWord + `[ -]` + tableWord + `s?\b`)
	// Public surfaces must not expose the former compound terminology in prose
	// or identifiers. Separator suffixes are included so model_table_rows is
	// reported as one offending token. The camel-case branch deliberately
	// requires ModelTable to be one token; this leaves TypeSpec declarations
	// such as "model TableDashboardPresentation" outside the product guardrail.
	public = regexp.MustCompile(`(?i)\b` + modelWord + `(?:[ _-]` + tableWord + `s?(?:[_-][a-z0-9]+)*|` + tableWord + `s?(?:[A-Z][a-z0-9]*)*)\b`)
	return prose, public
}

func TestPublicModelTableTerminologyMatcher(t *testing.T) {
	_, matcher := terminologyMatchers()
	tests := []struct {
		name string
		line string
		want string
	}{
		{name: "space singular", line: "model table", want: "model table"},
		{name: "space plural", line: "model tables", want: "model tables"},
		{name: "hyphen", line: "model-table", want: "model-table"},
		{name: "snake", line: "model_table", want: "model_table"},
		{name: "snake plural", line: "model_tables", want: "model_tables"},
		{name: "snake suffix", line: "model_table_rows", want: "model_table_rows"},
		{name: "camel", line: "ModelTable", want: "ModelTable"},
		{name: "lower camel plural", line: "modelTables", want: "modelTables"},
		{name: "lower camel suffix", line: "modelTableRows", want: "modelTableRows"},
		{name: "camel suffix", line: "ModelTableRows", want: "ModelTableRows"},
		{name: "canonical model", line: "Model", want: ""},
		{name: "semantic model", line: "Semantic Model", want: ""},
		{name: "model materialization", line: "model materialization", want: ""},
		{name: "materialized table", line: "materialized table", want: ""},
		{name: "TypeSpec model declaration", line: "model TableDashboardPresentation extends DashboardPresentation", want: ""},
		{name: "TypeSpec visualization declaration", line: "model TableVisualizationFormattingRule", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matcher.FindString(tt.line); got != tt.want {
				t.Errorf("matcher.FindString(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

func TestInternalPhysicalModelTableTerminologyRemainsAllowed(t *testing.T) {
	proseMatcher, publicMatcher := terminologyMatchers()
	tests := []struct {
		name string
		path string
		line string
	}{
		{name: "ModelTablePlan", path: "internal/analytics/materialize/runtime.go", line: "type ModelTablePlan struct {}"},
		{name: "physicalModelTable", path: "internal/analytics/materialize/runtime.go", line: "func physicalModelTable() {}"},
		{name: "PlanModelTable", path: "internal/analytics/materialize/runtime.go", line: "func PlanModelTable() {}"},
		{name: "model materialization prose", path: "internal/analytics/materialize/runtime.go", line: "model materialization"},
		{name: "materialized table prose", path: "internal/analytics/materialize/runtime.go", line: "materialized table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := proseMatcher
			if isPublicTerminologySurface(tt.path) {
				matcher = publicMatcher
			}
			if offending := matcher.FindString(tt.line); offending != "" {
				t.Errorf("internal physical/canonical terminology %q matched forbidden token %q", tt.line, offending)
			}
		})
	}
}
