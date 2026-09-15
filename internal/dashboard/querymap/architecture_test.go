package querymap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var (
	queryInputPattern      = regexp.MustCompile(`(?:\b(?:reportdef|querymap)\.)?\bQuery(Field|Filter|Sort)\b`)
	dataConstructorPattern = regexp.MustCompile(`\bdataquery\.(Field|Filter|Sort)\s*\{`)
)

// TestReportToDataQueryConversionHasOneOwner keeps the conversion boundary
// architectural, rather than relying only on the package comment. It scans
// every non-generated production Go file under internal/, so a new report
// mapper cannot hide in an uncatalogued dashboard or agent package.
func TestReportToDataQueryConversionHasOneOwner(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate querymap package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	internalRoot := filepath.Join(root, "internal")
	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if filepath.Clean(path) == filepath.Dir(thisFile) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if isGenerated(source) {
			return nil
		}
		if hasLocalReportDataMapping(path, source) {
			t.Errorf("%s constructs a report->dataquery mapping locally; use dashboard/querymap", filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReportDataQueryMappingDetector(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "field", source: `package example
import "github.com/flidai/leapview/internal/analytics/dataquery"
type QueryField struct{}
func fields(values []QueryField) []dataquery.Field { return []dataquery.Field{{}} }
`},
		{name: "filter", source: `package example
import "github.com/flidai/leapview/internal/analytics/dataquery"
type QueryFilter struct{}
func filters(values []QueryFilter) []dataquery.Filter { return []dataquery.Filter{{}} }
`},
		{name: "sort", source: `package example
import "github.com/flidai/leapview/internal/analytics/dataquery"
type QuerySort struct{}
func sorts(values []QuerySort) []dataquery.Sort { return []dataquery.Sort{{}} }
`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !hasLocalReportDataMapping("fixture.go", []byte(test.source)) {
				t.Fatalf("detector missed a local report %s mapper", test.name)
			}
		})
	}
	legitimateDataQuery := []byte(`package example
import "github.com/flidai/leapview/internal/analytics/dataquery"
func fields() []dataquery.Field { return []dataquery.Field{{}} }
`)
	if hasLocalReportDataMapping("fixture.go", legitimateDataQuery) {
		t.Fatal("detector flagged a dataquery constructor without report inputs")
	}
}

func hasLocalReportDataMapping(filename string, source []byte) bool {
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, filename, source, 0)
	if err != nil {
		return false
	}
	localMapping := false
	ast.Inspect(file, func(node ast.Node) bool {
		if localMapping {
			return false
		}
		function, ok := node.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			return true
		}
		start := set.Position(function.Type.Pos()).Offset
		end := set.Position(function.Type.End()).Offset
		bodyStart := set.Position(function.Body.Pos()).Offset
		bodyEnd := set.Position(function.Body.End()).Offset
		if start < 0 || end > len(source) || bodyStart < 0 || bodyEnd > len(source) {
			return true
		}
		signature := string(source[start:end])
		body := string(source[bodyStart:bodyEnd])
		if !queryInputPattern.MatchString(signature) || !dataConstructorPattern.MatchString(body) {
			return true
		}
		for _, kind := range []string{"Field", "Filter", "Sort"} {
			if strings.Contains(signature, "Query"+kind) && strings.Contains(body, "dataquery."+kind) {
				localMapping = true
				return false
			}
		}
		return true
	})
	return localMapping
}

func isGenerated(source []byte) bool {
	header := string(source)
	if len(header) > 512 {
		header = header[:512]
	}
	return strings.Contains(header, "Code generated") && strings.Contains(header, "DO NOT EDIT")
}
