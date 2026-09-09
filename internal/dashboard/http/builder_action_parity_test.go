package http

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestDashboardBuilderBrowserActionParity keeps the authored browser-action
// matrix honest without duplicating the action list in test code. The browser
// source is the contract for actions that can actually be emitted; the Go AST
// check follows the server's switch action translation and allows additional
// server-only actions used by headless/API callers. The marked docs table is
// parsed structurally so prose changes do not create false positives.
func TestDashboardBuilderBrowserActionParity(t *testing.T) {
	root := builderContractRepoRoot(t)
	browserSource := readBuilderContractFile(t, filepath.Join(root, "web", "components", "dashboard", "dashboard-builder.ts"))
	serverSourcePath := filepath.Join(root, "internal", "dashboard", "http", "builder.go")
	docsPath := filepath.Join(root, "docs", "articles", "operate", "dashboard-authoring.md")
	docsSource := readBuilderContractFile(t, docsPath)

	browserActions := browserBuilderCommandActions(browserSource)
	if len(browserActions) == 0 {
		t.Fatal("dashboard builder source yielded no literal this.emitCommand actions")
	}
	serverActions := translatedBuilderActions(t, serverSourcePath)
	documentedActions := documentedBuilderActions(t, docsSource)

	assertActionSetEqual(t, "browser source and docs matrix", browserActions, documentedActions)
	for action := range browserActions {
		if _, ok := serverActions[action]; !ok {
			t.Errorf("browser action %q has no server translation case in builder.go", action)
		}
	}
}

var browserBuilderCommandPattern = regexp.MustCompile(`this\.emitCommand\(\s*["']([a-z][a-z0-9_]*)["']`)
var documentedBuilderActionPattern = regexp.MustCompile(`^` + "`" + `([a-z][a-z0-9_]*)` + "`" + `$`)

func browserBuilderCommandActions(source string) map[string]struct{} {
	actions := make(map[string]struct{})
	for _, match := range browserBuilderCommandPattern.FindAllStringSubmatch(source, -1) {
		actions[match[1]] = struct{}{}
	}
	return actions
}

func translatedBuilderActions(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	source := readBuilderContractFile(t, path)
	file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	actions := make(map[string]struct{})
	ast.Inspect(file, func(node ast.Node) bool {
		switchStmt, ok := node.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		tag, ok := switchStmt.Tag.(*ast.Ident)
		if !ok || tag.Name != "action" {
			return true
		}
		for _, statement := range switchStmt.Body.List {
			clause, ok := statement.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expression := range clause.List {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				action, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote builder action %s: %v", literal.Value, err)
				}
				actions[action] = struct{}{}
			}
		}
		return true
	})
	if len(actions) == 0 {
		t.Fatalf("builder.go has no switch action translation cases")
	}
	return actions
}

func documentedBuilderActions(t *testing.T, source string) map[string]struct{} {
	t.Helper()
	const startMarker = "<!-- browser-action-parity:start -->"
	const endMarker = "<!-- browser-action-parity:end -->"
	start := strings.Index(source, startMarker)
	if start < 0 {
		t.Fatalf("dashboard authoring docs are missing %s", startMarker)
	}
	end := strings.Index(source[start+len(startMarker):], endMarker)
	if end < 0 {
		t.Fatalf("dashboard authoring docs are missing %s", endMarker)
	}
	body := source[start+len(startMarker) : start+len(startMarker)+end]
	actions := make(map[string]struct{})
	rows := 0
	for lineNumber, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 4 {
			t.Fatalf("browser-action matrix row %d has %d cells, want 4: %q", lineNumber+1, len(cells), line)
		}
		if strings.TrimSpace(cells[0]) == "Browser surface" || strings.Trim(strings.TrimSpace(cells[0]), "-") == "" {
			continue
		}
		for _, index := range []int{0, 2, 3} {
			if strings.TrimSpace(cells[index]) == "" {
				t.Fatalf("browser-action matrix row %d has an empty contract column: %q", lineNumber+1, line)
			}
		}
		match := documentedBuilderActionPattern.FindStringSubmatch(strings.TrimSpace(cells[1]))
		if len(match) != 2 {
			t.Fatalf("browser-action matrix row %d command cell must be one backtick action: %q", lineNumber+1, cells[1])
		}
		if _, exists := actions[match[1]]; exists {
			t.Fatalf("browser-action matrix documents duplicate action %q", match[1])
		}
		actions[match[1]] = struct{}{}
		rows++
	}
	if rows == 0 {
		t.Fatal("browser-action matrix contains no data rows")
	}
	return actions
}

func assertActionSetEqual(t *testing.T, label string, left, right map[string]struct{}) {
	t.Helper()
	for action := range left {
		if _, ok := right[action]; !ok {
			t.Errorf("%s: action %q is missing from right-hand set", label, action)
		}
	}
	for action := range right {
		if _, ok := left[action]; !ok {
			t.Errorf("%s: action %q is missing from left-hand set", label, action)
		}
	}
}

func builderContractRepoRoot(t *testing.T) string {
	t.Helper()
	seeds := make([]string, 0, 2)
	if _, filename, _, ok := runtime.Caller(0); ok {
		seeds = append(seeds, filepath.Dir(filename))
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		seeds = append(seeds, workingDirectory)
	}
	for _, seed := range seeds {
		for directory := filepath.Clean(seed); ; directory = filepath.Dir(directory) {
			if _, err := os.Stat(filepath.Join(directory, "docs", "articles", "operate", "dashboard-authoring.md")); err == nil {
				return directory
			}
			parent := filepath.Dir(directory)
			if parent == directory {
				break
			}
		}
	}
	t.Fatal("could not locate LeapView repository root for builder contract test")
	return ""
}

func readBuilderContractFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read builder contract file %s: %v", path, err)
	}
	return string(contents)
}
