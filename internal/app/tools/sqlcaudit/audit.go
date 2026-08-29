// Command sqlcaudit enforces the PostgreSQL sqlc boundary.
//
// It deliberately uses Go's parser rather than grep: multiline calls and raw
// string literals are handled by the syntax tree, while sqlc output in the
// repository's generated-package layout and test files are excluded before
// inspection. A source comment is never sufficient to claim generated status.
// sqlc_exceptions.yaml contains only the narrowly scoped ADR-0016 exceptions
// (DDL, dynamic identifiers/result shapes, or analyzer-incompatible protocol
// statements). Static handwritten SQL is never allowlisted: every static call
// must be converted to a generated sqlc query.
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

var markerRE = regexp.MustCompile(`(?m)sqlc-exception\s*:\s*([A-Za-z0-9_-]+)`)

var allowedClasses = map[string]struct{}{
	"schema-ddl":            {},
	"dynamic-identifier":    {},
	"dynamic-result-shape":  {},
	"analyzer-incompatible": {},
	"listen-protocol":       {},
}

type entry struct {
	File         string `yaml:"file"`
	Line         int    `yaml:"line"`
	Fingerprint  string `yaml:"fingerprint"`
	Function     string `yaml:"function"`
	Method       string `yaml:"method"`
	Class        string `yaml:"class"`
	Rationale    string `yaml:"rationale"`
	Verification string `yaml:"verification"`
}

type ledger struct {
	Entries []entry `yaml:"entries"`
}

type finding struct {
	File        string
	Line        int
	Function    string
	Method      string
	Fingerprint string
	Marker      string
	Source      string // marker, inventory, or unclassified
	Static      bool
	SQL         string
}

type auditResult struct {
	Findings []finding
	Errors   []error
}

func main() {
	root := flag.String("root", ".", "repository root")
	dumpExceptions := flag.Bool("dump-exceptions", false, "print exact exception inventory entries for unclassified dynamic calls")
	flag.Parse()
	result := audit(*root)
	if *dumpExceptions {
		fmt.Println("entries:")
		for _, f := range result.Findings {
			if f.Source != "unclassified" || f.Static {
				continue
			}
			class := exceptionClass(f)
			rationale := exceptionRationale(class)
			fmt.Printf("  - file: %s\n    line: %d\n    function: %s\n    method: %s\n    fingerprint: %s\n    class: %s\n    rationale: %s\n    verification: Covered by the owning capability tests.\n", f.File, f.Line, f.Function, f.Method, f.Fingerprint, class, rationale)
		}
		return
	}
	for _, err := range result.Errors {
		fmt.Fprintln(os.Stderr, "sqlc audit:", err)
	}
	if len(result.Errors) > 0 {
		os.Exit(1)
	}
	fmt.Printf("sqlc audit: %d PostgreSQL raw SQL call sites accounted for\n", len(result.Findings))
}

func exceptionClass(f finding) string {
	if f.Function == "ApplySchema" || strings.Contains(f.File, "/migrations/") {
		return "schema-ddl"
	}
	if strings.Contains(f.File, "/postgrestest/") {
		return "dynamic-identifier"
	}
	if f.Function == "Pool.Exec" || f.Function == "Pool.Query" || f.Function == "Pool.QueryRow" {
		return "analyzer-incompatible"
	}
	return "dynamic-result-shape"
}

func exceptionRationale(class string) string {
	switch class {
	case "schema-ddl":
		return "Capability-owned schema or migration DDL is executed as a caller-owned statement rather than a generated query leaf."
	case "dynamic-identifier":
		return "A validated PostgreSQL identifier is selected at runtime; identifiers cannot be supplied as query parameters."
	case "dynamic-result-shape":
		return "The result projection or filter shape varies at runtime; the owning capability validates the assembled statement."
	case "analyzer-incompatible":
		return "This PostgreSQL statement uses syntax or protocol behavior that the sqlc analyzer cannot model."
	case "listen-protocol":
		return "LISTEN is connection-local PostgreSQL protocol control and cannot be represented by a parameterized sqlc query."
	default:
		return "The owning capability documents and tests this narrowly scoped SQL exception."
	}
}

func validateExceptionClass(f finding, class string) error {
	switch class {
	case "dynamic-identifier", "dynamic-result-shape", "listen-protocol":
		if f.Static {
			return fmt.Errorf("sqlc exception class %q cannot classify a resolved static SQL expression", class)
		}
	case "schema-ddl":
		if f.Method != "Exec" {
			return fmt.Errorf("sqlc exception class %q requires an Exec call", class)
		}
		if f.Static && !looksLikeDDL(f.SQL) {
			return fmt.Errorf("sqlc exception class %q cannot classify a static non-DDL statement", class)
		}
	}
	return nil
}

func audit(root string) auditResult {
	var result auditResult
	root, err := filepath.Abs(root)
	if err != nil {
		result.Errors = append(result.Errors, err)
		return result
	}
	exceptions, exceptionErrors := loadLedgers(root, "sqlc_exceptions.yaml")
	result.Errors = append(result.Errors, exceptionErrors...)

	usedException := make(map[string]bool)
	markerUse := make(map[string]bool)

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".tmp") {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || !isPostgresGo(path) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if isGenerated(path, data) {
			return nil
		}
		findings, fileErrors := inspectFile(root, path, data, exceptions, usedException, markerUse)
		result.Findings = append(result.Findings, findings...)
		result.Errors = append(result.Errors, fileErrors...)
		return nil
	})
	if err != nil {
		result.Errors = append(result.Errors, err)
	}
	for key, e := range exceptions {
		if !usedException[key] {
			result.Errors = append(result.Errors, fmt.Errorf("stale sqlc exception inventory entry %s:%d (%s)", e.File, e.Line, e.Method))
		}
	}
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].Error() < result.Errors[j].Error() })
	sort.Slice(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		return result.Findings[i].Line < result.Findings[j].Line
	})
	return result
}

func inspectFile(root, path string, data []byte, exceptions map[string]entry, usedException map[string]bool, markerUse map[string]bool) ([]finding, []error) {
	relPath := rel(root, path)
	lines := strings.Split(string(data), "\n")
	markers := make([]struct {
		line  int
		class string
		key   string
	}, 0)
	for i, line := range lines {
		for _, match := range markerRE.FindAllStringSubmatchIndex(line, -1) {
			class := line[match[2]:match[3]]
			key := fmt.Sprintf("%s:%d:%d", relPath, i+1, match[0])
			markers = append(markers, struct {
				line  int
				class string
				key   string
			}{i + 1, class, key})
		}
	}

	fset := token.NewFileSet()
	// ParseFile's positions are tied to its own file set. Re-parse with a set
	// retained here so position-to-line conversion is stable.
	fset = token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, data, parser.ParseComments)
	if err != nil {
		return nil, []error{err}
	}
	funcs := functionRanges(parsed, fset)
	stringConsts := collectStringConsts(parsed)
	var findings []finding
	var errs []error
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		method := selectorName(call)
		if method != "Exec" && method != "Query" && method != "QueryRow" {
			return true
		}
		// A database call has context and SQL arguments. This excludes
		// unrelated selectors such as net/url.Values.Query().
		if len(call.Args) < 2 {
			return true
		}
		pos := fset.Position(call.Pos())
		fn := functionAt(funcs, call.Pos())
		resolvedSQL, resolved := sqlExpression(call.Args, stringConsts)
		base := finding{File: relPath, Line: pos.Line, Function: fn, Method: method, Fingerprint: expressionFingerprint(call, stringConsts), Static: resolved && !isProtocolSQL(resolvedSQL), SQL: resolvedSQL}
		markerIndex := nearestMarker(markers, pos.Line, markerUse)
		if markerIndex >= 0 {
			m := markers[markerIndex]
			if _, valid := allowedClasses[m.class]; !valid {
				errs = append(errs, fmt.Errorf("%s:%d: unknown sqlc exception class %q", relPath, pos.Line, m.class))
			} else if classErr := validateExceptionClass(base, m.class); classErr != nil {
				errs = append(errs, fmt.Errorf("%s:%d: %w", relPath, pos.Line, classErr))
			} else {
				base.Marker, base.Source = m.class, "marker"
				markerUse[m.key] = true
				findings = append(findings, base)
			}
			return true
		}
		key := findingKey(base)
		if e, ok := exceptions[key]; ok {
			if _, valid := allowedClasses[e.Class]; !valid {
				errs = append(errs, fmt.Errorf("%s:%d: unknown sqlc exception class %q", relPath, pos.Line, e.Class))
				return true
			}
			if classErr := validateExceptionClass(base, e.Class); classErr != nil {
				errs = append(errs, fmt.Errorf("%s:%d: %w", relPath, pos.Line, classErr))
				return true
			}
			base.Marker, base.Source = e.Class, "inventory"
			usedException[key] = true
			findings = append(findings, base)
			return true
		}
		if isStaticSQLExpr(call.Args, stringConsts) {
			errs = append(errs, fmt.Errorf("%s:%d: unclassified PostgreSQL %s call in %s (add a sqlc query or adjacent sqlc-exception:<class> only for a justified exception)", relPath, pos.Line, method, fn))
		} else {
			errs = append(errs, fmt.Errorf("%s:%d: unclassified dynamic PostgreSQL %s call in %s (add adjacent sqlc-exception:<class> or exact capability inventory entry)", relPath, pos.Line, method, fn))
		}
		base.Source = "unclassified"
		findings = append(findings, base)
		return true
	})
	for _, m := range markers {
		if !markerUse[m.key] {
			errs = append(errs, fmt.Errorf("%s:%d: stale sqlc exception marker %q", relPath, m.line, m.class))
		}
	}
	return findings, errs
}

func isStaticSQLExpr(args []ast.Expr, consts map[string]string) bool {
	value, ok := sqlExpression(args, consts)
	return ok && !isProtocolSQL(value)
}

func sqlExpression(args []ast.Expr, consts map[string]string) (string, bool) {
	if len(args) < 2 {
		return "", false
	}
	return resolveStringExpr(args[1], consts)
}

func isProtocolSQL(value string) bool {
	// LISTEN/UNLISTEN protocol statements remain dynamic SQL even when their
	// channel is a package constant. Keep them in the listen-protocol exception
	// class rather than treating them as ordinary static SQL.
	trimmed := strings.TrimSpace(value)
	return strings.HasPrefix(trimmed, "LISTEN ") || strings.HasPrefix(trimmed, "UNLISTEN ")
}

func looksLikeDDL(value string) bool {
	trimmed := strings.TrimSpace(value)
	for strings.HasPrefix(trimmed, "--") {
		if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
			trimmed = strings.TrimSpace(trimmed[newline+1:])
			continue
		}
		return false
	}
	upper := strings.ToUpper(trimmed)
	for _, keyword := range []string{"ALTER ", "COMMENT ", "CREATE ", "DO ", "DROP ", "GRANT ", "REVOKE ", "TRUNCATE "} {
		if strings.HasPrefix(upper, keyword) {
			return true
		}
	}
	return false
}

func collectStringConsts(file *ast.File) map[string]string {
	consts := make(map[string]string)
	// Resolve package-level string constants to their values. Iterating to a
	// fixed point handles declarations that refer to a constant declared later
	// in the same const block while remaining scope-safe (locals are ignored).
	type declaration struct {
		name string
		expr ast.Expr
	}
	var declarations []declaration
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var previous []ast.Expr
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			expressions := values.Values
			if len(expressions) == 0 {
				expressions = previous
			}
			for i, name := range values.Names {
				if i < len(expressions) {
					declarations = append(declarations, declaration{name: name.Name, expr: expressions[i]})
				}
			}
			if len(values.Values) > 0 {
				previous = values.Values
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, declaration := range declarations {
			value, ok := resolveStringExpr(declaration.expr, consts)
			if ok && consts[declaration.name] != value {
				consts[declaration.name] = value
				changed = true
			}
		}
	}
	return consts
}

func resolveStringExpr(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		value, err := strconv.Unquote(e.Value)
		return value, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := resolveStringExpr(e.X, consts)
		if !ok {
			return "", false
		}
		right, ok := resolveStringExpr(e.Y, consts)
		if !ok {
			return "", false
		}
		return left + right, true
	case *ast.ParenExpr:
		return resolveStringExpr(e.X, consts)
	case *ast.Ident:
		value, ok := consts[e.Name]
		return value, ok
	default:
		return "", false
	}
}

func expressionFingerprint(call *ast.CallExpr, consts map[string]string) string {
	if len(call.Args) < 2 {
		return "sha256:missing-sql-argument"
	}
	value, resolved := resolveStringExpr(call.Args[1], consts)
	var source string
	if resolved {
		// Fingerprint the semantic SQL value, not merely the identifier spelling,
		// so changing a package constant cannot evade static SQL detection.
		source = "sql:" + value
	} else {
		var b strings.Builder
		if err := format.Node(&b, token.NewFileSet(), call.Args[1]); err != nil {
			b.WriteString(fmt.Sprintf("%T", call.Args[1]))
		}
		source = b.String()
	}
	h := sha256.Sum256([]byte(source))
	return fmt.Sprintf("sha256:%x", h[:])
}

func selectorName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	return sel.Sel.Name
}

func nearestMarker(markers []struct {
	line  int
	class string
	key   string
}, line int, used map[string]bool) int {
	best := -1
	for i := range markers {
		delta := line - markers[i].line
		if delta < 0 || delta > 3 {
			continue
		}
		if used[markers[i].key] {
			continue
		}
		if best < 0 || markers[i].line > markers[best].line {
			best = i
		}
	}
	return best
}

type functionRange struct {
	start, end token.Pos
	name       string
}

func functionRanges(file *ast.File, fset *token.FileSet) []functionRange {
	var out []functionRange
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			name = receiverName(fn) + "." + name
		}
		out = append(out, functionRange{fn.Body.Pos(), fn.Body.End(), name})
		return true
	})
	_ = fset
	return out
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if ident, ok := t.(*ast.Ident); ok {
		return ident.Name
	}
	return "receiver"
}

func functionAt(ranges []functionRange, pos token.Pos) string {
	name := "<package>"
	width := token.Pos(1<<62 - 1)
	for _, r := range ranges {
		if pos >= r.start && pos <= r.end {
			if r.end-r.start < width {
				name, width = r.name, r.end-r.start
			}
		}
	}
	return name
}

func findingKey(f finding) string {
	return fmt.Sprintf("%s:%d:%s:%s:%s", f.File, f.Line, f.Function, f.Method, f.Fingerprint)
}

func loadLedgers(root, name string) (map[string]entry, []error) {
	out := make(map[string]entry)
	var errs []error
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".tmp") {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != name {
			return nil
		}
		loaded, loadErrs := loadLedger(path)
		for key, e := range loaded {
			if _, exists := out[key]; exists {
				errs = append(errs, fmt.Errorf("duplicate sqlc exception inventory entry %s", key))
			}
			out[key] = e
		}
		errs = append(errs, loadErrs...)
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	return out, errs
}

func loadLedger(path string) (map[string]entry, []error) {
	out := make(map[string]entry)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, []error{err}
	}
	var doc ledger
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return out, []error{fmt.Errorf("parse %s: %w", path, err)}
	}
	var errs []error
	for _, e := range doc.Entries {
		if e.File == "" || e.Function == "" || e.Method == "" || e.Fingerprint == "" || e.Line <= 0 {
			errs = append(errs, fmt.Errorf("%s: entries require file, line, function, method, and fingerprint", path))
			continue
		}
		if e.Class == "" || e.Rationale == "" || e.Verification == "" {
			errs = append(errs, fmt.Errorf("%s:%s:%d: exception requires class, rationale, and verification", path, e.File, e.Line))
			continue
		}
		key := findingKey(finding{File: filepath.ToSlash(e.File), Line: e.Line, Function: e.Function, Method: e.Method, Fingerprint: e.Fingerprint})
		if _, exists := out[key]; exists {
			errs = append(errs, fmt.Errorf("%s: duplicate entry %s", path, key))
			continue
		}
		e.File = filepath.ToSlash(e.File)
		out[key] = e
	}
	return out, errs
}

func rel(root, path string) string {
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(relPath)
}

func isPostgresGo(path string) bool {
	path = filepath.ToSlash(path)
	base := filepath.Base(path)
	if strings.HasPrefix(base, "postgres") {
		return true
	}
	for _, part := range strings.Split(path, "/") {
		if part == "postgres" || strings.HasPrefix(part, "postgres_") || strings.HasSuffix(part, "_postgres.go") {
			return true
		}
	}
	return false
}

func isGenerated(path string, data []byte) bool {
	// Every sqlc stanza emits beneath a capability-owned internal/db package.
	// Requiring both that path boundary and sqlc's exact header prevents a
	// handwritten repository from evading the raw-SQL audit with a forged
	// "generated" or generic "DO NOT EDIT" comment.
	normalized := "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")
	if !strings.Contains(normalized, "/internal/db/") {
		return false
	}
	return bytes.HasPrefix(data, []byte("// Code generated by sqlc. DO NOT EDIT.\n"))
}
