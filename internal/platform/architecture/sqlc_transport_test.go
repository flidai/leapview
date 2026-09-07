package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLCInvocationsPinDownloadTransport(t *testing.T) {
	root := repoRoot(t)
	const invocation = "go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1"
	const expectedPrefix = "GODEBUG=http2client=0 GOTOOLCHAIN=go1.26.7 " + invocation

	paths := map[string]int{
		"Taskfile.yml": 5,
		filepath.Join("scripts", "generate_build_sources.sh"): 1,
	}
	for relativePath, wantCount := range paths {
		relativePath, wantCount := relativePath, wantCount
		t.Run(relativePath, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(root, relativePath))
			if err != nil {
				t.Fatalf("read %s: %v", relativePath, err)
			}

			gotCount := 0
			for _, line := range strings.Split(string(body), "\n") {
				if !strings.Contains(line, "go run github.com/sqlc-dev/sqlc/cmd/sqlc@") {
					continue
				}
				gotCount++
				if !strings.Contains(line, expectedPrefix) {
					t.Errorf("sqlc invocation missing pinned transport/toolchain: %q", strings.TrimSpace(line))
				}
				if !strings.Contains(line, "--no-remote") {
					t.Errorf("sqlc invocation must retain --no-remote: %q", strings.TrimSpace(line))
				}
			}
			if gotCount != wantCount {
				t.Fatalf("sqlc invocation count = %d, want %d", gotCount, wantCount)
			}
		})
	}
}
