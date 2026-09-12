package authz

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSemanticAuthorizationHasOneProductionOwner prevents either transport
// package from growing a second direct consumer/planner authorization loop.
// Surface files may adapt metrics and status/concealment policy, but decisions
// belong to this package's adapter implementation.
func TestSemanticAuthorizationHasOneProductionOwner(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate queryauthz package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	ownerFiles := []string{
		"internal/dashboard/http/semantic_consumer_http.go",
		"internal/dashboard/semanticapi/semantic_authorization.go",
	}
	for _, relative := range ownerFiles {
		assertDelegatingOwnerFile(t, filepath.Join(root, relative), relative)
	}
	for _, directory := range []string{"internal/dashboard/http", "internal/dashboard/semanticapi"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(source)
			for _, forbidden := range []string{".Authorize(", "PlanRows(semanticquery.RowRequest", "semanticConsumerContextKey", "dashboardSemanticConsumerContextKey"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s contains local semantic authorization operation %q", filepath.ToSlash(path), forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func assertDelegatingOwnerFile(t *testing.T, path, relative string) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	text := string(source)
	if !strings.Contains(text, "internal/dashboard/queryauthz") {
		t.Errorf("%s does not delegate to queryauthz", relative)
	}
	for _, forbidden := range []string{
		".Authorize(",
		"PlanRows(semanticquery.RowRequest",
		"semanticConsumerContextKey",
		"dashboardSemanticConsumerContextKey",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s contains local semantic authorization operation %q", relative, forbidden)
		}
	}
}
