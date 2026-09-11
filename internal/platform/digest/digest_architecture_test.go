package digest

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCanonicalSHA256ValidationHasOneOwner keeps the exact identity grammar
// in platform/digest. Domain packages may wrap its error to preserve their
// own messages, but must not reimplement the hex/prefix predicate.
func TestCanonicalSHA256ValidationHasOneOwner(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate platform/digest package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	forbiddenByFile := map[string][]string{
		"internal/access/audit_outbox.go":              {"func canonicalAuditIntentDigest(", "len(\"sha256:\")+64"},
		"internal/platform/objectstore/objectstore.go": {"func isSHA256Identity(", "len(\"sha256:\")+64"},
		"internal/recoveryset/capture/service.go":      {"func canonicalDigest(", "len(\"sha256:\")+64"},
	}
	for relative, markers := range forbiddenByFile {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		for _, marker := range markers {
			if strings.Contains(string(body), marker) {
				t.Errorf("%s still contains local digest validation %q", relative, marker)
			}
		}
	}
	for _, relative := range []string{
		"internal/access/audit_outbox.go",
		"internal/platform/objectstore/objectstore.go",
		"internal/platform/objectstore/filesystem.go",
		"internal/platform/objectstore/s3.go",
		"internal/recoveryset/capture/service.go",
	} {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		if !strings.Contains(string(body), "platformdigest.ValidateSHA256Identity") {
			t.Errorf("%s does not use the platform digest owner", relative)
		}
	}
	for _, directory := range []string{
		"internal/access",
		"internal/platform/objectstore",
		"internal/recoveryset/capture",
	} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, walkErr error) error {
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
			for _, marker := range []string{
				"func canonicalAuditIntentDigest(",
				"func isSHA256Identity(",
				"func canonicalDigest(",
			} {
				if strings.Contains(string(body), marker) {
					t.Errorf("%s contains duplicate canonical digest validation %q", filepath.ToSlash(path), marker)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
