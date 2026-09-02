package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	complexityReviewThreshold = 15
	ownershipCommitWindow     = 200
	reportLimit               = 10
)

type complexityRecord struct {
	Path       string `json:"path"`
	Function   string `json:"function"`
	Line       int    `json:"line"`
	Complexity int    `json:"complexity"`
}

type ownershipRecord struct {
	Path    string   `json:"path"`
	Changes int      `json:"changes"`
	Authors []string `json:"authors"`
}

type report struct {
	ComplexFunctions int                `json:"complexFunctions"`
	Complexity       []complexityRecord `json:"complexity"`
	OwnershipCommits int                `json:"ownershipCommits"`
	Ownership        []ownershipRecord  `json:"ownership"`
}

type ownershipAccumulator struct {
	changes int
	authors map[string]struct{}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("qualitytrends", flag.ContinueOnError)
	root := flags.String("root", ".", "repository root")
	jsonOutput := flags.Bool("json", false, "render the report as JSON")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	files, err := trackedFiles(absoluteRoot)
	if err != nil {
		return err
	}
	complexity, complexFunctions, err := analyzeGoComplexity(absoluteRoot, files)
	if err != nil {
		return err
	}
	ownershipOutput, err := gitOutput(absoluteRoot, "log", "--first-parent", "-n", strconv.Itoa(ownershipCommitWindow), "--format=commit:%H%x09%aN", "--name-only")
	if err != nil {
		return fmt.Errorf("read ownership history: %w", err)
	}
	ownership, commits := parseOwnershipLog(string(ownershipOutput))
	result := report{ComplexFunctions: complexFunctions, Complexity: complexity, OwnershipCommits: commits, Ownership: ownership}
	if *jsonOutput {
		body, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(body))
		return nil
	}
	printReport(result)
	return nil
}

func trackedFiles(root string) ([]string, error) {
	output, err := gitOutput(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	var files []string
	for _, part := range bytes.Split(output, []byte{0}) {
		if len(part) > 0 {
			files = append(files, filepath.ToSlash(string(part)))
		}
	}
	return files, nil
}

func analyzeGoComplexity(root string, files []string) ([]complexityRecord, int, error) {
	var records []complexityRecord
	complexFunctions := 0
	for _, path := range files {
		if !strings.HasSuffix(path, ".go") || excludedPath(path) || strings.HasSuffix(path, ".gen.go") || strings.HasSuffix(path, "_generated.go") {
			continue
		}
		absolute := filepath.Join(root, filepath.FromSlash(path))
		body, err := os.ReadFile(absolute)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", path, err)
		}
		if generatedHeader(body) {
			continue
		}
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, absolute, body, 0)
		if err != nil {
			return nil, 0, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			value := functionComplexity(function.Body)
			if value > complexityReviewThreshold {
				complexFunctions++
			}
			records = append(records, complexityRecord{Path: path, Function: function.Name.Name, Line: set.Position(function.Pos()).Line, Complexity: value})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Complexity == records[j].Complexity {
			if records[i].Path == records[j].Path {
				return records[i].Line < records[j].Line
			}
			return records[i].Path < records[j].Path
		}
		return records[i].Complexity > records[j].Complexity
	})
	if len(records) > reportLimit {
		records = records[:reportLimit]
	}
	return records, complexFunctions, nil
}

func functionComplexity(body *ast.BlockStmt) int {
	complexity := 1
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.CommClause:
			complexity++
		case *ast.CaseClause:
			if typed.List != nil {
				complexity++
			}
		case *ast.BinaryExpr:
			if typed.Op == token.LAND || typed.Op == token.LOR {
				complexity++
			}
		}
		return true
	})
	return complexity
}

func parseOwnershipLog(output string) ([]ownershipRecord, int) {
	byPath := make(map[string]*ownershipAccumulator)
	currentAuthor := ""
	commits := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "commit:") {
			parts := strings.SplitN(line, "\t", 2)
			currentAuthor = "unknown"
			if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
				currentAuthor = strings.TrimSpace(parts[1])
			}
			commits++
			continue
		}
		path := filepath.ToSlash(strings.TrimSpace(line))
		if currentAuthor == "" || !authoredSource(path) {
			continue
		}
		item := byPath[path]
		if item == nil {
			item = &ownershipAccumulator{authors: make(map[string]struct{})}
			byPath[path] = item
		}
		item.changes++
		item.authors[currentAuthor] = struct{}{}
	}
	records := make([]ownershipRecord, 0, len(byPath))
	for path, item := range byPath {
		authors := make([]string, 0, len(item.authors))
		for author := range item.authors {
			authors = append(authors, author)
		}
		sort.Strings(authors)
		records = append(records, ownershipRecord{Path: path, Changes: item.changes, Authors: authors})
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Changes == records[j].Changes {
			if len(records[i].Authors) == len(records[j].Authors) {
				return records[i].Path < records[j].Path
			}
			return len(records[i].Authors) > len(records[j].Authors)
		}
		return records[i].Changes > records[j].Changes
	})
	if len(records) > reportLimit {
		records = records[:reportLimit]
	}
	return records, commits
}

func authoredSource(path string) bool {
	if path == "" || excludedPath(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return !strings.HasSuffix(path, ".d.ts")
	default:
		return false
	}
}

func excludedPath(path string) bool {
	wrapped := "/" + strings.Trim(filepath.ToSlash(path), "/") + "/"
	for _, segment := range []string{"/.git/", "/.tmp/", "/node_modules/", "/vendor/", "/dist/", "/gen/", "/generated/"} {
		if strings.Contains(wrapped, segment) {
			return true
		}
	}
	return false
}

func generatedHeader(body []byte) bool {
	for index, line := range bytes.Split(body, []byte{'\n'}) {
		if index >= 10 {
			break
		}
		lower := strings.ToLower(strings.TrimSpace(string(line)))
		if strings.HasPrefix(lower, "// code generated") && strings.Contains(lower, "do not edit") {
			return true
		}
	}
	return false
}

func gitOutput(root string, arguments ...string) ([]byte, error) {
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	return command.Output()
}

func printReport(result report) {
	fmt.Printf("Go structural complexity: %d functions above %d decision points\n", result.ComplexFunctions, complexityReviewThreshold)
	for _, item := range result.Complexity {
		fmt.Printf("- %s:%d %s: %d\n", item.Path, item.Line, item.Function, item.Complexity)
	}
	fmt.Printf("Ownership concentration: last %d first-parent commits\n", result.OwnershipCommits)
	for _, item := range result.Ownership {
		fmt.Printf("- %s: %d changes, %d authors\n", item.Path, item.Changes, len(item.Authors))
	}
}
