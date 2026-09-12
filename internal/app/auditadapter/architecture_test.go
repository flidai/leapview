package auditadapter

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestDomainAdaptersUseTheSharedAuthority prevents app composition adapters
// from growing a second repository-binding implementation. Intent builders,
// projections, and domain error normalization intentionally remain outside
// this mechanical owner.
func TestDomainAdaptersUseTheSharedAuthority(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate auditadapter package")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	files := []string{
		"internal/app/agentaudit/adapter.go",
		"internal/app/connectionbindingaudit/adapter.go",
		"internal/app/dashboardappearanceaudit/adapter.go",
		"internal/app/dashboardauthoringaudit/adapter.go",
		"internal/app/dashboardpublicationaudit/adapter.go",
		"internal/app/deploymentaudit/adapter.go",
		"internal/app/manageddataaudit/adapter.go",
		"internal/app/productaudit/adapter.go",
		"internal/app/releaseaudit/adapter.go",
		"internal/app/refreshpostgres/audit.go",
	}
	for _, relative := range files {
		body, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		text := string(body)
		if !strings.Contains(text, "internal/app/auditadapter") {
			t.Errorf("%s does not use the shared audit authority", relative)
		}
		for _, marker := range []string{
			"\n\taudit *accesspostgres.AuditRepository",
			"a.audit.RecordAuditEvent(",
			"a.audit.GetAuditEvent(",
			"w.Audit.RecordAuditEvent(",
			"a != nil && a.audit != nil",
		} {
			if strings.Contains(text, marker) {
				t.Errorf("%s still contains duplicated audit adapter mechanics %q", relative, marker)
			}
		}
	}
}
