package manageddataaudit

import (
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestNewWithRepositoryPreservesAccessAuditIdentity(t *testing.T) {
	audit := accesspostgres.New()
	adapter := NewWithRepository(audit)
	if !adapter.Matches(audit) {
		t.Fatal("adapter did not retain the supplied Access audit repository")
	}
	if adapter.Matches(accesspostgres.New()) {
		t.Fatal("adapter accepted a distinct Access audit repository")
	}
	var nilAdapter *Adapter
	if nilAdapter.Matches(audit) {
		t.Fatal("nil adapter matched an Access audit repository")
	}
}
