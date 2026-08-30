package dashboardappearanceaudit

import (
	"context"
	"testing"

	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
)

func TestRecordAuditEventFailsClosedWithoutTransaction(t *testing.T) {
	if err := New().RecordAuditEvent(context.Background(), nil, appearancepostgres.AuditInput{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
	var adapter *Adapter
	if err := adapter.RecordAuditEvent(context.Background(), nil, appearancepostgres.AuditInput{}); err == nil {
		t.Fatal("nil audit adapter accepted nil transaction")
	}
}
