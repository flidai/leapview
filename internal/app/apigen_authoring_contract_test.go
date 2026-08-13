package app

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	legacyAuditPattern          = regexp.MustCompile(`(?m)^\s*audit:\s*#\{`)
	legacyTargetPattern         = regexp.MustCompile(`(?m)^\s*targetParameter\s*:`)
	legacyAsyncReferencePattern = regexp.MustCompile(`(?m)^\s*(statusOperation|eventsOperation)\s*:`)
	redundantOperationIDPattern = regexp.MustCompile(`(?m)@operationId\("([A-Za-z_][A-Za-z0-9_]*)"\)\s*(?:\r?\n\s*@[^(\r\n]+(?:\([^\r\n]*\))?\s*)*\r?\n\s*(?:op\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
)

func TestAPIGenTypeSpecUsesErgonomicCommandAuthoring(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "api", "typespec")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".tsp" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(contents)
		relative := filepath.ToSlash(path)
		for _, check := range []struct {
			pattern *regexp.Regexp
			message string
		}{
			{legacyAuditPattern, "legacy nested command audit; use auditAction/guarantee and interface defaults"},
			{legacyTargetPattern, "string targetParameter; annotate the path parameter with @apigen.target"},
			{legacyAsyncReferencePattern, "string async operation reference; use @apigen.asyncExecution"},
		} {
			if location := check.pattern.FindStringIndex(source); location != nil {
				t.Errorf("%s:%d contains %s", relative, sourceLine(source, location[0]), check.message)
			}
		}
		for _, match := range redundantOperationIDPattern.FindAllStringSubmatchIndex(source, -1) {
			operationID := source[match[2]:match[3]]
			operationName := source[match[4]:match[5]]
			if operationID == operationName {
				t.Errorf(
					"%s:%d repeats operation ID %q; APIGen infers it from the operation declaration",
					relative,
					sourceLine(source, match[0]),
					operationID,
				)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sourceLine(source string, offset int) int {
	return strings.Count(source[:offset], "\n") + 1
}
