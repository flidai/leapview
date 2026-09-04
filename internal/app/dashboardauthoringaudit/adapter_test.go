package dashboardauthoringaudit

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
)

func TestRecordAuditIntentFailsClosedWithoutTransaction(t *testing.T) {
	if err := NewWithRepository(accesspostgres.New()).RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("audit adapter accepted nil transaction")
	}
	var adapter *Adapter
	if err := adapter.RecordAuditIntent(context.Background(), nil, access.AuditIntent{}); err == nil {
		t.Fatal("nil audit adapter accepted nil transaction")
	}
}
