package http

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHeaderPreferenceHasOneProductionOwner protects the compatibility
// contract from regrowing local copies in the five command/audit surfaces
// that historically implemented it independently.
func TestHeaderPreferenceHasOneProductionOwner(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate platform/http package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	consumers := []string{
		"internal/agent/http/command_audit.go",
		"internal/manageddata/http/command_audit.go",
		"internal/refresh/http/handler.go",
		"internal/dashboard/module/publication_command_audit.go",
		"internal/admin/module/publications.go",
	}
	for _, relative := range consumers {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		if !strings.Contains(string(body), "internal/platform/http") {
			t.Errorf("%s does not use the platform header owner", relative)
		}
	}
	forbidden := []string{
		"func firstNonEmptyHeader(",
		"func firstCommandHeader(",
		"func firstHeader(",
		"func firstPublicationHeader(",
		"func firstAdminPublicationHeader(",
	}
	for _, relative := range consumers {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, marker := range forbidden {
			if strings.Contains(string(body), marker) {
				t.Errorf("%s still owns local header preference %q", relative, marker)
			}
		}
	}
	forbiddenProduction := []string{
		"func firstNonEmptyHeader(",
		"func firstCommandHeader(",
		"func firstHeader(",
		"func firstPublicationHeader(",
		"func firstAdminPublicationHeader(",
	}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range forbiddenProduction {
			if strings.Contains(string(body), marker) {
				t.Errorf("%s contains a duplicate header preference owner %q", filepath.ToSlash(path), marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuditRequestIdentityHasOneDeploymentReleaseOwner(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate platform/http package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	for _, relative := range []string{
		"internal/deployment/module/candidate_sync.go",
		"internal/release/module/api.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(body)
		if !strings.Contains(text, "platformhttp.AuditRequestIdentity") {
			t.Errorf("%s does not use the shared audit request identity", relative)
		}
		if strings.Contains(text, `Header.Get("X-Request-ID")`) || strings.Contains(text, `Header.Get("X-Correlation-ID")`) {
			t.Errorf("%s still extracts audit request identity locally", relative)
		}
	}
}
