package dashboardappearanceaudit

import (
	"context"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	appearancepostgres "github.com/flidai/leapview/internal/dashboard/appearance/postgres"
)

func TestRecordAuditEventFailsClosedWithoutTransaction(t *testing.T) {
	if err := NewWithRepository(accesspostgres.New()).RecordAuditEvent(context.Background(), nil, appearancepostgres.AuditInput{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
	var adapter *Adapter
	if err := adapter.RecordAuditEvent(context.Background(), nil, appearancepostgres.AuditInput{}); err == nil {
		t.Fatal("nil audit adapter accepted nil transaction")
	}
}
