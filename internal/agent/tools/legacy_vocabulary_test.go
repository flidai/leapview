package tools

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// legacyPrivilegeVocabulary is the retired privilege vocabulary. Keep this
// inventory broad enough to catch names from every former agent, workspace,
// authoring, deployment, publication, and data-query surface.
var legacyPrivilegeVocabulary = regexp.MustCompile(`\b(?:USE_AGENT|VIEW_AGENT|MANAGE_PLATFORM|MANAGE_WORKSPACE|USE_WORKSPACE|VIEW_ITEM|QUERY_DATA|EDIT_ITEM|MANAGE_ITEM|AUTHOR_PROJECT|PUBLISH_RELEASE|REQUEST_DEPLOYMENT|APPROVE_DEPLOYMENT|ACTIVATE_DEPLOYMENT|MANAGE_PUBLICATIONS|VIEW_AUDIT|MANAGE_GRANTS|INGEST_DATA|PREVIEW_DATA|REFRESH_DATA|VIEW_DATA)\b`)

// TestNoLegacyPrivilegeVocabularyInActiveSurfaces protects the canonical
// resource contract at the source boundary. Generated API/reference artifacts
// are intentionally absent: their generators consume these authored inputs
// and are checked separately by generation drift tests.
func TestNoLegacyPrivilegeVocabularyInActiveSurfaces(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate legacy vocabulary test")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	for _, relativeRoot := range []string{
		"api/typespec",
		"docs/articles",
		"docs/guides",
		"internal",
		"scripts",
		"deploy",
		"web",
	} {
		root := filepath.Join(repoRoot, relativeRoot)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if path != root && excludedLegacyVocabularyPath(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if excludedLegacyVocabularyPath(path) || generatedLegacyVocabularyPath(path) || excludedLegacyVocabularyFile(path) {
				return nil
			}
			file, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("open %s: %w", path, err)
			}
			defer file.Close()
			scanner := bufio.NewScanner(file)
			scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
			lineNumber := 0
			for scanner.Scan() {
				lineNumber++
				for _, token := range legacyPrivilegeVocabulary.FindAllString(scanner.Text(), -1) {
					t.Errorf("legacy privilege %q in active surface %s:%d", token, path, lineNumber)
				}
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("scan %s: %w", path, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func excludedLegacyVocabularyPath(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		switch strings.ToLower(part) {
		case "adr", "fixture", "fixtures", "negative", "testdata":
			return true
		}
	}
	return false
}

func generatedLegacyVocabularyPath(path string) bool {
	slashPath := filepath.ToSlash(path)
	return strings.Contains(slashPath, "/docs/api/") ||
		strings.Contains(slashPath, "/docs/reference/") ||
		strings.Contains(slashPath, "/web/generated/") ||
		strings.Contains(slashPath, "/api/gen/") ||
		strings.HasSuffix(slashPath, ".gen.go")
}

func excludedLegacyVocabularyFile(path string) bool {
	slashPath := filepath.ToSlash(path)
	// These tests intentionally assert rejection of the retired vocabulary or
	// use arbitrary privilege strings as schema-generator fixtures.
	return strings.HasSuffix(slashPath, "/internal/agent/tools/legacy_vocabulary_test.go") ||
		strings.HasSuffix(slashPath, "/internal/app/route_inventory_test.go") ||
		strings.HasSuffix(slashPath, "/internal/project/schema/schema_test.go") ||
		strings.HasSuffix(slashPath, "/internal/app/tools/schemadocgen/main_test.go") ||
		strings.HasSuffix(slashPath, "/deploy/compose/deployment_test.go")
}
