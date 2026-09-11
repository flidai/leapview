// Command deadexports rejects exported Go declarations that have no repository
// references. The corpus intentionally includes documentation, configuration,
// generated artifacts, and non-Go source so reflection and cross-language
// contracts remain visible to the reviewable allowlist.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type declaration struct {
	Package  string `json:"package"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"`
}

type reviewedEntry struct {
	File     string `json:"file"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Receiver string `json:"receiver,omitempty"`
	Boundary string `json:"boundary"`
	Reason   string `json:"reason"`
}

type policy struct {
	Version        int               `json:"version"`
	BoundaryPolicy map[string]string `json:"boundaryPolicy"`
	Entries        []reviewedEntry   `json:"entries"`
}

type report struct {
	TotalDeclarations int             `json:"totalDeclarations"`
	CorpusFiles       int             `json:"corpusFiles"`
	Dead              []declaration   `json:"dead"`
	Allowlisted       []declaration   `json:"allowlisted"`
	StaleAllowlist    []reviewedEntry `json:"staleAllowlist,omitempty"`
}

var identifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
var generatedHeader = regexp.MustCompile(`(?m)^// Code generated .* DO NOT EDIT\.`)

// These are the standard method names whose implementations are commonly
// reached through an interface value without a repository call-site. They are
// deliberately conservative; a narrower method requires a reviewed policy
// entry rather than another heuristic.
var standardInterfaceMethods = map[string]bool{
	"As": true, "Begin": true, "Close": true, "Commit": true, "Done": true,
	"Do": true, "Error": true, "Err": true, "Exec": true, "Finalize": true,
	"Is": true, "Len": true, "Less": true, "Lock": true, "MarshalBinary": true,
	"MarshalJSON": true, "MarshalText": true, "MarshalXML": true, "Next": true,
	"Open": true, "Peek": true, "Pop": true, "Prepare": true, "Push": true,
	"Query": true, "QueryRow": true, "Read": true, "ReadAt": true, "ReadFrom": true,
	"Reset": true, "Rollback": true, "RLock": true, "Scan": true, "Seek": true,
	"Serve": true, "ServeHTTP": true, "String": true, "Swap": true, "TryLock": true,
	"TryRLock": true, "UnmarshalBinary": true, "UnmarshalJSON": true,
	"UnmarshalText": true, "UnmarshalXML": true, "Unlock": true, "Unwrap": true,
	"Validate": true, "Value": true, "Wait": true, "Write": true, "WriteAt": true,
	"WriteString": true, "WriteTo": true,
}

var generatedInputSentinels = []string{
	"internal/access/api/gen/server.apigen.gen.go",
	"internal/agent/api/gen/server.apigen.gen.go",
	"internal/analytics/api/gen/server.apigen.gen.go",
	"internal/app/api/gen/server.apigen.gen.go",
	"internal/dashboard/api/gen/server.apigen.gen.go",
	"internal/deployment/api/gen/server.apigen.gen.go",
	"internal/manageddata/api/gen/server.apigen.gen.go",
	"internal/project/api/gen/server.apigen.gen.go",
	"internal/refresh/api/gen/server.apigen.gen.go",
	"internal/release/api/gen/server.apigen.gen.go",
	"web/generated/signals/index.ts",
}

func main() {
	rootFlag := flag.String("root", ".", "repository root")
	allowlistFlag := flag.String("allowlist", ".quality/dead-exports.json", "reviewed dead-export allowlist")
	jsonFlag := flag.Bool("json", false, "emit the report as JSON")
	flag.Parse()

	root, err := filepath.Abs(*rootFlag)
	if err != nil {
		fail("resolve root: %v", err)
	}
	policyPath := *allowlistFlag
	if !filepath.IsAbs(policyPath) {
		policyPath = filepath.Join(root, policyPath)
	}
	pol, err := loadPolicy(policyPath, root)
	if err != nil {
		fail("load allowlist: %v", err)
	}
	report, err := scan(root, pol)
	if err != nil {
		fail("scan: %v", err)
	}
	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
	} else {
		fmt.Printf("exported declarations: %d\ntextual corpus files: %d\nallowlisted: %d\ndead: %d\n", report.TotalDeclarations, report.CorpusFiles, len(report.Allowlisted), len(report.Dead))
		for _, d := range report.Dead {
			fmt.Printf("DEAD %s:%d %s %s%s\n", d.File, d.Line, d.Kind, d.Name, receiverSuffix(d))
		}
		for _, e := range report.StaleAllowlist {
			fmt.Printf("STALE_ALLOWLIST %s %s %s\n", e.File, e.Kind, e.Name)
		}
	}
	if len(report.Dead) != 0 || len(report.StaleAllowlist) != 0 {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func loadPolicy(path, root string) (policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return policy{}, err
	}
	var p policy
	if err := json.Unmarshal(b, &p); err != nil {
		return policy{}, err
	}
	if p.Version != 1 {
		return policy{}, fmt.Errorf("unsupported version %d", p.Version)
	}
	for _, boundary := range []string{"reflection", "generated", "interface", "public-api"} {
		if strings.TrimSpace(p.BoundaryPolicy[boundary]) == "" {
			return policy{}, fmt.Errorf("boundaryPolicy.%s must document review criteria", boundary)
		}
	}
	seen := map[string]bool{}
	for i := range p.Entries {
		e := &p.Entries[i]
		clean := filepath.ToSlash(filepath.Clean(e.File))
		if clean == "." || filepath.IsAbs(e.File) || strings.HasPrefix(clean, "../") || clean != e.File {
			return policy{}, fmt.Errorf("entry %d has invalid repository-relative file %q", i, e.File)
		}
		e.File = clean
		if e.Kind != "func" && e.Kind != "method" && e.Kind != "type" && e.Kind != "var" && e.Kind != "const" {
			return policy{}, fmt.Errorf("entry %d has invalid kind %q", i, e.Kind)
		}
		if _, ok := p.BoundaryPolicy[e.Boundary]; !ok {
			return policy{}, fmt.Errorf("entry %d has invalid boundary %q", i, e.Boundary)
		}
		if len(strings.Fields(e.Reason)) < 8 {
			return policy{}, fmt.Errorf("entry %d reason must contain at least eight words", i)
		}
		key := declarationKey(declaration{File: e.File, Kind: e.Kind, Name: e.Name, Receiver: e.Receiver})
		if seen[key] {
			return policy{}, fmt.Errorf("duplicate entry %s", key)
		}
		seen[key] = true
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(e.File))); err != nil {
			return policy{}, fmt.Errorf("entry %d file %q: %w", i, e.File, err)
		}
	}
	return p, nil
}

func scan(root string, p policy) (report, error) {
	files, err := repositoryFiles(root)
	if err != nil {
		return report{}, err
	}
	if missing := missingGeneratedInputs(root); len(missing) != 0 {
		return report{}, fmt.Errorf("generated reference corpus is incomplete (%s); run `task generate` before deadexports:check", strings.Join(missing, ", "))
	}
	declFiles := make([]string, 0)
	allGoFiles := make([]string, 0)
	corpus := make([]string, 0)
	for _, file := range files {
		rel := relative(root, file)
		if ignored(rel) {
			continue
		}
		if strings.HasSuffix(file, ".go") {
			allGoFiles = append(allGoFiles, file)
			if !testFile(file) && !generated(file) && !generatedArtifact(rel) && !strings.Contains(rel, "/testdata/") && !scannerMetadata(rel) {
				declFiles = append(declFiles, file)
			}
		}
		if textual(file) && !scannerMetadata(rel) {
			corpus = append(corpus, file)
		}
	}

	fset := token.NewFileSet()
	var decls []declaration
	declOffsets := map[string]map[int]bool{}
	docOffsets := map[string]map[int]bool{}
	addOffset := func(dst map[string]map[int]bool, file string, offset int) {
		if dst[file] == nil {
			dst[file] = map[int]bool{}
		}
		dst[file][offset] = true
	}
	addDocOffsets := func(file string, src []byte, name string, groups ...*ast.CommentGroup) {
		for _, group := range groups {
			if group == nil {
				continue
			}
			for _, comment := range group.List {
				start := fset.Position(comment.Pos()).Offset
				end := fset.Position(comment.End()).Offset
				if start < 0 || end > len(src) || start >= end {
					continue
				}
				for _, loc := range identifier.FindAllIndex(src[start:end], -1) {
					if string(src[start+loc[0]:start+loc[1]]) == name {
						addOffset(docOffsets, file, start+loc[0])
					}
				}
			}
		}
	}
	for _, file := range declFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			return report{}, err
		}
		if generatedHeader.Match(src) {
			continue
		}
		af, err := parser.ParseFile(fset, file, src, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return report{}, fmt.Errorf("parse %s: %w", relative(root, file), err)
		}
		rel := relative(root, file)
		add := func(id *ast.Ident, kind, receiver string, groups ...*ast.CommentGroup) {
			if !ast.IsExported(id.Name) {
				return
			}
			pos := fset.Position(id.Pos())
			decls = append(decls, declaration{Package: af.Name.Name, File: rel, Line: pos.Line, Kind: kind, Name: id.Name, Receiver: receiver})
			addOffset(declOffsets, rel, pos.Offset)
			addDocOffsets(rel, src, id.Name, groups...)
		}
		for _, raw := range af.Decls {
			switch d := raw.(type) {
			case *ast.FuncDecl:
				receiver := ""
				kind := "func"
				if d.Recv != nil {
					kind = "method"
					receiver = receiverName(d)
				}
				add(d.Name, kind, receiver, d.Doc)
			case *ast.GenDecl:
				for _, rawSpec := range d.Specs {
					switch spec := rawSpec.(type) {
					case *ast.TypeSpec:
						add(spec.Name, "type", "", spec.Doc, spec.Comment)
					case *ast.ValueSpec:
						kind := "var"
						if d.Tok.String() == "const" {
							kind = "const"
						}
						for _, name := range spec.Names {
							add(name, kind, "", spec.Doc, spec.Comment)
						}
					}
				}
			}
		}
	}

	interfaceMethods := collectInterfaceMethods(fset, allGoFiles)
	counts := map[string]int{}
	for _, file := range corpus {
		src, err := os.ReadFile(file)
		if err != nil {
			return report{}, err
		}
		rel := relative(root, file)
		for _, loc := range identifier.FindAllIndex(src, -1) {
			if declOffsets[rel][loc[0]] || docOffsets[rel][loc[0]] {
				continue
			}
			counts[string(src[loc[0]:loc[1]])]++
		}
	}
	dead := make([]declaration, 0)
	for _, d := range decls {
		if d.Name == "init" || d.Name == "main" || counts[d.Name] > 0 {
			continue
		}
		if d.Kind == "method" && (interfaceMethods[d.Name] || standardInterfaceMethods[d.Name]) {
			continue
		}
		dead = append(dead, d)
	}
	sort.Slice(dead, func(i, j int) bool { return declarationKey(dead[i]) < declarationKey(dead[j]) })
	result := report{TotalDeclarations: len(decls), CorpusFiles: len(corpus)}
	allowlisted := map[string]bool{}
	for _, d := range dead {
		if entry, ok := matchingEntry(p.Entries, d); ok {
			allowlisted[declarationKey(d)] = true
			result.Allowlisted = append(result.Allowlisted, d)
			_ = entry
		} else {
			result.Dead = append(result.Dead, d)
		}
	}
	for _, entry := range p.Entries {
		if !allowlisted[declarationKey(declaration{File: entry.File, Kind: entry.Kind, Name: entry.Name, Receiver: entry.Receiver})] {
			result.StaleAllowlist = append(result.StaleAllowlist, entry)
		}
	}
	return result, nil
}

// Generation is a deliberate precondition of this guard. Ignored APIGen,
// sqlc, and signal outputs contain real consumers but are not available in a
// clean checkout. Failing closed here makes a direct invocation deterministic
// instead of producing a different dead-export set based on workspace state.
func missingGeneratedInputs(root string) []string {
	var missing []string
	for _, path := range generatedInputSentinels {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			missing = append(missing, path)
		}
	}
	return missing
}

func collectInterfaceMethods(fset *token.FileSet, files []string) map[string]bool {
	methods := map[string]bool{}
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		af, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(af, func(node ast.Node) bool {
			iface, ok := node.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, field := range iface.Methods.List {
				for _, name := range field.Names {
					methods[name.Name] = true
				}
			}
			return true
		})
	}
	return methods
}

func matchingEntry(entries []reviewedEntry, d declaration) (reviewedEntry, bool) {
	for _, entry := range entries {
		if declarationKey(declaration{File: entry.File, Kind: entry.Kind, Name: entry.Name, Receiver: entry.Receiver}) == declarationKey(d) {
			return entry, true
		}
	}
	return reviewedEntry{}, false
}

func declarationKey(d declaration) string {
	return strings.Join([]string{d.File, d.Kind, d.Name, d.Receiver}, "\x00")
}

func receiverName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	t := d.Recv.List[0].Type
	for {
		switch value := t.(type) {
		case *ast.StarExpr:
			t = value.X
		case *ast.IndexExpr:
			t = value.X
		case *ast.IndexListExpr:
			t = value.X
		case *ast.Ident:
			return value.Name
		case *ast.SelectorExpr:
			return value.Sel.Name
		default:
			return "?"
		}
	}
}

func repositoryFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	files := make([]string, 0)
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" {
			return
		}
		full := filepath.Join(root, filepath.FromSlash(path))
		if _, err := os.Stat(full); err == nil && !seen[full] {
			seen[full] = true
			files = append(files, full)
		}
	}
	for _, path := range strings.Split(string(out), "\x00") {
		add(path)
	}
	// Generation intentionally leaves several consumer artifacts ignored by
	// Git. Include only known generated paths; arbitrary build output is not a
	// source reference corpus.
	ignored := exec.Command("git", "-C", root, "ls-files", "-oi", "--exclude-standard", "-z")
	if generated, err := ignored.Output(); err == nil {
		for _, path := range strings.Split(string(generated), "\x00") {
			if generatedArtifact(path) {
				add(path)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func generatedArtifact(path string) bool {
	path = filepath.ToSlash(path)
	return strings.Contains(path, "/gen/") || strings.HasPrefix(path, "web/generated/") || strings.HasSuffix(path, ".gen.go") || strings.HasSuffix(path, "_gen.go") || strings.HasSuffix(path, ".sql.go") || strings.HasSuffix(path, ".gen.ts") || strings.HasSuffix(path, ".gen.json")
}

func relative(root, file string) string {
	path, _ := filepath.Rel(root, file)
	return filepath.ToSlash(path)
}

func testFile(path string) bool { return strings.HasSuffix(path, "_test.go") }

func generated(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && generatedHeader.Match(b)
}

func textual(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && !strings.ContainsRune(string(b), '\x00')
}

func ignored(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" || part == "node_modules" || part == ".tmp" || part == ".venv" || part == "vendor" {
			return true
		}
	}
	return false
}

func scannerMetadata(path string) bool {
	return path == ".quality/dead-exports.json" || strings.HasPrefix(path, "internal/app/tools/deadexports/")
}

func receiverSuffix(d declaration) string {
	if d.Receiver == "" {
		return ""
	}
	return " receiver=" + d.Receiver
}
