package connectionbindingaudit

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
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
}

func TestNilAdapterFailsClosed(t *testing.T) {
	var adapter *Adapter
	if err := adapter.RecordAuditEvent(t.Context(), nil, access.AuditIntent{}); !errors.Is(err, connectionbinding.ErrAdministrationAuditUnavailable) {
		t.Fatalf("nil adapter error = %v, want audit unavailable", err)
	}
}
