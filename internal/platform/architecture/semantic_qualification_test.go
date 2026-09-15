package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// This checks evidence inventory integrity, not the truth of a PASS claim.
// Referenced suites still have to execute; file existence never qualifies a rule.
func TestSemanticQualificationMatrixCoversEveryRequirement(t *testing.T) {
	root := repoRoot(t)
	spec := readArchitectureFixture(t, root, "adr/specifications/semantic-access-policy-conformance.md")
	matrix := readArchitectureFixture(t, root, "adr/specifications/semantic-access-qualification.md")
	profile := readArchitectureFixture(t, root, "adr/specifications/semantic-access-supported-profile.md")
	expected := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?m)^- \*\*([A-Z]+-[0-9]{2}):\*\*`).FindAllStringSubmatch(spec, -1) {
		expected[match[1]] = true
	}
	if len(expected) == 0 {
		t.Fatal("normative requirement inventory is empty")
	}
	for _, id := range []string{"ID-LIF", "ID-TOMB", "ID-ROLL", "ID-ISO"} {
		expected[id] = true
	}
	seen := map[string]bool{}
	statuses := map[string]string{}
	statusCounts := map[string]int{}
	links := regexp.MustCompile(`\]\((\.\./\.\./[^)#]+)(?:#[^)]*)?\)`)
	ids := regexp.MustCompile(`^(?:[A-Z]+-[0-9]{2}|ID-(?:LIF|TOMB|ROLL|ISO))$`)
	testNames := regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]*\b`)
	functions := map[string]map[string]bool{}
	for _, line := range strings.Split(matrix, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "| "), " |"), " | ")
		if len(cells) == 0 || len(strings.Fields(cells[0])) == 0 || !ids.MatchString(strings.Fields(cells[0])[0]) {
			continue
		}
		id := strings.Fields(cells[0])[0]
		if !expected[id] || seen[id] {
			t.Errorf("unexpected or duplicate requirement %s", id)
		}
		seen[id] = true
		if len(cells) != 6 {
			t.Errorf("%s must have six evidence columns, got %d", id, len(cells))
			continue
		}
		switch cells[1] {
		case "PASS", "PARTIAL", "FAIL", "NOT APPLICABLE":
			statuses[id] = cells[1]
			statusCounts[cells[1]]++
		default:
			t.Errorf("%s has unknown status %q", id, cells[1])
		}
		for index := 2; index < len(cells); index++ {
			if strings.TrimSpace(cells[index]) == "" {
				t.Errorf("%s has empty owner/evidence/limitation column %d", id, index)
			}
		}
		if cells[1] == "PASS" && (len(links.FindAllString(cells[3], -1)) == 0 || !strings.Contains(cells[4], "_test.go")) {
			t.Errorf("%s PASS needs linked implementation and executable test evidence", id)
		}
		for _, link := range links.FindAllStringSubmatch(line, -1) {
			path := filepath.Clean(filepath.Join(root, "adr/specifications", link[1]))
			if !strings.HasPrefix(path, root+string(filepath.Separator)) {
				t.Errorf("%s evidence escapes repository: %s", id, link[1])
				continue
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("%s evidence link: %v", id, err)
			}
		}
		if cells[1] == "PASS" || len(testNames.FindAllString(cells[4], -1)) > 0 {
			names := testNames.FindAllString(cells[4], -1)
			if len(names) == 0 {
				t.Errorf("%s PASS requires named executable tests", id)
			}
			available := map[string]bool{}
			for _, link := range links.FindAllStringSubmatch(cells[4], -1) {
				path := filepath.Clean(filepath.Join(root, "adr/specifications", link[1]))
				if !strings.HasSuffix(path, "_test.go") {
					continue
				}
				if functions[path] == nil {
					file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
					if err != nil {
						t.Errorf("%s test reference: %v", id, err)
						continue
					}
					functions[path] = map[string]bool{}
					for _, declaration := range file.Decls {
						if function, ok := declaration.(*ast.FuncDecl); ok && function.Recv == nil {
							functions[path][function.Name.Name] = true
						}
					}
				}
				for name := range functions[path] {
					available[name] = true
				}
			}
			for _, name := range names {
				if !available[name] {
					t.Errorf("%s references test %s absent from its linked test files", id, name)
				}
			}
		}
	}
	for id := range expected {
		if !seen[id] {
			t.Errorf("qualification matrix omits %s", id)
		}
	}
	if got := [4]int{statusCounts["PASS"], statusCounts["PARTIAL"], statusCounts["FAIL"], statusCounts["NOT APPLICABLE"]}; got != [4]int{51, 51, 0, 0} {
		t.Errorf("qualification status totals = %v, want [51 51 0 0]", got)
	}
	for id, want := range map[string]string{"VAL-11": "PARTIAL", "STR-08": "PASS", "ENF-06": "PASS"} {
		if statuses[id] != want {
			t.Errorf("%s status = %q, want %q", id, statuses[id], want)
		}
	}
	for _, boundary := range []string{
		"active for the qualified supported profile",
		"Request-bound dashboard query authorization",
		"Explore protected catalog projection",
		"agent/MCP semantic resource reads",
		"no external provider adapter is qualified",
		"Complete cross-layer qualification remains **PARTIAL**",
		"unsupported rollup/substitution",
		"Audit persistence failure prevents protected admission or release",
		"FAI-649 binds this exact profile",
	} {
		if !strings.Contains(profile, boundary) {
			t.Errorf("supported profile omits boundary %q", boundary)
		}
	}
}
