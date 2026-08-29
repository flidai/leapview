package connectionbindingaudit

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
)

func TestNilAdapterFailsClosed(t *testing.T) {
	var adapter *Adapter
	if err := adapter.RecordAuditEvent(t.Context(), nil, access.AuditIntent{}); !errors.Is(err, connectionbinding.ErrAdministrationAuditUnavailable) {
		t.Fatalf("nil adapter error = %v, want audit unavailable", err)
	}
}
